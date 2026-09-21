package commands

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"flag"
	"fmt"
	"math/big"
	"os"
	"time"

	"github.com/ogtechnologies/mozartpay/internal/config"
	"github.com/ogtechnologies/mozartpay/internal/did"
	"github.com/ogtechnologies/mozartpay/internal/models"
	"github.com/ogtechnologies/mozartpay/internal/soroban"
	"github.com/ogtechnologies/mozartpay/internal/ui"
	"github.com/ogtechnologies/mozartpay/internal/wallet"
	"github.com/stellar/go/keypair"
	"github.com/stellar/go/xdr"
)

func newContractCmd(cfg *config.Config) *Command {
	cmd := &Command{
		Name:  "contract",
		Short: "Interact with Soroban smart contracts on Stellar",
		Long:  "Deploy and manage smart contracts on Stellar using pure Go Soroban RPC.",
		cfg:   cfg,
	}
	cmd.addSub(newContractDeployCmd(cfg))
	cmd.addSub(newContractCreateAgreementCmd(cfg))
	cmd.addSub(newContractShowCmd(cfg))
	cmd.addSub(newContractListCmd(cfg))
	cmd.addSub(newContractAttestIdentityCmd(cfg))
	cmd.addSub(newContractConnectWalletCmd(cfg))
	cmd.addSub(newContractFundAssetCmd(cfg))
	cmd.addSub(newContractExecuteCmd(cfg))
	cmd.addSub(newContractSettleCmd(cfg))
	cmd.addSub(newContractSetCmd(cfg))
	cmd.addSub(newContractSetRegistryCmd(cfg))
	cmd.addSub(newContractAnchorReportCmd(cfg))
	cmd.Run = func(c *Command, args []string) error {
		c.printHelp()
		return nil
	}
	return cmd
}

// ─── contract deploy ───────────────────────────

func newContractDeployCmd(cfg *config.Config) *Command {
	fs := flag.NewFlagSet("deploy", flag.ContinueOnError)
	wasmPath := fs.String("wasm", "", "Path to the .wasm file to deploy (required)")
	network := fs.String("network", "", "Stellar network: stellar-testnet or stellar-mainnet (defaults to config)")
	constructorArgsStr := fs.String("constructor-args", "", "Constructor args as comma-separated XDR ScVal base64 strings (optional)")
	ownerAddr := fs.String("owner", "", "Owner address for contracts with __constructor(owner: Address). Defaults to deployer address")

	return &Command{
		Name:  "deploy",
		Short: "Deploy any .wasm contract to Soroban (pure Go, no stellar CLI)",
		Long:  "Uploads WASM bytecode and creates a contract instance on the Soroban network.",
		Flags: fs,
		Run: func(c *Command, args []string) error {
			if *wasmPath == "" {
				ui.Error("WASM file path required (--wasm)")
				return fmt.Errorf("missing --wasm flag")
			}

			wasmBytes, err := os.ReadFile(*wasmPath)
			if err != nil {
				ui.Error(fmt.Sprintf("Failed to read WASM file: %v", err))
				return fmt.Errorf("failed to read wasm: %w", err)
			}

			net := *network
			if net == "" {
				net = cfg.Network
			}
			if net == "" {
				net = "stellar-testnet"
			}

			svc := wallet.NewService()
			account, err := svc.GetActiveAccount()
			if err != nil {
				return fmt.Errorf("no active wallet: %w", err)
			}

			kp, err := keypair.ParseFull(account.PrivateKey)
			if err != nil {
				return fmt.Errorf("invalid private key in wallet: %w", err)
			}

			ui.Header("Deploy Soroban Contract")
			ui.Info(fmt.Sprintf("WASM file: %s (%d bytes)", *wasmPath, len(wasmBytes)))
			ui.Info(fmt.Sprintf("Network: %s", net))
			ui.Info(fmt.Sprintf("Deployer: %s", account.Address))

			client := soroban.NewClientForNetwork(net)
			defer client.Close()

			ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
			defer cancel()

			ui.SectionLabel("Uploading WASM bytecode...")

			var constructorArgs []xdr.ScVal
			if *constructorArgsStr != "" {
				for _, argB64 := range splitComma(*constructorArgsStr) {
					var scv xdr.ScVal
					if err := xdr.SafeUnmarshalBase64(argB64, &scv); err != nil {
						return fmt.Errorf("invalid constructor arg %q: %w", argB64, err)
					}
					constructorArgs = append(constructorArgs, scv)
				}
			} else if *ownerAddr != "" {
				ownerScAddr, err := soroban.AccountToScAddress(*ownerAddr)
				if err != nil {
					return fmt.Errorf("invalid owner address: %w", err)
				}
				constructorArgs = []xdr.ScVal{soroban.ScvAddress(ownerScAddr)}
			} else {
				deployerScAddr, err := soroban.AccountToScAddress(account.Address)
				if err != nil {
					return fmt.Errorf("failed to convert deployer address: %w", err)
				}
				constructorArgs = []xdr.ScVal{soroban.ScvAddress(deployerScAddr)}
			}

			result, err := client.DeployWithConstructorArgs(ctx, kp, wasmBytes, constructorArgs)
			if err != nil {
				ui.Error(fmt.Sprintf("Deployment failed: %v", err))
				return fmt.Errorf("deploy failed: %w", err)
			}

			ui.Success("Contract deployed successfully!")
			ui.Info(fmt.Sprintf("Contract ID: %s", result.ContractID))
			ui.Info(fmt.Sprintf("WASM Hash:   %s", result.WasmHash))
			ui.Info(fmt.Sprintf("Upload TX:   %s", result.UploadTxHash))
			ui.Info(fmt.Sprintf("Create TX:   %s", result.CreateTxHash))

			explorerURL := "https://stellar.expert/explorer/testnet/contract/" + result.ContractID
			if net == "stellar-mainnet" {
				explorerURL = "https://stellar.expert/explorer/public/contract/" + result.ContractID
			}
			ui.Info(fmt.Sprintf("Explorer: %s", explorerURL))

			cfg.ContractID = result.ContractID
			if err := config.Save(cfg); err != nil {
				ui.Warn("Failed to save contract ID to config")
			} else {
				ui.SectionLabel("Contract ID saved to config")
			}

			return nil
		},
	}
}

// ─── contract set ───────────────────────────

func newContractSetCmd(cfg *config.Config) *Command {
	fs := flag.NewFlagSet("set", flag.ContinueOnError)
	contractID := fs.String("id", "", "Contract ID to store")

	return &Command{
		Name:  "set",
		Short: "Store the deployed contract ID",
		Flags: fs,
		Run: func(c *Command, args []string) error {
			if *contractID == "" {
				ui.Error("Contract ID required (--id)")
				return fmt.Errorf("missing contract ID")
			}

			cfg.ContractID = *contractID
			if err := config.Save(cfg); err != nil {
				return fmt.Errorf("failed to save config: %w", err)
			}

			ui.Success(fmt.Sprintf("Contract ID stored: %s", *contractID))
			return nil
		},
	}
}

// ─── contract create-agreement ───────────────────────────

func newContractCreateAgreementCmd(cfg *config.Config) *Command {
	fs := flag.NewFlagSet("create-agreement", flag.ContinueOnError)
	counterparty := fs.String("counterparty", "", "Optional counterparty address")
	expiresAt := fs.Uint64("expires-at", 0, "Optional expiration timestamp (Unix)")
	disputeWindow := fs.Uint("dispute-window", 86400, "Dispute window in seconds")

	return &Command{
		Name:  "create-agreement",
		Short: "Create a new orchestrated agreement",
		Flags: fs,
		Run: func(c *Command, args []string) error {
			if cfg.ContractID == "" {
				ui.Error("No contract ID configured. Run: mozartpay contract set --id <CONTRACT_ID>")
				return fmt.Errorf("contract ID not set")
			}

			kp, net, account, err := getKeypairAndNetwork(cfg)
			if err != nil {
				return err
			}

			ui.Header("Create Orchestrated Agreement")
			ui.Info(fmt.Sprintf("Initiator: %s", account.Address))
			ui.Info(fmt.Sprintf("Contract: %s", cfg.ContractID))

			initiatorAddr, err := soroban.AccountToScAddress(account.Address)
			if err != nil {
				return fmt.Errorf("failed to convert address: %w", err)
			}

			// create_agreement(initiator, counterparty: Option<Address>,
			//                  expires_at: Option<u64>, dispute_window: u32)
			var counterpartyArg, expiresArg xdr.ScVal
			if *counterparty != "" {
				cpAddr, err := soroban.AccountToScAddress(*counterparty)
				if err != nil {
					return fmt.Errorf("invalid counterparty address: %w", err)
				}
				v := soroban.ScvAddress(cpAddr)
				counterpartyArg = soroban.ScvOption(&v)
			} else {
				counterpartyArg = soroban.ScvOption(nil)
			}
			if *expiresAt > 0 {
				v := soroban.ScvU64(*expiresAt)
				expiresArg = soroban.ScvOption(&v)
			} else {
				expiresArg = soroban.ScvOption(nil)
			}

			invArgs := []xdr.ScVal{
				soroban.ScvAddress(initiatorAddr),
				counterpartyArg,
				expiresArg,
				soroban.ScvU32(uint32(*disputeWindow)),
			}

			ui.SectionLabel("Submitting transaction...")

			client := soroban.NewClientForNetwork(net)
			defer client.Close()

			ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
			defer cancel()

			result, err := client.Invoke(ctx, kp, cfg.ContractID, "create_agreement", invArgs)
			if err != nil {
				ui.Error(fmt.Sprintf("Transaction failed: %v", err))
				return fmt.Errorf("invoke failed: %w", err)
			}

			ui.Success(fmt.Sprintf("Agreement created. TX: %s", result.TxHash))

			// Decode the returned agreement ID (BytesN<32>) and persist it.
			// The contract return value lives in ResultMetaXDR, not ResultXDR.
			if retVal, rerr := soroban.ReturnValueFromMetaXDR(result.ResultMetaXDR); rerr == nil {
				if b, berr := soroban.DecodeScBytesN32(retVal); berr == nil {
					idHex := hex.EncodeToString(b[:])
					ui.Info(fmt.Sprintf("Agreement ID: %s", idHex))
					cfg.LastAgreementID = idHex
					cfg.AgreementIDs = append(cfg.AgreementIDs, idHex)
					if err := config.Save(cfg); err != nil {
						ui.Warn("Failed to save agreement ID to config")
					}
				} else {
					ui.Warn(fmt.Sprintf("Could not decode agreement ID: %v", berr))
				}
			} else {
				ui.Warn(fmt.Sprintf("Could not decode agreement ID: %v", rerr))
			}
			ui.Info(fmt.Sprintf("Explorer: https://stellar.expert/explorer/testnet/contract/%s", cfg.ContractID))

			return nil
		},
	}
}

// ─── contract show ───────────────────────────

func newContractShowCmd(cfg *config.Config) *Command {
	fs := flag.NewFlagSet("show", flag.ContinueOnError)
	agreementID := fs.String("id", "", "Agreement ID (uses last created if omitted)")

	return &Command{
		Name:  "show",
		Short: "Display agreement details",
		Flags: fs,
		Run: func(c *Command, args []string) error {
			if cfg.ContractID == "" {
				ui.Error("No contract ID configured")
				return fmt.Errorf("contract ID not set")
			}

			id := *agreementID
			if id == "" {
				id = cfg.LastAgreementID
			}
			if id == "" {
				ui.Error("No agreement ID provided and no recent agreement found")
				return fmt.Errorf("agreement ID required")
			}

			_, net, _, err := getKeypairAndNetwork(cfg)
			if err != nil {
				return err
			}

			ui.Header("Agreement Details")

			client := soroban.NewClientForNetwork(net)
			defer client.Close()

			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer cancel()

			idBytes, err := soroban.ParseBytesN32Hex(id)
			if err != nil {
				ui.Error(fmt.Sprintf("Invalid agreement ID (expected 64-char hex): %v", err))
				return err
			}

			result, err := client.SimulateOnly(ctx, cfg.ContractID, "get_agreement", []xdr.ScVal{
				soroban.ScvBytesN32(idBytes),
			})
			if err != nil {
				ui.Error(fmt.Sprintf("Query failed: %v", err))
				return fmt.Errorf("simulate failed: %w", err)
			}

			fmt.Println(result)
			return nil
		},
	}
}

// ─── contract list ───────────────────────────

func newContractListCmd(cfg *config.Config) *Command {
	return &Command{
		Name:  "list",
		Short: "List agreements by initiator",
		Run: func(c *Command, args []string) error {
			if cfg.ContractID == "" {
				ui.Error("No contract ID configured")
				return fmt.Errorf("contract ID not set")
			}

			_, net, account, err := getKeypairAndNetwork(cfg)
			if err != nil {
				return err
			}

			ui.Header("My Agreements")

			initiatorAddr, err := soroban.AccountToScAddress(account.Address)
			if err != nil {
				return fmt.Errorf("failed to convert address: %w", err)
			}

			client := soroban.NewClientForNetwork(net)
			defer client.Close()

			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer cancel()

			result, err := client.SimulateOnly(ctx, cfg.ContractID, "get_initiator_agreements", []xdr.ScVal{
				soroban.ScvAddress(initiatorAddr),
			})
			if err != nil {
				ui.Error(fmt.Sprintf("Query failed: %v", err))
				return fmt.Errorf("simulate failed: %w", err)
			}

			if len(cfg.AgreementIDs) > 0 {
				ui.SectionLabel("Stored Agreement IDs:")
				for i, id := range cfg.AgreementIDs {
					marker := ""
					if id == cfg.LastAgreementID {
						marker = " (latest)"
					}
					fmt.Printf("  %d. %s%s\n", i+1, id, marker)
				}
			}

			ui.SectionLabel("On-chain Result:")
			fmt.Println(result)
			return nil
		},
	}
}

// ─── contract attest-identity ───────────────────────────

func newContractAttestIdentityCmd(cfg *config.Config) *Command {
	fs := flag.NewFlagSet("attest-identity", flag.ContinueOnError)
	agreementID := fs.String("id", "", "Agreement ID (hex)")
	didFlag := fs.String("did", "", "Decentralized Identifier (default: active DID)")
	method := fs.String("method", "", "DID method: web | key | ethr | ebsi | soroban (default: inferred from DID)")
	vcType := fs.String("vc-type", "national_id", "Type of verifiable credential")
	vcHashFlag := fs.String("vc-hash", "", "Hex VC hash to use as attestation hash (default: hash of saved vc_latest, else generated)")
	verifier := fs.String("verifier", "", "Optional on-chain verifier contract ID (C...)")

	return &Command{
		Name:  "attest-identity",
		Short: "Attest DID and VC for an agreement (Layer 1)",
		Flags: fs,
		Run: func(c *Command, args []string) error {
			if cfg.ContractID == "" {
				ui.Error("No contract ID configured")
				return fmt.Errorf("contract ID not set")
			}

			id := *agreementID
			if id == "" {
				id = cfg.LastAgreementID
			}
			if id == "" {
				ui.Error("No agreement ID provided")
				return fmt.Errorf("agreement ID required")
			}

			idBytes, err := soroban.ParseBytesN32Hex(id)
			if err != nil {
				ui.Error(fmt.Sprintf("Invalid agreement ID (expected 64-char hex): %v", err))
				return err
			}

			didStr := *didFlag
			if didStr == "" {
				didStr = cfg.ActiveDID
			}
			if didStr == "" {
				ui.Error("DID required (--did or set an active DID via 'did create')")
				return fmt.Errorf("DID required")
			}

			// Attestation hash: explicit --vc-hash, else hash of saved VC, else generated
			var attestationHash [32]byte
			switch {
			case *vcHashFlag != "":
				attestationHash, err = soroban.ParseBytesN32Hex(*vcHashFlag)
				if err != nil {
					ui.Error(fmt.Sprintf("Invalid --vc-hash: %v", err))
					return err
				}
			default:
				var vc models.VerifiableCredential
				if lerr := config.LoadState("vc_latest", &vc); lerr == nil {
					attestationHash, err = did.VCHash(&vc)
					if err != nil {
						return fmt.Errorf("hash VC: %w", err)
					}
					ui.Info("Using VC hash from saved credential as attestation hash")
				} else {
					attestationHash, err = soroban.ParseBytesN32Hex(generateHash(id + didStr + time.Now().String()))
					if err != nil {
						return fmt.Errorf("generate attestation hash: %w", err)
					}
				}
			}

			// DID method enum: explicit flag or inferred from the DID prefix
			methodVariant := *method
			if methodVariant == "" {
				methodVariant = didMethodFromDID(didStr)
			}
			methodScv, err := didMethodEnumScVal(methodVariant)
			if err != nil {
				ui.Error(err.Error())
				return err
			}

			var verifierArg xdr.ScVal
			if *verifier != "" {
				vAddr, verr := soroban.ParseContractID(*verifier)
				if verr != nil {
					return fmt.Errorf("invalid verifier contract ID: %w", verr)
				}
				v := soroban.ScvAddress(vAddr)
				verifierArg = soroban.ScvOption(&v)
			} else {
				verifierArg = soroban.ScvOption(nil)
			}

			kp, net, _, err := getKeypairAndNetwork(cfg)
			if err != nil {
				return err
			}

			ui.Header("Attest Identity")
			ui.KV("Agreement", id)
			ui.KV("DID", didStr)
			ui.KV("Method", methodVariant)
			ui.KV("Attestation Hash", hex.EncodeToString(attestationHash[:]))

			invArgs := []xdr.ScVal{
				soroban.ScvBytesN32(idBytes),
				soroban.ScvString(didStr),
				methodScv,
				soroban.ScvSymbol(*vcType),
				soroban.ScvBytesN32(attestationHash),
				verifierArg,
			}

			ui.SectionLabel("Submitting transaction...")

			client := soroban.NewClientForNetwork(net)
			defer client.Close()

			ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
			defer cancel()

			result, err := client.Invoke(ctx, kp, cfg.ContractID, "attest_identity", invArgs)
			if err != nil {
				ui.Error(fmt.Sprintf("Transaction failed: %v", err))
				return fmt.Errorf("invoke failed: %w", err)
			}

			ui.Success("Identity attested successfully")
			ui.Info(fmt.Sprintf("TX: %s", result.TxHash))
			return nil
		},
	}
}

// ─── contract connect-wallet ───────────────────────────

func newContractConnectWalletCmd(cfg *config.Config) *Command {
	fs := flag.NewFlagSet("connect-wallet", flag.ContinueOnError)
	agreementID := fs.String("id", "", "Agreement ID")
	walletType := fs.String("type", "wwwallet", "Wallet type: wwwallet | external | testnet")
	passkey := fs.Bool("passkey", true, "Enable passkey authentication")

	return &Command{
		Name:  "connect-wallet",
		Short: "Connect wallet to an agreement (Layer 2)",
		Flags: fs,
		Run: func(c *Command, args []string) error {
			if cfg.ContractID == "" {
				ui.Error("No contract ID configured")
				return fmt.Errorf("contract ID not set")
			}

			id := *agreementID
			if id == "" {
				id = cfg.LastAgreementID
			}
			if id == "" {
				ui.Error("No agreement ID provided")
				return fmt.Errorf("agreement ID required")
			}

			kp, net, account, err := getKeypairAndNetwork(cfg)
			if err != nil {
				return err
			}

			ui.Header("Connect Wallet")

			idBytes, err := soroban.ParseBytesN32Hex(id)
			if err != nil {
				ui.Error(fmt.Sprintf("Invalid agreement ID (expected 64-char hex): %v", err))
				return err
			}

			stellarAddr, err := soroban.AccountToScAddress(account.Address)
			if err != nil {
				return fmt.Errorf("failed to convert address: %w", err)
			}

			invArgs := []xdr.ScVal{
				soroban.ScvBytesN32(idBytes),
				soroban.ScvAddress(stellarAddr),
				soroban.ScvSymbol(*walletType),
				soroban.ScvBool(*passkey),
				soroban.ScvU32(1),
			}

			ui.SectionLabel("Submitting transaction...")

			client := soroban.NewClientForNetwork(net)
			defer client.Close()

			ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
			defer cancel()

			result, err := client.Invoke(ctx, kp, cfg.ContractID, "connect_wallet", invArgs)
			if err != nil {
				ui.Error(fmt.Sprintf("Transaction failed: %v", err))
				return fmt.Errorf("invoke failed: %w", err)
			}

			ui.Success("Wallet connected successfully")
			ui.Info(fmt.Sprintf("TX: %s", result.TxHash))
			return nil
		},
	}
}

// ─── contract fund-asset ───────────────────────────

func newContractFundAssetCmd(cfg *config.Config) *Command {
	fs := flag.NewFlagSet("fund-asset", flag.ContinueOnError)
	agreementID := fs.String("id", "", "Agreement ID")
	assetCode := fs.String("asset", "USDC", "Asset code")
	amount := fs.String("amount", "100", "Total amount")
	locked := fs.String("locked", "0", "Amount to lock as collateral")

	return &Command{
		Name:  "fund-asset",
		Short: "Fund and set asset for an agreement (Layer 3)",
		Flags: fs,
		Run: func(c *Command, args []string) error {
			if cfg.ContractID == "" {
				ui.Error("No contract ID configured")
				return fmt.Errorf("contract ID not set")
			}

			id := *agreementID
			if id == "" {
				id = cfg.LastAgreementID
			}
			if id == "" {
				ui.Error("No agreement ID provided")
				return fmt.Errorf("agreement ID required")
			}

			kp, net, _, err := getKeypairAndNetwork(cfg)
			if err != nil {
				return err
			}

			idBytes, err := soroban.ParseBytesN32Hex(id)
			if err != nil {
				ui.Error(fmt.Sprintf("Invalid agreement ID (expected 64-char hex): %v", err))
				return err
			}

			amountI, ok := new(big.Int).SetString(*amount, 10)
			if !ok {
				ui.Error(fmt.Sprintf("Invalid amount: %s", *amount))
				return fmt.Errorf("invalid amount")
			}
			lockedI, ok := new(big.Int).SetString(*locked, 10)
			if !ok {
				ui.Error(fmt.Sprintf("Invalid locked amount: %s", *locked))
				return fmt.Errorf("invalid locked amount")
			}

			ui.Header("Fund Asset")

			invArgs := []xdr.ScVal{
				soroban.ScvBytesN32(idBytes),
				soroban.ScvSymbol(*assetCode),
				soroban.ScvI128(amountI),
				soroban.ScvI128(lockedI),
				soroban.ScvSymbol("fungible"),
				soroban.ScvOption(nil), // contract_id: Option<BytesN<32>>
			}

			ui.SectionLabel("Submitting transaction...")

			client := soroban.NewClientForNetwork(net)
			defer client.Close()

			ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
			defer cancel()

			result, err := client.Invoke(ctx, kp, cfg.ContractID, "fund_and_set_asset", invArgs)
			if err != nil {
				ui.Error(fmt.Sprintf("Transaction failed: %v", err))
				return fmt.Errorf("invoke failed: %w", err)
			}

			ui.Success("Asset funded successfully")
			ui.Info(fmt.Sprintf("TX: %s", result.TxHash))
			return nil
		},
	}
}

// ─── contract execute ───────────────────────────

func newContractExecuteCmd(cfg *config.Config) *Command {
	fs := flag.NewFlagSet("execute", flag.ContinueOnError)
	agreementID := fs.String("id", "", "Agreement ID")
	txHash := fs.String("tx-hash", "", "Transaction hash for settlement")

	return &Command{
		Name:  "execute",
		Short: "Execute an agreement (Layer 5)",
		Flags: fs,
		Run: func(c *Command, args []string) error {
			if cfg.ContractID == "" {
				ui.Error("No contract ID configured")
				return fmt.Errorf("contract ID not set")
			}

			id := *agreementID
			if id == "" {
				id = cfg.LastAgreementID
			}
			if id == "" {
				ui.Error("No agreement ID provided")
				return fmt.Errorf("agreement ID required")
			}

			idBytes, err := soroban.ParseBytesN32Hex(id)
			if err != nil {
				ui.Error(fmt.Sprintf("Invalid agreement ID (expected 64-char hex): %v", err))
				return err
			}

			finalTxHash := *txHash
			if finalTxHash == "" {
				finalTxHash = generateHash(id + time.Now().String())
			}
			txHashBytes, err := soroban.ParseBytesN32Hex(finalTxHash)
			if err != nil {
				ui.Error(fmt.Sprintf("Invalid tx hash (expected 64-char hex): %v", err))
				return err
			}

			kp, net, _, err := getKeypairAndNetwork(cfg)
			if err != nil {
				return err
			}

			ui.Header("Execute Agreement")

			invArgs := []xdr.ScVal{
				soroban.ScvBytesN32(idBytes),
				soroban.ScvBytesN32(txHashBytes),
			}

			ui.SectionLabel("Submitting transaction...")

			client := soroban.NewClientForNetwork(net)
			defer client.Close()

			ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
			defer cancel()

			result, err := client.Invoke(ctx, kp, cfg.ContractID, "execute_agreement", invArgs)
			if err != nil {
				ui.Error(fmt.Sprintf("Transaction failed: %v", err))
				return fmt.Errorf("invoke failed: %w", err)
			}

			ui.Success("Agreement executed successfully")
			ui.Info(fmt.Sprintf("TX: %s", result.TxHash))
			return nil
		},
	}
}

// ─── contract settle ───────────────────────────

func newContractSettleCmd(cfg *config.Config) *Command {
	fs := flag.NewFlagSet("settle", flag.ContinueOnError)
	agreementID := fs.String("id", "", "Agreement ID")

	return &Command{
		Name:  "settle",
		Short: "Settle an agreement",
		Flags: fs,
		Run: func(c *Command, args []string) error {
			if cfg.ContractID == "" {
				ui.Error("No contract ID configured")
				return fmt.Errorf("contract ID not set")
			}

			id := *agreementID
			if id == "" {
				id = cfg.LastAgreementID
			}
			if id == "" {
				ui.Error("No agreement ID provided")
				return fmt.Errorf("agreement ID required")
			}

			kp, net, _, err := getKeypairAndNetwork(cfg)
			if err != nil {
				return err
			}

			idBytes, err := soroban.ParseBytesN32Hex(id)
			if err != nil {
				ui.Error(fmt.Sprintf("Invalid agreement ID (expected 64-char hex): %v", err))
				return err
			}

			ui.Header("Settle Agreement")

			invArgs := []xdr.ScVal{
				soroban.ScvBytesN32(idBytes),
			}

			ui.SectionLabel("Submitting transaction...")

			client := soroban.NewClientForNetwork(net)
			defer client.Close()

			ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
			defer cancel()

			result, err := client.Invoke(ctx, kp, cfg.ContractID, "settle_agreement", invArgs)
			if err != nil {
				ui.Error(fmt.Sprintf("Transaction failed: %v", err))
				return fmt.Errorf("invoke failed: %w", err)
			}

			ui.Success("Agreement settled successfully")
			ui.Info(fmt.Sprintf("TX: %s", result.TxHash))
			return nil
		},
	}
}

// ─── contract set-registry ───────────────────────────

func newContractSetRegistryCmd(cfg *config.Config) *Command {
	fs := flag.NewFlagSet("set-registry", flag.ContinueOnError)
	registryID := fs.String("id", "", "DID registry contract ID (C...) — default: did set-registry value")

	return &Command{
		Name:  "set-registry",
		Short: "Configure the DID registry on the OA contract (enables compliance gating)",
		Flags: fs,
		Run: func(c *Command, args []string) error {
			if cfg.ContractID == "" {
				ui.Error("No contract ID configured")
				return fmt.Errorf("contract ID not set")
			}

			regID := *registryID
			if regID == "" {
				regID = cfg.DIDRegistryContractID
			}
			if regID == "" {
				ui.Error("Registry contract ID required (--id or 'did set-registry')")
				return fmt.Errorf("registry contract ID required")
			}

			regAddr, err := soroban.ParseContractID(regID)
			if err != nil {
				return fmt.Errorf("invalid registry contract ID: %w", err)
			}

			kp, net, _, err := getKeypairAndNetwork(cfg)
			if err != nil {
				return err
			}

			ui.Header("Set DID Registry on OA Contract")
			ui.KV("OA Contract", cfg.ContractID)
			ui.KV("Registry", regID)

			client := soroban.NewClientForNetwork(net)
			defer client.Close()

			ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
			defer cancel()

			ui.SectionLabel("Submitting transaction...")
			result, err := client.Invoke(ctx, kp, cfg.ContractID, "set_did_registry", []xdr.ScVal{
				soroban.ScvAddress(regAddr),
			})
			if err != nil {
				ui.Error(fmt.Sprintf("Transaction failed: %v", err))
				return fmt.Errorf("invoke failed: %w", err)
			}

			ui.Success("DID registry configured on OA contract")
			ui.Info(fmt.Sprintf("TX: %s", result.TxHash))
			ui.Info("Compliance gating is now active for attest/execute/settle")
			return nil
		},
	}
}

// ─── contract anchor-report ───────────────────────────

func newContractAnchorReportCmd(cfg *config.Config) *Command {
	fs := flag.NewFlagSet("anchor-report", flag.ContinueOnError)
	agreementID := fs.String("id", "", "Agreement ID (hex, default: last created)")
	reportHash := fs.String("report-hash", "", "Hex SHA-256 of the compliance report (default: hash of saved report_latest)")
	isoRef := fs.String("iso-ref", "", "Optional ISO 20022 message reference")

	return &Command{
		Name:  "anchor-report",
		Short: "Anchor a compliance report hash on-chain (Reporting layer)",
		Flags: fs,
		Run: func(c *Command, args []string) error {
			if cfg.ContractID == "" {
				ui.Error("No contract ID configured")
				return fmt.Errorf("contract ID not set")
			}

			id := *agreementID
			if id == "" {
				id = cfg.LastAgreementID
			}
			if id == "" {
				ui.Error("No agreement ID provided")
				return fmt.Errorf("agreement ID required")
			}

			var hash [32]byte
			var err error
			if *reportHash != "" {
				hash, err = soroban.ParseBytesN32Hex(*reportHash)
				if err != nil {
					ui.Error(fmt.Sprintf("Invalid --report-hash: %v", err))
					return err
				}
			} else {
				var report models.TransactionReport
				if lerr := config.LoadState("report_latest", &report); lerr != nil {
					ui.Error("No --report-hash and no saved report. Run 'report generate' first.")
					return fmt.Errorf("no report: %w", lerr)
				}
				data, _ := json.Marshal(report)
				hash = sha256.Sum256(data)
				ui.Info("Using SHA-256 of saved report_latest")
			}

			return invokeAnchorReport(cfg, id, hash, *isoRef)
		},
	}
}

// invokeAnchorReport submits anchor_report to the OA contract. Shared by
// 'contract anchor-report' and 'report generate --anchor'.
func invokeAnchorReport(cfg *config.Config, agreementID string, hash [32]byte, isoRef string) error {
	idBytes, err := soroban.ParseBytesN32Hex(agreementID)
	if err != nil {
		ui.Error(fmt.Sprintf("Invalid agreement ID (expected 64-char hex): %v", err))
		return err
	}

	var isoArg xdr.ScVal
	if isoRef != "" {
		v := soroban.ScvString(isoRef)
		isoArg = soroban.ScvOption(&v)
	} else {
		isoArg = soroban.ScvOption(nil)
	}

	kp, net, _, err := getKeypairAndNetwork(cfg)
	if err != nil {
		return err
	}

	ui.Header("Anchor Report On-Chain")
	ui.KV("Agreement", agreementID)
	ui.KV("Report Hash", hex.EncodeToString(hash[:]))

	client := soroban.NewClientForNetwork(net)
	defer client.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()

	ui.SectionLabel("Submitting transaction...")
	result, err := client.Invoke(ctx, kp, cfg.ContractID, "anchor_report", []xdr.ScVal{
		soroban.ScvBytesN32(idBytes),
		soroban.ScvBytesN32(hash),
		isoArg,
	})
	if err != nil {
		ui.Error(fmt.Sprintf("Transaction failed: %v", err))
		return fmt.Errorf("invoke failed: %w", err)
	}

	ui.Success("Report anchored on-chain")
	ui.Info(fmt.Sprintf("TX: %s", result.TxHash))
	return nil
}

// ─── Helpers ───────────────────────────

// didMethodFromDID infers the DID method variant from a DID string prefix.
func didMethodFromDID(didStr string) string {
	for _, m := range []string{"web", "key", "ethr", "ebsi", "soroban"} {
		if len(didStr) > len(m)+4 && didStr[:len(m)+4] == "did:"+m+":" {
			return m
		}
	}
	return "key"
}

// didMethodEnumScVal encodes a DID method name as the contract's DIDMethod
// enum (unit enum → Vec[Symbol(variant)]).
func didMethodEnumScVal(method string) (xdr.ScVal, error) {
	variant := ""
	switch method {
	case "web":
		variant = "Web"
	case "key":
		variant = "Key"
	case "ethr":
		variant = "Ethr"
	case "ebsi":
		variant = "Ebsi"
	case "soroban":
		variant = "Soroban"
	default:
		return xdr.ScVal{}, fmt.Errorf("unknown DID method %q (web|key|ethr|ebsi|soroban)", method)
	}
	return soroban.ScvVec([]xdr.ScVal{soroban.ScvSymbol(variant)}), nil
}

func getKeypairAndNetwork(cfg *config.Config) (*keypair.Full, string, *models.Account, error) {
	svc := wallet.NewService()
	account, err := svc.GetActiveAccount()
	if err != nil {
		return nil, "", nil, fmt.Errorf("no active wallet: %w", err)
	}

	kp, err := keypair.ParseFull(account.PrivateKey)
	if err != nil {
		return nil, "", nil, fmt.Errorf("invalid private key in wallet: %w", err)
	}

	net := cfg.Network
	if net == "" {
		net = "stellar-testnet"
	}

	return kp, net, account, nil
}

func generateHash(input string) string {
	h := sha256.New()
	h.Write([]byte(input))
	return fmt.Sprintf("%x", h.Sum(nil))
}

func splitComma(s string) []string {
	if s == "" {
		return nil
	}
	var result []string
	start := 0
	for i := 0; i < len(s); i++ {
		if s[i] == ',' {
			result = append(result, s[start:i])
			start = i + 1
		}
	}
	result = append(result, s[start:])
	return result
}

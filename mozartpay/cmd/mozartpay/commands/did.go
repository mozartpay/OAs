package commands

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"time"

	"github.com/ogtechnologies/mozartpay/internal/config"
	"github.com/ogtechnologies/mozartpay/internal/did"
	"github.com/ogtechnologies/mozartpay/internal/models"
	"github.com/ogtechnologies/mozartpay/internal/ui"
	"github.com/ogtechnologies/mozartpay/internal/wallet"
	mpCrypto "github.com/ogtechnologies/mozartpay/pkg/crypto"
)

// proof type constants for CLI flags
const (
	proofTypeEd25519 = "ed25519"
	proofTypeJWS     = "jws"
)

func newDIDCmd(cfg *config.Config) *Command {
	cmd := &Command{
		Name:  "did",
		Short: "Manage decentralized identifiers and verifiable credentials",
		Long:  "Create DIDs using web/key/ethr/ebsi methods. Issue and verify W3C Verifiable Credentials.",
		cfg:   cfg,
	}

	// Sub-commands
	cmd.addSub(newDIDCreateCmd(cfg))
	cmd.addSub(newDIDAttestCmd(cfg))
	cmd.addSub(newDIDVerifyCmd(cfg))
	cmd.addSub(newDIDShowCmd(cfg))
	cmd.addSub(newDIDSetRegistryCmd(cfg))
	cmd.addSub(newDIDRegisterCmd(cfg))
	cmd.addSub(newDIDResolveCmd(cfg))
	cmd.addSub(newDIDDeactivateCmd(cfg))
	cmd.addSub(newDIDAnchorCmd(cfg))
	cmd.addSub(newDIDRevokeCmd(cfg))
	cmd.addSub(newDIDStatusCmd(cfg))

	cmd.Run = func(c *Command, args []string) error {
		c.printHelp()
		return nil
	}

	return cmd
}

// ─── did create ───────────────────────────────

func newDIDCreateCmd(cfg *config.Config) *Command {
	fs := flag.NewFlagSet("create", flag.ContinueOnError)
	method := fs.String("method", "key", "DID method: web | key | ethr | ebsi | soroban")
	domain := fs.String("domain", "mozartpay.com", "Domain for did:web (used with --method web)")
	output := fs.String("output", "pretty", "Output format: pretty | json")
	register := fs.Bool("register", false, "Register the DID on-chain in the registry (requires did set-registry)")

	return &Command{
		Name:  "create",
		Short: "Create a new DID document",
		Flags: fs,
		Run: func(c *Command, args []string) error {
			ui.Header("Create DID")

			m := models.DIDMethod(*method)

			var doc *models.DIDDocument
			var err error

			if m == models.DIDMethodSoroban {
				// did:soroban derives from the active Stellar wallet — no keygen
				doc, err = sorobanDIDDocFromWallet()
				if err != nil {
					ui.Error(err.Error())
					return err
				}
			} else {
				spin := ui.NewSpinner(fmt.Sprintf("Generating did:%s document...", m))
				spin.Start()
				time.Sleep(600 * time.Millisecond)

				var svc *did.Service
				svc, err = did.NewService()
				if err != nil {
					spin.Stop(false, "Key generation failed")
					return err
				}

				if m == models.DIDMethodWeb {
					doc, err = svc.CreateDID(m, *domain)
				} else {
					doc, err = svc.CreateDID(m)
				}
				if err != nil {
					spin.Stop(false, err.Error())
					return err
				}
				spin.Stop(true, "DID document created")
			}

			if *output == "json" {
				fmt.Println(did.PrettyPrint(doc))
				return nil
			}

			ui.SectionLabel("DID Document")
			ui.KV("DID", doc.ID)
			ui.KV("Method", string(doc.Method))
			ui.KV("Key Type", doc.VerificationMethod[0].Type)
			ui.KV("Public Key", safeTrunc(doc.VerificationMethod[0].PublicKeyHex, 32)+"...")
			ui.KV("Created", doc.Created.Format(time.RFC3339))

			// Save to state
			if err := config.SaveState("did_"+string(m), doc); err == nil {
				ui.Info("Saved to ~/.mozartpay/state/did_" + string(m) + ".json")
			}

			// Update config with active DID
			cfg.ActiveDID = doc.ID
			cfg.DIDMethod = string(m)
			config.Save(cfg)

			// Optionally register on-chain
			if *register {
				if err := registerDIDOnChain(cfg, doc); err != nil {
					ui.Warn(fmt.Sprintf("On-chain registration failed: %v", err))
				}
			}

			return nil
		},
	}
}

// sorobanDIDDocFromWallet builds a did:soroban DID document from the active
// Stellar wallet. The wallet account is both the controller and the sole
// verification method (the G-address encodes the ed25519 public key).
func sorobanDIDDocFromWallet() (*models.DIDDocument, error) {
	ws := wallet.NewService()
	acc, err := ws.GetActiveWallet()
	if err != nil || acc.Address == "" {
		return nil, fmt.Errorf("no active wallet — run 'mozartpay wallet connect --provider stellar' first")
	}

	didStr := "did:soroban:" + acc.Address
	vmID := didStr + "#keys-1"

	return &models.DIDDocument{
		Context: []string{"https://www.w3.org/ns/did/v1"},
		ID:      didStr,
		Method:  models.DIDMethodSoroban,
		VerificationMethod: []models.VerificationKey{
			{
				ID:           vmID,
				Type:         "Ed25519VerificationKey2020",
				Controller:   didStr,
				PublicKeyHex: acc.Address,
			},
		},
		Authentication: []string{vmID},
		Created:        time.Now().UTC(),
	}, nil
}

// registerDIDOnChain registers a DID document in the on-chain registry.
// did:soroban DIDs use the contract's register_soroban_did derivation;
// other methods use register_did with the full document.
func registerDIDOnChain(cfg *config.Config, doc *models.DIDDocument) error {
	if cfg.DIDRegistryContractID == "" {
		return fmt.Errorf("no DID registry configured — run 'mozartpay did set-registry --id <contract-id>'")
	}

	kp, net, account, err := getKeypairAndNetwork(cfg)
	if err != nil {
		return err
	}

	reg := did.NewRegistryClient(net, cfg.DIDRegistryContractID)
	defer reg.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()

	ui.SectionLabel("Registering DID on-chain...")

	if doc.Method == models.DIDMethodSoroban {
		derived, res, err := reg.RegisterSorobanDID(ctx, kp, account.Address)
		if err != nil {
			return fmt.Errorf("register_soroban_did: %w", err)
		}
		ui.Success(fmt.Sprintf("DID registered on-chain: %s", derived))
		ui.Info(fmt.Sprintf("TX: %s", res.TxHash))
		return nil
	}

	res, err := reg.RegisterDID(ctx, kp, doc, account.Address, nil)
	if err != nil {
		return fmt.Errorf("register_did: %w", err)
	}
	ui.Success("DID registered on-chain")
	ui.Info(fmt.Sprintf("TX: %s", res.TxHash))
	return nil
}

// ─── did attest ───────────────────────────────

func newDIDAttestCmd(cfg *config.Config) *Command {
	fs := flag.NewFlagSet("attest", flag.ContinueOnError)
	method := fs.String("method", "ebsi", "DID method for attestation")
	vcType := fs.String("vc", "national-id", "VC type: national-id | kyc | accreditation")
	name := fs.String("name", "", "Holder full name")
	country := fs.String("country", "AT", "ISO country code")
	birthYear := fs.String("birth-year", "", "Holder birth year (optional, omitted if empty)")
	level := fs.String("level", "KYC_LEVEL_2", "KYC level for the credential")
	output := fs.String("output", "pretty", "Output format: pretty | json")
	useWalletKey := fs.Bool("use-wallet-key", false, "Sign VC with the active wallet's key instead of an ephemeral key")
	proofType := fs.String("proof-type", proofTypeEd25519, "Proof type: ed25519 | jws")
	anchor := fs.Bool("anchor", false, "Anchor the VC hash on-chain in the DID registry (requires did set-registry)")

	return &Command{
		Name:  "attest",
		Short: "Issue a Verifiable Credential (VC) via national ID / KYC",
		Flags: fs,
		Run: func(c *Command, args []string) error {
			ui.Header("DID Attestation")

			if *name == "" {
				*name = ui.Prompt("Holder name:")
			}

			m := models.DIDMethod(*method)

			spin := ui.NewSpinner("Creating DID and issuing VC...")
			spin.Start()
			time.Sleep(800 * time.Millisecond)

			var svc *did.Service
			if *useWalletKey {
				ws := wallet.NewService()
				acc, werr := ws.GetActiveWallet()
				if werr != nil || acc.PrivateKey == "" {
					spin.Stop(false, "No active wallet with private key found")
					return fmt.Errorf("no active wallet: %w", werr)
				}
				if *proofType == proofTypeJWS {
					key, kerr := mpCrypto.DeriveKeyFromSeed(acc.PrivateKey)
					if kerr != nil {
						spin.Stop(false, "Key derivation failed")
						return kerr
					}
					svc = did.NewServiceWithECDSAKey(key)
				} else {
					s, serr := did.NewServiceWithStellarSeed(acc.PrivateKey)
					if serr != nil {
						spin.Stop(false, "Stellar seed error: "+serr.Error())
						return serr
					}
					svc = s
				}
				ui.Info(fmt.Sprintf("VC signed with wallet key (%s...)", acc.Address[:8]))
			} else {
				if *proofType == proofTypeJWS {
					eckey, eerr := mpCrypto.GenerateKeyPair()
					if eerr != nil {
						spin.Stop(false, "Key generation failed")
						return eerr
					}
					svc = did.NewServiceWithECDSAKey(eckey)
				} else {
					var err error
					svc, err = did.NewService()
					if err != nil {
						spin.Stop(false, "Failed")
						return err
					}
				}
			}

			doc, err := svc.CreateDID(m)
			if err != nil {
				spin.Stop(false, err.Error())
				return err
			}

			vcTypeName := vcTypeToName(*vcType)
			vc, err := svc.IssueNationalIDVC(doc.ID, doc.ID, *name, *country, "", *birthYear, *level)
			if err != nil {
				spin.Stop(false, err.Error())
				return err
			}
			vc.Type = append(vc.Type[:1], vcTypeName)

			spin.Stop(true, "Attestation complete")

			config.SaveState("vc_latest", vc)
			config.SaveState("did_"+string(m), doc)
			cfg.ActiveDID = doc.ID
			cfg.DIDMethod = string(m)
			config.Save(cfg)

			if *output == "json" {
				fmt.Println(did.PrettyPrint(vc))
				return nil
			}

			ui.SectionLabel("Verifiable Credential")
			ui.KV("VC ID", vc.ID)
			ui.KV("Type", vcTypeName)
			ui.KV("Issuer DID", safeTrunc(vc.Issuer, 40)+"...")
			ui.KV("Subject", *name)
			ui.KV("Country", *country)
			ui.KV("Level", *level)
			ui.KV("Issued", vc.IssuanceDate.Format(time.RFC3339))
			ui.KV("Expires", vc.ExpirationDate.Format("2006-01-02"))
			ui.KV("Proof Type", vc.Proof.Type)
			if vc.Proof.ProofValue != "" {
				ui.KV("Proof Value (trunc)", safeTrunc(vc.Proof.ProofValue, 24)+"...")
			}
			if vc.Proof.JWSSignature != "" {
				ui.KV("JWS (trunc)", safeTrunc(vc.Proof.JWSSignature, 24)+"...")
			}
			if *useWalletKey {
				ui.KVColor("Signed By", "wallet key", ui.BrightGreen)
			} else {
				ui.KV("Signed By", "ephemeral key")
			}

			ui.Info("Saved to ~/.mozartpay/state/vc_latest.json")
			ui.KVColor("Active DID", safeTrunc(doc.ID, 50)+"...", ui.Teal)

			// Optionally anchor the VC hash on-chain
			if *anchor {
				if err := anchorVCOnChain(cfg, vc, doc); err != nil {
					ui.Warn(fmt.Sprintf("On-chain anchor failed: %v", err))
				}
			}

			return nil
		},
	}
}

// anchorVCOnChain registers the issuer DID (if needed) and anchors the VC hash
// in the DID registry contract.
func anchorVCOnChain(cfg *config.Config, vc *models.VerifiableCredential, doc *models.DIDDocument) error {
	if cfg.DIDRegistryContractID == "" {
		return fmt.Errorf("no DID registry configured — run 'mozartpay did set-registry --id <contract-id>'")
	}

	kp, net, account, err := getKeypairAndNetwork(cfg)
	if err != nil {
		return err
	}

	reg := did.NewRegistryClient(net, cfg.DIDRegistryContractID)
	defer reg.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()

	// Ensure issuer DID is registered on-chain
	exists, err := reg.DIDExists(ctx, doc.ID)
	if err != nil {
		return fmt.Errorf("did_exists check: %w", err)
	}
	if !exists {
		ui.SectionLabel("Registering issuer DID on-chain...")
		if _, err := reg.RegisterDID(ctx, kp, doc, account.Address, nil); err != nil {
			return fmt.Errorf("register_did: %w", err)
		}
		ui.Success("Issuer DID registered")
	}

	vcHash, err := did.VCHash(vc)
	if err != nil {
		return err
	}

	subject := doc.ID
	if sid, ok := vc.CredentialSubject["id"].(string); ok && sid != "" {
		subject = sid
	}

	vcType := "VerifiableCredential"
	if len(vc.Type) > 1 {
		vcType = vc.Type[1]
	}

	ui.SectionLabel("Anchoring credential hash...")
	result, err := reg.AnchorCredential(ctx, kp, vcHash, vc.Issuer, subject, vcType)
	if err != nil {
		return fmt.Errorf("anchor_credential: %w", err)
	}

	ui.Success("Credential anchored on-chain")
	ui.KV("VC Hash", hex.EncodeToString(vcHash[:]))
	ui.Info(fmt.Sprintf("TX: %s", result.TxHash))
	return nil
}

// checkOnChainStatus queries the DID registry for the credential's revocation
// status and prints it. Non-fatal: failures are reported as warnings.
func checkOnChainStatus(cfg *config.Config, vc *models.VerifiableCredential) {
	vcHash, err := did.VCHash(vc)
	if err != nil {
		ui.Warn(fmt.Sprintf("Could not compute VC hash: %v", err))
		return
	}

	net := cfg.Network
	if net == "" {
		net = "stellar-testnet"
	}

	reg := did.NewRegistryClient(net, cfg.DIDRegistryContractID)
	defer reg.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	rec, err := reg.CredentialStatus(ctx, vcHash)
	if err != nil {
		ui.Info("On-chain status: not anchored (or registry unreachable)")
		return
	}

	ui.SectionLabel("On-Chain Status")
	ui.KV("VC Hash", hex.EncodeToString(vcHash[:]))
	if rec.Status == "Revoked" {
		ui.KVColor("Revocation", "REVOKED", ui.BrightRed)
		if rec.RevokedAt != nil {
			ui.KV("Revoked At", rec.RevokedAt.Format(time.RFC3339))
		}
	} else {
		ui.KVColor("Revocation", "VALID (not revoked)", ui.BrightGreen)
	}
}

// ─── did verify ───────────────────────────────

func newDIDVerifyCmd(cfg *config.Config) *Command {
	fs := flag.NewFlagSet("verify", flag.ContinueOnError)
	vcFile := fs.String("vc-file", "", "Path to VC JSON file (optional, uses saved state if omitted)")

	return &Command{
		Name:  "verify",
		Short: "Verify a Verifiable Credential",
		Flags: fs,
		Run: func(c *Command, args []string) error {
			ui.Header("Verify VC")

			var vc models.VerifiableCredential
			if *vcFile != "" {
				data, err := os.ReadFile(*vcFile)
				if err != nil {
					return fmt.Errorf("read VC file: %w", err)
				}
				if err := json.Unmarshal(data, &vc); err != nil {
					return fmt.Errorf("parse VC file: %w", err)
				}
			} else if err := config.LoadState("vc_latest", &vc); err != nil {
				ui.Warn("No saved VC found. Run 'mozartpay did attest' first.")
				return nil
			}

			spin := ui.NewSpinner("Verifying credential...")
			spin.Start()
			time.Sleep(500 * time.Millisecond)

			svc, _ := did.NewService()
			valid, err := svc.Verify(&vc)
			if err != nil {
				spin.Stop(false, "Verification failed: "+err.Error())
				return fmt.Errorf("verification failed: %w", err)
			}
			spin.Stop(valid, map[bool]string{true: "Credential is VALID", false: "Credential is INVALID"}[valid])

			ui.SectionLabel("Verification Result")
			ui.KV("VC ID", vc.ID)
			ui.KV("Issuer", safeTrunc(vc.Issuer, 40)+"...")
			ui.KV("Proof Type", vc.Proof.Type)
			ui.KV("Valid", fmt.Sprintf("%v", valid))
			ui.KV("Expires", vc.ExpirationDate.Format("2006-01-02"))
			ui.KV("Status", map[bool]string{true: "✓ VERIFIED", false: "✗ INVALID"}[valid])

			// Check on-chain revocation status if registry is configured
			if cfg.DIDRegistryContractID != "" {
				checkOnChainStatus(cfg, &vc)
			}

			if !valid {
				return fmt.Errorf("credential verification failed")
			}
			return nil
		},
	}
}

// ─── did show ────────────────────────────────

func newDIDShowCmd(cfg *config.Config) *Command {
	return &Command{
		Name:  "show",
		Short: "Show supported DID methods",
		Run: func(c *Command, args []string) error {
			ui.Header("DID Methods")
			fmt.Println()

			methods := []struct {
				method  string
				spec    string
				usecase string
			}{
				{"did:web", "W3C DID Core", "Web-hosted DIDs for organizations (TLS-anchored)"},
				{"did:key", "W3C DID Core", "Self-sovereign, portable, no registry required"},
				{"did:ethr", "ERC-1056", "Ethereum-based DID, smart contract anchoring"},
				{"did:ebsi", "EBSI v3", "EU Blockchain Services Infrastructure (eIDAS 2.0)"},
				{"did:soroban", "MozartPay", "Stellar account DID, derived on-chain via registry"},
			}

			t := ui.NewTable("Method", "Spec", "Use Case")
			for _, m := range methods {
				t.AddRow(ui.Teal_(m.method), m.spec, m.usecase)
			}
			t.Print()

			if cfg.ActiveDID != "" {
				ui.SectionLabel("Active DID")
				ui.KV("DID", cfg.ActiveDID)
				ui.KV("Method", cfg.DIDMethod)
			}

			return nil
		},
	}
}

// ─── did set-registry ────────────────────────

func newDIDSetRegistryCmd(cfg *config.Config) *Command {
	fs := flag.NewFlagSet("set-registry", flag.ContinueOnError)
	id := fs.String("id", "", "DID registry contract ID (C...)")

	return &Command{
		Name:  "set-registry",
		Short: "Set the DID registry contract ID",
		Flags: fs,
		Run: func(c *Command, args []string) error {
			if *id == "" {
				ui.Error("Contract ID required (--id)")
				return fmt.Errorf("contract ID required")
			}
			cfg.DIDRegistryContractID = *id
			if err := config.Save(cfg); err != nil {
				return fmt.Errorf("save config: %w", err)
			}
			ui.Success(fmt.Sprintf("DID registry contract set to %s", *id))
			return nil
		},
	}
}

// ─── did register ────────────────────────────

func newDIDRegisterCmd(cfg *config.Config) *Command {
	fs := flag.NewFlagSet("register", flag.ContinueOnError)
	docFile := fs.String("doc-file", "", "Path to DID document JSON (default: saved state for active method)")
	controller := fs.String("controller", "", "Controller Stellar address (default: active wallet)")

	return &Command{
		Name:  "register",
		Short: "Register a DID document on-chain in the registry contract",
		Flags: fs,
		Run: func(c *Command, args []string) error {
			if cfg.DIDRegistryContractID == "" {
				ui.Error("No DID registry contract configured. Run 'mozartpay did set-registry --id <contract-id>'")
				return fmt.Errorf("registry contract ID not set")
			}

			var doc models.DIDDocument
			if *docFile != "" {
				data, err := os.ReadFile(*docFile)
				if err != nil {
					return fmt.Errorf("read DID document: %w", err)
				}
				if err := json.Unmarshal(data, &doc); err != nil {
					return fmt.Errorf("parse DID document: %w", err)
				}
			} else {
				method := cfg.DIDMethod
				if method == "" {
					method = "key"
				}
				if err := config.LoadState("did_"+method, &doc); err != nil {
					ui.Warn(fmt.Sprintf("No saved DID document for method %q. Run 'mozartpay did create' first or pass --doc-file.", method))
					return fmt.Errorf("no DID document: %w", err)
				}
			}

			kp, net, account, err := getKeypairAndNetwork(cfg)
			if err != nil {
				return err
			}

			ctrl := *controller
			if ctrl == "" {
				ctrl = account.Address
			}

			ui.Header("Register DID On-Chain")
			ui.KV("DID", doc.ID)
			ui.KV("Controller", ctrl)
			ui.KV("Registry", cfg.DIDRegistryContractID)

			reg := did.NewRegistryClient(net, cfg.DIDRegistryContractID)
			defer reg.Close()

			ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
			defer cancel()

			ui.SectionLabel("Submitting transaction...")
			result, err := reg.RegisterDID(ctx, kp, &doc, ctrl, nil)
			if err != nil {
				ui.Error(fmt.Sprintf("Transaction failed: %v", err))
				return fmt.Errorf("register_did failed: %w", err)
			}

			ui.Success("DID registered on-chain")
			ui.Info(fmt.Sprintf("TX: %s", result.TxHash))
			return nil
		},
	}
}

// ─── did resolve ─────────────────────────────

func newDIDResolveCmd(cfg *config.Config) *Command {
	fs := flag.NewFlagSet("resolve", flag.ContinueOnError)
	didFlag := fs.String("did", "", "DID to resolve (default: active DID)")
	output := fs.String("output", "pretty", "Output format: pretty | json")

	return &Command{
		Name:  "resolve",
		Short: "Resolve a DID from the on-chain registry",
		Flags: fs,
		Run: func(c *Command, args []string) error {
			if cfg.DIDRegistryContractID == "" {
				ui.Error("No DID registry contract configured. Run 'mozartpay did set-registry --id <contract-id>'")
				return fmt.Errorf("registry contract ID not set")
			}

			d := *didFlag
			if d == "" {
				d = cfg.ActiveDID
			}
			if d == "" {
				ui.Error("No DID provided (--did) and no active DID configured")
				return fmt.Errorf("DID required")
			}

			net := cfg.Network
			if net == "" {
				net = "stellar-testnet"
			}

			ui.Header("Resolve DID")
			ui.KV("DID", d)

			reg := did.NewRegistryClient(net, cfg.DIDRegistryContractID)
			defer reg.Close()

			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer cancel()

			doc, err := reg.ResolveDID(ctx, d)
			if err != nil {
				ui.Error(fmt.Sprintf("Resolution failed: %v", err))
				return fmt.Errorf("resolve_did failed: %w", err)
			}

			if *output == "json" {
				data, _ := json.MarshalIndent(doc, "", "  ")
				fmt.Println(string(data))
				return nil
			}

			ui.SectionLabel("DID Document (on-chain)")
			ui.KV("DID", doc.DID)
			ui.KV("Method", string(doc.Method))
			ui.KV("Controller", doc.Controller)
			ui.KV("Created", doc.CreatedAt.Format(time.RFC3339))
			ui.KV("Updated", doc.UpdatedAt.Format(time.RFC3339))
			ui.KV("Deactivated", fmt.Sprintf("%v", doc.Deactivated))

			if len(doc.VerificationMethods) > 0 {
				ui.SectionLabel("Verification Methods")
				for _, vm := range doc.VerificationMethods {
					ui.KV(vm.ID, fmt.Sprintf("%s (%s...)", vm.Type, safeTrunc(vm.PublicKeyHex, 16)))
				}
			}
			if len(doc.Services) > 0 {
				ui.SectionLabel("Services")
				for _, s := range doc.Services {
					ui.KV(s.ID, fmt.Sprintf("%s -> %s", s.Type, s.Endpoint))
				}
			}

			return nil
		},
	}
}

// ─── did deactivate ──────────────────────────

func newDIDDeactivateCmd(cfg *config.Config) *Command {
	fs := flag.NewFlagSet("deactivate", flag.ContinueOnError)
	didFlag := fs.String("did", "", "DID to deactivate (default: active DID)")

	return &Command{
		Name:  "deactivate",
		Short: "Deactivate a DID in the on-chain registry (tombstone)",
		Flags: fs,
		Run: func(c *Command, args []string) error {
			if cfg.DIDRegistryContractID == "" {
				ui.Error("No DID registry contract configured. Run 'mozartpay did set-registry --id <contract-id>'")
				return fmt.Errorf("registry contract ID not set")
			}

			d := *didFlag
			if d == "" {
				d = cfg.ActiveDID
			}
			if d == "" {
				ui.Error("No DID provided (--did) and no active DID configured")
				return fmt.Errorf("DID required")
			}

			kp, net, _, err := getKeypairAndNetwork(cfg)
			if err != nil {
				return err
			}

			ui.Header("Deactivate DID")
			ui.KV("DID", d)

			reg := did.NewRegistryClient(net, cfg.DIDRegistryContractID)
			defer reg.Close()

			ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
			defer cancel()

			ui.SectionLabel("Submitting transaction...")
			result, err := reg.DeactivateDID(ctx, kp, d)
			if err != nil {
				ui.Error(fmt.Sprintf("Transaction failed: %v", err))
				return fmt.Errorf("deactivate_did failed: %w", err)
			}

			ui.Success("DID deactivated on-chain")
			ui.Info(fmt.Sprintf("TX: %s", result.TxHash))
			return nil
		},
	}
}

// ─── did anchor ──────────────────────────────

func newDIDAnchorCmd(cfg *config.Config) *Command {
	fs := flag.NewFlagSet("anchor", flag.ContinueOnError)
	vcFile := fs.String("vc-file", "", "Path to VC JSON file (default: saved vc_latest state)")
	subjectDID := fs.String("subject-did", "", "Subject DID (default: credentialSubject.id from VC)")

	return &Command{
		Name:  "anchor",
		Short: "Anchor a verifiable credential hash on-chain",
		Flags: fs,
		Run: func(c *Command, args []string) error {
			if cfg.DIDRegistryContractID == "" {
				ui.Error("No DID registry contract configured. Run 'mozartpay did set-registry --id <contract-id>'")
				return fmt.Errorf("registry contract ID not set")
			}

			var vc models.VerifiableCredential
			if *vcFile != "" {
				data, err := os.ReadFile(*vcFile)
				if err != nil {
					return fmt.Errorf("read VC file: %w", err)
				}
				if err := json.Unmarshal(data, &vc); err != nil {
					return fmt.Errorf("parse VC file: %w", err)
				}
			} else if err := config.LoadState("vc_latest", &vc); err != nil {
				ui.Warn("No saved VC found. Run 'mozartpay did attest' first or pass --vc-file.")
				return fmt.Errorf("no VC: %w", err)
			}

			vcHash, err := did.VCHash(&vc)
			if err != nil {
				return err
			}

			subject := *subjectDID
			if subject == "" {
				if sid, ok := vc.CredentialSubject["id"].(string); ok {
					subject = sid
				}
			}
			if subject == "" {
				ui.Error("Subject DID required (--subject-did or credentialSubject.id in VC)")
				return fmt.Errorf("subject DID required")
			}

			vcType := "VerifiableCredential"
			if len(vc.Type) > 1 {
				vcType = vc.Type[1]
			} else if len(vc.Type) == 1 {
				vcType = vc.Type[0]
			}

			kp, net, _, err := getKeypairAndNetwork(cfg)
			if err != nil {
				return err
			}

			ui.Header("Anchor Credential On-Chain")
			ui.KV("VC Hash", hex.EncodeToString(vcHash[:]))
			ui.KV("Issuer DID", safeTrunc(vc.Issuer, 50))
			ui.KV("Subject DID", safeTrunc(subject, 50))
			ui.KV("VC Type", vcType)

			reg := did.NewRegistryClient(net, cfg.DIDRegistryContractID)
			defer reg.Close()

			ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
			defer cancel()

			ui.SectionLabel("Submitting transaction...")
			result, err := reg.AnchorCredential(ctx, kp, vcHash, vc.Issuer, subject, vcType)
			if err != nil {
				ui.Error(fmt.Sprintf("Transaction failed: %v", err))
				return fmt.Errorf("anchor_credential failed: %w", err)
			}

			ui.Success("Credential anchored on-chain")
			ui.Info(fmt.Sprintf("TX: %s", result.TxHash))
			return nil
		},
	}
}

// ─── did revoke ──────────────────────────────

func newDIDRevokeCmd(cfg *config.Config) *Command {
	fs := flag.NewFlagSet("revoke", flag.ContinueOnError)
	vcHashFlag := fs.String("vc-hash", "", "Hex-encoded VC hash to revoke (default: hash of saved vc_latest)")

	return &Command{
		Name:  "revoke",
		Short: "Revoke an anchored credential on-chain",
		Flags: fs,
		Run: func(c *Command, args []string) error {
			if cfg.DIDRegistryContractID == "" {
				ui.Error("No DID registry contract configured. Run 'mozartpay did set-registry --id <contract-id>'")
				return fmt.Errorf("registry contract ID not set")
			}

			vcHash, err := resolveVCHash(*vcHashFlag)
			if err != nil {
				ui.Error(err.Error())
				return err
			}

			kp, net, _, err := getKeypairAndNetwork(cfg)
			if err != nil {
				return err
			}

			ui.Header("Revoke Credential")
			ui.KV("VC Hash", hex.EncodeToString(vcHash[:]))

			reg := did.NewRegistryClient(net, cfg.DIDRegistryContractID)
			defer reg.Close()

			ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
			defer cancel()

			ui.SectionLabel("Submitting transaction...")
			result, err := reg.RevokeCredential(ctx, kp, vcHash)
			if err != nil {
				ui.Error(fmt.Sprintf("Transaction failed: %v", err))
				return fmt.Errorf("revoke_credential failed: %w", err)
			}

			ui.Success("Credential revoked on-chain")
			ui.Info(fmt.Sprintf("TX: %s", result.TxHash))
			return nil
		},
	}
}

// ─── did status ──────────────────────────────

func newDIDStatusCmd(cfg *config.Config) *Command {
	fs := flag.NewFlagSet("status", flag.ContinueOnError)
	vcHashFlag := fs.String("vc-hash", "", "Hex-encoded VC hash to query (default: hash of saved vc_latest)")
	output := fs.String("output", "pretty", "Output format: pretty | json")

	return &Command{
		Name:  "status",
		Short: "Query on-chain credential status",
		Flags: fs,
		Run: func(c *Command, args []string) error {
			if cfg.DIDRegistryContractID == "" {
				ui.Error("No DID registry contract configured. Run 'mozartpay did set-registry --id <contract-id>'")
				return fmt.Errorf("registry contract ID not set")
			}

			vcHash, err := resolveVCHash(*vcHashFlag)
			if err != nil {
				ui.Error(err.Error())
				return err
			}

			net := cfg.Network
			if net == "" {
				net = "stellar-testnet"
			}

			ui.Header("Credential Status")
			ui.KV("VC Hash", hex.EncodeToString(vcHash[:]))

			reg := did.NewRegistryClient(net, cfg.DIDRegistryContractID)
			defer reg.Close()

			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer cancel()

			rec, err := reg.CredentialStatus(ctx, vcHash)
			if err != nil {
				ui.Error(fmt.Sprintf("Query failed: %v", err))
				return fmt.Errorf("credential_status failed: %w", err)
			}

			if *output == "json" {
				data, _ := json.MarshalIndent(rec, "", "  ")
				fmt.Println(string(data))
				return nil
			}

			ui.SectionLabel("On-Chain Record")
			ui.KV("Issuer DID", rec.IssuerDID)
			ui.KV("Subject DID", rec.SubjectDID)
			ui.KV("VC Type", rec.VCType)
			ui.KV("Anchored", rec.AnchoredAt.Format(time.RFC3339))
			if rec.RevokedAt != nil {
				ui.KV("Revoked", rec.RevokedAt.Format(time.RFC3339))
			}
			if rec.Status == "Revoked" {
				ui.KVColor("Status", "REVOKED", ui.BrightRed)
			} else {
				ui.KVColor("Status", "VALID", ui.BrightGreen)
			}

			return nil
		},
	}
}

// ─────────────────────────────────────────────
// Helpers
// ─────────────────────────────────────────────

// resolveVCHash parses a hex-encoded VC hash flag, or falls back to hashing
// the saved vc_latest credential.
func resolveVCHash(flagVal string) ([32]byte, error) {
	var zero [32]byte
	if flagVal != "" {
		b, err := hex.DecodeString(flagVal)
		if err != nil {
			return zero, fmt.Errorf("invalid --vc-hash (expected hex): %w", err)
		}
		if len(b) != 32 {
			return zero, fmt.Errorf("--vc-hash must be 32 bytes, got %d", len(b))
		}
		var h [32]byte
		copy(h[:], b)
		return h, nil
	}

	var vc models.VerifiableCredential
	if err := config.LoadState("vc_latest", &vc); err != nil {
		return zero, fmt.Errorf("no --vc-hash provided and no saved VC found: %w", err)
	}
	return did.VCHash(&vc)
}

func safeTrunc(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n]
}

func vcTypeToName(t string) string {
	switch t {
	case "national-id":
		return "NationalIdentityCredential"
	case "kyc":
		return "KYCCredential"
	case "accreditation":
		return "AccreditationCredential"
	default:
		return "VerifiableCredential"
	}
}

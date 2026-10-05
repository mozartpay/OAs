//go:build e2e

// Package e2e contains live end-to-end tests against Stellar testnet.
// These tests deploy real contract instances and submit real transactions,
// so they are excluded from `make test` via the `e2e` build tag.
//
// Run with:
//
//	cd mozartpay && go test -tags e2e ./e2e/ -v -timeout 15m
//
// Environment overrides:
//
//	E2E_NETWORK        Stellar network (default: stellar-testnet)
//	E2E_REGISTRY_WASM  Path to did_registry.wasm (default: ../contracts/dist/did_registry.wasm)
//	E2E_OA_WASM        Path to mozartpay_contracts.wasm (default: ../contracts/dist/mozartpay_contracts.wasm)
package e2e

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"math/big"
	"os"
	"testing"
	"time"

	"github.com/stellar/go/keypair"
	"github.com/stellar/go/xdr"

	"github.com/ogtechnologies/mozartpay/internal/did"
	"github.com/ogtechnologies/mozartpay/internal/models"
	"github.com/ogtechnologies/mozartpay/internal/soroban"
	"github.com/ogtechnologies/mozartpay/internal/wallet"
)

// TestE2E_Testnet_OA_DIDRegistry exercises the full Orchestrated Agreement
// lifecycle on Stellar testnet with two fresh wallets:
//
//	Wallet A (initiator): deploys both contracts, registers did:soroban,
//	issues + anchors a VC, and drives the OA lifecycle to Settled.
//	Wallet B (counterparty): registers its own did:soroban and is set as
//	the agreement counterparty.
//
//	1.  Create + fund two fresh Stellar keypairs via friendbot
//	2.  Deploy did_registry.wasm (constructor: owner = A)
//	3.  Deploy mozartpay_contracts.wasm (constructor: owner = A)
//	4.  set_did_registry on the OA contract (enables compliance gating)
//	5.  register_soroban_did for A and B; resolve both on-chain
//	6.  Issue a national-ID VC for A, anchor its hash in the registry
//	7.  OA lifecycle: create_agreement → attest_identity → connect_wallet →
//	    fund_and_set_asset → execute_agreement → settle_agreement
//	8.  Assert final on-chain state == Settled
//	9.  Negative check: revoke the VC, then attest_identity on a new
//	    agreement must fail (CredentialRevoked)
func TestE2E_Testnet_OA_DIDRegistry(t *testing.T) {
	network := envOr("E2E_NETWORK", "stellar-testnet")
	registryWasm := envOr("E2E_REGISTRY_WASM", "../contracts/dist/did_registry.wasm")
	oaWasm := envOr("E2E_OA_WASM", "../contracts/dist/mozartpay_contracts.wasm")

	ctx := context.Background()
	client := soroban.NewClientForNetwork(network)
	defer client.Close()

	// ── Step 1: fresh wallets ────────────────────────────────────────
	kpA, err := keypair.Random()
	if err != nil {
		t.Fatalf("generate keypair A: %v", err)
	}
	kpB, err := keypair.Random()
	if err != nil {
		t.Fatalf("generate keypair B: %v", err)
	}
	t.Logf("Wallet A (initiator):   %s", kpA.Address())
	t.Logf("Wallet B (counterparty): %s", kpB.Address())

	fundWallet(t, client, kpA)
	fundWallet(t, client, kpB)

	// ── Step 2: deploy DID registry ──────────────────────────────────
	registryID := deploy(t, client, kpA, registryWasm)
	t.Logf("DID registry contract: %s", registryID)

	// ── Step 3: deploy OA contract ───────────────────────────────────
	oaID := deploy(t, client, kpA, oaWasm)
	t.Logf("OA contract: %s", oaID)

	// ── Step 4: link registry to OA contract ─────────────────────────
	regAddr, err := soroban.ParseContractID(registryID)
	if err != nil {
		t.Fatalf("parse registry contract ID: %v", err)
	}
	invoke(t, client, kpA, oaID, "set_did_registry", []xdr.ScVal{
		soroban.ScvAddress(regAddr),
	})

	// ── Step 5: register DIDs on-chain ───────────────────────────────
	reg := did.NewRegistryClient(network, registryID)
	defer reg.Close()

	didA := registerSorobanDID(t, reg, kpA)
	didB := registerSorobanDID(t, reg, kpB)
	t.Logf("DID A: %s", didA)
	t.Logf("DID B: %s", didB)

	docA := resolveDID(t, reg, didA)
	if docA.Controller != kpA.Address() {
		t.Fatalf("resolved DID A controller = %s, want %s", docA.Controller, kpA.Address())
	}
	if docA.Deactivated {
		t.Fatal("resolved DID A is unexpectedly deactivated")
	}
	resolveDID(t, reg, didB)

	// ── Step 6: issue VC + anchor hash on-chain ──────────────────────
	vcSvc, err := did.NewServiceWithStellarSeed(kpA.Seed())
	if err != nil {
		t.Fatalf("DID service from wallet seed: %v", err)
	}
	vc, err := vcSvc.IssueNationalIDVC(didA, didA, "E2E Test Holder", "AT", "", "", "KYC_LEVEL_2")
	if err != nil {
		t.Fatalf("issue VC: %v", err)
	}
	vcHash, err := did.VCHash(vc)
	if err != nil {
		t.Fatalf("hash VC: %v", err)
	}

	anchCtx, anchCancel := context.WithTimeout(ctx, 2*time.Minute)
	if _, err := reg.AnchorCredential(anchCtx, kpA, vcHash, didA, didA, "NationalIdentityCredential"); err != nil {
		anchCancel()
		t.Fatalf("anchor_credential: %v", err)
	}
	anchCancel()
	t.Logf("VC anchored: %s", hex.EncodeToString(vcHash[:]))

	rec := credentialStatus(t, reg, vcHash)
	if rec.Status == "Revoked" {
		t.Fatal("freshly anchored credential reports Revoked")
	}

	// ── Step 7: OA lifecycle ─────────────────────────────────────────
	initiatorAddr, err := soroban.AccountToScAddress(kpA.Address())
	if err != nil {
		t.Fatalf("initiator address: %v", err)
	}
	counterpartyAddr, err := soroban.AccountToScAddress(kpB.Address())
	if err != nil {
		t.Fatalf("counterparty address: %v", err)
	}

	// expires_at=None → dispute_window_end stays unset, so settle is not
	// time-gated. dispute_window must still be > 0 (contract validation).
	cpVal := soroban.ScvAddress(counterpartyAddr)
	createRes := invoke(t, client, kpA, oaID, "create_agreement", []xdr.ScVal{
		soroban.ScvAddress(initiatorAddr),
		soroban.ScvOption(&cpVal),
		soroban.ScvOption(nil),
		soroban.ScvU32(60),
	})
	agreementID := agreementIDFromResult(t, createRes)
	t.Logf("Agreement ID: %s", hex.EncodeToString(agreementID[:]))

	// Layer 1: identity attestation — gated by the DID registry (DID must
	// resolve, anchored credential must not be revoked).
	invoke(t, client, kpA, oaID, "attest_identity", []xdr.ScVal{
		soroban.ScvBytesN32(agreementID),
		soroban.ScvString(didA),
		didMethodEnum("Soroban"),
		soroban.ScvSymbol("national_id"),
		soroban.ScvBytesN32(vcHash),
		soroban.ScvOption(nil), // verifier
	})
	assertAgreementState(t, client, oaID, agreementID, "Active")

	// Layer 2: connect wallet
	invoke(t, client, kpA, oaID, "connect_wallet", []xdr.ScVal{
		soroban.ScvBytesN32(agreementID),
		soroban.ScvAddress(initiatorAddr),
		soroban.ScvSymbol("testnet"),
		soroban.ScvBool(false),
		soroban.ScvU32(1),
	})

	// Layer 3: fund asset → state Funded
	invoke(t, client, kpA, oaID, "fund_and_set_asset", []xdr.ScVal{
		soroban.ScvBytesN32(agreementID),
		soroban.ScvSymbol("XLM"),
		soroban.ScvI128(big.NewInt(100)),
		soroban.ScvI128(big.NewInt(0)),
		soroban.ScvSymbol("fungible"),
		soroban.ScvOption(nil), // contract_id
	})
	assertAgreementState(t, client, oaID, agreementID, "Funded")

	// Layer 5: execute → state Executed (re-runs compliance check)
	settlementHash := sha256.Sum256([]byte("e2e-settlement-" + hex.EncodeToString(agreementID[:])))
	invoke(t, client, kpA, oaID, "execute_agreement", []xdr.ScVal{
		soroban.ScvBytesN32(agreementID),
		soroban.ScvBytesN32(settlementHash),
	})
	assertAgreementState(t, client, oaID, agreementID, "Executed")

	// Settle → state Settled
	invoke(t, client, kpA, oaID, "settle_agreement", []xdr.ScVal{
		soroban.ScvBytesN32(agreementID),
	})
	assertAgreementState(t, client, oaID, agreementID, "Settled")

	// ── Step 8: negative check — revoked credential blocks attestation ──
	revCtx, revCancel := context.WithTimeout(ctx, 2*time.Minute)
	if _, err := reg.RevokeCredential(revCtx, kpA, vcHash); err != nil {
		revCancel()
		t.Fatalf("revoke_credential: %v", err)
	}
	revCancel()

	if rec := credentialStatus(t, reg, vcHash); rec.Status != "Revoked" {
		t.Fatalf("credential status = %q, want Revoked", rec.Status)
	}

	createRes2 := invoke(t, client, kpA, oaID, "create_agreement", []xdr.ScVal{
		soroban.ScvAddress(initiatorAddr),
		soroban.ScvOption(&cpVal),
		soroban.ScvOption(nil),
		soroban.ScvU32(60),
	})
	agreementID2 := agreementIDFromResult(t, createRes2)

	attCtx, attCancel := context.WithTimeout(ctx, 2*time.Minute)
	_, err = client.Invoke(attCtx, kpA, oaID, "attest_identity", []xdr.ScVal{
		soroban.ScvBytesN32(agreementID2),
		soroban.ScvString(didA),
		didMethodEnum("Soroban"),
		soroban.ScvSymbol("national_id"),
		soroban.ScvBytesN32(vcHash),
		soroban.ScvOption(nil),
	})
	attCancel()
	if err == nil {
		t.Fatal("attest_identity succeeded with a revoked credential — compliance gating broken")
	}
	t.Logf("attest_identity correctly rejected after revocation: %v", err)
}

// ─── Helpers ────────────────────────────────────────────────────────

func envOr(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}

// fundWallet funds a fresh keypair via friendbot and waits until the
// account is visible to the Soroban RPC.
func fundWallet(t *testing.T, c *soroban.Client, kp *keypair.Full) {
	t.Helper()

	svc := wallet.NewService()
	if _, err := svc.FundTestnetAccount(&models.Account{
		Address:    kp.Address(),
		PublicKey:  kp.Address(),
		PrivateKey: kp.Seed(),
		Network:    models.NetworkStellarTestnet,
		Type:       models.WalletStellar,
	}); err != nil {
		t.Fatalf("friendbot funding for %s: %v", kp.Address(), err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	for {
		if _, err := c.LoadAccount(ctx, kp.Address()); err == nil {
			t.Logf("Funded: %s", kp.Address())
			return
		}
		select {
		case <-ctx.Done():
			t.Fatalf("account %s not visible to RPC after funding", kp.Address())
		case <-time.After(2 * time.Second):
		}
	}
}

// deploy uploads a WASM contract and creates an instance with the standard
// __constructor(owner: Address) argument (deployer = owner).
func deploy(t *testing.T, c *soroban.Client, kp *keypair.Full, wasmPath string) string {
	t.Helper()

	wasmBytes, err := os.ReadFile(wasmPath)
	if err != nil {
		t.Fatalf("read wasm %s: %v (run `cd contracts && make build` first)", wasmPath, err)
	}

	owner, err := soroban.AccountToScAddress(kp.Address())
	if err != nil {
		t.Fatalf("owner address: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()

	res, err := c.DeployWithConstructorArgs(ctx, kp, wasmBytes, []xdr.ScVal{
		soroban.ScvAddress(owner),
	})
	if err != nil {
		t.Fatalf("deploy %s: %v", wasmPath, err)
	}
	t.Logf("Deployed %s → %s (upload tx %s)", wasmPath, res.ContractID, res.UploadTxHash)
	return res.ContractID
}

// invoke submits a write call and fails the test on error.
func invoke(t *testing.T, c *soroban.Client, kp *keypair.Full, contractID, method string, args []xdr.ScVal) *soroban.InvokeResult {
	t.Helper()

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()

	res, err := c.Invoke(ctx, kp, contractID, method, args)
	if err != nil {
		t.Fatalf("%s: %v", method, err)
	}
	t.Logf("%s OK — tx %s", method, res.TxHash)
	return res
}

// simulate runs a read-only call and returns the decoded return ScVal.
func simulate(t *testing.T, c *soroban.Client, contractID, method string, args []xdr.ScVal) xdr.ScVal {
	t.Helper()

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	retXDR, err := c.SimulateOnly(ctx, contractID, method, args)
	if err != nil {
		t.Fatalf("simulate %s: %v", method, err)
	}
	var retVal xdr.ScVal
	if err := xdr.SafeUnmarshalBase64(retXDR, &retVal); err != nil {
		t.Fatalf("decode %s result: %v", method, err)
	}
	return retVal
}

// registerSorobanDID registers a did:soroban DID for the keypair's account
// and returns the derived DID string.
func registerSorobanDID(t *testing.T, reg *did.RegistryClient, kp *keypair.Full) string {
	t.Helper()

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()

	didStr, res, err := reg.RegisterSorobanDID(ctx, kp, kp.Address())
	if err != nil {
		t.Fatalf("register_soroban_did for %s: %v", kp.Address(), err)
	}
	t.Logf("register_soroban_did OK — tx %s", res.TxHash)

	existsCtx, existsCancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer existsCancel()
	exists, err := reg.DIDExists(existsCtx, didStr)
	if err != nil {
		t.Fatalf("did_exists(%s): %v", didStr, err)
	}
	if !exists {
		t.Fatalf("did_exists(%s) = false after registration", didStr)
	}
	return didStr
}

func resolveDID(t *testing.T, reg *did.RegistryClient, didStr string) *models.ResolvedDID {
	t.Helper()

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	doc, err := reg.ResolveDID(ctx, didStr)
	if err != nil {
		t.Fatalf("resolve_did(%s): %v", didStr, err)
	}
	if doc.DID != didStr {
		t.Fatalf("resolved DID = %s, want %s", doc.DID, didStr)
	}
	return doc
}

func credentialStatus(t *testing.T, reg *did.RegistryClient, vcHash [32]byte) *models.CredentialStatusRecord {
	t.Helper()

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	rec, err := reg.CredentialStatus(ctx, vcHash)
	if err != nil {
		t.Fatalf("credential_status: %v", err)
	}
	return rec
}

// agreementIDFromResult decodes the BytesN<32> agreement ID returned by
// create_agreement from the transaction's result meta.
func agreementIDFromResult(t *testing.T, res *soroban.InvokeResult) [32]byte {
	t.Helper()

	retVal, err := soroban.ReturnValueFromMetaXDR(res.ResultMetaXDR)
	if err != nil {
		t.Fatalf("extract create_agreement return value: %v", err)
	}
	id, err := soroban.DecodeScBytesN32(retVal)
	if err != nil {
		t.Fatalf("decode agreement ID: %v", err)
	}
	return id
}

// assertAgreementState simulates get_agreement_state and checks the enum
// variant name (unit enums serialize as Vec[Symbol]).
func assertAgreementState(t *testing.T, c *soroban.Client, contractID string, id [32]byte, want string) {
	t.Helper()

	retVal := simulate(t, c, contractID, "get_agreement_state", []xdr.ScVal{
		soroban.ScvBytesN32(id),
	})
	vec, err := soroban.DecodeScVec(retVal)
	if err != nil || len(vec) == 0 {
		t.Fatalf("decode agreement state enum: %v", err)
	}
	got, err := soroban.DecodeScString(vec[0])
	if err != nil {
		t.Fatalf("decode state variant: %v", err)
	}
	if got != want {
		t.Fatalf("agreement state = %s, want %s", got, want)
	}
	t.Logf("Agreement state: %s", got)
}

// didMethodEnum encodes a DIDMethod unit enum variant as Vec[Symbol],
// matching the contract's expected encoding.
func didMethodEnum(variant string) xdr.ScVal {
	return soroban.ScvVec([]xdr.ScVal{soroban.ScvSymbol(variant)})
}

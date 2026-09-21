package did

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"time"

	"github.com/stellar/go/keypair"
	"github.com/stellar/go/xdr"

	"github.com/ogtechnologies/mozartpay/internal/models"
	"github.com/ogtechnologies/mozartpay/internal/soroban"
)

// RegistryClient wraps the Soroban RPC client for DID registry contract calls.
// The registry contract stores full DID documents on-chain and tracks
// credential status (Valid/Revoked) keyed by VC content hash.
type RegistryClient struct {
	client     *soroban.Client
	contractID string
}

// NewRegistryClient creates a registry client for the given network and contract ID.
func NewRegistryClient(network, contractID string) *RegistryClient {
	return &RegistryClient{
		client:     soroban.NewClientForNetwork(network),
		contractID: contractID,
	}
}

// Close releases the underlying RPC client resources.
func (r *RegistryClient) Close() {
	r.client.Close()
}

// VCHash computes the content hash of a Verifiable Credential.
// Canonicalization: Go's encoding/json produces deterministic output —
// struct fields serialize in declaration order and map keys are sorted
// lexicographically — so SHA-256 over the marshaled VC is stable across runs.
func VCHash(vc *models.VerifiableCredential) ([32]byte, error) {
	data, err := json.Marshal(vc)
	if err != nil {
		return [32]byte{}, fmt.Errorf("marshal VC for hashing: %w", err)
	}
	return sha256.Sum256(data), nil
}

// ─── Write operations ─────────────────────────

// RegisterDID submits register_did to the registry contract.
// controller must be a Stellar account address (G...) whose keypair signs the tx.
func (r *RegistryClient) RegisterDID(
	ctx context.Context,
	kp *keypair.Full,
	doc *models.DIDDocument,
	controller string,
	services []models.DIDServiceEntry,
) (*soroban.InvokeResult, error) {
	ctrlAddr, err := soroban.AccountToScAddress(controller)
	if err != nil {
		return nil, fmt.Errorf("invalid controller address: %w", err)
	}

	vms := make([]xdr.ScVal, 0, len(doc.VerificationMethod))
	for _, vm := range doc.VerificationMethod {
		vms = append(vms, vmEntryToScVal(vm))
	}

	svcs := make([]xdr.ScVal, 0, len(services))
	for _, s := range services {
		svcs = append(svcs, serviceEntryToScVal(s))
	}

	args := []xdr.ScVal{
		soroban.ScvString(doc.ID),
		didMethodToScVal(doc.Method),
		soroban.ScvAddress(ctrlAddr),
		soroban.ScvVec(vms),
		soroban.ScvVec(svcs),
	}

	return r.client.Invoke(ctx, kp, r.contractID, "register_did", args)
}

// UpdateDID submits update_did to rotate verification methods / services.
func (r *RegistryClient) UpdateDID(
	ctx context.Context,
	kp *keypair.Full,
	doc *models.DIDDocument,
	services []models.DIDServiceEntry,
) (*soroban.InvokeResult, error) {
	vms := make([]xdr.ScVal, 0, len(doc.VerificationMethod))
	for _, vm := range doc.VerificationMethod {
		vms = append(vms, vmEntryToScVal(vm))
	}

	svcs := make([]xdr.ScVal, 0, len(services))
	for _, s := range services {
		svcs = append(svcs, serviceEntryToScVal(s))
	}

	args := []xdr.ScVal{
		soroban.ScvString(doc.ID),
		soroban.ScvVec(vms),
		soroban.ScvVec(svcs),
	}

	return r.client.Invoke(ctx, kp, r.contractID, "update_did", args)
}

// RegisterSorobanDID submits register_soroban_did, deriving a
// did:soroban:<G-address> DID on-chain from the given account. Returns the
// derived DID string.
func (r *RegistryClient) RegisterSorobanDID(
	ctx context.Context,
	kp *keypair.Full,
	account string,
) (string, *soroban.InvokeResult, error) {
	addr, err := soroban.AccountToScAddress(account)
	if err != nil {
		return "", nil, fmt.Errorf("invalid account address: %w", err)
	}

	res, err := r.client.Invoke(ctx, kp, r.contractID, "register_soroban_did", []xdr.ScVal{
		soroban.ScvAddress(addr),
	})
	if err != nil {
		return "", nil, err
	}

	retVal, err := soroban.ReturnValueFromMetaXDR(res.ResultMetaXDR)
	if err != nil {
		return "", res, fmt.Errorf("parse register_soroban_did result: %w", err)
	}
	didStr, err := soroban.DecodeScString(retVal)
	if err != nil {
		return "", res, fmt.Errorf("decode derived DID: %w", err)
	}
	return didStr, res, nil
}

// DeactivateDID submits deactivate_did (tombstone) to the registry contract.
func (r *RegistryClient) DeactivateDID(
	ctx context.Context,
	kp *keypair.Full,
	did string,
) (*soroban.InvokeResult, error) {
	return r.client.Invoke(ctx, kp, r.contractID, "deactivate_did", []xdr.ScVal{
		soroban.ScvString(did),
	})
}

// AnchorCredential submits anchor_credential to record a VC hash on-chain.
func (r *RegistryClient) AnchorCredential(
	ctx context.Context,
	kp *keypair.Full,
	vcHash [32]byte,
	issuerDID, subjectDID, vcType string,
) (*soroban.InvokeResult, error) {
	args := []xdr.ScVal{
		soroban.ScvBytesN32(vcHash),
		soroban.ScvString(issuerDID),
		soroban.ScvString(subjectDID),
		soroban.ScvSymbol(vcType),
	}
	return r.client.Invoke(ctx, kp, r.contractID, "anchor_credential", args)
}

// RevokeCredential submits revoke_credential for an anchored VC hash.
func (r *RegistryClient) RevokeCredential(
	ctx context.Context,
	kp *keypair.Full,
	vcHash [32]byte,
) (*soroban.InvokeResult, error) {
	return r.client.Invoke(ctx, kp, r.contractID, "revoke_credential", []xdr.ScVal{
		soroban.ScvBytesN32(vcHash),
	})
}

// ─── Read operations (simulation only) ────────

// ResolveDID queries resolve_did and decodes the on-chain DID document.
func (r *RegistryClient) ResolveDID(ctx context.Context, did string) (*models.ResolvedDID, error) {
	retXDR, err := r.client.SimulateOnly(ctx, r.contractID, "resolve_did", []xdr.ScVal{
		soroban.ScvString(did),
	})
	if err != nil {
		return nil, err
	}

	var retVal xdr.ScVal
	if err := xdr.SafeUnmarshalBase64(retXDR, &retVal); err != nil {
		return nil, fmt.Errorf("parse resolve_did result: %w", err)
	}

	return decodeDIDDocument(retVal)
}

// DIDExists queries did_exists.
func (r *RegistryClient) DIDExists(ctx context.Context, did string) (bool, error) {
	retXDR, err := r.client.SimulateOnly(ctx, r.contractID, "did_exists", []xdr.ScVal{
		soroban.ScvString(did),
	})
	if err != nil {
		return false, err
	}

	var retVal xdr.ScVal
	if err := xdr.SafeUnmarshalBase64(retXDR, &retVal); err != nil {
		return false, fmt.Errorf("parse did_exists result: %w", err)
	}

	return soroban.DecodeScBool(retVal)
}

// CredentialStatus queries credential_status for an anchored VC hash.
func (r *RegistryClient) CredentialStatus(ctx context.Context, vcHash [32]byte) (*models.CredentialStatusRecord, error) {
	retXDR, err := r.client.SimulateOnly(ctx, r.contractID, "credential_status", []xdr.ScVal{
		soroban.ScvBytesN32(vcHash),
	})
	if err != nil {
		return nil, err
	}

	var retVal xdr.ScVal
	if err := xdr.SafeUnmarshalBase64(retXDR, &retVal); err != nil {
		return nil, fmt.Errorf("parse credential_status result: %w", err)
	}

	return decodeCredentialRecord(retVal)
}

// ─── ScVal encoding helpers ───────────────────

// didMethodToScVal encodes the DIDMethod enum as Vec[Symbol(variant)].
func didMethodToScVal(m models.DIDMethod) xdr.ScVal {
	variant := "Key"
	switch m {
	case models.DIDMethodWeb:
		variant = "Web"
	case models.DIDMethodKey:
		variant = "Key"
	case models.DIDMethodEthr:
		variant = "Ethr"
	case models.DIDMethodEBSI:
		variant = "Ebsi"
	case models.DIDMethodSoroban:
		variant = "Soroban"
	}
	return soroban.ScvVec([]xdr.ScVal{soroban.ScvSymbol(variant)})
}

// vmEntryToScVal encodes a VerificationMethodEntry struct as an ScvMap
// with snake_case symbol keys matching the Rust field names.
func vmEntryToScVal(vm models.VerificationKey) xdr.ScVal {
	return soroban.ScvMap(map[string]xdr.ScVal{
		"id":                   soroban.ScvString(vm.ID),
		"vm_type":              soroban.ScvSymbol(vm.Type),
		"controller":           soroban.ScvString(vm.Controller),
		"public_key_multibase": soroban.ScvString(vm.PublicKeyHex),
	})
}

// serviceEntryToScVal encodes a ServiceEntry struct as an ScvMap.
func serviceEntryToScVal(s models.DIDServiceEntry) xdr.ScVal {
	return soroban.ScvMap(map[string]xdr.ScVal{
		"id":       soroban.ScvString(s.ID),
		"svc_type": soroban.ScvSymbol(s.Type),
		"endpoint": soroban.ScvString(s.Endpoint),
	})
}

// ─── ScVal decoding helpers ───────────────────

// decodeEnumVariant extracts the variant name from a unit enum encoded as Vec[Symbol].
func decodeEnumVariant(v xdr.ScVal) (string, error) {
	vec, err := soroban.DecodeScVec(v)
	if err != nil {
		return "", fmt.Errorf("decode enum: %w", err)
	}
	if len(vec) == 0 {
		return "", fmt.Errorf("empty enum vec")
	}
	return soroban.DecodeScString(vec[0])
}

func decodeDIDDocument(v xdr.ScVal) (*models.ResolvedDID, error) {
	m, err := soroban.DecodeScMap(v)
	if err != nil {
		return nil, fmt.Errorf("decode DIDDocumentData: %w", err)
	}

	doc := &models.ResolvedDID{}

	if val, ok := m["did"]; ok {
		if doc.DID, err = soroban.DecodeScString(val); err != nil {
			return nil, fmt.Errorf("field did: %w", err)
		}
	}
	if val, ok := m["method"]; ok {
		variant, verr := decodeEnumVariant(val)
		if verr != nil {
			return nil, fmt.Errorf("field method: %w", verr)
		}
		doc.Method = methodVariantToModel(variant)
	}
	if val, ok := m["controller"]; ok {
		if doc.Controller, err = soroban.DecodeScAddress(val); err != nil {
			return nil, fmt.Errorf("field controller: %w", err)
		}
	}
	if val, ok := m["verification_methods"]; ok {
		vms, verr := soroban.DecodeScVec(val)
		if verr != nil {
			return nil, fmt.Errorf("field verification_methods: %w", verr)
		}
		for _, vmVal := range vms {
			vm, verr := decodeVMEntry(vmVal)
			if verr != nil {
				return nil, verr
			}
			doc.VerificationMethods = append(doc.VerificationMethods, *vm)
		}
	}
	if val, ok := m["services"]; ok {
		svcs, serr := soroban.DecodeScVec(val)
		if serr != nil {
			return nil, fmt.Errorf("field services: %w", serr)
		}
		for _, sVal := range svcs {
			svc, serr := decodeServiceEntry(sVal)
			if serr != nil {
				return nil, serr
			}
			doc.Services = append(doc.Services, *svc)
		}
	}
	if val, ok := m["created_at"]; ok {
		ts, terr := soroban.DecodeScU64(val)
		if terr != nil {
			return nil, fmt.Errorf("field created_at: %w", terr)
		}
		doc.CreatedAt = time.Unix(int64(ts), 0).UTC()
	}
	if val, ok := m["updated_at"]; ok {
		ts, terr := soroban.DecodeScU64(val)
		if terr != nil {
			return nil, fmt.Errorf("field updated_at: %w", terr)
		}
		doc.UpdatedAt = time.Unix(int64(ts), 0).UTC()
	}
	if val, ok := m["deactivated"]; ok {
		if doc.Deactivated, err = soroban.DecodeScBool(val); err != nil {
			return nil, fmt.Errorf("field deactivated: %w", err)
		}
	}

	return doc, nil
}

func decodeVMEntry(v xdr.ScVal) (*models.VerificationKey, error) {
	m, err := soroban.DecodeScMap(v)
	if err != nil {
		return nil, fmt.Errorf("decode VerificationMethodEntry: %w", err)
	}
	vm := &models.VerificationKey{}
	if val, ok := m["id"]; ok {
		if vm.ID, err = soroban.DecodeScString(val); err != nil {
			return nil, fmt.Errorf("field id: %w", err)
		}
	}
	if val, ok := m["vm_type"]; ok {
		if vm.Type, err = soroban.DecodeScString(val); err != nil {
			return nil, fmt.Errorf("field vm_type: %w", err)
		}
	}
	if val, ok := m["controller"]; ok {
		if vm.Controller, err = soroban.DecodeScString(val); err != nil {
			return nil, fmt.Errorf("field controller: %w", err)
		}
	}
	if val, ok := m["public_key_multibase"]; ok {
		if vm.PublicKeyHex, err = soroban.DecodeScString(val); err != nil {
			return nil, fmt.Errorf("field public_key_multibase: %w", err)
		}
	}
	return vm, nil
}

func decodeServiceEntry(v xdr.ScVal) (*models.DIDServiceEntry, error) {
	m, err := soroban.DecodeScMap(v)
	if err != nil {
		return nil, fmt.Errorf("decode ServiceEntry: %w", err)
	}
	svc := &models.DIDServiceEntry{}
	if val, ok := m["id"]; ok {
		if svc.ID, err = soroban.DecodeScString(val); err != nil {
			return nil, fmt.Errorf("field id: %w", err)
		}
	}
	if val, ok := m["svc_type"]; ok {
		if svc.Type, err = soroban.DecodeScString(val); err != nil {
			return nil, fmt.Errorf("field svc_type: %w", err)
		}
	}
	if val, ok := m["endpoint"]; ok {
		if svc.Endpoint, err = soroban.DecodeScString(val); err != nil {
			return nil, fmt.Errorf("field endpoint: %w", err)
		}
	}
	return svc, nil
}

func decodeCredentialRecord(v xdr.ScVal) (*models.CredentialStatusRecord, error) {
	m, err := soroban.DecodeScMap(v)
	if err != nil {
		return nil, fmt.Errorf("decode CredentialRecord: %w", err)
	}

	rec := &models.CredentialStatusRecord{}

	if val, ok := m["vc_hash"]; ok {
		hash, herr := soroban.DecodeScBytesN32(val)
		if herr != nil {
			return nil, fmt.Errorf("field vc_hash: %w", herr)
		}
		rec.VCHash = hex.EncodeToString(hash[:])
	}
	if val, ok := m["issuer_did"]; ok {
		if rec.IssuerDID, err = soroban.DecodeScString(val); err != nil {
			return nil, fmt.Errorf("field issuer_did: %w", err)
		}
	}
	if val, ok := m["subject_did"]; ok {
		if rec.SubjectDID, err = soroban.DecodeScString(val); err != nil {
			return nil, fmt.Errorf("field subject_did: %w", err)
		}
	}
	if val, ok := m["vc_type"]; ok {
		if rec.VCType, err = soroban.DecodeScString(val); err != nil {
			return nil, fmt.Errorf("field vc_type: %w", err)
		}
	}
	if val, ok := m["status"]; ok {
		if rec.Status, err = decodeEnumVariant(val); err != nil {
			return nil, fmt.Errorf("field status: %w", err)
		}
	}
	if val, ok := m["anchored_at"]; ok {
		ts, terr := soroban.DecodeScU64(val)
		if terr != nil {
			return nil, fmt.Errorf("field anchored_at: %w", terr)
		}
		rec.AnchoredAt = time.Unix(int64(ts), 0).UTC()
	}
	if val, ok := m["revoked_at"]; ok {
		opt, oerr := soroban.DecodeScOption(val)
		if oerr != nil {
			return nil, fmt.Errorf("field revoked_at: %w", oerr)
		}
		if opt != nil {
			ts, terr := soroban.DecodeScU64(*opt)
			if terr != nil {
				return nil, fmt.Errorf("field revoked_at inner: %w", terr)
			}
			t := time.Unix(int64(ts), 0).UTC()
			rec.RevokedAt = &t
		}
	}

	return rec, nil
}

func methodVariantToModel(variant string) models.DIDMethod {
	switch variant {
	case "Web":
		return models.DIDMethodWeb
	case "Key":
		return models.DIDMethodKey
	case "Ethr":
		return models.DIDMethodEthr
	case "Ebsi":
		return models.DIDMethodEBSI
	case "Soroban":
		return models.DIDMethodSoroban
	default:
		return models.DIDMethod(variant)
	}
}

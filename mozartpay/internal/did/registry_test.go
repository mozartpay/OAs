package did

import (
	"testing"
	"time"

	"github.com/ogtechnologies/mozartpay/internal/models"
)

func TestVCHashDeterministic(t *testing.T) {
	vc := &models.VerifiableCredential{
		Context: []string{"https://www.w3.org/ns/credentials/v2"},
		ID:      "urn:uuid:test-1234",
		Type:    []string{"VerifiableCredential", "NationalIdentityCredential"},
		Issuer:  "did:key:z6MkIssuer",
		CredentialSubject: map[string]interface{}{
			"id":      "did:key:z6MkSubject",
			"name":    "Alice",
			"country": "AT",
			"level":   "KYC_LEVEL_2",
		},
		IssuanceDate:   time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC),
		ExpirationDate: time.Date(2027, 1, 1, 0, 0, 0, 0, time.UTC),
		Proof: models.VCProof{
			Type:               models.ProofTypeEd25519,
			Created:            time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC),
			ProofPurpose:       "assertionMethod",
			VerificationMethod: "did:key:z6MkIssuer#keys-1",
			ProofValue:         "zABC123",
		},
	}

	h1, err := VCHash(vc)
	if err != nil {
		t.Fatalf("VCHash failed: %v", err)
	}
	h2, err := VCHash(vc)
	if err != nil {
		t.Fatalf("VCHash failed on second call: %v", err)
	}
	if h1 != h2 {
		t.Fatal("VCHash is not deterministic")
	}

	// Mutating a field must change the hash
	vc2 := *vc
	vc2.Issuer = "did:key:z6MkOther"
	h3, err := VCHash(&vc2)
	if err != nil {
		t.Fatalf("VCHash failed: %v", err)
	}
	if h1 == h3 {
		t.Fatal("VCHash did not change after mutation")
	}
}

func TestVCHashMapKeyOrderIndependent(t *testing.T) {
	// Two VCs with identical content but maps built in different order
	// must produce the same hash (Go sorts map keys in json.Marshal).
	subj1 := map[string]interface{}{"a": "1", "b": "2", "c": "3"}
	subj2 := map[string]interface{}{"c": "3", "b": "2", "a": "1"}

	vc1 := &models.VerifiableCredential{CredentialSubject: subj1}
	vc2 := &models.VerifiableCredential{CredentialSubject: subj2}

	h1, _ := VCHash(vc1)
	h2, _ := VCHash(vc2)
	if h1 != h2 {
		t.Fatal("VCHash differs for equivalent maps with different insertion order")
	}
}

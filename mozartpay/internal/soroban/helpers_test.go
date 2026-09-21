package soroban

import (
	"testing"

	"github.com/stellar/go/xdr"
)

func TestScvMapRoundTrip(t *testing.T) {
	m := ScvMap(map[string]xdr.ScVal{
		"did":         ScvString("did:key:z6MkTest"),
		"deactivated": ScvBool(false),
		"created_at":  ScvU64(1700000000),
	})

	if m.Type != xdr.ScValTypeScvMap {
		t.Fatalf("expected ScvMap, got %s", m.Type)
	}

	decoded, err := DecodeScMap(m)
	if err != nil {
		t.Fatalf("DecodeScMap failed: %v", err)
	}

	didStr, err := DecodeScString(decoded["did"])
	if err != nil || didStr != "did:key:z6MkTest" {
		t.Fatalf("did field: got %q, err %v", didStr, err)
	}

	ts, err := DecodeScU64(decoded["created_at"])
	if err != nil || ts != 1700000000 {
		t.Fatalf("created_at field: got %d, err %v", ts, err)
	}

	b, err := DecodeScBool(decoded["deactivated"])
	if err != nil || b {
		t.Fatalf("deactivated field: got %v, err %v", b, err)
	}
}

func TestScvMapSortedKeys(t *testing.T) {
	m := ScvMap(map[string]xdr.ScVal{
		"zebra": ScvU32(1),
		"alpha": ScvU32(2),
		"mango": ScvU32(3),
	})

	entries := **m.Map
	if len(entries) != 3 {
		t.Fatalf("expected 3 entries, got %d", len(entries))
	}
	// Keys must be sorted for deterministic XDR
	want := []string{"alpha", "mango", "zebra"}
	for i, e := range entries {
		got := string(*e.Key.Sym)
		if got != want[i] {
			t.Fatalf("entry %d: expected key %q, got %q", i, want[i], got)
		}
	}
}

func TestScvVecRoundTrip(t *testing.T) {
	v := ScvVec([]xdr.ScVal{ScvU32(1), ScvU32(2), ScvU32(3)})
	vec, err := DecodeScVec(v)
	if err != nil {
		t.Fatalf("DecodeScVec failed: %v", err)
	}
	if len(vec) != 3 {
		t.Fatalf("expected 3 elements, got %d", len(vec))
	}
}

func TestScvOption(t *testing.T) {
	// None encodes as ScvVoid
	none := ScvOption(nil)
	if none.Type != xdr.ScValTypeScvVoid {
		t.Fatalf("expected ScvVoid for None, got %s", none.Type)
	}
	decoded, err := DecodeScOption(none)
	if err != nil {
		t.Fatalf("DecodeScOption(None) failed: %v", err)
	}
	if decoded != nil {
		t.Fatal("expected nil for None option")
	}

	// Some encodes as the unwrapped inner value
	inner := ScvU64(42)
	some := ScvOption(&inner)
	if some.Type != xdr.ScValTypeScvU64 {
		t.Fatalf("expected ScvU64 for Some, got %s", some.Type)
	}
	decoded, err = DecodeScOption(some)
	if err != nil {
		t.Fatalf("DecodeScOption(Some) failed: %v", err)
	}
	if decoded == nil {
		t.Fatal("expected non-nil for Some option")
	}
	val, err := DecodeScU64(*decoded)
	if err != nil || val != 42 {
		t.Fatalf("expected 42, got %d, err %v", val, err)
	}
}

func TestScvBytesN32RoundTrip(t *testing.T) {
	var hash [32]byte
	for i := range hash {
		hash[i] = byte(i)
	}
	v := ScvBytesN32(hash)
	decoded, err := DecodeScBytesN32(v)
	if err != nil {
		t.Fatalf("DecodeScBytesN32 failed: %v", err)
	}
	if decoded != hash {
		t.Fatal("BytesN32 round trip mismatch")
	}
}

func TestDecodeScMapWrongType(t *testing.T) {
	_, err := DecodeScMap(ScvU32(5))
	if err == nil {
		t.Fatal("expected error for non-map ScVal")
	}
}

func TestDecodeScAddressAccount(t *testing.T) {
	addr, err := AccountToScAddress("GBRPYHIL2CI3FNQ4BXLFMNDLFJUNPU2HY3ZMFSHONUCEOASW7QC7OX2H")
	if err != nil {
		t.Fatalf("AccountToScAddress failed: %v", err)
	}
	v := ScvAddress(addr)
	decoded, err := DecodeScAddress(v)
	if err != nil {
		t.Fatalf("DecodeScAddress failed: %v", err)
	}
	if decoded != "GBRPYHIL2CI3FNQ4BXLFMNDLFJUNPU2HY3ZMFSHONUCEOASW7QC7OX2H" {
		t.Fatalf("address mismatch: %s", decoded)
	}
}

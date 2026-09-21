package soroban

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"math/big"
	"sort"

	"github.com/stellar/go/network"
	"github.com/stellar/go/strkey"
	"github.com/stellar/go/xdr"
)

const (
	TestnetRPCURL = "https://soroban-testnet.stellar.org"
	MainnetRPCURL = "https://soroban-mainnet.stellar.org"
)

func NetworkPassphrase(net string) string {
	switch net {
	case "stellar-mainnet":
		return network.PublicNetworkPassphrase
	default:
		return network.TestNetworkPassphrase
	}
}

func NetworkRPCURL(net string) string {
	switch net {
	case "stellar-mainnet":
		return MainnetRPCURL
	default:
		return TestnetRPCURL
	}
}

func ParseContractID(id string) (xdr.ScAddress, error) {
	decoded, err := strkey.Decode(strkey.VersionByteContract, id)
	if err != nil {
		return xdr.ScAddress{}, fmt.Errorf("invalid contract ID %q: %w", id, err)
	}
	if len(decoded) != 32 {
		return xdr.ScAddress{}, fmt.Errorf("contract ID must be 32 bytes, got %d", len(decoded))
	}
	var contractID xdr.ContractId
	copy(contractID[:], decoded)
	return xdr.ScAddress{
		Type:       xdr.ScAddressTypeScAddressTypeContract,
		ContractId: &contractID,
	}, nil
}

func EncodeContractID(addr xdr.ScAddress) (string, error) {
	if addr.Type != xdr.ScAddressTypeScAddressTypeContract || addr.ContractId == nil {
		return "", fmt.Errorf("not a contract address")
	}
	return strkey.Encode(strkey.VersionByteContract, addr.ContractId[:])
}

func AccountToScAddress(address string) (xdr.ScAddress, error) {
	accountID, err := xdr.AddressToAccountId(address)
	if err != nil {
		return xdr.ScAddress{}, fmt.Errorf("invalid account address %q: %w", address, err)
	}
	return xdr.ScAddress{
		Type:      xdr.ScAddressTypeScAddressTypeAccount,
		AccountId: &accountID,
	}, nil
}

func RandomSalt() ([32]byte, error) {
	var salt [32]byte
	_, err := rand.Read(salt[:])
	return salt, err
}

func ScvString(s string) xdr.ScVal {
	v, _ := xdr.NewScVal(xdr.ScValTypeScvString, xdr.ScString(s))
	return v
}

func ScvSymbol(s string) xdr.ScVal {
	v, _ := xdr.NewScVal(xdr.ScValTypeScvSymbol, xdr.ScSymbol(s))
	return v
}

func ScvBytes(b []byte) xdr.ScVal {
	v, _ := xdr.NewScVal(xdr.ScValTypeScvBytes, xdr.ScBytes(b))
	return v
}

func ScvBool(b bool) xdr.ScVal {
	v, _ := xdr.NewScVal(xdr.ScValTypeScvBool, b)
	return v
}

func ScvU32(n uint32) xdr.ScVal {
	v, _ := xdr.NewScVal(xdr.ScValTypeScvU32, xdr.Uint32(n))
	return v
}

func ScvU64(n uint64) xdr.ScVal {
	v, _ := xdr.NewScVal(xdr.ScValTypeScvU64, xdr.Uint64(n))
	return v
}

func ScvI32(n int32) xdr.ScVal {
	v, _ := xdr.NewScVal(xdr.ScValTypeScvI32, xdr.Int32(n))
	return v
}

func ScvI64(n int64) xdr.ScVal {
	v, _ := xdr.NewScVal(xdr.ScValTypeScvI64, xdr.Int64(n))
	return v
}

// ScvI128 builds an ScvI128 from a big.Int. Negative values are handled via
// two's complement (big.Int bitwise ops treat negatives as infinite two's
// complement).
func ScvI128(n *big.Int) xdr.ScVal {
	mask := new(big.Int).Sub(new(big.Int).Lsh(big.NewInt(1), 64), big.NewInt(1))
	lo := new(big.Int).And(n, mask)
	hi := new(big.Int).Rsh(new(big.Int).Set(n), 64)
	parts := xdr.Int128Parts{Hi: xdr.Int64(hi.Int64()), Lo: xdr.Uint64(lo.Uint64())}
	v, _ := xdr.NewScVal(xdr.ScValTypeScvI128, parts)
	return v
}

func ScvAddress(addr xdr.ScAddress) xdr.ScVal {
	v, _ := xdr.NewScVal(xdr.ScValTypeScvAddress, addr)
	return v
}

// ScvVec builds an ScvVec from a slice of ScVals.
func ScvVec(vals []xdr.ScVal) xdr.ScVal {
	vec := xdr.ScVec(vals)
	v, _ := xdr.NewScVal(xdr.ScValTypeScvVec, &vec)
	return v
}

// ScvMap builds an ScvMap from key/value pairs. Keys are encoded as symbols
// and sorted lexicographically for deterministic encoding.
func ScvMap(pairs map[string]xdr.ScVal) xdr.ScVal {
	keys := make([]string, 0, len(pairs))
	for k := range pairs {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	entries := make(xdr.ScMap, 0, len(pairs))
	for _, k := range keys {
		entries = append(entries, xdr.ScMapEntry{
			Key: ScvSymbol(k),
			Val: pairs[k],
		})
	}
	v, _ := xdr.NewScVal(xdr.ScValTypeScvMap, &entries)
	return v
}

// ScvMapEntry builds a single map entry with a symbol key.
func ScvMapEntry(key string, val xdr.ScVal) xdr.ScMapEntry {
	return xdr.ScMapEntry{Key: ScvSymbol(key), Val: val}
}

// ScvBytesN32 builds an ScvBytes from a fixed 32-byte array (BytesN<32>).
func ScvBytesN32(b [32]byte) xdr.ScVal {
	return ScvBytes(b[:])
}

// ScvOption encodes Option<T> per soroban-sdk convention: None = ScvVoid,
// Some(v) = the inner ScVal directly (unwrapped).
func ScvOption(v *xdr.ScVal) xdr.ScVal {
	if v == nil {
		return ScvVoid()
	}
	return *v
}

// ScvVoid builds a void ScVal (unit type).
func ScvVoid() xdr.ScVal {
	v, _ := xdr.NewScVal(xdr.ScValTypeScvVoid, nil)
	return v
}

// DecodeScMap extracts a symbol-keyed map from an ScVal of type ScvMap.
func DecodeScMap(v xdr.ScVal) (map[string]xdr.ScVal, error) {
	if v.Type != xdr.ScValTypeScvMap || v.Map == nil {
		return nil, fmt.Errorf("expected ScvMap, got %s", v.Type.String())
	}
	out := make(map[string]xdr.ScVal, len(**v.Map))
	for _, entry := range **v.Map {
		if entry.Key.Type != xdr.ScValTypeScvSymbol || entry.Key.Sym == nil {
			return nil, fmt.Errorf("map key is not a symbol: %s", entry.Key.Type.String())
		}
		out[string(*entry.Key.Sym)] = entry.Val
	}
	return out, nil
}

// DecodeScVec extracts the elements of an ScVal of type ScvVec.
func DecodeScVec(v xdr.ScVal) ([]xdr.ScVal, error) {
	if v.Type != xdr.ScValTypeScvVec || v.Vec == nil {
		return nil, fmt.Errorf("expected ScvVec, got %s", v.Type.String())
	}
	return []xdr.ScVal(**v.Vec), nil
}

// DecodeScString extracts a string from an ScvString or ScvSymbol ScVal.
func DecodeScString(v xdr.ScVal) (string, error) {
	switch v.Type {
	case xdr.ScValTypeScvString:
		if v.Str == nil {
			return "", fmt.Errorf("nil string value")
		}
		return string(*v.Str), nil
	case xdr.ScValTypeScvSymbol:
		if v.Sym == nil {
			return "", fmt.Errorf("nil symbol value")
		}
		return string(*v.Sym), nil
	default:
		return "", fmt.Errorf("expected ScvString or ScvSymbol, got %s", v.Type.String())
	}
}

// DecodeScAddress extracts an address string (G... or C...) from an ScvAddress ScVal.
func DecodeScAddress(v xdr.ScVal) (string, error) {
	if v.Type != xdr.ScValTypeScvAddress || v.Address == nil {
		return "", fmt.Errorf("expected ScvAddress, got %s", v.Type.String())
	}
	switch v.Address.Type {
	case xdr.ScAddressTypeScAddressTypeAccount:
		if v.Address.AccountId == nil {
			return "", fmt.Errorf("nil account ID")
		}
		return v.Address.AccountId.Address(), nil
	case xdr.ScAddressTypeScAddressTypeContract:
		if v.Address.ContractId == nil {
			return "", fmt.Errorf("nil contract ID")
		}
		return strkey.Encode(strkey.VersionByteContract, v.Address.ContractId[:])
	default:
		return "", fmt.Errorf("unsupported address type %s", v.Address.Type.String())
	}
}

// DecodeScU64 extracts a uint64 from an ScvU64 ScVal.
func DecodeScU64(v xdr.ScVal) (uint64, error) {
	if v.Type != xdr.ScValTypeScvU64 || v.U64 == nil {
		return 0, fmt.Errorf("expected ScvU64, got %s", v.Type.String())
	}
	return uint64(*v.U64), nil
}

// DecodeScBool extracts a bool from an ScvBool ScVal.
func DecodeScBool(v xdr.ScVal) (bool, error) {
	if v.Type != xdr.ScValTypeScvBool || v.B == nil {
		return false, fmt.Errorf("expected ScvBool, got %s", v.Type.String())
	}
	return *v.B, nil
}

// DecodeScBytesN32 extracts a [32]byte from an ScvBytes ScVal.
func DecodeScBytesN32(v xdr.ScVal) ([32]byte, error) {
	var out [32]byte
	if v.Type != xdr.ScValTypeScvBytes || v.Bytes == nil {
		return out, fmt.Errorf("expected ScvBytes, got %s", v.Type.String())
	}
	b := []byte(*v.Bytes)
	if len(b) != 32 {
		return out, fmt.Errorf("expected 32 bytes, got %d", len(b))
	}
	copy(out[:], b)
	return out, nil
}

// DecodeScOption decodes an Option<T> ScVal per soroban-sdk convention:
// ScvVoid = None (returns nil), anything else = Some(v) (returns the value).
func DecodeScOption(v xdr.ScVal) (*xdr.ScVal, error) {
	if v.Type == xdr.ScValTypeScvVoid {
		return nil, nil
	}
	return &v, nil
}

// ParseBytesN32Hex parses a 64-char hex string into a [32]byte (e.g. an
// agreement ID or hash flag).
func ParseBytesN32Hex(s string) ([32]byte, error) {
	var out [32]byte
	b, err := hex.DecodeString(s)
	if err != nil {
		return out, fmt.Errorf("invalid hex: %w", err)
	}
	if len(b) != 32 {
		return out, fmt.Errorf("expected 32 bytes, got %d", len(b))
	}
	copy(out[:], b)
	return out, nil
}

// DecodeBytesN32Result decodes a base64 ScVal return value (BytesN<32>) into
// a hex string — e.g. the agreement ID returned by create_agreement.
func DecodeBytesN32Result(resultXDR string) (string, error) {
	var v xdr.ScVal
	if err := xdr.SafeUnmarshalBase64(resultXDR, &v); err != nil {
		return "", fmt.Errorf("parse result XDR: %w", err)
	}
	b, err := DecodeScBytesN32(v)
	if err != nil {
		return "", err
	}
	return hex.EncodeToString(b[:]), nil
}

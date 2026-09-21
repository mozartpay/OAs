# DID Registry — On-Chain DID Resolution & Credential Status

A standalone Soroban smart contract that provides **on-chain DID resolution** and **verifiable credential status tracking** for the Stellar ecosystem. Designed as a reusable public good — any Stellar address can register DIDs, and any party can resolve them without trusting an off-chain resolver.

## Architecture

```
┌─────────────┐     ┌──────────────────┐     ┌─────────────────────┐
│  Go CLI     │────▶│  internal/did/   │────▶│  DID Registry       │
│  did *      │     │  registry.go     │     │  Contract (Soroban) │
└─────────────┘     └──────────────────┘     └─────────────────────┘
                           │                          │
                     internal/soroban            Persistent storage:
                     (pure-Go RPC client)        - DID → DIDDocumentData
                                                 - VC hash → CredentialRecord
                                                 - Controller → Vec<DID>
```

### Contract: `contracts/did_registry/`

Separate crate producing `did_registry.wasm`. Deployed as its own contract instance, independent of the Orchestrated Agreement contract.

**Types**

| Type | Kind | Fields |
|------|------|--------|
| `DIDMethod` | enum | `Web`, `Key`, `Ethr`, `Ebsi`, `Soroban` |
| `VerificationMethodEntry` | struct | `id`, `vm_type`, `controller`, `public_key_multibase` |
| `ServiceEntry` | struct | `id`, `svc_type`, `endpoint` |
| `DIDDocumentData` | struct | `did`, `method`, `controller`, `verification_methods`, `services`, `created_at`, `updated_at`, `deactivated` |
| `CredentialStatus` | enum | `Valid`, `Revoked` |
| `CredentialRecord` | struct | `vc_hash`, `issuer_did`, `subject_did`, `vc_type`, `status`, `anchored_at`, `revoked_at` |

**Methods**

| Method | Auth | Description |
|--------|------|-------------|
| `register_did` | controller | Store full DID document on-chain |
| `resolve_did` | none | Return DID document (panics if not found / deactivated) |
| `update_did` | controller | Rotate verification methods and services |
| `deactivate_did` | controller | Tombstone a DID |
| `did_exists` | none | Check registration status |
| `get_controller_dids` | none | List DIDs for a controller address |
| `get_did_count` | none | Total registered DIDs |
| `anchor_credential` | issuer controller | Record VC hash with `Valid` status |
| `revoke_credential` | issuer controller | Set status to `Revoked` |
| `credential_status` | none | Query credential record |

**Events**: `did_registered`, `did_updated`, `did_deactivated`, `vc_anchored`, `vc_revoked`

## CLI Commands

```bash
# Configure the registry contract
mozartpay did set-registry --id C...

# Register a DID document on-chain
mozartpay did register                          # uses saved DID state
mozartpay did register --doc-file my-did.json   # from file
mozartpay did register --controller G...        # explicit controller

# Resolve a DID (read-only, no transaction)
mozartpay did resolve                           # active DID
mozartpay did resolve --did did:key:z6Mk...     # specific DID
mozartpay did resolve --output json             # JSON output

# Deactivate a DID
mozartpay did deactivate --did did:key:z6Mk...

# Anchor a credential hash on-chain
mozartpay did anchor                            # uses saved vc_latest
mozartpay did anchor --vc-file cred.json        # from file
mozartpay did attest --anchor                   # issue + anchor in one step

# Check credential status (read-only)
mozartpay did status                            # hash of saved vc_latest
mozartpay did status --vc-hash <hex>            # explicit hash

# Revoke a credential
mozartpay did revoke --vc-hash <hex>

# Verify a VC (crypto proof + on-chain revocation check)
mozartpay did verify
```

## VC Hash Canonicalization

`did.VCHash()` computes SHA-256 over `json.Marshal(vc)`. Go's `encoding/json` produces deterministic output: struct fields serialize in declaration order and map keys sort lexicographically. This means the same VC always produces the same hash — critical for on-chain anchoring and revocation lookups.

## XDR Encoding Notes

Soroban contract types map to `xdr.ScVal` as follows:

- **Structs** → `ScvMap` with symbol keys matching Rust field names (snake_case), sorted lexicographically
- **Unit enums** → `ScvVec` containing a single `ScvSymbol` with the variant name (e.g., `Vec[Symbol("Key")]` for `DIDMethod::Key`)
- **`Option<T>`** → `ScvVec`: empty = `None`, single-element = `Some(v)`
- **`BytesN<32>`** → `ScvBytes` (32-byte value)
- **`String`** → `ScvString`, **`Symbol`** → `ScvSymbol`, **`u64`** → `ScvU64`, **`bool`** → `ScvBool`, **`Address`** → `ScvAddress`

The Go helpers in `internal/soroban/helpers.go` handle both directions: `ScvMap`/`ScvVec`/`ScvOption`/`ScvBytesN32` for encoding, `DecodeScMap`/`DecodeScVec`/`DecodeScString`/`DecodeScAddress`/`DecodeScU64`/`DecodeScBool`/`DecodeScBytesN32`/`DecodeScOption` for decoding.

## Deployment

```bash
# Build the contract
cd contracts && make did-registry

# Deploy to testnet
mozartpay contract deploy --wasm dist/did_registry.wasm --network stellar-testnet

# Configure the CLI
mozartpay did set-registry --id <contract-id-from-deploy>
```

## Testnet Walkthrough

```bash
# 1. Create a DID
mozartpay did create --method key

# 2. Register it on-chain
mozartpay did register

# 3. Resolve it back
mozartpay did resolve

# 4. Issue a VC and anchor it
mozartpay did attest --name "Alice" --anchor

# 5. Check credential status
mozartpay did status

# 6. Verify (crypto + on-chain revocation)
mozartpay did verify

# 7. Revoke
mozartpay did revoke

# 8. Verify again — shows REVOKED
mozartpay did verify
```

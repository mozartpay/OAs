# Hyperledger Fabric Integration — Implementation Plan

Integration of Hyperledger Fabric into the MozartPay CLI via the **Fabric Gateway client API for Go** (`github.com/hyperledger/fabric-gateway`). Fabric provides a permissioned ledger layer alongside the existing Stellar/Soroban rails — suited to regulated payment flows, private data collections, and X.509/MSP-based identity.

> **Note:** `fabric-sdk-go` is deprecated (Fabric v2.5+). All client work uses `fabric-gateway`, which requires Fabric v2.4+ peers with the gateway service enabled.

---

## Goals

- Submit and evaluate chaincode transactions on a Fabric channel from the CLI
- Manage Fabric identity (X.509 cert + ECDSA key) via config, with pluggable signing
- Stream chaincode events with checkpoint resume
- Follow existing codebase conventions: `Command` struct pattern, `internal/ui` output, `~/.mozartpay/` config/state

## Non-Goals

- Fabric network provisioning (use `fabric-samples` test-network or an existing network)
- Chaincode authoring (out of scope; the CLI must work with any installed chaincode)
- Replacing Stellar/Soroban rails — Fabric is an additional rail

---

## Architecture

```
cmd/mozartpay/commands/fabric.go     → CLI command group (fabric configure|status|query|submit|events)
internal/fabric/client.go            → gRPC connection + Gateway lifecycle
internal/fabric/identity.go          → X.509 identity + ECDSA signing (pluggable)
internal/fabric/contract.go          → generic submit/evaluate wrappers
internal/config/config.go            → FabricConfig on Config
~/.mozartpay/fabric/                 → PEM credentials (0600)
~/.mozartpay/state/fabric_*.json     → event checkpoints, last tx
```

### Dependency

```bash
cd mozartpay && go get github.com/hyperledger/fabric-gateway@latest
```

Pulls in `google.golang.org/grpc` and `github.com/hyperledger/fabric-protos-go-apiv2`. No other external deps — identity handling uses stdlib `crypto/x509` + `crypto/ecdsa`.

---

## Phase 1 — Client & Identity (`internal/fabric/`)

### `client.go`

- `type Client struct { conn *grpc.ClientConn; gw *client.Gateway }`
- `NewClient(cfg config.FabricConfig) (*Client, error)`:
  - Load peer TLS cert → `credentials.NewClientTLSFromFile(tlsCertPath, "")`
  - `grpc.NewClient(endpoint, grpc.WithTransportCredentials(creds))`
  - Build `client.Gateway` via `client.Connect(id, client.WithClientConnection(conn), client.WithSign(sign), client.WithHash(hash.SHA256))`
  - Default timeouts via `client.WithEvaluateTimeout(30s)`, `WithEndorseTimeout(30s)`, `WithSubmitTimeout(2m)`, `WithCommitStatusTimeout(1m)`
- `Close()` — close the gRPC conn
- `Contract(channel, chaincode) *client.Contract`

### `identity.go`

- `LoadIdentity(cfg config.FabricConfig) (identity.Identity, identity.Sign, error)`:
  - Read cert PEM → `x509.ParseCertificate` → `identity.NewX509Identity(mspID, cert)`
  - Read key PEM → `x509.ParsePKCS8PrivateKey` (fallback `ParseECPrivateKey`) → `identity.NewPrivateKeySign(key)`
- Signing is a function value (`identity.Sign`) so HSM/PKCS11 or `internal/wallet` delegation can be swapped in later without touching call sites.

### `contract.go`

- `Submit(ctx, channel, chaincode, fn string, args ...string) (result []byte, txID string, err error)` — `contract.SubmitTransaction`; capture `transactionID` from the submitted transaction
- `Evaluate(ctx, channel, chaincode, fn string, args ...string) ([]byte, error)` — `contract.EvaluateTransaction`
- `SubmitAsync` variant returning `*client.Commit` for callers that want to check commit status separately
- Keep fully generic — no MozartPay-specific chaincode assumptions (same rule as `contract deploy` supporting any `.wasm`)

### Error handling

Distinguish and surface:

- `*client.EndorseError` — endorsement phase failure
- `*client.SubmitError` — submit phase failure
- `*client.CommitStatusError` — commit status failure
- `*client.CommitError` — chaincode returned non-OK validation code (expose `TransactionCode`)

These distinctions matter for compliance reporting (`internal/reporting`).

---

## Phase 2 — Config (`internal/config/config.go`)

```go
type FabricConfig struct {
    Enabled      bool   `json:"fabricEnabled"`
    PeerEndpoint string `json:"fabricPeerEndpoint"`   // e.g. localhost:7051
    TLSCertPath  string `json:"fabricTLSCertPath"`    // peer TLS CA cert PEM
    MSPID        string `json:"fabricMspId"`          // e.g. Org1MSP
    CertPath     string `json:"fabricCertPath"`       // user X.509 signing cert PEM
    KeyPath      string `json:"fabricKeyPath"`        // user ECDSA private key PEM
    Channel      string `json:"fabricChannel"`        // default channel
    Chaincode    string `json:"fabricChaincode"`      // default chaincode
}
```

- Add `Fabric FabricConfig` field to `Config`
- Credential files live under `~/.mozartpay/fabric/` with `0600` perms
- No private key material in `config.json` — paths only

---

## Phase 3 — CLI commands (`cmd/mozartpay/commands/fabric.go`)

New `newFabricCmd(cfg)` registered in `NewRootCmd()` under a new **"Fabric"** help group.

| Command | Description |
|---------|-------------|
| `fabric configure` | Set endpoint, MSP ID, cert/key paths, default channel/chaincode; persist via `config.Save` |
| `fabric status` | Connectivity check — dial gateway peer, report TLS + identity validity |
| `fabric query --fn <name> [--args a,b,c] [--channel] [--chaincode]` | `Evaluate` — read-only, no commit |
| `fabric submit --fn <name> [--args a,b,c] [--channel] [--chaincode]` | `Submit` — write, print tx ID + commit status |
| `fabric events [--chaincode] [--start-block N]` | Stream chaincode events; checkpoint to `~/.mozartpay/state/fabric_checkpoint.json` for resume |

Conventions:

- All output via `internal/ui` (`ui.Header`, `ui.KV`, `ui.NewSpinner`, `ui.Success/Error/Warn`) — no raw `fmt.Println` except `--output json`
- Lazy connection: dial inside each `Run` func, `defer client.Close()` — same pattern as `did.NewRegistryClient` + `reg.Close()`
- `context.WithTimeout` per call: 30s evaluate, 2m submit (mirrors `did.go`)
- `--output pretty|json` flag on query/submit/status

---

## Phase 4 — Events & Checkpointing

- `contract.Events(ctx, client.WithStartBlock(n))` → iterate `ChaincodeEvent`s
- Persist last-seen block number + tx ID via `config.SaveState("fabric_checkpoint", ...)`
- On reconnect, resume from checkpoint; surface event errors to the caller (gateway API does not auto-reconnect — client must re-issue the events request)

---

## Phase 5 — MCP exposure (optional, follow-up)

Add `fabric_query` / `fabric_submit` tools in `internal/mcp/tools.go` + handlers in `internal/mcp/handlers.go`. Trivial once `internal/fabric` exists — handlers call the same package.

---

## Testing

### Unit tests

- `internal/fabric/identity_test.go` — generate test ECDSA key + self-signed X.509 cert, verify `LoadIdentity` round-trip and sign/verify
- `internal/fabric/contract_test.go` — mock `client.Contract` interface where feasible

### Integration testing (manual, documented)

```bash
# fabric-samples test network
cd fabric-samples/test-network
./network.sh up createChannel -c mychannel
./network.sh deployCC -ccn basic -ccp ../asset-transfer-basic/chaincode-go -ccl go

# CLI
mozartpay fabric configure --endpoint localhost:7051 --msp Org1MSP \
  --tls-cert $PWD/organizations/peerOrganizations/org1.example.com/peers/peer0.org1.example.com/tls/ca.crt \
  --cert $PWD/organizations/peerOrganizations/org1.example.com/users/User1@org1.example.com/msp/signcerts/cert.pem \
  --key  $PWD/organizations/peerOrganizations/org1.example.com/users/User1@org1.example.com/msp/keystore/*_sk \
  --channel mychannel --chaincode basic

mozartpay fabric status
mozartpay fabric submit --fn InitLedger
mozartpay fabric query  --fn GetAllAssets
mozartpay fabric submit --fn CreateAsset --args asset13,blue,5,tom,35
mozartpay fabric query  --fn ReadAsset --args asset13
mozartpay fabric events
```

### Lint

- `go vet ./...` and `golangci-lint run` must pass (`.golangci.yml`: errcheck, govet+shadow, staticcheck, ineffassign, unused, misspell; gci/gofmt)

---

## Implementation order

1. `internal/fabric/identity.go` + `client.go` + `FabricConfig`
2. `fabric configure` + `fabric status` — verify connectivity end-to-end
3. `fabric query` — read path (simplest)
4. `fabric submit` — write path with commit status + tx ID
5. `fabric events` + checkpointing
6. (Optional) MCP tools

## Risks / notes

- **Go version**: `fabric-gateway` v1.11+ requires Go 1.25+ to build from source; project targets Go 1.24 — verify toolchain compatibility or pin an earlier release.
- **Private key handling**: PEM key path in config, `0600` perms, never logged. Consider `ui.Prompt` confirmation before first `submit`.
- **No shell-out**: all interaction via `fabric-gateway` gRPC — consistent with the "no stellar CLI shell-out" rule for Soroban.

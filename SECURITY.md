# Security Policy

## Supported Versions

| Version | Supported |
|---------|-----------|
| 0.2.x (latest) | ✅ |
| < 0.2.0 | ❌ |

## Reporting a Vulnerability

If you discover a security vulnerability in MozartPay CLI, please report it responsibly:

1. **Do not** open a public GitHub issue
2. Email **hi@ogtechnologies.co** with a description of the vulnerability, reproduction steps, and potential impact
3. Include the relevant component (CLI, Soroban client, MCP server, WebAuthn ceremony, Docker, k8s)
4. You will receive an acknowledgment within 48 hours
5. A fix or mitigation will be prioritized based on severity

We follow responsible disclosure. Security researchers will be credited (with permission) in release notes.

---

## Stellar Key Management

### Local Storage

- Private keys (Stellar seeds, `S...`) are stored locally in `~/.mozartpay/config.json`
- Keys are **never** transmitted to external services unless explicitly signing a transaction
- The `keypair.Full` type is used for in-memory signing — private key material is never logged or printed
- Config file permissions should be restricted to the owner (`chmod 600 ~/.mozartpay/config.json`)

### Wallet Operations

- `wallet.ResolveSeed(account)` resolves the seed for signing (plaintext, or an OS-keychain unlock grant for passkey-protected wallets) — the seed is not exposed beyond this point
- Testnet faucet funding (`mozartpay wallet fund`) only works on `stellar-testnet`
- Mainnet operations require an explicitly connected wallet with a funded account
- The active wallet address (`G...`) is safe to display; the private seed (`S...`) is never shown

### Soroban Transaction Security

- All Soroban transactions are **simulated before submission** — the simulation response provides `SorobanTransactionData` and auth entries that are attached to the final transaction
- Transactions are signed with the network passphrase (`network.TestNetworkPassphrase` or `network.PublicNetworkPassphrase`) to prevent cross-network replay attacks
- `submitAndWait()` polls the RPC until the transaction is confirmed on-chain (`SUCCESS` or `FAILED`)
- Default transaction base fee: 100,000 stroops
- Default transaction timeout: 300 seconds
- Default poll interval: 3 seconds

### Simulate-Only (Read-Only) Queries

- `SimulateOnly()` uses a randomly generated dummy keypair (`keypair.Random()`) — no real private keys are needed
- The dummy address is only used to satisfy the transaction builder's source account requirement
- No transaction is submitted to the network — the simulation response is returned directly
- This is safe for read-only contract queries like `get_agreement`, `get_initiator_agreements`, `owner`, `is_paused`

---

## Passkey Protection (WebAuthn / FIDO2)

### Seed Encryption at Rest

- `mozartpay wallet passkey register` encrypts the Stellar seed with **AES-256-GCM** and stores only the ciphertext (`privateKeyEnc`) in the wallet file — the plaintext seed is removed
- The encryption key is a random 32-byte value stored in the **OS keychain** (macOS Keychain, Windows Credential Manager, Linux Secret Service) under the `mozartpay` service
- Passkey credentials are stored at `~/.mozartpay/state/passkeys/<address>.json` with `0600` permissions

### Verified Local Ceremonies

- Registration and authentication run real WebAuthn ceremonies via `github.com/go-webauthn/webauthn` on an **ephemeral loopback server** (`localhost:0`) — no external server or third-party service
- Ceremony endpoints are protected by a random session token and time out after ~2 minutes
- User verification is **required** (biometric/PIN); attestation is not requested (`PreferNoAttestation`)

### Unlock Grants

- A successful assertion writes a **time-limited unlock grant** (default 15 minutes) to the OS keychain — signing works only while a grant is live
- `mozartpay wallet passkey lock` drops the grant immediately; grants are deleted on expiry
- All signing paths funnel through `wallet.ResolveSeed` — a locked protected wallet fails with an "unlock first" error instead of signing
- `mozartpay wallet passkey disable` requires a fresh assertion, restores the plaintext seed, and deletes all passkey state

---

## Docker & Kubernetes Security

### Docker

- **Multi-stage builds** — Build stage uses `golang:1.24-alpine`, final images use `alpine:latest` (minimal attack surface)
- **CGO disabled** — `CGO_ENABLED=0` ensures static binaries with no C dependency vulnerabilities
- **Health checks** — All services include health check endpoints
- **Network isolation** — Docker Compose uses a dedicated `mozartpay` bridge network
- **No secrets in images** — Secrets are passed via environment variables at runtime, never baked into images
- **Non-root containers** — Alpine images run with default non-root user where possible

### Kubernetes

- **Namespace isolation** — Dedicated `mozartpay` namespace (`k8s/00-namespace.yaml`)
- **Resource limits** — CPU and memory limits configured for all deployments
- **Health probes** — Liveness and readiness probes on all deployments
- **Network policies** — Network policies restrict pod-to-pod communication (`k8s/06-networking.yaml`)
- **Monitoring** — Prometheus and Grafana for observability (`k8s/07-monitoring.yaml`)
- **Secrets** — Kubernetes secrets for sensitive data (database credentials, API keys)

### Services

| Service | Port | Purpose |
|---------|------|---------|
| MCP | 3000 | AI assistant integration (SSE) |
| PostgreSQL | 5432 | Database (internal only) |
| Redis | 6379 | Cache (internal only) |

---

## Supply Chain Security

### Go Modules

- All dependencies are pinned in `go.sum` with cryptographic hashes
- `go mod tidy` ensures minimal dependency tree
- Primary external dependency: `github.com/stellar/go` (Stellar Go SDK with Soroban RPC support)
- Direct dependencies are minimal: `charmbracelet` (TUI), `go-chi` (HTTP), `stellar/go` (Stellar), `prometheus` (metrics), `google/uuid`, `mattn/go-sqlite3`

### Dependency Scanning

```bash
make security    # Run gosec security scanner
make lint        # Run golangci-lint (includes staticcheck)
go list -m all   # List all dependencies
```

### Vulnerability Checking

```bash
govulncheck ./...  # Run Go vulnerability checker (if installed)
```

---

## Security Tooling

### golangci-lint

Configured in `.golangci.yml` with the following enabled linters:
- `errcheck` — Check for unchecked errors
- `govet` — Go vet checks (including shadow detection)
- `staticcheck` — Static analysis
- `ineffassign` — Ineffective assignments
- `unused` — Unused code detection
- `misspell` — Spelling mistakes

Formatters:
- `gci` — Import ordering
- `gofmt` — Code formatting (simplify enabled)

### gosec

```bash
make security    # Run gosec security scanner
```

gosec scans for:
- Hardcoded credentials
- SQL injection
- Weak crypto
- Command injection
- File path traversal
- Other common Go security issues

---

## Best Practices for Contributors

### Key & Secret Handling

- **Never** hardcode private keys, API keys, or secrets in source code
- **Never** log or print private key material (`S...` seeds)
- Use environment variables for secrets in Docker/k8s deployments
- Validate all user inputs (Stellar addresses, amounts, contract IDs)

### Soroban Transactions

- **Always** simulate before submitting — never skip the simulation step
- Use `SimulateOnly()` for read-only queries (no transaction submission needed)
- Use `Invoke()` for write operations (simulates, assembles, signs, submits, polls)
- Extract return values from `SimulateTransactionResponse.Results[0].ReturnValueXDR`
- Use `HostFunctionTypeCreateContractV2` when constructor args are needed

### Code Quality

- Use `internal/ui` package for terminal output (avoids leaking sensitive data to stdout)
- Follow the `Command` struct pattern for all new CLI commands
- Use `soroban.Scv*` helper functions for `xdr.ScVal` construction
- Run `make lint` and `make security` before submitting changes
- Test on testnet before mainnet

### Input Validation

- Validate Stellar addresses using `xdr.AddressToAccountId()` or `strkey.Decode()`
- Validate contract IDs using `strkey.Decode(strkey.VersionByteContract, ...)`
- Validate amounts and timestamps as positive integers
- Sanitize all user-provided strings before use in transactions

---

## Data Protection & Privacy

OG Technologies EU is based in Vienna, Austria. The CLI is self-hosted and sends no telemetry;
operators are the controllers of any personal data they process. Because Stellar and Soroban
ledgers are immutable, **never write raw PII on-chain** — store hashes or DIDs on-chain and
keep personal data off-chain. Data subject requests and breach notifications:
**hi@ogtechnologies.co** (GDPR Art. 33 — 72-hour authority notification).

---

## Incident Response

If a security incident occurs:

1. **Assess** — Determine the scope and severity of the incident
2. **Contain** — Rotate affected keys, revoke compromised credentials
3. **Notify** — Email hi@ogtechnologies.co with incident details
4. **Fix** — Patch the vulnerability and release a new version
5. **Review** — Post-mortem analysis to prevent recurrence

### Key Rotation

If a Stellar private key is compromised:
1. Create a new wallet: `mozartpay wallet connect --provider stellar --network stellar-testnet`
2. Fund the new account: `mozartpay wallet fund --network stellar-testnet`
3. Transfer assets from the compromised account to the new account
4. Remove the compromised key from `~/.mozartpay/config.json`

---

*Security is a shared responsibility. Report issues responsibly and help keep MozartPay secure.*

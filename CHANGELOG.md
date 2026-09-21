# Changelog

All notable changes to MozartPay CLI are documented here.

The format follows [Keep a Changelog](https://keepachangelog.com/en/1.1.0/),
and versioning follows [Semantic Versioning](https://semver.org/).

## [Unreleased]

## [0.2.0] - 2026-09-20

### Added

- `vc-api` command: W3C VC API server (`/credentials/issue`, `/credentials/verify`) for conformance testing
- `integrations tansu`: Tansu project governance and versioning queries on Stellar (16 subcommands)
- `integrations news` / `integrations finnhub`: market data integrations (Finnhub, Alpha Vantage)
- `swap triangular`: 3-leg arbitrage scanner, monitor, and backtester
- `chat train-data` / `chat finetune` / `chat model`: local LLM fine-tuning pipeline (GGUF, Alpaca/ChatML/Llama2 formats)
- `internal/zk`: Noir-based zero-knowledge proof generation and verification
- ISO 20022 pacs.002, pacs.004, and pacs.009 message support (alongside pacs.008)
- DID registry Soroban contract (`contracts/did_registry`)
- MCP skills knowledge packs (`internal/mcp/skills/`)
- Market scanner (`internal/scanner`) and triangular arbitrage engine (`internal/triangular`)

## [0.1.0-mvp]

### Added

- Initial MVP: DID attestation (web/key/ethr/ebsi) with National ID VCs
- Wallet management (wwWallet, Stellar native, EOA, testnet faucet)
- Payments via x402, Tempo FX, and direct Stellar rails
- SEP-41 fungible token and non-fungible asset creation
- Pure-Go Soroban RPC client (deploy, invoke, simulate)
- MCP server for AI assistant integration (stdio/SSE)
- WebAuthn/FIDO2 passkey authentication server
- Compliance reporting with ISO 20022 pacs.008 XML export
- OA Score, StellarCarbon, x402, and Tempo integrations

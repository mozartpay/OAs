# Makefile for MozartPay Soroban Smart Contracts
# Compiles Rust contracts to WASM for Stellar deployment

.PHONY: all build check clean test deploy-local

# Default target
all: check build

# Check Rust toolchain and dependencies
check:
	@echo "Checking Rust toolchain..."
	@rustup target list --installed | grep -q wasm32v1-none || (echo "Installing wasm32v1-none target..." && rustup target add wasm32v1-none)
	@echo "Toolchain ready ✓"

# Build all contracts to WASM
build:
	@echo "Building contracts to WASM..."
	@# did-registry must build first: mozartpay-contracts imports its spec via
	@# contractimport!(file = "dist/did_registry.wasm"), which reads the file
	@# at macro-expansion time.
	@cargo build -p did-registry --release --target wasm32v1-none
	@mkdir -p dist
	@cp target/wasm32v1-none/release/did_registry.wasm dist/
	@cargo build -p mozartpay-contracts --release --target wasm32v1-none
	@cp target/wasm32v1-none/release/*.wasm dist/ 2>/dev/null || true
	@echo ""
	@echo "Build complete! WASM files:"
	@ls -la dist/*.wasm 2>/dev/null || echo "No WASM files found"

# Build individual contracts
orchestrated-agreement:
	@cargo build -p mozartpay-contracts --release --target wasm32v1-none

did-registry:
	@cargo build -p did-registry --release --target wasm32v1-none

# Clean build artifacts
clean:
	@cargo clean
	@rm -rf dist/
	@echo "Build artifacts cleaned"

# Run contract tests
test:
	@cargo test --workspace --lib

# Format code
fmt:
	@cargo fmt

# Check code without building
check-only:
	@cargo check --target wasm32v1-none

# Show contract sizes (for optimization review)
sizes: build
	@echo ""
	@echo "Contract WASM Sizes:"
	@ls -lh dist/*.wasm | awk '{print $$9, "->", $$5}'

# Local deployment info (manual step reference)
deploy-info:
	@echo ""
	@echo "=== Deployment Commands ==="
	@echo ""
	@echo "1. Install Stellar CLI:"
	@echo "   cargo install stellar-cli --locked"
	@echo ""
	@echo "2. Configure testnet:"
	@echo "   stellar network add testnet --rpc-url https://soroban-testnet.stellar.org --network-passphrase 'Test SDF Network ; September 2015'"
	@echo ""
	@echo "3. Deploy Orchestrated Agreement:"
	@echo "   mozartpay contract deploy --wasm dist/mozartpay_contracts.wasm --network stellar-testnet"
	@echo ""
	@echo "4. Deploy DID Registry:"
	@echo "   mozartpay contract deploy --wasm dist/did_registry.wasm --network stellar-testnet"
	@echo "   mozartpay did set-registry --id <contract-id>"
	@echo ""

# Help
help:
	@echo "Available targets:"
	@echo "  make build       - Build all contracts to WASM"
	@echo "  make orchestrated-agreement - Build only OA contract"
	@echo "  make did-registry - Build only DID registry contract"
	@echo "  make clean       - Clean build artifacts"
	@echo "  make test        - Run contract tests"
	@echo "  make sizes       - Show optimized WASM sizes"
	@echo "  make deploy-info - Show deployment instructions"

# MozartPay Conformance Test Vectors

Test vector suite for MozartPay DLT retail payment conformance validation, aligned with ISO 20022, W3C VC, and EBSI standards.

## Overview

This directory contains pre-generated test vectors for validating MozartPay CLI command outputs against expected schemas and conformance criteria. The test suite covers:

- **Semantic Mapping**: ISO 20022 ↔ DLT transaction mapping (tc001-tc004)
- **Compliance Fields**: KYC/AML, regulatory reporting (tc101-tc104)
- **VC Attributes**: W3C Verifiable Credential profiles (tc201-tc204)
- **Integration**: End-to-end payment flows (e2e001-e2e002)

## Directory Structure

```
test-vectors/
├── README.md                          # This file
├── Makefile                           # Test execution targets
├── schema/                            # JSON schemas for validation
│   ├── test-vector.schema.json        # Test configuration schema
│   └── results.schema.json            # Test results schema
├── semantic-mapping/                  # ISO 20022 ↔ DLT semantics
│   ├── tc001-p2p-stablecoin/         # P2P stablecoin payment
│   ├── tc002-cbdc-pos/               # CBDC point-of-sale
│   ├── tc003-merchant-settlement/    # Merchant settlement batch
│   └── tc004-cross-border/           # Cross-border FX payment
├── compliance-fields/                 # KYC/AML, regulatory reporting
│   ├── tc101-kyc-verified/           # KYC verification
│   ├── tc102-aml-threshold/          # AML threshold check
│   ├── tc103-travel-rule/            # Travel Rule compliance
│   └── tc104-consent-proof/          # Consent proof verification
├── vc-attributes/                    # W3C VC profile for retail payments
│   ├── tc201-national-id/            # National ID credential
│   ├── tc202-selective-disclosure/   # Selective disclosure
│   ├── tc203-kyc-attribute-set/      # KYC attribute set
│   └── tc204-consent-attribute/      # Consent attribute
├── integration/                      # End-to-end scenarios
│   ├── e2e001-full-payment-flow/     # Full payment flow
│   └── e2e002-dispute-handling/      # Dispute handling flow
├── runner/                           # Test harness
│   ├── run-tests.sh                  # Main test runner
│   └── compare-output.py             # Output comparison utility
└── results/                          # Generated test results
    └── .gitkeep
```

## Requirements

- **jq**: JSON processor (required by test runner)
- **python3**: Python 3.x
- **jsonschema**: Python package for JSON Schema validation
- **bc**: Arbitrary precision calculator

### Installation

```bash
# Install jq (macOS)
brew install jq

# Install jq (Ubuntu/Debian)
sudo apt-get install jq

# Install jsonschema Python package
pip3 install jsonschema
```

## Usage

### Running All Tests

```bash
cd test-vectors
make test
```

### Running Specific Test Categories

```bash
# Semantic mapping tests only
make test-semantic

# Compliance fields tests only
make test-compliance

# VC attributes tests only
make test-vc

# Integration tests only
make test-e2e
```

### Using the Test Runner Directly

```bash
# Run all tests
./runner/run-tests.sh

# Run specific category
./runner/run-tests.sh --filter semantic-mapping
```

### Validating Test Configuration Schemas

```bash
make validate-schema
```

### Cleaning Results

```bash
make clean
```

## Test Vector Format

Each test case directory contains:

- **test-config.json**: Test configuration with pass/fail criteria
- **input.*.json**: Input data (DLT transaction, VC, claims)
- **expected.*.xml/json**: Expected output files
- **expected.*.jwt**: Expected JWT proofs (for VC tests)

### test-config.json Structure

```json
{
  "testId": "tc001-p2p-stablecoin",
  "name": "P2P Stablecoin Payment with ISO 20022 Mapping",
  "description": "Validates that a Stellar-based USDC payment produces correct pacs.008 XML",
  "scenario": "p2p-stablecoin",
  "category": "semantic-mapping",
  "dlt": "stellar",
  "asset": "USDC",
  "amount": "100.00",
  "mozartpayCommand": "mozartpay pay send --to GABCD... --amount 100 --asset USDC --rail direct --network stellar-testnet --output json",
  "passCriteria": {
    "fieldPresence": ["GrpHdr.MsgId", "CdtTrfTxInf.PmtId.EndToEndId"],
    "fieldValues": {
      "CdtTrfTxInf.Amt.InstdAmt.Ccy": "USD"
    },
    "passThreshold": 0.8
  }
}
```

## Conformance Criteria

### Pass/Fail Threshold

- **Target Pass Rate**: 80% (0.8)
- Tests must achieve a score ≥ 0.8 to pass conformance
- Score is calculated as: (validations passed) / (total validations)

### Validation Types

1. **Field Presence**: Required fields must exist in output
2. **Field Values**: Specific fields must match expected values
3. **Schema Compliance**: Output must conform to JSON schemas
4. **Structure Validation**: XML/JSON structure must be valid

## Test Categories

### Semantic Mapping (tc001-tc004)

Validates mapping between DLT transactions and ISO 20022 pacs.008 messages:

- **tc001**: P2P stablecoin payment with SupplementaryData
- **tc002**: CBDC point-of-sale payment with merchant data
- **tc003**: Merchant settlement batch processing
- **tc004**: Cross-border payment with Tempo FX integration

### Compliance Fields (tc101-tc104)

Validates KYC/AML and regulatory reporting:

- **tc101**: KYC verification with VC attachment
- **tc102**: AML threshold risk assessment
- **tc103**: Travel Rule compliance with originator/beneficiary info
- **tc104**: Consent proof for GDPR compliance

### VC Attributes (tc201-tc204)

Validates W3C Verifiable Credential structures:

- **tc201**: National ID credential issuance
- **tc202**: Selective disclosure with privacy
- **tc203**: Comprehensive KYC attribute set
- **tc204**: Consent attribute with revocation

### Integration (e2e001-e2e002)

End-to-end workflow validation:

- **e2e001**: Full payment flow (DID → VC → Payment → Report → ISO 20022)
- **e2e002**: Dispute handling workflow with evidence collection

## Results

Test results are generated in the `results/` directory with timestamp:

```
results/results_20260301_120000.json
```

### Results Schema

Results follow the `schema/results.schema.json` format:

```json
{
  "testRun": "20260301_120000",
  "timestamp": "2026-03-01T12:00:00Z",
  "results": [
    {
      "testId": "tc001-p2p-stablecoin",
      "status": "pass",
      "score": 0.85,
      "validations": [...],
      "errors": []
    }
  ],
  "summary": {
    "total": 14,
    "passed": 12,
    "failed": 2,
    "passRate": 0.857,
    "conformanceStatus": "passed"
  }
}
```

## MozartPay CLI Commands

Test vectors are aligned with actual MozartPay CLI commands:

### Payment Commands

```bash
mozartpay pay send --to <address> --amount <amount> --asset <XLM|USDC|EURC> --rail <direct|x402|tempo|zk> --network <stellar-testnet|stellar-mainnet> --output <pretty|json>
mozartpay pay quote --from <currency> --to <currency>
mozartpay pay x402 --resource <url> --price <price> --asset <asset> --to <address>
mozartpay pay zk --to <address> --amount <amount> --asset <asset> --privacy <full|selective>
```

### Report Commands

```bash
mozartpay report generate --vc-attach --output <pretty|json|iso20022> --tx <txHash>
mozartpay report show --output <pretty|json>
mozartpay report iso20022
```

### DID Commands

```bash
mozartpay did create --method <web|key|ethr|ebsi> --domain <domain> --output <pretty|json>
mozartpay did attest --method <ebsi> --vc <national-id|kyc|accreditation> --name <name> --country <country> --output <pretty|json>
mozartpay did verify --vc-file <path>
```

## Standards Compliance

This test suite validates conformance with:

- **ISO 20022**: Financial messaging (pacs.008.001.08)
- **W3C DID Core 1.0**: Decentralized Identifiers
- **W3C Verifiable Credentials**: VC Data Model 2.0
- **EBSI v3**: EU Blockchain Services Infrastructure
- **SEP-41**: Stellar Token Interface
- **FATF Travel Rule**: Cross-border payment regulations

## Contributing

When adding new test vectors:

1. Create a new test case directory following the naming convention
2. Add `test-config.json` with appropriate pass/fail criteria
3. Include input files (`.json`, `.jwt`)
4. Include expected output files (`.xml`, `.json`, `.jwt`)
5. Validate against schemas: `make validate-schema`
6. Run tests: `make test`

## License

Copyright © 2026 OG Technologies EU. All rights reserved.

## Contact

- **Organization**: OG Technologies EU
- **Location**: Vienna, Austria
- **Web**: https://mozartpay.com

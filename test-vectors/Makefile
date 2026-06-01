.PHONY: test test-semantic test-compliance test-vc test-e2e clean validate-schema help

# Default target
.DEFAULT_GOAL := help

# Directory paths
TEST_VECTORS_DIR := $(shell pwd)
RUNNER_DIR := $(TEST_VECTORS_DIR)/runner
RESULTS_DIR := $(TEST_VECTORS_DIR)/results
SCHEMA_DIR := $(TEST_VECTORS_DIR)/schema

# Test runner script
RUN_TESTS := $(RUNNER_DIR)/run-tests.sh

help:
	@echo "MozartPay Conformance Test Suite"
	@echo "================================"
	@echo ""
	@echo "Available targets:"
	@echo "  make test              - Run all conformance tests"
	@echo "  make test-semantic     - Run semantic mapping tests (tc001-tc004)"
	@echo "  make test-compliance   - Run compliance fields tests (tc101-tc104)"
	@echo "  make test-vc           - Run VC attributes tests (tc201-tc204)"
	@echo "  make test-e2e          - Run integration tests (e2e001-e2e002)"
	@echo "  make validate-schema   - Validate all test-config.json against schema"
	@echo "  make clean             - Clean generated results"
	@echo "  make help              - Show this help message"
	@echo ""
	@echo "Examples:"
	@echo "  make test"
	@echo "  make test-semantic"
	@echo "  make clean && make test"

test:
	@echo "Running all conformance tests..."
	@$(RUN_TESTS)

test-semantic:
	@echo "Running semantic mapping tests..."
	@$(RUN_TESTS) --filter semantic-mapping

test-compliance:
	@echo "Running compliance fields tests..."
	@$(RUN_TESTS) --filter compliance-fields

test-vc:
	@echo "Running VC attributes tests..."
	@$(RUN_TESTS) --filter vc-attributes

test-e2e:
	@echo "Running integration tests..."
	@$(RUN_TESTS) --filter integration

validate-schema:
	@echo "Validating test-config.json files against schema..."
	@command -v python3 >/dev/null 2>&1 || { echo "Error: python3 is required"; exit 1; }
	@python3 -c "import jsonschema" 2>/dev/null || { echo "Error: jsonschema Python package required. Install with: pip3 install jsonschema"; exit 1; }
	@echo "Checking test-config.json files..."
	@failed=0; \
	for config in $$(find . -name "test-config.json"); do \
		echo "  Validating $$config..."; \
		if python3 -c "import json, jsonschema, sys; jsonschema.validate(json.load(open('$$config')), json.load(open('$(SCHEMA_DIR)/test-vector.schema.json')))" 2>/dev/null; then \
			echo "    ✓ Valid"; \
		else \
			echo "    ✗ Invalid"; \
			failed=$$((failed + 1)); \
		fi; \
	done; \
	if [ $$failed -eq 0 ]; then \
		echo "All test-config.json files are valid"; \
	else \
		echo "$$failed test-config.json files failed validation"; \
		exit 1; \
	fi

clean:
	@echo "Cleaning results directory..."
	@rm -f $(RESULTS_DIR)/results_*.json
	@echo "Results directory cleaned"
	@echo "Placeholder .gitkeep preserved"

# Validate that required tools are available
check-tools:
	@command -v jq >/dev/null 2>&1 || { echo "Error: jq is required. Install from https://stedolan.github.io/jq/"; exit 1; }
	@command -v python3 >/dev/null 2>&1 || { echo "Error: python3 is required"; exit 1; }
	@command -v bc >/dev/null 2>&1 || { echo "Error: bc is required"; exit 1; }
	@echo "All required tools are installed"

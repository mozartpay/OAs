#!/bin/bash
# Conformance test runner for DLT retail payment test vectors
# Performs static validation of test vectors against JSON schemas

set -e

TEST_VECTORS_DIR="$(cd "$(dirname "$0")/.." && pwd)"
RESULTS_DIR="$TEST_VECTORS_DIR/results"
SCHEMA_DIR="$TEST_VECTORS_DIR/schema"
TIMESTAMP=$(date -u +"%Y%m%d_%H%M%S")
RESULT_FILE="$RESULTS_DIR/results_$TIMESTAMP.json"

# Colors for output
RED='\033[0;31m'
GREEN='\033[0;32m'
YELLOW='\033[1;33m'
BLUE='\033[0;34m'
NC='\033[0m' # No Color

# Check if jq is available
if ! command -v jq &> /dev/null; then
    echo -e "${RED}Error: jq is required but not installed. Please install jq to run tests.${NC}"
    exit 1
fi

# Check if python3 is available for schema validation
if ! command -v python3 &> /dev/null; then
    echo -e "${RED}Error: python3 is required but not installed. Please install python3 to run tests.${NC}"
    exit 1
fi

# Check if jsonschema is available
if ! python3 -c "import jsonschema" 2>/dev/null; then
    echo -e "${YELLOW}Warning: jsonschema Python package not found. Installing...${NC}"
    pip3 install jsonschema --quiet || {
        echo -e "${RED}Error: Failed to install jsonschema. Please install it manually: pip3 install jsonschema${NC}"
        exit 1
    }
fi

echo -e "${BLUE}========================================${NC}"
echo -e "${BLUE}MozartPay Conformance Test Runner${NC}"
echo -e "${BLUE}========================================${NC}"
echo ""

# Initialize results
echo "{\"testRun\": \"$TIMESTAMP\", \"timestamp\": \"$(date -u +"%Y-%m-%dT%H:%M:%SZ")\", \"environment\": {\"runnerVersion\": \"1.0.0\", \"schemaVersion\": \"1.0.0\"}, \"results\": [], \"summary\": {}}" > $RESULT_FILE

# Function to validate JSON schema
validate_schema() {
    local file=$1
    local schema=$2
    local test_id=$3
    
    if python3 -c "import json, jsonschema, sys; jsonschema.validate(json.load(open('$file')), json.load(open('$schema')))" 2>/dev/null; then
        return 0
    else
        return 1
    fi
}

# Function to check field presence in JSON
check_field_presence() {
    local file=$1
    local field=$2
    
    if [ -f "$file" ]; then
        if echo "$field" | grep -q '\.'; then
            # Nested field - use jq
            local IFS='.' read -ra PARTS <<< "$field"
            local query=".$field"
            if jq -e "$query" "$file" > /dev/null 2>&1; then
                return 0
            else
                return 1
            fi
        else
            # Top-level field
            if jq -e ".$field" "$file" > /dev/null 2>&1; then
                return 0
            else
                return 1
            fi
        fi
    fi
    return 1
}

# Function to check field value in JSON
check_field_value() {
    local file=$1
    local field=$2
    local expected=$3
    
    if [ -f "$file" ]; then
        local actual=$(jq -r ".$field" "$file" 2>/dev/null)
        if [ "$actual" = "$expected" ]; then
            return 0
        else
            return 1
        fi
    fi
    return 1
}

# Function to run a single test
run_test() {
    local test_path=$1
    local test_id=$(basename $test_path)
    local config_file="$test_path/test-config.json"
    
    echo -e "${BLUE}Running $test_id...${NC}"
    
    # Load test config
    if [ ! -f "$config_file" ]; then
        echo -e "${RED}  ✗ Missing test-config.json${NC}"
        jq --arg id "$test_id" --arg status "error" \
           '.results += [{"testId": $id, "status": $status, "timestamp": "'$(date -u +"%Y-%m-%dT%H:%M:%SZ")'", "errors": ["Missing test-config.json"]}]' \
           $RESULT_FILE > $RESULT_FILE.tmp && mv $RESULT_FILE.tmp $RESULT_FILE
        return 1
    fi
    
    # Validate test config against schema
    if ! validate_schema "$config_file" "$SCHEMA_DIR/test-vector.schema.json" "$test_id"; then
        echo -e "${RED}  ✗ test-config.json schema validation failed${NC}"
        jq --arg id "$test_id" --arg status "error" \
           '.results += [{"testId": $id, "status": $status, "timestamp": "'$(date -u +"%Y-%m-%dT%H:%M:%SZ")'", "errors": ["Schema validation failed"]}]' \
           $RESULT_FILE > $RESULT_FILE.tmp && mv $RESULT_FILE.tmp $RESULT_FILE
        return 1
    fi
    
    local validations_passed=0
    local validations_total=0
    local validations=()
    local errors=()
    
    # Extract pass criteria
    local pass_threshold=$(jq -r '.passCriteria.passThreshold // 0.8' "$config_file")
    
    # Check field presence
    local field_presence=$(jq -r '.passCriteria.fieldPresence // []' "$config_file")
    if [ "$field_presence" != "[]" ]; then
        for field in $(echo "$field_presence" | jq -r '.[]'); do
            validations_total=$((validations_total + 1))
            local file_to_check=""
            
            # Determine which file to check based on field
            if [[ "$field" == *"GrpHdr"* ]] || [[ "$field" == *"CdtTrfTxInf"* ]]; then
                file_to_check=$(jq -r '.expectedFiles.iso200ared // ""' "$config_file")
            elif [[ "$field" == *"kycVerification"* ]] || [[ "$field" == *"amlAssessment"* ]]; then
                file_to_check=$(jq -r '.expectedFiles.report // ""' "$config_file")
            elif [[ "$field" == *"@context"* ]] || [[ "$field" == *"credentialSubject"* ]]; then
                file_to_check=$(jq -r '.expectedFiles.vc // ""' "$config_file")
            fi
            
            if [ -n "$file_to_check" ] && [ -f "$test_path/$file_to_check" ]; then
                if check_field_presence "$test_path/$file_to_check" "$field"; then
                    validations_passed=$((validations_passed + 1))
                    validations+=("{\"check\": \"Field presence: $field\", \"passed\": true}")
                else
                    errors+=("Field $field not found in $file_to_check")
                    validations+=("{\"check\": \"Field presence: $field\", \"passed\": false}")
                fi
            else
                validations_total=$((validations_total - 1))
            fi
        done
    fi
    
    # Check field values
    local field_values=$(jq -r '.passCriteria.fieldValues // {}' "$config_file")
    if [ "$field_values" != "{}" ]; then
        for field in $(echo "$field_values" | jq -r 'keys[]'); do
            validations_total=$((validations_total + 1))
            local expected=$(echo "$field_values" | jq -r ".\"$field\"")
            local file_to_check=""
            
            # Determine which file to check
            if [[ "$field" == *"Ccy"* ]] || [[ "$field" == *"InstdAmt"* ]]; then
                file_to_check=$(jq -r '.expectedFiles.iso200ared // ""' "$config_file")
            elif [[ "$field" == *"vcAttached"* ]] || [[ "$field" == *"status"* ]]; then
                file_to_check=$(jq -r '.expectedFiles.report // ""' "$config_file")
            fi
            
            if [ -n "$file_to_check" ] && [ -f "$test_path/$file_to_check" ]; then
                if check_field_value "$test_path/$file_to_check" "$field" "$expected"; then
                    validations_passed=$((validations_passed + 1))
                    validations+=("{\"check\": \"Field value: $field = $expected\", \"passed\": true}")
                else
                    local actual=$(jq -r ".$field" "$test_path/$file_to_check" 2>/dev/null)
                    errors+=("Field $field: expected $expected, got $actual")
                    validations+=("{\"check\": \"Field value: $field\", \"passed\": false, \"expected\": \"$expected\", \"actual\": \"$actual\"}")
                fi
            else
                validations_total=$((validations_total - 1))
            fi
        done
    fi
    
    # Validate expected files against schemas
    if [ -f "$test_path/expected.vc.jsonld" ]; then
        validations_total=$((validations_total + 1))
        # VC validation - check required structure
        if jq -e '.["@context"]' "$test_path/expected.vc.jsonld" > /dev/null 2>&1 && \
           jq -e '.type' "$test_path/expected.vc.jsonld" > /dev/null 2>&1 && \
           jq -e '.credentialSubject' "$test_path/expected.vc.jsonld" > /dev/null 2>&1; then
            validations_passed=$((validations_passed + 1))
            validations+=("{\"check\": \"VC structure validation\", \"passed\": true}")
        else
            errors+=("VC structure validation failed")
            validations+=("{\"check\": \"VC structure validation\", \"passed\": false}")
        fi
    fi
    
    if [ -f "$test_path/expected.iso20022.xml" ]; then
        validations_total=$((validations_total + 1))
        # XML validation - check basic structure
        if grep -q "<Document" "$test_path/expected.iso20022.xml" && \
           grep -q "pacs.008" "$test_path/expected.iso20022.xml"; then
            validations_passed=$((validations_passed + 1))
            validations+=("{\"check\": \"ISO 20022 XML structure\", \"passed\": true}")
        else
            errors+=("ISO 20022 XML structure validation failed")
            validations+=("{\"check\": \"ISO 20022 XML structure\", \"passed\": false}")
        fi
    fi
    
    # Calculate score
    local score=0
    if [ $validations_total -gt 0 ]; then
        score=$(echo "scale=3; $validations_passed / $validations_total" | bc)
    fi
    
    # Determine pass/fail
    local status="pass"
    local compare_result=$(echo "$score >= $pass_threshold" | bc)
    if [ "$compare_result" = "0" ]; then
        status="fail"
    fi
    
    # Build validations array for JSON
    local validations_json="["
    local first=true
    for v in "${validations[@]}"; do
        if [ "$first" = true ]; then
            first=false
        else
            validations_json+=","
        fi
        validations_json+="$v"
    done
    validations_json+="]"
    
    # Build errors array
    local errors_json="["
    local first=true
    for e in "${errors[@]}"; do
        if [ "$first" = true ]; then
            first=false
        else
            errors_json+=","
        fi
        errors_json+="\"$e\""
    done
    errors_json+="]"
    
    # Record result
    jq --arg id "$test_id" --arg status "$status" --argjson score "$score" \
       --argjson validations "$validations_json" --argjson errors "$errors_json" \
       '.results += [{"testId": $id, "status": $status, "timestamp": "'$(date -u +"%Y-%m-%dT%H:%M:%SZ")'", "score": $score, "validations": $validations, "errors": $errors}]' \
       $RESULT_FILE > $RESULT_FILE.tmp && mv $RESULT_FILE.tmp $RESULT_FILE
    
    if [ "$status" = "pass" ]; then
        echo -e "${GREEN}  ✓ PASSED (score: $score)${NC}"
    else
        echo -e "${RED}  ✗ FAILED (score: $score, threshold: $pass_threshold)${NC}"
        for e in "${errors[@]}"; do
            echo -e "${RED}    - $e${NC}"
        done
    fi
    
    return 0
}

# Function to run tests in a category
run_category() {
    local category=$1
    local category_path="$TEST_VECTORS_DIR/$category"
    
    if [ ! -d "$category_path" ]; then
        return
    fi
    
    for test in "$category_path"/*/; do
        if [ -d "$test" ]; then
            run_test "$test"
        fi
    done
}

# Parse command line arguments
FILTER=""
while [[ $# -gt 0 ]]; do
    case $1 in
        --filter)
            FILTER="$2"
            shift 2
            ;;
        --help)
            echo "Usage: $0 [--filter <category>]"
            echo "Categories: semantic-mapping, compliance-fields, vc-attributes, integration"
            exit 0
            ;;
        *)
            echo "Unknown option: $1"
            exit 1
            ;;
    esac
done

# Run tests
if [ -n "$FILTER" ]; then
    echo -e "${YELLOW}Filter: Running only $FILTER tests${NC}"
    run_category "$FILTER"
else
    run_category "semantic-mapping"
    run_category "compliance-fields"
    run_category "vc-attributes"
    run_category "integration"
fi

# Calculate summary
TOTAL=$(jq '.results | length' $RESULT_FILE)
PASSED=$(jq '.results | map(select(.status == "pass")) | length' $RESULT_FILE)
FAILED=$(jq '.results | map(select(.status == "fail")) | length' $RESULT_FILE)
ERRORS=$(jq '.results | map(select(.status == "error")) | length' $RESULT_FILE)

PASS_RATE=$(echo "scale=2; $PASSED * 100 / $TOTAL" | bc)
PASS_RATE_DECIMAL=$(echo "scale=3; $PASSED / $TOTAL" | bc)

TARGET_RATE=0.8
CONFORMANCE_STATUS="passed"
COMPARE_RESULT=$(echo "$PASS_RATE_DECIMAL < $TARGET_RATE" | bc)
if [ "$COMPARE_RESULT" = "1" ]; then
    CONFORMANCE_STATUS="failed"
fi

# Update summary in results
jq --arg total "$TOTAL" --arg passed "$PASSED" --arg failed "$FAILED" --arg skipped "$ERRORS" \
   --arg passRate "$PASS_RATE_DECIMAL" --arg target "$TARGET_RATE" --arg conformance "$CONFORMANCE_STATUS" \
   '.summary = {"total": ($total | tonumber), "passed": ($passed | tonumber), "failed": ($failed | tonumber), "skipped": ($skipped | tonumber), "passRate": ($passRate | tonumber), "targetPassRate": ($target | tonumber), "conformanceStatus": $conformance}' \
   $RESULT_FILE > $RESULT_FILE.tmp && mv $RESULT_FILE.tmp $RESULT_FILE

echo ""
echo -e "${BLUE}========================================${NC}"
echo -e "${BLUE}Conformance Test Results${NC}"
echo -e "${BLUE}========================================${NC}"
echo -e "Total tests:   $TOTAL"
echo -e "${GREEN}Passed:        $PASSED${NC}"
echo -e "${RED}Failed:        $FAILED${NC}"
echo -e "${YELLOW}Errors:        $ERRORS${NC}"
echo -e "Pass rate:     ${PASS_RATE}% (target: 80%)"
echo -e "Results saved: $RESULT_FILE"
echo -e "${BLUE}========================================${NC}"

if [ "$CONFORMANCE_STATUS" = "failed" ]; then
    echo -e "${RED}❌ Conformance FAILED - pass rate below 80%${NC}"
    exit 1
else
    echo -e "${GREEN}✅ Conformance PASSED${NC}"
    exit 0
fi

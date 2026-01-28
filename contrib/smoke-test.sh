#!/bin/bash
# Smoke test for truenas-acme-hetzner
# Usage: ./smoke-test.sh <domain> [tah-binary-path]
#
# Prerequisites:
# - Valid Hetzner Cloud API token in ~/.tahtoken
# - Domain zone exists in Hetzner Cloud DNS
# - dig command available (optional, for verification)

set -e

DOMAIN="${1:?Usage: $0 <domain> [tah-binary-path]}"
TAH="${2:-./tah}"
TEST_NAME="_acme-test-$(date +%s)"
TEST_VALUE="test-value-$(date +%s)"

RED='\033[0;31m'
GREEN='\033[0;32m'
YELLOW='\033[1;33m'
NC='\033[0m'

pass() { echo -e "${GREEN}✓ PASS${NC}: $1"; }
fail() { echo -e "${RED}✗ FAIL${NC}: $1"; exit 1; }
info() { echo -e "${YELLOW}→${NC} $1"; }

# DNS lookup with retry for propagation
dns_lookup_retry() {
    local record="$1"
    local expect_empty="$2"
    local max_attempts=6
    local wait_secs=5

    for ((i=1; i<=max_attempts; i++)); do
        DNS_RESULT=$(dig +short TXT "$record" @hydrogen.ns.hetzner.com 2>/dev/null || true)

        if [[ "$expect_empty" == "true" ]]; then
            if [[ -z "$DNS_RESULT" ]]; then
                return 0
            fi
        else
            if [[ -n "$DNS_RESULT" ]]; then
                echo "$DNS_RESULT"
                return 0
            fi
        fi

        if [[ $i -lt $max_attempts ]]; then
            echo -e "  ${YELLOW}...${NC} waiting ${wait_secs}s for propagation (attempt $i/$max_attempts)"
            sleep $wait_secs
        fi
    done
    return 1
}

echo "========================================"
echo "truenas-acme-hetzner Smoke Test"
echo "========================================"
echo "Domain: $DOMAIN"
echo "Binary: $TAH"
echo "Test record: $TEST_NAME.$DOMAIN"
echo ""

# Test 0: Binary exists and is executable
info "Test 0: Checking binary..."
if [[ ! -x "$TAH" ]]; then
    fail "Binary not found or not executable: $TAH"
fi
pass "Binary exists and is executable"

# Test 1: Help command
info "Test 1: Running help command..."
if $TAH help | grep -q "Usage:"; then
    pass "Help command works"
else
    fail "Help command failed"
fi

# Test 2: Token file check
info "Test 2: Checking token file..."
if [[ ! -f ~/.tahtoken ]]; then
    fail "Token file ~/.tahtoken not found. Run: $TAH init"
fi
if [[ ! -s ~/.tahtoken ]]; then
    fail "Token file ~/.tahtoken is empty"
fi
pass "Token file exists and is not empty"

# Test 3: Built-in test command
info "Test 3: Running built-in test (creates/deletes hcdcadclk record)..."
if $TAH test "$DOMAIN"; then
    pass "Built-in test command passed"
else
    fail "Built-in test command failed"
fi

# Test 4: Set command (create ACME challenge record)
info "Test 4: Creating TXT record with 'set' command..."
FULL_NAME="$TEST_NAME.$DOMAIN"
if $TAH set "$DOMAIN" "$FULL_NAME" "$TEST_VALUE"; then
    pass "Set command succeeded"
else
    fail "Set command failed"
fi

# Test 4b: Verify record exists (optional, requires dig)
if command -v dig &> /dev/null; then
    info "Test 4b: Verifying record via DNS lookup..."
    if RESULT=$(dns_lookup_retry "$TEST_NAME.$DOMAIN" "false"); then
        pass "DNS lookup confirmed record exists: $RESULT"
    else
        fail "DNS lookup failed - record not found after retries"
    fi
else
    echo -e "${YELLOW}  SKIP${NC}: dig not available, skipping DNS verification"
fi

# Test 5: Unset command (remove ACME challenge record)
info "Test 5: Removing TXT record with 'unset' command..."
if $TAH unset "$DOMAIN" "$FULL_NAME" "$TEST_VALUE"; then
    pass "Unset command succeeded"
else
    fail "Unset command failed"
fi

# Test 5b: Verify record removed (optional)
if command -v dig &> /dev/null; then
    info "Test 5b: Verifying record removed via DNS lookup..."
    if dns_lookup_retry "$TEST_NAME.$DOMAIN" "true"; then
        pass "DNS lookup confirmed record removed"
    else
        echo -e "${YELLOW}  WARN${NC}: Record may still exist (propagation delay)"
    fi
fi

# Test 6: Set duplicate (add to existing RRSet)
info "Test 6: Testing duplicate record handling..."
VALUE1="value1-$(date +%s)"
VALUE2="value2-$(date +%s)"

$TAH set "$DOMAIN" "$FULL_NAME" "$VALUE1" || fail "First set failed"
$TAH set "$DOMAIN" "$FULL_NAME" "$VALUE2" || fail "Second set (add to RRSet) failed"
pass "Multiple records in same RRSet works"

# Cleanup
info "Cleanup: Removing test records..."
$TAH unset "$DOMAIN" "$FULL_NAME" "$VALUE1" || echo "  Cleanup warning: couldn't remove value1"
$TAH unset "$DOMAIN" "$FULL_NAME" "$VALUE2" || echo "  Cleanup warning: couldn't remove value2"

echo ""
echo "========================================"
echo -e "${GREEN}All tests passed!${NC}"
echo "========================================"

#!/usr/bin/env bash
# scripts/check-handler-coverage.sh — C008 §4.1/§4.3 CI gate.
#
# Verifies the integration test surface tracks the handler surface so we
# don't silently regress coverage when handlers are added/renamed:
#
#   1. Route count threshold — must stay >= MIN_ROUTES (current production
#      baseline; raise as new endpoints land but never lower).
#   2. Test function count threshold — must stay >= MIN_TESTS, computed
#      from C008 §4.4 simplified plan: Batch-B 5-case matrix + Batch-A/C/D/E
#      single happy path per route. Drops below threshold → CI fail.
#
# Run from repo root or server/ — both work.
#
# Exit codes: 0 = green; non-zero = regression.

set -euo pipefail

# Resolve to server/ so internal paths work regardless of cwd.
script_dir="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
cd "$script_dir/.."  # script lives in server/scripts/

# Thresholds — bump only after a corresponding test addition lands.
MIN_ROUTES="${MIN_ROUTES:-88}"
MIN_TESTS="${MIN_TESTS:-410}"

route_count="$(grep -rEn 'authed\.(GET|POST|PUT|PATCH|DELETE)' internal/http/ \
    --include='*.go' | grep -v _test.go | wc -l | tr -d ' ')"

test_count="$(grep -hcE '^func TestM4' tests/integration/m4_*.go 2>/dev/null \
    | awk '{sum+=$1} END {print sum+0}')"

printf 'route count : %s (threshold %s)\n' "$route_count" "$MIN_ROUTES"
printf 'test count  : %s (threshold %s)\n' "$test_count" "$MIN_TESTS"

if [ "$route_count" -lt "$MIN_ROUTES" ]; then
    echo "FAIL: route count $route_count < $MIN_ROUTES — handlers were removed?" >&2
    exit 1
fi

if [ "$test_count" -lt "$MIN_TESTS" ]; then
    echo "FAIL: TestM4* count $test_count < $MIN_TESTS — integration regression" >&2
    exit 1
fi

# Sanity grep: every handler file must have a corresponding m4_*_test.go that
# at least mentions its family (catches "added handler, never tested").
families=(announcement approval channel favorite friend message notification \
          presence quick_reply reaction scheduled urgent module sync settings)
missing=()
for f in "${families[@]}"; do
    if ! grep -lE "TestM4.*${f}" tests/integration/m4_*.go >/dev/null 2>&1; then
        # Allow plural / variant matches — heuristic.
        case "$f" in
            quick_reply) grep -lE 'TestM4.*QuickReply' tests/integration/m4_*.go >/dev/null 2>&1 && continue ;;
            channel)     grep -lE 'TestM4.*Channel'    tests/integration/m4_*.go >/dev/null 2>&1 && continue ;;
            settings)    grep -lE 'TestM4.*Settings'   tests/integration/m4_*.go >/dev/null 2>&1 && continue ;;
        esac
        missing+=("$f")
    fi
done
if [ "${#missing[@]}" -gt 0 ]; then
    echo "FAIL: handler families with no TestM4* coverage: ${missing[*]}" >&2
    exit 1
fi

echo "OK: handler-coverage gate green."

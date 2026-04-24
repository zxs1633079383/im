#!/usr/bin/env bash
# seed-users.sh — pre-register N users through the im gateway so k6 VUs can
# login-only (no bcrypt stampede during the actual load test).
#
# Usage:
#   IM_GATEWAY=http://localhost:38080 scripts/seed-users.sh
#   SEED_USER_COUNT=2000 scripts/seed-users.sh
#
# Parallelism is bounded (default 10) to avoid overrunning bcrypt CPU during
# seed itself; turning it up too high triggers the same stampede we're
# trying to avoid.

set -euo pipefail

IM_GATEWAY="${IM_GATEWAY:-http://localhost:38080}"
USER_PREFIX="${USER_PREFIX:-k6pre}"
N="${SEED_USER_COUNT:-1000}"
PASSWORD="${V4_PASS:-v4test1234}"
PARALLELISM="${SEED_PARALLELISM:-10}"

echo "==> seeding $N users via $IM_GATEWAY (parallelism=$PARALLELISM)"
echo "    prefix=${USER_PREFIX} password=${PASSWORD}"

# Probe reachability (treat any HTTP response as "reachable"; xargs step will
# surface real failures loudly if the gateway is down).
probe=$(curl -sS -m 5 -o /dev/null -w '%{http_code}' \
    -X POST "$IM_GATEWAY/api/auth/login" \
    -H 'Content-Type: application/json' \
    -d '{"login":"__probe__","password":"__probe__"}' 2>/dev/null || echo "")
if [[ -z "$probe" || "$probe" == "000" ]]; then
    echo "error: gateway unreachable at $IM_GATEWAY (probe=$probe)" >&2
    exit 2
fi
echo "    probe http=$probe (reachable)"

register_one() {
    local i="$1"
    local username="${USER_PREFIX}${i}"
    local body="{\"username\":\"${username}\",\"email\":\"${username}@k6.load\",\"password\":\"${PASSWORD}\",\"display_name\":\"${username}\"}"
    local status
    status=$(curl -sS -m 10 -o /dev/null -w "%{http_code}" \
        -X POST "${IM_GATEWAY}/api/auth/register" \
        -H "Content-Type: application/json" \
        -d "$body" 2>/dev/null || echo "000")
    case "$status" in
        201|409) : ;;
        *) echo "WARN: $username got status $status" >&2 ;;
    esac
}
export IM_GATEWAY USER_PREFIX PASSWORD
export -f register_one

START=$(date +%s)
for i in $(seq 1 "$N"); do
    register_one "$i" &
    while (( $(jobs -rp | wc -l) >= PARALLELISM )); do
        wait -n
    done
done
wait
END=$(date +%s)
echo "==> seeded $N users in $((END-START))s"

#!/usr/bin/env bash
# seed-mm-cookies-bulk.sh — generate N synthetic mm-shaped cookie/user
# fixtures and HSet them into the upstream cses Redis HASH "User", then
# write a CSV (cookieId,userId) for k6 / load tests to consume.
#
# Reproducing internal/testutil.CookieFixture's wire shape exactly so a
# pre cluster sees the same JSON the production cses-server would write.
#
# Usage:
#   IM_REDIS=localhost:26379 N=300 ./seed-mm-cookies-bulk.sh
#   N=1000 OUT=/tmp/k6-cookies.csv ./seed-mm-cookies-bulk.sh
#
# Env:
#   IM_REDIS          host:port   (default localhost:6379)
#   IM_REDIS_PASSWORD auth        (optional)
#   IM_REDIS_DB       db number   (default 0; ignored on Cluster)
#   N                 count       (default 300, k6 VU baseline)
#   OUT               csv path    (default ./mm-cookies-bulk.csv)
#   COMPANY_ID        team id     (default RealCompanyID 张立超 company)
#   PREFIX            id prefix   (default 'a' — keeps 24-char hex valid)
set -euo pipefail

REDIS_HOST=${IM_REDIS:-localhost:6379}
REDIS_PASSWORD=${IM_REDIS_PASSWORD:-}
REDIS_DB=${IM_REDIS_DB:-0}
N=${N:-300}
OUT=${OUT:-./mm-cookies-bulk.csv}
COMPANY_ID=${COMPANY_ID:-6111fb0a202d425d221c53db}
PREFIX=${PREFIX:-a}

if ! command -v redis-cli >/dev/null 2>&1; then
    echo "redis-cli not on PATH — install redis-tools and retry" >&2
    exit 1
fi

if [[ ${#PREFIX} -ne 1 ]] || [[ ! "$PREFIX" =~ ^[a-f]$ ]]; then
    echo "PREFIX must be a single lowercase hex char (a-f), got '$PREFIX'" >&2
    exit 1
fi

redis_args=(-h "${REDIS_HOST%:*}" -p "${REDIS_HOST##*:}")
[[ -n "$REDIS_PASSWORD" ]] && redis_args+=(-a "$REDIS_PASSWORD" --no-auth-warning)
[[ -n "$REDIS_DB" ]] && redis_args+=(-n "$REDIS_DB")

# pad-hex 23 — print integer i as 23 lowercase hex chars (zero-padded).
# Combined with the 1-char PREFIX this yields 24-char valid hex satisfying
# auth.ValidateUserID.
pad_hex_23() { printf '%023x' "$1"; }

START=$(date +%s)
: > "$OUT"
echo "==> seeding $N cookies into $REDIS_HOST db=$REDIS_DB"
echo "    prefix=$PREFIX company_id=$COMPANY_ID out=$OUT"

# Build a single batched MULTI/EXEC pipeline so one TCP RTT covers all N
# HSets (vs. N round-trips when looped). 3-7× faster on remote pre cluster.
{
    for ((i=1; i<=N; i++)); do
        hex=$(pad_hex_23 "$i")
        cookie_id="${PREFIX}${hex}"
        # Different leading char keeps userId distinct from cookieId in case
        # both end up in the same lookup namespace. 'b' is also valid hex.
        user_id="b${hex}"
        printf '%s,%s\n' "$cookie_id" "$user_id" >> "$OUT"
        # JSON quoted field key — matches Mattermost Java writer shape.
        field="\"$cookie_id\""
        # Compose payload inline; only the fields handlers actually read
        # (UserID / CompanyID / Name / UserName) need to be set.
        body="{\"id\":\"$user_id\",\"userId\":\"$user_id\",\"userName\":\"k6-$cookie_id\",\"name\":\"k6-$cookie_id\",\"companyId\":\"$COMPANY_ID\",\"orgId\":\"$COMPANY_ID\",\"roles\":[\"Member\"],\"orgRole\":\"Member\"}"
        printf 'HSET User %s %s\n' "$field" "$(printf '%q' "$body")"
    done
} | redis-cli "${redis_args[@]}" --pipe-mode >/dev/null 2>&1 || {
    # --pipe-mode is fast but very strict; fall back to a per-line loop
    # if the cluster rejects the multi-line stream (Cluster prefers
    # single-key pipelining).
    while IFS=, read -r cookie_id user_id; do
        body="{\"id\":\"$user_id\",\"userId\":\"$user_id\",\"userName\":\"k6-$cookie_id\",\"name\":\"k6-$cookie_id\",\"companyId\":\"$COMPANY_ID\",\"orgId\":\"$COMPANY_ID\",\"roles\":[\"Member\"],\"orgRole\":\"Member\"}"
        redis-cli "${redis_args[@]}" HSET User "\"$cookie_id\"" "$body" >/dev/null
    done < "$OUT"
}

END=$(date +%s)
echo "==> seeded $N cookies in $((END-START))s"
echo "    csv → $OUT (cookieId,userId per line)"
echo
echo "verify any one entry with:"
first=$(head -1 "$OUT" | cut -d, -f1)
echo "  redis-cli ${redis_args[*]} HGET User '\"$first\"' | head -c 120"

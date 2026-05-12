#!/usr/bin/env bash
# seed-mm-cookies-bulk.sh — generate N synthetic mm-shaped user fixtures
# and SET them into the upstream cses Redis as `UserData:<userId>` STRING
# keys, then write a CSV (cookieId,userId — equal per v0.7.4) for k6 /
# load tests to consume.
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
echo "==> seeding $N userIds into $REDIS_HOST db=$REDIS_DB"
echo "    prefix=$PREFIX company_id=$COMPANY_ID out=$OUT"

# Pass 1 — write the full CSV first. v0.7.4: cookieId == userId, so each
# line carries the same id twice for source-compat with old k6 scripts
# that expect a (cookieId,userId) tuple.
: > "$OUT"
for ((i=1; i<=N; i++)); do
    hex=$(pad_hex_23 "$i")
    user_id="${PREFIX}${hex}"
    printf '%s,%s\n' "$user_id" "$user_id" >> "$OUT"
done

# Pass 2 — replay the CSV into Redis. v0.7.4: STRING SET UserData:<userId>;
# inline new nested wire shape (organizes[]) so fixtures stay faithful to
# what cses Java will write in production.
echo "==> replaying CSV → SET UserData:<userId>"
seed_count=0
while IFS=, read -r _cookie_id user_id; do
    body="{\"id\":\"$user_id\",\"mobile\":\"\",\"name\":\"k6-$user_id\",\"userName\":\"k6-$user_id\",\"userId\":\"\",\"organizes\":[{\"companyId\":\"$COMPANY_ID\",\"orgId\":\"$COMPANY_ID\",\"orgType\":\"Member\",\"userId\":\"$user_id\",\"userName\":\"k6-$user_id\"}]}"
    redis-cli "${redis_args[@]}" SET "UserData:${user_id}" "$body" >/dev/null
    seed_count=$((seed_count + 1))
    if (( seed_count % 100 == 0 )); then
        echo "    ... $seed_count / $N seeded"
    fi
done < "$OUT"

END=$(date +%s)
echo "==> seeded $seed_count userIds in $((END-START))s"
echo "    csv → $OUT (cookieId,userId per line — equal in v0.7.4)"
echo
echo "verify any one entry with:"
first=$(head -1 "$OUT" | cut -d, -f2)
echo "  redis-cli ${redis_args[*]} GET 'UserData:$first' | head -c 200"

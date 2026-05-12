#!/usr/bin/env bash
# seed-mm-cookies.sh — populate the upstream cses Redis STRING `UserData:<userId>`
# fixtures so im handlers can authenticate cookieId-only requests during
# local / pre integration runs.
#
# v0.7.4 wire change: cookieId header value == userId, and the user profile
# moved from HASH "User" field <quoted-cookieId> to STRING UserData:<userId>.
# This is the manual flip-side of internal/testutil.CookieFixture: same wire
# shape, but for use with `IM_GATEWAY=...` against a port-forwarded cses Redis
# (or the local docker-compose redis if you flipped IM to it).
#
# Usage:
#   IM_REDIS=localhost:6379 IM_REDIS_PASSWORD= \
#     server/scripts/seed-mm-cookies.sh
#
# Reads optional env:
#   IM_REDIS          — host:port (default localhost:6379)
#   IM_REDIS_PASSWORD — auth, optional
#   IM_REDIS_DB       — db number (default 0; ignored on Cluster)
#   COOKIE_FILE       — path to a JSON file with overrides (see below)
#
# COOKIE_FILE format: a JSON array of records like
#   [
#     {"userId":"...","companyId":"...","userName":"...","mobile":"..."},
#     ...
#   ]
# When COOKIE_FILE is set, only those entries are seeded. Otherwise the
# built-in pre fixture (张立超) is the only entry written.
set -euo pipefail

REDIS_HOST=${IM_REDIS:-localhost:6379}
REDIS_PASSWORD=${IM_REDIS_PASSWORD:-}
REDIS_DB=${IM_REDIS_DB:-0}

if ! command -v redis-cli >/dev/null 2>&1; then
    echo "redis-cli not on PATH — install redis-tools and retry" >&2
    exit 1
fi

redis_args=(-h "${REDIS_HOST%:*}" -p "${REDIS_HOST##*:}")
if [[ -n "${REDIS_PASSWORD}" ]]; then
    redis_args+=(-a "${REDIS_PASSWORD}" --no-auth-warning)
fi
if [[ -n "${REDIS_DB}" ]]; then
    redis_args+=(-n "${REDIS_DB}")
fi

# write_one userId payload — push one SET entry under key UserData:<userId>.
# v0.7.4: cookieId header value equals userId; no JSON-quoted field wrap.
write_one() {
    local user_id=$1 payload=$2
    redis-cli "${redis_args[@]}" SET "UserData:${user_id}" "${payload}" >/dev/null
}

if [[ -n "${COOKIE_FILE:-}" ]]; then
    if ! command -v jq >/dev/null 2>&1; then
        echo "COOKIE_FILE requires jq — brew install jq" >&2
        exit 1
    fi
    count=0
    while IFS= read -r record; do
        user_id=$(jq -r '.userId // .id' <<<"${record}")
        write_one "${user_id}" "${record}"
        count=$((count + 1))
        echo "seeded userId=${user_id} (record from ${COOKIE_FILE})"
    done < <(jq -c '.[]' "${COOKIE_FILE}")
    echo "done — ${count} fixtures written to ${REDIS_HOST} db=${REDIS_DB}"
    exit 0
fi

# Built-in pre fixture: 张立超 — verified against cses-server-pre-0 login.
# v0.7.4 shape: id=userId on top, companyId / orgId nested in organizes[].
read -r -d '' ZHANGLICHAO <<'JSON' || true
{"id":"676cc4ccfbbc501161d5cd65","mobile":"17692704771","name":"张立超","userName":"张立超","userId":"","organizes":[{"companyId":"6111fb0a202d425d221c53db","companyName":"中企云链（北京）信息科技有限公司","deptId":"616cee6ef7a6ae6354cddd9b","deptName":"技术部","orgId":"6311a17c50c75d009ed3864f","orgName":"后端开发","orgType":"Member","userId":"676cc4ccfbbc501161d5cd65","userName":"张立超"}]}
JSON

write_one "676cc4ccfbbc501161d5cd65" "${ZHANGLICHAO}"
echo "seeded built-in pre fixture: userId=676cc4ccfbbc501161d5cd65 (张立超)"
echo "smoke (cookieId header == userId, companyId header required):"
echo "  curl -H 'cookieId: 676cc4ccfbbc501161d5cd65' \\"
echo "       -H 'companyId: 6111fb0a202d425d221c53db' \\"
echo "       \"http://localhost:38080/api/auth/me\""

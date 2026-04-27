#!/usr/bin/env bash
# seed-mm-cookies.sh — populate the upstream cses Redis HASH "User" with
# Mattermost-shaped fixtures so im handlers can authenticate cookieId-only
# requests during local / pre integration runs.
#
# This is the manual flip-side of internal/testutil.CookieFixture: the same
# wire shape, but for use with `IM_GATEWAY=...` against a port-forwarded
# cses Redis (or the local docker-compose redis if you flipped IM to it).
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
#     {"cookieId":"...","userId":"...","companyId":"...","userName":"..."},
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

# write_one cookieId payload — push one HSet entry, with the cookieId
# wrapped in JSON quotes (matches cses-server's Java writer).
write_one() {
    local cookie_id=$1 payload=$2
    local field="\"${cookie_id}\""
    redis-cli "${redis_args[@]}" HSET User "${field}" "${payload}" >/dev/null
}

if [[ -n "${COOKIE_FILE:-}" ]]; then
    if ! command -v jq >/dev/null 2>&1; then
        echo "COOKIE_FILE requires jq — brew install jq" >&2
        exit 1
    fi
    count=0
    while IFS= read -r record; do
        cookie_id=$(jq -r '.cookieId' <<<"${record}")
        write_one "${cookie_id}" "${record}"
        count=$((count + 1))
        echo "seeded cookieId=${cookie_id} (record from ${COOKIE_FILE})"
    done < <(jq -c '.[]' "${COOKIE_FILE}")
    echo "done — ${count} fixtures written to ${REDIS_HOST} db=${REDIS_DB}"
    exit 0
fi

# Built-in pre fixture: 张立超 — verified against cses-server-pre-0 login response.
# Reusable across unit tests, scripts/e2e-pre.mjs, and manual cli replay.
read -r -d '' ZHANGLICHAO <<'JSON' || true
{"id":"676cc4ccfbbc501161d5cd65","userId":"676cc4ccfbbc501161d5cd65","userName":"张立超","name":"张立超","email":"","mobile":"17692704771","companyId":"6111fb0a202d425d221c53db","deptId":"616cee6ef7a6ae6354cddd9b","deptName":"技术部","orgId":"6311a17c50c75d009ed3864f","orgName":"后端开发","orgRole":"Member","openId":"","unionId":"","roles":["Member"],"sex":0,"state":0,"orgState":4,"updateTime":0,"isTeacher":false,"inJinQi":false,"trace":false}
JSON

write_one "69eec6dbe6876865ff98945a" "${ZHANGLICHAO}"
echo "seeded built-in pre fixture: cookieId=69eec6dbe6876865ff98945a userId=676cc4ccfbbc501161d5cd65 (张立超)"
echo "smoke: curl -H 'cookieId: 69eec6dbe6876865ff98945a' \"http://localhost:38080/api/auth/me\""

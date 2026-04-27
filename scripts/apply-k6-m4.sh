#!/usr/bin/env bash
# apply-k6-m4.sh — M4 cookie-auth k6 driver.
#
# 1. Bulk-seeds N cookies into the (port-forwarded) pre Redis with
#    server/scripts/seed-mm-cookies-bulk.sh.
# 2. Loads the resulting CSV + scripts/v4-load-m4.js into the im-k6-script
#    ConfigMap (one Job per file mount).
# 3. (Re)kicks the im-k6-load Job and tails logs.
#
# Replaces apply-k6.sh for v0.6.x+. The old script depended on
# /api/auth/register + /login + JWT, all retired in M4.
#
# Usage:
#   IM_REDIS=localhost:26379 IM_GATEWAY=http://localhost:38080 \
#       TARGET_VUS=300 ./scripts/apply-k6-m4.sh
#
# Env knobs:
#   NAMESPACE      default im-v2
#   IM_REDIS       host:port for bulk-seed (must be reachable from this machine)
#   N              cookie pool size (default = TARGET_VUS)
#   TARGET_VUS     k6 ramping VU target (default 300)
#   RAMP_SEC       default 60
#   SOAK_SEC       default 180
#   DOWN_SEC       default 30
#   SEND_PER_ITER  default 3
#   SKIP_SEED      1 = reuse existing CSV (default empty: re-seed)
set -euo pipefail

NS="${NAMESPACE:-im-v2}"
SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
REPO_ROOT="$(cd "$SCRIPT_DIR/.." && pwd)"
SEED_SCRIPT="$REPO_ROOT/server/scripts/seed-mm-cookies-bulk.sh"
K6_SCRIPT="$SCRIPT_DIR/v4-load-m4.js"
JOB_YAML="$REPO_ROOT/deploy/k8s/60-k6-loadtest.yaml"
COOKIE_CSV="${COOKIE_CSV:-/tmp/im-k6-cookies.csv}"

TARGET_VUS="${TARGET_VUS:-300}"
N="${N:-$TARGET_VUS}"
RAMP_SEC="${RAMP_SEC:-60}"
SOAK_SEC="${SOAK_SEC:-180}"
DOWN_SEC="${DOWN_SEC:-30}"
SEND_PER_ITER="${SEND_PER_ITER:-3}"

if [[ "${SKIP_SEED:-0}" != "1" ]]; then
    : "${IM_REDIS:?IM_REDIS required (e.g. localhost:26379 with port-forward)}"
    echo "==> bulk-seeding $N cookies into $IM_REDIS"
    IM_REDIS="$IM_REDIS" N="$N" OUT="$COOKIE_CSV" "$SEED_SCRIPT"
fi

[[ -s "$COOKIE_CSV" ]] || { echo "$COOKIE_CSV is empty; aborting" >&2; exit 2; }
echo "==> cookie pool: $(wc -l <"$COOKIE_CSV") rows from $COOKIE_CSV"

echo "==> refreshing configmap im-k6-script (script + cookies)"
kubectl -n "$NS" create configmap im-k6-script \
    --from-file=v4-load-m4.js="$K6_SCRIPT" \
    --from-file=cookies.csv="$COOKIE_CSV" \
    --dry-run=client -o yaml | kubectl apply -f -

echo "==> deleting existing Job (if any)"
kubectl -n "$NS" delete job im-k6-load --ignore-not-found --wait=true >/dev/null

# Render + apply Job patched to point at the new script + add the M4 env knobs.
TMP_JOB=$(mktemp)
trap 'rm -f "$TMP_JOB"' EXIT
awk '/^---$/ {p=1; next} p' "$JOB_YAML" \
  | sed \
        -e 's#v4-load.js#v4-load-m4.js#g' \
        -e "s#__TARGET_VUS__#$TARGET_VUS#g" \
        -e "s#__RAMP_SEC__#$RAMP_SEC#g" \
        -e "s#__SOAK_SEC__#$SOAK_SEC#g" \
        -e "s#__DOWN_SEC__#$DOWN_SEC#g" \
  | yq eval '
        (.spec.template.spec.containers[0].env) +=
            [{"name":"COOKIE_CSV","value":"/scripts/cookies.csv"},
             {"name":"SEND_PER_ITER","value":"'"$SEND_PER_ITER"'"}]
    ' - > "$TMP_JOB" 2>/dev/null || {
        # yq missing — fall back to plain apply (env knobs read from
        # ConfigMap-baked defaults in the script).
        awk '/^---$/ {p=1; next} p' "$JOB_YAML" \
          | sed -e 's#v4-load.js#v4-load-m4.js#g' \
                -e "s#__TARGET_VUS__#$TARGET_VUS#g" \
                -e "s#__RAMP_SEC__#$RAMP_SEC#g" \
                -e "s#__SOAK_SEC__#$SOAK_SEC#g" \
                -e "s#__DOWN_SEC__#$DOWN_SEC#g" > "$TMP_JOB"
    }
kubectl apply -f "$TMP_JOB"

echo "==> tailing job logs (Ctrl-C detaches; job keeps running)"
kubectl -n "$NS" wait --for=condition=Ready pod -l app.kubernetes.io/component=load-test --timeout=60s || true
kubectl -n "$NS" logs -f job/im-k6-load || true

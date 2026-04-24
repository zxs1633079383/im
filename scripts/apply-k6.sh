#!/usr/bin/env bash
# apply-k6.sh — load scripts/v4-load.js into the im-v2 ConfigMap and (re)kick
# the k6 Job. Run once after the script changes; to re-run with a fresh
# batch of users delete the Job and apply again.
#
# Usage:
#   scripts/apply-k6.sh            # default TARGET_VUS=500, 3min soak
#   TARGET_VUS=5000 scripts/apply-k6.sh

set -euo pipefail

NS="${NAMESPACE:-im-v2}"
SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
REPO_ROOT="$(cd "$SCRIPT_DIR/.." && pwd)"
K6_SCRIPT="$REPO_ROOT/scripts/v4-load.js"
JOB_YAML="$REPO_ROOT/deploy/k8s/60-k6-loadtest.yaml"

# TARGET_VUS defaults to the ConfigMap-baked value if caller didn't override.
TARGET_VUS="${TARGET_VUS:-500}"
SEED_USER_COUNT="${SEED_USER_COUNT:-$TARGET_VUS}"

# Seed step — ensures the user population exists so k6 VUs can login-only.
# Expects a port-forward on localhost:38080. Skip with SKIP_SEED=1.
if [[ "${SKIP_SEED:-0}" != "1" ]]; then
    echo "==> seeding $SEED_USER_COUNT users (SKIP_SEED=1 to skip)"
    IM_GATEWAY="${IM_GATEWAY:-http://localhost:38080}" \
    SEED_USER_COUNT="$SEED_USER_COUNT" \
        bash "$SCRIPT_DIR/seed-users.sh"
fi

echo "==> refreshing configmap im-k6-script in $NS"
kubectl -n "$NS" create configmap im-k6-script \
    --from-file=v4-load.js="$K6_SCRIPT" \
    --dry-run=client -o yaml \
  | kubectl apply -f -

echo "==> deleting existing Job (if any)"
kubectl -n "$NS" delete job im-k6-load --ignore-not-found --wait=true

# Extract only the Job from the multi-doc manifest so the ConfigMap we just
# wrote doesn't get clobbered by the placeholder.
echo "==> applying Job"
awk '/^---$/ {p=1; next} p' "$JOB_YAML" | kubectl apply -f -

# TARGET_VUS is baked into the Job via an env patch before create; at this
# point the Job exists already, so use `kubectl set env` — but on some
# clusters the Job pod template is immutable once created. Workaround: patch
# the ConfigMap'd ENV via a throwaway Deployment or delete+recreate. Simplest
# — the Job yaml uses `value: "500"` which is authoritative unless overridden.
# To change VUs, edit the yaml and re-apply, or delete the Job first.
echo "==> TARGET_VUS (Job baseline) = 500; override at yaml level if needed."

echo "==> streaming logs (Ctrl-C to detach; job keeps running)"
kubectl -n "$NS" wait --for=condition=Ready pod -l app.kubernetes.io/component=load-test --timeout=60s || true
kubectl -n "$NS" logs -f job/im-k6-load || true

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

if [[ -n "${TARGET_VUS:-}" ]]; then
    kubectl -n "$NS" set env job/im-k6-load TARGET_VUS="$TARGET_VUS" || true
fi

echo "==> streaming logs (Ctrl-C to detach; job keeps running)"
kubectl -n "$NS" wait --for=condition=Ready pod -l app.kubernetes.io/component=load-test --timeout=60s || true
kubectl -n "$NS" logs -f job/im-k6-load || true

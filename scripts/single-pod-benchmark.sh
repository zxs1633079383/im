#!/usr/bin/env bash
# single-pod-benchmark.sh — isolate gateway to a single replica, disable HPA,
# run the full-chain k6 loop at increasing VU counts to characterize the true
# per-pod ceiling. Restores HPA + replica count at the end (even on error).
#
# Usage:
#   kubectl -n im-v2 port-forward svc/im-gateway 38080:8080 &
#   scripts/single-pod-benchmark.sh
#
# Knobs:
#   VU_LEVELS  default "50 100 200 400"
#   SOAK_SEC   default 60
#   RAMP_SEC   default 30
#   DOWN_SEC   default 20

set -uo pipefail

NS="${NAMESPACE:-im-v2}"
SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
REPO_ROOT="$(cd "$SCRIPT_DIR/.." && pwd)"
REPORT_DIR="$REPO_ROOT/server/docs/benchmark"
K6_SCRIPT="$SCRIPT_DIR/fullchain-load.js"
JOB_YAML="$REPO_ROOT/deploy/k8s/60-k6-loadtest.yaml"
HPA_YAML="$REPO_ROOT/deploy/k8s/rendered/40-hpa.yaml"

VU_LEVELS="${VU_LEVELS:-50 100 200 400}"
SOAK_SEC="${SOAK_SEC:-60}"
RAMP_SEC="${RAMP_SEC:-30}"
DOWN_SEC="${DOWN_SEC:-20}"
RUN_STAMP="$(date +%Y-%m-%d-%H%M)-solo"

mkdir -p "$REPORT_DIR"

# --- save HPA state then disable it ---
echo "==> snapshotting HPA and scaling down to 1 replica"
HPA_BACKUP="$(mktemp)"
kubectl -n "$NS" get hpa im-gateway -o yaml > "$HPA_BACKUP" 2>/dev/null || true
kubectl -n "$NS" delete hpa im-gateway --ignore-not-found >/dev/null
kubectl -n "$NS" scale deploy/im-gateway --replicas=1 >/dev/null
kubectl -n "$NS" rollout status deploy/im-gateway --timeout=120s >/dev/null

restore() {
    echo "==> restoring HPA + replicas=3"
    if [[ -s "$HPA_BACKUP" ]]; then
        kubectl apply -f "$HPA_BACKUP" >/dev/null || true
    fi
    kubectl -n "$NS" scale deploy/im-gateway --replicas=3 >/dev/null || true
    rm -f "$HPA_BACKUP"
}
trap restore EXIT

for VU in $VU_LEVELS; do
    REPORT="$REPORT_DIR/$RUN_STAMP-VU${VU}.md"
    echo
    echo "========================================"
    echo "  Single-pod round VU=$VU  report=$REPORT"
    echo "========================================"

    # Refresh script ConfigMap (fullchain with shared-peer setup()).
    kubectl -n "$NS" create configmap im-k6-script \
        --from-file=v4-load.js="$K6_SCRIPT" \
        --dry-run=client -o yaml | kubectl apply -f - >/dev/null

    kubectl -n "$NS" delete job im-k6-load --ignore-not-found --wait=true >/dev/null

    PEER_POOL_VAL=$(( VU < 50 ? VU : 50 ))
    awk '/^---$/ {p=1; next} p' "$JOB_YAML" \
      | sed \
          -e "s|__TARGET_VUS__|$VU|g" \
          -e "s|__RAMP_SEC__|$RAMP_SEC|g" \
          -e "s|__SOAK_SEC__|$SOAK_SEC|g" \
          -e "s|__DOWN_SEC__|$DOWN_SEC|g" \
          -e "s|__PEER_POOL__|$PEER_POOL_VAL|g" \
      | kubectl apply -f - >/dev/null

    kubectl -n "$NS" wait --for=condition=Ready pod \
        -l app.kubernetes.io/component=load-test --timeout=120s >/dev/null || true
    POD=$(kubectl -n "$NS" get pods -l app.kubernetes.io/component=load-test \
        -o jsonpath='{.items[0].metadata.name}')

    LOG_TMP="$(mktemp)"
    kubectl -n "$NS" logs -f "$POD" > "$LOG_TMP" 2>&1 &
    LOG_PID=$!

    DEADLINE=$(( SOAK_SEC + RAMP_SEC + DOWN_SEC + 60 ))
    kubectl -n "$NS" wait --for=condition=complete job/im-k6-load \
        --timeout="${DEADLINE}s" >/dev/null 2>&1 || \
    kubectl -n "$NS" wait --for=condition=failed job/im-k6-load \
        --timeout=10s >/dev/null 2>&1 || true
    kill "$LOG_PID" 2>/dev/null || true
    wait "$LOG_PID" 2>/dev/null || true

    # Capture single-pod peak CPU just before k6 ramp-down.
    CPU_TOP=$(kubectl -n "$NS" top pods 2>/dev/null | awk '/im-gateway-/ {cpu=$2; sub("m","",cpu); print cpu}' | head -1)
    POD_CPU_PCT=$(( (${CPU_TOP:-0} * 100) / 2000 ))

    K6_SUMMARY=$(grep -E '^\s*im_|^\s*http_req|^\s*checks|^\s*iterations|^\s*ws_|level=(error|warn)' "$LOG_TMP" | head -60)

    {
        echo "# Single-Pod Benchmark VU=$VU — $RUN_STAMP"
        echo
        echo "Image: harbor.jinqidongli.com/x9-go/im/im-gateway:v1.0.0-pre-3"
        echo "Replicas: 1  HPA: disabled"
        echo "pg.max_conns: 50  pg.max_idle: 25  peer: shared"
        echo
        echo "| field | value |"
        echo "|---|---|"
        echo "| VU level | $VU |"
        echo "| ramp / soak / down | ${RAMP_SEC}s / ${SOAK_SEC}s / ${DOWN_SEC}s |"
        echo "| Peak gateway CPU (m) | ${CPU_TOP:-n/a} |"
        echo "| Peak CPU % of pod limit | ${POD_CPU_PCT}% |"
        echo
        echo "## k6 summary"
        echo '```'
        echo "$K6_SUMMARY"
        echo '```'
        echo
        echo "## Raw log tail"
        echo '```'
        tail -40 "$LOG_TMP"
        echo '```'
    } > "$REPORT"
    echo "   report written: $REPORT"

    rm -f "$LOG_TMP"
    kubectl -n "$NS" delete job im-k6-load --wait=false >/dev/null 2>&1 || true

    # Stop early if the single pod is melting down.
    if [[ "$POD_CPU_PCT" -ge 90 ]]; then
        echo "==> STOP: single-pod CPU at ${POD_CPU_PCT}% — hit ceiling"
        echo -e "\n---\n**Loop stopped: single-pod CPU ≥ 90%.**" >> "$REPORT"
        break
    fi

    echo "   cooldown 30s..."
    for _ in $(seq 1 6); do sleep 5; done
done

echo
echo "done. reports:"
ls -la "$REPORT_DIR/" | grep "$RUN_STAMP" || true

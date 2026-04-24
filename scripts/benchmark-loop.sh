#!/usr/bin/env bash
# benchmark-loop.sh — autonomous full-chain load-test loop.
#
# Per round:
#   1. (Re)seed the peer user pool to cover the target VU count.
#   2. Rebuild the im-k6-script ConfigMap with the full-chain script.
#   3. Patch TARGET_VUS + RAMP_SEC + SOAK_SEC into the Job yaml and apply.
#   4. Stream k6 logs until the Job terminates (pass or fail).
#   5. Snapshot gateway HPA / pod CPU / errors from logs.
#   6. Write server/docs/benchmark/YYYY-MM-DD-HHMM-VU${N}.md.
#   7. Stop if gateway CPU sum > 80% of total requests, HPA ≥ 15 pods, or
#      k6 action-ok rate < 0.95 — we're approaching pre-cluster limits.
#
# Run once from the repo root with a kubectl context on pre:
#   kubectl -n im-v2 port-forward svc/im-gateway 38080:8080 &
#   scripts/benchmark-loop.sh
#
# Knobs:
#   VU_LEVELS  = "100 300 800 1500"   space-separated gradient
#   SOAK_SEC   = 60                   per-round soak duration
#   RAMP_SEC   = 30
#   DOWN_SEC   = 30

# Intentionally NOT using `-e`: one round's empty `kubectl top` output must
# not abort the whole gradient. We handle errors per-step and keep going.
set -uo pipefail

NS="${NAMESPACE:-im-v2}"
SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
REPO_ROOT="$(cd "$SCRIPT_DIR/.." && pwd)"
REPORT_DIR="$REPO_ROOT/server/docs/benchmark"
K6_SCRIPT="$SCRIPT_DIR/fullchain-load.js"
JOB_YAML="$REPO_ROOT/deploy/k8s/60-k6-loadtest.yaml"

VU_LEVELS="${VU_LEVELS:-100 300 800 1500}"
SOAK_SEC="${SOAK_SEC:-60}"
RAMP_SEC="${RAMP_SEC:-30}"
DOWN_SEC="${DOWN_SEC:-30}"
RUN_STAMP="$(date +%Y-%m-%d-%H%M)"

mkdir -p "$REPORT_DIR"
echo "benchmark run $RUN_STAMP starting. reports -> $REPORT_DIR/"

# Seed the max VU count once; each round reuses the pool.
MAX_VU=$(echo "$VU_LEVELS" | tr ' ' '\n' | sort -rn | head -1)
if [[ "${SKIP_SEED:-0}" == "1" ]]; then
    echo "==> SKIP_SEED=1 — skipping seed (pool expected to already exist)"
else
    export SEED_USER_COUNT="$MAX_VU"
    export SEED_PARALLELISM="${SEED_PARALLELISM:-15}"
    echo "==> seeding $MAX_VU users (once)"
    bash "$SCRIPT_DIR/seed-users.sh" || { echo "seed failed"; exit 2; }
fi

for VU in $VU_LEVELS; do
    REPORT="$REPORT_DIR/$RUN_STAMP-VU${VU}.md"
    echo
    echo "========================================"
    echo "  Round VU=$VU  report=$REPORT"
    echo "========================================"

    # 1. Refresh ConfigMap with full-chain script.
    kubectl -n "$NS" create configmap im-k6-script \
        --from-file=v4-load.js="$K6_SCRIPT" \
        --dry-run=client -o yaml | kubectl apply -f - >/dev/null

    # 2. Delete existing Job + re-render from placeholder yaml.
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

    # 3. Wait for pod + stream logs into a buffer.
    echo "waiting for k6 pod..."
    kubectl -n "$NS" wait --for=condition=Ready pod \
        -l app.kubernetes.io/component=load-test --timeout=120s >/dev/null || true
    POD=$(kubectl -n "$NS" get pods -l app.kubernetes.io/component=load-test \
        -o jsonpath='{.items[0].metadata.name}')

    LOG_TMP="$(mktemp)"
    kubectl -n "$NS" logs -f "$POD" > "$LOG_TMP" 2>&1 &
    LOG_PID=$!

    # 4. Wait for Job completion (or timeout).
    DEADLINE=$(( SOAK_SEC + RAMP_SEC + DOWN_SEC + 60 ))
    kubectl -n "$NS" wait --for=condition=complete job/im-k6-load \
        --timeout="${DEADLINE}s" >/dev/null 2>&1 || \
    kubectl -n "$NS" wait --for=condition=failed job/im-k6-load \
        --timeout=10s >/dev/null 2>&1 || true
    kill "$LOG_PID" 2>/dev/null || true
    wait "$LOG_PID" 2>/dev/null || true

    # 5. Snapshot cluster state AT THE END of the round.
    HPA_STATE=$(kubectl -n "$NS" get hpa im-gateway -o jsonpath='{.status.desiredReplicas}/{.status.currentReplicas}' 2>&1 || echo "n/a")
    HPA_REPLICAS=$(kubectl -n "$NS" get hpa im-gateway -o jsonpath='{.status.currentReplicas}' 2>&1 || echo 0)
    CPU_TOP=$(kubectl -n "$NS" top pods 2>/dev/null | awk '/im-gateway-/ {cpu=$2; sub("m","",cpu); sum+=cpu; cnt++} END{if(cnt)print sum" "cnt}')
    CPU_SUM=$(echo "$CPU_TOP" | awk '{print $1}')
    CPU_POD_CNT=$(echo "$CPU_TOP" | awk '{print $2}')
    CPU_SUM="${CPU_SUM:-0}"
    CPU_POD_CNT="${CPU_POD_CNT:-0}"
    K6_SUMMARY=$(tail -60 "$LOG_TMP" | grep -E '^\s*im_|^\s*http_req|^\s*checks|^\s*iterations|^\s*data_|^\s*ws_|^\s*push_latency|^\s*level=(error|warn)' | head -80)

    # Threshold check.
    STOP_REASON=""
    if [[ -n "$HPA_REPLICAS" && "$HPA_REPLICAS" -ge 15 ]]; then
        STOP_REASON="HPA scaled to $HPA_REPLICAS pods (near max 20)"
    fi
    if [[ -n "$CPU_SUM" && "$CPU_POD_CNT" -gt 0 ]]; then
        CPU_LIMIT=$(( CPU_POD_CNT * 2000 ))   # 2000m per pod from manifest
        CPU_PCT=$(( CPU_SUM * 100 / CPU_LIMIT ))
        if [[ "$CPU_PCT" -ge 80 ]]; then
            STOP_REASON="gateway CPU at ${CPU_PCT}% of cluster limit"
        fi
    fi

    # 6. Emit the round's report.
    {
        echo "# Benchmark VU=$VU — $RUN_STAMP"
        echo
        echo "| field | value |"
        echo "|---|---|"
        echo "| VU level | $VU |"
        echo "| ramp / soak / down | ${RAMP_SEC}s / ${SOAK_SEC}s / ${DOWN_SEC}s |"
        echo "| gateway pods (end) | $HPA_STATE |"
        echo "| CPU sum (m) / pods | ${CPU_SUM:-n/a} / ${CPU_POD_CNT:-0} |"
        echo "| CPU % of cluster limit | ${CPU_PCT:-n/a}% |"
        echo "| stop-reason | ${STOP_REASON:-none} |"
        echo
        echo "## k6 summary (tail)"
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

    if [[ -n "$STOP_REASON" ]]; then
        echo "==> STOP: $STOP_REASON"
        {
            echo
            echo "---"
            echo "**Loop stopped here — subsequent VU levels skipped to protect pre cluster.**"
        } >> "$REPORT"
        break
    fi

    # Cooldown: let HPA scale down + Redis TTL expire stale routing.
    echo "   cooldown 45s before next round..."
    for _ in $(seq 1 9); do sleep 5; done
done

echo
echo "benchmark loop done. reports:"
ls -la "$REPORT_DIR/" | grep "$RUN_STAMP" || true

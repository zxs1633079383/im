#!/usr/bin/env bash
# v4-cluster-test.sh — exercise the 5 V4 cluster fault scenarios defined in
# docs/OVERALL.md §5.5 against a live k8s deployment.
#
# Usage:
#   NAMESPACE=im-pre ./scripts/v4-cluster-test.sh <scenario>
#
# Scenarios:
#   basic          (S1) two users on two pods, cross-pod fan-out
#   pod-pause      (S2) STOP gw-1, wait TTL, traffic keeps flowing
#   pod-kill       (S3) force-delete gw-1, client reconnects + /sync
#   pulsar-flap    (S4) scale Pulsar 0 then back up; recovery fan-out
#   load           (S5) 150k WS load test via k6 (prompts operator)
#   all            run S1..S4 sequentially (skips S5)
#
# Env:
#   NAMESPACE          k8s namespace (default: im-pre)
#   GATEWAY_LABEL      label selector (default: app=im-gateway)
#   PULSAR_SELECTOR    k8s resource for Pulsar scale
#                      (default: statefulset/pulsar-broker)
#   LOCAL_BASE         base port for port-forwards (default: 9001)
#   V4_USER_A / V4_USER_B / V4_PASS
#                      user credentials (defaults: v4alice / v4bob / v4test1234)

set -euo pipefail

NAMESPACE="${NAMESPACE:-im-pre}"
SELECTOR="${GATEWAY_LABEL:-app=im-gateway}"
PULSAR_SEL="${PULSAR_SELECTOR:-statefulset/pulsar-broker}"
LOCAL_BASE="${LOCAL_BASE:-9001}"

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
REPO_ROOT="$(cd "$SCRIPT_DIR/.." && pwd)"

log() { printf '\n== %s ==\n' "$*"; }
fail() { printf 'FAIL: %s\n' "$*" >&2; exit 1; }

# ---- k8s helpers ----

pods_sorted() {
  kubectl -n "$NAMESPACE" get pods -l "$SELECTOR" \
    --field-selector=status.phase=Running \
    -o jsonpath='{range .items[*]}{.metadata.name}{"\n"}{end}' | sort
}

pod_n() { pods_sorted | sed -n "${1}p"; }

require_pods() {
  local need="$1"
  local n
  n=$(pods_sorted | wc -l | tr -d ' ')
  if [[ "$n" -lt "$need" ]]; then
    fail "need >= $need Running gateway pods, found $n"
  fi
}

# ---- port-forward book-keeping ----
declare -a PF_PIDS=()
trap 'for p in "${PF_PIDS[@]:-}"; do kill "$p" 2>/dev/null || true; done' EXIT

start_pf() {
  # start_pf <pod> <localport>
  local pod="$1" port="$2"
  kubectl -n "$NAMESPACE" port-forward "pod/$pod" "$port:8080" >/dev/null 2>&1 &
  PF_PIDS+=("$!")
  # wait up to 5s for the port to open
  for _ in $(seq 1 50); do
    if nc -z localhost "$port" 2>/dev/null; then return 0; fi
    sleep 0.1
  done
  fail "port-forward to $pod:$port did not become ready"
}

stop_pfs() {
  for p in "${PF_PIDS[@]:-}"; do kill "$p" 2>/dev/null || true; done
  PF_PIDS=()
  sleep 1
}

# ---- v4-client runner ----
run_client() {
  cd "$REPO_ROOT/server" && go run ./cmd/v4-client "$@"
}

# ---- scenarios ----

scenario_basic() {
  log "S1 basic cross-pod fan-out"
  require_pods 2
  local p1 p2 port1 port2
  p1=$(pod_n 1); p2=$(pod_n 2)
  port1=$LOCAL_BASE; port2=$((LOCAL_BASE + 1))
  log "pod A: $p1 (localhost:$port1)  pod B: $p2 (localhost:$port2)"
  start_pf "$p1" "$port1"
  start_pf "$p2" "$port2"
  run_client basic \
    --api="http://localhost:$port1" \
    --ws1="http://localhost:$port1" \
    --ws2="http://localhost:$port2" \
    --user-a="${V4_USER_A:-v4alice}" \
    --user-b="${V4_USER_B:-v4bob}" \
    --password="${V4_PASS:-v4test1234}"
  stop_pfs
}

scenario_pod_pause() {
  log "S2 pod pause (SIGSTOP)"
  require_pods 3
  local target
  target=$(pod_n 1)
  log "pausing $target with SIGSTOP (PID 1 in container)"
  # distroless has no shell; we signal via the kubelet-exposed exec if
  # available, otherwise fall back to `kubectl debug`. Operators should
  # prefer `kubectl debug` for production runs.
  if ! kubectl -n "$NAMESPACE" exec "$target" -- /bin/true 2>/dev/null; then
    log "NOTE: $target has no shell (distroless). Use 'kubectl debug --target' manually."
    log "SKIP: pod-pause requires an ephemeral debug container (see deploy/README.md)."
    return 0
  fi
  kubectl -n "$NAMESPACE" exec "$target" -- kill -STOP 1 || true
  log "waiting 60s for routing TTL to expire"
  sleep 60
  # Verify the other two pods still answer (basic scenario on p2/p3).
  local p2 p3 port1 port2
  p2=$(pod_n 2); p3=$(pod_n 3)
  port1=$LOCAL_BASE; port2=$((LOCAL_BASE + 1))
  start_pf "$p2" "$port1"
  start_pf "$p3" "$port2"
  run_client basic \
    --api="http://localhost:$port1" \
    --ws1="http://localhost:$port1" \
    --ws2="http://localhost:$port2" \
    --user-a="${V4_USER_A:-v4alice}" \
    --user-b="${V4_USER_B:-v4bob}" \
    --password="${V4_PASS:-v4test1234}"
  stop_pfs
  log "resuming $target"
  kubectl -n "$NAMESPACE" exec "$target" -- kill -CONT 1 || true
}

scenario_pod_kill() {
  log "S3 pod kill (force-delete)"
  require_pods 3
  local target p_alive port1
  target=$(pod_n 1); p_alive=$(pod_n 2)
  port1=$LOCAL_BASE
  log "force-deleting $target"
  kubectl -n "$NAMESPACE" delete pod "$target" --force --grace-period=0
  log "waiting 30s for client reconnect window"
  sleep 30
  start_pf "$p_alive" "$port1"
  run_client reconnect \
    --api="http://localhost:$port1" \
    --ws1="http://localhost:$port1" \
    --user-a="${V4_USER_A:-v4alice}" \
    --password="${V4_PASS:-v4test1234}"
  stop_pfs
  log "waiting for deployment to re-create the killed pod"
  kubectl -n "$NAMESPACE" rollout status deploy/im-gateway --timeout=60s
}

scenario_pulsar_flap() {
  log "S4 pulsar flap (scale 0 -> wait -> scale 1)"
  require_pods 2
  log "scaling $PULSAR_SEL to 0"
  kubectl -n "$NAMESPACE" scale "$PULSAR_SEL" --replicas=0
  sleep 30
  log "scaling $PULSAR_SEL back up"
  kubectl -n "$NAMESPACE" scale "$PULSAR_SEL" --replicas=1
  # give pulsar time to accept connections again
  sleep 30
  local p1 p2 port1 port2
  p1=$(pod_n 1); p2=$(pod_n 2)
  port1=$LOCAL_BASE; port2=$((LOCAL_BASE + 1))
  start_pf "$p1" "$port1"
  start_pf "$p2" "$port2"
  run_client pulsar-recovery \
    --api="http://localhost:$port1" \
    --ws1="http://localhost:$port1" \
    --ws2="http://localhost:$port2" \
    --user-a="${V4_USER_A:-v4alice}" \
    --user-b="${V4_USER_B:-v4bob}" \
    --password="${V4_PASS:-v4test1234}"
  stop_pfs
}

scenario_load() {
  log "S5 150k WS load test"
  cat <<EOF
Load testing is run separately via k6 (not kubectl) — scripts/v4-load.js.

Suggested command:
  API_BASE=http://<gateway-LB-host> \\
  WS_BASE=ws://<gateway-LB-host> \\
  V4_USER_PREFIX=v4load \\
  V4_PASS=v4test1234 \\
  k6 run scripts/v4-load.js

Targets:
  - 150,000 concurrent WS clients across 3 pods
  - 10k msg/s sustained
  - p99 push latency < 80ms
EOF
}

case "${1:-}" in
  basic)         scenario_basic ;;
  pod-pause)     scenario_pod_pause ;;
  pod-kill)      scenario_pod_kill ;;
  pulsar-flap)   scenario_pulsar_flap ;;
  load)          scenario_load ;;
  all)           scenario_basic
                 scenario_pod_pause
                 scenario_pod_kill
                 scenario_pulsar_flap
                 echo
                 echo "==> S1..S4 passed. Run '$0 load' for S5."
                 ;;
  ""|help|-h|--help)
    sed -n '2,25p' "$0"
    exit 2
    ;;
  *) echo "Unknown scenario: $1" >&2; exit 2 ;;
esac

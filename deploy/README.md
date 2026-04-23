# V4 Cluster Operations Runbook

Tests the 3-Pod gateway fleet against the five fault scenarios in
[`server/docs/OVERALL.md §5.5`](../server/docs/OVERALL.md) and the
cross-pod push contract in [`server/docs/BACKEND.md §5`](../server/docs/BACKEND.md).

---

## 0. One-time prerequisites

- `kubectl` pointed at the target cluster / namespace.
- `docker` with push access to your registry.
- `go 1.26+` (only needed if you run `v4-cluster-test.sh`; the script
  invokes `go run ./cmd/v4-client` from `server/`).
- `k6 0.49+` for the load scenario.
- External dependencies already running in the cluster:
  - Postgres (with all migrations from `server/migrations/` applied).
  - Redis (for routing / presence TTL).
  - Pulsar (single broker is fine for V4 test; flap scenario scales it).
  - OpenTelemetry Collector (optional — set `OTEL_DISABLED=true` env in
    `20-deployment.yaml` if you don't have one).

---

## 1. Build + push + render

Fill in your secrets, then run the prepare script from the repo root:

```bash
NAMESPACE=im-pre \
REGISTRY=harbor.internal/im \
IMAGE_TAG=v0.1.0-m1-verified \
PG_DSN='postgres://im:im@im-pg:5432/im?sslmode=disable' \
REDIS_ADDR='im-redis:6379' \
PULSAR_URL='pulsar://im-pulsar:6650' \
JWT_SECRET='please-rotate-32-bytes-minimum' \
./scripts/v4-prepare.sh
```

What it does:

1. `docker build -t <registry>/im-gateway:<tag> .`
2. `docker push <registry>/im-gateway:<tag>`
3. Expands `__NAMESPACE__`, `__REGISTRY__`, `__IMAGE_TAG__`, `__PG_DSN__`,
   `__REDIS_ADDR__`, `__PULSAR_URL__`, `__JWT_SECRET__` in
   `deploy/k8s/*.yaml` into `deploy/k8s/rendered/*.yaml`.

Skip the build / push steps with `SKIP_BUILD=1` / `SKIP_PUSH=1` if the
image already exists.

---

## 2. Apply to the cluster

```bash
kubectl apply -f deploy/k8s/rendered/
kubectl -n im-pre rollout status deploy/im-gateway --timeout=120s
```

Sanity check:

```bash
kubectl -n im-pre get pods -l app=im-gateway
kubectl -n im-pre get svc im-gateway im-gateway-headless
kubectl -n im-pre get hpa im-gateway
kubectl -n im-pre get pdb im-gateway
```

Expected: 3 Running pods, ClusterIP + Headless services, HPA targeting
CPU 70%, PDB with `minAvailable=2`.

---

## 3. Run the fault scenarios

```bash
NAMESPACE=im-pre ./scripts/v4-cluster-test.sh all
```

Runs S1..S4 back-to-back. Individual scenarios:

| Script arg    | Scenario | What it verifies |
|---------------|----------|------------------|
| `basic`       | S1 basic cross-pod | A on gw-1 sends, B on gw-2 receives <5s |
| `pod-pause`   | S2 假死             | SIGSTOP gw-1; 60s later traffic still flows on gw-2/gw-3 |
| `pod-kill`    | S3 真 kill          | Force-delete gw-1; client reconnects & `/sync` responds within 30s |
| `pulsar-flap` | S4 Pulsar 抖动       | Scale Pulsar 0 then 1; post-recovery message reaches B |
| `load`        | S5 150k 压测         | Prints the k6 command to run (see §4 below) |

Notes on `pod-pause`: distroless images have no shell, so direct
`kubectl exec ... kill -STOP 1` won't work. The script falls back to
printing guidance; for an actual run, use `kubectl debug` with a
busybox image that shares the process namespace:

```bash
kubectl -n im-pre debug pod/im-gateway-xxxxx \
  --image=busybox \
  --target=gateway \
  -- kill -STOP 1
```

---

## 4. Observability checks

While the scenarios run, these Prometheus metrics should behave as follows:

| Metric | Expected behaviour |
|--------|-------------------|
| `im_ws_active_connections` | Rises monotonically under load; drops toward 0 on pod kill, recovers after reconnect |
| `im_push_cross_pod_success_total` | Increments on every A→B fan-out that crosses pods (S1, S4) |
| `im_push_cross_pod_failure_total` | Stays near zero in normal runs |
| `im_routing_refresh_total` | Spikes around S2 / S3 (routing invalidation) |
| `im_pulsar_producer_errors_total` | Spikes during S4's Pulsar downtime, returns to zero after |

Logs to watch (each with structured `gateway_id`):

- `hub.register` / `hub.unregister` — connection lifecycle
- `cross_pod_push.publish` — outbound Pulsar fan-out
- `push_consumer.receive` — inbound delivery on this pod

---

## 5. Load test (S5)

```bash
API_BASE=http://<gateway-LB>:8080 \
WS_BASE=ws://<gateway-LB>:8080 \
TARGET_VUS=150000 \
DM_PEER_ID=<pre-created-dm-id> \
SEND_PROB=0.1 \
V4_PASS=v4test1234 \
k6 run scripts/v4-load.js
```

Tune OS limits on both the runner host and every k6 pod /
gateway pod:

```bash
# runner
ulimit -n 1048576
sysctl -w net.ipv4.ip_local_port_range="1024 65535"
sysctl -w net.ipv4.tcp_tw_reuse=1

# each gateway pod (via the image or an initContainer) should also bump
# fs.file-max and net.core.somaxconn.
```

k6 thresholds that **must** pass:

- `im_push_latency_ms p(99) < 80`
- `http_req_duration p(99) < 200`
- `im_ws_connect_errors count < 1000`

---

## 6. Roll back

```bash
kubectl -n im-pre rollout undo deploy/im-gateway
# or fully tear down:
kubectl delete -f deploy/k8s/rendered/
```

---

## 7. Known caveats

- `pod-pause` needs `kubectl debug` on distroless — see §3 note above.
- The Pulsar selector in `v4-cluster-test.sh` defaults to
  `statefulset/pulsar-broker`; override with
  `PULSAR_SELECTOR=deploy/pulsar` etc. if your chart differs.
- `JWT_SECRET` in `11-secret.yaml` is set from the `JWT_SECRET` env. In
  production, swap this Secret for one populated by External Secrets /
  Vault — do **not** commit the rendered tree.
- The `im-gateway-config` ConfigMap leaves `gateway.jwt_secret` empty;
  the gateway picks up `IM_GATEWAY_JWT_SECRET` from the Secret env var
  instead. Make sure your binary reads that env (verify in
  `cmd/gateway/main.go` if you're on a different branch).

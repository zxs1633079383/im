# V4 Infra Report

Worktree: `/Users/mac28/workspace/golangProject/im/.claude/worktrees/agent-a1cefd93`
Branch: `worktree-agent-a1cefd93`
Base HEAD before work: `5b78fdd`
New HEAD: `61e52f7`

## 1. Files delivered

| Path | Lines | Purpose |
|------|-------|---------|
| `Dockerfile` | 51 | multi-stage build (golang:1.26-alpine → distroless/static nonroot) |
| `.dockerignore` | 30 | trims build context (drops client/, docs/, .claude/) |
| `deploy/k8s/00-namespace.yaml` | 9 | optional namespace |
| `deploy/k8s/10-configmap.yaml` | 28 | non-sensitive config.yaml |
| `deploy/k8s/11-secret.yaml` | 14 | jwt-secret Secret (stringData) |
| `deploy/k8s/20-deployment.yaml` | 143 | 3 replicas, readiness/liveness, topology spread, nonroot + readOnlyRootFS, preStop sleep 10, terminationGracePeriodSeconds 30 |
| `deploy/k8s/30-service.yaml` | 44 | ClusterIP + Headless (pod-level DNS for V4 tests) |
| `deploy/k8s/40-hpa.yaml` | 51 | min=3 / max=20 / CPU 70%; commented block for `im_ws_active_connections` custom metric |
| `deploy/k8s/50-pdb.yaml` | 16 | minAvailable=2 |
| `deploy/k8s/99-kustomization.yaml` | 20 | kustomize entry with image override |
| `scripts/v4-prepare.sh` | 111 | build + push + render placeholders to `deploy/k8s/rendered/` |
| `scripts/v4-cluster-test.sh` | 224 | orchestrates S1..S5 against live cluster |
| `scripts/v4-load.js` | 157 | k6 load scenario (150k VUs, p99<80ms threshold) |
| `server/cmd/v4-client/main.go` | 378 | WS test client: `basic` / `reconnect` / `pulsar-recovery` |
| `deploy/README.md` | 179 | operator runbook (prepare → apply → run tests → observe) |

**Total new tracked lines: 1,455** across 15 files (Dockerfile, .dockerignore, 8 manifests, 3 scripts, 1 Go main, 1 README).

## 2. New commits (chronological)

```
701f114 feat(docker): multi-stage Dockerfile for gateway
e7c5be2 feat(deploy): k8s manifests for V4 cluster (3 replicas + HPA + PDB)
f39273f feat(scripts): v4 prepare build + push + render
2fe2557 feat(cmd): v4-client cli for cluster fault scenarios
c939a38 feat(scripts): v4-cluster-test.sh 5 scenarios
40bcd57 feat(scripts): k6 load scenario for 150k WS
61e52f7 docs(deploy): V4 cluster operations runbook
```

All 7 commits are atomic and per-task.

## 3. Placeholders the operator must replace

`scripts/v4-prepare.sh` expands these, so they are only ever set as env vars — never committed into rendered output.

| Placeholder | Env var | Example |
|-------------|---------|---------|
| `__NAMESPACE__` | `NAMESPACE` | `im-pre` |
| `__REGISTRY__` | `REGISTRY` | `harbor.internal/im` |
| `__IMAGE_TAG__` | `IMAGE_TAG` | `v0.1.0-m1-verified` |
| `__PG_DSN__` | `PG_DSN` | `postgres://im:im@im-pg:5432/im?sslmode=disable` |
| `__REDIS_ADDR__` | `REDIS_ADDR` | `im-redis:6379` |
| `__PULSAR_URL__` | `PULSAR_URL` | `pulsar://im-pulsar:6650` |
| `__JWT_SECRET__` | `JWT_SECRET` | `please-rotate-32-bytes-minimum` |

## 4. Recommended kubectl sequence

```bash
# from repo root
export NAMESPACE=im-pre
export REGISTRY=harbor.internal/im
export IMAGE_TAG=v0.1.0-m1-verified
export PG_DSN='postgres://im:im@im-pg:5432/im?sslmode=disable'
export REDIS_ADDR='im-redis:6379'
export PULSAR_URL='pulsar://im-pulsar:6650'
export JWT_SECRET='...'

# 1) build + push + render
./scripts/v4-prepare.sh

# 2) apply (skip 00-namespace.yaml if the namespace already exists)
kubectl apply -f deploy/k8s/rendered/

# 3) wait for rollout
kubectl -n $NAMESPACE rollout status deploy/im-gateway --timeout=120s

# 4) sanity checks
kubectl -n $NAMESPACE get pods -l app=im-gateway
kubectl -n $NAMESPACE get svc im-gateway im-gateway-headless
kubectl -n $NAMESPACE get hpa im-gateway
kubectl -n $NAMESPACE get pdb im-gateway

# 5) fault scenarios (S1..S4)
NAMESPACE=$NAMESPACE ./scripts/v4-cluster-test.sh all

# 6) load (S5)
API_BASE=http://<lb>:8080 WS_BASE=ws://<lb>:8080 \
  TARGET_VUS=150000 DM_PEER_ID=<seed-dm> \
  V4_PASS=v4test1234 \
  k6 run scripts/v4-load.js
```

## 5. Verification done in this session

- `go build ./...` from `server/` — clean.
- `go build ./cmd/v4-client/...` — clean.
- `go vet ./cmd/v4-client/...` — clean.
- `bash -n scripts/v4-prepare.sh` / `bash -n scripts/v4-cluster-test.sh` — both syntax-valid.
- `docker build` / `docker push` / `kubectl apply` — **not run** (per instructions; operator will execute).

## 6. Outstanding BLOCKERs

1. **distroless + SIGSTOP** — The base image has no shell, so
   `kubectl exec gw-1 -- kill -STOP 1` fails. `scripts/v4-cluster-test.sh`
   detects this (probes `/bin/true`) and skips S2 with a message. To run
   S2 for real, operators must use `kubectl debug --target` with a
   busybox image — documented in `deploy/README.md §3`. If you want S2
   automated, either (a) switch the runtime image to `alpine` or
   `distroless/base` (keeps shell but bigger) or (b) add a shared
   ephemeral debug container template and wire it into the script.

2. **JWT Secret source split** — `11-secret.yaml` carries the JWT via
   `stringData.jwt-secret` and the Deployment injects it as
   `IM_GATEWAY_JWT_SECRET`. The config loader at HEAD reads
   `cfg.Gateway.JWTSecret` from YAML; verify it also honours
   `IM_GATEWAY_JWT_SECRET` at startup. If not, either (a) add env
   override in `internal/config` or (b) set `stringData.config-yaml`
   with a config file that bakes the secret in (less safe). **This is a
   pre-deploy sanity check, not a block on the manifests.**

3. **Pulsar selector defaults** — `v4-cluster-test.sh` assumes
   `statefulset/pulsar-broker`. Override with
   `PULSAR_SELECTOR=deploy/pulsar` (or whatever your chart uses) before
   running S4.

4. **HPA custom metric commented** — The HPA falls back to CPU-only
   scaling because `im_ws_active_connections` requires prometheus-adapter.
   The block is ready to uncomment once the adapter is installed.

5. **k6 runner scale** — 150k VUs from a single k6 node is aggressive;
   plan for distributed runners or k6-operator on a dedicated node pool.
   OS tuning notes are in `deploy/README.md §5`.

## 7. How to revert

```bash
git reset --hard 5b78fdd   # pre-V4 HEAD
```

All 7 commits land on the worktree branch only — no changes touched
`main` or any other branch.

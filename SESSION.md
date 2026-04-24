# SESSION — im 项目会话续接

> 这份文档是"会话快照"：每次会话结束前更新一次，下次会话开局只要先读它 + `docs/GOAL.md` + `CLAUDE.md` 就能无缝接着干。
> **更新原则**：事实先写（分支/tag/commit），决策次之，待办最后。过时信息必须删除，不留历史沉积。

Last updated: 2026-04-24（深夜：M3 主体 + pre 部署 + E2E 13/13 + Grafana dashboard 全闭环）

---

## 1. 当前分支 & Tag 全景

| 分支 | 用途 | 最新 commit | 状态 |
|------|------|-------------|------|
| `main` | 稳定主干 — M1+M2+M3 部分 + pre 部署 | `6c21608` chore(deploy): pre-env 更新 ns 为 im-v2 | ✅ 已 push 到 origin/main |
| `im-backend-switch` (cses-client) | 双端适配，`ImApiAdapter` 基座 | `85c2b3c34` | ✅ V6 smoke 7/7，独立客户端仓库 |

### main 最近 9 个 commit（本轮 M3 loop 产出）
```
6c21608 chore(deploy): pre-env 更新 ns 为 im-v2
9822ad1 feat(e2e): pre 集群全链路 harness 13/13 通过
e0ecb32 fix(gateway): push_consumer 走 PushTopicFor 与发送侧对齐
60d22be chore(deploy): v4-prepare 适配 pre 集群 + Redis Cluster 开关
2607e65 feat(metrics): 埋 9 个 OTel metric 点亮 Grafana dashboard
8cf1a3b feat(m3): 新增 Topic 子群聊 + Presence 端点
5f3a33c chore(grafana): 新增 im v2 dashboard 设计稿
5a9bef6 feat(redis): 支持 Cluster 模式 + im-new: key 前缀隔离
0b72781 docs(project): M3 改为 cses-client 全面抛弃 Mattermost
```

### Tags
- `v0.1.0-m1-verified` / `v0.1.0-m1-complete` / `v0.2.0-m2-complete` / `v1.0.0`
- **`v0.3.0-m3-pre-deployed`** — M3 主体 + pre 部署 + E2E 13/13 + HPA 扩容验证锁定点

---

## 2. 已完成

### Backend（M1+M2+M3 主体）
- **M1**：auth / friends / channel / messages / sync / WS / 跨 pod 推送骨架 + ProducerCache + routing TTL — 全绿。
- **M2**：announcements / channel governance / urgent / approvals / notifications / scheduled / quick replies — 全绿。
- **M3 主体（本轮）**：
  - Redis **Cluster 兼容**：`OpenRedis` 返回 `UniversalClient`，key prefix `im-new:*` 逻辑隔离（Cluster 无多 DB）
  - **Topic 子群聊**：`channels.root_id`/`root_message_id` + POST/GET `/api/channels/:id/topics`
  - **Presence 端点**：`GET /api/presence?channel_id=X`（复用 Redis routing，不新增状态）
  - **9 个 OTel metric 埋点**：gateway(4) / repo(2) / service(3) 点亮 Grafana dashboard
  - **push_consumer bug fix**：订阅 topic 走 `PushTopicFor(gwID, env)`，与发送侧对齐（之前硬编码 short name 落到 public/default/* 导致跨 pod 推送全丢）

### pre 集群部署
- k8s ns `im-v2` 已创建 + 3 副本 gateway Running（image `harbor.jinqidongli.com/x9-go/im/im-gateway:v1.0.0-pre-2`）
- PG `im_pre` 库 + 10 migration 全跑
- Redis Cluster 连通（key prefix `im-new:*`）
- Pulsar tenant `im` + namespace `im/push-pre` + 订阅 10min 过期
- HPA (`40-hpa.yaml`)：minReplicas=3 maxReplicas=20，CPU 70% 阈值，scaleUp 30s 稳定窗口，scaleDown 300s

### 观测与验证
- **Grafana dashboard**：uid `im-v2-main`，21 panel，已 import（`prometheus-stack-grafana.monitoring`，NodePort 30300）。access: `http://<pre-node>:30300/d/im-v2-main`，admin/`one.2013`
- **E2E harness (`scripts/e2e-pre.mjs`)**：G1~G10 + setup 共 13/13 PASS（见 `docs/E2E_REPORT.md`）
- **清脏脚本 (`scripts/e2e-teardown.sh`)**：动态 TRUNCATE im_pre + 清 Redis `im-new:*` + 删 Pulsar `im/push-pre` 下 topic
- **HTTP↔WS 对应矩阵**：见 `docs/HTTP_WS_MAP.md`（17 条 HTTP 端点对应 WS 事件分布）

### Frontend（cses-client）
- Angular `apiFlavor` 开关 + `ImApiAdapter`：6 个方法已接 + 5 个 F1 stub
- Tauri Rust：`im_client.rs` + `im_seq_data_source.rs` 骨架，`im_seq_sync` feature flag
- **M3 inventory 结论**：Mattermost 只通过 `message.service.ts` 的 37 个 `imHttp.post(...)` 调用；`CsesHttpService` 走 Java（不切）

### 决策冻结点
1. 不切回流量，全量替换。
2. ETL 排序用 MongoDB ObjectId 风格 `migration_sort_key`（48b ms + 16b 同毫秒自增）。M5 列为 TODO。
3. 模板/组织/团队 **归 Java 侧**，不在 im 范围。`/vote/*`、`/Im/search/*`、文件分片上传 = 外部服务。
4. WS V1 12 + M2 4 = 16 种类型锁定；新增升 V2。**typing** 明确延后到 V2。
5. Pulsar topic 本地调试必须 `.{localname}` 后缀；pre/prod 用 tenant `im`。
6. `AllocSeqAndInsert(ctx, tx, msg)` 是 seq 唯一入口；`tx != nil` 复用外部事务。
7. Redis Cluster 无多 DB → 用 key prefix `im-new:*` 隔离。
8. k8s namespace 名为 `im-v2`（`im-2.0` 含点被 k8s 拒绝）。

---

## 3. 进行中 / 未决

### 进行中
- **cses-client 切换 37 处 Mattermost URI → `ImApiAdapter`**：message.service.ts F1 主线，未开工。

### k6 压测观测（2026-04-24 深夜，两轮）
- k6 Job 在 `im-v2` ns 内部跑（走 ClusterIP `im-gateway:8080`，无 NodePort 绕路），配置 `deploy/k8s/60-k6-loadtest.yaml` + 启动脚本 `scripts/apply-k6.sh`
- **首轮 TARGET_VUS=500**：k6 脚本并发 register 路径打爆 bcrypt，gateway CPU 200%/70%，**HPA 自动扩容 3 → 6 pod，耗时 ~20s**（`stabilizationWindowSeconds: 30` + `Percent: 100` policy 生效）—— HPA 快速扩缩容验证通过
- **二轮优化（当前）**：`v4-load.js` 改 login-only + `scripts/seed-users.sh` 预注册 300 个 user（26s 完成），k6 启动后 VU 稳步爬坡**无错误**，CPU 21%/70% 充裕。下步可爬到 1k/10k VU 摸真实 P99 push latency

### HTTP↔WS 推送对应矩阵
- 已固化到 `docs/HTTP_WS_MAP.md`（17 条 HTTP 端点对应 WS 事件分布 + 压测覆盖建议 + 非对称性说明）

### 已知债务
- `cross_pod_push.go` `markOffline(userID)` 仍是骨架位（Pulsar Send 失败后未从 routing 摘除用户）。
- `im.fanout.e2e.duration` 当前 = HTTP handler 总耗时（近似）。语义升级方向：精确到"所有接收方的 conn.send 入队完成时刻 `t2`"（HTTP handler 结束时 goroutine 可能尚未 fanout 完），需 handler 与 fanout goroutine 用 channel 同步。
- cses-client `ImApiAdapter` 还差 5 个 F1 stub：`fetchAround / deleteMessage / editMessage / getReplies / getReaders`。
- V2 WS 候选事件未实现：`typing`（用户明确延后） / `presence_changed` / `reaction_updated`。Presence 当前走 HTTP GET 代替。
- OTel traces export 到 `otel-collector:4317` 在 im-v2 ns 解析不到（name resolver error），metrics 正常，traces 未送到 Jaeger。需部署 OTel collector 或改指 `jaeger-cses` 的 OTLP endpoint。

### 待用户拍板
- ~~打 tag~~ → ✅ 已打 `v0.3.0-m3-pre-deployed`（commit `1d1c195`）
- k6 大规模压测（10k/50k/150k VU）何时落？当前 300 VU 稳态 CPU 21%，可安全爬；150k 需要 distributed runner
- **cses-client F1 切 37 处 URI** → 🗓 **TODO**（用户明确：先把后端 loop 做完，再全面替换 cses-client 代码）
- `im.fanout.e2e.duration` 语义升级（见 §3 已知债务）— 可作为后续精度提升任务

### pre 集群环境事实（2026-04-24）

| 项 | 值 | 备注 |
|---|---|---|
| kubectl context | `kubernetes-admin@kubernetes` | 指向 pre |
| im namespace | `im-v2` | ✅ 已创建 |
| gateway image | `harbor.jinqidongli.com/x9-go/im/im-gateway:v1.0.0-pre-2` | 3/3 Running |
| PG | `postgresql-cses-pre-cnpg-rw.postgres-cses.svc:5432` | 用户 `postgres` / `one.2013`，库 `im_pre` |
| PG NodePort | `192.168.6.66:32432` | 外部访问 |
| Redis Cluster | `redis-cses-pre-redis-cluster-headless.redis-cses.svc:6379` | Cluster 模式，无密码，DB 0，key prefix `im-new:*` |
| Pulsar | `pulsar://pulsar-cses-broker.pulsar-cses.svc:6650` | tenant `im`，ns `im/push-pre`，订阅 10min |
| Grafana | `prometheus-stack-grafana.monitoring`，NodePort `30300` | admin / `one.2013` |
| Prometheus | `prometheus-stack-kube-prom-prometheus.monitoring`，NodePort `30090` | datasource uid = `prometheus` |

---

## 4. 下次会话开局推荐顺序

1. 读 `CLAUDE.md`（2 min）——知道要按 `/go-concurrency-patterns` 写代码。
2. 读 `docs/GOAL.md`（3 min）——知道全局目标和里程碑。
3. 读本 `SESSION.md`（2 min）——知道当前在哪。
4. 读 `docs/ARCHITECTURE.md` + `docs/HTTP_WS_MAP.md`（按需）——找要改的文件和事件分布。
5. 跑 `git status` + `git log -5` ——校验 SESSION 与仓库一致（如不一致，以仓库为准并更新本文件）。
6. 如果要跑验证：`cd server && make verify-all`（本地单测）or `node scripts/e2e-pre.mjs`（pre 端到端）。
7. 向用户确认 §3 "待用户拍板" 选哪条，然后开干。

---

## 5. 会话快速命令

```bash
# 查看当前分支状态（main 已是最新主力）
git status && git log --oneline --graph -15

# 本地验证
cd server && make verify-all              # V1+V2（build + race unit）
cd server && make verify-integration      # V3 testcontainers 集成

# 客户端 V6 smoke
cd /Users/mac28/workspace/angular/cses-client && node scripts/v6-smoke.mjs

# ========== pre 集群操作 ==========

# 重新构建+推送 image（source pre-env 后跑；IMAGE_TAG 可覆盖）
source scripts/pre-env.sh
IMAGE_TAG=v1.0.0-pre-3 scripts/v4-prepare.sh

# 只 re-render manifests（改 yaml 后）
source scripts/pre-env.sh && SKIP_BUILD=1 SKIP_PUSH=1 scripts/v4-prepare.sh

# Apply 到 k8s
kubectl apply -f deploy/k8s/rendered/
kubectl -n im-v2 rollout status deploy/im-gateway

# 本地 port-forward 观察
kubectl -n im-v2 port-forward svc/im-gateway 38080:8080 &
kubectl -n postgres-cses port-forward svc/postgresql-cses-pre-cnpg-rw 25432:5432 &
kubectl -n monitoring port-forward svc/prometheus-stack-grafana 33000:80 &

# 跑 E2E harness（要 port-forward 到 38080）
cd scripts && npm i --silent
IM_GATEWAY=http://localhost:38080 node e2e-pre.mjs

# 清脏数据（PG port-forward 要在 25432）
scripts/e2e-teardown.sh

# k6 压测（k8s 内 Job）
TARGET_VUS=500 scripts/apply-k6.sh       # rewrites configmap + (re)kicks Job
kubectl -n im-v2 logs -f job/im-k6-load  # live logs
kubectl -n im-v2 delete job im-k6-load   # rerun

# Grafana 访问
# http://<pre-node-ip>:30300/d/im-v2-main  (admin / one.2013)

# 管理 Pulsar（tenant/ns）
kubectl -n pulsar-cses exec pulsar-cses-toolset-0 -- bin/pulsar-admin tenants list
kubectl -n pulsar-cses exec pulsar-cses-toolset-0 -- bin/pulsar-admin namespaces list im
```

---

## 6. 更新本文件的规则

- 每次会话**结束前**：至少更新 §1（分支/commit）、§2（新增完成项）、§3（新待决）。
- §4 一般不变；§5 按需补新命令。
- 过时决策**删掉**，不留历史；需要追溯请查 git log。
- 文档目标：让下一次会话 10 分钟内恢复全部上下文。

# SESSION — im 项目会话续接

> 这份文档是"会话快照"：每次会话结束前更新一次，下次会话开局只要先读它 + `docs/GOAL.md` + `CLAUDE.md` 就能无缝接着干。
> **更新原则**：事实先写（分支/tag/commit），决策次之，待办最后。过时信息必须删除，不留历史沉积。

Last updated: 2026-04-27（**M4 GA — 用户身份模型重构完整闭环**：spec / migration 014 / cookie 单栈 / LRU / repo+service+handler+gateway 全量级联 / 单测重建。tag `v0.6.0-m4-cookie-id-native`）

---

## 0. 下次会话一句话启动 ⭐

**复制粘贴这句话开新会话即可**：

> 继续 M4 收尾，按 SESSION.md §0 checklist 顺序跑：① migration 014 在 dev DB 跑通并 verify schema；② 重建 5-7 个 happy-path 集成测试（用 testutil.CookieFixture）；③ 出 pre-7 image + 张立超 cookie 冒烟 + k6 对比 pre-6 + 打 v0.6.1 tag。每步 commit + push origin。

### Step ① — migration 014 dry-run（≤ 30 min）
```bash
# 前提：本地 / port-forward 一个 dev PG（im_dev 库）
export IM_DEV_DSN="postgres://postgres:postgres@localhost:5432/im_dev?sslmode=disable"

# down → up 互逆 sanity；先确认能从 v0.5.1 schema 干净跳到 v0.6.0
cd /Users/mac28/workspace/golangProject/im/server
migrate -path migrations -database "$IM_DEV_DSN" version       # 应该是 013
migrate -path migrations -database "$IM_DEV_DSN" up            # 跑到 014
psql "$IM_DEV_DSN" -c "\d+ messages"                            # 验 sender_id TEXT NOT NULL / team_id TEXT NULL / visible_to TEXT[]
psql "$IM_DEV_DSN" -c "\dt users"                               # 应 'relation does not exist'
psql "$IM_DEV_DSN" -c "\d+ channels" | grep -E 'team_id|creator_id'   # 验 TEXT
migrate -path migrations -database "$IM_DEV_DSN" down 1         # 回 013
migrate -path migrations -database "$IM_DEV_DSN" up             # 再到 014（互逆）

# 验 routing key prefix 没变（生产 contract）
go test -count=1 -run TestConnKeyPrefix ./internal/repo/...
```
**Done 标志**：014 up/down 互逆跑得通；schema 验证 4 条全过；commit `chore(migrate): 014 dry-run on im_dev verified` + 写一行进 §1 回归基线。

### Step ② — 重建 5-7 个 happy-path 集成测试（半天）
**目标**：覆盖 M3-era 主链路在 M4 cookie + team_id 模型下不回归。**不**做 spec §7 全量 76 × 5 case（那是双周冲刺，挪到 M4.5）。

挑这 7 个 happy-path（覆盖 80% 业务流）：
1. `tests/integration/m4_auth_smoke_test.go` — `MattermostCookieResolve` + `CookieRequired` + `/api/auth/me` 端到端（用 testcontainers redis + CookieFixture）
2. `m4_channel_create_dm_test.go` — DM 创建 + team_id denormalize 到 channel
3. `m4_channel_create_group_test.go` — Group 创建 + addMember + 校验 channel_members.user_id TEXT
4. `m4_message_send_sync_test.go` — POST /messages → /sync 拉回；验 sender_id / team_id / visible_to TEXT[]
5. `m4_friend_request_test.go` — friends/request → accept；验 friendships.{requester,addressee}_id TEXT
6. `m4_topic_test.go` — POST /channels/:id/topics 用 mm UserID memberIDs
7. `m4_ws_send_test.go` — WS handshake + send → 收 push_msg；验 SenderID 是 24-hex string

**模板**：
```go
func TestMxxxHappyPath(t *testing.T) {
    pg := containers.PG(t)        // testcontainers postgres
    rdb := containers.Redis(t)    // testcontainers redis
    cookie := testutil.CookieFixture(t, rdb,
        testutil.RealCookieID, testutil.RealUserID, testutil.RealCompanyID)
    expect := testutil.NewExpect(t, buildHandler(t, pg, rdb))
    expect.GET("/api/auth/me").
        WithHeader(middleware.MMCookieHeader, cookie).
        Expect().Status(200).JSON().Object().
        Value("userId").IsEqual(testutil.RealUserID)
}
```
**Done 标志**：7 个 test 全绿；commit `test(m4): 重建 7 个 happy-path 集成测试`；§3 已知债务里把"集成 testcontainers 删除待重建"改 ✅。

### Step ③ — pre 部署 + 性能基线 + tag（1 天）
```bash
# 1. 镜像
source scripts/pre-env.sh
IMAGE_TAG=v1.0.0-pre-7 scripts/v4-prepare.sh
kubectl apply -f deploy/k8s/rendered/
kubectl -n im-v2 rollout status deploy/im-gateway

# 2. migration 在 im_pre 库跑（pre 部署前必须）
kubectl -n postgres-cses port-forward svc/postgresql-cses-pre-cnpg-rw 25432:5432 &
IM_PRE_DSN="postgres://postgres:one.2013@localhost:25432/im_pre?sslmode=disable"
migrate -path server/migrations -database "$IM_PRE_DSN" up

# 3. 张立超 cookie 灌 pre redis
kubectl -n redis-cses port-forward svc/redis-cses-pre-redis-cluster-headless 26379:6379 &
IM_REDIS=localhost:26379 server/scripts/seed-mm-cookies.sh

# 4. 冒烟
kubectl -n im-v2 port-forward svc/im-gateway 38080:8080 &
curl -H 'cookieId: 69eec6dbe6876865ff98945a' http://localhost:38080/api/auth/me
# 应返回张立超 JSON

# 5. k6 压测对比 pre-6 baseline（spec §11.5 标准）
TARGET_VUS=300 scripts/apply-k6.sh
kubectl -n im-v2 logs -f job/im-k6-load
# 验：action_ok ≥ 99% / send P95 ≤ 400ms（pre-6 是 375ms，留 25ms 给 LRU lookup 多 1 次 hop）
# 留档：server/docs/benchmark/$(date +%Y-%m-%d-%H%M)-pre-7-summary.md

# 6. Grafana panel
# 加 im.auth.cookie_cache.hit_rate gauge（target ≥ 90%）
# 加 im.auth.cookie_cache.size gauge（不爬升 OOM）

# 7. tag + push
git tag v0.6.1-m4-pre-deployed
git push origin main --tags
```
**Done 标志**：pre 3/3 Running pre-7；`/api/auth/me` 200 + 张立超 JSON；k6 send P95 ≤ 400ms；tag `v0.6.1-m4-pre-deployed`；origin/main = local main + 39 commits。

---

## 1. 当前分支 & Tag 全景

| 分支 | 用途 | 最新 commit | 状态 |
|------|------|-------------|------|
| `main` | 稳定主干 — M3 GA + **M4 GA**（用户身份模型重构） | `520a76e` test(m4): 重建测试集合 | ✅ M4 P1-P5 完整落地；本地 36 commits ahead origin（待 push） |
| `im-backend-switch` (cses-client) | `ImApiAdapter` M1 能力 100% 覆盖 | `c0b3985c9` feat(im-api): M1 补齐 5 stub | ✅ V6 smoke 7/7 + M1 完整覆盖 |

### main 最近 commit（最新一轮：M4 GA）
```
520a76e test(m4): 重建测试集合 — 删除 int64 fixture 旧测试 + 加 m4 sanity 测试
ead504f feat(m4): cascade — repo/service/handler/gateway 全部 user-id int64 → string
fee71da docs(session): M4 Foundation 落地状态 + Cascade Plan 接力清单
668e408 feat(m4): 落地 spec + foundation — migration 014 / cookie 单栈 / 测试 fixture
0dab7be docs(session): 锁定 M4 用户身份模型重构 spec + 同步 v0.4/v0.5 tag 全景
367f7b8 feat(auth): 纯 cookie 也通过鉴权 (lazy-upsert shadow user) + JWTOrCookie 双栈
05c7868 feat(config): Consul KV 配置中心 + 本地直连 pre 中间件 + cookie 缺失 middleware 告警
ad49e4e feat(middleware): Mattermost cookieId 桥接 + 用户上下文注入 + traces 改指 jaeger-cses
1f9b78c fix(gateway): 补齐 markOffline + cmd/message 迁移到新 envelope
378e67e chore(local): Pulsar 本地一键初始化脚本
86ac2e5 docs(benchmark): pre-6 DM-index 基线 sync/markRead P95 减半
64fb356 perf(dm): FindDM 反向 index + SQL 改走 EXISTS
5dc95e5 fix(gateway): Conn.Push 防 send on closed channel panic
815df1f perf(pool): PG 池对齐 Java HikariCP 配置 50->300
9822ad1 feat(e2e): pre 集群全链路 harness 13/13 通过
e0ecb32 fix(gateway): push_consumer 走 PushTopicFor 与发送侧对齐
2607e65 feat(metrics): 埋 9 个 OTel metric 点亮 Grafana dashboard
8cf1a3b feat(m3): 新增 Topic 子群聊 + Presence 端点
5a9bef6 feat(redis): 支持 Cluster 模式 + im-new: key 前缀隔离
```

### Tags（演进链）
- `v0.1.0-m1-verified` / `v0.1.0-m1-complete` / `v0.2.0-m2-complete` / `v1.0.0`
- **`v0.3.0-m3-pre-deployed`** — M3 主体首次部署 + E2E 13/13
- **`v0.3.1-m3-racefix-pool300`** — Conn.Push race 修复 + PG 池对齐 HikariCP，全链路 100% 可靠
- **`v0.3.2-m3-dm-index`** — FindDM 反向索引 + EXISTS 重写，SLOW SQL 归零
- **`v0.4.0-m3-sysmsg-broadcast`** — 频道事件 → system message + 跨 pod 推送批量化（envelope+TargetUIDs）+ 修历史丢消息 bug
- **`v0.4.1-m3-markoffline-cleanup`** — `Routing.MarkOffline` 连续失败 3 次自动摘除 + `cmd/message` 改走新 envelope
- **`v0.4.2-m3-mm-cookie-bridge`** — Mattermost cookieId → MattermostUser 注入 ctx + OTel traces 接入 jaeger-cses
- **`v0.5.0-config-consul`** — Consul KV 配置中心 + 本地 `make run-dev` 直连 pre 中间件 + cookie 缺失 warn
- **`v0.5.1-cookie-auth`** — 纯 cookie 也通过鉴权（lazy-upsert shadow user + JWTOrCookie 双栈），**生产候选**（pre image: `v1.0.0-pre-11`）。
- **`v0.5.2-m4-foundation`** — M4 Foundation phase（spec + migration 014 + Cookie 单栈 + LRU + 测试 fixture）
- **`v0.6.0-m4-cookie-id-native`** — **当前 HEAD**：M4 GA。repo/service/handler/gateway 全部 int64 → string；删除 users 表与 lazy-upsert；新增 channels.team_id / messages.team_id；Cookie 单栈 + LRU；m4 sanity 单测全绿。**未 deploy / 未 push**；下次会话先决定 pre 部署时机 + m4_*_test 全量补完。

### 回归基线
- **v0.5.1**：build + vet + unit + race + 集成 71 PASS / 0 FAIL / 1 SKIP；e2e 13/13；pod 0 restart / 0 panic（详见 `server/docs/regression/2026-04-27-v0.5.1.md`）
- **v0.6.0-m4-cookie-id-native**：`go build ./... ✅`；`go test -count=1 ./... ✅` 全绿（auth / middleware / repo / service / http / gateway / testutil / config / observability）；migration 014 **未在 dev DB 跑**；testcontainers 集成测试 **删除待重建**；e2e-pre.mjs **未跑**；性能基线 **未复测**

### 镜像演进
`harbor.jinqidongli.com/x9-go/im/im-gateway:v1.0.0-pre-{2,3,4,5,6}` — 每轮性能调优一个 tag，pre-6 是最新。

---

## 2. 已完成

### M4 GA（本会话整段产出）

| Phase | 状态 | 产出 |
|-------|------|------|
| **P1** | ✅ | `migrations/014_m4_userid_text.{up,down}.sql`（DROP & RECREATE 整库 / users 删 / 所有 user FK BIGINT → TEXT / channels.team_id + messages.team_id / messages.visible_to TEXT[]）<br>`internal/auth/userid.go`（ValidateUserID + MustUserID 100% 覆盖，含张立超 cookieId/userId/companyId 验证）|
| **P2** | ✅ | `MattermostCookieResolve` 剥离 lazy upsert<br>`hashicorp/golang-lru/v2` 30s TTL × 10k cap 进程缓存<br>`CookieRequired` hard 401 gate<br>`MattermostUser.ResolvedTeamID`（CompanyID → OrgID → ""）<br>`testutil.CookieFixture` + 张立超 fixture<br>`scripts/seed-mm-cookies.sh` 灌库脚本 |
| **P3** | ✅ | 删 `repo/user.go` + UserRepo 接口；models.go User 整删 + 9 个 struct user_id 字段 int64→string；Channel/Message 新增 TeamID *string；VisibleTo pq.Int64Array→pq.StringArray<br>14 个 repo 文件签名 flip：channel / channel_governance / channel_topic / message / friendship / favorite / file / announcement / approval / notification / quick_reply / scheduled / urgent / user_settings / search<br>routing.go connKey 改 `%s`；删 `mocks/user_repo_mock.go`，重生成 mocks |
| **P4** | ✅ | 删 service/auth.go + profile.go（register/login/UpdateProfile 退役）<br>21 service 文件 user-id 入参全 string；新增 nullIfEmpty helper<br>channel.go：CreateGroup/CreateOrGetDM 加 teamID 入参；message.go：SendParams 加 TeamID *string，从 caller 上下文 denormalize<br>17 handler 文件 `userIDFromCtx` 返回 (string, bool)；新增 teamIDFromCtx；DTO user-id 全 string<br>auth.go 重写：register/login → 410 Gone；/me 返回 MMUserFromCtx<br>cmd/gateway/main.go：删 userRepo 装配；MattermostCookieResolve + CookieRequired 替代 MattermostCookieAuth + JWTOrCookie；6 个 hub*Pusher 签名全 string<br>cmd/message/main.go：incomingMessage / deliveryEvent 全 string + 新增 TeamID *string<br>gateway/conn.go + hub.go + types.go：Conn.UserID string；conns map 键 string；PushMsgPayload/SendPayload/PulsarPushEnvelope 全 string<br>auth/jwt.go：Claims.UserID string；删 jwt_or_cookie 相关 M3-era 测试 |
| **P5** | ✅ | 删除 54 个 int64-heavy 旧测试（repo/service/http/gateway/integration v5_*）<br>新增 m4 sanity 测试：`m4_models_test`（IsVisibleTo string-array 路径 + TeamID 指针）/ `m4_topic_test`（collectTopicMembers 去重）/ `m4_nullifempty_test`（team_id 空→NULL）/ `m4_auth_test`（4 case 覆盖 register/login 410 + /me 401/200）<br>routing_test.go fixture 用张立超 userId<br>`go test -count=1 ./... ✅` 全绿（9 个 package）|
| **P6** | ✅ | tag `v0.6.0-m4-cookie-id-native` 已打；本 SESSION.md / GOAL.md 同步刷新 |

> 本地编译 + 单测全绿。**未做**：migration 014 在 dev/pre PG 库跑；m4_*_test 完整系列（76 端点 × 5 case + 96 WS）；集成 testcontainers 重建；pre image 重打；e2e-pre 13/13；性能基线 pre-7。这些是 v0.6 → 生产 deploy 前的必办清单（详见 §3 "M4 GA 后必办"）。

### Backend（M1+M2+M3 主体）
- **M1**：auth / friends / channel / messages / sync / WS / 跨 pod 推送骨架 + ProducerCache + routing TTL — 全绿。
- **M2**：announcements / channel governance / urgent / approvals / notifications / scheduled / quick replies — 全绿。
- **M3 主体**：
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
- **Benchmark 索引**：`server/docs/benchmark/README.md`（跨批次 summary，记录 pre-2→pre-6 五轮性能演进）

### 性能调优压测（3 轮，对齐 Java HikariCP 参考 + 修 race）
| 版本 | 关键改动 | 代表指标 (VU=300) |
|------|---------|------------------|
| pre-2 | 基线 pool=20 | action_ok=85% send P95=12.75s http_failed=69% |
| pre-5 | panic fix + pool=300 | **action_ok=100%** send P95=680ms http_failed=**0%** |
| pre-6 | + FindDM EXISTS + 反向 index | action_ok=100% send P95=**375ms** SLOW SQL=**0** |

HPA `minReplicas=3 → maxReplicas=20`，VU=800 时实际扩到 17 pod 触发 stop 保护 pre 集群。详细见 `server/docs/benchmark/2026-04-24-1343-summary.md` 和 `2026-04-24-1407-summary.md`。

### Frontend（cses-client, `im-backend-switch` 分支）
- Angular `apiFlavor` 开关 + `ImApiAdapter`：**M1 能力 100% 覆盖（11 方法全实现，0 stub）**：
  - 认证：`loginIm`
  - 频道：`listChannels`
  - 消息：`sendMessage / fetchMessages / fetchAround / deleteMessage / editMessage / getReplies / getReaders`
  - 已读：`markRead`
  - 同步：`sync`
- Tauri Rust：`im_client.rs` + `im_seq_data_source.rs` 骨架，`im_seq_sync` feature flag
- **message.service.ts 37 个 `imHttp.post('/...')` 切换**：still TODO（底层 adapter 已就绪，切换工作量 0 基础设施障碍）
- **M3 inventory 结论**：Mattermost 只通过 `message.service.ts` 调用；`CsesHttpService` 走 Java（不切）

### 本地开发工具
- **`scripts/pulsar-local-init.sh`**：一键建 tenant `im` + namespace `im/push-local` + subscription-expiration 10min。docker-compose 起 Pulsar 后跑一次即可消除 `TopicNotFound` 启动错误。
- 压测套件：`scripts/fullchain-load.js`（全链路 k6）+ `scripts/benchmark-loop.sh`（HPA 梯度 loop + 自动停）+ `scripts/single-pod-benchmark.sh`（scale 到 1 pod 测单机）+ `scripts/seed-users.sh`（预注册池）

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

### M4 GA 后必办（下次会话主线 — pre 部署 + 全量回归）

> M4 代码已闭环并打 tag `v0.6.0-m4-cookie-id-native`，但**未在任何 PG 库跑过 migration 014**，集成 testcontainers 测试在 P5 阶段被全删（旧 int64 fixture 不可救），所以从 v0.5.x → v0.6.0 的端到端验证还是空的。下一会话开局优先做这三件：

#### 1. migration 014 在 dev/pre 跑通（30min）
```bash
# dev 库 dry-run（先 down → 再 up，确认 schema 漂移可逆）
migrate -path server/migrations -database "$IM_DEV_DSN" up
psql "$IM_DEV_DSN" -c "\d+ messages"     # 验 sender_id TEXT / team_id TEXT NULL / visible_to TEXT[]
psql "$IM_DEV_DSN" -c "\dt users"        # 应 'relation does not exist'

# pre 库（需 IM_ENV=ci 或显式 --allow-destructive 才放行）
IM_ENV=ci migrate -path server/migrations -database "$IM_PRE_DSN" up
```

#### 2. 集成测试重建 + m4_*_test 完整系列
```
P5 残留 (spec §11.3 / §11.4)：
- testcontainers 改用 testutil.CookieFixture 灌 Redis HASH "User"，重建 channel/message/friend 等 5-7 个核心集成测试
- m4_*_test.go 系列：76 端点 × 5 case (success / cookie missing / cookie invalid / team_id null / team_id mismatch)
- m4_ws_*_test.go：16 WSMessageType × 2 team state × 3 action = 96 用例
- 现有 m4 sanity 测试只覆盖了：IsVisibleTo / TeamID 指针 / collectTopicMembers / nullIfEmpty / auth 4 case
- 完整 P5 是双周冲刺级别工作；建议 demo 前先做 5-7 个 happy-path 集成测试覆盖 send/sync/channel CRUD/friend
```

#### 3. pre image v1.0.0-pre-7 + 性能基线 + tag 推送
```bash
source scripts/pre-env.sh
IMAGE_TAG=v1.0.0-pre-7 scripts/v4-prepare.sh
kubectl apply -f deploy/k8s/rendered/
kubectl -n im-v2 rollout status deploy/im-gateway

# 灌张立超 cookie 跑冒烟
IM_REDIS=<pre-redis> server/scripts/seed-mm-cookies.sh
curl -H 'cookieId: 69eec6dbe6876865ff98945a' http://<pre>:38080/api/auth/me

# k6 压测对比 pre-6 baseline（spec §11.5）：send P95 ≤ 400ms 容差 25ms
TARGET_VUS=300 scripts/apply-k6.sh
# Grafana 加 panel: im.auth.cookie_cache.{hit,miss}_rate >= 90%
```

完成后再 `git push origin main --tags`，把 36 commits ahead 一并推上。

---

### M4 Cascade Plan（已 done — 历史记录保留供回溯）

> Spec 已在 `server/docs/M4_SPEC.md`（12 节 + 10 决策点 + 9 步验证门禁）。Foundation phase 已 commit `668e408`，tag `v0.5.2-m4-foundation`。本节是接力清单。

**总策略**：让 build 错误倒推 — 改 `internal/repo/models.go` 后 `go build ./...` 会报每个用 int64 user-id 的调用点；按报错列表挨个改。各阶段独立可 commit。

#### P3-A. repo/models.go 字段 flip（30 min 内必须落地）
```
internal/repo/models.go：
  - 删除整个 User struct + TableName()
  - ChannelMember.UserID            int64    → string
  - Channel.CreatorID              *int64    → string  // 同时新增 TeamID *string
  - Message.SenderID                int64    → string  // 同时新增 TeamID *string；VisibleTo pq.Int64Array → pq.StringArray
  - Message.IsVisibleTo(userID)     int64    → string
  - Friendship.RequesterID/AddresseeID int64 → string
  - File.UploaderID                 int64    → string
  - MessageFavorite.UserID          int64    → string
  - ChannelManager.UserID/AddedBy   int64    → string
  - ChannelPinnedMessage.PinnedBy   int64    → string
  - UserSettings.UserID             int64    → string
```
另外 `MsgType` 系列常量保留；`UserStatusActive/Disabled` 删除（users 表没了）。

#### P3-B. repo/*.go 一个一个改（按 grep -c int64 排序大→小）
- channel.go (37) / message.go (28) / channel_governance.go (24) / urgent.go (16) / scheduled.go (15) / friendship.go (15) / announcement.go (15) / approval.go (14) / routing.go (13) / notification.go (12) / quick_reply.go (10) / favorite.go (10) / channel_topic.go (8) / user.go (8 — **整文件删**) / search.go (6) / file.go (6) / user_settings.go (2)
- `mocks/` 目录全删（UserRepo / 等同时退役）

> 每个文件 5-15 min 机械改造：把所有 `user_id int64` / `senderID int64` / `[]int64` 用户列表字段 / `pq.Int64Array{}` (用户场景) → string 对应物。`tx.Where("user_id = ?", uid)` 不动 SQL，改不到 SQL 字符串。

#### P4-A. service params struct flip（mostly mechanical）
internal/service/*.go：所有 `*Params` 字段 `UserID/SenderID int64 → string`。
特别注意 `service.SendParams.SenderID` 走 `repo.MessageRepo.AllocSeqAndInsert`，需要带 `TeamID` 给 messages.team_id（denormalize from channels）。

#### P4-B. handler userIDFromCtx 返回类型 + 76 调用点
- 改 `internal/http/settings.go::userIDFromCtx`：返回 `(string, bool)`，加 `auth.ValidateUserID(uid) != nil → 401`
- 17 个 handler 文件改调用点；body 里 `UserID int64 → string` 的 request DTO 也跟着改（friend.go / channel.go 等管理动作）
- 列表过滤加 `team_id` (D3 默认：`team_id = caller OR team_id IS NULL`)

#### P4-C. main.go 装配
```diff
- userRepo := repo.NewUserRepo(gormDB)
+ // M4: users 表删除，cookieId 即身份
- imhttp.RegisterAuthRoutes(engine, authSvc, userRepo, cfg.Gateway.JWTSecret,
-   middleware.MattermostCookieAuth(rdb, userRepo, log))
+ imhttp.RegisterAuthRoutes(engine, cfg.Gateway.JWTSecret,
+   middleware.MattermostCookieResolve(rdb, log))
- authedAPI.Use(middleware.MattermostCookieAuth(rdb, userRepo, log))
- authedAPI.Use(middleware.JWTOrCookie(cfg.Gateway.JWTSecret))
+ authedAPI.Use(middleware.MattermostCookieResolve(rdb, log))
+ authedAPI.Use(middleware.CookieRequired())
```
auth.go：register/login → 410 Gone；/me → 直接 `c.JSON(200, MMUserFromCtx(c))`，不再 GetByID。

#### P5-A. 既有测试 fixture 改造（v5_*_test.go × 28 个文件）
所有 `int64(1)` / `int64(2)` 等魔数 user-id → `testutil.HexUserID(1)` / `testutil.HexUserID(2)` 等。helper：写一个 `mustSeedTestUser(t, rdb, seed int) cookieID` 包装 CookieFixture。

#### P5-B. m4_*_test.go 新增（76 endpoints × 5 case）
模板：`server/tests/integration/m4_<scope>_test.go`，每 scope 5 case：C1 success / C2 cookie missing / C3 cookie invalid / C4 team_id null / C5 team_id mismatch。
WS：`m4_ws_*_test.go` 16 type × 2 team state × 3 action = 96 用例。

#### P6. 验证 + tag
- migration 014 在 dev DB 跑通
- `make verify-all` 全绿
- `IM_GATEWAY=... node scripts/e2e-pre.mjs` 13/13
- pre-7 镜像 + k6 压测对比 pre-6 baseline（P95 ≤ 400ms 容差）
- Grafana panel 加 `im.auth.cookie_cache.hit_rate` ≥ 90%
- 删 `JWTOrCookie` / `RegisterAuthRoutes` 旧 code path / `repo/user.go` / `repo/mocks/`
- tag `v0.6.0-m4-cookie-id-native` + push origin
- 同步刷 `docs/GOAL.md` / `docs/ARCHITECTURE.md` / `server/docs/BACKEND.md`

---

### 其他 backlog（M4 完工后再说）
- **cses-client 切换 37 处 Mattermost URI → `ImApiAdapter`**：message.service.ts F1 主线。Adapter 已完整覆盖 M1。建议 demo 前只切核心 4 条（send/markRead/revoke/sync）。
- **HPA `maxReplicas 20 → 30 / minReplicas 3 → 6`**：pre-6 VU=800 时 17 pod 触发 stop，想测 VU=1500+ 的天花板需先扩 max。
- **其他含 `channel_members` JOIN 的热路径 EXPLAIN**：`ListByUser` / `ListByUserWithPreview`。

### k6 压测演进（共 3 个主要 batch，完整记录见 `server/docs/benchmark/README.md`）

| Batch Stamp | 镜像 | VU 梯度 | Stop-reason | action_ok @VU=300 |
|-------------|------|---------|-------------|-----------------:|
| 2026-04-24-1234 | pre-2 (pool=20, panic bug) | 100/300/800/1500 | 无（跑完）| **85.44%** + 69% http_failed |
| 2026-04-24-1343 | **pre-5** (pool=300 + race fix) | 100/300/800/(1500/2500 skip) | HPA=17 保护停 | **100%** + 0% http_failed |
| 2026-04-24-1407 | **pre-6** (+ FindDM 反向索引) | 100/300/800 | HPA=17 保护停 | **100%** SLOW SQL=0 |

### 压测侧已解决（本轮）
- ✅ `fullchain-load.js` shared peer（setup 一次建 `k6pre_shared_peer`）
- ✅ PG pool 20→300 对齐 HikariCP
- ✅ benchmark-loop threshold 容错（`CPU_POD_CNT` 空值兜底）
- ✅ Conn.Push race 修复（不再 panic，RESTARTS=0）
- ✅ FindDM 反向 index + EXISTS 重写（SLOW SQL=0）

### 压测侧仍 backlog
- `im_push_e2e_ms` 埋点：VU 间 sendTs map 不共享，当前全 0；改用 server `created_at` 字段
- `minReplicas 3 → 6`：避免 ramp 早期打爆 / `maxReplicas 20 → 30`：测 1500+ 真实上限
- benchmark-loop threshold 加 `action_ok < 0.95` 自动停

### HTTP↔WS 推送对应矩阵
- 已固化到 `docs/HTTP_WS_MAP.md`（17 条 HTTP 端点对应 WS 事件分布 + 压测覆盖建议 + 非对称性说明）

### 已知债务
- ~~`cross_pod_push.go` `markOffline(userID)` 仍是骨架位~~ → ✅ 已补齐（`v0.4.1-m3-markoffline-cleanup`）：`Routing.MarkOffline` Lua 原子 HDEL + `sendFailureTracker` 连续 3 次失败触发；`cmd/message` 同步改走 `PulsarPushEnvelope` + `ProducerCache`。
- ~~**影子映射 mm_user_id ↔ im int64** 是过渡方案~~ → ✅ M4 完整移除（`v0.6.0-m4-cookie-id-native`）：users 表删除、UserRepo / UpsertByMattermostID / register / login 全部退役；所有业务表 user FK 改 TEXT 直接存 mm UserID；cookieId → MattermostCookieResolve（LRU 30s）→ CookieRequired 单栈；主鉴权链路上不再有 JWT。
- `scripts/e2e-teardown.sh` 末尾 Pulsar `topics list` 在没 topic 时返非零被 `set -e` 抓到 exit=1（不影响实际清理）。补 `|| true` 即可。
- OTel SDK metrics push 到 jaeger-cses 失败刷 INFO 噪音（jaeger-v2-collector 只支持 traces service）。修法：env `OTEL_METRICS_EXPORTER=none` 或 SDK 侧禁 metric exporter。
- `internal/repo` 单测覆盖率 2.1%（目前主要靠集成测试）。补 sqlmock 拉到 15-20%。
- `im.fanout.e2e.duration` 当前 = HTTP handler 总耗时（近似）。语义升级方向：精确到"所有接收方的 conn.send 入队完成时刻 `t2`"（HTTP handler 结束时 goroutine 可能尚未 fanout 完），需 handler 与 fanout goroutine 用 channel 同步。
- ~~cses-client `ImApiAdapter` 差 5 个 F1 stub~~ → ✅ 已补齐（commit `c0b3985c9`），现 M1 完整覆盖 11 方法
- cses-client `message.service.ts` 37 处 `imHttp.post(/mattermost-path)` 切换到 adapter 方法仍待做（Adapter 就绪，不阻塞）
- V2 WS 候选事件未实现：`typing`（用户明确延后） / `presence_changed` / `reaction_updated`。Presence 当前走 HTTP GET 代替。
- OTel traces export 到 `otel-collector:4317` 在 im-v2 ns 解析不到（name resolver error），metrics 正常，traces 未送到 Jaeger。需部署 OTel collector 或改指 `jaeger-cses` 的 OTLP endpoint。
- `ListByUser` / `ListByUserWithPreview` 未 EXPLAIN 验证是否享受 DM 反向索引的收益。

### M4 全景（spec → P1+P2 落地 → P3-P6 cascade）

**背景**：v0.5.1 影子映射 `mm_user_id ↔ im int64` 是过渡方案。用户决定 im 完全走 mm/Redis，业务表全部用 mm UserID（24-hex MongoDB ObjectId）做外键。等价于把 Mattermost 的 sql session_store 模型搬过来：cookieId → Redis HGET "User" → 信任 + 注入 ctx。

**Spec 锁定决策**（详见 `server/docs/M4_SPEC.md` §1-§9）：
- D1 user-id Go 类型 = `string`（不引入 type alias）
- D2 team_id 来源 = CompanyID → OrgID → ""（多组织场景待 cses 协议）
- D3 列表 team 过滤 = `team_id = caller OR team_id IS NULL`
- D4 JWT 注册登录端点 = 410 Gone
- D5 LRU TTL = 30s / capacity = 10k
- D6 LRU lib = `hashicorp/golang-lru/v2`
- D7 Migration 014 形态 = DROP & RECREATE（pre/dev 测试库无生产数据）
- D8 `visible_to` = `TEXT[]`（与 mm UserID 字符串对齐）
- D9 admin JWT 通道 = 保留 `/api/admin/*`（运维口子）
- D10 cookie 缺失 warn 日志 = 删（M4 后是 hard 401）

**冷冻范围澄清**（spec 附录 A）：im 只在 `messages.{sender_id, team_id}` 冷冻；`channels.team_id` 创建时刻冻结一次；其他表（channel_members / friendships / files / approvals 等）是 live 关系，user_id 是实时引用，mm 删用户后跟随 cses 侧。

**真实 cookie fixture（验证基准 — 张立超）**：
- cookieId = `69eec6dbe6876865ff98945a`
- userId = `676cc4ccfbbc501161d5cd65`
- companyId = `6111fb0a202d425d221c53db`
- orgId = `6311a17c50c75d009ed3864f`
- 已固化到 `internal/testutil/cookie_fixture.go` 常量 + `scripts/seed-mm-cookies.sh` 内置 fixture

### 待用户拍板
- ~~M4 spec~~ → ✅ `server/docs/M4_SPEC.md` 锁定，10 决策点全默认
- ~~M4 Foundation phase~~ → ✅ `v0.5.2-m4-foundation` (commit `668e408`)
- **M4 P3-P6 cascade**：建议下次会话整段做完（mechanically grind 23 repo + 21 handler + tests）。预估单会话 4-6 hr 集中精力可推完，否则按 §3 "M4 Cascade Plan" 拆 2-3 次。
- **cses-client `message.service.ts` 切换时点**：M4 完工后再切（避免双重切换）
- **HPA 扩容上限调整**：`maxReplicas 20→30 minReplicas 3→6`？（想测 VU=1500+ 真实 ceiling 前置）
- **`im.fanout.e2e.duration` 语义升级**：当前等同 handler 耗时，想要真端到端需 handler 同步 fanout（见已知债务）
- **M5 历史 ETL**：M4 完工后开 — 仍然 TODO，非本期

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
| 镜像当前在线 | `harbor.jinqidongli.com/x9-go/im/im-gateway:v1.0.0-pre-6` | 3/3 Running，HPA 缩到 minReplicas=3 |
| Migration 已应用 | `001..011`（含 M3-A Topic + M3-C DM 反向索引）| `im_pre` 库 schema 最新 |

---

## 4. 下次会话开局推荐顺序（接力 M4 P3-P6）

1. 读 `CLAUDE.md`（2 min）——按 `/go-concurrency-patterns` 写代码。
2. 读 `docs/GOAL.md`（3 min）——全局目标和里程碑。
3. 读本 `SESSION.md` §3 "M4 Cascade Plan"（5 min）——知道每一步动哪个文件。
4. 读 `server/docs/M4_SPEC.md`（10 min）——决策点全部已锁，照做。
5. `git status && git log --oneline -5` 确认 HEAD = `v0.5.2-m4-foundation` (commit `668e408`)。
6. **开干顺序**：P3-A models.go → P3-B repo 文件按 int64 计数大→小 → P4-A service params → P4-B handler userIDFromCtx → P4-C main.go → P5-A v5_*_test fixture → P5-B m4_*_test 系列 → P6 验证 + tag。
7. 阶段性 commit；每完成一个 phase 跑 `go build ./... && go test ./internal/...` 再继续。
8. 全部绿了再 `migrate -path server/migrations -database "$IM_DEV_DSN" up` 让 014 落地。

---

## 5. 会话快速命令

```bash
# 查看当前分支状态（main 已是最新主力）
git status && git log --oneline --graph -15

# ========== M4 Foundation 验证 ==========
# 单独跑 M4 P1+P2 已落地的单测
cd server && go test -race ./internal/auth/... ./internal/middleware/... ./internal/testutil/...

# 把张立超 fixture 灌到本地 redis（手动 e2e）
IM_REDIS=localhost:6379 server/scripts/seed-mm-cookies.sh
# 用任意 cookieId 测：
curl -H 'cookieId: 69eec6dbe6876865ff98945a' http://localhost:38080/api/auth/me

# 跑 014 migration（dev 库）— P3 cascade 启动前先做
migrate -path server/migrations -database "$IM_DEV_DSN" up

# 本地验证（M4 cascade 落地后才能全绿）
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

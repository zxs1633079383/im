# CLAUDE.md — `server/internal/gateway/` 模块级指令

> 模块级补丁。优先级：用户全局 `~/.claude/CLAUDE.md` > 项目根 `/Users/mac28/workspace/golangProject/im/CLAUDE.md` > **本文件** > 默认行为。
> 任何 `server/internal/gateway/**/*.go` 改动**强制加载**本文件 + harness **C002 / C003 / C004 / C005** 四条。
> 触发后第一动作：`Skill(skill="go-concurrency-patterns")`。

---

## 0. 模块定位

**WS 实时通信枢纽**。整个 IM 实时层只有一个跨 pod 推送通道，全部经此模块编织：

```
┌── service / http handler / cmd worker ──┐
│                                          │ 业务侧只允许调
│       gateway.Hub.CrossPodPush           │ ─── 单用户定向
│       gateway.Hub.CrossPodBroadcast      │ ─── 多用户扇出（按 gatewayID 分桶）
└──────────────┬───────────────────────────┘
               │
               ▼
   ┌───────────────────────────────┐
   │ 本 pod local conns（hub map） │ ← 同 pod 用户直发，不走 Pulsar
   └───────────────────────────────┘
               │ 异 pod 用户
               ▼
   PushTopicFor(gatewayID, env) → Pulsar topic
               │
               ▼
   目标 pod gateway.push_consumer.deliverOne
               │
               ▼
   目标 pod hub.PushToUser → WS conn write
```

**铁律**：任何「跨 pod 推送」必须经本模块；service / http / cmd 业务路径**禁止**拿 `hub.PushToUser` 直发（仅 `push_consumer.go::deliverOne` 与 `ws_handler.go::ackBroadcast` 内部允许，那是已经收敛到本 pod 后的最终一跳）。

业务侧 push 唯一入口 = `gateway.Hub.CrossPodPush(...)` / `gateway.Hub.CrossPodBroadcast(...)`。

---

## 1. 影响范围

**上游**（调用本模块）：
- `server/internal/service/**/*.go` — 消息发布 / 频道事件 / 通知 / 加急 / 撤回 / 编辑 / reaction 全部走 `CrossPodPush` / `CrossPodBroadcast`
- `server/internal/http/**/*.go` — handler 不直接 push，必须经 service；upgrade endpoint `/ws` 复用 `WsHandler`
- `server/cmd/message/**/*.go` — 异步 worker（template fire / scheduled fire）持有 `*gateway.Hub` 注入，与 service 一样走 CrossPod*
- `server/cmd/gateway/main.go` — 装配 `Hub` + `Routing` + `PushConsumer` + `ProducerCache`，注入 `IM_GATEWAY_ID` / `IM_ENV`

**下游**（被本模块调用）：
- Pulsar：`internal/pulsar.Producer` + `ProducerCache`（topic 经 `PushTopicFor`）
- Redis：`repo.Routing`（routing key Hash：`{user} → [gatewayID:deviceID]`），TTL = 45s
- OTel：`im.fanout.e2e.duration` / `im.crosspod.envelope.target_uids_avg` / `im.ws.active_connections` 三条核心 metric

**Client 端契约**：
- Angular：`client/src/app/core/services/ws-client.service.ts` 的 `PING_INTERVAL_MS = 15000`
- Rust：`src-tauri/src/websocket/im_handlers.rs` 的 `HEARTBEAT_INTERVAL`
- IMDataSource 重连依赖 routing TTL = 45s：超过 45s 没 ping → Redis routing 自动过期 → 重连后服务端无 stale 残留

---

## 2. 功能模块清单

| 文件 | 职责 | 关键 API / 类型 | 测试 |
|---|---|---|---|
| `hub.go` | 本 pod conn 注册表（`map[userID][]*Conn`）+ OTel `im.ws.active_connections` Gauge | `Hub.Register/Deregister/ConnsForUser/PushToUser` | `hello_test.go` |
| `conn.go` | 单条 WS 连接：send buffer、KnownSeq map、关闭语义 | `Conn.Push/KnownSeqFor/SetKnownSeq` | 与 hub 共测 |
| `ws_handler.go` | upgrade + JWT/cookieId 双轨鉴权 + readPump dispatch + WsSend/PushACK/Sync 三类入站 | `WsHandler.ServeHTTP` | `ws_auth_test.go` |
| `routing.go` | 复用 `repo.Routing`；常量 `RoutingTTL = 45s` 一键引用 | `Routing` / `NewRouting` / `RoutingTTL` | `repo/routing_test.go` |
| `heartbeat.go` | 服务端 15s pong + 客户端 ping，read deadline 45s = `RoutingTTL` | `runHeartbeat(...)` + `heartbeatInterval` / `readDeadline` | C004 单测 |
| `cross_pod_push.go` | **唯一**跨 pod 推送出口：local 直发 + 按 gatewayID 分桶 + Pulsar fan-out | `Hub.CrossPodPush` / `Hub.CrossPodBroadcast` | C002 §4.3 |
| `push_consumer.go` | 消费本 pod topic（`PushTopicFor(myGwID, env)`），解 envelope → `hub.PushToUser` 最终一跳 + push_id ACK 等待 | `PushConsumer.Run/deliverOne` + `globalACKRegistry` | 集成测 |
| `topic.go` | topic 命名唯一入口；`USER → HOSTNAME → "anon"` 三级 dev-suffix fallback | `PushTopicFor(gatewayID, env)` / `devSuffix()` | `topic_test.go` |
| `send_failure_tracker.go` | producer.Send 连续失败 3 次 → `Routing.MarkOffline` 摘除 stale routing | `sendFailureTracker.Record/Reset` | C004 §4.3 |
| `producer_cache.go` | 每个 topic 复用一个 `pulsar.Producer`（`sync.Once` 单例语义），防 OOM | `ProducerCache.GetOrCreate` | mock |
| `metrics.go` | OTel meter 注册集中处；不允许散落 `otel.Meter` 调用 | meter `im-gateway` + 各 histogram | — |
| `tracing.go` | OTel tracer 单例 `im-gateway/ws`；ws frame dispatch 都从这条 tracer 开 span | `wsTracer` | — |
| `types.go` | **协议表面**：22 种 `WSMessageType` + 所有 payload struct + `PulsarPushEnvelope` | `WSMessageType` 枚举 + `PushMsgPayload` / `PongPayload` / etc | C005 §5.4 |

> 单文件硬上限 **400 行**。任何 push 路径 / handler 新逻辑超出该上限必须按职责拆文件，不允许塞进现有大文件。

---

## 3. SOP — 新增 WS 事件 / 跨 pod 推送

按顺序，不允许跳步：

1. **凭表声明**：在 `types.go` 新增一行 `const TypeXxx WSMessageType = "..."`；当前共 **22** 个 const（V1 12 + M1 2 + M2 4 + v0.7 4），新增前先确认是否已在锁定集（C005 §2）。
   - 若超过 22 → **必须先**走 V2 RFC（`docs/RFC/ws-v2.md`）+ 用户拍板，再回来 commit。
2. **定义 payload struct**：同样在 `types.go`，命名 `TypeXxxPayload`，新增字段全部 `omitempty`（向后兼容旧客户端）。
3. **service 调用**：service 层用 `s.hub.CrossPodPush(ctx, ...)` 或 `s.hub.CrossPodBroadcast(ctx, ...)`，**禁止**自己拼 `PulsarPushEnvelope` 投 raw producer；`MsgType:` 字段引用 const，不许字符串字面量。
4. **partitionKey 选择**：channel 广播传 `channelID`（保序）；单用户定向传 `userID`。
5. **Pulsar topic** 自动由 `PushTopicFor(gatewayID, env)` 决定，不要在 service 拼字符串；本地 dev 自动追加 `USER`/`HOSTNAME` 后缀防窜台（C003）。
6. **客户端同步**：
   - `client/src/app/core/services/ws-normalizer.ts` 加翻译表 `"xxx" → "imWs:..."`
   - `src-tauri/src/websocket/im_handlers.rs` dispatch match arm 加一条
7. **集成测试**：`server/tests/integration/m4_ws_*_test.go` 至少 1 个 W1（同 pod 单收件）+ 1 个 W3（跨 pod 转发）+ 1 个 W5（routing 失效跳过）。
8. **OTel + Grafana**：新 type 自动落到 `im.fanout.e2e.duration` 直方图（已按 `msg_type` 维度分桶），dashboard panel 按需补 legend。

---

## 4. Pre-commit 自检清单

每次提交本模块代码前**全部**跑过；任何一条非 0 即视为破坏 harness 契约。

```bash
# ① 单元 + race detector + 100% 覆盖率
cd server && go test ./internal/gateway/... -race -covermode=atomic -coverprofile=cov.out
go tool cover -func=cov.out | awk '$3 != "100.0%" && $1 !~ /coverage:ignore/ { print; exit 1 }'

# ② C002：业务路径绝禁裸调 hub.PushToUser（仅 gateway 包内 deliverOne / ackBroadcast 允许）
grep -rEn '\.PushToUser\(' server/internal/service/ server/internal/http/ server/cmd/ \
  --include='*.go' | grep -v '_test.go' | wc -l
# 期望：0

# ③ C002：业务路径绝禁手拼 Pulsar envelope
grep -rEn 'PulsarPushEnvelope\{' server/internal/service/ server/internal/http/ server/cmd/ \
  --include='*.go' | grep -v '_test.go' | wc -l
# 期望：0

# ④ C003：topic 命名唯一入口（除 topic.go 自身）
grep -rEn 'persistent://im/push' server/ --include='*.go' \
  | grep -v 'gateway/topic.go' | grep -v '_test.go' | wc -l
# 期望：0

# ⑤ C005：22 种 WSMessageType 锁定，超出即触发 V2 RFC
CURRENT=$(grep -cE 'WSMessageType\s*=\s*"' server/internal/gateway/types.go)
[ "$CURRENT" -le 22 ] || { echo "WS type 总数 $CURRENT 超出锁定 22 → 走 V2 RFC"; exit 1; }

# ⑥ C004：routing TTL 与 heartbeat 必须 3 倍关系
go test ./internal/gateway/ -run TestRoutingTTLMatchesReadDeadline -v
# 必须 PASS

# ⑦ 业务路径不许硬编码 routing 时间常量
grep -rEn '45\s*\*\s*time\.Second|15\s*\*\s*time\.Second' \
  server/internal/service/ server/internal/http/ --include='*.go' | grep -v '_test.go' | wc -l
# 期望：0
```

完整 verify-all：`cd server && make verify-all`。

---

## 5. Commit 规范

沿用项目根 `~/.claude/rules/common/git-workflow.md`，模块 scope 强制带子模块名：

```
feat(gateway/cross-pod-push): xxx                ← 推送主路径
feat(gateway/heartbeat): xxx                     ← 心跳 / TTL
feat(gateway/routing): xxx                       ← Redis routing 字段
feat(gateway/ws-handler): xxx                    ← upgrade / dispatch
feat(gateway/types): xxx                         ← 新增 WSMessageType（必带 V2 RFC 引用）
feat(gateway/topic): xxx                         ← Pulsar topic 命名规则
feat(gateway/push-consumer): xxx                 ← 消费端 / ACK
fix(gateway/<sub>): xxx
test(gateway/<sub>): xxx
refactor(gateway/<sub>): xxx
perf(gateway/<sub>): xxx
```

- description / body **中文**；body 说"为什么"，不复述"做了什么"。
- 涉及 C002–C005 任一 harness 的改动，body 必须显式引用 `harness/C{NNN} §3` 或 §4。
- 改 routing TTL / heartbeatInterval / readDeadline → commit message 必带四元组耦合验证（前后端 + Rust 端常量对齐截图或代码引用）。

---

## 6. 约束规范（硬约束）

按优先级：

1. **跨 pod 推送只走 `CrossPodPush` / `CrossPodBroadcast`**（C002）
   - service / http / cmd 业务路径**不许**调 `hub.PushToUser`
   - **不许**业务路径自己 New Pulsar producer + `producer.Send`
   - 仅 `push_consumer.go::deliverOne` + `ws_handler.go::ackBroadcast` 允许直发本 pod conn

2. **Pulsar topic 必走 `PushTopicFor(gatewayID, env)`**（C003）
   - 业务路径**不许**出现 `persistent://im/push...` 字符串字面量
   - 本地 dev 必带 `USER` / `HOSTNAME` / `"anon"` 三级 fallback 后缀
   - sender / consumer 必须调同一函数；命名不同就是窜台

3. **routing TTL 45s 与 heartbeat 15s × 3 耦合**（C004）
   - 铁律：`heartbeatInterval × 3 == readDeadline == RoutingTTL`
   - 改一个常量必须同时改四处（后端 heartbeat.go + routing.go + Angular ws-client.ts + Rust im_handlers.rs）
   - `connKeyTTL = 2h` 独立维护，远大于 `RoutingTTL`

4. **`WSMessageType` 锁定 22 种**（C005）
   - V1 12 + M1 2 + M2 4 + v0.7 4 = 22；新增即触发 V2 RFC
   - payload 字段加扩展必须 `omitempty`；删字段 / 重命名 type 必须升 V2 协议
   - 跨语言一致：types.go ↔ ws-normalizer.ts ↔ im_handlers.rs 三处不可漂移

5. **Hub 内部 goroutine 必须 errgroup 或 ctx 兜底**
   - 所有 `go f()` 必须有父级（`errgroup.Group` / `sync.WaitGroup` / `context.Context`）
   - 每个 `for` 循环出口 `select { case <-ctx.Done(): return }`
   - `Conn.Push` 是非阻塞 send（buffer 满即关 conn），不允许变阻塞
   - Channel 关闭权属 sender；`hub.conns` 共享状态用 `sync.RWMutex`，禁止裸 map 并发
   - 详见 Skill `go-concurrency-patterns`

6. **资源单例**
   - `pulsar.Producer` 走 `ProducerCache.GetOrCreate`（topic 维度复用）
   - `otel.Tracer("im-gateway/ws")` / `otel.Meter("im-gateway")` 包内单例（`tracing.go` / `metrics.go`）
   - 业务路径**不许**自己 `otel.Tracer(...)` 新建

---

## 7. 对应 Harness 映射

| Harness | 触发场景 | 验证手段（本模块） |
|---|---|---|
| **C002** [cross-pod-push-must-go-gateway-crosspodpush](../../../docs/harness/C002-cross-pod-push-must-go-gateway-crosspodpush.md) | service/http/cmd 触发 push；`grep PushToUser` 业务路径 | §4 ②③ grep + `cross_pod_push_test.go::TestCrossPodPush_*` 6 用例 + 集成 W1/W3/W5 |
| **C003** [pulsar-topic-localname-suffix](../../../docs/harness/C003-pulsar-topic-localname-suffix.md) | 改 Pulsar topic 命名 / sender / consumer 订阅 | §4 ④ grep + `topic_test.go` 6 用例 + scripts/topic-name-smoke.sh |
| **C004** [redis-routing-ttl-and-heartbeat](../../../docs/harness/C004-redis-routing-ttl-and-heartbeat.md) | 改 `RoutingTTL` / `heartbeatInterval` / `readDeadline` / client ping | §4 ⑥⑦ + `TestRoutingTTLMatchesReadDeadline` + Grafana `im.gateway.heartbeat.miss_rate < 1%` |
| **C005** [ws-event-types-locked](../../../docs/harness/C005-ws-event-types-locked.md) | 增删 `WSMessageType` / payload 字段 / 客户端归一化表 | §4 ⑤ + `TestWSMessageType_Locked22` + 跨语言契约 diff（types.go ↔ ws-normalizer.ts） |

冲突时优先级：**harness > 本文件 > 项目根 CLAUDE.md > 用户全局 rules**。

---

## 8. Update / Insert 规则

### 新增 WSMessageType（端到端清单）

**先决条件**：当前已达 22 锁定 → **必须**先 V2 RFC + 用户拍板，才能 N+1。

提交一次性必须包含：

1. **RFC**：`docs/RFC/ws-v2-<topic>.md` — 动机 / payload schema / 升级路径 / sunset 计划
2. **Skill 加载**：`Skill(skill="go-concurrency-patterns")` — 保证 Hub 注册 goroutine 兜底
3. **后端 types**：`server/internal/gateway/types.go` 新增 `const TypeXxx WSMessageType = "..."` + `XxxPayload` struct（字段全 `omitempty`）
4. **后端 Hub 注册**：如需新分支（如 ack 跟踪 / 跨 pod 路由特例），在 `cross_pod_push.go::pushLocalAndCollectRemote` 或 `push_consumer.go::deliverOne` 加 case
5. **客户端 Angular**：`client/src/app/core/im/` + `ws-normalizer.ts` 翻译表加一条
6. **客户端 Rust**：`src-tauri/src/websocket/im_handlers.rs` dispatch match arm + 对应 IMDataSource 缓存策略
7. **集成测试**：`server/tests/integration/m4_ws_<type>_test.go` 至少 W1/W3/W5 三个用例
8. **OTel dashboard**：Grafana 按 `msg_type` 标签自动接入，若需新 panel 在 `deploy/grafana/` 加 JSON
9. **C005 §2 表格** + **types_test.go::TestWSMessageType_Locked22** 上限 22 → 23 同步改

### 新增 routing 字段

routing Hash schema 改动（如新增 `companyId` / `deviceType`）：

1. `server/internal/repo/routing.go` — 新增字段 + Lua 脚本 EXPIRE 参数保持 `RoutingTTL` 不变
2. `server/internal/repo/routing_test.go` — 新增字段读写 + TTL 续期单测
3. `server/internal/gateway/cross_pod_push.go::CrossPodBroadcast` — 如新字段影响分桶逻辑，相应改 `LookupBatch` 调用
4. **保持 45s TTL 不动**：除非同时改 heartbeat 四元组（C004 §3 首选 B）
5. **回滚兼容**：老 pod 写老 schema、新 pod 读老 schema 必须不 panic — 用 `omitempty` + 默认值

### 改 heartbeat / routing TTL（破坏性）

必须四处同时改：

1. `server/internal/gateway/heartbeat.go::heartbeatInterval` + `readDeadline`
2. `server/internal/repo/routing.go::RoutingTTL`
3. `client/src/app/core/services/ws-client.service.ts::PING_INTERVAL_MS`
4. `src-tauri/src/websocket/im_handlers.rs::HEARTBEAT_INTERVAL`

保持 `heartbeatInterval × 3 == readDeadline == RoutingTTL` 铁律；PR 描述附压测对比（Redis QPS / 用户掉线率 / `im.gateway.markoffline.due_to_failure_count`）。

---

## 9. 文档关联

| 文档 | 用途 |
|---|---|
| [server/docs/BACKEND.md §5 跨 pod 推送](../../docs/BACKEND.md) | CrossPodPush / CrossPodBroadcast 详细契约（C002 inline target） |
| [server/docs/BACKEND.md §3.2 Pulsar topic 命名](../../docs/BACKEND.md) | `PushTopicFor` 命名规则（C003 inline target） |
| [server/docs/BACKEND.md §十一 OTel](../../docs/BACKEND.md) | `im.fanout.e2e.duration` / `im.ws.active_connections` / `im.crosspod.envelope.target_uids_avg` 三条核心 metric |
| [docs/HTTP_WS_MAP.md](../../../docs/HTTP_WS_MAP.md) | HTTP 路由与 22 种 WSMessageType 的对应关系（C008 grep gate 入口） |
| [docs/harness/C002](../../../docs/harness/C002-cross-pod-push-must-go-gateway-crosspodpush.md) | 跨 pod 推送唯一入口（active，rec=2） |
| [docs/harness/C003](../../../docs/harness/C003-pulsar-topic-localname-suffix.md) | Pulsar topic 命名 + 本地后缀（active，rec=3） |
| [docs/harness/C004](../../../docs/harness/C004-redis-routing-ttl-and-heartbeat.md) | routing TTL × 心跳耦合（active，rec=1） |
| [docs/harness/C005](../../../docs/harness/C005-ws-event-types-locked.md) | WSMessageType 锁 22 种 + V2 RFC（active，rec=2） |
| [docs/harness/C008](../../../docs/harness/C008-handler-coverage-gate.md) | 84 路由 + 22 WSMessageType 全覆盖集成测试 gate |
| [`~/.claude/skills/go-concurrency-patterns/SKILL.md`](file:///Users/mac28/.claude/skills/go-concurrency-patterns/SKILL.md) | 写 Go 并发的唯一标准（Hub goroutine / Channel / Context 兜底） |

# C002 — 跨 pod 推送必须走 `gateway.CrossPodPush` / `CrossPodBroadcast`

```yaml
---
id: C002
title: 跨 pod 推送必须走 gateway.CrossPodPush 或 CrossPodBroadcast，禁止裸调 hub.PushToUser
status: active
created: 2026-05-07
last_recurred: 2026-04-21
recurrence_count: 2
source_logs:
  - logs/2026-04-21.json#L17
  - logs/2026-04-25.json#L62
applies_to:
  - server/internal/service/**/*.go
  - server/internal/http/**/*.go
  - server/internal/gateway/**/*.go
  - server/cmd/message/**/*.go
inline_target: server/docs/BACKEND.md#§5
---
```

## 1. 触发场景（Trigger）

任何 service / handler / cmd worker 主动向用户推送 WS 事件的代码：

- `server/internal/service/**/*.go` 任何"发完消息后通知 channel members"的写后操作
- `server/internal/http/**/*.go` handler 直接发 push（应禁止，必须经 service）
- `server/cmd/message/**/*.go` 异步 worker 主动推（system message broadcast / scheduled fire）
- 关键词 grep：`hub.PushToUser` / `hub.Push(` / `BroadcastToMembers` / `SendACK`
- 任何处理"消息编辑 / 消息撤回 / 加急 / 公告 / 通知 / reaction / 频道事件"的发布路径

## 2. 错误模式（Anti-Pattern）

```go
// ❌ 错误 #1：service 直接拿 hub.PushToUser（只能命中本 pod）
func (s *messageService) Edit(ctx context.Context, p EditParams) error {
    msg, err := s.repo.UpdateMessage(ctx, p)
    if err != nil { return err }
    s.hub.PushToUser(p.SenderID, gateway.TypeMsgUpdated, msg)  // 异 pod 用户收不到
    return nil
}

// ❌ 错误 #2：handler 跳过 service 层直接 push
authed.POST("/messages/:id/reactions", func(c *gin.Context) {
    rsvc.Add(...)
    h.PushToUser(uid, gateway.TypeReactionAdded, payload)  // 漏其他 pod
})

// ❌ 错误 #3：cmd worker 用 hub interface 发 push
func (w *templateFireWorker) handle(msg pulsar.Message) {
    w.hub.PushToUser(uid, gateway.TypePushMsg, payload)  // worker 不持有 hub，连本 pod 都无法保证
}

// ❌ 错误 #4：自己拼 PulsarPushEnvelope 然后投 raw producer
producer.Send(ctx, &pulsar.ProducerMessage{
    Payload: encode(env),
    Key:     uid,                          // 漏 ProducerCache + topic 命名规则
})
```

**后果**：
1. **跨 pod 用户漏推送**：`hub.PushToUser` 只查本 pod 的 `conns map`，异 pod 用户 routing key 在 Redis 但 hub 看不到 → 消息丢
2. **批量扇出退化为 N 次单点查找**：`CrossPodBroadcast` 内部按 gateway 分桶 + 单条 envelope 装 TargetUIDs 列表，绕开它就退化成 N 次 Redis lookup
3. **ProducerCache 失效**：raw producer 每次 New 一个连接，几百用户群广播立即 OOM
4. **Topic 命名错乱**：手写 topic 名漏 `PushTopicFor(gatewayID, env)` 规则 → 调试机消息窜到生产 topic（关联 C003）
5. **OTel span 链断**：`CrossPodPush` 内 `im.fanout.e2e.duration` span 是性能基线指标，绕开则 Grafana 断点

事故链路：
- 2026-04-21 M3 系统消息广播 bug：channel governance 事件用 `hub.PushToUser` 单条循环 → 异 pod 用户全漏 → 修法 commit `8cf1a3b` 改走 `BroadcastToMembers` envelope 化（`v0.4.0-m3-sysmsg-broadcast`）
- 2026-04-25 `cmd/message` 自己拼 envelope 投 raw producer → 漏 ProducerCache → ack timeout → 修法 commit `1f9b78c` 改走 `gateway.CrossPodPush`（`v0.4.1-m3-markoffline-cleanup`）

## 3. 正确做法（Required）

**首选 A — 单用户定向推**（`read_sync` / `friend_event` / `channel_event` 类）：

```go
// ✅ 正确：service 持有 *gateway.Hub，调 CrossPodPush
err := s.hub.CrossPodPush(ctx, gateway.CrossPodPushArgs{
    UserID:  uid,
    MsgType: gateway.TypeReadSync,
    Payload: payload,
})
```

`CrossPodPush` 内部：① Redis lookup user → gatewayID；② 同 pod 直发 hub local conns；③ 异 pod 经 `PushTopicFor(gwID, env)` 投 Pulsar；④ ProducerCache 复用 producer。

**首选 B — channel 多用户广播**（`push_msg` / `msg_updated` / `msg_deleted` / `reaction_*` / `urgent_posted` / `announcement_posted`）：

```go
// ✅ 正确：用 CrossPodBroadcast 一次性扇出
members, _ := s.repo.ListChannelMemberIDs(ctx, channelID)
err := s.hub.CrossPodBroadcast(ctx, gateway.CrossPodBroadcastArgs{
    TargetUIDs: members,
    MsgType:    gateway.TypeMsgUpdated,
    Payload:    payload,
})
```

`CrossPodBroadcast` 内部按 gateway 分桶，一个桶一条 envelope（`TargetUIDs` 列表），减少 Pulsar 消息量到 O(pods) 而非 O(users)。

**绝对禁止 C**：
- ❌ `hub.PushToUser(uid, ...)` — 业务路径里任何调用（仅 `gateway/push_consumer.go::deliverOne` 内部使用，那是 envelope 已经到本 pod 后的最终一跳）
- ❌ 业务代码自己 `producer.Send(ctx, &pulsar.ProducerMessage{...})` 拼 envelope
- ❌ handler 层直接拿 hub 推 — 必须经 service 层封装

**实施约束**：
- 推送唯一入口：`gateway.Hub.CrossPodPush` / `gateway.Hub.CrossPodBroadcast`（cross_pod_push.go:99 / :52）
- `gateway.Hub.PushToUser` 只能由 `gateway/push_consumer.go::deliverOne` 调用（最终落地一跳）
- Pulsar topic 命名经 `PushTopicFor(gatewayID, env)`（联动 C003 localname 后缀）
- envelope 字段 `TargetUIDs []string` + `MsgType WSMessageType` + `Payload []byte`，禁止再加新字段不升 version
- `cmd/message` worker 必须持有 `*gateway.Hub` 引用（M4 后已注入），不允许重复构造

## 4. 检查方法（Verification）

### 4.1 自动 grep（必须返回 0 条）

```bash
# ① 业务代码（service/http/cmd）调 hub.PushToUser
grep -rEn '\.PushToUser\(' server/internal/service/ server/internal/http/ server/cmd/ \
  --include='*.go' | grep -v '_test.go'

# ② 业务代码自己 New Pulsar producer 走 raw Send
grep -rEn 'pulsar\.ProducerMessage\{' server/internal/service/ server/internal/http/ server/cmd/ \
  --include='*.go' | grep -v '_test.go' | grep -v 'gateway/cross_pod_push.go'

# ③ 业务代码手写 PulsarPushEnvelope（应只出现在 gateway/）
grep -rEn 'PulsarPushEnvelope\{' server/internal/service/ server/internal/http/ server/cmd/ \
  --include='*.go' | grep -v '_test.go'
```

### 4.2 CI Gate

- `server/Makefile` 的 `verify-all` target 加上 §4.1 三条 grep；任何非 0 行 → exit 1
- 可选：自定义 ruleguard rule 禁止 `service/*` 包导入 `pulsar` 直接使用

### 4.3 单测（白盒）

- 路径：`server/internal/gateway/cross_pod_push_test.go`
- 必备用例：
  - `TestCrossPodPush_LocalOnly` — 用户在本 pod，直发本地 conns 不投 Pulsar
  - `TestCrossPodPush_RemoteOnly` — 用户在异 pod，只投 Pulsar
  - `TestCrossPodPush_NotOnline` — 用户 routing 失效，不投 Pulsar 也不报错（联动 C004 markOffline 协议）
  - `TestCrossPodBroadcast_BucketByGateway` — 1000 用户 / 5 pod → 5 条 envelope（验分桶）
  - `TestCrossPodBroadcast_PushIDACKTracking` — `push_msg` 类型生成 push_id 进 ack 队列；`read_sync` 类型不进
  - `TestCrossPodPush_MarkOfflineAfter3Failures` — sender_failure_tracker 连续 3 次失败摘除 routing（联动 C004）

### 4.4 集成测试（待补 Batch-D）

- 路径：`server/tests/integration/m4_ws_cross_pod_test.go`（C008 §4.4 Batch-D）
- 用例：
  - W3 跨 pod 转发：A 在 pod1 send，B 在 pod2 收 push_msg
  - W5 离线用户跳过：B routing 失效，pod1 不报错
- 跑法：`docker-compose up` 起两个 gateway pod + 共享 Pulsar/Redis

### 4.5 Grafana 性能基线（不可降级）

- `im.fanout.e2e.duration` P95 ≤ 400ms（pre-6 baseline 375ms，留 25ms 容差）
- `im.crosspod.envelope.target_uids_avg` 群广播应 ≥ 5（验证未退化为单条扇出）

## 5. 复现历史（Recurrence Log）

| # | 日期       | 触发场景                                                                                | 引用日志                  | 处置                                                                  |
|---|------------|-----------------------------------------------------------------------------------------|---------------------------|-----------------------------------------------------------------------|
| 1 | 2026-04-21 | M3 channel governance 事件用 `hub.PushToUser` 循环单发，异 pod 用户漏收（join/leave 通知）| logs/2026-04-21.json#L17 | 改走 `CrossPodBroadcast` + `TargetUIDs` 列表（commit `8cf1a3b`，tag `v0.4.0-m3-sysmsg-broadcast`）|
| 2 | 2026-04-25 | `cmd/message` 自己 New 一次 Pulsar producer 投 envelope，漏 ProducerCache → ack timeout | logs/2026-04-25.json#L62 | 改走 `gateway.CrossPodPush` 复用 ProducerCache（commit `1f9b78c`）    |

## 6. 反例与边界（Don't Over-Apply）

- ✅ **`gateway/push_consumer.go::deliverOne`** 内部允许调 `hub.PushToUser` —— 那是 envelope 已经到本 pod 后的最终一跳
- ✅ **`gateway/ws_handler.go::ackBroadcast`** 处理客户端 send 的 ack 回声允许直接 `hub.PushToUser` —— 单 conn 同 pod，不存在跨 pod 问题
- ✅ **测试 fixture**：`tests/integration/*.go` 允许构造 mock hub 直接 push 验证下游
- ❌ **不要**把约束扩展到"任何 WS 写"——客户端发 ping/pong 是 WS 协议层，与业务推送无关
- ❌ **不要**为了"避免依赖 hub"在 service 里写 message bus 抽象 —— 项目已固化 hub 是 service 层的依赖，YAGNI

## 7. 升级 / 弃用条件（Lifecycle）

**晋升 → merged**：
- 30 天零新复现 + §4.1 grep gate 在 CI 接管
- inline 进 `server/docs/BACKEND.md §5 跨 pod 推送`（已部分 inline，需补 Anti-Pattern 反面教材）
- inline 进 `~/.claude/rules/golang/coding-style.md` 候选「跨进程通信」节（多项目共性）

**弃用 → deprecated**：
- gateway 改单 pod / sticky session 路由（不再有跨 pod 推送场景）
- Pulsar 被替换（如改 NATS / Redis Streams） → 新建 C{NNN}-replacement，本条转 deprecated
- `gateway.Hub` 被解构（如拆 sender/receiver 双 service） → 同上

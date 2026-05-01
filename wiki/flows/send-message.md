---
type: flow
title: 发消息全链路
status: stable
last_verified: 2026-04-28
sources:
  - docs/ARCHITECTURE.md#§3.1
  - server/internal/service/message.go
  - server/internal/repo/message.go:88-130
  - server/internal/gateway/cross_pod_push.go:54-117
related:
  - entities/alloc-seq-and-insert
  - entities/cross-pod-push
  - entities/hub
  - entities/routing
  - concepts/seq-cursor
  - concepts/cross-pod-push
confidence: high
---

# Flow：发消息全链路（最热路径）

> `POST /api/channels/:id/messages` → 落库 + 跨 pod 推送 + 多端 fan-out。理解 IM 性能基线必读。

## 全链路 ASCII

```text
Client
  │ POST /api/channels/:id/messages
  ▼
http.MessageHandler.Send                 [parse + 鉴权 cookie + ctx.User]
  │
service.MessageService.Send              [业务编排，组装 Message struct]
  │
  ├─[A]─ repo.MessageRepo.Send (tx)
  │       ├─ idempotency check (channel_id, client_msg_id)
  │       ├─ AllocSeqAndInsert(ctx, tx, msg)         ★ 见 [[entities/alloc-seq-and-insert]]
  │       │   ├─ UPDATE channels SET last_seq=last_seq+1 RETURNING
  │       │   └─ INSERT messages (seq, sender_id TEXT, team_id, visible_to[], ...)
  │       └─ IncrementPhantomCount (if visible_to set)
  │
  └─[B]─ Hub.CrossPodBroadcast(ctx, recipients, channelID, "push_msg", payload, ...)
           ├─ pushLocalAndCollectRemote(recipients) → fan-out 本 pod conn
           ├─ routing.LookupBatch(remote)            → map[uid][]gatewayID
           ├─ aggregate by gatewayID
           └─ for each gw: producerCache.GetOrCreate(topic).Send(envelope)
                                              │
                                              ▼
              ┌────────────────────────────────────────────┐
              │ 目标 pod's push_consumer subscribe topic   │
              │ → Hub.PushToUser → ws conn → 客户端        │
              └────────────────────────────────────────────┘
```

## 关键不变量

1. **A 在事务内** —— seq 分配 + insert 原子；commit 前不投 push
2. **B 在 commit 后** —— 否则 receiver 可能拿到尚未持久化的消息
3. **A 的事务外 panic** —— 不投 push，客户端会通过 `/api/sync` 心跳补拉（不丢）
4. **B 的 partition key = channelID** —— 同 channel 顺序保证

## 详细步骤

### 1. HTTP 入口（薄层）

```go
// server/internal/http/message.go
func (h *MessageHandler) Send(c *gin.Context) {
    var req SendMessageReq
    if err := c.ShouldBindJSON(&req); err != nil { c.JSON(400, ...); return }
    msg, err := h.svc.Send(c.Request.Context(), c.MustGet("user").(*User), req)
    ...
}
```

仅做：parse + 鉴权（middleware 已注入 user） + 错误映射。**不**直接访问 repo。

### 2. Service 编排

`MessageService.Send`：
1. 构造 `repo.Message{ChannelID, SenderID(string), TeamID, Content, ClientMsgID, VisibleTo, ReplyTo}`
2. `repo.Send(ctx, msg)` —— 走 tx
3. 拿到 `msg.ID, msg.Seq`
4. 异步触发 `CrossPodBroadcast`（不阻塞 HTTP 响应）
5. 返 `{id, seq, ts}`

### 3. Repo 事务

见 [[entities/alloc-seq-and-insert]]。

### 4. 跨 pod 推送

见 [[entities/cross-pod-push]] / [[concepts/cross-pod-push]]。

## 失败模式

| 失败点 | 后果 | 兜底 |
|--------|------|-----|
| A 事务内 PG 错 | 返 500，消息未落库 | 客户端 retry（client_msg_id 幂等） |
| A commit 后 push 失败 | 消息已落库，receiver 暂时收不到 | 心跳 + `/api/sync` 增量补拉 |
| 目标 pod consumer down | Pulsar 持久化兜底 | pod 恢复后回放 |
| `markOffline` 进 L3 | 当前仅 logging，不删 routing | 已知 gap，见 [[flows/cross-pod-failure]] |

## 性能数据

- A：1ms 量级（PG row lock + insert）
- B 本 pod：< 1ms
- B 跨 pod：5–10ms（Pulsar broker RTT）
- 端到端 p99：取决于 visibleTo 大小（phantom_count 批量 UPDATE 是慢点）

## 相关测试

- 单元：`message_test.go`、`cross_pod_push_test.go`
- 集成：`v5_single_flows_test.go` V5.1、`m4_message_send_sync_test.go`
- 集成 G 组：`v5_groups_test.go` G1（发→接→read_sync）

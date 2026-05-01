---
type: entity
title: Hub.CrossPodPush / CrossPodBroadcast
status: stable
last_verified: 2026-04-28
sources:
  - server/internal/gateway/cross_pod_push.go:54-117
  - server/docs/BACKEND.md#§5
related:
  - concepts/cross-pod-push
  - entities/routing
  - entities/producer-cache
  - entities/hub
  - flows/send-message
  - flows/cross-pod-failure
confidence: high
---

# `Hub.CrossPodPush` / `Hub.CrossPodBroadcast`

> 跨 pod 推送的**唯一路径**。解决「本地 hub 命中即发，其他 pod 用户被静默丢弃」的旧 Mattermost 痛点。

## 两个函数

| 函数 | 用途 | partitionKey |
|------|------|-------------|
| `CrossPodPush(userID, ...)` | 单用户推送（read_sync / friend_event / channel_event） | userID |
| `CrossPodBroadcast(userIDs[], partitionKey, ...)` | N 个用户广播（push_msg、announcement 等频道级 fan-out） | channelID |

`CrossPodPush` 是 `CrossPodBroadcast` 的薄壳子（`[]string{userID}`）。

## 4 步 flow（CrossPodBroadcast）

源码位置：`server/internal/gateway/cross_pod_push.go:54-117`

1. **Local fan-out** — 先跑 `pushLocalAndCollectRemote`：本 pod hub 命中的用户**直接**`PushRawToUser`，从 remote 集剔除
2. **Batch lookup** — 剩下的 user 一次 `routing.LookupBatch(userIDs)`（一次 Redis pipelined HGETALL，N 个调用 → 1 次 round-trip）
3. **Aggregate by gatewayID** — 把 N 个 user bucket 到 M 个 pod，每个 pod 一次 `producer.Send`，载荷复用同一份 `json.Marshal`
4. **Envelope** — wire 协议是 `PulsarPushEnvelope{TargetUIDs, MsgType, Payload}`，对端 pod `push_consumer` 拆包后逐个 `hub.PushToUser`

## 关键依赖

- [[entities/routing]] — `LookupBatch` 提供 user → []gatewayID 映射
- [[entities/producer-cache]] — `GetOrCreate(topic)` 复用 256 个 LRU producer
- [[entities/hub]] — `PushRawToUser` 本地推送

## partitionKey 选择规则

| 场景 | partitionKey |
|------|-------------|
| 频道广播（push_msg） | `strconv.FormatInt(channelID, 10)` —— 同 channel 顺序保证 |
| 单用户事件（read_sync / friend_event） | `strconv.FormatInt(userID, 10)` |

错误的 partitionKey 会让消息在不同分区上乱序到达 —— 客户端依赖 seq 重排，但 push 顺序仍以 partition 单调为前提。

## 与 routing TTL 的协作

`Routing` 表 TTL = 45s，心跳 15s × 3。**目标 pod 心跳一旦掉队**，本函数查不到 routing，user 落入 `markOffline` 骨架（见 [[flows/cross-pod-failure]]）。

## 失败处理（current state）

`producer.Send` 失败仅 logging（`log.Warn` + tracing span error）。**没有**清 Redis 路由 / 没有死信 / 没有重投。这是**已知 gap**，标记在 [[milestones/M5-historical-etl]] 之后再补。

## 测试

- 单元：`cross_pod_push_test.go`、`producer_cache_test.go` 用 stub `routingBatchLookup` / `producerGetter` 注入
- 集成：v5_groups_test 覆盖 G3（跨 pod push fan-out）

## 何时不用 CrossPodPush

- 本 pod 内回包（如 send_ack）—— 直接 `hub.PushToUser` 或 `conn.Push`
- 写操作的同步响应（HTTP 200）—— 不走 push 通道

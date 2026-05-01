---
type: concept
title: 跨 pod 推送的三层 fallback
status: stable
last_verified: 2026-04-28
sources:
  - server/docs/BACKEND.md#§5
  - docs/ARCHITECTURE.md#§3.3
  - server/internal/gateway/cross_pod_push.go
related:
  - entities/cross-pod-push
  - entities/routing
  - entities/producer-cache
  - flows/cross-pod-failure
confidence: high
---

# 跨 pod 推送的三层 fallback

> Telegram 风格：**Hub 只保存内存连接，跨 pod 走总线**。本设计取代 Mattermost 的「Cluster 广播 + dead queue 128」。

## 三层路径

```text
推送 user X：
  L1 ✅ 本 pod hub 命中    → hub.PushToUser(X)         [≤1ms]
  L2 ⚠️ Redis routing 命中 → producer.Send(target)     [~5ms 含 Pulsar RTT]
  L3 ❌ routing miss       → markOffline(X) 骨架       [user 真离线]
```

为什么这三层而不是两层：
- 单 pod IM 撑不住生产规模 —— 必须多 pod 横向扩展
- 任何用户消息都可能要送到「另一台机器上的 conn」
- 不能轮询所有 pod —— Pulsar topic by gatewayID 让推送 O(1)

## Pulsar topic 命名（写在石头上）

```text
persistent://im/push/msg.push.{gatewayID}
本地调试自动追加 .{localname} 后缀避免窜台：
persistent://im/push/msg.push.gw-pod-3.zlc-mac
```

`PushTopicFor(gatewayID, env)`，位置 `server/internal/gateway/topic.go`。

详见 [[decisions/hard-constraints]] 第 3 条 + [[entities/cross-pod-push]]。

## Producer 复用

每个目标 gatewayID 一个 Pulsar producer。复用由 [[entities/producer-cache]]（256 LRU + onEvict 关 producer）保证。

## Envelope 协议

```go
type PulsarPushEnvelope struct {
    TargetUIDs []string         // 接收方
    MsgType    WSMessageType    // V1 12 + M2 4 锁定
    Payload    json.RawMessage  // 已序列化，pod 间复用
}
```

接收方 pod 的 `push_consumer` 拆包 → 逐 user `hub.PushToUser` → WS。

## 顺序保证

**partition 维度单调** —— 同一 channel_id 走同一 Pulsar partition，消费顺序与发送一致。客户端依赖 seq 重排，但有序投递可减少 buffer 延迟。

partitionKey 选择见 [[entities/cross-pod-push]]。

## 与 routing TTL 的耦合

L2 命中前提：[[entities/routing]] 在 [[concepts/routing-ttl]] 之内有效。心跳掉队 → routing 过期 → user 进入 L3。

## 为什么不用 gRPC server-to-server

- 需要服务发现 + 健康检查 + 超时重试 → 复杂
- Pulsar 自带 broker / 持久化 / partition / consumer group
- IM 场景下「最多送一次 + 客户端 push_ack 幂等」语义对齐 Pulsar
- 节点故障时消息留在 broker，不丢

## 已知 gap（要注意）

- L3 `markOffline` 没真删除 routing，下次 push 还会失败一次
- 没有死信队列（Mattermost 的「dead queue 128」没复刻）
- 详见 [[flows/cross-pod-failure]]

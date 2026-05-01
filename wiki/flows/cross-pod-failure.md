---
type: flow
title: 跨 pod 推送失败处理
status: drafting
last_verified: 2026-04-28
sources:
  - server/internal/gateway/cross_pod_push.go
  - docs/ARCHITECTURE.md#§3.3
related:
  - entities/cross-pod-push
  - entities/routing
  - concepts/cross-pod-push
  - concepts/routing-ttl
confidence: medium
---

# Flow：跨 pod 推送失败处理

> 此页记录**当前实现 + 已知 gap**。`status: drafting` 因为「真删除路由」尚未落地。

## 失败分类

| 阶段 | 失败 | 当前行为 |
|------|------|---------|
| L1 本 pod hub | conn 已断（chan full / closed） | `PushToUser` 返回 0 → 进 L2 |
| L2 routing.Lookup | Redis miss / TTL 过期 | 进 L3 markOffline 骨架 |
| L2 routing.Lookup | Redis down | logging + tracing error，**不投 Pulsar** |
| L2 producer.Send | Pulsar broker 拒绝 / 超时 | logging + tracing error，**当次失败** |
| L3 markOffline | （骨架）只 logging | **没真删除 routing** ← 已知 gap |

## 当前代码做了什么

```go
// 简化伪代码（实际见 cross_pod_push.go）
func crossPodBroadcastImpl(ctx, remote, ...) {
    routes, err := routing.LookupBatch(ctx, remote)
    if err != nil {
        log.Warn("routing lookup", "error", err)
        return  // ⚠️ 整批放弃，不分级降级
    }
    for gw, uids := range bucketByGateway(routes) {
        producer, err := cache.GetOrCreate(ctx, topicFor(gw, env))
        if err != nil { log.Warn(...); continue }
        if err := producer.Send(ctx, partitionKey, envelope); err != nil {
            log.Warn(...)
            // 没有 markOffline；没有清 Redis 路由
        }
    }
}
```

## 已知 gap（要补）

1. **markOffline 真删路由** —— `producer.Send` 失败应当 `routing.Deregister(uid, gw)`，否则下次 push 还会失败一次
2. **死信队列** —— Mattermost 的 dead queue 128 没复刻；当前失败仅日志
3. **重投策略** —— 没有 backoff 重投，单次 broker 抖动就丢一帧
4. **routing.LookupBatch 整批失败** —— 应当降级为「跳过这批，但继续后续」而不是 return

## 客户端兜底（**这是当前主防线**）

即使 push 丢，客户端不丢消息，因为：
1. 心跳 [[concepts/ws-event-types]] 每 15s 一次 → server 在 pong 中告诉客户端「频道 X 有 delta」
2. 客户端发 [[flows/incremental-sync]] `/api/sync` → 拉回缺的 msg
3. 最坏延迟 ≈ heartbeat 周期（15s）

> **这是为什么当前 gap 不致命**：client-side seq cursor + 心跳兜底是真正的「最终一致性」保证。Push 是性能优化，不是消息可达保证。

## 监控应当采

- `im_push_pulsar_send_failed_total{gateway}` —— 当前未采
- `im_push_routing_lookup_failed_total` —— 当前未采
- `im_push_l3_markoffline_total` —— 当前未采

## 改进路径

按优先级：
1. 高：加 metrics + 告警（看见才能改）
2. 中：markOffline 真删路由 + 单元测试
3. 低：死信队列（设计前先看实际 send_failed 率）
4. 待定：backoff 重投（怕雪崩）

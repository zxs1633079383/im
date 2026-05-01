---
type: entity
title: gateway.ProducerCache
status: stable
last_verified: 2026-04-28
sources:
  - server/internal/gateway/producer_cache.go
  - server/docs/BACKEND.md#§5
related:
  - entities/cross-pod-push
  - concepts/cross-pod-push
confidence: medium
---

# `gateway.ProducerCache`

> 跨 pod 推送的 **Pulsar producer 共享缓存**。256 LRU + onEvict 关 producer 防泄漏。

## 数据结构

```go
type ProducerCache struct {
    capacity int                     // = 256
    lru      *lruImpl                // gateway 内部 LRU
    factory  func(topic) Producer    // 注入 pulsar client 的 NewProducer
    mu       sync.Mutex
}
```

## 核心方法

```go
GetOrCreate(ctx, topic string) (sender, error)
```

- 命中：返回缓存的 producer
- 未命中：调 factory 建 → 写入 LRU → 如果 LRU 触发淘汰 → `onEvict(p)` 调 `p.Close()`

## 为什么是 256

| 维度 | 数字 |
|------|------|
| 一个 producer 对应一个 `msg.push.{gatewayID}` topic | 1:1 |
| 集群最大 gateway pod 数（HPA 上限） | 17 |
| 留余量给本地调试 / 同名分支 | 256 |

256 是经验值，远大于真实 topic 数；触发 LRU 淘汰的可能性极低，但 `onEvict` 必须正确防止边缘情况下泄漏。

## 关键不变量

1. **每个 topic 至多 1 个活 producer** —— 多 producer 写同一 topic 会引发 race / 顺序错乱
2. **被淘汰的 producer 必须 Close** —— Pulsar producer 持有 broker 连接 + buffer，不 close 会泄漏
3. **factory 必须线程安全** —— 多 goroutine 并发 `GetOrCreate` 同一 topic 时只能建一次

## 与 [[entities/cross-pod-push]] 的协作

```text
CrossPodBroadcast
  └─ for each gatewayID in routingResult:
       producerCache.GetOrCreate(topicFor(gatewayID, env))
         └─ producer.Send(ctx, key=partitionKey, payload=envelope)
```

## 拓展点（未做）

- 没有 prometheus metrics（cache hit/miss、evict count）—— 应当加进 OTel 9 项 metric
- 没有 close-on-shutdown 钩子 —— main 进程退出时所有 producer 不优雅关闭，依赖 Pulsar 心跳超时

## 测试

`producer_cache_test.go` 覆盖：
- 命中复用
- 未命中调 factory
- 淘汰触发 Close
- 并发 GetOrCreate 同 topic 只建一次

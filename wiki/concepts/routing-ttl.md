---
type: concept
title: Routing TTL = 45s + heartbeat 15s × 3
status: stable
last_verified: 2026-04-28
sources:
  - docs/GOAL.md#§4.4
  - server/docs/BACKEND.md#§5
related:
  - entities/routing
  - concepts/cross-pod-push
confidence: high
---

# Routing TTL = 45s + heartbeat 15s × 3

> 写在石头上的耦合数字。**改 TTL 必须同时改 heartbeat 周期**，单边动会破容错。

## 数字组合

| 参数 | 值 | 注释 |
|------|----|----|
| `Redis TTL` | **45s** | routing key 过期时间 |
| `heartbeat 周期` | **15s** | 客户端 → 服务端心跳间隔 |
| 容错系数 | **3×** | 连续 3 次丢心跳才会 routing 过期 |

`heartbeat × 容错 = TTL` 是约束等式。改任一项必须保持等式。

## 为什么是这组数字

- **15s 心跳**：移动网络 NAT 超时通常 60s，15s 远低于此
- **3× 容错**：单次丢心跳常见（弱网瞬断），2 次少见，3 次多半真挂了
- **45s TTL**：从「最近一次心跳」算起，45s 不更新就让 broker 自动清理
- 切换 pod 时（部署滚动）TTL 自然过期，不需要主动 deregister

## 一旦改了会出什么事

| 改动 | 后果 |
|------|------|
| TTL 30s + heartbeat 15s | 单次丢心跳就掉路由 → 真断线率上升 |
| TTL 60s + heartbeat 15s | 容错 4× 但用户离线 60s 内还在收 push 失败 |
| TTL 45s + heartbeat 30s | 单次丢心跳后剩 15s 余量，2 次必挂 |
| 心跳异步 / 不可靠 | 整个模型崩 |

## 客户端实现要求

WS conn 必须每 15s 主动 `ping`（携带 channel_seqs，见 [[concepts/ws-event-types]]）；服务端收到 ping 时调 [[entities/routing]] `Heartbeat(userID, gatewayID, 45s)` 续期。

## 与 [[concepts/cross-pod-push]] L3 的关系

routing 过期 → cross-pod-push L2 miss → 进入 L3 `markOffline` 骨架。当前 L3 仅 logging，未实际删除路由（见 [[flows/cross-pod-failure]]）。

## 为什么不用 Pub/Sub presence

- Pub/Sub 不持久化，订阅者断线消息丢
- 需要每个 conn 都订阅自己 → 元数据爆炸
- TTL hash 模型简单，O(1) 查找

## 数据点（监控应当采）

- `im_routing_active_keys` —— 当前活的 routing 数（≈ 在线用户数 × 平均设备数）
- `im_routing_ttl_expired_total` —— TTL 过期 counter（心跳掉队）
- `im_routing_lookup_p99` —— Redis HGETALL p99

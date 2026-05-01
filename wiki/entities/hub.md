---
type: entity
title: gateway.Hub
status: stable
last_verified: 2026-04-28
sources:
  - server/internal/gateway/hub.go
  - server/docs/BACKEND.md#§2.3
related:
  - entities/routing
  - entities/cross-pod-push
  - concepts/ws-event-types
confidence: high
---

# `gateway.Hub` — 本 pod WebSocket 注册中心

> 单 pod 内 `userID → []*Conn` 映射，**纯内存**，无锁竞争路径走 sync.Map。
> 跨 pod 状态在 [[entities/routing]]，本表只管「我自己 pod 上谁还活着」。

## 数据结构

```go
type Hub struct {
    conns sync.Map           // userID(string) → *userBucket
}

type userBucket struct {
    mu       sync.RWMutex
    conns    []*Conn         // 同一用户多设备
}
```

## 核心 API

| 方法 | 用途 |
|------|------|
| `Register(userID, *Conn)` | WS upgrade 成功后挂入 |
| `Deregister(userID, *Conn)` | conn 关闭时摘掉 |
| `ConnsForUser(userID) []*Conn` | 查多设备连接 |
| `PushToUser(userID, msgType, payload) int` | 序列化 + 写每个 conn；返回成功推送条数 |
| `PushRawToUser(userID, msgType, rawPayload) int` | 复用已序列化的 payload（CrossPodBroadcast 优化点） |

## 与 [[entities/routing]] 的协作

Hub 只感知本 pod。**跨 pod 找用户必须**先 `routing.Lookup(userID)` 拿 gatewayID 列表 → 不在本 pod → 走 [[entities/cross-pod-push]] 投 Pulsar。

```text
推送某用户 X：
  ① hub.PushToUser(X) → sent > 0  ✅ 直发完事
  ② hub.PushToUser(X) → sent == 0 ❌ 进入 cross-pod 路径
       └─ routing.Lookup(X) → [gw-pod-3]
            └─ producer.Send(topic=msg.push.gw-pod-3)
                 └─ gw-pod-3 的 push_consumer 收到 → hub.PushToUser(X) → ✅
```

## 多设备 fan-out

`PushToUser` 把同一 payload 写给该用户**所有**conn（多设备并行接收）。每个 conn 有 `send chan *WSFrame` 缓冲，写 chan 满 → drop（**非阻塞**），避免一个慢设备拖死推送线程。

## 与 [[concepts/ws-event-types]] 的关系

`PushToUser(msgType, ...)` 的 `msgType` 必须是 V1 12 + M2 4 = 16 锁定值之一；新增类型必须升 V2 + Rust IPC 事件名 + 三方文档同步。

## 性能特性

- `sync.Map` 在「写少读多」下击败 RWMutex —— Hub 注销 / 注册频率远低于 push
- userBucket 的 `RWMutex` 只在多设备下争用
- 推送走非阻塞 channel write，单 conn 卡死不影响其他

## 测试

- 单元：`hub_test.go` 覆盖注册 / 注销 / 多设备 fan-out / drop on full
- race：`go test -race ./internal/gateway/...` 必须 clean

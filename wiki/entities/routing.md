---
type: entity
title: gateway.Routing
status: stable
last_verified: 2026-04-28
sources:
  - server/internal/gateway/routing.go
  - server/internal/repo/routing.go
  - docs/GOAL.md#§4.4
related:
  - concepts/routing-ttl
  - entities/cross-pod-push
  - entities/hub
confidence: high
---

# `gateway.Routing` — Redis 路由表

> 跨 pod 推送的「黄页」。`userID → []gatewayID` 映射，TTL 45s 心跳 15s。

## 数据布局（Redis）

```text
HASH "im:routing:{userID}"
  field: gateway_id          (e.g. "gw-pod-3")
  value: timestamp_ms        (心跳时间)
  TTL: 45s（被心跳续期）
```

一个 user 同时连多 pod（多设备）→ HASH 多 field。心跳每 15s 续期，3 次容错。

## 关键方法

| 方法 | 用途 |
|------|------|
| `Register(userID, gatewayID, ttl)` | 用户连上时注册 |
| `Deregister(userID, gatewayID)` | 用户断开时注销 |
| `Heartbeat(userID, gatewayID, ttl)` | 续期 |
| `Lookup(userID) → []gatewayID` | 单用户查找 |
| `LookupBatch(userIDs []) → map[userID][]gatewayID` | **批量** pipelined HGETALL |

`LookupBatch` 是 [[entities/cross-pod-push]] 的关键依赖 —— 把 N 次 round-trip 压成一次。

## 数字耦合（不可单边改）

| 参数 | 值 | 改动要求 |
|------|----|---------|
| TTL | 45s | 必须 = heartbeat × 容错系数 |
| heartbeat | 15s | 客户端 / 服务端都改 |
| 容错系数 | 3× | 改要重新算丢包率 |

详见 [[concepts/routing-ttl]]。

## 失败语义

- `Lookup` 返回空 → user offline（或 pod 心跳掉队）→ 进入 `markOffline` 骨架（**未落地真删除**）
- `Register` 失败 → WS 升级失败，客户端会重连
- `Heartbeat` 失败 → 一次失败可容忍（45s 内还有 2 次机会）；连续失败 → conn 主动关闭

## 与 [[entities/hub]] 的关系

```text
本 pod hub.Register(userID, *Conn)        ← 内存
   └─ routing.Register(userID, myGwID)    ← Redis（跨 pod 可见）

本 pod hub.Deregister                     ← 内存
   └─ routing.Deregister(userID, myGwID)  ← Redis
```

两者必须**同步发生**，否则 leaked routing → 推送丢失或推到死 pod。

## 已知 gap

- 没有「pod shutdown 时批量 Deregister 本 pod 所有 user」—— 依赖 TTL 自然过期，最坏 45s 切流期
- `markOffline` 仅日志，没真删除路由 → 下次 push 还是会失败一次（见 [[flows/cross-pod-failure]]）

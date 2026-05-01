---
type: decision
title: Strangler-fig 收官（仅 GET /ws 走 legacy）
status: stable
last_verified: 2026-04-28
sources:
  - server/docs/BACKEND.md#§2.1
related:
  - milestones/M3-stability-cluster
  - decisions/no-traffic-rollback
confidence: high
---

# Strangler-fig 收官

> Phase 6 → 7.8 完成。Gin 接管所有 HTTP 路由，仅 `GET /ws` 通过 NoRoute 落到 legacy mux。

## 决策

- **Gin 是 HTTP 主入口**：所有 `/api/*` 路由由 `RegisterXxxRoutes` 注册
- **Legacy mux 仅剩** `GET /ws`（WebSocket upgrade）—— 因 mux 历史用 mux router 升级
- **替换 Mattermost 后**可彻底移除 legacy mux

## Strangler-fig 历程

| Phase | 内容 |
|-------|------|
| 1–5 | 把每个模块从 mm 插件搬到 Gin handler，旧 path 仍兼容 |
| 6 | 主体业务搬完，少量长尾 endpoint 仍走 mux |
| 7 | 长尾清理完毕 |
| **7.8** | 仅 WebSocket upgrade 留在 mux，其他全 Gin |

## 为什么不把 WS 也搬到 Gin

- gorilla/websocket 与 mux 的 `Upgrader` 集成成熟
- 改路由不带来直接收益（WS 协议本身不变）
- M6 切流后会有彻底重写的窗口，那时再合并

## NoRoute fallback

```go
// 大致结构
ginEngine.NoRoute(func(c *gin.Context) {
    legacyMux.ServeHTTP(c.Writer, c.Request)
})
```

任何 Gin 没有 match 的 path 都丢给 legacy mux。M6 后这块代码删除，legacy mux 一并删除。

## 与 [[decisions/no-traffic-rollback]] 的关系

不留回切 = 旧 mux 早晚要彻底删；当前阶段保留是「物理无法一次性切」，不是「保险起见留着」。

## 测试覆盖

集成测试覆盖了所有 Gin 路由 + WS upgrade 路径，legacy mux 的其他 path 已无业务调用。

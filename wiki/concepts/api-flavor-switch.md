---
type: concept
title: apiFlavor 双栈切换策略
status: stable
last_verified: 2026-04-28
sources:
  - client/src/app/core/config/api.config.ts
  - SESSION.md
  - server/docs/FRONTEND.md
related:
  - entities/im-api-adapter
  - entities/im-seq-data-source
  - milestones/M4-cookie-id-native
confidence: medium
---

# `apiFlavor` 双栈切换

> 客户端层面的灰度开关。`'mattermost' | 'im'` 两个值控制 HTTP 与 WS 走 cses 旧后端还是 im 新后端。

## 核心设计

切换信号**不在客户端配置文件**，而是 cses Java 后端登录响应下发：

```jsonc
// POST /User/login 响应（cses）
{
  ...
  "imGatewayHttp":      "192.168.6.41:30880",   // 非空 → 切 im
  "imGatewayWebSocket": "192.168.6.41:30880"
}
```

字段空 → `apiFlavor = 'mattermost'`；非空 → `apiFlavor = 'im'`。

## 三方一致性

| 层 | 切换机制 |
|----|--------|
| Angular HTTP | `apiFlavor` ↔ `ImApiAdapter` 路由表（[[entities/im-api-adapter]]） |
| Angular WS | `imGatewayWebSocket` 直连 |
| Tauri Rust | feature flag `im_seq_sync` ↔ `ImSeqDataSource`（[[entities/im-seq-data-source]]） |

三层应当对齐，否则会出现「前端切了，Rust 还在 bitmap 模式」的不一致。

## 为什么不留回切

- 流量灰度由 cses 单点决定（不下发字段 = mattermost；下发 = im）
- 成功后**不留 feature flag 回 Mattermost**（[[decisions/no-traffic-rollback]]）
- 减少客户端配置爆炸 → 一处开关，多端联动

## 切换时机

- 用户登录或重连主 WS 时 → 读 cses 下发字段 → 决定本会话用哪个栈
- 切换需重连 WS（`reconnectMainWs` 触发）

## 调试期表现

```
console:
🔀 [MessageHttpService] apiFlavor: mattermost → im
🔄 [LOGIN] reconnectMainWs ... routedToIm: true
```

Network 验：
- `imHttp.*` → `http://192.168.6.41:30880/api/...`
- `this.http.*` → `196.168.1.177:3399/api/cses/...`

## 已知坑

| 现象 | 排查 |
|------|------|
| `apiFlavor: mattermost → mattermost` 没切 | cses 没下发 imGatewayHttp，看 XPA-12.yaml `imGatewayEnabled: true` |
| WS upgrade 401 | cookieId 没在 cses Redis，`HGET User '"<cookieId>"'` |
| `ImEndpointNotMappedError` | route-table.ts 漏了 endpoint 映射 |

详见 SESSION.md §0 的联调清单。

---
type: entity
title: ImApiAdapter (Angular client)
status: stable
last_verified: 2026-04-28
sources:
  - client/src/app/core/config/api.config.ts
  - client/src/app/core/messages/message.service.ts
  - server/docs/FRONTEND.md
  - SESSION.md
related:
  - concepts/api-flavor-switch
  - entities/im-seq-data-source
confidence: medium
---

# `ImApiAdapter` —— 前端双栈分流

> Angular 端：所有新接口走 ImApiAdapter，**不**直接拼 URL。`apiFlavor` 切换决定调 mm csesapi 还是 im。

## 配置中心

`client/src/app/core/config/api.config.ts`：

```ts
apiFlavor: 'mattermost' | 'im'
imGatewayHttp: string             // 由 cses Java 后端登录时下发
imGatewayWebSocket: string        // 同上
```

`apiFlavor` **不**在前端 environment.ts 写死。由 cses 登录响应（`/User/login`）的字段决定：

```text
{
  imGatewayHttp:        "192.168.6.41:30880"
  imGatewayWebSocket:   "192.168.6.41:30880"
}
```

字段空 → 留 `mattermost`；非空 → 切到 `im`。

## 路由表

`route-table.ts`（典型条目）：

| Endpoint | 走向 |
|----------|------|
| `imHttp.*`（auth/channel/message/sync/file/search/...） | im 后端 `imGatewayHttp:30880/api/...` |
| `this.http.*`（vote/Im/search/average/template） | 旧 cses Java `196.168.1.177:3399/api/cses/...` |

新加 endpoint **必须**先在 `route-table.ts` 登记 → 否则运行期 throw `ImEndpointNotMappedError`，console 报红。

## message.service.ts 的角色

「双栈消息聚合层」。每条消息相关 API 调用先看 `apiFlavor`：

```ts
if (this.config.apiFlavor === 'im') {
  return this.imHttp.post('/messages', ...)   // 命中 ImApiAdapter
}
return this.http.post('/posts/create', ...)   // 旧 mattermost
```

逐步替换中（37 个 URI），M4 完工后整批迁移。

## 与 Tauri Rust 的协作

WebSocket 同样双栈：`apiFlavor=im` 时 Rust 侧用 [[entities/im-seq-data-source]]；否则继续 IMDataSource。Rust flag：`im_seq_sync`。

## 已知陷阱

1. **`apiFlavor: mattermost → mattermost`** —— cses 没下发字段 → 检查 XPA-12.yaml `imGatewayEnabled: true`（详见 SESSION.md §0）
2. **`ImEndpointNotMappedError`** —— 漏网调用 → 5 分钟在 route-table 补 mapping
3. **WS upgrade 401** —— cookieId 没在 cses Redis → `redis-cli HGET User '"<cookieId>"'` 验

## 当前进度（v0.7.2）

- ✅ Angular 切换 import 完整
- ✅ Mattermost 死代码全删（commit `7c8a0c972`）
- ✅ tsc 干净，src-tauri 0 改动
- ⏳ 等 Tauri client 联调（17692704771/123456 登录）

## 历史误解（2026-04-30 纠正，见 [[log]]）

第一版分析（**未落盘，仅会话内**）建议在 ImApiAdapter / route-table.ts 维护一张 cses-shape → im REST 的**翻译表**，让 message.service.ts 调用面零改动。**这是错的**：

- 翻译层是永久技术债 —— cses-shape 在前端代码里活多久，新人就要查多久 mapper
- 与 [[decisions/strangler-fig-collapsed]] 哲学不对称 —— 后端不留兼容层，前端也不该留
- bitmap 协议（`/posts/getPostsAfterFromSegment` 等）应该**整族砍掉**而不是"翻译成 `after?seq=`"，因为 [[entities/im-seq-data-source]] + `POST /api/sync` 已经接管增量同步

**正确路线**（[[syntheses/min-cost-mattermost-cutover]] v2）：
- message.service.ts 37 条 imHttp 调用**直接 rewrite** 成 im RESTful path + body
- `route-table.ts` 退化为"哪些 endpoint 在 im / 哪些在 cses Java"的二分表，**不做 path 翻译**
- 客户端 cses-shape 一次性消失，与后端 strangler-fig 收官对称

route-table.ts 的"必须先登记否则 throw `ImEndpointNotMappedError`" 机制本身仍保留 —— 它是双栈分流的安全网，不是翻译层。

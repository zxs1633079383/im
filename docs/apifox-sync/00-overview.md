# 00 总览 — 体系图 + 公共契约

## 1. 体系图

```
                       cses-client (Angular + Tauri Rust)
                                   │
              cookieId + companyId header + userId 三件套
                                   ▼
   ┌────────────────────── im gateway :8080 ─────────────────────────┐
   │  middleware: otelgin → corsMiddleware → responseEnvelope         │
   │     ↓                                                            │
   │  /api/* group:                                                   │
   │     MattermostCookieResolve (LRU 10k × 30s + Redis GET UserData:<id>) │
   │     CookieRequired (gate)                                        │
   │     ↓                                                            │
   │  RegisterXxxRoutes(authedAPI, svc, pusher) ←—— 92 路由           │
   │                                                                  │
   │  WS /ws (server-side mux not gin):                               │
   │     ws_handler.authenticate (同一 ResolveCookieID)               │
   │     conn = Hub.Register(conn) + heartbeat + readPump              │
   │     readPump dispatch: ping / push_ack / send                    │
   └──────────────────────────────────────────────────────────────────┘
              │                                            │
              ▼                                            ▼
         PostgreSQL                                 Pulsar (push fan-out)
   (messages / channels /                       PushTopicFor(gwID,env) per pod
    channel_members ...)                       PulsarPushEnvelope.TargetUIDs
```

## 2. 鉴权三件套（v0.7.4 起）

| header | 含义 |
|---|---|
| `cookieId` | 24-hex 字符串，**== userId**（v0.7.4 collapsed） |
| `userId` | 同 cookieId，显式冗余以便上游审计 |
| `companyId` | 24-hex 租户 ID，stamp 到 ctx 的 `teamIDCtxKey` |

服务端解析路径：

1. `middleware.MattermostCookieResolve`：读 `cookieId` header → LRU 命中直接复用 → 未命中走 `Redis GET UserData:<cookieId>` → JSON 反序列化为 `MattermostUser` → 写 ctx 的 `UserIDKey` / `UsernameKey` / `mmUserCtxKey`
2. `middleware.CookieRequired`：读 ctx 的 `UserIDKey`，缺失即 `401 missing auth: cookieId header required`

参考 fixture（生产已存在）：`UserData:676cc4ccfbbc501161d5cd65` = 张立超 / companyId `6111fb0a202d425d221c53db`。

WS 路径走 `middleware.ResolveCookieID(ctx, rdb, cookieID, log)` —— 完全同一套缓存 + Redis 路径，header 来源额外支持 `?cookieId=` 查询参数（浏览器无法自定义 header 时用）。

## 3. 全局响应信封

`internal/http/response_envelope.go` 中间件无差别包：

```json
{ "status": "success", "data": { ... } }
{ "status": "error",   "error": "missing auth: cookieId header required" }
```

- handler 调用 `c.JSON(code, data)` 时，`data` 落到 envelope 的 `.data`；
- handler 调用 `c.JSON(code, gin.H{"error": "..."})` 时，error 字段被提到 `.error`；
- HTTP 状态码沿用 handler 设的（200 / 201 / 4xx / 5xx）。

cses-client interceptor 必须删除 `isWrappedResponse` 那段双重 unwrap —— 现在永远是一层。

## 4. 92 路由分组速查

| group | 数量 | 起点 |
|---|---|---|
| 健康检查 | 3 | /healthz /readyz /metrics |
| 登录鉴权 | 3 | /api/auth/* |
| 消息收发（含 6 子组） | 23 | /api/messages/* /api/channels/:id/messages |
| 群聊管理（4 子组） | 22 | /api/channels/* |
| 公告 | 6 | /api/announcements/* |
| 审批 | 7 | /api/approvals/* |
| 通知中心 | 4 | /api/notifications/* |
| 反应表情 | 3 | /api/messages/:id/reactions |
| 快捷回复 | 4 | /api/quick-replies/* |
| 收藏 | 3 | /api/favorites/* |
| 好友 | 6 | /api/friends/* |
| 用户 | 1 | /api/users/search |
| 文件 | 3 | /api/files/* |
| 在线状态 | 2 | /api/presence /api/channels/online-status |
| 模块入口 | 1 | /api/modules |
| 设置 | 2 | /api/settings |
| 搜索 | 1 | /api/search |

总计：**92** 路由（vs CLAUDE.md 提到的 84，差额来自 v0.7.x 增量端点 + 健康检查 + WS）。

## 5. WS 协议总览

- **path**：`GET /ws`（在 cmd/gateway/main.go:160 用 net/http mux 而非 gin）
- **auth**：`cookieId` / `CookieId` / `Cookieid` header **或** `?cookieId=` / `?cookie_id=` 查询参数。失败 401。
- **拓扑**：Hub 是 per-process userId→conn[]，跨 pod 推送走 Pulsar PushTopicFor(gwID,env)
- **心跳**：服务端发起。client 发 ping 也行，但服务端**不会回 pong**（client 的 ping 只用来 refresh routing TTL + ChannelSeqs，pong 是服务端 → client 单向）
- **22 个 type** = 4 client→server + 18 server→client
- **WSFrame envelope**：`{"type": "...", "payload": json_str_or_object}`

完整 type 表见 `05-websocket-protocol.md`。

## 6. 关键边界

| 边界 | 契约 |
|---|---|
| user_id 的 wire 类型 | 全场景 string（24-hex mm UserID），v0.7.4 起 cookieId == userId |
| 消息 ID 的 wire 类型 | int64（PG BIGINT），客户端用 JSON number 解析需注意精度 |
| created_at 的 wire 类型 | RFC3339（含时区），不是 unix ms |
| msg_type 取值 | 1=text 2=image 3=file 4=system 99=phantom |
| 跨 pod 推送 | gateway.CrossPodPush / CrossPodBroadcast 唯一入口，不得直接 Hub.PushToUser |
| 消息写入 | repo.MessageRepo.AllocSeqAndInsert 唯一入口（详见 docs/harness/C001） |
| WS 事件锁定 | 22 种（详见 docs/harness/C005），新增走 V2 RFC |

# CSES-CLIENT × im 后端 对接契约 Integration Guide

> **谁读这个**：cses-client（Angular + Tauri）端 / 联调 QA / cses Java 桥接团队。
> **不读什么**：im 后端内部架构（看 `docs/ARCHITECTURE.md`）/ 后端开发流程（看 `docs/GOAL.md` + `CLAUDE.md`）。
>
> **当前后端状态**（2026-05-12）：`v0.7.3-backend-final` + cses-client 9 gap 补丁
> - **87 路由 + 26 WSMessageType 全实现**（v0.7.3-backend-final 84+22，加 cses-client 9 gap 落地：DELETE channels/:id、GET /messages/:id/replies/branch、PATCH /channels/:id/members/:user_id/nickname；channel_closed / channel_member_updated / schedule_created / schedule_canceled）
> - 198 集成测试全绿（Batch-A 12 + B 130 + C 27 + D 20 + E 6 + push hook 2 + ws ref 1，新增 gap 端点单测在 P1 补）
> - 9 条 active harness（C001-C009 CI gate 卡死契约）
> - envelope `{status, data?, error?}` 全局统一
> - cookieId 单栈鉴权 + LRU 30s 缓存
> - 26 WS type 中 22 server→client 全有 active happy path
> - **push_msg payload 新增 `type` + `props` 字段**：msg_type=4 系统消息自动带 `type:"NOTICE"`（gap #9 字段对齐），cses-client Rust message_service 现有 `data.type == "NOTICE"` 分支无需改造即可识别
>
> **关联文档**：
> - `docs/IM_DATA_MODEL_新版数据模型字典.md` — **entity / DTO / payload 字段字典**（本文是 endpoint contract，那篇是 schema reference）
> - `docs/CSES_CLIENT_CUTOVER.md` — 客户端 cutover 4 Phase 计划（Phase 1 ✅，Phase 2-4 ⏳）
> - `docs/HTTP_WS_MAP.md` — HTTP ↔ WS 推送对应矩阵
> - `docs/harness/C005` — WS 22 type 锁定决策
> - `wiki/comparisons/csesapi-vs-im-coverage.md` — 125 cses 路由 vs 84 im 路由对账
> - cses-client 仓库 `src/app/core/im-api/route-table.ts` — 路径翻译表
> - cses-client 仓库 `src/app/core/im-api/ws-normalizer.ts` — WS 事件归一化
> - cses-client 仓库 `docs/messagev3api统计.md` — 客户端实际调用 39+14 端点 + 16 imWs 事件清单

---

## 1. 三仓库联调拓扑

```
┌─────────────────────────────────────────────────────────────┐
│  cses-client (Angular + Tauri 2)                            │
│  - apiFlavor 切换器（mattermost ↔ im）                      │
│  - ImApiAdapter（消息收发 / sync 增量）                     │
│  - ws-normalizer（22 type → 16 imWs:* 事件）                │
└─────────────────┬───────────────────────────────────────────┘
                  │
                  ├─→ cses Java（vote / search / average / file 分片 / template / modules / Bot / Webhook ...）
                  │   保留路径 /api/cses/...（继续走老栈）
                  │
                  └─→ im Go（messages / channels / friends / sync / WS / 治理 / 公告 / ...）
                      路径前缀 /api/...
                      auth header: cookieId (24-char hex)
                      响应 envelope: {status, data?, error?}
```

| 仓库 | branch | 当前 HEAD | 角色 |
|---|---|---|---|
| im (go) — 本仓 | `main` | `a8628b9` (`v0.7.3-backend-final`) | 提供 RESTful + WS 后端 |
| cses (java) | `Feature-new-im` | `20e750bea` | 登录/鉴权/老栈业务，下发 `imGatewayHttp` + `imGatewayWebSocket` 字段 |
| cses-client (angular) | `im-backend-switch` | `7c8a0c972` | 客户端，执行 cutover Phase 2-4 |

---

## 2. 鉴权契约

### 2.1 单栈：仅信 cookieId （v0.7.4 UserData 模型）

> v0.6.0 M4 起，im 后端**不维护本地 users 表**。所有用户身份从 cses 写入的 Redis 解析。
> v0.7.4 起 Redis 数据形态由 `HASH "User"` 改为 `STRING "UserData:<userId>"`；同时
> cookieId header 的值不再是独立 session token，**等于 mm UserID 自身**。

**HTTP 请求**：

```http
GET /api/me
cookieId:  676cc4ccfbbc501161d5cd65    ← 即 userId（v0.7.4）
companyId: 6111fb0a202d425d221c53db    ← 新增必传 header，im 用于 team_id 兜底
```

- `cookieId` header（大小写不敏感，常量 `internal/middleware.MMCookieHeader`）：
  24 字符小写 hex；**值等于 mm UserID**（v0.7.4 wire 简化，从前的 session token 模型废弃）
- `companyId` header（新增，常量 `internal/middleware.MMTeamHeader`）：当前活跃公司 id；
  im 通过 `TeamIDFromCtx(c)` 直接读，**不再从 Redis 派生**；缺失视为 NULL（无 team 上下文）
- cses 登录时写 Redis：`SET UserData:<userId> <json>`，json 是身份信息（id/mobile/name/userName + organizes[]）
- im `MattermostCookieResolve` 中间件 `GET UserData:<userId>` + LRU 30s 缓存 → `MMUserFromCtx(c)` 拿当前用户
- `CookieRequired` 中间件后置：cookieId header 缺失 / 无效 → **401**

**Redis wire 形态对比**：

| 维度 | v0.6 - v0.7.3（旧） | v0.7.4（新）|
|---|---|---|
| Redis 类型 | HASH | **STRING** |
| Key | `User` | `UserData:<userId>` |
| Field | `"<cookieId>"`（JSON-quoted）| ——（无）|
| Value | 平铺 MattermostUser JSON | 嵌套：id/mobile/name/userName + organizes[] |
| cookieId 语义 | 独立 session token（24-hex）| **= mm UserID** |
| 公司/组织字段 | 平铺 payload.companyId / orgId / deptId | **不取 payload，从 `companyId` header 读** |

**新 JSON wire 示例**（cses Java 写入）：

```json
{
    "id": "676cc4ccfbbc501161d5cd65",
    "mobile": "17692704771",
    "name": "张立超",
    "userName": "张立超",
    "userId": "",
    "organizes": [
        {
            "companyId": "6111fb0a202d425d221c53db",
            "companyName": "中企云链（北京）信息科技有限公司",
            "orgId": "6311a17c50c75d009ed3864f",
            "orgName": "后端开发",
            "deptId": "616cee6ef7a6ae6354cddd9b",
            "deptName": "技术部",
            "userId": "676cc4ccfbbc501161d5cd65",
            "userName": "张立超"
        }
    ]
}
```

im 后端**只读** id / mobile / name / userName 4 个顶层字段；**完全忽略** organizes[] 数组
（多 organize 由 cses-client 通过 `companyId` header 携带当前活跃公司）。

**WebSocket 握手**：

```
GET /ws?device=web-<uuid>
cookieId: 676cc4ccfbbc501161d5cd65    ← 即 userId（v0.7.4）
```

3 种鉴权来源（按优先级）：
1. `CookieId` / `cookieId` HTTP header（首选，与 HTTP 一致）
2. `?cookieId=<value>` / `?cookie_id=<value>` query 参数（浏览器 fallback，不能设 header 时）
3. `?token=<jwt>` query 参数（JWT 老路径，**仅 admin 入口保留** `/api/admin/*`，业务**禁用**）

握手失败统一 401，**不要**降级到 200。

> ⚠️ **WS 握手不需要 companyId header**：team_id 在 WS 协议层没有用，channel-level
> 数据访问已经通过 channel_members 表绑定关系实现。companyId header **只在 HTTP**
> 请求上必传。

### 2.2 已废弃：register / login

```http
POST /api/register   →  410 Gone
POST /api/login      →  410 Gone
```

cses-client 不应再调这两个端点。

### 2.3 cookieId fixture（联调用）

张立超账号（`internal/testutil/cookie_fixture.go`）：

| 字段 | 值 |
|---|---|
| **cookieId（== userId）** | `676cc4ccfbbc501161d5cd65` |
| companyId（header 必传） | `6111fb0a202d425d221c53db` |
| orgId | `6311a17c50c75d009ed3864f` |
| name | 张立超 |
| mobile | 17692704771 |

灌库脚本：`server/scripts/seed-mm-cookies.sh`（v0.7.4 `SET UserData:<userId>` STRING）

curl smoke：

```bash
curl -H 'cookieId: 676cc4ccfbbc501161d5cd65' \
     -H 'companyId: 6111fb0a202d425d221c53db' \
     http://192.168.6.41:30880/api/me
```

---

## 3. 响应 envelope 契约（C007）

**所有** `/api/*` 响应（包括 4xx/5xx 错误）走全局 envelope wrap，由 `internal/http/response_envelope.go::responseEnvelope` 中间件统一处理。

### 3.1 成功（2xx）

```json
{
    "status": "success",
    "data": <handler 原始 body>
}
```

`data` 可以是 object / array / primitive，原样保留 handler 返回值。

### 3.2 错误（4xx / 5xx）

```json
{
    "status": "error",
    "error": "human-readable message"
}
```

handler 内部可写 `c.AbortWithStatusJSON(401, gin.H{"error": "missing auth"})`，envelope 中间件提取 `error` 字段 + 包装。HTTP status code 不变（4xx 仍是 4xx）。

### 3.3 客户端 interceptor 行为

cses-client `imHttp` interceptor 应：
- 看到 `status == "success"` → 取 `data` 作为业务数据
- 看到 `status == "error"` → 抛业务异常 + `error` 作为 message
- **不再做** `isWrappedResponse` 双重判断（cses Java 老 shape 不存在 envelope，这是 im 独有）

### 3.4 跳过 envelope 的路径

```
GET /healthz       k8s liveness probe，返回 plain "ok"
GET /readyz        k8s readiness probe
GET /metrics       Prometheus 抓取，text/plain
GET /ws            WebSocket upgrade（hijack underlying conn，envelope 无法 wrap）
```

---

## 4. HTTP 端点全集（84 路由 / 17 family）

> **格式约定**：所有路径都以 `/api` 为前缀（如 `/me` 实际是 `/api/me`）。每个 family 的 import 都会被 `RegisterAuthRoutes` / `RegisterMessageRoutes` 等 handler 注册到 `engine.Group("/api")`。

### 4.1 auth (3)
| Method | Path | 说明 | 客户端调用 |
|---|---|---|---|
| GET | `/me` | 当前用户 (MMUserFromCtx 透传) | login 后立即 GET 一次确认 cookie 生效 |
| POST | `/register` | **410 Gone** | 不要调 |
| POST | `/login` | **410 Gone** | 不要调 |

### 4.2 channel (9)
| Method | Path | 说明 |
|---|---|---|
| POST | `/channels` | 创建 group channel；body `{name, member_ids: []string}` |
| POST | `/channels/dm` | 创建 / 取 DM；body `{peer_id: string}`，幂等（已有则直接返回） |
| GET | `/channels` | 当前用户参与的 channel 列表 |
| GET | `/channels/:id` | 单 channel 元数据 |
| GET | `/channels/:id/members` | 成员列表（含 role / is_top / notify_pref） |
| POST | `/channels/:id/members` | 加成员；body `{user_id: string}`；触发 channel_event WS push 给被加者 |
| DELETE | `/channels/:id/members/:user_id` | 踢人（owner-only） |
| POST | `/channels/:id/leave` | 主动退出 |
| PUT | `/channels/:id` | 整体替换（业务上很少用，建议走 PATCH） |
| DELETE | `/channels/:id` | **owner 解散群聊（v0.7.3 gap #1）**；标 `channels.deleted_at = now()`；触发 **channel_closed** WS 给原 channel 全员。Idempotent — 已关闭返 200 + 当前 channel snapshot |
| PATCH | `/channels/:id/members/:user_id/nickname` | **设置群内昵称（v0.7.3 gap #5）**；body `{nick_name: string}` ≤ 64 字符，空串清除回退到全局名；caller==target 即可，否则需 admin/owner；触发 **channel_member_updated** WS 给 channel 全员 |

### 4.3 channel-governance (8)
| Method | Path | 说明 |
|---|---|---|
| PATCH | `/channels/:id` | 局部更新 notice/purpose/orient/permission/name；触发 **channel_info_updated** 给全成员 |
| POST | `/channels/:id/managers/:user_id` | owner 加管理员 |
| DELETE | `/channels/:id/managers/:user_id` | owner 移除管理员 |
| GET | `/channels/:id/managers` | 管理员列表 |
| POST | `/channels/:id/pins/:message_id` | 置顶消息（owner / manager） |
| DELETE | `/channels/:id/pins/:message_id` | 取消置顶 |
| GET | `/channels/:id/pins` | 置顶列表 |
| PATCH | `/channels/:id/members/:user_id` | 改成员 role / notify_pref / **is_top**；is_top flip 触发 **channel_top_updated** 给调用者多设备 |

### 4.4 channel-topic (2)
| Method | Path | 说明 |
|---|---|---|
| POST | `/channels/:id/topics` | 把消息升为话题（替代 cses `/posts/makeTopic`） |
| GET | `/channels/:id/topics` | 话题列表 |

### 4.5 message (11)
| Method | Path | 说明 |
|---|---|---|
| POST | `/channels/:id/messages` | 发消息；body `{content, msg_type, visible_to?, client_msg_id?}`；触发 **push_msg** 给 channel members |
| GET | `/channels/:id/messages` | 列消息；query `?before=<seq>&limit=N` |
| GET | `/channels/:id/messages/around` | 围绕一条消息的上下文；query `?timestamp=<ms>&radius=N` |
| POST | `/channels/:id/read` | 推进 last_read_seq；**body 为空**（server 用当前 channel.Seq）；触发 **read_sync** 给同用户其他设备 |
| GET | `/messages/:id/readers` | 已读用户列表 |
| GET | `/messages/:id/replies` | thread replies（一次性返回全部，无分页） |
| GET | `/messages/:id/replies/branch` | **二级 thread 子回复分页（v0.7.3 gap #2）**；query `?offset=N&limit=M`（默认 0/50，上限 200）；返回 `{messages, has_more, offset, limit}`。替代 mattermost `/posts/getReplyBranch` |
| GET | `/messages/:id/after` | 从某 seq 之后的增量（替代 cses `/posts/getPostsAfterFromSegment`） |
| PATCH | `/messages/:id` | 编辑（仅 sender）；触发 **msg_updated** |
| DELETE | `/messages/:id` | 撤回（仅 sender）；触发 **msg_deleted** |
| POST | `/messages/forward` | 转发；body `{message_id: int64, target_channel_ids: [int64]}` |
| POST | `/messages/batch` | 批量发；body `{channel_id, messages: [{content, msg_type}]}`；非成员消息静默跳过返 201 + 空 array |

### 4.6 message-template + read-stats (Phase 1 三件套)
| Method | Path | 说明 |
|---|---|---|
| POST | `/messages/:id/received` | 模板"已收到"按钮；append callerID 到 `props.template.userIds`，幂等；触发 **msg_updated** 给 channel members |
| GET | `/messages/read-stats` | 批量读统计；query `?ids=1,2,3`（**用 `.WithQuery`，C006 规则**）；返回 `{stats: [{message_id, total_members, read_count, unread_count, unread_user_ids[], has_more_unread}]}` |

### 4.7 message-attachment (1)
| Method | Path | 说明 |
|---|---|---|
| GET | `/messages/:id/attachments` | 取消息附件元数据（file_id 列表） |

### 4.8 sync (1)
| Method | Path | 说明 |
|---|---|---|
| POST | `/sync` | 增量同步；body `{channels: [{id, seq}]}`；返回 `{channels: [{id, server_seq, messages: []}]}`。**取代 cses 13 条 bitmap 协议**（参考 `wiki/concepts/seq-cursor.md`）。Tauri Rust ImSeqDataSource 单点调用 |

### 4.9 friend (7)
| Method | Path | 说明 |
|---|---|---|
| POST | `/friends/request` | 发好友请求；触发 **friend_event** (`event_type: "request"`) |
| POST | `/friends/accept` | 同意；触发 friend_event accepted |
| POST | `/friends/reject` | 拒绝 |
| POST | `/friends/block` | 拉黑 |
| GET | `/friends` | 好友列表 |
| GET | `/friends/pending` | 待处理 |
| GET | `/users/search` | 搜索用户（仅 friend 邀请用，不是全文搜索） |

### 4.10 favorite (3) — 替代 cses `/post_bookmark/*`
| Method | Path | 说明 |
|---|---|---|
| POST | `/favorites/:message_id` | 收藏 |
| DELETE | `/favorites/:message_id` | 取消（删别人的收藏 → 404） |
| GET | `/favorites` | 列表 |

### 4.11 announcement (6) — 替代 cses `/post/announcement/*`
| Method | Path | 说明 |
|---|---|---|
| POST | `/announcements` | 创建（owner / manager-only）；触发 **announcement_posted** 给 channel members |
| POST | `/announcements/:id/read` | 标已读 |
| GET | `/announcements/:id` | 详情 |
| GET | `/announcements/:id/acks` | 已读列表（manager-only） |
| GET | `/channels/:id/announcements` | 频道公告列表 |
| DELETE | `/announcements/:id` | 删除（owner / manager-only） |

### 4.12 urgent (4)
| Method | Path | 说明 |
|---|---|---|
| POST | `/messages/urgent` | 发加急；body `{channel_id, content, client_msg_id?}`；触发 **urgent_posted** |
| POST | `/messages/:id/urgent/confirm` | 确认收到（非 sender） |
| POST | `/messages/:id/urgent/cancel` | 取消（sender / manager） |
| GET | `/messages/:id/urgent/confirmations` | 确认列表 |

### 4.13 approval (7)
| Method | Path | 说明 |
|---|---|---|
| POST | `/approvals` | 提交申请；body `{channel_id, approver_id, subject, content, props?}`；触发 **approval_updated** 给 requester + approver |
| POST | `/approvals/:id/approve` | 审批人通过 |
| POST | `/approvals/:id/reject` | 审批人拒绝 |
| POST | `/approvals/:id/cancel` | 申请人取消（仅 pending 状态） |
| GET | `/approvals/pending` | 我作为 approver 的待办 |
| GET | `/approvals/mine` | 我作为 requester 的提交 |
| GET | `/approvals/:id` | 详情（仅 requester / approver） |

### 4.14 notification (4)
| Method | Path | 说明 |
|---|---|---|
| POST | `/notifications` | 发通知；body `{receiver_id, title, body, type}`；触发 **notification_received** 给 receiver |
| POST | `/notifications/:id/read` | 标已读 |
| GET | `/notifications/sent` | 我发出的（response shape `{notifications: [...]}`） |
| GET | `/notifications/received` | 我收到的 |

### 4.15 quick-reply (4)
| Method | Path | 说明 |
|---|---|---|
| POST | `/quick-replies` | 创建快捷回复模板 |
| GET | `/quick-replies` | 列表（response shape `{quick_replies: [...]}`） |
| PATCH | `/quick-replies/:id` | 更新 label / content |
| DELETE | `/quick-replies/:id` | 删除 |

### 4.16 reaction (3) — v0.7.0 新增，替代 cses `/posts/quickReply` 表情功能
| Method | Path | 说明 |
|---|---|---|
| POST | `/messages/:id/reactions` | 加 reaction；body `{emoji}`；触发 **reaction_added** 给 channel members |
| DELETE | `/messages/:id/reactions/:emoji` | 减 reaction；触发 **reaction_removed** |
| GET | `/messages/:id/reactions` | 列出该 message 所有 reaction |

### 4.17 scheduled (3)
| Method | Path | 说明 |
|---|---|---|
| POST | `/messages/scheduled` | 创建定时消息；body `{channel_id, content, msg_type, scheduled_at}`；scheduled_at 必须 ≥ 60s 未来 |
| DELETE | `/messages/scheduled/:id` | 取消（sender-only，pending 状态） |
| GET | `/messages/scheduled` | 我的定时消息列表（response shape `{scheduled: [...]}`） |

### 4.18 presence (2) — v0.7.2 新增
| Method | Path | 说明 |
|---|---|---|
| GET | `/presence` | 单频道在线列表；query `?channel_id=X`（C006 规则用 `.WithQuery`）；返回 `{online_user_ids: []string}` |
| GET | `/channels/online-status` | 批量在线状态；query `?channel_ids=1,2,3&include_users=true`；返回 `{channels: [{channel_id, online_count, online_user_ids?}]}` |

### 4.19 module (1) — v0.7.2 新增
| Method | Path | 说明 |
|---|---|---|
| GET | `/modules` | 模块分组列表（migration 016 seed 6 行） |

### 4.20 settings (2)
| Method | Path | 说明 |
|---|---|---|
| GET | `/settings` | 当前用户偏好；无 row 时返回默认 `{theme: "system", language: "zh", notification_enabled: true}` |
| PUT | `/settings` | 局部更新；body 任一字段；**注意：notification_enabled 字段有 GORM Upsert default:true bug，避免单独 flip false**（见 `internal/http/settings.go:73` 注释） |

### 4.21 search / file（**im 不实现，留 cses Java**）
| Method | Path | 说明 |
|---|---|---|
| GET | `/search` | 全文搜索 — **cses Java 拥有**，cses-client 走 `this.http`（baseUrl `196.168.1.177:3399/api/cses`）|
| POST | `/files` | 文件上传 — **cses Java + 外部 OSS 拥有** |
| GET | `/files/:id` | 文件元数据 — 同上 |

### 4.22 运维（不需要 cookie）
| Method | Path | 说明 |
|---|---|---|
| GET | `/healthz` | k8s liveness，plain "ok" |
| GET | `/readyz` | k8s readiness，plain "ok" |
| GET | `/metrics` | Prometheus 抓取 |
| GET | `/ws` | WebSocket upgrade（cookie 鉴权）|

---

## 5. WebSocket 协议（26 WSMessageType / C005 锁定）

> **v0.7.3 gap 补丁新增 4 个 server→client type**（已挂入锁定列表，下次升级 C005 harness 同步）：
> `channel_closed` / `channel_member_updated` / `schedule_created` / `schedule_canceled`

### 5.1 连接

```
ws://<gateway-host>:<port>/ws?device=web-<uuid>
Header: CookieId: <24-hex>
```

Frame 格式（JSON over WS text frame）：

```json
{ "type": "<type-string>", "payload": { ...payload object... } }
```

> ⚠️ **Wire 格式注意**：`payload` 字段是 raw JSON object（`json.RawMessage`），**不是 base64 string**。后端 `gateway.WSFrame` struct 字段是 `[]byte`，但生产编解码用匿名 struct + `json.RawMessage` 直接读 / 写。客户端如果用 Go SDK 拿 `gateway.WSFrame` JSON marshal 会得到 base64，**这是 trap**（参考 `tests/integration/m4_ws_fixture_test.go::wireFrame`）。

### 5.2 4 种 client → server

| type | 用途 | payload struct |
|---|---|---|
| `ping` | 客户端心跳，每 15s 一次 | `PingPayload{ channel_seqs: {<channel_id_str>: <seq>} }` |
| `send` | 发消息（除 HTTP 外的 WS 路径） | `SendPayload{ client_msg_id, channel_id, content, msg_type, visible_to[] }` |
| `push_ack` | 客户端 ACK 服务端推的 push_msg（按 push_id） | `PushACKPayload{ push_id }` |
| `sync` | （**保留**，**当前未由 ws_handler 处理** — sync 走 HTTP `POST /api/sync`）| `SyncPayload{ channels: [{id, seq}] }` |

### 5.3 22 种 server → client（v0.7.3 + 4 gap-补丁 type）

| type | 触发 | payload struct |
|---|---|---|
| `pong` | 服务端心跳，每 15s tick；携带 channel seq diff | `PongPayload{ server_time, channel_seqs }` |
| `push_msg` | 别人在你的 channel 发消息 | `PushMsgPayload{ push_id, channel_id, seq, server_msg_id, sender_id, content, msg_type, visible_to, created_at }` |
| `send_ack` | 你 WS send 后服务端确认 | `SendACKPayload{ client_msg_id, server_msg_id, seq, channel_id }` |
| `sync_resp` | （**保留**，未实际发送）| — |
| `read_sync` | 同用户在另一设备 mark read | `ReadSyncPayload{ channel_id, read_seq }` |
| `friend_event` | 好友请求 / 接受 / 拒绝 | `FriendEventPayload{ event_type: "request"|"accepted"|"rejected", from_user_id }` |
| `channel_event` | 你被加入新 channel | `ChannelEventPayload{ event_type: "added", channel_id, name }` |
| `msg_updated` | 消息被编辑（M1 / 模板已收到也走这个）| 整条 `repo.Message` JSON snapshot |
| `msg_deleted` | 消息被撤回 | `{ msg_id, channel_id, deleted_at }` |
| `announcement_posted` | 频道公告新发 | 整条 announcement JSON |
| `urgent_posted` | 加急消息发到你所在 channel | 整条 message JSON（含 is_urgent=true） |
| `approval_updated` | 审批 create / approve / reject / cancel；推 requester + approver | 整条 approval JSON |
| `notification_received` | 你收到新通知 | 整条 notification JSON |
| `reaction_added` | 你 channel 里有人加 reaction | `{ channel_id, message_id, user_id, emoji }` |
| `reaction_removed` | 同上，减 reaction | 同上 |
| `channel_top_updated` | 你（同用户多设备）置顶 / 取消置顶频道 | `{ channel_id, is_top: bool }` |
| `channel_info_updated` | owner / manager 改 channel notice / purpose / orient / permission / name | 整条 channel JSON snapshot |
| `channel_closed` | **owner 解散群聊（v0.7.3 gap #1+#3）** | `{ channel_id, actor_id, deleted_at }` |
| `channel_member_updated` | **加/移成员/退群/群昵称变更（v0.7.3 gap #4+#5）** | `{ channel_id, change_type: "join"\|"leave"\|"kick"\|"nickname", actor_id, target_id, nick_name?, members:[{user_id, role, nick_name?, is_top?, notify_pref?}] }` |
| `schedule_created` | **本人在另一台设备创建定时消息（v0.7.3 gap #7）** | `{ channel_id, scheduled_id, has_schedule_post:true }` |
| `schedule_canceled` | **本人在另一台设备取消定时消息（v0.7.3 gap #7）** | `{ channel_id, scheduled_id, has_schedule_post }`（最后一条取消时 has_schedule_post=false）|

### 5.3.1 NOTICE 字段（v0.7.3 gap #9）

`push_msg` payload 顶层 `type` 字段：
- 普通消息（msg_type=1/2/3/99）→ 字段省略（`omitempty`）
- 系统消息（msg_type=4，AddMember / RemoveMember / LeaveChannel / CloseChannel / SetMemberNickname 触发）→ `type:"NOTICE"`
- 系统消息 payload 同时携带 `props` JSONB 字符串：`{"sys_type":"member_joined"|"member_removed"|"member_left"|"channel_created"|"channel_updated"|"channel_closed"|"member_nickname", "actor_id":"...", "target_id":"...", "nick_name":"...", "name":"..."}`

cses-client Rust `message_service.rs::apply_notice_changes_standalone` 现有 `data.get("type") == "NOTICE"` 分支可直接识别。

### 5.4 客户端 ws-normalizer 翻译表（cses-client 端实现）

cses-client 在 `src/app/core/im-api/ws-normalizer.ts` 把 22 type 归一化成 16 个 `imWs:*` 事件供业务订阅：

| im 后端 type | cses-client `imWs:*` 事件 | 说明 |
|---|---|---|
| `push_msg` | `imWs:post:received` | 经 `normalizeExpediteFields` |
| `msg_updated` | `imWs:post:updated` | 同上 |
| `msg_deleted` | `imWs:post:deleted` | 新增 |
| `read_sync` | `imWs:post:read` | 已读同步 |
| `channel_event` | `imWs:channel:created` | 被拉群 |
| `channel_info_updated` | `imWs:channel:update` | notice / purpose 等 |
| `channel_top_updated` | `imWs:channel:topUpdated` | per-user 置顶 |
| `friend_event` | `imWs:friend:event` | 三种 sub-event 在 payload.event_type |
| `announcement_posted` / `urgent_posted` / `approval_updated` / `notification_received` | 透传（同名）| 业务直接 typeIs 匹配 |
| `reaction_added` / `reaction_removed` | 透传 | 同上 |
| `pong` / `send_ack` / `sync_resp` | wrapper（业务一般不消费） | 控制帧 |
| **未知 type** | 原样转 envelope | 业务自己处理 / drop |

cses-client cutover Phase 2-4 期间会逐步删 `imWs:post:read`（迁移到 channel-level last_read_seq + 异步 read-stats）。详见 `docs/CSES_CLIENT_CUTOVER.md` Phase 4。

### 5.5 心跳与 routing 续期

- 客户端 ping 间隔：**15 秒**
- 服务端 read deadline：**45 秒**（3 个 ping 周期，允许丢 2 次心跳）
- Redis routing key TTL：**45 秒**
- 服务端 ping handler **不立即回 pong**；pong 由 server 自己 15s tick 推

```
client ping → server 更新 lastPong + 续 routing TTL，但不发 pong
                           ↓
                     15s 后 server 主动 tick 推 pong + channel_seqs diff
```

> 客户端不应阻塞等待 ping 后立即 pong。

---

## 6. apiFlavor 切换信号（cses Java 下发）

cses Java 登录响应 / 配置里下发：

```json
{
    "imGatewayHttp": "http://192.168.6.41:30880",
    "imGatewayWebSocket": "ws://192.168.6.41:30880",
    "imGatewayEnabled": true
}
```

cses-client 启动时：
- `imGatewayEnabled: true` + `imGatewayHttp` 非空 → `apiFlavor = "im"`，`imHttp` 客户端 baseUrl 切到 `imGatewayHttp`
- 否则 → `apiFlavor = "mattermost"`，`imHttp` 走 cses Java 旧路径 `/api/cses/*`

Console 验证 log（cses-client 端）：

```
🔀 [MessageHttpService] apiFlavor: mattermost → im
🔄 [LOGIN] reconnectMainWs ... routedToIm: true
```

---

## 7. cses Java vs im 路径对照（v0.7.x cutover）

> **决策**：直接 rewrite，**不留翻译层**（cutover §1 决策 1）。客户端 `route-table.ts` 在过渡期里有翻译表，**最终目标是删掉**。

| cses Java 老路径 | im 新路径 | 备注 |
|---|---|---|
| `POST /channel/create` | `POST /api/channels` (DM 用 `/api/channels/dm`) | |
| `POST /channel/{id}/change/{displayName,notice,purpose,orient,permission,...}` | `PUT/PATCH /api/channels/:id` | 局部字段 |
| `POST /channel/{id}/change/top` | `PATCH /api/channels/:id/members/:user_id { is_top }` | per-user |
| `POST /channel/{id}/{add,remove}/manger` | `POST/DELETE /api/channels/:id/managers/:user_id` | typo "manger" → 修回 "manager" |
| `POST /channel/{id}/{add,remove,load}/postPinned` | `POST/DELETE/GET /api/channels/:id/pins/:message_id` | |
| `POST /channel/{id}/onlineStatus` | `GET /api/channels/online-status?channel_ids=...` | M3 batch |
| `POST /channel/{id}/member/leave` | `POST /api/channels/:id/leave` | |
| `POST /channel/{id}/member/snapshot` | `GET /api/channels/:id/members` 全量 | snapshot 协议废弃（cutover §决策 4） |
| `POST /channel/close` | **`DELETE /api/channels/:id`** | **v0.7.3 gap #1**；owner-only；channels.deleted_at 软删；触发 `channel_closed` WS |
| `POST /channels/member/byIds` | `GET /api/channels/:id/members`（全量降级） | **v0.7.3 gap #6**；cses-client `verifyMemberCountAndSync` 走全量替代 batch by IDs |
| `POST /channel/{id}/member/changeName`（昵称设置）| **`PATCH /api/channels/:id/members/:user_id/nickname`** | **v0.7.3 gap #5**；body `{nick_name}` ≤64；触发 `channel_member_updated{change_type:"nickname"}` |
| `POST /posts/createPosts` | `POST /api/channels/:id/messages` | |
| `POST /posts/revoke` | `DELETE /api/messages/:id` | |
| `POST /posts/getReplies` | `GET /api/messages/:id/replies` | 一次性返回全部 |
| `POST /posts/getReplyBranch` | **`GET /api/messages/:id/replies/branch?offset=N&limit=M`** | **v0.7.3 gap #2**；二级 thread 子回复分页；返回 `{messages, has_more, offset, limit}` |
| `POST /posts/{urgentPost,urgentConfirm,urgentCancel}` | `POST /api/messages/urgent` / `/messages/:id/urgent/{confirm,cancel}` | |
| `POST /posts/{createSchedule,cancelSchedule,getSchedule}` | `POST/DELETE/GET /api/messages/scheduled` | |
| `POST /posts/quickReply` | `POST /api/channels/:id/messages` + `quick_reply_id` 字段 | im 已支持 0 改动 |
| `POST /posts/makeTopic` | `POST /api/channels/:id/topics` | |
| `POST /post/{id}/{read,read/list}` | `POST /api/channels/:id/read` + `GET /api/messages/:id/readers` | **post-level read 砍**（cutover 决策 4） |
| `POST /post/announcement/{save,read,delete,list,detail,acceptList}` | `POST /api/announcements` 等 6 路由 | |
| `POST /post/templateReceived` | `POST /api/messages/:id/received` | path 化（去 channelId） |
| `POST /post_bookmark/{create,delete,load}` | `POST/DELETE/GET /api/favorites/:message_id` | |
| `POST /modules/getAll` | `GET /api/modules` | v0.7.2 新增 |
| **bitmap 协议族**（`/channel/bitmap` / `/channels/load/increment*` / `/posts/get{PostsAfter,Updated,Latest}Posts` / `/posts/getStartAndEndTime` / `/posts/remove(SegmentRemove)` / `/post_segment*` / `/posts_segment_config` / `/posts_segment_daily_metadata/*`）| **整族砍** | seq 单调 cursor 替代，`POST /api/sync` 接管。Tauri Rust ImSeqDataSource 单点调用 |

完整 125 → 84 对账见 `wiki/comparisons/csesapi-vs-im-coverage.md`。

---

## 8. 已知坑 / 边界（cses-client 必读）

| 编号 | 坑 | 处理 |
|---|---|---|
| **C006** | httpexpect / fetch 路径里**禁止**拼 `?q=v` | client 用 `URLSearchParams` / `imHttp.params` API；server 测试用 `.WithQuery(k, v)` |
| **C007** | 所有 `/api/*` 响应被 envelope wrap | client interceptor 解 `data` / `error`，**不要**做 cses Java 老栈的 `isWrappedResponse` 双重判断 |
| **C005** | WS 22 type 锁定 | 客户端 `ws-normalizer.ts` 必须有翻译；新 type 走 V2 RFC + 前后端同步 |
| **WSFrame wire** | 服务端 `payload` 是 raw JSON object，不是 base64 | 客户端编解码用匿名 struct + `json.RawMessage`，不要直接用 `gateway.WSFrame` JSON marshal |
| **post-level read 已废** | `/post/read` / `/posts/getPostsAfter*` 服务端**不再有** | 只用 channel-level `POST /api/channels/:id/read` + 异步 `GET /api/messages/read-stats?ids=...` |
| **`POST /channels/:id/read` body 为空** | cses 老 `/channels/view` body 含 channelId | 直接 POST 不带 body，server 用 path id + 当前 channel.Seq |
| **`POST /messages/batch` 静默跳非成员** | 期望 403 的话会失败 | 当前合约：返 201 + 空 messages 数组（看后续是否收紧）|
| **`/messages/around` 用 `timestamp`** | 不是 `message_id` + `radius` | query `?timestamp=<unix_ms>` |
| **`channel_top_updated` is per-user** | 多设备同步专用 | 别期望它推给 channel 其他成员 |
| **settings PUT 别 flip notification_enabled false** | GORM Upsert default:true bug | 等后端修，避免单独 false 写入 |
| **favorite 跨用户无 channel-member check** | 当前合约：用户 A 收藏别人 DM 消息也 201 | 等后端是否收紧 |
| **v0.7.3 NOTICE 字段** | `push_msg.type == "NOTICE"` 表示 msg_type=4 系统消息 | cses-client Rust `apply_notice_changes_standalone` 现有 `data.type == "NOTICE"` 分支直接命中（gap #9 字段对齐，无需重写）|
| **v0.7.3 channel_member_updated 完整 channel snapshot** | 加 / 移 / 离 / 昵称统一走这一个 WS type | cses-client 用 `change_type` 字段分流（join\|kick\|leave\|nickname），payload.members 是 post-change 全量 roster，**不需再调** GET /channels/:id/members 拉成员 |
| **v0.7.3 schedule_created/canceled 仅推 sender 多设备** | 不广播 channel 全员 | cses-client 用 `has_schedule_post` 翻 dialog.hasSchedulePost；channel 其他成员看不到 sender 的草稿状态（by design）|
| **v0.7.3 DELETE /channels/:id idempotent** | 已 closed 的 channel 重复 DELETE 返 200 + 当前 snapshot，不再二次广播 channel_closed | client 端可放心重试|

---

## 9. 联调 cheatsheet

### 9.1 启动 cses-client（无需 cargo / tauri build）

```bash
cd /Users/mac28/workspace/angular/cses-client      # ⭐ 锁定目录（不是 temp/）
git status                                         # 检查未提交改动
git pull origin im-backend-switch                  # 拉最新
yarn start                                         # = tauri:dev，自动起 Angular + Tauri
```

### 9.2 联调 smoke checklist

| Step | 期望 |
|---|---|
| ① 用 `17692704771/123456` 登录 | 登录成功 |
| ② DevTools console 看 log | `🔀 apiFlavor: mattermost → im` + `🔄 reconnectMainWs routedToIm: true` |
| ③ Network 看 imHttp 调用 | 命中 `http://192.168.6.41:30880/api/...` |
| ④ Network 看 this.http 调用 | vote / Im/search / average 仍走 cses Java |
| ⑤ Network 看响应 shape | `{status: "success", data: ...}` 而非裸 body |
| ⑥ WS 帧（任意聊天页面）| frame `{type, payload}`，type 从 5.3 表里 |
| ⑦ 拉人入群（owner / admin 操作）| 收 `channel_member_updated{change_type:"join", members:[...]}`（**v0.7.3 gap #4**）|
| ⑧ owner 解散群聊 | DELETE /api/channels/:id → 全员收 `channel_closed` + `channel.deleted_at` 设置（**v0.7.3 gap #1+#3**）|
| ⑨ 改群昵称 | PATCH …/nickname → 全员收 `channel_member_updated{change_type:"nickname", nick_name:"…"}`（**v0.7.3 gap #5**）|
| ⑩ 创建/取消定时消息 | 多设备登录同账号 → 仅本人其他设备收 `schedule_created` / `schedule_canceled`（**v0.7.3 gap #7**）|
| ⑪ 系统消息 NOTICE 解析 | 加/移成员、解散群等 push_msg payload 含 `type:"NOTICE"` + `props` JSONB（**v0.7.3 gap #9**）|

### 9.3 后端 smoke 命令（im 项目这边）

```bash
# pre 集群灌张立超 cookie
IM_REDIS=<pre-redis> server/scripts/seed-mm-cookies.sh

# 验 cookie 走通
curl -H 'CookieId: 69eec6dbe6876865ff98945a' http://192.168.6.41:30880/api/me
# 应返回完整 mm user JSON envelope-wrapped

# 跑全集成测试（本地 docker）
cd server && go test -tags integration -timeout 45m ./tests/integration/...
# ok 611s / 198 case PASS / 0 FAIL
```

### 9.4 联调出问题第一时间看

| 症状 | 第一看 |
|---|---|
| `apiFlavor: mattermost → mattermost` 没切 | cses 没下发 `imGatewayHttp`，看 XPA-12.yaml `imGatewayEnabled: true` |
| WS upgrade 401 | cookieId 没在 cses Redis，`redis-cli -h <host> HGET User '"<cookieId>"'` |
| `ImEndpointNotMappedError` 红色 | cses-client `route-table.ts` 漏网 imHttp 调用，补翻译条目 |
| 响应 shape 与预期不符 | 检查客户端 interceptor 是否处理 envelope；删 cses Java 老栈的 `isWrappedResponse` 双重判断 |
| 跨 pod 用户收不到 push_msg | 查 Redis routing key TTL（45s），看 `routing.MarkOffline` 计数 |
| client send 后没收到 send_ack | 查 wire 格式：payload 是不是 base64 字符串（应是 JSON object，参考 §5.1 trap） |

---

## 10. 后续路线（**cses-client 仓库**的 cutover，im 后端已收尾）

> ⚠️ **重要澄清**：本节描述的 Phase 2/3/4 全部是 **cses-client 仓库** (`/Users/mac28/workspace/angular/cses-client`) 的工作，**不是 im 后端**。im 后端 84 路由 / 22 WSMessageType / 198 集成测试全部完成（tag `v0.7.3-backend-final`），endpoint 端已 **production-ready**，**不需要再做 Phase 2/3/4**。
>
> 详见 `docs/CSES_CLIENT_CUTOVER.md` 的 cutover 计划。本文档只关心**契约**。

### 10.1 im 后端仓库 tag 链（已完结，本仓 push origin）

```
v0.7.3-im-backend-base       Phase 1 三件套（envelope 中间件 + POST received + GET read-stats）
v0.7.3-harness-base          C001-C008 框架
v0.7.3-batch-b-tests         130 集成测试（5 case 矩阵）
v0.7.3-batch-b-envelope      envelope 契约对齐
v0.7.3-batch-c-tests         27 happy（announcement/approval/notification/quick_reply/reaction/scheduled）
v0.7.3-batch-d-tests         20 WS 测试（含 channel_info+top push hook）
v0.7.3-batch-e-tests         6 happy（module/sync/presence/settings）
v0.7.3-backend-final  ⭐     im 后端 production-ready 总封 tag
```

### 10.2 cses-client 仓库 tag 链（前端 cutover 进行中）

> ⚠️ 这些 tag 在 **cses-client 仓库** branch `im-backend-switch`，不在本 im 后端仓库。

| Phase | 范围 | cses-client 仓库 tag | 当前状态 |
|---|---|---|---|
| Phase 2a ✅ | apiFlavor=im 登录后从 im-server 拉频道列表覆盖 hash 缓存 | `v0.7.3-phase2a-channel-cache-from-im` | 已打 (commit `7203e5bc9`) |
| Phase 2b ✅ | 模板已收到 path 化 + optimistic + WS 对账 | `v0.7.3-phase2b-template-received` | 已打 (commit `10cd3efcc`) |
| Phase 3 ⚠️ | onChannelRead 6 处切 + 砍 post 级 dead code | `v0.7.3-phase3-channel-read` | 已打 (commit `e70bcd3c0`) — 但 grep 仍 21 处 onPostRead/inViewMsgRead/handle_post_read 残留 + Rust `handle_post_read` 100 行未删 |
| Phase 4 ❌ | message-status 异步 + 加急 2 处 + mention 改 read_sync + 33 处 readBits 清理 + 26 处 imHttp 老 path rewrite | `v0.7.3-phase4-read-stats-ui` | **未启动**（`read-stats` / `getReadStats` 集成 0 处 / readBits[ 索引访问还有 3 处） |
| 联调 + smoke + k6 ❌ | 三件套验证后打 | `v0.7.3-client-verified` | 未启动 |

**Phase 4 未做的具体清单**（按 §12.16 10 步走）：

1. message-status.component.ts 异步 read-stats 重构（`computed` → `signal + effect` 异步加载）
2. 加急弹窗 2 处（`message.component.ts:380-413` + `messageContainerShared.service.ts:504-527`）异步化
3. messageWindowGlabal mention 清理改订阅 `read_sync` WS 事件
4. 33 处 `readBits` 引用清理（type.d.ts / event.type.ts 字段声明 + 业务逻辑访问）
5. 26 处 imHttp 老 cses-shape 路径 rewrite（`/posts/*` 12 处 + `/channel/*` 14 处）
6. Rust `src-tauri/src/websocket/im_handlers.rs::handle_post_read` 删 ~100 行
7. `imWs:post:read` IPC 事件订阅删 ~10 行（`ipcEventHandler.service.ts:126,278`）
8. 9 个 cses-client 本地 commit push origin
9. 联调全跑通（smoke 7/7 / 三件套全绿 / 张立超 cookie 端到端）
10. k6 send P95 ≤ 400ms 性能基线
11. tag `v0.7.3-client-verified`

---

## 11. 维护契约

- 后端契约变更（路由 / WS type / payload shape / envelope）必须先在本文档登记 + 一个 harness（参考 C001-C009 模板）
- cses-client 团队改 `route-table.ts` / `ws-normalizer.ts` 时，**先**在 PR 描述里 link 本文档对应小节
- 本文档由 im 项目 owner 维护；cses-client 端发现契约不一致 / 缺漏 → 提 issue 不要直接改

---

## 12. 客户端 entry 细粒度迁移对照（v0.7.3 与之前 stale shape 的差异）

> **背景**：cses-client 仓库 `src/app/core/im-api/message.types.ts` 写于 M3 时期，部分 entity 字段已经因 v0.6 (M4 用户身份模型重构) + v0.7 (envelope + WS 22 type) 演进而 stale。本节按 entity 列出**实测**差异 + ts 类型 before/after，cses-client 端按本节直接改 `message.types.ts` / `sync.types.ts` / 对应 service。

### 12.1 ImMessage（重点修：user-id 全转 string）

> 文件：`src/app/core/im-api/message.types.ts:25-52`

| 字段 | 客户端**当前** ts 类型（M3 stale） | im 后端 v0.7.3 实际 | 改造 |
|---|---|---|---|
| `id` | `number` | `int64` → JS `number` | ✅ 不改（int64 ≤ 2^53 安全） |
| `channel_id` | `number` | `int64` | ✅ 不改 |
| `seq` | `number` | `int64` | ✅ 不改 |
| **`sender_id`** | `number` | **`string` (mm UserID 24-hex)** | ⚠️ **必改 → `string`** |
| `msg_type` | `'text'\|'image'\|'file'\|'system'\|'phantom'\|string` | **`number` (1=text/2=image/3=file/4=system/99=phantom)** | ⚠️ **必改 → `number`**（后端是 int16 数字，不是字符串枚举） |
| `content` | `string` | `string` | ✅ |
| `reply_to` | `number?` | `*int64` | ✅ |
| `file_ids` | `number[]?` | **不在 messages 字段里** | ⚠️ 删除字段 → 走 `GET /api/messages/:id/attachments` 单独拉 |
| **`visible_to`** | `number[]?` | **`string[]?` (mm UserIDs)** | ⚠️ **必改 → `string[]`** |
| **`created_at`** | `number` (unix ms) | **`string` (RFC3339)** | ⚠️ **必改 → `string`**，client `new Date(created_at)` 解析 |
| `updated_at` | `number?\|null` | `*time.Time` (RFC3339) | ⚠️ **必改 → `string?`** |
| `deleted` | `boolean?` | `bool, omitempty` | ✅ |
| **`server_msg_id`** | `string?` | **不在 message 字段里**（仅 send_ack payload 有）| ⚠️ 删除 |
| **新增 `team_id`** | — | `string?` (mm CompanyID frozen) | ⚠️ **加** |
| **新增 `is_urgent`** | — | `bool, omitempty` | ⚠️ **加** |
| **新增 `props`** | — | `string?` (JSONB raw text，需二次 `JSON.parse`) | ⚠️ **加** |
| **新增 `forwarded_from`** | — | `*int64` | ⚠️ **加** |
| **新增 `deleted_at`** | — | `string?` | ⚠️ **加** |

**修后正确版**：

```typescript
export interface ImMessage {
    id: number;                       // int64
    channel_id: number;
    seq: number;
    client_msg_id?: string;            // M1 idempotency, ≤36 chars
    sender_id: string;                 // mm UserID 24-hex  ⚠️ 不是 number
    team_id?: string;                  // frozen at write
    msg_type: number;                  // 1/2/3/4/99（不是字符串）
    content: string;
    visible_to?: string[];             // mm UserIDs
    reply_to?: number;
    forwarded_from?: number;
    created_at: string;                // RFC3339
    updated_at?: string;
    deleted?: boolean;
    deleted_at?: string;
    is_urgent?: boolean;
    props?: string;                    // JSONB raw — 需 JSON.parse
}
```

### 12.2 ImChannel（重点修：creator_id + 大量字段补全）

> 文件：`src/app/core/im-api/message.types.ts:55-68`

```typescript
// ❌ stale：creator_id 是 number、字段缺一大半
export interface ImChannel {
    id: number;
    type: number;
    name: string;
    avatar_url?: string;
    seq: number;
    creator_id?: number | null;
    created_at: string | number;
    updated_at?: string | number | null;
}

// ✅ v0.7.3 正确版（按 docs/IM_DATA_MODEL_新版数据模型字典.md §2.1 1:1 对齐）
export interface ImChannel {
    id: number;
    type: number;                  // 1=DM / 2=Group
    name: string;
    avatar_url: string;             // not null default ""
    seq: number;
    creator_id: string;             // mm UserID 24-hex（M4 起 not null）
    team_id?: string;               // mm CompanyID frozen
    notice: string;
    purpose: string;
    picture_url: string;
    props: string;                  // JSONB raw — 需 JSON.parse
    orient: number;                 // int16
    permission: number;             // 0=open/1=approval/2=closed
    is_top: boolean;                // channel-level pin
    root_id?: number;               // M3 子群聊：父 channel
    root_message_id?: number;       // M3 子群聊：分叉点
    created_at: string;             // RFC3339（不再是 number 兼容）
    updated_at: string;
}
```

### 12.3 ImLoginResponse / ImLoginRequest（**整段删除**）

> 文件：`src/app/core/im-api/message.types.ts:82-100`

cses-client M4 起鉴权完全走 cookieId（cses Java 登录 → 写 Redis hash → im 读）。`POST /api/login` / `POST /api/register` 在 v0.7.x 后端**返 410 Gone**。

```typescript
// ❌ 全删
export interface ImLoginResponse { ... }
export interface ImLoginRequest { ... }

// ✅ 替代：GET /api/me 拿当前用户（透传 MMUser from Redis）
export interface ImMe {
    userId: string;        // mm UserID 24-hex
    companyId: string;
    orgId: string;
    name: string;
    avatarUrl?: string;
    // 其他 mm User 字段...（透传 cses 写入的 hash）
}
```

### 12.4 ImChannelWithPreview（响应 shape 不一样）

> v0.7.3 `GET /api/channels` 实际返回 `Channel[]`（`Channel` entity 直接列表），**不是** `ImChannelWithPreview[]`。预览 / 未读 走 `GET /api/messages/read-stats`（异步） + `GET /api/channels/:id/messages?limit=1`（最近一条）拼装。

```typescript
// ❌ 客户端旧假设：单接口拿全套预览
GET /api/channels → ImChannelWithPreview[]

// ✅ v0.7.3 实际：分两步拼装
GET /api/channels → ImChannel[]
// 然后 client side 自己 batch:
GET /api/messages/read-stats?ids=<最近消息 ids> → ReadStat[]
// + 每个频道的 last_msg 走 sync 增量或 list 拉最新一条
```

### 12.5 ImReader（字段名小改）

> 文件：`src/app/core/im-api/message.types.ts:155-161`

| 客户端 stale | v0.7.3 后端 | 改造 |
|---|---|---|
| `user_id: number` | `user_id: string` | ⚠️ 必改 |
| `read_at: number` | `read_at: string` (RFC3339) | ⚠️ 必改 |

### 12.6 GetReadersResponse 分页

`next_cursor` 当前 v0.7.3 后端实际无分页支持（`GET /api/messages/:id/readers` 一次返全列表）。客户端**先按非分页处理**，未来后端加 cursor 时再补。

### 12.7 SendMessageRequest（msg_type 类型差异）

> 文件：`src/app/core/im-api/message.types.ts:103-109`

```typescript
// ❌ stale
export interface SendMessageRequest {
    msg_type?: ImMsgType;          // 'text' | 'image' | ...
    content: string;
    reply_to?: number;
    file_ids?: number[];
    visible_to?: number[];          // ← number[]
}

// ✅ v0.7.3
export interface SendMessageRequest {
    content: string;
    msg_type?: number;              // 1/2/3/4，默认 1
    visible_to?: string[];          // mm UserIDs
    reply_to?: number;
    client_msg_id?: string;          // idempotency UUID
    file_ids?: number[];             // 附件，独立加进 messages 后用 message_attachments join
    quick_reply_id?: number;         // v0.7.0 替代 cses /posts/quickReply
}
```

### 12.8 FetchMessagesParams（query 参数名 → before/limit）

> 文件：`src/app/core/im-api/message.types.ts:112-117`

```typescript
// ❌ stale 假设了三种 cursor
{ after_seq, before_seq, around_seq, limit }

// ✅ v0.7.3 实际：按用途拆三个 endpoint
GET /api/channels/:id/messages?before=<seq>&limit=N    // 翻页
GET /api/channels/:id/messages/around?timestamp=<ms>&radius=N  // 跳转上下文（用 timestamp 不是 seq！）
GET /api/messages/:id/after?limit=N                            // 从某 msg seq 之后增量
```

### 12.9 FetchMessagesResponse / has_older / has_newer 字段

> 文件：`src/app/core/im-api/message.types.ts:119-126`

后端实际响应 shape 是 `{messages, has_more, next_before?}`（**单方向 has_more**，不是双向 has_older/has_newer）。

### 12.10 sync 类型（`src/app/core/im-api/sync.types.ts`）

```typescript
// ✅ v0.7.3 sync wire 格式
export interface SyncRequest {
    channels: { id: number; seq: number }[];
}

export interface SyncResponse {
    channels: SyncChannelEntry[];
}

export interface SyncChannelEntry {
    id: number;
    server_seq: number;             // 当前 server 此 channel 最大 seq
    messages: ImMessage[];           // 增量补回（seq > client.seq）
}
```

### 12.11 WS 帧（`src/app/core/im-api/ws-normalizer.ts`）

cses-client 已经有 22 type 翻译表（`ws-normalizer.ts:156-164` 透传 channel_top_updated / channel_info_updated）。**关键 trap**：

```typescript
// ❌ 错误：用 gateway.WSFrame{}（Go 类型，[]byte 字段会 base64）
const frame: WSFrame = JSON.parse(rawText);  // payload 是 base64 string ❌

// ✅ 正确：用匿名 wire 类型 payload 是 raw JSON object
interface WireFrame<P = unknown> {
    type: string;
    payload?: P;   // 不是 string 不是 base64，是 JSON object
}
const frame = JSON.parse(rawText) as WireFrame<PushMsgPayload>;
// frame.payload 直接是 {push_id, channel_id, seq, ...} 对象
```

### 12.12 envelope 解包（`src/app/core/im-api/im-api.adapter.ts`）

```typescript
// ❌ 错误：把 cses Java 老栈 isWrappedResponse 双重判断保留
if (response.isWrappedResponse) {
    return response.data;
} else if (response.data?.data) {
    return response.data.data;   // 兼容老栈 ← 这层删掉
}

// ✅ v0.7.3 正确：单层 envelope，统一解
async function unwrap<T>(resp: Response): Promise<T> {
    const env = await resp.json() as { status: 'success'|'error'; data?: T; error?: string };
    if (env.status === 'error') {
        throw new ImApiError(resp.status, env.error || 'unknown');
    }
    return env.data as T;
}
```

### 12.13 onChannelRead / onPostRead（**post-level read 已废**）

```typescript
// ❌ stale 假设：post-level 已读
POST /post/{id}/read              // ← 后端不存在，返 404

// ✅ v0.7.3：只有 channel-level
POST /api/channels/:id/read        // body 为空，server 用当前 channel.Seq
// 想看具体哪条已读 / 谁未读：
GET /api/messages/read-stats?ids=1,2,3  // 异步批量查
```

cses-client cutover Phase 3 / 4 重构 33 处 `readBits` → 异步 `read-stats` UI 就是为了这个。

### 12.14 模板"已收到"（path 化）

```typescript
// ❌ cses 老 path
POST /post/templateReceived  body:{postId, channelId}

// ✅ v0.7.3
POST /api/messages/:id/received  body:{}
// channelId 由 server 用 message.channel_id 推出，不需传
```

### 12.15 在线状态（单接口 → 批量）

```typescript
// ❌ cses 老 path（单频道）
POST /channel/:id/onlineStatus

// ✅ v0.7.3 batch
GET /api/channels/online-status?channel_ids=1,2,3&include_users=true
// 用 .WithQuery 不要拼 ?，C006 规则
```

### 12.16 Phase 2-4 客户端代码改造路径（按依赖序）

| 步骤 | 文件 | 改动 |
|---|---|---|
| **1** | `src/app/core/im-api/message.types.ts` | 按 §12.1-12.10 改造所有字段类型（user-id number→string / created_at number→RFC3339 / msg_type 字符串→数字 / 加 props/team_id/is_urgent 等）|
| **2** | `src/app/core/im-api/im-api.adapter.ts` | envelope unwrap 单层化（§12.12）；删 isWrappedResponse 双重判断 |
| **3** | `src/app/core/im-api/route-table.ts` | 砍 cses 老 path 翻译（不留兼容层），未翻译端点 throw `ImEndpointNotMappedError`（已有）|
| **4** | `src/app/core/im-api/ws-normalizer.ts` | 验证 22 type 全在翻译表里（已基本 OK，删 `imWs:post:read` 翻译）|
| **5** | `src/pages/message-v3/service/message.service.ts` | 6 处 onChannelRead 切 path（§12.13）|
| **6** | `chat-content-base.component.ts` | 砍 onPostRead / inViewMsgRead dead code |
| **7** | `src-tauri/src/websocket/im_handlers.rs` | 删 `handle_post_read` (~100 行) |
| **8** | `message-status.component.ts` + 加急弹窗 2 处 + mention 清理 | 异步 read-stats UI 重构（§12.13 后半 + cutover Phase 4）|
| **9** | 33 处 `readBits` 引用 | 删除 / 替换 |
| **10** | 联调 + smoke + k6 → tag `v0.7.3-client-verified` | |

详见 `docs/CSES_CLIENT_CUTOVER.md` Phase 2-4 章节。

---

**Owner**：im 项目 + cses-client 项目联合
**最后更新**：2026-05-08（v0.7.3-backend-final + §12 细粒度迁移对照）
**下次更新触发**：v0.7.4 / v0.8.x 任何路由 / WS type / envelope / entry 字段变更

---
type: comparison
title: csesapi vs im 接口覆盖对账（125 → 80）
status: stable
last_verified: 2026-04-30
sources:
  - /Users/mac28/workspace/golangProject/mattermost/server/channels/csesapi/*.go
  - /Users/mac28/workspace/golangProject/im/server/internal/http/*.go
  - docs/HTTP_WS_MAP.md
  - docs/GOAL.md §3 不在范围
related:
  - syntheses/min-cost-mattermost-cutover
  - concepts/seq-cursor
  - concepts/api-flavor-switch
  - decisions/no-traffic-rollback
  - decisions/strangler-fig-collapsed
  - milestones/M6-mattermost-decom
confidence: high
---

# csesapi 全集 vs im /api 覆盖对账

> 一张永久对账表：Mattermost csesapi 实际暴露的 ~125 个 HTTP 路由 → 新版 im 应该如何裁切。
> **不是"翻译表"，是"裁切表"**：每条路由要么 im 已对应实现 / 要么由 Rust 同步层接管 / 要么留 cses Java / 要么直接砍掉。

---

## 1. 全集统计（按子模块）

| csesapi 子模块 | 路由数 | 性质 | 处理决策 |
|---|---|---|---|
| `channel.go` | 33 | 写入 + 读 + **bitmap 同步**（`/bitmap`、`/load/increment(ByChannelId)`） | 30 由 im REST 替代；3 条 bitmap 砍 |
| `posts.go` | 36 | 写入 + 读 + **bitmap 同步**（`/getPostsAfterIndex/FromSegment/UpdatedPosts/LatestPost/StartAndEndTime`、`/remove`(SegmentRemove)） | 28 由 im REST 替代；6 条 bitmap 砍；公告 6 条 → im `announcement.go` |
| `post_segment{,_config,_daily_metadata}` | 6 | **纯 bitmap segment 协议** | **整族砍**（[[concepts/seq-cursor]] 替代）|
| `post_bookmark.go` | 3 | 收藏 | im `favorite.go` 已覆盖 |
| `post_approval.go` | 1 | 审批 | im `approval.go` 已覆盖 |
| `search.go` | 4 | 检索 | 留 cses Java（`/Im/search/*` 走 `this.http`） |
| `user.go` | 3 | 用户 | 走 [[concepts/cookie-id-native]]：从 `Redis HASH "User"` 解析；im 不实现 |
| `notification.go` | 2 | 通知 | im `notification.go` 已覆盖 |
| `cses_group.go` | 1 | 群模块 | GOAL §3 排除（留 cses Java）|
| `bot.go` + `bot_manage.go` + `bot_agent.go` + `agent.go` + `webhook_dispatcher.go` | ~37 | Bot/Agent/Webhook 全族 | GOAL §3 排除（M6 之后再说）|
| `health.go` + `debug.go` | 2 | 运维 | im 自带 `/healthz` `/readyz`；debug/reset 不需要 |
| **合计** | **~125** | | |

---

## 2. 三类裁切（核心决策）

### 2.1 ✅ im REST 已覆盖（一对一替换）

> 客户端调用直接改 path + body shape，零翻译层。

| csesapi | → im /api/ |
|---|---|
| `POST /channel/create` | `POST /api/channels` (DM 用 `/api/channels/dm`) |
| `POST /channel/{id}/change/displayName` | `PUT /api/channels/:id` |
| `POST /channel/{id}/change/{notice,info,source,picture,props,orient,purpose,permission}` | `PUT/PATCH /api/channels/:id` (内部走 `channel_governance.go`) |
| `POST /channel/{id}/change/top` | `PATCH /api/channels/:id/members/:user_id { is_top: bool }` |
| `POST /channel/{id}/add/manger`、`/remove/manger` | `POST /api/channels/:id/managers/:user_id` / `DELETE` |
| `POST /channel/{id}/add/postPinned`、`/remove/postPinned`、`/load/postPinned` | `POST/DELETE/GET /api/channels/:id/pins/:message_id` |
| `POST /channel/{id}/onlineStatus` | `GET /api/channels/online-status` (M3 batch) |
| `POST /channel/{id}/close` | `POST /api/channels/:id/leave` 或权限码 |
| `POST /channel/{id}/member/snapshot` | `GET /api/channels/:id/members` 全量（无 bitmap，**snapshot 协议废弃**：现在客户端只要 `userId + companyId(=teamId)`） |
| `POST /channel/{id}/createSpecifyOwner` | `POST /api/channels` + body `creator_id` |
| `POST /channelmember/{id}/{change,change/role,change/notify,leave}` | `PATCH /api/channels/:id/members/:user_id` / `POST /api/channels/:id/leave` |
| `POST /posts/createPosts`、`/posts/create` | `POST /api/channels/:id/messages` |
| `POST /posts/revoke` | `DELETE /api/messages/:id` |
| `POST /posts/getReplies`、`/getReplyBranch` | `GET /api/messages/:id/replies` |
| `POST /posts/{urgentPost,urgentConfirm,urgentCancel}` | `POST /api/messages/urgent` / `POST /api/messages/:id/urgent/{confirm,cancel}` |
| `POST /posts/{createSchedule,cancelSchedule,getSchedule}` | `POST/DELETE/GET /api/messages/scheduled` |
| `POST /posts/quickReply` | `POST /api/channels/:id/messages` + 字段 `quick_reply_id`（im 已支持，0 改动） |
| `POST /posts/makeTopic` | `POST /api/channels/:id/topics` |
| `POST /post/{id}/{read,read/list}` | `POST /api/channels/:id/read` + `GET /api/messages/:id/readers` |
| `POST /post/{id}/announcement/{save,read,delete,list,detail,acceptList}` | `POST/DELETE/GET /api/announcements`、`POST /api/announcements/:id/read`、`GET /api/channels/:id/announcements`、`GET /api/announcements/:id/acks` |
| `POST /post_bookmark/{create,delete,load}` | `POST/DELETE/GET /api/favorites/:message_id` |
| `POST /post_approval/{id}/approval` | `POST /api/approvals` + `/approve` / `/reject` / `/cancel` |
| `POST /notification/{loadSend,loadTarget}` | `GET /api/notifications/{sent,received}` |

### 2.2 ❌ 整族砍掉（bitmap → seq cursor）

> 客户端不再有这些 HTTP 调用面。增量同步由 Tauri Rust [[entities/im-seq-data-source]] + `POST /api/sync` 接管，业务组件**只订阅 `im/message` IPC 事件**。

| 砍掉的 csesapi | 砍掉理由 |
|---|---|
| `POST /channel/{id}/bitmap` | seq 单调取代 bitmap |
| `POST /channels/load/increment` | 全量频道增量 → `POST /api/sync`（每频道 last_seq 游标）|
| `POST /channel/{id}/load/incrementByChannelId` | 单频道增量 → `POST /api/sync` 单频道 entry |
| `POST /posts/getPostsAfterIndex` | bitmap 索引消失 → seq 直接 |
| `POST /posts/getPostsAfterFromSegment` | segment 协议消失 → `GET /api/messages/:id/after` 或 `POST /api/sync` |
| `POST /posts/getUpdatedPosts` | 增量更新 → WS `msg_updated` push |
| `POST /posts/getLatestPost` | 启动拉最新 → `POST /api/sync` 首次拉取 |
| `POST /posts/getStartAndEndTime` | 按时间段查 segment → 不需要（seq 单调即时间序）|
| `POST /posts/remove`(SegmentRemove) | segment 删除 → 普通 `DELETE /api/messages/:id` |
| `POST /post_segment` | segment 写入协议 → 不需要 |
| `POST /post_segment/batch` | 同上 |
| `POST /posts_segment_config` | segment 配置 → 不需要 |
| `POST /posts_segment_daily_metadata/{load,ack,batchAck}` | 每日 metadata ack 协议 → seq cursor 自带断点续传，不需要 ack |

→ **共 13 条 bitmap 协议接口，im 后端 0 实现。**

### 2.3 留 cses Java（不归 im 拥有）

> GOAL.md §3 已明确：客户端继续用 `this.http`（baseUrl = `196.168.1.177:3399/api/cses`），不走 imHttp。

| csesapi 子集 | 数量 |
|---|---|
| `search/{post,user,channel,do}` | 4 |
| `groups` | 1 |
| `cses_group` (modules/teams/personal_guard 内部) | ~3 |
| Bot / bot_manage / bot_agent / agent / webhook | ~37 |
| `posts/templateReceived` | 1 |
| `users/{list,status/ids}`、`POST /users` | 3 |
| `health` / `debug` | 2 |
| 客户端非 message-v3 已存在的 vote / average | ~10 |
| **合计** | **~60** |

---

## 3. 数字总览

| 维度 | 值 |
|---|---|
| csesapi 全集 | ~125 |
| → im REST 已覆盖（直接替换） | ~50 |
| → 砍掉（bitmap 协议） | 13 |
| → 留 cses Java（GOAL §3 排除） | ~60 |
| → im 需新增 endpoint | **0** |
| → im 需新增字段 | 0~1（`quick_reply_id`，已支持）|

---

## 4. 与 wiki 其他页面的关系

- **替换 bitmap 整族的理论基础**：[[concepts/seq-cursor]]
- **客户端切换信号**：[[concepts/api-flavor-switch]]（cses Java 登录响应下发）
- **Rust 同步层**：[[entities/im-seq-data-source]]
- **不留回切**：[[decisions/no-traffic-rollback]]
- **裁切的目标里程碑**：[[milestones/M6-mattermost-decom]]
- **本期最低成本路线图**：[[syntheses/min-cost-mattermost-cutover]]

## 5. 历史误解（避免重蹈）

> **见 [[log]] 2026-04-30 contradiction 条目**：第一版分析里曾建议"客户端加 path 翻译表把 cses-shape 映射到 im REST"，错。正确路线是**直接 rewrite 调用面**，让 cses-shape 在客户端代码里自然消失，不留兼容层。

# im 后端 Entry / Schema Reference

> 给 cses-client 团队 / 联调 QA / cses Java 桥接团队的**数据模型字典**。
> 配套 `docs/CSES_CLIENT_内部对接契约.md`（路由 + WS 协议）使用：那篇是 endpoint
> contract，本篇是 **payload 字段类型 / 序列化形式 / 枚举值** 详表。
>
> **当前后端版本**：`v0.7.3-backend-final`（HEAD `048c5b4`）
> **同步对象**：
> - `server/internal/repo/*.go` 业务实体（GORM model）
> - `server/internal/http/*.go` 各 family request/response DTO
> - `server/internal/gateway/types.go` WS payload struct
>
> **客户端 TypeScript 接口建议**：cses-client 端建一个独立 `core/im-api/types.ts`，按本文档 1:1 对齐字段名 / 类型 / 可空性。**禁止**把 cses Java 老接口的 DialogItem / ChatMessage 类型直接复用——shape 不同（Mattermost ID 是 string，im 业务 ID 是 int64）。

---

## 目录
1. [ID 类型与序列化约定](#1-id-类型与序列化约定)
2. [Domain Entities（13 个数据库实体 + JSON 字段全表）](#2-domain-entities)
3. [HTTP Request / Response DTOs（按 family）](#3-http-request--response-dtos)
4. [WebSocket Payload（22 type 对应 struct）](#4-websocket-payload)
5. [Enums / Constants](#5-enums--constants)
6. [Envelope 包装（C007）](#6-envelope-包装)
7. [可空性（pointer / omitempty）规则](#7-可空性规则)

---

## 1. ID 类型与序列化约定

| 概念 | Go 类型 | JSON 类型 | 编码 | 备注 |
|---|---|---|---|---|
| **mm UserID** | `string` | `string` | 24-char lowercase hex | MongoDB ObjectId 格式。所有 `*_id` 用户字段（sender_id / requester_id / approver_id / ...）。**不是** UUID，**不是** 数字。 |
| **CookieID** | `string` | `string` | 24-char lowercase hex | 同上格式。`CookieId` HTTP header / `?cookie_id=` query / `?token=` 是 JWT 老路径仅 admin 用 |
| **channel_id** | `int64` | `number` (JS `number`，安全 ≤ 2^53) | autoincrement | im 后端用 PostgreSQL bigserial。客户端 JS `number` 精度足够（2^53 ≈ 9e15，远大于实际行数）|
| **message_id** | `int64` | `number` | autoincrement | 同上 |
| **seq** | `int64` | `number` | autoincrement (channel-scoped) | `(channel_id, seq)` 唯一约束，单调递增，gap-free（`AllocSeqAndInsert` 保证原子）|
| **client_msg_id** | `string` | `string` | 客户端生成（建议 UUID v4） | 36 字符上限。idempotency key — 重发同一 client_msg_id 不会重复入库 |
| **push_id** | `string` | `string` | 服务端生成 | `push_msg` payload 的幂等 key，客户端 ACK 时回传同值 |
| **TeamID** | `*string` | `string \| null` | mm CompanyID（24-hex）/ OrgID 兜底 | nullable，无 org 用户为 null |
| **created_at / updated_at / decided_at / deleted_at** | `time.Time` / `*time.Time` | RFC3339 string (`"2026-05-08T15:23:19.761944+08:00"`) | UTC offset | 后端用本机 timezone（pre / prod 都 UTC+08）。客户端建议用 ISO 8601 解析 |

**TypeScript 客户端类型约定建议**：

```typescript
type UserID = string;       // 24-hex
type ChannelID = number;    // int64-as-number, JS safe
type MessageID = number;
type Seq = number;
type ClientMsgID = string;
type PushID = string;
type ISOTime = string;      // RFC3339
type TeamID = string | null;
```

---

## 2. Domain Entities

> 所有字段 GORM tag + JSON tag 直接来自 `server/internal/repo/*.go`。客户端如果要按 entity 解响应 body，对照本节字段名 / 类型 / 是否 omitempty。

### 2.1 `Channel` — `channels` 表

| JSON 字段 | 类型 | nullable | 默认 | 说明 |
|---|---|---|---|---|
| `id` | `number` (int64) | no | autoincrement | 主键 |
| `type` | `number` (int16) | no | — | 1=DM / 2=Group（见 §5） |
| `name` | `string` | no | `""` | 群名（DM 一般为空） |
| `avatar_url` | `string` | no | `""` | |
| `seq` | `number` (int64) | no | 0 | 当前 channel 最大消息 seq |
| `creator_id` | `string` (mm UserID) | no | — | 创建者 |
| `team_id` | `string \| undefined` | yes (omitempty) | null | mm CompanyID frozen at create |
| `notice` | `string` | no | `""` | 群公告（govern PATCH 触发 `channel_info_updated`） |
| `purpose` | `string` | no | `""` | 群简介 |
| `picture_url` | `string` | no | `""` | |
| `props` | `string` (JSONB raw text) | no | `"{}"` | 业务自定义；客户端再 `JSON.parse` 一层 |
| `orient` | `number` (int16) | no | 0 | 业务自定义 tag |
| `permission` | `number` (int16) | no | 0 | 0=open / 1=approval / 2=closed |
| `is_top` | `boolean` | no | false | 频道全局 pin（与 channel_members.is_top per-user 不同）|
| `root_id` | `number \| undefined` | yes | null | M3 子群聊：父 channel_id |
| `root_message_id` | `number \| undefined` | yes | null | M3 子群聊：从哪条消息分叉 |
| `created_at` | `string` (RFC3339) | no | now() | |
| `updated_at` | `string` (RFC3339) | no | now() | |
| `deleted_at` | `string \| undefined` (RFC3339) | yes (omitempty) | NULL | **v0.7.3 gap #1+#3**：owner DELETE /channels/:id 设 deleted_at = now()；cses-client 读 `channel.deleted_at` 翻 dialog.deleteAt 软删除标记 |

### 2.2 `ChannelMember` — `channel_members` 表（复合 PK `(user_id, channel_id)`）

| JSON | 类型 | 说明 |
|---|---|---|
| `user_id` | `string` (mm UserID) | |
| `channel_id` | `number` | |
| `role` | `number` (int16) | 1=member / 2=admin / 3=owner（见 §5） |
| `last_read_seq` | `number` | channel-level 已读位置（**post-level read 已废**） |
| `phantom_count` | `number` | M3 phantom message 计数 |
| `phantom_at_read` | `number` | |
| `notify_pref` | `number` (int16) | 0=all / 1=mentions / 2=none |
| `is_top` | `boolean` | per-user 置顶（PATCH 触发 `channel_top_updated`） |
| `nick_name` | `string` | **v0.7.3 gap #5** per-(user, channel) 群昵称；空串=回退全局名；PATCH /channels/:id/members/:user_id/nickname 触发 `channel_member_updated{change_type:"nickname"}` 给全员 |
| `joined_at` | `string` (RFC3339) | |

### 2.3 `Message` — `messages` 表

| JSON | 类型 | nullable | 说明 |
|---|---|---|---|
| `id` | `number` (int64) | no | |
| `channel_id` | `number` (int64) | no | |
| `seq` | `number` (int64) | no | (channel_id, seq) 唯一 |
| `client_msg_id` | `string` | yes (omitempty) | 客户端 UUID，36 字符上限 |
| `sender_id` | `string` (mm UserID) | no | |
| `team_id` | `string \| undefined` | yes (omitempty) | denormalised from channels.team_id at write time, frozen |
| `msg_type` | `number` (int16) | no | 1=text / 2=image / 3=file / 4=system / 99=phantom |
| `content` | `string` | no | 文本内容 |
| `visible_to` | `string[] \| undefined` | yes (omitempty) | mm UserIDs，nil = broadcast 全 channel；非 nil = 定向消息 |
| `reply_to` | `number \| undefined` | yes | 回复的 message_id |
| `forwarded_from` | `number \| undefined` | yes | 转发源 message_id |
| `created_at` | `string` (RFC3339) | no | |
| `updated_at` | `string \| undefined` | yes | 编辑后才有值 |
| `deleted` | `boolean` | omitempty | 软删（true 的同时 deleted_at 有值）|
| `deleted_at` | `string \| undefined` | yes | |
| `is_urgent` | `boolean` | omitempty | true = 加急消息 |
| `props` | `string \| undefined` (JSONB raw) | yes | 系统消息（msg_type=4）+ 模板消息用，例：`{"sys_type":"member_joined","actor_id":"<24hex>"}` 或 `{"template":{"type":"TEXT","userIds":["<uid>"]}}` |

### 2.4 `Friendship` — `friendships` 表

| JSON | 类型 | 说明 |
|---|---|---|
| `id` | `number` | autoinc |
| `requester_id` | `string` (mm UserID) | 发起方 |
| `addressee_id` | `string` (mm UserID) | 接收方 |
| `status` | `number` (int16) | 1=pending / 2=accepted / 3=rejected / 4=blocked |
| `created_at` / `updated_at` | `string` | |

### 2.5 `File` + `MessageAttachment`

```typescript
interface File {
    id: number;                 // int64
    uploader_id: string;        // mm UserID
    file_name: string;
    file_size: number;          // bytes
    mime_type: string;
    // storage_path: 后端字段，json:"-" 不暴露
    thumbnail_path?: string;
    created_at: string;
}

interface MessageAttachment {  // 关联表
    message_id: number;
    file_id: number;
}
```

> `File` 路由（POST /files / GET /files/:id / GET /messages/:id/attachments）当前 **走 cses Java + 外部 OSS**，im 这边的 file table 仅做 metadata 索引。

### 2.6 `MessageFavorite` — `message_favorites` 表

```typescript
interface MessageFavorite {
    user_id: string;       // mm UserID
    message_id: number;
    created_at: string;
}
```

`GET /api/favorites` 实际返回 `FavoriteWithMessage[]`（join 了 messages）：

```typescript
interface FavoriteWithMessage {
    user_id: string;
    message_id: number;
    created_at: string;
    message: Message;      // join 出来的 Message entity
}
```

### 2.7 `Module` — `modules` 表（v0.7.2 新增）

```typescript
interface Module {
    name: string;          // PK，e.g. "MEETING_CHAT"
    label?: string;        // 显示名，e.g. "会议聊天"
    url?: string;          // 跳转 URL
    id?: string;           // 业务 id（与表 PK name 不同）
}
```

migration 016 seed 6 行：会议聊天 / 审批 / 任务 / 成果导向 / 切换公司 / 文档。

### 2.8 `MessageReaction` — `message_reactions` 表（v0.7.0 新增，复合 PK `(message_id, user_id, emoji)`）

```typescript
interface MessageReaction {
    message_id: number;
    user_id: string;       // mm UserID
    emoji: string;         // varchar(64)
    created_at: string;
}
```

### 2.9 `ChannelManager` — `channel_managers` 表

```typescript
interface ChannelManager {
    channel_id: number;
    user_id: string;       // mm UserID
    added_by: string;      // mm UserID（owner）
    added_at: string;
}
```

### 2.10 `ChannelPinnedMessage` — `channel_pinned_messages` 表

```typescript
interface ChannelPinnedMessage {
    channel_id: number;
    message_id: number;
    pinned_by: string;     // mm UserID
    pinned_at: string;
}
```

### 2.11 `UserSettings` — `user_settings` 表

```typescript
interface UserSettings {
    user_id: string;                // mm UserID, PK
    notification_enabled: boolean;
    theme: string;                  // 默认 "system" (server hardcode 'light' 但 service 层 fallback 'system')
    language: string;               // "zh-CN" / "zh" / "en"
    settings_json: string;          // JSONB raw，业务自管 schema
    updated_at: string;
}
```

> ⚠️ **GORM Upsert default:true bug**：PUT /settings 单独 flip `notification_enabled: false` 会被 default 覆盖回 true。不要单独改这个字段，等后端修。

### 2.12 `Announcement` + `AnnouncementAck`

```typescript
interface Announcement {
    id: number;
    channel_id: number;
    creator_id: string;        // mm UserID
    title: string;
    content: string;
    props: string;             // JSONB raw
    deleted: boolean;
    created_at: string;
    updated_at: string;
}

interface AnnouncementAck {    // PK (announcement_id, user_id)
    announcement_id: number;
    user_id: string;
    acknowledged_at: string;
}
```

`GET /announcements/:id/acks` 响应 shape `{acks: AnnouncementAck[]}`（外层包一层）。

### 2.13 `Approval` — `approvals` 表

```typescript
interface Approval {
    id: number;
    channel_id: number;
    requester_id: string;       // mm UserID
    approver_id: string;        // mm UserID
    subject: string;
    content: string;
    props: string;              // JSONB raw
    status: number;             // 0=pending / 1=approved / 2=rejected / 3=cancelled
    decided_at?: string;        // null until decided
    decision_note?: string;     // 审批理由
    created_at: string;
    updated_at: string;
}
```

`GET /approvals/{pending,mine}` 响应 shape `{approvals: Approval[]}`。

### 2.14 `Notification` — `notifications` 表

```typescript
interface Notification {
    id: number;
    sender_id: string;          // mm UserID
    receiver_id: string;
    title: string;
    body: string;
    type: number;               // 0=generic / 1=mention / 2=system
    read_at?: string;           // null = 未读
    props: string;              // JSONB raw
    created_at: string;
}
```

`GET /notifications/{sent,received}` 响应 shape `{notifications: Notification[]}`。

### 2.15 `ScheduledMessage` — `scheduled_messages` 表

```typescript
interface ScheduledMessage {
    id: number;
    channel_id: number;
    sender_id: string;              // mm UserID
    content: string;
    msg_type: number;
    visible_to?: string[];          // mm UserIDs，omitempty
    reply_to?: number;
    file_ids?: number[];            // pq.Int64Array
    scheduled_at: string;           // 必须 ≥ 60s 未来
    status: number;                 // 0=pending / 1=delivered / 2=cancelled / 3=failed
    delivered_message_id?: number;  // status=delivered 时填
    error?: string;                 // status=failed 时填
    created_at: string;
    updated_at: string;
}
```

`GET /messages/scheduled` 响应 shape `{scheduled: ScheduledMessage[]}`。

### 2.16 `UrgentConfirmation` — `urgent_confirmations` 表（PK `(message_id, user_id)`）

```typescript
interface UrgentConfirmation {
    message_id: number;
    user_id: string;        // mm UserID
    confirmed_at: string;
}
```

`GET /messages/:id/urgent/confirmations` 响应 shape `{confirmations: UrgentConfirmation[]}`。

### 2.17 `QuickReply` — `quick_replies` 表

```typescript
interface QuickReply {
    id: number;
    user_id: string;        // mm UserID（仅本人可见可编辑）
    label: string;          // 短标签（按钮文本）
    content: string;        // 实际发送的内容
    sort_order: number;     // 排序
    created_at: string;
    updated_at: string;
}
```

`GET /quick-replies` 响应 shape `{quick_replies: QuickReply[]}`。

---

## 3. HTTP Request / Response DTOs

> 仅列**非 entity 直接返回**的 request body / 复合 response shape。entity 直返的 endpoint（如 `GET /channels/:id` 直返 `Channel`）参考 §2。

### 3.1 channel

```typescript
// POST /channels
interface CreateGroupReq {
    name: string;
    member_ids: string[];     // mm UserIDs（owner 自动加入）
}

// POST /channels/dm
interface CreateDMReq {
    peer_id: string;          // mm UserID
}
// 返回：Channel（已存在则幂等返回，新建 201，已存在 200）

// PUT /channels/:id（整体替换，少用）
interface UpdateChannelReq {
    name?: string;
    avatar_url?: string;
    notice?: string;
    purpose?: string;
}

// POST /channels/:id/members
interface AddMemberReq {
    user_id: string;          // mm UserID
}

// DELETE /channels/:id        v0.7.3 gap #1 + #3 — owner解散群聊
// body 为空；返回 Channel（含 deleted_at 时间戳）；触发 channel_closed WS 给原 channel 全员。
// 错误码：403 only_owner / 404 not_found / 200 idempotent_already_closed
// 注意：cses-client 老 path `POST /channel/close`，body `{channelId}`。后端不再保留这个路径。

// PATCH /channels/:id/members/:user_id/nickname   v0.7.3 gap #5
interface SetMemberNicknameReq {
    nick_name: string;        // <= 64 字符；空串清除 override → 回退到全局名
}
// 返回：ChannelMember（含 nick_name）；caller==target 或 admin/owner 可调；
// 触发 channel_member_updated{change_type:"nickname"} 给全员。
```

### 3.2 channel-governance

```typescript
// PATCH /channels/:id（局部，每字段独立 nullable，omitempty 即"未变更"）
interface PatchChannelReq {
    name?: string;
    avatar_url?: string;
    notice?: string;
    purpose?: string;
    picture_url?: string;
    props?: any;              // JSON object（后端按 raw JSON 透传）
    orient?: number;
    permission?: number;
    is_top?: boolean;         // channel-level（与 PATCH /members/:user_id 的 is_top 不同）
}

// PATCH /channels/:id/members/:user_id
interface PatchMemberReq {
    role?: number;            // owner-only 才能改
    notify_pref?: number;     // self-only
    is_top?: boolean;         // self-only，触发 channel_top_updated WS push
}
```

### 3.3 message

```typescript
// POST /channels/:id/messages
interface SendMessageReq {
    content: string;
    msg_type?: number;        // 默认 1=text
    visible_to?: string[];    // 定向消息：仅这些 mm UserIDs 能看
    reply_to?: number;        // 回复某条消息
    client_msg_id?: string;   // idempotency
    file_ids?: number[];      // 附件
    quick_reply_id?: number;  // 替代 cses /posts/quickReply
}

// PATCH /messages/:id
interface EditMessageReq {
    content: string;
}

// POST /messages/forward
interface ForwardMessageReq {
    message_id: number;
    target_channel_ids: number[];
}

// POST /messages/batch
interface BatchSendReq {
    channel_id: number;       // 注意：批量发到同一 channel
    messages: SendMessageReq[];
}
// 响应：{messages: Message[], skipped: number[]}（非成员 channel 静默跳过到 skipped）

// GET /channels/:id/messages（分页响应）
interface FetchMessagesResp {
    messages: Message[];
    has_more: boolean;        // 后端是否还有更多
    next_before?: number;     // 下一页的 before seq
}

// GET /messages/:id/replies/branch?offset=N&limit=M     v0.7.3 gap #2
// 二级 thread 子回复分页查询（替代 cses /posts/getReplyBranch）
interface ReplyBranchResp {
    messages: Message[];      // reply_to == rootID 的非删除消息，seq ASC
    has_more: boolean;        // 同 fetch limit+1 探测
    offset: number;           // 当前页 offset
    limit: number;            // 当前页 limit (上限 200)
}
```

### 3.4 message-template + read-stats（Phase 1）

```typescript
// POST /messages/:id/received  （body 为空）
// 响应：Message（更新后的，props.template.userIds 含 callerID）

// GET /messages/read-stats?ids=1,2,3
interface ReadStatsResp {
    stats: ReadStat[];
}

interface ReadStat {
    message_id: number;
    total_members: number;
    read_count: number;
    unread_count: number;
    unread_user_ids: string[];     // 截断到前 50
    has_more_unread: boolean;
}
```

### 3.5 sync

```typescript
// POST /sync
interface SyncReq {
    channels: SyncChannelState[];
}

interface SyncChannelState {
    id: number;
    seq: number;       // client 当前 max seq
}

interface SyncResp {
    channels: SyncChannelEntry[];
}

interface SyncChannelEntry {
    id: number;
    server_seq: number;
    messages: Message[];   // 增量补回的消息（seq > client.seq）
}
```

### 3.6 friend

```typescript
// POST /friends/request
interface SendFriendRequestReq {
    addressee_id: string;
}

// POST /friends/{accept,reject}
interface FriendshipIDReq {
    friendship_id: number;
}

// POST /friends/block
interface BlockReq {
    user_id: string;
}
```

### 3.7 channel-topic

```typescript
// POST /channels/:id/topics
interface CreateTopicReq {
    root_message_id: number;
    name: string;
    member_ids?: string[];
}
// 响应：Channel（root_id + root_message_id 已设置）
```

### 3.8 announcement

```typescript
// POST /announcements
interface SaveAnnouncementReq {
    channel_id: number;
    title: string;
    content: string;
    props?: any;          // JSONB
}

// POST /announcements/:id/read（body 为空）
// 响应：{ack: AnnouncementAck}
```

### 3.9 urgent

```typescript
// POST /messages/urgent
interface SendUrgentReq {
    channel_id: number;
    content: string;
    client_msg_id?: string;
}
// 响应：Message（is_urgent=true）

// POST /messages/:id/urgent/{confirm,cancel}（body 为空）
```

### 3.10 approval

```typescript
// POST /approvals
interface CreateApprovalReq {
    channel_id: number;
    approver_id: string;
    subject: string;
    content: string;
    props?: any;
}

// POST /approvals/:id/{approve,reject}
interface DecisionReq {
    note?: string;          // 审批理由
}

// POST /approvals/:id/cancel（body 为空）
```

### 3.11 notification

```typescript
// POST /notifications
interface SendNotificationReq {
    receiver_id: string;
    title: string;
    body: string;
    type: number;            // 0/1/2，见 §5
    props?: any;
}
// 响应：Notification

// POST /notifications/:id/read（body 为空）
// 响应：{notification: Notification, status: "read"}
```

### 3.12 quick-reply

```typescript
// POST /quick-replies
interface CreateQuickReplyReq {
    label: string;
    content: string;
    sort_order?: number;
}

// PATCH /quick-replies/:id
interface PatchQuickReplyReq {
    label?: string;
    content?: string;
    sort_order?: number;
}
```

### 3.13 reaction

```typescript
// POST /messages/:id/reactions
interface ReactionAddReq {
    emoji: string;          // varchar(64)
}
// 响应：{reaction: MessageReaction, status: "added" | "exists"}
// DELETE /messages/:id/reactions/:emoji（body 为空）
```

### 3.14 scheduled

```typescript
// POST /messages/scheduled
interface CreateScheduledReq {
    channel_id: number;
    content: string;
    msg_type: number;
    visible_to?: string[];
    reply_to?: number;
    file_ids?: number[];
    scheduled_at: string;       // RFC3339，必须 ≥ now+60s
}
// 响应：ScheduledMessage
```

### 3.15 settings

```typescript
// PUT /settings
interface UpdateSettingsReq {
    notification_enabled?: boolean;   // ⚠️ default:true bug，避免单独 false
    theme?: string;
    language?: string;
    settings_json?: string;           // JSONB raw（客户端 JSON.stringify 一层）
}
// 响应：UserSettings（postwrite 状态）

// GET /settings：UserSettings（无 row 时默认 {theme:"system", language:"zh", notification_enabled:true}）
```

---

## 4. WebSocket Payload

> 全部走 `{type, payload}` wire 格式（**payload 是 raw JSON object 不是 base64**，详见 `CSES_CLIENT_内部对接契约.md §5.1` trap）。

### 4.1 client → server

```typescript
// type: "ping"
interface PingPayload {
    channel_seqs?: { [channelIdStr: string]: number };  // key 是 channel_id 转字符串
}

// type: "send"
interface SendPayload {
    client_msg_id: string;
    channel_id: number;
    content: string;
    msg_type?: number;
    visible_to?: string[];
}

// type: "push_ack"
interface PushACKPayload {
    push_id: string;
}

// type: "sync"  (保留，当前未由 ws_handler 处理 — sync 走 HTTP)
interface SyncPayload {
    channels: { id: number; seq: number }[];
}
```

### 4.2 server → client

```typescript
// type: "pong"
interface PongPayload {
    server_time: number;       // unix ms
    channel_seqs?: { [channelIdStr: string]: number };  // 仅含 server_seq > client_seq 的 channel
}

// type: "push_msg"
interface PushMsgPayload {
    push_id: string;           // ack 回传同值
    type?: "NOTICE";           // v0.7.3 gap #9 — 仅 msg_type=4 系统消息携带
    channel_id: number;
    seq: number;
    server_msg_id: number;
    sender_id: string;
    content?: string;
    msg_type: number;          // 1=text/2=image/3=file/4=system/99=phantom
    visible_to?: string[];
    props?: string;            // v0.7.3 gap #9 — JSONB raw text；msg_type=4 时填，需 JSON.parse 后看 sys_type 派生 channel 元数据
    created_at: string;
}

// type: "send_ack"
interface SendACKPayload {
    client_msg_id: string;
    server_msg_id: number;
    seq: number;
    channel_id: number;
}

// type: "read_sync"
interface ReadSyncPayload {
    channel_id: number;
    read_seq: number;
}

// type: "friend_event"
interface FriendEventPayload {
    event_type: "request" | "accepted" | "rejected";
    from_user_id: string;
}

// type: "channel_event"
interface ChannelEventPayload {
    event_type: "added";
    channel_id: number;
    name: string;
}

// type: "channel_top_updated"  (per-user 多设备同步)
interface ChannelTopPayload {
    channel_id: number;
    is_top: boolean;
}

// type: "msg_updated" / "msg_deleted" / "announcement_posted" / "urgent_posted" /
// "approval_updated" / "notification_received" / "channel_info_updated"
//   payload 是对应 entity 整条 JSON（Message / Announcement / Approval / Notification / Channel）

// type: "reaction_added" / "reaction_removed"
interface ReactionPayload {
    channel_id: number;
    message_id: number;
    user_id: string;
    emoji: string;
}

// type: "channel_closed"     v0.7.3 gap #1 + #3
interface ChannelClosedPayload {
    channel_id: number;
    actor_id: string;          // mm UserID of the owner
    deleted_at: string;        // RFC3339；与 channels.deleted_at 一致
}

// type: "channel_member_updated"     v0.7.3 gap #4 + #5
// 同一个 type 承载 4 种成员变更，change_type 字段分流；payload.members 是
// post-change 的完整 roster snapshot — 客户端可一遍替换本地成员状态，不需
// 再调 GET /channels/:id/members。
interface ChannelMemberUpdatedPayload {
    channel_id: number;
    change_type: "join" | "leave" | "kick" | "nickname";
    actor_id: string;          // 触发者 mm UserID
    target_id: string;         // 被影响成员 mm UserID（leave 时 actor==target）
    nick_name?: string;        // 仅 change_type==nickname 填
    members: ChannelMemberSummary[];
}

interface ChannelMemberSummary {
    user_id: string;
    role: number;              // 1/2/3 见 §5.2
    nick_name?: string;
    is_top?: boolean;
    notify_pref?: number;
}

// type: "schedule_created" / "schedule_canceled"     v0.7.3 gap #7
// 仅推 sender 多设备，不广播 channel 全员。
interface ChannelSchedulePayload {
    channel_id: number;
    scheduled_id: number;
    has_schedule_post: boolean;   // 最后一条取消时 false，否则 true
}
```

---

## 5. Enums / Constants

### 5.1 Channel.type

| 值 | 名 | 说明 |
|---|---|---|
| 1 | DM | 1v1 直接消息 |
| 2 | Group | 群聊 |

### 5.2 ChannelMember.role

| 值 | 名 | 说明 |
|---|---|---|
| 1 | Member | 普通成员 |
| 2 | Admin | 管理员（介于 owner / member 之间，未来扩展用）|
| 3 | Owner | 创建者，唯一 |

### 5.3 ChannelMember.notify_pref

| 值 | 名 | 说明 |
|---|---|---|
| 0 | All | 所有消息都通知 |
| 1 | Mentions | 仅 @ 提及 |
| 2 | None | 静音 |

### 5.4 Channel.permission

| 值 | 名 | 说明 |
|---|---|---|
| 0 | Open | 任何人可加入 |
| 1 | Approval | 需要审批 |
| 2 | Closed | 仅邀请 |

### 5.5 Message.msg_type

| 值 | 名 | 说明 |
|---|---|---|
| 1 | Text | 普通文本 |
| 2 | Image | 图片（content 一般是 file_id 引用） |
| 3 | File | 文件 |
| 4 | System | 系统消息（props.sys_type 描述子类型） |
| 99 | Phantom | M3 幻影消息（仅特定用户可见的隐形 seq slot） |

### 5.6 Friendship.status

| 值 | 名 |
|---|---|
| 1 | Pending |
| 2 | Accepted |
| 3 | Rejected |
| 4 | Blocked |

### 5.7 Approval.status

| 值 | 名 |
|---|---|
| 0 | Pending |
| 1 | Approved |
| 2 | Rejected |
| 3 | Cancelled |

### 5.8 ScheduledMessage.status

| 值 | 名 |
|---|---|
| 0 | Pending |
| 1 | Delivered |
| 2 | Cancelled |
| 3 | Failed |

### 5.9 Notification.type

| 值 | 名 |
|---|---|
| 0 | Generic |
| 1 | Mention |
| 2 | System |

### 5.10 WSMessageType（26 种锁定，C005 + v0.7.3 gap 补丁）

详见 `CSES_CLIENT_内部对接契约.md §5`。简表：

```
client→server (4): ping, send, push_ack, sync
server→client (22):
  V1 12 - pong, push_msg, send_ack, sync_resp, read_sync,
          friend_event, channel_event, msg_updated
  M1  2 - msg_deleted (msg_updated shared with V1)
  M2  4 - announcement_posted, urgent_posted, approval_updated, notification_received
  v0.7 4 - reaction_added, reaction_removed,
           channel_top_updated, channel_info_updated
  v0.7.3-gap 4 - channel_closed, channel_member_updated,
                 schedule_created, schedule_canceled
```

### 5.11 NoticeType（v0.7.3 gap #9 顶层字段）

`push_msg` payload 顶层 `type` 字段，wire 形态：

| 值 | 触发 msg_type | 客户端解析 |
|---|---|---|
| "" (omit) | 1/2/3/99（text/image/file/phantom）| 常规聊天气泡 |
| `"NOTICE"` | 4（system）| cses-client Rust 走 `apply_notice_changes_standalone`，按 `props.sys_type` 派生 channel 元数据 |

### 5.12 sys_type 全量枚举（messages.props.sys_type）

| 值 | 触发 |
|---|---|
| `channel_created` | CreateGroup |
| `channel_updated` | Update (name/avatar/notice/...) |
| `channel_closed` | **v0.7.3 gap #1** owner 解散群聊 |
| `member_joined` | AddMember |
| `member_removed` | RemoveMember (admin kick) |
| `member_left` | LeaveChannel (self) |
| `member_nickname` | **v0.7.3 gap #5** SetMemberNickname |

---

## 6. Envelope 包装（C007）

**所有 `/api/*` 响应**经 `responseEnvelope` middleware 二次包装。客户端 interceptor 必须 unwrap：

```typescript
// 2xx 成功
type SuccessEnvelope<T> = {
    status: "success";
    data: T;       // entity / DTO / array / primitive，取决于 endpoint
};

// 4xx/5xx 错误
type ErrorEnvelope = {
    status: "error";
    error: string;   // 可读错误信息
};

type Envelope<T> = SuccessEnvelope<T> | ErrorEnvelope;

// 客户端 interceptor 模板
async function unwrap<T>(resp: Response): Promise<T> {
    const env = await resp.json() as Envelope<T>;
    if (env.status === "error") {
        throw new ImApiError(resp.status, env.error);
    }
    return env.data;
}
```

**跳过 envelope 的路径**：`/healthz` / `/readyz` / `/metrics` / `/ws`。

---

## 7. 可空性规则

### 7.1 Go pointer = JS optional/null

| Go | JSON 序列化（带 omitempty）| TypeScript 建议 |
|---|---|---|
| `*string` | `"foo"` 或省略 | `string \| undefined`（用 `?`） |
| `*int64` | `123` 或省略 | `number \| undefined` |
| `*time.Time` | RFC3339 string 或省略 | `string \| undefined` |

### 7.2 Go slice / nil

| Go | JSON | TS |
|---|---|---|
| `[]string{}` | `[]` | `string[]` (空数组) |
| `nil` `[]string` 带 omitempty | 字段消失 | `string[] \| undefined` |
| `pq.StringArray` (Postgres TEXT[]) | `["a","b"]` 或 nil omit | 同上 |
| `pq.Int64Array` | `[1,2,3]` 或 nil omit | `number[] \| undefined` |

### 7.3 GORM tag `not null;default:'<x>'`

服务端写入时，Go 零值（`""` / `0` / `false`）会被 GORM **替换为 default**。客户端**不要**依赖"我传 false 必定写 false"。`UserSettings.notification_enabled` 是这个 bug 最典型的例子。

### 7.4 nullable JSONB（`Props *string` 类型）

| Go | JSON | TS |
|---|---|---|
| `Props string` (not null, default `'{}'`) | `"props": "{}"`（字符串！）| `string`（客户端再 `JSON.parse` 一层） |
| `Props *string` (nullable) | `"props": "..."` 或 `"props": null` 或 omit | `string \| null \| undefined` |

**注意**：JSONB 字段在 wire 里是 **JSON-encoded string**（字符串），不是已经解析的对象。`Channel.props = "{\"foo\":1}"`，客户端要 `JSON.parse(channel.props)` 才能拿到 `{foo: 1}`。

---

## 8. 维护契约

- 后端 entity / DTO / payload 字段变更（加 / 删 / 改类型 / 改 nullable）必须**先**改本文档**再**写代码
- 加新字段：要么 omitempty（旧客户端忽略），要么升 V2（破坏性变更）
- 字段重命名：禁止（直接破坏旧客户端）；要改名走 alias + 双写过渡期
- 客户端发现字段缺漏 / 类型不符 → 提 issue 不要直接改后端 model

---

**Owner**：im 项目（`server/internal/repo/*.go` + `server/internal/http/*.go` + `server/internal/gateway/types.go` 是 source of truth）
**最后更新**：2026-05-08（v0.7.3-backend-final 配套）
**下次更新触发**：任何 entity / DTO / WS payload 字段变更 / 新增 entity

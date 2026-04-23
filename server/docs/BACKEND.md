# 后端架构与替换路线 — im/server

> 目标：以本项目为基础，全面替换旧 Mattermost (`server/channels/csesapi` + `channels/app/platform`)。
> 设计思想：Telegram 式高性能 — 简化协议、seq 单调递增、心跳即增量、单 Pod 状态最小化、跨 Pod 走消息总线。

---

## 一、设计原则（TG 风格）

| 原则 | Mattermost 做法 | im 做法 |
|------|----------------|--------|
| **协议最小化** | 70+ WebSocketEvent 常量 | **WSMessageType V1 锁定 12 种**（见 §1.1） |
| **增量单调** | bitmap(天/段) + segmentId 索引 | 频道级单调 `seq`，member 维度 `last_read_seq` |
| **心跳即同步** | 独立 `increment_channel` 事件下发 bitmap | `ping.channel_seqs` ↔ `pong.channel_seqs`（仅含 delta 频道） |
| **状态外置** | Session + DeviceId + UserSegmentDailyMetadata 维度 | Hub 只保存内存连接，路由走 Redis，跨 Pod 推送走 Pulsar |
| **推送语义** | Publish → Cluster 广播 → Hub 分片 → dead queue 128 | Pusher → Hub.PushToUser → 未命中时 Pulsar `msg.push.{gateway_id}` |
| **HTTP 风格** | RPC：`/posts/create`、`/channel/change/displayName` (~120 端点) | REST：`/channels/:id/messages`、`/channels/:id/members/:uid` (~36 端点) |

核心收益：**单 Gateway 二进制 ≈ 230 文件，节点级状态几乎无**。可水平扩展到 N 个 Gateway Pod，状态全部落在 Postgres / Redis / Pulsar。

### 1.1 WSMessageType 版本锁定

**V1（M1 交付，12 种）** — 三份文档 + Go 代码以此为准：

| 类型 | 方向 | 用途 |
|------|------|------|
| `ping` | C→S | 心跳，携带 `channel_seqs` 做增量探测 |
| `pong` | S→C | 心跳回包，仅回含 delta 的频道 |
| `send` | C→S | 客户端发消息（HTTP 外的快车道，可选） |
| `send_ack` | S→C | `send` 的服务端确认，返回 seq 与 server_msg_id |
| `push_msg` | S→C | 服务端推送新消息（含 phantom） |
| `push_ack` | C→S | 客户端 ACK 推送（按 push_id 幂等去重） |
| `sync_resp` | S→C | WS 内嵌 `/sync` 响应（可选快车道） |
| `read_sync` | S→C | 同用户其他设备的已读位置推进 |
| `friend_event` | S→C | 好友请求 / 接受 / 拒绝 |
| `channel_event` | S→C | 频道成员变动（added / removed） |
| **`msg_updated`** | S→C | **M1 新增**：消息编辑后推送 |
| **`msg_deleted`** | S→C | **M1 新增**：消息撤回后推送 |

**V2（M5 以后视需求再加，3 种候选，不在 M1-M6 验收范围）：**

| 类型 | 说明 |
|------|------|
| `reaction_updated` | 表情反馈变化 |
| `typing` | 正在输入状态 |
| `presence` | 用户在线状态变化（替代 Mattermost 的 `status_change`） |

> **约束：** 三份文档 + `internal/gateway/types.go` + Rust Tauri 事件命名必须严格对齐这张表。实现时 Rust 侧 IPC 事件命名沿用 `imWs:msg:pushed`、`imWs:msg:updated`、`imWs:msg:deleted` 等，与 WSMessageType 一一映射。

---

## 二、进程内组件

### 2.1 入口

```
server/cmd/gateway/main.go:run
  ├─ observability.Init        (OTel tracer + metric)
  ├─ config.Load               (yaml, IM_CONFIG 覆盖)
  ├─ repo.Open(GORM/Postgres)  (8 个 Repo 实例化)
  ├─ repo.OpenRedis            (路由表)
  ├─ gateway.NewHub            (单 Hub，userID→[]*Conn)
  ├─ gateway.NewRouting(rdb)   (跨 Pod 寻址)
  ├─ pulsar.New                (push 消费者 + message consumer)
  ├─ gateway.NewPushConsumer   (msg.push.{gatewayID})
  ├─ gateway.NewWsHandler      (持有 Hub/Routing/ChannelRepo)
  └─ Gin engine
       ├─ RegisterAuthRoutes          (/api/auth/*)
       └─ authedAPI = Group("/api") with JWT
            ├─ RegisterProfileRoutes
            ├─ RegisterSettingsRoutes
            ├─ RegisterFriendRoutes(pusher=hubFriendEventPusher)
            ├─ RegisterChannelRoutes(pusher=hubChannelEventPusher)
            ├─ RegisterMessageRoutes(Pusher+ReadSyncer)
            ├─ RegisterSyncRoutes
            ├─ RegisterSearchRoutes
            ├─ RegisterFileRoutes
            └─ RegisterFavoriteRoutes
```

**strangler-fig 已收官**（Phase 6 → 7.8），Gin 为主，仅 `GET /ws` 通过 NoRoute 落到 legacy mux。替换 Mattermost 后可彻底移除 legacy mux。

### 2.2 分层

```
HTTP Handler (internal/http)
   │ 仅做参数解析、错误映射；禁止直接访问 repo/DB
   ▼
Service (internal/service)  ← consumer-side small interface
   │ 业务编排、事务组装、push hook 调用
   ▼
Repo (internal/repo, GORM)
   │ 只写 SQL/ORM；不含业务逻辑
   ▼
Postgres
```

**对比 Mattermost 的 4 层（Handler → App → Store interface → SQLStore）：** im 的 Service 合并了 App 层的职责，但用「consumer-side interface」让 Service 只看到自己需要的 Store 子集（见 `service/sync.go:25` 的 `SyncChannelStore`）。减一层但不牺牲可测试性。

### 2.3 WebSocket 网关

```
Conn (internal/gateway/conn.go)
  ├─ knownSeq   map[channelID]int64   (用户当前本地 seq)
  ├─ send       chan *WSFrame (writePump 缓冲)
  └─ Push(type, payload) bool         (非阻塞写)

Hub (internal/gateway/hub.go)
  └─ map[int64][]*Conn                (userID → 多设备连接)
     Register / Deregister / ConnsForUser / PushToUser

PushConsumer (internal/gateway/push_consumer.go)
  ├─ topic = msg.push.{gatewayID}
  ├─ Pulsar 消费 PulsarPushEvent
  ├─ ackRegistry（幂等：收到对端 push_ack 后 resolve）
  └─ deliverWithRetry（hub 中无目标则重试 → 失败落死信）

Routing (internal/gateway/routing.go)
  ├─ Register(userID, gatewayID, ttl) → Redis
  └─ Deregister
     └─ 服务层要推用户 X 时：
         ① 本 Pod 命中 → 直接 hub.PushToUser
         ② 未命中 → 查 Redis → 发 Pulsar msg.push.{targetGatewayID}
```

### 2.4 心跳与增量（TG 式精髓）

```
Client → Ping { channel_seqs: {10086: 5230, 10087: 128} }
          │
Server ─ heartbeat.go → 调用 ChannelSeqStore.GetMemberChannelSeqs
          │             ↓
         对比本地 vs 服务端 seq
          │
Client ← Pong { server_time, channel_seqs: {10086: 5275} }
          │                     ↑ 只含有新消息的频道
          ▼
客户端发起 POST /api/sync 或 GET /channels/:id/messages?after_seq=5230
```

**一个心跳 = 一次增量信号通告**。客户端拿到 pong 就知道哪些频道需要补拉，不用额外的「定时同步」。

---

## 三、对照 Mattermost csesapi — 已覆盖 / 待补齐

### 3.1 ✅ 已覆盖（保持行为兼容，用 REST 重写）

| 功能 | 旧 | 新 |
|------|----|----|
| 发消息 | `POST /api/cses/posts/create` | `POST /api/channels/:id/messages` |
| 拉消息 | `/posts/get`、`/getPostsAfterIndex`、`/getLatestPost`、`/getUpdatedPosts` | `GET /api/channels/:id/messages?after_seq/before_seq/around_seq`；**按时间跳段** `GET /api/channels/:id/messages/around?timestamp=<ms>&limit=50`（M1 交付） |
| 转发 | `/posts/getReplyBranch`（引用） | `POST /api/messages/forward` (1→N) |
| 频道查看/已读 | `/channels/view`、`/post/read` | `POST /api/channels/:id/read` |
| 频道创建 | `/channel/create`、`/createSpecifyOwner` | `POST /api/channels`、`/api/channels/dm` |
| 频道列表 | 通过 login 返回 + `/channels/load/increment` | `GET /api/channels` + `POST /api/sync` |
| 频道详情/改名 | `/channel/change/info`、`/change/displayName` | `GET /api/channels/:id`、`PUT /api/channels/:id` |
| 成员增删 | `/channel/member/change`、`/leave` | `POST /api/channels/:id/members`、`DELETE .../:user_id`、`POST /leave` |
| 成员列表 | `/channel/member/snapshot` | `GET /api/channels/:id/members` |
| 收藏 | `/post/bookmark/(create/delete/load)` | `POST/DELETE/GET /api/favorites/:message_id` |
| 附件 | Mattermost `/files/*` 原生 | `POST /api/files`、`GET /files/:id`、`GET /messages/:id/attachments` |
| ~~搜索~~ | ~~`/search/(post/user/channel/do)`~~ | **由 Java 远程服务承担**（见 §3.4），im 不实现 |
| 增量同步 | `/channels/load/increment` + WS `increment_channel` + bitmap + segment | `POST /api/sync` |

### 3.2 ❌ 尚缺 — 替换 Mattermost 前必须补齐的模块

按**业务价值 × 迁移难度**排优先级：

#### P0（聊天核心，无它不可替换）
- **消息撤回** `POST /posts/revoke` → 建议 `DELETE /api/messages/:id` + WS 推送 `message_revoked`
- **线程回复** `/posts/getReplies`、`/getReplyBranch` → `GET /api/messages/:id/replies`；发消息时加 `reply_to` 已在 `sendMessageReq` 支持，缺查询 API
- **已读列表** `/post/read/list`（谁看过这条消息）→ `GET /api/messages/:id/readers`。当前 `last_read_seq` 模型可支撑（按频道 seq 反查成员），但需要实现
- **批量发消息** `/posts/createPosts` → 可选，若前端依赖则补
- **消息编辑** 当前未见 → `PATCH /api/messages/:id`

#### P1（企业协作核心）
- **公告 /post/announcement/\*** 6 个端点 → `/api/announcements/*`
- **紧急消息 /posts/urgentPost + urgentConfirm + urgentCancel** → `/api/messages/urgent/*`
- **通知 /notification/(loadSend/loadTarget)** → `/api/notifications`（消息通知历史）
- **审批 /post/approval** → `/api/approvals`
- **定时消息 /posts/createSchedule + cancelSchedule + getSchedule** → `/api/messages/scheduled`
- **快捷回复 /posts/quickReply** → `/api/messages/:id/quick-reply`
- **频道属性精细改动**：change/(notice|purpose|top|picture|props|orient|permission)、add/manger、add/postPinned 等约 15 个端点 → 可以合并成 `PATCH /api/channels/:id`（JSON Patch 或部分字段更新） + 独立 `/pin`、`/manager` 端点

#### P2（辅助功能）
- **投票** `/vote/*` (5 个) → **不在 Go 侧实现**，走 Java 远程调用。在 im/server 暂列为 TODO：后端只需保留用户鉴权头透传能力，前端直接命中 Java 服务
- **模板消息** `/post/templateReceived` → `/api/messages/:id/template-received`
- **群模块列表** `/modules/getAll`、`/groups`、`/teams/*` → `/api/modules`、`/api/groups`、`/api/teams/*`

#### P3（可暂缓）
- **AI Agent** `/agents/*`（6 个） — 独立子系统，可作为新 service
- **Bot 管理** `/bot-manage/*`（9 个） — 独立
- **Webhook** `/webhook/(config/test)` — 独立

### 3.3 🔒 `POST /api/sync` 接口契约（前后端 + Rust 统一对齐）

> 这是整个替换计划的**核心同步端点**，前后端 + Rust 三侧今晚开工前必须以此为准。

#### 请求

```http
POST /api/sync
Authorization: Bearer <jwt>
Content-Type: application/json

{
  "channels": [
    { "id": 10086, "seq": 5230 },
    { "id": 10087, "seq":    0 }   // seq=0 = 新频道或首次同步
  ]
}
```

#### 请求边界

| 参数 | 上限 | 超限行为 |
|------|-----|---------|
| `channels` 数量 | `max_channels_per_call = 500` | 返回 `400 Bad Request {error:"too many channels"}` |
| 单频道 `seq` | int64 非负 | 负值视为 0 |

客户端频道超 500 时应分批调用；**同一用户并发 /sync 无保证顺序**，客户端需以每频道 `server_seq` 的最大值为真相。

#### 响应

```json
{
  "channels": [
    {
      "id": 10086,
      "server_seq": 5275,
      "unread": 12,
      "messages": [ { "seq": 5231, ... }, ... ],
      "has_more": false
    }
  ]
}
```

| 字段 | 含义 |
|------|------|
| `server_seq` | 服务端该频道当前最大 seq（即 `channels.last_seq`） |
| `unread` | 未读数 = `(server_seq - last_read_seq) - (phantom_count - phantom_at_read)`，非负 |
| `messages` | **按 seq 升序**的消息数组；可能为空 |
| `has_more` | `true` 表示 `client_seq` 与 `server_seq` 之间还有未返回消息（gap 过大走 fast-forward），客户端应主动用 `GET /channels/:id/messages?after_seq=X` 分页追赶 |

**返回规则（对齐 `service/sync.go:Sync`）：**
1. `client_seq >= server_seq` → 该频道从响应中**省略**（不出现在 `channels` 数组里）
2. `server_seq - client_seq <= 100` → 返回全部缺失消息，`has_more=false`
3. `gap > 100` or 新频道（`client_seq=0`）→ 返回最后 50 条 + `has_more=true`
4. 用户已退出的频道 → 响应中不出现（由 `GetMemberChannelSeqs` 自然过滤）

#### Go 结构体（服务端权威定义）

```go
// internal/service/sync.go (已存在，补注释锁定)
type SyncCursor struct {
    ID  int64 `json:"id"`
    Seq int64 `json:"seq"`
}
type SyncParams struct {
    Cursors []SyncCursor `json:"channels"`
}
type SyncChannelDelta struct {
    ID        int64         `json:"id"`
    ServerSeq int64         `json:"server_seq"`
    Unread    int64         `json:"unread"`
    Messages  []repo.Message `json:"messages,omitempty"`
    HasMore   bool          `json:"has_more,omitempty"`
}
type SyncResult struct {
    Channels []SyncChannelDelta `json:"channels"`
}
```

#### 边界常量（`internal/service/sync.go`）

```go
const (
    SyncGapThreshold     = 100   // 小 gap 上限，小于等于返回全量
    SyncMsgLimit         = 50    // 大 gap / 新频道返回最近 N 条
    MaxChannelsPerCall   = 500   // 单次请求频道上限（新增，M1 落地）
)
```

#### 错误码

| HTTP | body | 场景 |
|------|------|------|
| 400 | `{error:"invalid JSON"}` | 请求体解析失败 |
| 400 | `{error:"too many channels"}` | 频道数超过 500 |
| 401 | （gin JWT 中间件标准响应） | 无 token 或过期 |
| 500 | `{error:"internal error"}` | 服务端异常 |

#### 客户端使用模式

```
1. 首次登录          → POST /sync with seqs all 0
2. 每次 ping 回来    → 从 pong.channel_seqs 知道哪些频道有 delta
3. 收到 delta 通知   → POST /sync with 这些频道的当前 client_seq
4. has_more=true     → 循环 GET /channels/:id/messages?after_seq=last_returned_seq 到追平
```

### 3.4 🚫 外部服务直连（im 不拥有，前端/调用方直接命中 Java / 独立服务）

| 模块 | Mattermost 端点 | 承担方 | im/server 边界 |
|------|---------------|-------|----------------|
| **搜索** | `/Im/search/*`、`/search/(post/user/channel/do)` | **Java 搜索服务** | im 不提供 `/api/search`；前端保留对 Java 的直连；im 侧已有的 `internal/service/search.go` + `GET /api/search` 要**撤销或仅作为内部简易检索保留**（不作为生产搜索路径） |
| **投票** | `/vote/*` (5 端点) | **Java 投票服务** | 不实现，列入 TODO |
| **文件上传分片 / 断点续传** | — | 独立对象存储服务 | im 只保留 `POST /api/files` 作元数据登记 + `GET /files/:id`、`GET /messages/:id/attachments` |

> **影响 M1/M2 范围：** 搜索从 `已覆盖` 列表撤下；`internal/http/search.go` 和 `internal/service/search.go` 两个文件**暂不删**，但不纳入后续功能迭代；文档和前端迁移不再承诺搜索由 im 提供。

### 3.5 ⚠️ 故意裁掉 — bitmap / segment 整条链路

| 端点 | 状态 |
|------|------|
| `/post/segment`、`/post/segment/batch` | **弃用**，改为 `GET /messages?around_seq=` |
| `/posts/segment/daily/metadata/(load/ack/batchAck)` | **弃用** |
| `/post/segment/config` | **弃用** |
| `/channel/bitmap` | **弃用** |

**按日期跳段回看（合规审计/业务复盘）必须实现以下端点，替代 bitmap 跳段能力：**
```
GET /api/channels/:id/messages/around?timestamp=<ms>&limit=50
```
实现要点：
- 索引 `(channel_id, created_at)`，用 `WHERE created_at <= $ts ORDER BY created_at DESC LIMIT limit/2` 拿左半边
- 再用 `WHERE created_at > $ts ORDER BY created_at ASC LIMIT limit/2` 拿右半边
- 合并返回，附带 `has_older / has_newer` 标志给客户端判断是否还可以继续拉
- 对应 Service：`MessageService.FetchAroundTimestamp(ctx, chID, uid, ts, limit)`
- 对应 Handler：`message.go` 新增 `authed.GET("/channels/:id/messages/around", ...)`

**列入 M1 交付范围**（见 §六）。

---

## 四、数据模型要点

### 4.1 seq 生成与原子分配

消息表必须保证 `(channel_id, seq)` 单调递增、gap-less（没有空洞）。

**方案：** Channel 计数器 `channels.last_seq`，用 `UPDATE ... RETURNING` 在同一事务里完成。

**硬性约束（Repo 层唯一入口）：**

```go
// internal/repo/message.go
// AllocSeqAndInsert 是唯一获取 seq 并写消息的入口。
// 禁止别处自己先 UPDATE channels 再 INSERT messages —— 两步分离会在崩溃窗口产生空洞。
//
// 事务复用约定（重要）：
//   - tx != nil  → 复用外部事务（Service 层需要在更大事务里同时做其他写操作时用）
//   - tx == nil  → 内部开新事务（最常见，独立发消息路径）
//
// 两种模式下 UPDATE channels + INSERT messages 的原子性都保证。
func (r *MessageRepo) AllocSeqAndInsert(
    ctx context.Context,
    tx *gorm.DB,   // 可选外部事务：tx != nil 时复用，tx == nil 时内部开新事务
    msg *Message,
) (int64, error) {
    run := func(db *gorm.DB) error {
        var nextSeq int64
        // 1) 行锁 + 自增：PG 下 UPDATE...RETURNING 是原子的；无需 SELECT FOR UPDATE
        if err := db.WithContext(ctx).Raw(`
            UPDATE channels
               SET last_seq = last_seq + 1
             WHERE id = ?
         RETURNING last_seq
        `, msg.ChannelID).Scan(&nextSeq).Error; err != nil {
            return fmt.Errorf("alloc seq: %w", err)
        }
        msg.Seq = nextSeq

        // 2) 同事务插入消息。任一步失败整体回滚，last_seq 不会被"吃掉"。
        if err := db.WithContext(ctx).Create(msg).Error; err != nil {
            return fmt.Errorf("insert message: %w", err)
        }
        return nil
    }

    if tx != nil {
        // 复用外部事务：不自己 Begin/Commit，交给外层控制
        if err := run(tx); err != nil {
            return 0, err
        }
        return msg.Seq, nil
    }
    // 无外部事务：内部开新事务
    err := r.db.Transaction(func(newTx *gorm.DB) error { return run(newTx) })
    if err != nil {
        return 0, err
    }
    return msg.Seq, nil
}
```

**Service 层使用示例：**

```go
// 场景 A：独立发消息（最常见）
seq, err := r.messages.AllocSeqAndInsert(ctx, nil, msg)

// 场景 B：发消息 + 更新线程计数 + ack 文件附件，需在同一事务
err := r.db.Transaction(func(tx *gorm.DB) error {
    seq, err := r.messages.AllocSeqAndInsert(ctx, tx, msg)
    if err != nil {
        return err
    }
    if err := r.threads.IncrReplyCount(ctx, tx, msg.ReplyTo); err != nil {
        return err
    }
    if err := r.files.MarkConsumed(ctx, tx, msg.FileIDs); err != nil {
        return err
    }
    return nil
})
```

**并发正确性：**
- `UPDATE...RETURNING` 在 PG 下会自动加行锁，同一 channel 的并发 INSERT 排队串行化
- 跨 channel 无竞争（行锁粒度 = 单 channel 一行）
- 崩溃恢复：事务回滚使 `last_seq` 回退，不产生空洞
- `send_ack` 返回的 seq 必须来自此函数的返回值，禁止 Service 层另行读取

**Service 层调用约定：**

```go
// service/message.go: SendMessage → MessageRepo.AllocSeqAndInsert
// 禁止 service/xxx.go 出现任何 UPDATE channels SET last_seq 语句（CI 加 grep 检查）
```

**性能：** 行锁粒度为单 channel 单行，极大群（如 1 万人群）单频道 QPS 理论上限 ≈ 5k msg/s（PG 本地事务 ~0.2ms）。超出需走 §七「写路径加速」方案（Pulsar 异步持久化 + Redis 预分配 seq）。

### 4.2 已读模型

```sql
channel_members (
  channel_id, user_id,
  last_read_seq     BIGINT,   -- 已读到的 seq
  phantom_count     BIGINT,   -- 频道内不对该用户可见的消息数
  phantom_at_read   BIGINT,   -- last_read_seq 时刻的 phantom_count 快照
  ...
)
```

`unread = (server_seq - last_read_seq) - (phantom_count - phantom_at_read)`

**phantom 机制**：定向消息（`visible_to`）对非目标用户保存为 `MsgTypePhantom`，保证 seq 连续不跳号，同时 unread 计算时扣除。

### 4.3 文件附件

`files` 表 + `messages.file_ids` 外键数组。文件上传返回 file_id，发消息时带上，附件消费读 `GET /messages/:id/attachments`。

---

## 五、跨 Pod 推送闭环（M1 P0，集群水平扩容前置条件）

> **定位：** 这是生产集群模式的核心前置。未完成前，多 Pod 部署会出现「用户连到 Pod A，消息从 Pod B 发出 → 推送失败」的致命问题。**必须在 M1 第一周交付最小闭环**。

### 5.1 现状与目标

**现状**：`cmd/gateway/main.go:255-305` 的 4 个 Pusher（message/readSync/friend/channel）**全部只调 `hub.PushToUser`**，没有一个查 `routing.Lookup` 或 `pulsarClient.Produce`。跨 Pod 能力从 0 开工。

**目标**：用户在 Pod A 连接，消息在 Pod B 产生 → 实时推送到 A；Pod 扩缩容 / 被 kill / 假死 / OOM 重启时，消息不丢，最多 **短暂重试窗口**。

### 5.2 `crossPodPush` 统一工具函数（M1 第一周必须落地）

抽公用函数替换 4 处直调，所有跨 Pod 场景走同一套代码：

```go
// internal/gateway/cross_pod_push.go (新增)
// crossPodPush 把 (userID, type, payload) 投递到用户所有在线 Pod。
// 语义：尽力而为 + 幂等 + 失败可重试，不保证严格有序。
func (h *Hub) crossPodPush(
    ctx context.Context,
    userID int64,
    msgType WSMessageType,
    payload any,
    routing *Routing,
    producer *pulsar.Producer,
    gatewayID string,
    log *slog.Logger,
) {
    pushID := generatePushID(msgType, payload)   // 见 §5.4 幂等设计

    // 1) 本 Pod 命中：直接投递（不走 Pulsar，省一跳）
    if sent := h.PushToUser(userID, msgType, payload); sent > 0 {
        return
    }

    // 2) 查 Redis routing，拿用户所有在线 gateway
    targets, err := routing.Lookup(ctx, userID)
    if err != nil {
        log.Warn("routing lookup failed, falling back to offline path",
            "user_id", userID, "error", err)
        markOffline(userID, payload)   // 离线落库（未来接 APNs/FCM）
        return
    }
    if len(targets) == 0 {
        markOffline(userID, payload)
        return
    }

    // 3) 对每个 Pod 发 Pulsar（跳过本 Pod，本 Pod 已在步骤 1 处理）
    for _, gwID := range targets {
        if gwID == gatewayID {
            continue
        }
        topic := pushTopicFor(gwID)  // 环境感知：见 §5.6 命名规则
        producer, err := producerCache.GetOrCreate(ctx, topic)
        if err != nil {
            log.Error("pulsar producer create failed",
                "topic", topic, "error", err)
            continue
        }
        event := buildPulsarEvent(pushID, userID, msgType, payload)
        if _, err := producer.Send(ctx, &pulsar.ProducerMessage{
            Payload: event,
            Key:     fmt.Sprintf("%d", userID),  // 同用户消息到同分区，保证 per-user 顺序
        }); err != nil {
            log.Error("pulsar send failed",
                "target_gw", gwID, "user_id", userID, "push_id", pushID, "error", err)
            // 不中断循环：其他 Pod 仍要试
        }
    }
}
```

**Producer 缓存设计（Pulsar Go SDK 按 topic 绑定，每个 target gateway 独立 producer）：**

```go
// internal/gateway/producer_cache.go
type ProducerCache struct {
    client  pulsar.Client
    cache   *lru.Cache[string, pulsar.Producer]  // topic → Producer
    ttl     time.Duration                         // 10 min，空闲自动淘汰
}

func (pc *ProducerCache) GetOrCreate(ctx context.Context, topic string) (pulsar.Producer, error) {
    if p, ok := pc.cache.Get(topic); ok {
        return p, nil
    }
    p, err := pc.client.CreateProducer(pulsar.ProducerOptions{
        Topic:           topic,
        SendTimeout:     5 * time.Second,
        MaxPendingMsgs:  10000,
    })
    if err != nil {
        return nil, err
    }
    pc.cache.Add(topic, p)  // 淘汰时自动 Close Producer
    return p, nil
}
```

**替换点（4 处）：**
- `hubMessagePusher.PushMessage` → `crossPodPush(TypePushMsg, ...)`
- `hubReadSyncer.PushReadSync` → `crossPodPush(TypeReadSync, ...)`
- `hubFriendEventPusher.PushFriendEvent` → `crossPodPush(TypeFriendEvent, ...)`
- `hubChannelEventPusher.PushChannelEvent` → `crossPodPush(TypeChannelEvent, ...)`

### 5.3 Pod 失效 / 假死 / Kill 的容错设计

这是本节最关键的部分。Pod 的故障模式分三类，每类都有特定处理。

#### 5.3.1 三种故障模式

| 模式 | 特征 | 对推送的影响 |
|------|------|-------------|
| **真 Kill** | SIGKILL / OOM / Node drain；TCP RST；无法继续运行 | 客户端连接立即断开，会重连到新 Pod |
| **优雅关闭** | SIGTERM → grace period；有机会清理 | 需要 Pod 主动 `Deregister` |
| **假死** | 进程在，GC 卡顿 / goroutine 饿死 / 网络 partition | 客户端连接不断，Pod 不响应；最难处理 |

#### 5.3.2 Routing 注册与心跳续期（修正 TTL）

```
TTL = 45s（心跳间隔 15s × 3 = 3 次心跳容错窗口）

生命周期：
  ① 连接建立          → routing.Register(userID, gatewayID, connID, ttl=45s)
  ② 每次 ping         → routing.Refresh(userID, gatewayID, connID, ttl=45s) [服务端代码在 ping handler 里]
  ③ 正常关闭          → routing.Deregister(userID, gatewayID, connID)
  ④ 30s 无 ping        → 服务端主动 close；routing.Deregister
  ⑤ Pod SIGTERM       → 所有连接遍历 Deregister；Sleep(grace period-2s) 等消费完 Pulsar 再退
  ⑥ Pod SIGKILL/假死  → 45s 后 Redis key 过期，消息自然不再路由到死 Pod
```

**Redis 数据结构：**
```
SET  routing:user:{userID}   {gatewayID1:connID1, gatewayID2:connID2, ...}
EXPIRE routing:user:{userID} 45
```

用 Redis Set + EXPIRE 实现。`Lookup` 返回去重的 gatewayID 列表。

**关键实现点：**
- `ping` handler 必须同时做两件事：(1) 更新 Conn.knownSeq；(2) 调 `routing.Refresh()` 续期
- `Refresh` 用 Lua 脚本原子化：`SADD` 成员 + `EXPIRE` key，避免 race
- 启动时 Pod 要先订阅 `msg.push.{gatewayID}`，**等订阅 ready 再接受 LB 流量**（liveness probe 控制）

#### 5.3.3 Pulsar 跨 Pod 推送失败场景

Pod 目标不在线 / Pulsar 投递失败 / 消费端未 ACK，三种情况分别处理：

```
场景 A：目标 Pod 已真死（45s TTL 过期前消息仍被路由过去）
  → Pulsar 消息堆积在 topic msg.push.{deadGwID}
  → 处理：topic 配置 TTL = 60s + `subscription expiry time = 10 min`
         dead Pod 对应的订阅 10 分钟未消费后自动清理
  → 堆积期间消息不丢（Pulsar 持久化），但也没人消费 → 期望客户端重连新 Pod 后通过 /sync 补齐

场景 B：Pulsar 生产失败（Pulsar 整体故障）
  → producer.Send 返回 err
  → 记日志 + metric `im.push.pulsar_error`
  → 不中断：crossPodPush 循环到下一个 target
  → 全部失败 → markOffline；客户端重连必然走 /sync，消息不会永久丢

场景 C：PushConsumer 消费但 client 未 ACK（客户端假死）
  → ackRegistry.await(pushID) 等 30s 超时
  → 超时后 conn.markInactive + Deregister
  → 客户端下次重连自然走 /sync 补
```

**总结原则：** 跨 Pod 推送只是「最佳情况下的实时通道」，**消息的权威来源始终是 PostgreSQL + seq**。任何跨 Pod 失败最终都由客户端的 `/api/sync` 兜底——这是本架构容灾的根本。

#### 5.3.4 gatewayID 的生成

避免 K8s Pod 名漂移 / 复用造成的 topic 残留：

```go
// cmd/gateway/main.go:run
gatewayID := os.Getenv("GATEWAY_ID")
if gatewayID == "" {
    gatewayID = "gw-" + uuid.New().String()  // 每次进程启动生成新 UUID
}
log.Info("gateway id", "id", gatewayID)
```

**禁止**用 pod name / pod IP 作 gatewayID——K8s 下两者都可能漂移或复用。UUID 保证每个进程生命周期内唯一、进程重启后换新 ID、旧 topic 自然由 TTL 回收。

#### 5.3.5 优雅关闭（SIGTERM 处理）

```go
// cmd/gateway/main.go
<-quit  // SIGTERM
log.Info("shutting down...")

// 步骤 1：停止接受新连接（HTTP server shutdown）
srv.Shutdown(shutdownCtx)

// 步骤 2：遍历当前所有 conn 主动 Deregister（释放 routing）
hub.ForEachConn(func(c *Conn) {
    routing.Deregister(c.UserID, gatewayID, c.ID)
    c.Close()
})

// 步骤 3：消费完 Pulsar 积压（pushConsumer 有 drain 方法，最多 5s）
pushConsumer.Drain(5 * time.Second)

// 步骤 4：关闭 Pulsar 订阅（Pulsar 侧标记订阅失活，topic 走 TTL 自然清理）
pulsarClient.Close()
```

**K8s 配置配套：**
```yaml
terminationGracePeriodSeconds: 30  # 给足优雅关闭时间
readinessProbe:
  httpGet: { path: /readyz, port: 8080 }  # 退出时 /readyz 返回 503，LB 立即摘掉
```

### 5.4 幂等与去重

**pushID 全局唯一**：
```
pushID = fmt.Sprintf("%s-%d-%d", msgTypeShort, targetUserID, seq)
```

客户端收到 `push_msg` 后发 `push_ack { push_id }`；服务端 `ackRegistry.resolve(pushID)` 去重。重复推送同一个 pushID 是**正确行为**（Pulsar at-least-once 语义），客户端凭 pushID 去重即可。

### 5.5 集群水平扩容能力验证（M1 退出标准）

启动 3 个 Gateway Pod（任意命名，UUID 会自动生成），按以下场景验证：

1. **基础跨 Pod**：用户 A 连 gw-1，B 连 gw-2 → A 发消息，B 必须实时收到
2. **Pod 假死模拟**：`kill -STOP` 暂停 gw-1 进程 → 45s 后 routing 里不再命中 gw-1；新消息路由到其他 Pod；`kill -CONT` 恢复后 gw-1 客户端自然重连
3. **Pod kill**：`kubectl delete pod gw-1 --force` → 45s 内客户端重连到 gw-2/gw-3；新消息通过 /sync 自动补齐
4. **Pulsar 故障模拟**：`docker stop pulsar` 30s → 期间消息走 markOffline；恢复后客户端 /sync 补齐；无数据丢失
5. **压测**：3 Pod 承接 150k 并发 WS、10k msg/s，投递 P99 <80ms

扩容到 N Pod 只需 `kubectl scale deploy/im-gateway --replicas=N`，无需任何代码 / 配置改动。

### 5.6 Pulsar topic 设计（环境感知命名）

**命名规则（写进 `config.Env` + `pushTopicFor`）：**

| 环境 | topic 名 | 作用 |
|------|---------|------|
| **prod** | `persistent://im/push/msg.push.{gatewayID}` | 生产，固定 namespace `im/push`（运维预先在 pulsar-admin 创建） |
| **pre**（预发） | `persistent://im/push-pre/msg.push.{gatewayID}` | 预发，固定 namespace `im/push-pre` |
| **local**（开发者本地） | `persistent://im/push-local/msg.push.{gatewayID}.{localname}` | **自动加 localname 后缀**避免多开发者本地调试互相串消息 |

**`pushTopicFor` 实现：**

```go
// internal/gateway/topic.go
func pushTopicFor(gatewayID string) string {
    switch cfg.Env {
    case "prod":
        return fmt.Sprintf("persistent://im/push/msg.push.%s", gatewayID)
    case "pre":
        return fmt.Sprintf("persistent://im/push-pre/msg.push.%s", gatewayID)
    default: // local / dev
        localname := os.Getenv("USER")
        if h, _ := os.Hostname(); h != "" && localname == "" {
            localname = h
        }
        if localname == "" {
            localname = "anon"
        }
        return fmt.Sprintf("persistent://im/push-local/msg.push.%s.%s", gatewayID, localname)
    }
}
```

**配置要求（`config.yaml`）：**

```yaml
env: prod  # prod | pre | local
pulsar:
  url: "pulsar://pulsar-prod.im:6650"
  # prod/pre 环境：namespace 预先由运维创建（一次性）
  #   pulsar-admin namespaces create im/push --retention-time 0 --retention-size 0
  #   pulsar-admin namespaces set-subscription-expiration-time im/push 10  # 分钟
  # local 环境：启动自动创建，不配置 retention（默认 60s 清）
```

**其他 topic：**

| Topic | 用途 | 订阅模式 | TTL |
|-------|------|---------|-----|
| `msg.push.dlq`（可选，P1） | 送达失败落地 | Shared，运维告警消费 | 7 天保留 |

**topic 生命周期：**
- `gatewayID` 是 UUID，每个进程启动自动新建；同一 Pod 重启 → 新 UUID → 新 topic
- Pod 退出时主动 `pulsarClient.Close()` 断订阅
- 生产的 namespace `im/push` 订阅 10 分钟无活跃消费自动清理
- 本地的 namespace `im/push-local` 空闲 60s 即清（避免开发机 topic 堆积）
- 运维每周跑 `pulsar-admin topics list persistent://im/push | wc -l`，异常增长时告警

---

## 六、替换 Mattermost 的执行路线（6 个里程碑，严格按此节奏推进）

### DAY 0：启动前夜（今晚必须完成的 3 件事）

> **目的：** 把"后续所有功能共用的基座"在 M1 第一行代码前先锁死。这三件做完才进 M1 主循环。
> **依赖关系：** 三件彼此无强依赖，可**并行开工**；但合并顺序推荐 #3 → #2 → #1（契约先定、数据底座次之、推送基座最后，因为 #1 体量最大）。
> **CC 协同节奏：** 建议一个 PR 一件事，今晚内三个 PR 全部 open + self-review，明早 M1 第一条"撤回 / 编辑"就能直接调 crossPodPush + AllocSeqAndInsert 开工。

#### 🔒 DAY 0 #1 — `crossPodPush` 骨架 + routing TTL 续期 + Pulsar producer 缓存（后续所有功能的推送基座）

**前置：** 无（现有 `gateway.Hub / Routing / PushConsumer / pulsarClient` 全部可复用）
**交付：** 1 个 PR，改 7 个文件
**工作量：** 2-3 小时

- [ ] **新建** `internal/gateway/producer_cache.go`（见 §5.2 设计）
  - `ProducerCache` 按 topic 缓存 `pulsar.Producer`（LRU + 10min idle TTL）
  - `GetOrCreate(ctx, topic) (pulsar.Producer, error)`
  - 淘汰回调：自动 `Close()` Producer
- [ ] **新建** `internal/gateway/topic.go`（见 §5.6 环境感知命名）
  - `pushTopicFor(gatewayID string) string` — 按 `cfg.Env` (prod/pre/local) 返回完整 topic 路径
  - local 环境自动加 `$USER` 或 `hostname` 后缀避免多开发者串消息
- [ ] **新建** `internal/gateway/cross_pod_push.go`
  - 对齐 §5.2 伪代码：本 Pod 命中短路 → routing.Lookup → 遍历 target Pod 走 `ProducerCache.GetOrCreate(pushTopicFor(gwID))` → `producer.Send` 带 `Key=userID` 保序
  - 全离线 → `markOffline(userID, msgType, pushID)` 占位（先空实现打日志，M2 接离线推送填）
- [ ] **改** `internal/gateway/routing.go`
  - 加 `Refresh(ctx, userID, gatewayID, connID) error`（Lua 脚本：`SADD routing:user:{userID} member` + `EXPIRE 45`）
  - 常量 `routingTTL = 45 * time.Second`；注释对齐 §5.3.2（心跳 15s × 3）
- [ ] **改** `internal/gateway/ws_handler.go` / `heartbeat.go`
  - ping handler 收到心跳后调 `routing.Refresh`（同步调用，失败记日志不中断）
- [ ] **改** `internal/gateway/conn.go`
  - 连接关闭时 `routing.Deregister`（若已有忽略）
- [ ] **改** `cmd/gateway/main.go`
  - `gatewayID` 改为启动时生成 UUID：`gatewayID := "gw-" + uuid.New().String()`（避 pod 名漂移，见 §5.3.4）
  - 实例化 `ProducerCache` 并注入到 4 个 Pusher
  - 4 个 Pusher（`hubMessagePusher / hubReadSyncer / hubFriendEventPusher / hubChannelEventPusher`）全部改走 `hub.crossPodPush(...)`，不再直接 `hub.PushToUser`

**本地验收：** `env=local` 起 2 个 gateway 进程（都自动带 `$USER` 后缀 topic），用户连 A 发消息，B 上对方用户实时收到；`go test ./internal/gateway -race` 通过。

**配置要求：** pre/prod 运维需预建 Pulsar namespace：
```bash
# 仅 prod/pre 各执行一次（一次性，DAY 0 #1 merge 前让运维跑）
pulsar-admin namespaces create im/push
pulsar-admin namespaces set-subscription-expiration-time im/push 10   # 分钟
pulsar-admin namespaces create im/push-pre
pulsar-admin namespaces set-subscription-expiration-time im/push-pre 10
```

#### 🔒 DAY 0 #2 — `AllocSeqAndInsert` Repo API 签名锁定（消息写路径基座）

**前置：** 无
**交付：** 1 个 PR，改 2 个文件（实现可延后到明天，但**签名今晚必须锁**）
**工作量：** 30 分钟（仅签名 + TODO 占位）

- [ ] **改** `internal/repo/message.go`
  - 加函数签名 + godoc（注释说明唯一入口 + 事务复用约定，对齐 §4.1）：
    ```go
    // AllocSeqAndInsert 是唯一获取 seq 并写消息的入口。
    // 同事务包 UPDATE channels.last_seq + INSERT messages，失败整体回滚；
    // 禁止别处自己先 UPDATE 再 INSERT —— 两步分离会在崩溃窗口产生 seq 空洞。
    //
    // 事务复用：
    //   - tx != nil  → 复用外部事务（Service 层要在更大事务里组合其他写操作）
    //   - tx == nil  → 内部开新事务（独立发消息路径，最常见）
    func (r *MessageRepo) AllocSeqAndInsert(
        ctx context.Context,
        tx *gorm.DB,
        msg *Message,
    ) (int64, error) {
        // TODO(DAY 0 #2 impl): 明天补实现，见 BACKEND.md §4.1
        panic("not implemented — see docs/BACKEND.md §4.1")
    }
    ```
- [ ] **新建** `internal/repo/message_test.go` 里的 TODO 测试骨架（带 `//go:build integration` tag + `t.Skip` 占位）
  - `TestAllocSeqAndInsert_Concurrent`（真 PG 下并发 100 goroutine 同频道发消息，断言 seq 严格递增 gap-less）
  - `TestAllocSeqAndInsert_RollbackOnInsertFail`（mock insert 失败，断言 last_seq 回滚）
  - `TestAllocSeqAndInsert_ReuseExternalTx`（外部 tx 与内部事务两路径都验证）
- [ ] **改** `internal/service/message.go` 的 `SendMessage`
  - 调用点预留 `TODO(DAY 0 #2): 改调 r.messages.AllocSeqAndInsert(ctx, nil, msg)` 注释（不动实现，避免破坏现有测试）
- [ ] **CI 检查草案**（可后续补到 Makefile）：
  - `grep -r "UPDATE channels.*last_seq" internal/service/` 必须返回空
  - `grep -r "UPDATE channels.*last_seq" internal/repo/` 只允许出现在 `message.go`

**验收：** `go build ./...` 通过；签名 + godoc + test 占位 merge 即可。

#### 🔒 DAY 0 #3 — `POST /api/sync` 契约锁定（前后端 + Rust 并进基准）

**前置：** 无（BACKEND §3.3 已写完整契约，此项是把契约落到代码注释 + 边界保护）
**交付：** 1 个后端 PR，改 2 个文件
**工作量：** 30 分钟

- [ ] **改** `internal/service/sync.go`
  - 在 `SyncParams / SyncResult / SyncChannelDelta` 顶部加 godoc，写明"**contract locked, 对齐 BACKEND §3.3，改动前先改文档并广播前端 + Rust**"
  - 加常量 `MaxChannelsPerCall = 500`（http handler 里做 guard）
- [ ] **改** `internal/http/sync.go`
  - `authed.POST("/sync", ...)` handler 里加 `if len(in.Channels) > service.MaxChannelsPerCall → 400 {error:"too many channels"}`

**跨 repo TS 类型同步策略（本次决策）：**

> **前端 TS 类型不通过文件拷贝同步。** 前端 owner 在 `cses-client` 开新分支，**直接按 BACKEND §3.3 的 Go struct 和 JSON tag 手写 TS 接口** （Angular/Tauri 项目标准做法，不需要生成工具）。
>
> 变更流程：
> 1. 后端改 `internal/service/sync.go` 的 struct/tag 前 → 先改 BACKEND §3.3 + 发 Slack 通知前端
> 2. 前端在 cses-client 新分支对齐改动 TS 类型
> 3. 两侧同步 merge，减少契约漂移窗口

**前端侧（cses-client 新分支）手写 TS 参考：**

```typescript
// cses-client 分支：src/app/core/im-api/sync.types.ts
// 严格对齐 im/server docs/BACKEND.md §3.3 —— Go struct/JSON tag 为权威源
export interface SyncCursor {
  id: number;
  seq: number;
}
export interface SyncRequest {
  channels: SyncCursor[];
}
export interface SyncChannelDelta {
  id: number;
  server_seq: number;
  unread: number;
  messages?: Message[];      // Message 定义对齐 im repo.Message
  has_more?: boolean;
}
export interface SyncResponse {
  channels: SyncChannelDelta[];
}
export const MAX_CHANNELS_PER_CALL = 500;
```

**验收：** `go test ./internal/http -run TestSync -race` 通过（现有测试不破）；前端 owner 在 cses-client 分支 push 对应 TS 类型文件，两侧 diff 人工 review 一遍。

#### DAY 0 合并顺序 & 协同

```
今晚并行开三个 PR：
  PR-A: DAY 0 #3 (契约锁定)    ← 最快可合，半小时
  PR-B: DAY 0 #2 (seq 签名)    ← 30 分钟
  PR-C: DAY 0 #1 (crossPodPush) ← 1-2 小时

合并顺序建议：
  1. 先合 PR-A （解锁前端 / Rust 的并进，契约定）
  2. 再合 PR-B （解锁 M1 所有发消息相关功能的 Repo 调用点）
  3. 最后合 PR-C （体量最大，独立审查；合并后 M1 所有功能默认享受跨 Pod 推送）

明早 M1 第一条"消息撤回"开工时：
  - Service 层调 r.AllocSeqAndInsert（虽然撤回不生成新 seq，但撤回 WS 事件走 crossPodPush(TypeMsgDeleted)）
  - Handler 参考 BACKEND.md §3.2 P0 清单
  - 前端 / Rust 对齐 TS 契约开工
```

---

### M1：补齐 P0 聊天核心 + 跨 Pod 集群能力（3 周）
**主题：让单次部署可以水平扩容到 N Pod**

- [ ] **跨 Pod Pulsar 推送闭环**（见 §5.3 清单，M1 核心前置）
- [ ] **集群水平扩容验证**（见 §5.4，3 Pod 压测）
- [ ] 消息撤回 `DELETE /api/messages/:id` + WS `msg_deleted` 事件
- [ ] 消息编辑 `PATCH /api/messages/:id` + WS `msg_updated` 事件
- [ ] 线程回复查询 `GET /api/messages/:id/replies`（send 已支持 `reply_to`）
- [ ] 已读列表 `GET /api/messages/:id/readers`（按 `last_read_seq ≥ msg.seq` 反查成员）
- [ ] **按时间跳段回看** `GET /api/channels/:id/messages/around?timestamp=<ms>&limit=50`（见 §3.3）
- [ ] 单元 + 集成测试覆盖 80%
- [ ] gateway 压测退出标准：单 Pod 50k 并发 WS，集群 150k；10k msg/s，投递 P99 <80ms

### M2：P1 企业协作（4 周）
**主题：把 Mattermost 独占的协作能力在 im 这侧落地**

- [ ] 公告 `/api/announcements/*`（6 端点：save/read/list/acceptList/delete/detail）
- [ ] 紧急消息 `/api/messages/urgent/*`（发送 / 确认 / 取消）
- [ ] 审批 `/api/approvals`
- [ ] 通知 `/api/notifications`（loadSend + loadTarget）
- [ ] 定时消息 `/api/messages/scheduled`（创建 / 取消 / 列表）
- [ ] 快捷回复 `POST /api/messages/:id/quick-reply`
- [ ] 频道精细化管理：`PATCH /api/channels/:id` + `/managers/:uid`、`/pins/:msgID` 子资源
- [ ] 成员属性修改：`PATCH /api/channels/:id/members/:user_id`（role / notify）
- [ ] OTel trace 全链路贯通（gateway → service → repo → pulsar）

> ⚠️ **范围说明（本项目不拥有）：**
> - 文件上传分片 / 断点续传 → 由独立对象存储服务负责，im 侧只保留 `POST /api/files` 作为元数据登记入口
> - 搜索引擎高级能力（全文 / 近义 / 排序调权）→ 依赖独立 Search 服务
> - 以上均 **不纳入 M2 验收**

### M3：P2 辅助 + 前端主体切换（3 周，与 M2 末端并行）
- [ ] 模板消息 `/api/messages/:id/template-received`
- [ ] 组织信息 `/api/modules`、`/api/groups`、`/api/teams/*`
- [ ] 前端 F1 + F2 + F3 同步推进（见 `FRONTEND.md`）
- [ ] **TODO（不在 Go 侧实现）：** 投票 `/vote/*` 走 Java 远程调用；im 只负责鉴权头透传

### M4：Bot 生态 + 数据迁移准备（3 周）
- [ ] AI Agent `/api/agents/*`（独立 subservice）
- [ ] Bot 管理 `/api/bots/*`
- [ ] Webhook `/api/webhooks/*`
- [ ] **数据迁移 Dry-run：**
  - Mattermost PostgreSQL → im Schema 映射 ETL 脚本
  - `Posts` → `messages`（按 channel 排序分配 seq）
  - `ChannelMembers.MsgCount/LastViewedAt` → `last_read_seq`
  - `Channels.TotalMsgCount` → `last_seq`
  - `Users` 保留 ID 映射表
- [ ] 影子双写开关（业务层发消息同时写新旧两侧）
- [ ] 数据校验脚本（消息数 / 未读数 / 已读 seq 对齐）

### M5：全量切换（2 周，**不灰度、不回切**）
- [ ] 切流前 3 天：staging 环境全量模拟（真实数据 Snapshot + 真实客户端行为回放），无重大问题才进正式切
- [ ] 切流当天低峰窗口：Mattermost 写冻结 → 增量 ETL 追补 → 前端 baseUrl 全量切 im
- [ ] 切流后 48-72 小时：错误率、P99 延迟、连接数、未读数抽样持续观察
- [ ] 监控面板：im 侧（集群 N Pod 负载均衡曲线、跨 Pod 推送成功率、/sync QPS + P99）+ Mattermost 侧（应无流量）
- [ ] hotfix 通道就绪：问题在 im 侧热修，**不回切到 Mattermost**（理由见 `OVERALL.md §三` 切流策略）

### M6：下线 Mattermost（1 周）
- [ ] 前端彻底移除 `/api/cses/*` 调用代码（除 `/vote/*` TODO 保留）
- [ ] 旧 Mattermost 进程停服、数据归档
- [ ] 清理 `baseUrl` 中的 `mattermostHttp` 配置项
- [ ] 删除 `bitmap`、`postSegmentConfig` 等旧表
- [ ] im/server 删除 legacy mux（`cmd/gateway/main.go` 的 `Legacy:` 字段）

---

> **今晚验证收敛到一条命令：** `make verify-all`（6 步、90 分钟跑完，见 `OVERALL.md §5.1`）。全绿 = 今晚 GO、可推 staging。压测 / 数据迁移 / 业务 KPI 等长尾验证放到 staging 和生产后。下表是生产 P99/容量目标，**不在今晚 `make verify-all` 范围**。

## 七、性能基线（TG 水平对标）

| 维度 | TG 公开数据 | im 目标 |
|------|------------|--------|
| 单 DC 并发连接 | 数百万 | 单 Pod 50k，集群 500k |
| 消息投递 P99 | <200ms（跨 DC） | <50ms（同 DC） |
| 协议帧大小 | MTProto ~ 150 B min | JSON 当前；可选 MessagePack 降到 ~ 60% |
| 心跳频率 | 75s | 15s（客户端已定） |
| 存储分片 | 按 chat_id 哈希 | 按 channel_id 哈希（Postgres 多租户 / Citus） |

**提速清单：**
1. WS 帧改 MessagePack（可选，保留 JSON 兼容开关）
2. `messages.content` 大字段压缩（gzip by gateway before persist，或 PG toast 压缩）
3. 读路径：`GetMemberChannelSeqs` 加 Redis 缓存（TTL 30s + invalidate on SendMessage）
4. 写路径：`SendMessage` 走 Pulsar 异步持久化 + 立即返回 seq（需要 channel 的 last_seq 从 Redis 取 + 乐观更新）
5. 冷消息分层：>30 天迁移到 S3/低成本存储（对齐 Mattermost 的 tiered storage）

---

## 八、接口风格守则

写新 endpoint 时遵循：
1. **路径即资源**：`/api/channels/:id/messages`，不用 `/posts/createPost`
2. **方法即动作**：POST 创建、GET 读取、PUT 全量改、PATCH 部分改、DELETE 删
3. **响应即实体**：成功返回 entity JSON；错误返回 `{error: "..."}`；不用 `{success, data, message}` 三字段封装
4. **路由注册用 `RegisterXxxRoutes(authed *gin.RouterGroup, svc *service.XxxService, ...)` 格式**，hook 通过可选 struct 传入（见 `MessageRouteOpts`）
5. **Service 用 consumer-side interface** 定义依赖（小接口 1-3 个方法）
6. **错误用 `errors.Is` 语义映射到 HTTP 状态码**：`ErrNotMember → 403`、`ErrNotFound → 404`、其他 → 500

---

## 九、下一步行动

### 今晚（DAY 0 — 严格按 §六 DAY 0 清单落地）
1. **PR-A** 锁 `POST /api/sync` 契约（`internal/service/sync.go` godoc + `MaxChannelsPerCall`；前端在 cses-client 新分支按 §3.3 手写 TS）
2. **PR-B** 锁 `AllocSeqAndInsert` Repo 签名（`ctx, tx *gorm.DB, msg` 三参）+ 测试骨架（`internal/repo/message.go` + `message_test.go`）
3. **PR-C** `crossPodPush` + routing TTL 续期 + ProducerCache + pushTopicFor（`cross_pod_push.go` + `producer_cache.go` + `topic.go` + `routing.go` + `heartbeat.go` + `main.go`）

**合并顺序：** A → B → C；明早 M1 第一条"消息撤回"可直接调用三者。

**PR-C merge 前的运维动作：** 需要 SRE 在 Pulsar 上建 namespace `im/push` 和 `im/push-pre`（见 §六 DAY 0 #1 脚本）。本地调试不需要，local 命名空间启动时自动创建。

### 本周
- 全面启动 M1 主循环：撤回 / 编辑 / 线程 / 已读列表 / 按时间跳段 5 个端点并行开工
- 前端 F0：在 cses-client 新分支按 §3.3 手写 TS 类型 + 搭 `ImApiAdapter` 骨架
- Rust 侧：`ImHttpClient.sync()` 用同一份 struct 开始实现

### 持续
- 沉淀 `docs/api.md`（OpenAPI spec，供 `apifox-import` skill 自动同步至 Apifox）
- M1 压测脚本按 §5.5 五个场景逐项通过

## 十一、OTel Trace 覆盖清单（M2-H 交付）

所有业务关键路径现已带 span 埋点，可在 Jaeger/Tempo 查询端到端耗时。

### Service 层（tracer = `im-server/service`）
- MessageService：所有导出方法
- ChannelService + ChannelGovernanceService：所有导出方法
- AuthService / ProfileService / SettingsService / FriendService / FavoriteService / SearchService / FileService / SyncService
- M2 新增：AnnouncementService / UrgentService / ApprovalService / NotificationService / ScheduledService / QuickReplyService

### Repo 层（tracer = `im-server/repo`）
- MessageRepo：AllocSeqAndInsert / SoftDelete / UpdateContent / FetchForUser / FetchAfter / FetchAround 等热点方法

### Gateway 层（tracer = `im-server/gateway`）
- CrossPodPush.Send：每次跨 Pod Pulsar 发送记录 target.gateway / push.topic / push.type / target.user_id 属性；失败时 span.SetStatus(Error) + RecordError

### 未覆盖（故意留空）
- HTTP middleware 层：otelgin 已自动埋点（cmd/gateway/main.go）
- Pulsar PushConsumer 消费侧：OTel Pulsar-go SDK 未广泛支持 context 传播，M3 再补
- Worker（ScheduledWorker）：goroutine 启动处可加 root span，本次未做

### 查询示例（Jaeger UI）
- `service="im-server/service" operation="MessageService.SendMessage"`
- `service="im-server/gateway" operation="CrossPodPush.Send" target.user_id=<uid>`

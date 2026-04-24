# im/server — 技术蓝图 · 技术选型 · 压测指标 · Router 全流程

> 本文基于 gitnexus 索引（`im` 仓库 @ `60ddd3f`，230 files / 1908 symbols / 5341 edges / 86 processes）
> 和 `server/` 源码的静态分析撰写。与 `BACKEND.md`（改造纵深）/`OVERALL.md`（替换路线）/`FRONTEND.md`（客户端侧）互为补充：
> - **本文负责：** 技术栈 × 选型理由 × SLO × 从 router 入口逐层追踪的功能流程。
> - **不重复：** 替换 Mattermost 的里程碑、数据迁移、前端切换（在另外三篇）。

目录：
1. [项目定位](#一项目定位)
2. [进程拓扑](#二进程拓扑)
3. [技术栈与选型理由](#三技术栈与选型理由)
4. [配置清单](#四配置清单)
5. [Router 全景与功能流程](#五router-全景与功能流程)
6. [WebSocket 实时链路](#六websocket-实时链路)
7. [数据模型与存储](#七数据模型与存储)
8. [可观测性](#八可观测性)
9. [性能压测必达指标](#九性能压测必达指标)
10. [测试与质量门](#十测试与质量门)
11. [部署形态（K8s）](#十一部署形态k8s)

---

## 一、项目定位

im/server 是一个**借鉴 Telegram 思想**（单调 seq + 心跳差分 + 无状态服务）的高并发 IM 后端，目标是**整体替换旧 Mattermost (csesapi + web_hub)**。核心命题：

- **协议窄化**：对外只有 10 种 WS 帧（`ping/pong/send/send_ack/push_msg/push_ack/sync/sync_resp/read_sync/friend_event/channel_event`），远少于 Mattermost 70+。
- **状态最少化**：服务端不持有会话级状态（除 Hub 中的 WS 连接句柄），所有可恢复状态都在 Postgres/Redis；Pod 宕机客户端 1 次 `/api/sync` 就能追平。
- **分层极简**：HTTP → Service → Repo（3 层），相较 Mattermost 的 Handler→App→Store interface→SQLStore 减少一层抽象。

---

## 二、进程拓扑

仓库内 3 个可编译 binary（`server/cmd/`）：

```
┌────────────────────────────────────────────────────────────┐
│  Client (Angular + Tauri/Rust)                             │
└──────────────┬─────────────────────────┬───────────────────┘
               │ HTTP + WS               │
┌──────────────▼─────────────────────────▼───────────────────┐
│  cmd/gateway   —— gin + gorilla/ws                         │
│  · /api/*  REST   · /ws  WebSocket  · /healthz /readyz     │
│  · Hub(本地连接)  · PushConsumer(订阅 msg.push.{gwID})     │
└──────┬────────────────┬──────────────────────┬─────────────┘
       │ GORM            │ Pulsar producer      │ Redis HSET
       │                 │  msg.incoming        │ user:connections:{uid}
       ▼                 ▼                      ▼
┌────────────┐   ┌────────────────┐    ┌──────────────────┐
│ PostgreSQL │   │ Apache Pulsar  │◀───│ cmd/message       │
│  11 tables │   │ msg.incoming   │    │ Pulsar consumer +│
│  (init.sql)│   │ msg.push.{gw}  │───▶│ push fan-out     │
└────────────┘   │ msg.deliver.{gw}│    └──────────────────┘
                 └────────────────┘
                                        ┌──────────────────┐
                                        │ cmd/sync         │
                                        │ (占位，Plan 7)   │
                                        └──────────────────┘
```

- **gateway**（`cmd/gateway/main.go`）：对外唯一进程，承担 HTTP API + WebSocket。由 `imhttp.New` 构造 Gin engine，`http.NewServeMux` 只保留 `/ws`；未命中 Gin 的请求通过 `engine.NoRoute` 转给 mux（strangler 收尾阶段，当前已完全切到 Gin，只剩 `/ws`）。
- **message**（`cmd/message/main.go`）：Pulsar 消费者，订阅 `msg.incoming`，负责**跨 Pod 推送扇出**。为每位在线成员按其 `GatewayIDsForUser()` 查询结果把 `PulsarPushEvent` 发布到 `msg.push.{gwID}`。
- **sync**（`cmd/sync/main.go`）：空壳，`TODO: Plan 7`。真正同步逻辑目前在 gateway 的 `/api/sync` 同步调用 `service.SyncService`。

> 当前 `cmd/gateway/main.go` 的 `WsHandler.WithSendSupport()` 使 WS send 直接在 gateway 内完成持久化 + 扇出（`ws.send` 分支），**没有走 Pulsar**。Pulsar `msg.incoming` 路径当前只有 message 进程启动时会消费，但 gateway 不产它 —— 这是 T1「跨 Pod Pulsar 闭环」要补的（见 OVERALL §三）。

---

## 三、技术栈与选型理由

### 3.1 运行时 & 语言

| 组件 | 版本 | 选型理由 |
|------|------|----------|
| Go | 1.26.2 | 1.22+ 的 `net/http` 新路由语法 `"GET /ws"`（见 `cmd/gateway/main.go:125`）；runtime/trace 与 OTel 联动成熟；`sync.RWMutex`/chan 足够撑 50k 连接/Pod。 |
| Gin | 1.11.0 | 群组路由 + 高性能（基于 radix tree）；OTel 中间件 `otelgin` 开箱即用；比 chi/echo 社区活跃度更高。 |
| gorilla/websocket | 1.5.3 | 生态最成熟，支持 ping/pong frame 控制；`ReadBufferSize=1024, WriteBufferSize=4096`（`ws_handler.go:24-29`）。 |
| GORM | 1.30.1 + postgres 1.6.0 | `PrepareStmt=true` 减少规划开销；`gorm.io/plugin/opentelemetry` 自动把 SQL 挂到当前 span；repo 层统一接口便于 mock 测试。 |
| lib/pq | 1.10.9 | 只为 `pq.Int64Array` 支持 `BIGINT[]`（`visible_to` 字段），与 GORM 并存。 |
| Redis (go-redis v9) | 9.18.0 | 仅用作路由表（`user:connections:{uid}` HASH，TTL 2h），不作缓存；pipeline 模式一次写完 HSET+Expire。 |
| Pulsar (pulsar-client-go) | 0.18.0 | 多租户、按 topic 分区、分层存储；跨 Pod 推送天然契合 topic-per-pod（`msg.push.{gatewayID}`）；比 Kafka 的 subscription 模型更灵活（shared/key_shared/exclusive）。 |
| golang-jwt/jwt/v5 | 5.3.1 | HS256 + 32-byte secret；WS 握手支持 `?token=` query，REST 走 `Authorization: Bearer`。 |
| OpenTelemetry | 1.40.0 | OTLP/gRPC 上报；`runtime` instrumentation 给 Go GC/goroutine/mem；厂商中立，可接 Jaeger/Tempo。 |

### 3.2 选型的 5 个关键取舍

| 决策 | 取舍 | 理由 |
|------|------|------|
| **Gin + legacy mux 同居** | 未选择重写全部路由 | strangler-fig 迁移期间 `engine.NoRoute` 兜底 legacy 路由，逐阶段替换；到 Phase 7.8 全部迁到 Gin，legacy mux 只剩 `/ws`。 |
| **GORM 而非 pgx/sqlc** | 开发速度 > 单条 SQL 极致性能 | `PrepareStmt` 基本抹平了差距；复杂 SQL（如 `FetchAround`）仍用 `db.Raw` 写原生。 |
| **Redis 只做路由** | 不做缓存层 | IM 读写比约 1:1，缓存命中率低；PostgreSQL 索引+`PrepareStmt` 足够；减少一致性复杂度。 |
| **Pulsar 而非 Kafka** | 多租户 + topic 分区更灵活 | 每个 gateway Pod 独占一个 `msg.push.{gwID}` topic，扩容时新 Pod 自己订阅自己的 topic 即可；Kafka 做不到动态 topic。 |
| **单 Hub map，未做分片** | KISS，50k conn/Pod 在压测内未见锁瓶颈 | `RWMutex` 读多写少；`ConnsForUser` 只读锁拷贝切片；必要时可升级为分片 Hub（`runtime.NumCPU()` 个桶 + 一致性哈希，Mattermost 方案）。 |

### 3.3 Dev / Build / Ops 工具链

- **Makefile**：在 `server/Makefile`，含 `build / test / lint / run` 目标。
- **容器**：`docker-compose.yml` 提供 PG/Redis/Pulsar/Jaeger/Grafana 开发环境。
- **测试容器**：`testcontainers-go` + modules/postgres + modules/redis，集成测试 (`tests/integration/*_test.go`) 真起 PG/Redis。
- **覆盖率**：`go test -race -cover ./...`，目标 ≥ 80%（common/testing.md）。

---

## 四、配置清单

`config.example.yaml` 完整键位：

```yaml
pg:       dsn, max_conns           # repo.Open 默认 MaxIdle=5, ConnMaxLifetime=30m
redis:    addr, password, db
pulsar:   url
gateway:  http_addr, jwt_secret    # ≥ 32 bytes；空串直接 return 1
          upload_dir               # 文件服务落盘根目录
          gateway_id (optional)    # 未显式配置则走 config.ResolveGatewayID：HOSTNAME → uuid.NewString()
```

环境变量开关：
- `IM_CONFIG=/path/to/config.yaml`（默认 `config.yaml`）
- `OTEL_DISABLED=true`（禁用追踪/指标导出）
- `HOSTNAME`（K8s Downward API 写入，用作 `gateway_id` fallback）

---

## 五、Router 全景与功能流程

### 5.1 路由总表

`cmd/gateway/main.go` 构建顺序：
1. `imhttp.New(Config{ServiceName, Legacy: corsHandler(mux), Mode})` → Gin engine；注入 Recovery / otelgin / CORS；挂 `/healthz`/`/readyz`。
2. `RegisterAuthRoutes(engine, …)` → `/api/auth/{register,login,me}`。
3. `authedAPI := engine.Group("/api").Use(middleware.JWTGin(secret))` → 8 个 Register…Routes 调用。
4. `mux.Handle("GET /ws", wsHandler)` → 仅 `/ws` 走 legacy。

| 方法 | 路径 | 鉴权 | Handler 文件 : 行 | 流程说明（§5.2 ~ §5.11） |
|------|------|------|-------------------|--------------------------|
| GET  | `/healthz` | 公开 | `internal/http/router.go:35` | 返回 `ok`，K8s liveness |
| GET  | `/readyz`  | 公开 | `internal/http/router.go:36` | 返回 `ok`，K8s readiness（可扩展 DB/Redis check）|
| POST | `/api/auth/register` | 公开 | `auth.go:45` | §5.2 注册 |
| POST | `/api/auth/login`    | 公开 | `auth.go:63` | §5.2 登录 |
| GET  | `/api/auth/me`       | JWT  | `auth.go:84` | §5.2 `me` |
| PUT  | `/api/users/me` | JWT | `profile.go:31` | §5.3 资料更新 |
| GET  | `/api/settings` | JWT | `settings.go:26` | §5.4 设置读取 |
| PUT  | `/api/settings` | JWT | `settings.go:39` | §5.4 设置写入 |
| POST | `/api/friends/request` | JWT | `friend.go:43` | §5.5 好友请求 |
| POST | `/api/friends/accept`  | JWT | `friend.go:71` | §5.5 同意 |
| POST | `/api/friends/reject`  | JWT | `friend.go:96` | §5.5 拒绝 |
| GET  | `/api/friends` | JWT | `friend.go:121` | §5.5 好友列表 |
| GET  | `/api/friends/pending` | JWT | `friend.go:137` | §5.5 待处理列表 |
| POST | `/api/friends/block` | JWT | `friend.go:153` | §5.5 拉黑 |
| GET  | `/api/users/search` | JWT | `friend.go:174` | §5.5 用户搜索 |
| POST | `/api/channels` | JWT | `channel.go:64`  | §5.6 建群 |
| POST | `/api/channels/dm` | JWT | `channel.go:95` | §5.6 建/取 DM |
| GET  | `/api/channels` | JWT | `channel.go:123` | §5.6 频道列表（带预览） |
| GET  | `/api/channels/:id` | JWT | `channel.go:140` | §5.6 单频道 |
| PUT  | `/api/channels/:id` | JWT | `channel.go:163` | §5.6 改名/改头像 |
| POST | `/api/channels/:id/members` | JWT | `channel.go:191` | §5.6 加人 |
| DEL  | `/api/channels/:id/members/:user_id` | JWT | `channel.go:223` | §5.6 踢人 |
| GET  | `/api/channels/:id/members` | JWT | `channel.go:254` | §5.6 成员列表 |
| POST | `/api/channels/:id/leave` | JWT | `channel.go:278` | §5.6 退群 |
| POST | `/api/channels/:id/messages` | JWT | `message.go:81` | §5.7 发消息 |
| GET  | `/api/channels/:id/messages` | JWT | `message.go:135` | §5.7 拉消息（after/before/around） |
| POST | `/api/channels/:id/read` | JWT | `message.go:183` | §5.7 已读标记 |
| POST | `/api/messages/forward` | JWT | `message.go:214` | §5.7 转发（最多 10 目标） |
| POST | `/api/sync` | JWT | `sync.go:51` | §5.8 批量增量同步 |
| GET  | `/api/search` | JWT | `search.go:34` | §5.9 全文搜索 |
| POST | `/api/files` | JWT | `file.go:30` | §5.10 文件上传（multipart, ≤50MB） |
| GET  | `/api/files/:id` | JWT | `file.go:76` | §5.10 下载 |
| GET  | `/api/messages/:id/attachments` | JWT | `file.go:106` | §5.10 附件列表 |
| POST | `/api/favorites/:message_id` | JWT | `favorite.go:31` | §5.11 收藏 |
| DEL  | `/api/favorites/:message_id` | JWT | `favorite.go:51` | §5.11 取消收藏 |
| GET  | `/api/favorites` | JWT | `favorite.go:75` | §5.11 收藏列表 |
| GET  | `/ws?token=&device=` | JWT(Query) | `gateway/ws_handler.go:82` | §6 WebSocket |

**共 36 个 HTTP 路由 + 1 个 WS 端点。**

### 5.2 Auth 流程（3 个端点）

```
POST /api/auth/register
  ShouldBindJSON(registerReq{username,email,password,display_name})
  → binding 失败 422
  → AuthService.Register
        → UserRepo.Create（bcrypt.HashPassword; unique(username/email) 冲突 → ErrUserExists → 409）
        → auth.IssueToken(secret, uid) → JWT HS256
  → 201 {token, user}

POST /api/auth/login
  → AuthService.Login(login, password)  // login 含 '@' 即 email，否则 username
        → UserRepo.GetByUsernameOrEmail → bcrypt.CompareHashAndPassword → ErrBadCreds → 401
        → IssueToken
  → 200 {token, user}

GET /api/auth/me          [JWT middleware 独立 Group，见 auth.go:82]
  c.Get(UserIDKey) → UserRepo.GetByID → 200 user | 404
```

### 5.3 Profile（1 个端点）

```
PUT /api/users/me
  middleware.JWTGin 已把 user_id 放进 gin.Context
  → ProfileService.UpdateProfile(uid, displayName, avatarURL)
        → UserRepo.UpdateProfile（单表 UPDATE；空字符串跳过字段）
  → 200 updated user
```

### 5.4 Settings（2 个端点）

```
GET  /api/settings → SettingsService.Get → UserSettingsRepo.Get（缺省自动 INSERT 默认行）→ 200
PUT  /api/settings → SettingsService.Upsert → UPSERT on user_id → 200
```

### 5.5 Friend + User Search（7 个端点）

```
POST /api/friends/request
  → FriendService.SendRequest(uid, addresseeID)
        → FriendshipRepo.Insert (unique(requester, addressee)，冲突 ErrAlreadyExists→409)
  → 如果 pusher≠nil → hub.PushToUser(addresseeID, "friend_event"{event_type:"request",from_user_id:uid})
  → 201 {status:"pending"}

POST /api/friends/accept    → UPDATE status=accepted + 双向插入另一条 accepted 记录（service 层保证）
POST /api/friends/reject    → UPDATE status=rejected
GET  /api/friends           → SELECT u.* FROM users JOIN friendships WHERE status=accepted
GET  /api/friends/pending   → SELECT addressee 请求
POST /api/friends/block     → UPSERT status=blocked
GET  /api/users/search?q=…  → SearchRepo.SearchUsers（ILIKE q% on username/display_name，排除调用者 + 已拉黑）
```

### 5.6 Channel（9 个端点）

核心逻辑在 `service.ChannelService` + `repo.ChannelRepo`：

```
POST /api/channels       → CreateGroup：tx 内 INSERT channels + INSERT channel_members (caller=owner, 其它=member)
POST /api/channels/dm    → FindDM(userA, userB) → 存在返回 200 ch；不存在 INSERT (type=1) + 2 条 members，返回 201
GET  /api/channels       → ListByUserWithPreview：JOIN last message + 计算 unread 列（见 §6 seq 算法）
GET  /api/channels/:id   → 校验成员身份 → 200 Channel
PUT  /api/channels/:id   → role ∈ {admin,owner} 才可改名/改头像（CASE 语句只替换非空字段）
POST /api/channels/:id/members (x2) / DELETE .../members/:user_id / GET .../members / POST .../leave
```

- **权限约束**：成员增删改、改名，均需 role>=admin；owner 不可 leave（`ErrOwnerCannotLeave`→403）。
- **实时通知**：新增成员成功后，gateway 侧 `hubChannelEventPusher.PushChannelEvent` 立刻推 `channel_event{added}`，让对方立即刷新频道列表。

### 5.7 Message（4 个端点，核心）

```
POST /api/channels/:id/messages   sendMessageReq{content, client_msg_id, msg_type, visible_to, reply_to, file_ids}
  → MessageService.SendMessage:
      MessageRepo.Send(tx 内):
        1. idempotency: 若 (channel_id, client_msg_id) 已存在 → 返回原 id/seq（短路）
        2. ChannelRepo.IncrementSeq(tx, ch) → UPDATE channels SET seq=seq+1 RETURNING seq
        3. INSERT message（VisibleTo != nil 时带 pq.Int64Array）
        4. 如 visible_to 指定：ChannelRepo.IncrementPhantomCount(tx, ch, 排除 sender+可见人)
           → UPDATE channel_members SET phantom_count += 1 WHERE user_id NOT IN (...)
      FileRepo.LinkAttachments(msg.ID, file_ids) —— 可选
  → 回 201 msg
  → 后台 goroutine（独立 context）pushToMembers：
         ChannelRepo.ListMembers → 对每个 member 调用 hub.PushMessage
         visible_to 不含该 member 则把 msg 改成 MsgTypePhantom（content 留空）

GET /api/channels/:id/messages?after_seq|before_seq|around_seq&limit(≤100, default 50)
  分支：
    after_seq   → MessageRepo.FetchForUser  (seq > after, visible_to IS NULL OR userID=ANY OR sender=userID)
    before_seq  → MessageRepo.FetchBefore  (seq < before 倒序取 N 再正序返回)
    around_seq  → MessageRepo.FetchAround  (UNION 前一半 + 后一半)
    default     → FetchMessages(1<<62, 50) 最新 50 条
  → 200 {"messages":[...]}

POST /api/channels/:id/read
  → ChannelService.MarkRead
      → ChannelRepo.MarkRead：UPDATE channel_members SET last_read_seq=MAX(当前, channels.seq), phantom_at_read=phantom_count
  → pusher.PushReadSync(uid, ch, seq)   —— 通知该用户其它设备
  → 200 {seq}

POST /api/messages/forward  {message_id, target_channel_ids[≤10]}
  → ForwardMessages：源身份校验（必须是源频道成员）→ 逐目标 MessageRepo.Send 并带 forwarded_from
  → 201 {messages:[...]}
```

**关键不变量**：
- `seq` 单调递增：`IncrementSeq` 必须与 `INSERT messages` 在同一 tx，避免竞态下 seq 和 insert 错位。
- **幂等**：`(channel_id, client_msg_id)` 唯一索引，客户端重试不会产生重复消息。
- **Phantom 可见性**：`visible_to != NULL` 表示定向消息；不在名单内的成员看到占位符（`msg_type=2`），但 `seq` 仍递增，保证客户端 seq 连续。

### 5.8 Sync（1 个端点，重要）

```
POST /api/sync   {channels:[{id,seq}...]}
  → SyncService.Sync(callerID, cursors):
      serverSeqs = ChannelRepo.GetMemberChannelSeqs(uid)   // 一次 SQL 拉齐所有频道 seq
      for each server channel:
         clientSeq := map(id→seq) 若未提供则算作未知频道
         若 clientSeq >= serverSeq → 跳过（无变更）
         unread := (serverSeq - last_read_seq) - (phantom_count - phantom_at_read)，≥0
         gap := serverSeq - clientSeq
         switch:
           unknown        → fetchLatest(serverSeq, 50) + has_more=(serverSeq>50)
           gap <= 100     → FetchForUser(clientSeq, 100)  [all missed]
           gap > 100      → fetchLatest(serverSeq, 50) + has_more=true
      → 200 {channels:[{id, server_seq, unread, messages?, has_more?}]}
```

- 语义特性：**新频道**（客户端未传 id）自动返回最近 50；**大 gap** 走 fast-forward，避免瞬时把几千条推过去压垮前端。
- 这是取代 Mattermost `bitmap + segment + ACK` 同步协议的核心接口；客户端只需维护每频道一个 int64 `seq`。

### 5.9 Search（1 个端点）

```
GET /api/search?q=...&type=all|messages|users|channels&limit=...
  → SearchService（按类型 fan-out）
      messages → messages.content 的 gin(to_tsvector) 索引，ts_rank 排序
      users    → username/display_name ILIKE
      channels → channels.name ILIKE（排除非成员频道）
  → 200 {messages:[], users:[], channels:[]}
```

### 5.10 File（3 个端点）

```
POST /api/files   multipart field="file"
  → header.Size > MaxUploadSize(50MB) → 413
  → FileService.Upload:
       uuid 生成 storage_path = <uploadDir>/<yyyy>/<mm>/<dd>/<uuid>.<ext>
       io.Copy 到盘 + INSERT files 表
  → 201 File

GET /api/files/:id
  → Download(id) → open file stream + set Content-Disposition + c.DataFromReader(size, mime, rc)

GET /api/messages/:id/attachments
  → SELECT f.* FROM files JOIN message_attachments WHERE message_id=?
  → 200 {"files":[...]}
```

> ⚠️ 上传分片 / 断点续传不在本服务范围（OVERALL §2.1），当前只做元数据 + 单次上传。

### 5.11 Favorite（3 个端点）

```
POST   /api/favorites/:message_id  → UPSERT (user_id, message_id) IGNORE conflict → 201
DELETE /api/favorites/:message_id  → DELETE → 204
GET    /api/favorites              → JOIN messages 按 created_at DESC → 200
```

---

## 六、WebSocket 实时链路

### 6.1 握手

```
GET /ws?token=<JWT>&device=<id>
  ws_handler.go:82 ServeHTTP:
    1. 校验 token → auth.ValidateToken (HS256)
    2. deviceID: query.device 或 "web-<unixNano>"
    3. upgrader.Upgrade（允许任意 origin；生产应收紧）
    4. NewConn(uid, deviceID, ws, hub) → hub.Register
    5. routing.Register(uid, deviceID) → Redis HSET user:connections:{uid} device=gatewayID + Expire 2h
    6. go runHeartbeat(ctx, conn, channelStore, log)
    7. readPump 循环在当前 goroutine 直到断开
    8. 断开：hub.Deregister + routing.Deregister
```

### 6.2 帧类型

| 方向 | Type | Payload | 含义 |
|------|------|---------|------|
| C→S | `ping` | `{channel_seqs:{id:seq}}` | 每 ~15s 一次；带本地 seq 用于服务端 diff |
| S→C | `pong` | `{server_time, channel_seqs}` | diff 只含 serverSeq>clientSeq 的频道（pull fallback） |
| C→S | `send` | `{client_msg_id, channel_id, content, msg_type, visible_to}` | 实时发消息路径（目前 gateway 内直写 DB，非 Pulsar） |
| S→C | `send_ack` | `{client_msg_id, server_msg_id, seq, channel_id}` | 发送回执 |
| S→C | `push_msg` | 同 REST 发消息的 body + `push_id` | 推送新消息 |
| C→S | `push_ack` | `{push_id}` | 客户端 ACK；服务端 `globalACKRegistry.resolve` 释放等待 |
| S→C | `read_sync` | `{channel_id, read_seq}` | 本人其它设备已读同步 |
| S→C | `friend_event` | `{event_type, from_user_id}` | 请求/同意/拒绝 |
| S→C | `channel_event` | `{event_type, channel_id, name}` | 被拉进频道 |

### 6.3 心跳与拉补偿（`heartbeat.go`）

- 服务端每 15s 调 `ChannelSeqStore.GetMemberChannelSeqs(uid)` 查 DB 最新 seq；
- 与 `Conn.knownSeq`（inbound `ping.channel_seqs` 更新）做 diff；
- 把 diff 以 `pong.channel_seqs` 下发；客户端据此决定是否拉取。
- 服务端 `ws.SetReadDeadline(45s)`，客户端 45s 内无任何 inbound 则断开 —— 即服务端侧 liveness。

### 6.4 推送可靠性（`push_consumer.go`）

```
PulsarPushEvent ──▶ PushConsumer.Handle
  → hub.ConnsForUser(targetUID)
  → 若空：直接 ACK Pulsar（pull fallback 兜底）
  → 否则 deliverWithRetry:
      attempt 0: Push 到所有连接；await ackCh(push_id), 3s 超时
      attempt 1: 等 5s 再推一次（maxRetries=1，共 2 次尝试）
      ACK 收到或超时都 ACK Pulsar —— 避免重放风暴；丢的由心跳 diff 找回
```

> **至少一次 + 心跳差分补偿**的组合等价于 Telegram 的 `updates.getDifference` 语义。

### 6.5 跨 Pod 拓扑（T1 待闭环）

- **已有**：Pod A 在 Redis 登记 `user:connections:{uid}` → `deviceX=gwA`。
- **已有**：message 服务把 `PulsarPushEvent` 发到 `msg.push.{gwA}`，gwA 消费本 topic。
- **待补**：gateway 需要在 HTTP 发消息后，**把消息 publish 到 `msg.incoming`** 而不是同步扇出，这样跨 Pod 的订阅者才能统一从 message 服务领活。见 OVERALL §3.1。

---

## 七、数据模型与存储

### 7.1 Schema（`migrations/001_init.up.sql`）

11 张表 + 4 个 updated_at 触发器：

```
users(id, username!, email!, password_hash, display_name, avatar_url, status, created_at, updated_at)
channels(id, type, name, avatar_url, seq, creator_id, …)       — type 1=DM, 2=Group
channel_members(user_id, channel_id) PK
  role (1=member, 2=admin, 3=owner)
  last_read_seq, phantom_count, phantom_at_read   — 未读数计算四元素
messages(id, channel_id, seq, client_msg_id, sender_id, msg_type, content, visible_to BIGINT[], reply_to, forwarded_from, created_at)
  UNIQUE (channel_id, seq)                                  — 同步顺序依赖
  UNIQUE (channel_id, client_msg_id)                        — 幂等发送
  INDEX (channel_id, seq)                                    — 翻页
  INDEX (sender_id, created_at)                              — 个人消息
  GIN to_tsvector('simple', content)                         — 全文搜索
friendships(requester_id, addressee_id, status) UNIQUE
files(id, uploader_id, file_name, file_size, mime_type, storage_path, thumbnail_path, …)
message_attachments(message_id, file_id) PK
message_favorites(user_id, message_id) PK
user_settings(user_id PK, notification_enabled, theme, language, settings_json JSONB)
```

**未读数公式**（客户端展示逻辑 = 服务端一致）：
```
unread = max(0, (channels.seq - channel_members.last_read_seq)
              - (channel_members.phantom_count - channel_members.phantom_at_read))
```
「phantom」代表定向消息里看不见你的那些条 —— 要从总差里扣掉，否则你会看到一个「永远减不到 0」的未读角标。

### 7.2 连接池配置（`repo/db.go:29`）

- `MaxOpen=25`（cfg 覆盖）/ `MaxIdle=5` / `ConnMaxLifetime=30m`
- `PrepareStmt=true`：重复 SQL 走 server-side prepared，消息发送场景收益明显（同一条 `INSERT messages` 每秒几千次）。
- `DisableForeignKeyConstraintWhenMigrating=true`：迁移期不写 FK 约束 DDL（生产仍在 SQL 里 `REFERENCES`）。
- GORM OTel plugin：所有 SQL 自动挂到当前 span（span name = `gorm.query`）。

### 7.3 Redis 键

| Key | Type | TTL | 用途 |
|-----|------|-----|------|
| `user:connections:{uid}` | HASH（field=deviceID, value=gatewayID） | 2h | 路由到具体 Pod；心跳 refresh；Deregister 清掉 |

---

## 八、可观测性

### 8.1 Tracing

- Gin：`otelgin.Middleware("im-gateway")` 每个 HTTP 请求一个 span。
- GORM：`tracing.NewPlugin(WithoutMetrics())` 给每条 SQL 挂子 span。
- Pulsar：`pulsar.Send/Consume` 用 `TextMapPropagator` 把 traceparent 注入 msg.Properties，消费端 Extract 后继续 trace（`internal/pulsar/client.go:76-88, 155-163`）。
- WS：`wsTracer = otel.Tracer("im-gateway/ws")`，`ws.send` / `ws.push_ack` 是独立的 SpanKindServer 根 span。

### 8.2 Metrics

- `otel/contrib/instrumentation/runtime`：Go runtime 指标（GC、goroutine、mem）。
- 业务指标：`im.ws.active_connections`（ObservableGauge，`hub.go:registerMetrics`）。
- PromQL / Grafana 面板见 `BACKEND.md §8` 与 `/observe` skill 产出。

### 8.3 Logging

- `slog.NewJSONHandler` + `observability.NewTraceHandler`：日志自动带 `trace_id` / `span_id`，Loki/Jaeger 可交叉跳转。

---

## 九、性能压测必达指标

> 这是上线门槛。任一指标未达标，不允许切流（参见 OVERALL §三 T5 退出条件）。

### 9.1 容量目标

| 指标 | 旧 Mattermost 生产 | **im 必达** | 测量位置 |
|------|-------------------|------------|----------|
| 单 Pod WS 并发连接 | ~10k | **50k** | `im.ws.active_connections` |
| 集群总 WS 并发 | N/A | **150k**（3 副本） | Σ Pod gauge |
| 集群发消息 QPS | ~2k | **10k** | 拥塞前最大持续 |
| 单 Pod 发消息 P99 延迟 | — | **<30ms** | HTTP span duration |
| 集群消息投递 P99（sender → 对端 push_msg） | ~300ms | **<50ms** 同 DC，**<80ms** 跨 Pod | traceparent 端到端 |
| /api/sync P99 | ~500ms | **<100ms** | HTTP span duration |
| Pulsar consumer lag | N/A | **<5s**（正常）/ **<30s**（峰值） | Pulsar 内置指标 |

### 9.2 资源预算（K8s 单 Pod）

| 项 | 预算 |
|----|------|
| CPU request / limit | 1 / 4 核 |
| Memory request / limit | 512 MiB / 2 GiB |
| 预估 50k conn 内存 | ≈ 50k × 32 KB（WS 读写缓冲+Conn 结构）≈ 1.6 GiB |
| GOMAXPROCS | 自动（K8s 下由 `uber-go/automaxprocs` 或 Go 1.26 默认对齐 cgroup quota） |
| PG 连接 | 25/Pod × 3 Pod = 75（PG max_connections 预留 200，其它服务留量） |

### 9.3 压测场景（k6 + WebSocket 扩展）

1. **Cold fan-in（首连）**：10k 客户端同时握手，每人 50 频道，各 1000 条历史；
   - 预期：握手 P99 < 500ms；随即一次 `/api/sync` P99 < 200ms；
   - 拒绝条件：upgrade 成功率 < 99.9%。
2. **常态对话（steady）**：1k 活跃频道，持续发 10k msg/s 1 小时；
   - 预期：`push_msg` 端到端 P99 < 80ms；错误率 < 0.01%。
3. **广播（fan-out）**：1 个 10k 成员频道，10 msg/s；
   - 预期：Pulsar 扇出 < 2× 消息速率；单条 fan-out P99 < 150ms 收口。

### 9.4 必测的异常路径

| 场景 | 期望行为 |
|------|---------|
| Pod 重启 | 15s 内所有原 Pod 用户完成重连 + sync；无重复消息；last_read_seq 不回退 |
| Redis 抖动（5s 不可用） | 路由写失败 warn；WS 建连降级但本 Pod push 可达；恢复后自动 Register |
| Pulsar 消费者落后 | 消息不丢；心跳 diff 即时补发；consumer lag 监控触发告警 |
| DB 慢查询（p99 > 500ms） | PrepareStmt 预编译下 `/api/sync` 仍 < 300ms；有 slow query log |

---

## 十、测试与质量门

- **单元测试**：`internal/**/*_test.go`，覆盖率目标 **≥ 80%**；`go test -race -cover ./...`。
- **集成测试**：`tests/integration/*_test.go`，用 `testcontainers-go/modules/{postgres,redis}` 起真 PG/Redis；使用 `httpexpect/v2` 做黑盒；所有路由都要覆盖正向+权限+边界。
- **Hub 单元测试**：`internal/gateway/hub_test.go` 验证并发 Register/Deregister/PushToUser。
- **中间件**：`middleware/jwt_gin_test.go` 覆盖缺 header、错 token、正常流、payload 非 int64 等分支。
- **PR 门槛**：
  - gofmt/goimports 过；
  - go vet + staticcheck 无 new；
  - 80% line coverage；
  - `gitnexus_detect_changes` 预期范围；
  - `gitnexus_impact` 对修改的公开符号跑一次，HIGH/CRITICAL 需 review 签字。

---

## 十一、部署形态（K8s）

### 11.1 Deployment 约束

```
im-gateway  Deployment replicas=3
  env:
    HOSTNAME: $(POD_NAME)                   # Downward API → 用作 gateway_id
    IM_CONFIG: /etc/im/config.yaml
  probes:
    livenessProbe : GET /healthz (3s period)
    readinessProbe: GET /readyz  (3s period)
  service:
    type: ClusterIP，WebSocket 需 Ingress 关闭 buffering / proxy-read-timeout ≥ 120s
  autoscale:
    HPA on im.ws.active_connections（Prometheus Adapter）阈值 40k/Pod
```

```
im-message  Deployment replicas=2  （无状态，不接外部流量）
im-sync     Deployment replicas=1  （Plan 7 前占位，不实际工作）
```

### 11.2 横向扩容与粘性

- **无粘性必要**：gateway 不持有会话状态（Hub 映射仅为本 Pod 的 WS 句柄）；用户连到任意 Pod 都能工作。
- **新 Pod 启动**：订阅 `msg.push.{自己的 gatewayID}` → LB（K8s Service / Ingress）分到该 Pod 的流量自动生效。
- **Pod 宕机**：客户端 WS `readDeadline=45s` 自动断开 → Tauri Rust 侧重连 → 命中其它 Pod → `/api/sync` 把断开期间的消息拉齐。
- **扩容手势**：`kubectl scale deploy/im-gateway --replicas=N`，秒级生效；无需预热、无需数据迁移。

---

## 附录 A — 关键执行流程（gitnexus processes 摘录）

gitnexus 为 `im` 索引出 86 个 process（执行流），下面是对本文档最相关的 7 条（跑 `READ gitnexus://repo/im/process/<name>` 可看全量步骤）：

1. `WsHandler.ServeHTTP` → 握手 → readPump → TypeSend/TypePushACK 分发
2. `WsHandler.handleSend` → MessageRepo.Send → Hub.PushToUser fan-out
3. `PushConsumer.Handle` → deliverWithRetry → ACK 或超时兜底
4. `MessageService.SendMessage` → Repo.Send(tx) → FileRepo.LinkAttachments → goroutine push
5. `SyncService.Sync` → GetMemberChannelSeqs → 四分支 (无变更/新频道/小 gap/大 gap)
6. `ChannelService.CreateGroup` → tx 内 INSERT channels + members + pusher 广播
7. `FriendService.SendRequest` → Insert → pusher friend_event

---

*文档最后校对：2026-04-23；对应 commit `60ddd3f`。当符号重命名或路由变更时，先跑 `gitnexus_impact` 再更新本文。*

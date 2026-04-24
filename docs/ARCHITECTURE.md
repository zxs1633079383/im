# im — Architecture & File Map

> 这份文档是项目的"静态地图"。看完它你应该能回答：某个功能走哪几层？某个文件干什么？拉高吞吐要改哪里？
> 动态状态（分支/tag/下一步）见 `SESSION.md`；整体战略目标见 `docs/GOAL.md`；详细里程碑见 `server/docs/BACKEND.md`。

---

## 1. 技术栈全景

### 1.1 Server (Go)

| 领域 | 选型 | 说明 |
|------|------|------|
| HTTP | Gin | 薄路由层，绑定 → service |
| ORM  | GORM + pgx | UPDATE RETURNING 在 Postgres 上原子 alloc seq |
| DB   | PostgreSQL 15 | 业务主库；seq 单调由 row-level UPDATE 保证 |
| MQ   | Apache Pulsar | 跨 pod 推送（Producer 共享缓存 256/LRU） |
| KV   | Redis | 用户 ↔ gateway_id 路由表，TTL 45s |
| WS   | gorilla/websocket | Hub + Conn，心跳 15s × 3 = 45s |
| Trace| OpenTelemetry + Jaeger | 3 tracer: `im-server/service`、`/repo`、`/gateway` |
| Test | testcontainers-go + httpexpect v2 | 每 test 独立 Postgres 容器 |

### 1.2 Client (Angular + Tauri)

| 层 | 选型 | 说明 |
|----|------|------|
| UI | Angular 17 standalone | `client/src/app/core/*` 按功能分包 |
| Native | Tauri 2 (Rust) | 本地 HTTP/WS 客户端，持久化 seq |
| Feature flag | `apiFlavor: 'mattermost' \| 'im'` | 双栈并存，默认仍走 mm |
| Rust flag | `im_seq_sync` | 切换 IMDataSource ↔ ImSeqDataSource |

### 1.3 部署

| 资源 | 位置 |
|------|------|
| Dockerfile | `server/Dockerfile`（distroless 多阶段） |
| Compose  | `docker-compose.yml`（本地 pg/redis/pulsar） |
| K8s     | `deploy/k8s/*.yaml`（3 副本 + HPA + PDB） |
| 脚本    | `scripts/v4-*.sh`, `scripts/v4-load.js` |

---

## 2. 目录地图

### 2.1 `server/cmd/` — 可执行入口

| Path | 角色 |
|------|------|
| `cmd/gateway/`  | 主二进制：HTTP + WS + scheduled worker 注入 |
| `cmd/message/`  | 命令行投递工具（人工触发 Pulsar 消息） |
| `cmd/sync/`     | 离线 /api/sync dump 工具，用于诊断游标 |
| `cmd/v4-client/`| V4 集群韧性压测客户端（Go WS） |

### 2.2 `server/internal/` — 业务代码

```
internal/
├── config/           # Viper 配置加载 + 校验
├── auth/             # JWT 签发 / 校验
├── middleware/       # Gin 中间件：auth、CORS、request-id、trace
├── observability/    # OTel provider 初始化（tracerProvider、meterProvider）
├── pulsar/           # Pulsar client 封装（Producer/Consumer factory）
├── deps/             # 依赖装配（wire 风格手工注入，所有 service 从这里出）
│
├── http/             # HTTP 路由 + handler（薄层 → service）
│   ├── router.go                 # 全部路由注册
│   ├── auth.go                   # /api/auth/*
│   ├── sync.go                   # /api/sync（M1 核心契约，见 BACKEND §3.3）
│   ├── message.go                # /api/channels/:id/messages + /around
│   ├── channel.go                # /api/channels/*
│   ├── channel_governance.go     # 拉黑 / 成员角色等治理接口
│   ├── announcement.go           # M2-B
│   ├── urgent.go                 # M2-D
│   ├── approval.go               # M2-E
│   ├── notification.go           # M2-F
│   ├── scheduled.go              # M2-G（定时消息）
│   ├── quick_reply.go            # M2-H
│   ├── favorite.go / file.go / search.go / friend.go / profile.go / settings.go
│   └── *_test.go                 # handler 级别单元测试（mock service）
│
├── service/          # 业务编排：事务、WS 推送、Pulsar 发布
│   ├── sync.go                   # SyncService.Sync —— seq 游标 + delta 计算
│   ├── message.go                # SendMessage 走 repo.AllocSeqAndInsert
│   ├── channel.go                # AddMember 返回 channelName 供推送
│   ├── friend.go                 # Accept/Reject 返回 requesterID 供推送
│   ├── announcement.go / urgent.go / approval.go / notification.go
│   ├── scheduled.go              # 建 / 取消定时
│   ├── scheduled_worker.go       # 后台轮询 → Deliver（复用 repo tx）
│   ├── tracing.go                # Tracer 常量
│   └── *_test.go                 # service 级单测
│
├── repo/             # 数据访问：所有 SQL 在这一层
│   ├── models.go                 # GORM 实体定义
│   ├── db.go                     # *gorm.DB + 迁移钩子
│   ├── errors.go                 # ErrNotFound 等统一错误
│   ├── message.go                # ★ AllocSeqAndInsert（single SQL, tx != nil 复用）
│   ├── channel.go / channel_governance.go
│   ├── friendship.go             # Accept/Reject 用 UPDATE RETURNING 返回 requesterID
│   ├── approval.go / announcement.go / urgent.go / notification.go
│   ├── scheduled.go / quick_reply.go
│   ├── favorite.go / file.go / search.go
│   ├── user.go / user_settings.go
│   ├── routing.go                # Redis: userID → gateway_id（TTL 45s）
│   ├── redis.go                  # Redis client factory
│   ├── tracing.go                # repo 层 Tracer
│   ├── mocks/                    # mockgen 生成，供 service_test 使用
│   └── *_test.go                 # 走 testcontainers 的仓储层集成测试
│
├── gateway/          # WebSocket + 跨 pod 推送
│   ├── ws_handler.go             # upgrade → conn
│   ├── conn.go                   # 单连接读写循环 + ping/pong
│   ├── hub.go                    # 本 pod 用户映射 + PushToUser
│   ├── heartbeat.go              # Redis routing 续期
│   ├── routing.go                # 查询 userID → 目标 gateway_id
│   ├── cross_pod_push.go         # ★ 三路径：local hit → routing → Pulsar
│   ├── producer_cache.go         # 256-entry LRU；onEvict 关 producer
│   ├── topic.go                  # PushTopicFor(gatewayID, env) + localname 后缀
│   ├── push_consumer.go          # 订阅本 gateway 的 push topic
│   ├── tracing.go                # gateway Tracer
│   ├── types.go                  # 12 + N WS 事件类型常量
│   └── *_test.go                 # producer cache / topic / hub 单测
│
└── testutil/
    └── containers/               # testcontainers-go Postgres helper
```

### 2.3 `server/migrations/` — 数据迁移

| 文件 | 目的 |
|------|------|
| `001_init.*.sql` | users / channels / members / messages（last_seq 列在 channels） |
| `002_m1_message_lifecycle.*` | messages 增 deleted / deleted_at / updated_at（同步用） |
| `003_m2_channel_governance.*` | 黑名单 / 成员角色 |
| `004_m2_announcements.*` | 公告 |
| `005_m2_urgent.*` | 紧急消息 |
| `006_m2_approvals.*` | 审批流（状态机 WHERE 守卫） |
| `007_m2_notifications.*` | 系统通知 |
| `008_m2_scheduled_messages.*` | 定时消息 |
| `009_m2_quick_replies.*` | 快捷回复模板 |

### 2.4 `server/tests/integration/` — 集成测试

| 文件 | 覆盖 |
|------|------|
| `v5_harness_test.go`          | 共享 env：testcontainers + 所有 recording fake |
| `v5_single_flows_test.go`     | V5.1–V5.10 单接口 flow |
| `v5_groups_test.go`           | G1–G10 模块组连续性（G5 需 V4 跳过） |
| `v5_m1_coverage_test.go`      | M1 补覆盖（readers / announcement detail / approval detail） |
| `v5_m2_*_test.go`             | M2 每个子模块独立测试（scheduled / notification / announcement / urgent / approval / quick-reply / broadcast） |

### 2.5 `server/docs/` — 战略文档

| 文件 | 目的 |
|------|------|
| `BACKEND.md` | M1–M6 里程碑 + 契约（§3.3 /api/sync、§4.1 AllocSeqAndInsert、§5 跨 pod 推送、§十一 OTel 覆盖） |
| `FRONTEND.md`| 前端切换计划 F0–F5、WS 事件重命名表（V1 12 + V2 候选 3）、Rust Tauri 改造 |
| `OVERALL.md` | 后前端整合路线图 T0–T6（17 周）、`make verify-all` 收敛、V5.3.1 十组连续性场景 |
| `TECH.md`    | 技术栈总览（栈选型、版本、跨子系统契约） |

### 2.6 `client/` — Angular + Tauri 双端

```
client/
├── src/app/core/               # Angular 核心服务（按功能分包）
│   ├── auth/ channels/ config/ db/ favorites/ files/ friends/
│   ├── i18n/ messages/ search/ theme/ ws/
│   ├── messages/message.service.ts   # 消息聚合服务（apiFlavor 分流）
│   └── config/api.config.ts          # apiFlavor 开关 + endpoint 表
├── src-tauri/                  # Rust native 层
│   ├── src/http/im_client.rs         # im HTTP 客户端（~390 行）
│   ├── src/data_center/im_seq_data_source.rs  # seq 游标持久化（~253 行）
│   └── src/lib.rs / main.rs
└── scripts/v6-smoke.mjs        # 端到端烟测（7/7 green 基线）
```

---

## 3. 关键数据流

### 3.1 发消息（最热路径）

```
Client → POST /api/channels/:id/messages
       → http.MessageHandler.Send
       → service.MessageService.Send
           ├─ repo.MessageRepo.AllocSeqAndInsert(ctx, nil, msg)   ★ 原子 alloc+insert
           │    (UPDATE channels SET last_seq=last_seq+1 RETURNING ... ; INSERT msg ...)
           └─ gateway.CrossPodPush(ctx, receiverID, "push_msg", payload, routing, cache, ...)
                ├─ local hub 命中 → hub.PushToUser
                └─ miss → routing.Lookup(userID) → producerCache.GetOrCreate(topic) → Send
Receiver gateway push_consumer 订阅 topic → hub.PushToUser → WS → 客户端
```

### 3.2 增量同步 (`/api/sync`)

契约见 `server/docs/BACKEND.md §3.3`，冻结字段：
- `cursor: { channel_id → seq }` 客户端持久化
- `limit_per_channel: 200`，`MaxChannelsPerCall = 500`
- 返回 `channels[{channel_id, msgs[], last_seq, has_more}]`

### 3.3 跨 pod 推送失败处理

- `routing.Lookup` 45s TTL，心跳 15s 续期（3× 容错）
- Pulsar `producer.Send` 失败 → `markOffline(userID)` 骨架位（待落地真删除逻辑）
- Producer 缓存 256 LRU，`onEvict` 调 `producer.Close()` 防泄漏

---

## 4. 编码 / 吞吐准则（Go 代码必读）

本项目所有 Go 代码必须满足 `~/.claude/skills/go-concurrency-patterns/SKILL.md`：

- `context.Context` 贯穿所有同步 / 异步调用，不得 `context.Background()` 穿透进业务层
- goroutine 有主：每个 `go` 都必须有 `WaitGroup` 或 `errgroup` 兜底，禁止裸奔
- channel 有向：函数签名使用 `<-chan` / `chan<-` 明确方向
- 同步原语：优先 channel；需要共享 map 用 `sync.Map` 或 RWMutex 隔离字段
- 退出：`select { case <-ctx.Done(): ... }` 必须在每一个 for-loop 里
- 并发安全指南：`sync.Pool` 复用 buffer，`atomic` 仅用于计数 / 引用切换
- Pulsar producer / Redis client 用 sync.Once + 全局单例，禁止每次新建

CI 强制：`go vet`、`staticcheck`、`gosec`、`go test -race ./...`。

---

## 5. 下一步查阅

- 想了解**全局目标和里程碑**：`docs/GOAL.md` + `server/docs/BACKEND.md §六`
- 想**接手当前会话**：`SESSION.md`
- 想**改动 Go 代码**：`CLAUDE.md` → 按 `/go-concurrency-patterns` 规范落地
- 想**跑全量验证**：`cd server && make verify-all`

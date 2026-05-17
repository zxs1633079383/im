# CLAUDE.md — `server/internal/repo` 模块指令

> 本文件是 **repo 数据访问层** 的模块级 Claude 指令，优先级：
> 用户全局 `~/.claude/CLAUDE.md` > 项目根 `/Users/mac28/workspace/golangProject/im/CLAUDE.md` > **本文件** > 默认行为。
> 任何改 `server/internal/repo/**/*.go`、`server/migrations/*.sql` 的会话，必须**先**完整读完本文件。

---

## 0. 模块定位（Why this module exists）

- **`repo/` 是 IM 后端**所有持久层 IO 的唯一通道**：PostgreSQL via gorm + 个别 Redis（仅 `routing.go` / `redis.go`）。
- **service 层、http handler、gateway、cmd/ worker 都不得绕过 repo 直接调 `*gorm.DB` / `database/sql`**。绕过 = C001 / C016 失守 = 跨 pod 推送丢消息 / seq 重复 / hot-path RMW 爆炸。
- **本层只搬数据，不写业务逻辑**：
  - ✅ 允许：SQL CRUD、单 INSERT/UPDATE/DELETE 的 CAS WHERE、JOIN 聚合、事务封装、`*gorm.DB` 复用
  - ❌ 禁止：跨 endpoint 编排、鉴权判定、广播 / Pulsar fan-out 调度（这些是 service 层的活）
  - 例外：`AllocSeqAndInsert` 内部触发 `gateway.CrossPodPush`（C001 历史决策，封装在 repo 内是为了 seq 分配与广播原子性）
- **热路径写复杂度强制 O(1)**：单 INSERT / 单 UPDATE WHERE PK / 单 DELETE WHERE PK。**严禁 read-modify-write JSON 数组列** —— 详见 §6 与 root `~/.claude/rules/common/performance.md §Hot-Path Write Complexity`。
- 上游契约：`server/docs/BACKEND.md §4.1`（AllocSeqAndInsert）、`docs/harness/C001` / `C016`。

---

## 1. 影响范围（Blast Radius）

### 1.1 上游调用者（改 repo 接口要 sweep 的位置）

| 目录 | 用途 | 重点文件 |
|---|---|---|
| `server/internal/service/**` | 业务编排层，本 repo 几乎所有方法的最大调用方 | `service/message*.go` / `service/channel*.go` / `service/approval.go` |
| `server/internal/http/**` | HTTP handler，少量直读（只读 path） | `http/message.go` / `http/channel.go` |
| `server/internal/gateway/**` | WS broadcast、push envelope；通过 `AllocSeqAndInsert` 回调间接依赖 | `gateway/dispatcher.go` |
| `server/cmd/**` | 异步 worker (scheduled fire / template send / system message) | `cmd/message/main.go` |
| `server/tests/integration/**` | 198+ 集成测试 fixture 直接构造 `repo.*` struct | `tests/integration/m4_*.go` |
| `server/internal/repo/mocks/**` | mockery 生成的接口 mock；改 interface 必须重生 | 所有 `*_mock.go` |

### 1.2 下游依赖

- **PostgreSQL**（生产 + 集成测试用 `testcontainers` 起容器）—— `server/migrations/*.up.sql` / `*.down.sql`
- **Redis**（仅 `routing.go` / `redis.go`：跨 pod routing key / userdata cache，非业务表）—— `IM_REDIS_CLUSTER=true` 时走 cluster client（C010）
- **OTel**：所有 repo 方法首行 `ctx, span := tracer.Start(ctx, "...")`，参考 `tracing.go`（package-level singleton）
- **gorm/v2** + `gorm.io/plugin/opentelemetry/tracing` —— 单一 `*gorm.DB` 单例由 `db.go::Open` 构造，禁止在 repo 内 new

### 1.3 与项目根硬约束的耦合

| 项目根 CLAUDE.md 锚点 | 本层落地位置 |
|---|---|
| §1.6 "所有消息写入走 `AllocSeqAndInsert`" | `repo/message.go::AllocSeqAndInsert` |
| §1.6 "Redis routing TTL 45s × 15s × 3" (C004) | `repo/routing.go` |
| §1.6 "team_id 可空" (C011) | `repo/models.go::Channel.TeamID *string` |
| §1.7 "ErrNotFound 统一语义" | `repo/errors.go` |

---

## 2. 功能模块清单（File Map）

> 当前 34 个 .go 文件 / 4340 行。`models.go` 是所有表结构的 single source of truth；其余按表分文件，**禁止把多个表混进同一个文件**。

| 文件 | 行数 | 职责 | 标签 |
|---|---|---|---|
| `db.go` | 78 | `Open(Config) (*gorm.DB, error)`：连接池单例、HikariCP 对齐配置、OTel tracing plugin 装配 | core |
| `module.go` | 32 | fx-style provider 装配（NewMessageRepo / NewChannelRepo / ...）；改 interface 必同步 | core |
| `errors.go` | 20 | `ErrNotFound` / `ErrForbidden` / `ErrGone` / `ErrInvalidTemplate` 统一语义入口 | core |
| `tracing.go` | 9 | package-level `tracer` 单例（OTel） | core |
| `models.go` | 290 | 所有表的 gorm struct + `TableName()` 锁定；改字段必更 migration | core |
| `metrics.go` | 103 | gorm 拦截器：SQL 耗时 / 行数 Prom 指标；`registerDBPoolMetrics(sqlDB)` | core |
| `message.go` | 535 | `MessageRepo` 接口 + `Send` / `AllocSeqAndInsert` / `PostSystemMessage` / `UpdateContent` / `SoftDelete` / `Fetch*` | **C001 唯一写入入口** |
| `message_props.go` | 104 | `UpdateMessageProps` + `AppendTemplateReceiver`（注意：当前仍是 RMW，但接受 lossy 语义 —— C016 §3.4 反例提醒） | improve |
| `message_v073.go` | 39 | v0.7.3 mention_list / urgent 切片字段访问器 | add |
| `read_stats.go` | 107 | `GetReadStatsBatch`：单 SQL JOIN + `FILTER` aggregates，**禁止改 N+1** | core |
| `channel.go` | 571 | Channel CRUD + `IncrementSeq` / `IncrementPhantomCount`；最大文件，超过 400 行硬限 → 待拆 | refactor TODO |
| `channel_governance.go` | 255 | 成员增删 + 角色变更 + owner transfer (C013) | improve |
| `channel_topic.go` | 106 | M3 topic / sub-channel 关系；`root_id` 父指针 | add |
| `channel_v073.go` | 59 | v0.7.3 channel close / nickname 字段访问器 | add |
| `approval.go` | 174 | 审批工单 CRUD + `Decide` 事务（RowsAffected=0 → ErrNotFound 模式经典版） | core |
| `urgent.go` | 124 | `UrgentRepo`：`SetUrgent` / `ClearUrgent` / `AddConfirmation` / `CountUnconfirmed` | core |
| `announcement.go` | 141 | 公告 CRUD | core |
| `notification.go` | 147 | 离线通知聚合（user-level 抓取） | core |
| `scheduled.go` | 186 | 定时消息 fire 队列；`cmd/message` worker 消费 | add |
| `reaction.go` | 77 | 表情回复（M4 引入），单 INSERT + 单 DELETE，O(1) 模板 | core |
| `favorite.go` | 106 | 收藏夹（用户级，独立表） | core |
| `quick_reply.go` | 121 | 快速回复模板 | core |
| `friendship.go` | 152 | 好友关系（M5 占位） | add |
| `file.go` | 69 | 上传文件元信息（OSS file_id 关联） | core |
| `search.go` | 106 | 消息全文检索（PG full-text） | core |
| `user_settings.go` | 49 | 用户偏好（NotifyPref / IsTop 等） | core |
| `routing.go` | 258 | **Redis** 跨 pod routing key 45s TTL + 心跳（C004） | core / Redis-only |
| `redis.go` | 51 | Redis client 单例（cluster / single 二选一，C010） | core |
| `*_test.go` | — | 单测：每个文件配对，白盒；用 sqlmock / testcontainers Postgres | test |

> **新增文件原则**（root §coding-style "单文件 ≤ 400 行"）：新建表 → 新建对应 `<table>.go` + `<table>_test.go`，**禁止往 `message.go` / `channel.go` 继续堆**。

---

## 3. SOP — 新增 / 改表的标准流程

```
[1] 设计 schema（O(1) 写入 checklist；C016 §2 反模式对照）
        ↓
[2] 写 migration：server/migrations/NNN_<topic>.up.sql + .down.sql
        ↓
[3] 改 models.go 加 struct + TableName()
        ↓
[4] 新建 repo/<table>.go：interface + gorm 实现 + 单测 <table>_test.go
        ↓
[5] mockery 重生 mock：cd server && go generate ./internal/repo/...
        ↓
[6] 改 module.go 注册 provider
        ↓
[7] service 层调用 + 集成测试 server/tests/integration/m{N}_<topic>_test.go
        ↓
[8] make verify-all 全绿（go test -race + grep gate + migrate up/down）
        ↓
[9] commit per phase + tag（§5）
```

**关键 gate**：
- **DDL 改完先本地 reset**：`make migrate-reset && make migrate-up` 必须 0 错；prepared statement cache 改 schema 后必须**重启 gateway**（C011 §3.4）
- **改 interface 必须 mockery**：忘了重生 mock 是 CI 红的常见 #1 原因
- **新增/改字段必须配套集成测试**：至少 1 个 happy path + 1 个 error path（root golang/testing.md §覆盖率 100%）

---

## 4. Pre-commit 自检清单

提交前必须本地跑过：

```bash
cd /Users/mac28/workspace/golangProject/im/server

# ① race detector + 100% coverage
go test -race -covermode=atomic -coverprofile=coverage.out ./internal/repo/...
go tool cover -func=coverage.out | awk '$3 != "100.0%" && $1 !~ /coverage:ignore/ { print; exit 1 }'

# ② static analysis
go vet ./internal/repo/...
golangci-lint run ./internal/repo/...

# ③ C001 grep gate —— INSERT INTO messages 必须只出现在 message.go
grep -rEn 'INSERT\s+INTO\s+messages' internal/ --include='*.go' | grep -v '_test.go' | grep -v 'repo/message.go'
# expect: 0 行

# ④ C016 grep gate —— 禁 RMW JSON 数组列
grep -rEn 'json\.Marshal\(.*queue|json\.Marshal\(.*list' internal/repo/ --include='*.go' | grep -v '_test.go'
# expect: 0 行（如果 props 类 lossy RMW 必须 inline 注释解释为何 acceptable，参考 message_props.go §Concurrency note）

# ⑤ C012 grep gate —— 无 int64 ID 字段
grep -rEn 'ID\s+int64|ChannelID\s+int64|MessageID\s+int64' internal/repo/ --include='*.go'
# expect: 0 行（seq 是 BIGINT 但不算 ID，可忽略 Seq int64）

# ⑥ C011 grep gate —— team_id 不能 NOT NULL
grep -rEn 'team_id\s+TEXT\s+NOT\s+NULL' migrations/ --include='*.sql'
# expect: 0 行

# ⑦ 单文件 ≤ 400 行硬限（root golang/coding-style.md §Hard Limits）
wc -l internal/repo/*.go | awk '$1 > 400 && $2 !~ /_test/ { print "❌ over 400 lines:", $0 }'
# 当前已知例外：message.go 535 / channel.go 571 —— 待拆，**不允许新建超过 400 行的文件**

# ⑧ 集成测试
make verify-integration
```

任意一条非 0 / 红 → **不准 commit**。

---

## 5. Commit / Tag 规范

### Commit message

**格式**：`<type>(repo/<table>): <中文描述>`

- `<type>` ∈ `feat` / `fix` / `refactor` / `perf` / `test` / `docs` / `chore`
- **scope 必须带表名**（`repo/message` / `repo/channel_member` / `repo/approval`），方便 review 与 git log grep
- 中文 description ≤ 50 字，不加句号
- body 用中文解释 **why**（不解释 what），每行 ≤ 72 字符

**正例**：

```
feat(repo/message): AllocSeqAndInsert 加 sender 副本写入兜底

C001 invariant 强化：跨 pod 推送通道断时，sender 端依赖本地
副本走轮询补偿。复用现有 tx，O(1) 单 INSERT，无额外 RTT。
```

```
fix(repo/channel_member): advance_read_seq CAS WHERE 修正

旧实现 last_read_seq <= new_seq 会接受相等值并触发空 UPDATE
+ 误广播；改严格 < 走 C016 §3.2 B 模板。
```

### Tag（多 Phase 大改）

跨多表 / 多 Phase 的迁移（如 C012 BIGINT→TEXT、加密字段引入）按 root §3.1 "一个 Phase = 一个模块 = 一个 tag" 落地：

- `v0.7.x-phase{N}-repo-<scope>` —— 模块完成
- tag 必带 message 含覆盖 commit 范围 + 验证状态（lint / test / migrate up/down）

---

## 6. 约束规范（Module-Level Invariants）

下面 5 条**铁律**任意一条违反 = PR 直接打回，不接受"下次注意"。

### 6.1 ① `AllocSeqAndInsert` 是消息写入唯一入口（C001）

- 唯一签名：`AllocSeqAndInsert(ctx context.Context, tx *gorm.DB, msg *Message) (int64, error)`
- `tx != nil` 复用外部事务，repo 内不 Begin / Commit；`tx == nil` 内部起 read-committed 事务
- 内部串：`UPDATE channels SET seq=seq+1 RETURNING seq` → `INSERT messages` → 触发 `gateway.CrossPodPush`
- **service / http / cmd / gateway 任何路径直接 `INSERT INTO messages` / `db.Create(&Message{})` 都是 grep gate hard fail**
- 例外：migrations seed / 测试 fixture（C001 §6 反例与边界）

### 6.2 ② 热路径写 O(1)，禁 RMW JSON 数组（C016 + root performance.md §Hot-Path）

- 单 INSERT / 单 UPDATE WHERE PK / 单 DELETE WHERE PK —— 写复杂度恒等 O(1)
- **绝对禁止**：
  - `mention_queue TEXT DEFAULT '[]'` 类 JSON 数组列做累计 / 队列
  - `serde_json::to_string(entire_wire_obj)` 塞单列 `props`
  - `channel_members` 加 `urgent_count BIGINT` 类 RMW 计数列（必须建对称 normalized 子表）
- **强制**累计 / 队列 / 列表 → normalized 子表 + 复合 PK + 索引（见 §8 模板）
- 现存例外：`message_props.go::UpdateMessageProps` 是 lossy RMW，**允许**仅因"模板已收到"低频 idempotent 场景且文件顶 godoc 已显式承担风险；新代码**不准**援引此例外

### 6.3 ③ 所有 ID 字段是 `string`（C012）

- 表层：所有自增主键 `TEXT DEFAULT gen_random_ulid()::text`，FK 列 TEXT
- Go 层：`type X struct { ID string }`，handler 直接读 `c.Param("id")` 不再 `strconv.ParseInt`
- 例外：`Seq int64`（单调 cursor，不是身份 ID）、`*_at` 时间戳、计数列
- 严禁混合形态：不允许某些表 TEXT、某些表 BIGINT —— 改一个就要扫一族

### 6.4 ④ `ErrNotFound` / `ErrForbidden` / `ErrGone` / `ErrInvalidTemplate` 统一语义（`errors.go`）

- **`RowsAffected == 0` → `ErrNotFound`**（`approval.go:107` 是经典样板）
- soft-deleted row 二次操作 → `ErrGone`（idempotent 上游可视作成功，跳过 fan-out）
- 上游用 `errors.Is(err, repo.ErrNotFound)` 判类型，**禁止字符串匹配**
- 包错误用 `fmt.Errorf("do X: %w", err)` 包一层上下文不断链

### 6.5 ⑤ 不写业务逻辑，只搬数据

- ❌ 禁止 repo 调 gateway 广播（C001 内的 `CrossPodPush` 是历史封装例外，新增功能走 service 层编排）
- ❌ 禁止 repo 调鉴权 / cookie 解析（middleware 层职责）
- ❌ 禁止 repo 内多 endpoint 编排（service 层职责）
- ✅ 允许：单表 / 跨表 JOIN / 事务封装 / index 利用 / SQL CAS

---

## 7. Harness 映射表

下表把本模块强相关的 4 条 active harness 映射到具体触发场景 + 验证手段。任何符合"触发场景"列描述的改动 → 必须按"§3 Required"落地 + 按"§4 Verification"自检。

| 编号 | 标题 | 本层触发场景 | 验证手段 |
|---|---|---|---|
| **C001** | `AllocSeqAndInsert` 是消息写入唯一入口 | 任何 `INSERT messages` / `db.Create(&Message{})` / `tx.Exec("INSERT INTO messages ...")` | `grep -rEn 'INSERT\s+INTO\s+messages' internal/ --include='*.go' \| grep -v _test.go \| grep -v 'repo/message.go'` 必须 0 行；单测 `TestAllocSeqAndInsert_Concurrent10WritersSameChannel` -race 跑 100 次绿 |
| **C011** | channels.team_id 必须 TEXT NULL | 任何 migration `ALTER COLUMN team_id` / repo 拼 SQL 带 team_id；`models.go::Channel.TeamID *string` | `grep -rEn 'team_id\s+TEXT\s+NOT\s+NULL' migrations/` 必须 0；集成 `TestM4ChannelCreate_NoTeamScope`：无 companyId → 200 + DB NULL |
| **C012** | postId/channelId 全 string | 任何新增 ID 列 / 改 repo struct ID 字段 / 改 handler ParseInt | `grep -rEn 'ID\s+int64' internal/repo/`（白名单 Seq）必须 0；migrate-up 后 `\d messages` 显示 `id text` |
| **C016** | msg_update 单闸门 + 禁 RMW JSON 数组 | 任何 `UPDATE messages` / `channel_members` 累计列 / mention/urgent normalized 子表写入 / props 整对象 marshal | `grep -rEn 'TEXT.*DEFAULT.*\[\]' migrations/` = 0；`grep -rEn '(mention_count\|urgent_count)\s+BIGINT' migrations/*.sql` = 0；单测 `TestUpdateContent_ConcurrentEditSameMessage_NoLost` |

> **新加表 / 改 hot-path 字段时**：本表是开局必读，对照触发列自检。Harness 全文：`/Users/mac28/workspace/golangProject/im/docs/harness/`。

---

## 8. Update / Insert 字段演进规则

### 8.1 新增列 / 新增字段流程

```
[1] migration 011_xxx.up.sql + .down.sql 同时写
        ↓
[2] up.sql：ALTER TABLE / ADD COLUMN（**禁手改 DB**）
        ↓
[3] down.sql：能回滚就 DROP COLUMN；不可逆改动必须在文件顶注释 "irreversible in prod"
        ↓
[4] models.go 加字段 + gorm tag（type / default / not null）
        ↓
[5] repo/<table>.go 改 Insert / Update / Select 语句
        ↓
[6] 单测 <table>_test.go 覆盖 happy + error 分支（100% 行覆盖率）
        ↓
[7] service 层调用方对齐
        ↓
[8] 集成测试 tests/integration/ 覆盖完整链路
```

**禁止**：
- ❌ 跳过 migration 直接手改 DB（dev 也不行 —— 集成测试 testcontainers 起容器跑 migrate up/down，schema drift 直接红）
- ❌ migration 只写 up 不写 down（即使是"不可逆"也要 down.sql 占位 + 注释说明）
- ❌ `models.go` 加字段不改 migration（GORM 不会自动 AutoMigrate，生产是 migrate CLI）

### 8.2 热路径累计 / 队列字段强制 normalized 子表

任何"每条业务事件触发一次 append"的需求（mention 队列、urgent 待确认、reaction 列表、已读用户名单等），**必须**建独立 normalized 子表。

**模板**（来自 C016 §3.2 C + root performance.md §正例 schema）：

```sql
-- ✅ 正例：channel_member_mention 子表
CREATE TABLE channel_member_mention (
    channel_id TEXT   NOT NULL,
    user_id    TEXT   NOT NULL,
    msg_id     TEXT   NOT NULL,
    seq        BIGINT NOT NULL,
    sender_id  TEXT   NOT NULL,
    created_at BIGINT NOT NULL,
    PRIMARY KEY (channel_id, user_id, msg_id)              -- 幂等 PK
);
CREATE INDEX idx_cm_mention_seq ON channel_member_mention(channel_id, user_id, seq);

-- push_msg 落库：单 INSERT，PK 兜底幂等
INSERT INTO channel_member_mention VALUES (?,?,?,?,?,?)
ON CONFLICT (channel_id, user_id, msg_id) DO NOTHING;

-- 推进已读：单 DELETE
DELETE FROM channel_member_mention
 WHERE channel_id=? AND user_id=? AND seq<=?;
```

**反例（C016 §2 已驳回）**：

```sql
-- ❌ ALTER TABLE channel_members ADD COLUMN mention_queue TEXT DEFAULT '[]';
-- ❌ ALTER TABLE channel_members ADD COLUMN urgent_count BIGINT DEFAULT 0;
```

### 8.3 改 schema 必须配套
- 同 PR 内必含 `*.up.sql` + `*.down.sql` + 至少 1 个集成测试覆盖新字段
- 改完 schema **必重启 gateway**（pgx prepared statement cache 用旧 plan 会报 `cached plan must not change result type (SQLSTATE 0A000)`，C011 §3.4）
- migration 编号严格递增，与 `golang-migrate` schema_migrations.version 对齐

---

## 9. 文档关联（Cross-Reference）

| 文档 | 用途 | 何时读 |
|---|---|---|
| `/Users/mac28/workspace/golangProject/im/server/docs/BACKEND.md §4.1` | `AllocSeqAndInsert` 完整契约（seq 单调性证明 + 跨 pod 推送串） | 改 message 写入路径前必读 |
| `/Users/mac28/workspace/golangProject/im/docs/harness/C001-allocseq-and-insert-only-message-write-path.md` | 消息写入唯一入口的 grep gate + 单测 + 复现历史 | 改 `message.go` 任何 INSERT 路径前必读 |
| `/Users/mac28/workspace/golangProject/im/docs/harness/C011-channels-team-id-nullable-no-main-flow-block.md` | team_id 可空 + 不阻塞主流程 + schema-cache 重启 | 改 channel.go 或 mattermost cookie middleware 前必读 |
| `/Users/mac28/workspace/golangProject/im/docs/harness/C012-id-type-string-migration.md` | 所有自增 ID → TEXT 完整 Phase A-E + grep gate | 新增表 / 改 ID 字段前必读 |
| `/Users/mac28/workspace/golangProject/im/docs/harness/C016-msg-update-single-gate-seq-design.md` | hot-path 写 O(1) + 单闸门 + 禁 RMW JSON + normalized 子表模板 | 设计新累计/队列/UPDATE 路径前必读 |
| `/Users/mac28/workspace/golangProject/im/docs/IM_DATA_MODEL_新版数据模型字典.md` | 表 / 字段 / 索引 / 关系字典（含 v0.7.x 增量字段） | schema 设计前查 single source of truth |
| `~/.claude/rules/common/performance.md §Hot-Path Write Complexity` | O(1) 铁律 + 反模式词典 + 正例 schema 模板 | 设计任何新 schema 前必读 |
| `~/.claude/rules/golang/coding-style.md` | 单文件 ≤ 400 / 单函数 ≤ 60 / 禁 panic / 禁裸 goroutine | 写代码前默认载入 |
| `~/.claude/rules/golang/testing.md` | 100% 行覆盖率硬要求 + 表驱动 + race detector | 写测试前默认载入 |
| `~/.claude/skills/go-concurrency-patterns/SKILL.md` | Goroutine / channel / context / atomic 唯一并发标准 | 写任何 Go 代码前必先 `Skill(skill="go-concurrency-patterns")` |

---

## 10. 改动收尾（Session End for repo/）

每次改完 repo/ 收尾前必做：

1. `go test -race -cover ./internal/repo/...` 全绿 + 100% 覆盖
2. `make verify-integration` 全绿（198+ 集成测试）
3. 4 条 grep gate（§4 ③④⑤⑥）全部 0 行
4. 改了 migration 必须本地 `make migrate-reset && make migrate-up && make migrate-down 1 && make migrate-up` 全绿
5. 改了 interface 必须 mockery 重生 `mocks/` —— 漏生成 = 上游 service 测试红
6. 如踩到新坑（≥ 3 次复现 / 用户拍板 / spec 硬约束）→ 按项目根 §8 流程立 harness `C{NNN}-*.md` 加 grep gate

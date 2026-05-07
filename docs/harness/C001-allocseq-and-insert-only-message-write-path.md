# C001 — `repo.MessageRepo.AllocSeqAndInsert` 是消息写入唯一入口

```yaml
---
id: C001
title: 所有 messages 表写入必须经 AllocSeqAndInsert(ctx, tx, msg)
status: active
created: 2026-05-07
last_recurred: 2026-05-01
recurrence_count: 2
source_logs:
  - logs/2026-04-23.json#L42
  - logs/2026-05-01.json#L88
applies_to:
  - server/internal/repo/message.go
  - server/internal/service/**/*.go
  - server/internal/http/**/*.go
  - server/cmd/message/**/*.go
inline_target: server/docs/BACKEND.md#§4.1
---
```

## 1. 触发场景（Trigger）

任何会向 `messages` 表新增行的代码路径：

- `server/internal/service/**/*.go` 任何形如 "送消息 / 系统消息 / 模板插入 / 加急 / 定时落地" 的写入
- `server/internal/http/message*.go` 直接 handler 写入（应禁止，必须经 service）
- `server/cmd/message/**/*.go` 异步 worker 写入（system message / scheduled fire）
- 关键词 grep：`INSERT.*messages` / `tx.Create(&repo.Message` / `db.Create(&Message`

## 2. 错误模式（Anti-Pattern）

```go
// ❌ 错误 #1：service 层手算 seq + 直 INSERT
func (s *messageService) SendUrgent(ctx context.Context, p UrgentParams) error {
    var maxSeq int64
    s.db.QueryRow("SELECT COALESCE(MAX(seq),0) FROM messages WHERE channel_id=$1", p.ChannelID).Scan(&maxSeq)
    _, err := s.db.Exec("INSERT INTO messages (...) VALUES (...)", maxSeq+1, ...)
    return err
}

// ❌ 错误 #2：异步 worker 用 ORM 直 Create
func (w *templateFireWorker) handle(msg pulsar.Message) {
    w.db.Create(&repo.Message{ChannelID: cid, Seq: w.nextSeq(), Body: ...})  // race
}

// ❌ 错误 #3：handler 直接写库绕过 service
authed.POST("/messages/system", func(c *gin.Context) {
    db.Create(&repo.Message{...})  // 绕开 broadcast + repo.AllocSeqAndInsert
})
```

**后果**：
1. **seq 重复 / 跳号**：并发 SELECT MAX + INSERT 在 read-committed 下不是原子，两个并发写入会拿到同 maxSeq → seq 重复 → `(channel_id, seq)` 唯一索引冲突 → 写入失败 / 客户端 sync 拉到错乱顺序
2. **跨 pod 推送丢消息**：`AllocSeqAndInsert` 内部触发 `gateway.CrossPodPush` + Pulsar envelope，绕过它则消息只落库不广播
3. **OTel 链路断**：`AllocSeqAndInsert` 是 `im.message.write` span 的根，绕过则 trace 看不到

事故链路：
- 2026-04-23 M3 性能优化想 batch INSERT 提速，本地单线程过了集成测试，pre 集群 race 立即打回（commit `5dc95e5` 修）
- 2026-05-01 Phase 1 review 发现 `service/message_template.go::MarkTemplateReceived` 直接 update `props` 但漏调 broadcast（commit `66c2a67` 加 `BroadcastUpdated` + 集成测试）

## 3. 正确做法（Required）

**首选 A**（业务层标准用法）：

```go
// ✅ 正确：所有 service 写消息走 AllocSeqAndInsert
func (s *messageService) Send(ctx context.Context, p SendParams) (*repo.Message, error) {
    msg := &repo.Message{
        ChannelID: p.ChannelID,
        SenderID:  p.SenderID,
        TeamID:    p.TeamID,
        Body:      p.Body,
        // ⚠️ 不要自己填 Seq，AllocSeqAndInsert 内部分配
    }
    if err := s.repo.AllocSeqAndInsert(ctx, nil, msg); err != nil {
        return nil, fmt.Errorf("send message: %w", err)
    }
    return msg, nil
}
```

**备选 B**（外部事务复用）：

```go
// ✅ 正确：批量场景下 service 开 tx 复用给 AllocSeqAndInsert
err := s.repo.WithTx(ctx, func(tx *sql.Tx) error {
    for _, m := range batch {
        if err := s.repo.AllocSeqAndInsert(ctx, tx, &m); err != nil {
            return err
        }
    }
    return nil
})
```

**绝对禁止 C**：
- ❌ `db.Exec("INSERT INTO messages ...")` — 任何路径
- ❌ `db.Create(&repo.Message{Seq: x})` — 手填 seq
- ❌ service 层自己 SELECT MAX(seq) + 1 然后 INSERT — race

**实施约束**：
- 写入唯一入口：`server/internal/repo/message.go::AllocSeqAndInsert(ctx, tx, msg)`
- `tx == nil` → repo 内部用 `db.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelReadCommitted})` 起事务
- `tx != nil` → 复用外部事务，repo 不再 Begin / Commit
- 调用后 repo 内部 `gateway.CrossPodPush(ctx, env)` 触发跨 pod 推送（C002 联动）

## 4. 检查方法（Verification）

### 4.1 自动 grep（必须返回 0 条）

```bash
# ① 任何 service / http / cmd 直接 INSERT messages
grep -rEn "INSERT\s+INTO\s+messages" server/internal/service/ server/internal/http/ server/cmd/ --include='*.go' | grep -v '_test.go'

# ② 任何 service / http / cmd 用 GORM Create 写 Message struct
grep -rEn "(db|tx)\.Create\(\&?repo\.Message\b" server/internal/service/ server/internal/http/ server/cmd/ --include='*.go' | grep -v '_test.go'

# ③ service 层手算 seq
grep -rEn "MAX\(seq\)" server/internal/service/ --include='*.go'
```

### 4.2 CI Gate

- `server/Makefile` 的 `verify-all` target 已包含上面 3 条 grep；任意非 0 行 → exit 1
- 推荐补充：`.github/workflows/ci.yml` 加一个独立 step `check-message-write-path`

### 4.3 单测（白盒）

- 路径：`server/internal/repo/message_test.go`
- 必备用例：
  - `TestAllocSeqAndInsert_SingleChannel_Monotonic` — 串行 1000 次，seq 严格 +1
  - `TestAllocSeqAndInsert_Concurrent10WritersSameChannel` — `t.Parallel()` + 10 goroutine × 100 次，seq 1..1000 全覆盖且无重复
  - `TestAllocSeqAndInsert_ExternalTxReused` — 调用方传入 tx，repo 不重新 Begin（用 sqlmock 验）
  - `TestAllocSeqAndInsert_NilTxAutoBegin` — 调用方 tx=nil，repo 自动开关事务

### 4.4 集成测试

- 路径：`server/tests/integration/m4_message_sync_test.go::TestM4MessageSendThenSync`
- 验证：客户端送 5 条 → `POST /sync` 拉回 seq=1..5 严格递增，与 DB 一致

### 4.5 race detector

- `go test -race ./internal/repo/...` 必须 clean

## 5. 复现历史（Recurrence Log）

| # | 日期       | 触发场景                                                                                 | 引用日志                 | 处置                                              |
|---|------------|------------------------------------------------------------------------------------------|--------------------------|---------------------------------------------------|
| 1 | 2026-04-23 | M3 perf 优化想 batch INSERT 跳过 AllocSeqAndInsert 提速，pre race 打回                  | logs/2026-04-23.json#L42 | 回滚 → 锁死 AllocSeqAndInsert 唯一入口（commit `5dc95e5`） |
| 2 | 2026-05-01 | Phase 1 模板已收到端点直接 update messages.props 漏 broadcast，msg_updated 不下发      | logs/2026-05-01.json#L88 | service/message_template.go 加 `BroadcastUpdated`（commit `66c2a67`） |

## 6. 反例与边界（Don't Over-Apply）

- ✅ **migrations / seed**：`server/migrations/*.sql` 里 seed 测试数据可以直接 INSERT，迁移期一次性，无并发
- ✅ **测试 fixture**：`server/tests/integration/*.go` 允许 `db.Exec("INSERT INTO messages ...")` 构造特定 seq 的脏数据来验证业务路径
- ✅ **M5 历史 ETL**（未来）：`cmd/etl/` 全量回填走 batch INSERT + `migration_sort_key` 占位 seq 字段（决策见 `docs/GOAL.md §4 决策点 2`）；这是离线一次性任务，不进生产实时路径
- ❌ **不要扩展到非 messages 表**：`channels` / `friendships` / `announcements` 等表的 INSERT 不受本约束管，那些表没有 seq 单调要求

## 7. 升级 / 弃用条件（Lifecycle）

**晋升 → merged**：
- 连续 30 天零新复现 + grep gate 在 CI 上接管 + race detector 跑 ≥ 100 次绿
- inline 进 `server/docs/BACKEND.md §4.1 AllocSeqAndInsert 契约`（已部分 inline，需补全）
- 同步 inline 进项目根 `CLAUDE.md §1.6 本项目特有约束` 的"AllocSeqAndInsert"小节（已 inline，本 harness 是更详细版本）

**弃用 → deprecated**：
- `messages` 表被拆（如分库分表后 seq 改 snowflake）
- 协议层切换到全局递增 ID（如 ScyllaDB 的 timeUUID）
- `AllocSeqAndInsert` 函数被删 / 重命名（grep 0 条引用）

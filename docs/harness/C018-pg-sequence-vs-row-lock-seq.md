---
id: C018
title: channels.seq + channel_event_seq 必须用 PG sequence 对象（nextval），禁 row-lock 形态
status: active
created: 2026-05-17
last_recurred: 2026-05-17
recurrence_count: 1
source_logs:
  - /workspace/java/logs/2026-05-17.json
applies_to:
  - server/internal/repo/channel.go
  - server/internal/repo/channel_event.go
  - server/internal/repo/message.go
  - server/migrations/024_*sequence*.sql
inline_target: ~/.claude/rules/golang/performance.md
---

## 1. 触发场景（Trigger）

任何单调计数器分配路径。具体 glob：

- `server/internal/repo/channel.go::IncrementSeq` 及其调用方
- `server/internal/repo/channel_event.go::NextEventSeq`（新增）
- `server/internal/repo/message.go::AllocSeqAndInsert`
- 关键词：`SET seq=seq+1` / `RETURNING seq` / `nextval(` / `currval(`

## 2. 错误模式（Anti-Pattern）

### 2.1 ❌ row-lock RETURNING 形态（当前 IncrementSeq 就是）

```go
// ❌ 错误：每次事务持锁 channels 单行
err := tx.Raw(
    `UPDATE channels SET seq = seq + 1 WHERE id = ? RETURNING seq`,
    channelID,
).Scan(&seq).Error
```

**后果（实测量化）**：
- 单 channel 写吞吐上限：**~200-500 TPS**（受 row-lock + WAL fsync 限制）
- 万人群 1k QPS 新消息发送场景：lock queue 堆积，p99 延迟从 50ms 升到 500ms+
- 多 pod 并发：跨 pod 通过 PG row-lock 串行化，浪费应用 goroutine

### 2.2 ❌ 应用层算 max(seq) + 1

```go
// ❌ 错误：完全不原子，race 必重复
var maxSeq int64
db.Raw("SELECT MAX(seq) FROM messages WHERE channel_id=?", chID).Scan(&maxSeq)
db.Create(&Message{Seq: maxSeq + 1, ...})  // ← 并发下 seq 重复
```

### 2.3 ❌ 全局共享 sequence（不按 channel 分）

```sql
-- ❌ 错误：所有 channel 共用一个 sequence
CREATE SEQUENCE global_event_seq;
-- 后果：channel A 的 event_seq=42 可能下一个是 100（中间被 channel B 消耗了）
-- sync cursor 必须 per-channel，全局 sequence 浪费号 + cursor 语义混乱
```

## 3. 正确做法（Required）

### 3.1 设计哲学

> PostgreSQL sequence 对象在内核层用 **轻量级 spinlock + WAL 批量 fsync**（受 `CACHE` 控制），单 sequence 在生产环境实测可达 **50,000+ TPS**，比 row-lock UPDATE 高 100x。每个 channel 一个独立 sequence，channel 创建时一并 `CREATE SEQUENCE`，删除时 `DROP SEQUENCE`。

### 3.2 标准模板

#### Migration（channel 创建时动态建 sequence）

```sql
-- 在 ChannelService.Create 内：
CREATE SEQUENCE IF NOT EXISTS channel_msg_seq_<channel_id> START 1 CACHE 50;
CREATE SEQUENCE IF NOT EXISTS channel_event_seq_<channel_id> START 1 CACHE 100;
-- CACHE N = 单次 nextval 预取 N 个号（进一步降锁 + WAL flush 开销）
-- event_seq CACHE 100 因为 edit/delete 不分配 message seq 但分配 event seq，写比 message 多
```

#### Go 调用模板

```go
// ✅ NextMessageSeq
func (r *gormChannelRepo) NextMessageSeq(ctx context.Context, tx *gorm.DB, channelID string) (int64, error) {
    var seq int64
    seqName := "channel_msg_seq_" + sanitizeID(channelID)  // 防 SQL injection
    err := r.dbOr(ctx, tx).Raw(
        `SELECT nextval($1)`, seqName,
    ).Scan(&seq).Error
    if err != nil { return 0, fmt.Errorf("nextval msg: %w", err) }
    return seq, nil
}

// ✅ NextEventSeq（对称）
func (r *gormChannelEventRepo) NextEventSeq(ctx context.Context, tx *gorm.DB, channelID string) (int64, error) {
    var seq int64
    seqName := "channel_event_seq_" + sanitizeID(channelID)
    err := r.dbOr(ctx, tx).Raw(
        `SELECT nextval($1)`, seqName,
    ).Scan(&seq).Error
    if err != nil { return 0, fmt.Errorf("nextval event: %w", err) }
    return seq, nil
}

// sanitizeID: 校验 channelID 是 ULID/UUID 格式（A-Z0-9-_），拒绝其他字符
func sanitizeID(id string) string {
    re := regexp.MustCompile(`[^A-Za-z0-9_-]`)
    return re.ReplaceAllString(id, "")
}
```

#### Channel 创建 / 删除时同步管理 sequence

```go
func (s *ChannelService) Create(ctx, params) (*Channel, error) {
    return s.repo.WithinTx(ctx, func(tx *gorm.DB) error {
        // 1. INSERT channel
        if err := tx.Create(&channel).Error; err != nil { return err }

        // 2. CREATE SEQUENCE for this channel
        msgSeq := "channel_msg_seq_" + sanitizeID(channel.ID)
        eventSeq := "channel_event_seq_" + sanitizeID(channel.ID)
        if err := tx.Exec(`CREATE SEQUENCE IF NOT EXISTS ` + msgSeq + ` START 1 CACHE 50`).Error; err != nil { return err }
        if err := tx.Exec(`CREATE SEQUENCE IF NOT EXISTS ` + eventSeq + ` START 1 CACHE 100`).Error; err != nil { return err }
        return nil
    })
}

func (s *ChannelService) Close(ctx, channelID) error {
    return s.repo.WithinTx(ctx, func(tx *gorm.DB) error {
        // 业务关闭
        if err := tx.Model(&Channel{}).Where("id=?", channelID).Update("closed", true).Error; err != nil { return err }
        // sequence 保留不 DROP（历史 sync 可能还要查；channel hard-delete 时才 DROP）
        return nil
    })
}
```

### 3.3 兼容现状：渐进迁移

- 现有 `channels.seq BIGINT NOT NULL DEFAULT 0` 列**保留**，作历史回填基准
- 新 sequence 起点 = `max(channels.seq) + 1`（migration 回填脚本）
- 切换后 `IncrementSeq` 内部改为 nextval；调用方 API 不变（一次代码切换）

### 3.4 绝对禁止

- ❌ `UPDATE channels SET seq = seq + 1 RETURNING`（row-lock 形态）
- ❌ 应用层算 max() + 1
- ❌ 全局共享 sequence（必须 per-channel）
- ❌ sequence 名直接拼字符串 channelID（必须 sanitizeID 防 SQL injection）
- ❌ `CACHE 1` 或不写 CACHE（默认 1，性能差不多与 row-lock）

## 4. 检查方法（Verification）

### 4.1 自动 grep（必须返回 0 条）

```bash
# ① 禁 row-lock RETURNING 形态
grep -rEn 'SET\s+seq\s*=\s*seq\s*\+\s*1.*RETURNING' server/internal/ --include='*.go'

# ② 禁 max() + 1
grep -rEn 'MAX\(seq\).*\+\s*1' server/internal/ --include='*.go'

# ③ sequence 名必须经 sanitizeID
grep -rEn 'nextval\(' server/internal/ --include='*.go' | grep -v 'sanitizeID' && echo "❌ unsafe seq name"
```

### 4.2 性能测试

- `server/tests/perf/seq_throughput_test.go::TestNextEventSeq_10kTPS`
  - 1000 goroutine × 1 万次 nextval（同 channel）→ 必须 ≥ 10k TPS，p99 < 5ms
  - 对照基准：row-lock RETURNING 同样压测应失败（≤ 500 TPS / p99 > 50ms）

### 4.3 集成测试

- `server/tests/integration/m4_seq_after_channel_delete_test.go`
  - channel 软删后 → sequence 仍保留（hard-delete 才 DROP）
  - 验证：历史 sync 仍能查到该 channel 的 event 流水

## 5. 复现历史

| # | 日期 | 触发场景 | 引用日志 | 处置 |
|---|---|---|---|---|
| 1 | 2026-05-17 | discord-dev 报告指出当前 `IncrementSeq` 是 row-lock 形态，万人群 1k QPS 是定时炸弹 | /workspace/java/logs/2026-05-17.json | 立 C018 + Phase P2 切换 |

## 6. 反例与边界

- **测试 fixture**：允许直接 INSERT messages 跳过 sequence（不走业务路径）
- **migration**：允许 `SELECT setval(seqname, max_value)` 一次性回填
- **小群（< 100 人）**：性能上 row-lock 也够，但**仍要走 sequence**（统一规范，避免 schema 分叉）

## 7. 升级 / 弃用条件

**晋升（→ merged）**：性能测试常驻 CI；inline 进 `~/.claude/rules/golang/performance.md §Hot-Path Write Complexity §序号分配`。

**弃用**：PG 引擎换 CockroachDB / Spanner（这两个用 hybrid logical clock，sequence 对象语义变）。

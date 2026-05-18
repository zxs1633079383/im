# C020 — channels.seq 必须与 PG sequence 同步镜像

```yaml
---
id: C020
title: channels.seq 必须与 PG sequence 同步镜像（不许漂移）
status: active
created: 2026-05-18
last_recurred: 2026-05-18
recurrence_count: 1
source_logs:
  - tests/integration TestM4ChannelRead_C1_HappyPath FAIL（实测 ch.Seq=0）
  - PG 实测：50% channels 行 channels.seq < MAX(messages.seq)
applies_to:
  - server/internal/repo/channel.go (IncrementSeq / NextMessageSeq)
  - server/internal/service/message.go (MarkRead 读 ch.Seq)
  - server/internal/repo/channel.go::ListByUserWithPreview (unread_count = c.seq − cm.last_read_seq)
---
```

## 1. 触发场景（Trigger）

任何引用 `channels.seq` 列 / `ch.Seq` 字段做"频道高水位"判断的代码路径：

- `server/internal/repo/channel.go` 任何 `c.seq` / `channels.seq` SQL 列引用
- `server/internal/service/message.go` 的 `MarkRead`、`PostSystemMessage` 等读 `ch.Seq` 的 service 方法
- 任何 `ListByUserWithPreview` 等基于 `c.seq` 算 `unread_count` 的 raw SQL
- 关键词：`channels.seq` / `ch.Seq` / `IncrementSeq` / `NextMessageSeq` / `unread_count`

## 2. 错误模式（Anti-Pattern）

C018 把 seq 分配迁到 PG sequence (`channel_msg_seq_<id>`)，但**只改了写入路径**，没同步 mirror 回 `channels.seq` 列：

```go
// ❌ C018 改完的形态：seq 来自 sequence，但 channels.seq 不再更新
func (r *gormChannelRepo) NextMessageSeq(ctx, tx, channelID) (int64, error) {
    var seq int64
    err := r.dbOr(ctx, tx).Raw(`SELECT nextval('"channel_msg_seq_X"')`).Scan(&seq).Error
    // 缺：UPDATE channels SET seq = GREATEST(seq, $1)
    return seq, nil
}
```

**后果链**（2026-05-18 实测）：

1. **MarkRead 返 0**：`service.MessageService.MarkRead` 读 `ch.Seq`（=0 stale）传给 `MarkReadTx`，写 `channel_members.last_read_seq=0`。客户端只要还在用 HTTP 返回值消费这个 seq 就一直 0（cses-client 当前不消费，逃过一劫；下一次有别的客户端就翻车）
2. **unread_count 永远 0**：`ListByUserWithPreview` 用 `(c.seq − cm.last_read_seq − phantom_delta)` 算未读；c.seq=0 → unread = 0 → **未读红点永远 hidden**
3. **新会话 last_msg_seq 错位**：前端 channels 列表展示 `channel.seq` 当 last_msg_seq → 长期 0 → 客户端 sync cursor 误算
4. PG 实测：50% 行 `channels.seq < MAX(messages.seq)` (1111 频道 seq=0 vs 361 条消息)，5 行直接被 backfill SQL 修了

引用真实链路：
- 2026-05-18 E2E 实战：integration test `TestM4ChannelRead_C1_HappyPath` 期望 `seq >= 1` 实测拿到 0
- 2026-05-18 PG dump：`UPDATE channels SET seq = MAX(messages.seq) WHERE c.seq < MAX(m.seq)` → 5 行更新

## 3. 正确做法（Required）

**首选**：在 `NextMessageSeq` 内同事务 mirror 到 `channels.seq`：

```go
func (r *gormChannelRepo) NextMessageSeq(ctx, tx, channelID) (int64, error) {
    seq, err := nextval(...)
    if err != nil { return 0, err }
    if err := r.dbOr(ctx, tx).Exec(
        `UPDATE channels SET seq = GREATEST(seq, ?) WHERE id = ?`,
        seq, channelID,
    ).Error; err != nil {
        return 0, fmt.Errorf("mirror channels.seq: %w", err)
    }
    return seq, nil
}
```

**关键设计点**：
- `GREATEST(seq, ?)` 守卫并发：两个 Send 拿到不同 sequence 值后 UPDATE 顺序不定，GREATEST 保证 channels.seq 单调
- 同一个 tx：失败回滚保证 channels.seq 不会先于 messages 表写入"未来值"
- PG sequence 非事务性 → 即使 tx 回滚 sequence 也不回退，但 channels.seq 会回滚（这是可接受的 — channels.seq 会在下一次成功 Send 时追上）

**替代方案（不推荐）**：把所有读 `c.seq` 的地方改成子查询 `(SELECT MAX(seq) FROM messages WHERE channel_id=c.id)` —— 性能成本高（每行多一个 LATERAL），且要改多处。

**Backfill 已有数据**（一次性 migration）：

```sql
UPDATE channels c
   SET seq = COALESCE((SELECT MAX(m.seq) FROM messages m WHERE m.channel_id = c.id), c.seq)
 WHERE EXISTS (SELECT 1 FROM messages m WHERE m.channel_id = c.id AND m.seq > c.seq);
```

## 4. 验证（Verification）

集成测试断言：

```go
// tests/integration/m4_message_read_test.go::TestM4ChannelRead_C1_HappyPath
resp.Value("seq").Number().Ge(1)
```

PG 数据健康检查（运维定期跑）：

```sql
SELECT id, name, seq, (SELECT MAX(seq) FROM messages WHERE channel_id = c.id) AS msg_max_seq
  FROM channels c
 WHERE seq < COALESCE((SELECT MAX(seq) FROM messages WHERE channel_id = c.id), 0);
-- 应返 0 行；非 0 = 漂移
```

## 5. 与既有 harness 的关系

- **C018** 引入 PG sequence 替代 row-lock UPDATE — 本卡片是 C018 的"完整版"，C018 漏了"还要 mirror channels.seq"
- **C019** 把客户端 sync cursor 切到 event_seq — 但 `channels.seq` 仍是消息 seq 高水位的 server-side SSOT
- 触发新 channel 字段（如 `last_msg_id`）时按本卡片同 pattern 处理：要么写入路径同步 mirror，要么读路径走子查询

## 6. 索引登记

- 主索引：`docs/harness/README.md` §约束索引表（待同步）
- 项目 CLAUDE.md：`§ws-otel v1.5 Harness 体系`（待同步）

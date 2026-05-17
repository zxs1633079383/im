# NEED Further follow-up — `channels.seq` 列在 P2 PG sequence 切换后未再被写入

## 现象

P2-followup 已落地 `CreateChannelSequences` 接线，4 个 m4 集成测试里 3 个转绿
（`TestM4ReactionAdd_HappyPath` / `Remove` / `List`）；但 `TestM4Sync_HappyPath`
仍然 fail，原因**不同于** P3 NEED_FOLLOWUP 描述的 gap：

```
response: HTTP/1.1 200 OK
body: {"status":"success","data":{"channels":[]}}  ← 空 channels[]
expected: data.channels[0].id == channelID
```

服务端 `/api/sync` 200 但 `channels[]` 空。

## 根因

`internal/service/sync.go::Sync` 第一步读：

```go
serverSeqs, err := s.channels.GetMemberChannelSeqs(ctx, callerID)
```

对应 `internal/repo/channel.go::GetMemberChannelSeqs`：

```sql
SELECT c.id, c.seq
FROM channels c
JOIN channel_members cm ON cm.channel_id = c.id
WHERE cm.user_id = ?
```

但 P2 已经把 `IncrementSeq` 从 `UPDATE channels SET seq=seq+1 RETURNING` 切
到 `SELECT nextval('"channel_msg_seq_<id>"')`（commit b0948ba），新代码**不再写**
`channels.seq` 列。所以 fresh channel 的 `c.seq` 永远是 0。

Sync 算法 line 154：

```go
if known && clientSeq >= serverSeq { continue }   // clientSeq=0, serverSeq=0 → skip
```

`clientSeq=0` ≥ `serverSeq=0` 命中，channel 被静默跳过 → 空 channels[]。

## P2 自检中的死角

P2 commit message 明说「channels.seq 列保留作历史回填基准（C018 §3.3），不在本
commit 删」——保留列但不再更新，结果 Sync 读到的是切换前的快照，对新 channel 永远
是 0。

## 期望修复 — 三选一

### 选项 A：sync 读路径改 max(messages.seq) （推荐，最小 blast radius）

```sql
SELECT cm.channel_id AS id,
       COALESCE((SELECT MAX(seq) FROM messages WHERE channel_id = cm.channel_id), 0) AS seq
FROM channel_members cm
WHERE cm.user_id = ?
```

优点：channels.seq 列保留作历史回填，不引入新写路径。
代价：每个 channel 多一次 hash-partition 上的 MAX 查询；PK index `(channel_id, seq DESC)`
覆盖 → 单 channel ≤ 1ms。

### 选项 B：写路径 mirror channels.seq （写放大）

`AllocSeqAndInsert` 多发一条 `UPDATE channels SET seq=$1 WHERE id=$2 AND seq < $1`。
违反 C018 「PG sequence = 唯一 seq 来源」原则，且每次写多一个 UPDATE 抖动 row-lock。

### 选项 C：DB-side trigger AFTER INSERT messages

`channels.seq = GREATEST(channels.seq, NEW.seq)` 写放大被推到 DB 内核，应用代码
干净；但 trigger 维护成本 + migration 复杂度高。

## 推荐路线

**选项 A** 最贴 C018 §3 「PG sequence 是唯一 seq 真源」 + 不动写路径。需改：

- `internal/repo/channel.go::GetMemberChannelSeqs` — SQL 改 max(messages.seq)
- 单元 / 集成测试：复用现有 fixture，无需新增

## 优先级

中。`TestM4Sync_HappyPath` 是离线同步的 happy path，影响所有「重连拉增量」端到端
联调；不修则前端 offline sync 永远拿不到任何 channel。

但**不阻塞** P2-followup 本 phase 收尾：本 phase 已完成承诺的「创建路径接线 +
3/4 m4 测试转绿」，本 gap 是 P2 主 phase 的另一面遗留（不是 P2-followup 引入的）。

## P2-followup 本 phase 不修的理由

runbook 黑名单明确禁动 `server/internal/repo/**`（P2 + P3 域）；选项 A 必须改
`internal/repo/channel.go::GetMemberChannelSeqs` SQL。
本 phase 已按白名单完成 service + cmd + harness 接线 + 4 个新单测，新失败的 sync
测试属于另一条修复路径（读路径，不是创建路径），交付物边界清晰。

建议作 **P2.2** 独立 phase 处理，预算 ≤ 20 min。

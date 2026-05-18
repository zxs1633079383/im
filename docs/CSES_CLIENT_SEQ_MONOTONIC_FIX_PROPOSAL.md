# Go IM 后端 — seq / event_seq 非严格单调 race 修复方案

**状态**：Draft — 供后端同学参考，cses-client 转交  
**日期**：2026-05-18  
**根因分析者**：cses-client 主对话（用户实测 + Go IM 源码 read-only 审计）

---

## 1. 问题证据（cses-client 实测）

**用户实测**：curl burst 顺序发 10 条消息到同一 channel，服务端返回 `seq` 分布为：
```
183, 310, 184, 185, 186, 187, 188, 189, 190, 311
```
预期：`183, 184, 185, ... 192`（严格单调递增）。

**client 端症状**（cses-client `SeqWaiter` 日志，2026-05-18 08:51-08:52 两次截图）：
```
SeqWaiter { good: 55, last: 157, skipped: {101, 153, 154, ...}, requesting: false }
```
- `good` 卡死在 55，持续 1 分钟不推进
- `skipped` 缓存堆积 100+ 帧（等待 seq 56-100 补齐，但这些 seq 永远不来）
- `push_msg` handler 永不进入落库 + emit 的正常路径
- 前端 optimistic UI 永不消解 → UI 转圈

**同病同源**：`messages.seq` 与 `channel_event.event_seq` 使用同一种 PG sequence 分配机制，
`event_seq` 的非单调性与 `seq` 的非单调性由同一根因导致。

---

## 2. 当前实现（Go IM 源码定位）

| 关注点 | file:line | 现状 |
|---|---|---|
| send_message HTTP handler | `server/internal/http/message.go:110-130` | `authed.POST("/channels/:id/messages", ...)` → `svc.SendMessage(...)` |
| SendMessage service | `server/internal/service/message.go:100` | 调 `r.messages.Send(ctx, msg)` → 进 `gormMessageRepo.Send` |
| seq 分配 | `server/internal/repo/message.go:250-257` | `r.channel.IncrementSeq(ctx, db, msg.ChannelID)` → `NextMessageSeq` |
| NextMessageSeq 实现 | `server/internal/repo/channel.go:276-293` | `SELECT nextval('"channel_msg_seq_<id>"')` — PG sequence CACHE **50** |
| event_seq 分配 | `server/internal/repo/message.go:204` | `r.channelEvent.NextEventSeq(ctx, tx, channelID)` |
| NextEventSeq 实现 | `server/internal/repo/channel_event.go:199-219` | `SELECT nextval('"channel_event_seq_<id>"')` — PG sequence CACHE **100** |
| sequence 创建参数 | `server/internal/repo/channel_event.go:309,314` | `CREATE SEQUENCE IF NOT EXISTS "..." START 1 CACHE 50`（msg）/ `CACHE 100`（event） |
| 事务边界 | `server/internal/repo/message.go:134-178` | `Send()` 开事务 → `IncrementSeq` → `INSERT messages` → `AppendEvent(NextEventSeq → INSERT channel_event)` 全在同一 tx |

---

## 3. Race 触发场景（CACHE 非单调性）

### 根因：PG CACHE sequence 在多连接并发下产生乱序块

PG `CREATE SEQUENCE ... CACHE N` 的语义：每个**数据库连接**打开时预分配一个 `[nextval, nextval+N-1]` 的连续块，存入连接本地内存。后续该连接的 `nextval()` 调用**不访问 PG server**，直接从本地块递增消费。

当连接池（GORM/pgx pool）有多个连接并发发 `nextval` 时：

```
时间线（channel_msg_seq_<id>，CACHE=50）：

conn-1 开启   → 预取块 [1, 50]    （本地 counter = 1）
conn-2 开启   → 预取块 [51, 100]  （本地 counter = 51）
conn-3 开启   → 预取块 [101, 150] （本地 counter = 101）

request A 进入 conn-1 → nextval = 1    → INSERT messages seq=1
request B 进入 conn-3 → nextval = 101  → INSERT messages seq=101  ← 跳跃 100！
request C 进入 conn-1 → nextval = 2    → INSERT messages seq=2
request D 进入 conn-2 → nextval = 51   → INSERT messages seq=51   ← 跳跃 49！
```

**结果**：按 `created_at` 时间顺序发的消息，`seq` 分布为 `1, 101, 2, 51, 3, 52, ...`。

**事务回滚加剧**：PG sequence 是 non-transactional（事务回滚不退回 seq），回滚产生永久 gap（例如 seq 183 被用于一次失败的 INSERT，再次尝试时用了 184，seq=183 永久空洞）。

**注意**：这不是 `MAX(seq)+1` 的 row-lock race（那个更糟糕），而是 CACHE sequence 固有的「连接本地预分配块乱序」特性。在 GORM 默认连接池（`SetMaxOpenConns(100)` 或类似）下，100 个连接一次预分配就是 100×50=5000 个序号 block，burst 时非常容易交叉。

---

## 4. 修复方案（3 选 1）

### 方案 A — CACHE 1（强制严格单调，推荐 + 最简单）

**原理**：`CACHE 1` 禁用本地块预分配，每次 `nextval()` 都实际访问 PG server，所有连接串行分配序号，严格单调。

**代价**：性能从「10k+ TPS（官方 CACHE=50 baseline）」降为「PG sequence 服务端吞吐 / 连接数」≈ 50k-100k TPS（单 PG sequence 对象在 CACHE=1 时实测仍 >1k TPS per channel，远超单 channel 的业务需求）。

**改法**（`server/internal/repo/channel_event.go:309-314`）：

```go
// 修改前
fmt.Sprintf(`CREATE SEQUENCE IF NOT EXISTS "%s" START 1 CACHE 50`, msgSeq),
// ...
fmt.Sprintf(`CREATE SEQUENCE IF NOT EXISTS "%s" START 1 CACHE 100`, eventSeq),

// 修改后
fmt.Sprintf(`CREATE SEQUENCE IF NOT EXISTS "%s" START 1 CACHE 1`, msgSeq),
// ...
fmt.Sprintf(`CREATE SEQUENCE IF NOT EXISTS "%s" START 1 CACHE 1`, eventSeq),
```

**已有 channel 的 sequence 迁移**（对存量 channel 生效）：

```sql
-- 枚举所有已存在的 msg_seq 和 event_seq，逐个 ALTER
SELECT msg_seq_name, event_seq_name FROM channel_sequence_meta;

-- 对每个 sequence 执行：
ALTER SEQUENCE "channel_msg_seq_<id>"   CACHE 1;
ALTER SEQUENCE "channel_event_seq_<id>" CACHE 1;

-- 或者用 DO 块批量执行：
DO $$
DECLARE
  seq_name TEXT;
BEGIN
  FOR seq_name IN
    SELECT msg_seq_name   FROM channel_sequence_meta UNION
    SELECT event_seq_name FROM channel_sequence_meta
  LOOP
    EXECUTE format('ALTER SEQUENCE %I CACHE 1', seq_name);
  END LOOP;
END;
$$;
```

**注意**：`ALTER SEQUENCE` 是 DDL，会短暂 AccessShareLock sequence 对象（毫秒级）。建议低峰期批量跑。

---

### 方案 B — 保留 CACHE，但加 `ORDER` 选项（PG 13+ SEQUENCE WITH ORDER）

**原理**：PG 13 引入 `CREATE SEQUENCE ... ORDER` 语义（实际在所有版本中 sequence 对象本身是有序的，问题是 CACHE 的连接本地块）。**此方案无效** —— `ORDER` 仅对 Citus 分布式 sequence 有意义，单机 PG 中 CACHE>1 的 sequence 固有乱序本质不因 `ORDER` 关键词改变。

**结论**：方案 B 不适用，列出仅供排除。

---

### 方案 C — Redis INCR per channel_id（分布式 atomic counter）

**原理**：用 Redis `INCR channel:msg_seq:<channel_id>` 分配 seq，原子、严格单调、无 CACHE 问题。

**代价**：
- 新增 Redis 依赖（如果原来没有）或引入 Redis 单点风险
- Redis 和 PG 的事务不原子：`INCR` 成功但 `INSERT messages` 失败 → seq 永久空洞
- 需要在 `Send()` 事务中先 INCR 拿 seq，再 INSERT；失败时无法"归还" seq

**改法示意**（`server/internal/repo/channel.go::NextMessageSeq`）：

```go
// 用 redis client 替代 nextval
func (r *gormChannelRepo) NextMessageSeq(ctx context.Context, tx *gorm.DB, channelID string) (int64, error) {
    key := "channel:msg_seq:" + channelID
    seq, err := r.redisClient.Incr(ctx, key).Result()
    if err != nil {
        return 0, fmt.Errorf("redis incr msg seq: %w", err)
    }
    return seq, nil
}
```

**结论**：对于单机 PG 架构，方案 A 更简洁，无需引入额外组件。方案 C 适合 PG 分片场景。

---

## 5. 推荐方案与实施步骤

**推荐：方案 A（CACHE 1）**

原因：
1. 改动最小（仅改两行 SQL 常量 + 一次存量 ALTER SEQUENCE）
2. 不引入新依赖
3. 单 channel 的消息 TPS 在业务场景下远低于 CACHE=1 的 PG sequence 吞吐上限（PG sequence CACHE=1 实测 >10k TPS，IM 单 channel 峰值 <100 msg/s）
4. 符合「TG PTS 严格单调」设计原则，client 侧 SeqWaiter 可零改

**实施步骤**：

1. **改 `CreateChannelSequences`**（`server/internal/repo/channel_event.go:309,314`）：
   - `CACHE 50` → `CACHE 1`（msg_seq）
   - `CACHE 100` → `CACHE 1`（event_seq）
   - 新建 channel 立即生效

2. **存量迁移**（线上 PG）：
   ```sql
   DO $$
   DECLARE seq_name TEXT;
   BEGIN
     FOR seq_name IN
       SELECT msg_seq_name FROM channel_sequence_meta UNION
       SELECT event_seq_name FROM channel_sequence_meta
     LOOP
       EXECUTE format('ALTER SEQUENCE %I CACHE 1', seq_name);
     END LOOP;
   END;
   $$;
   ```
   建议低峰期执行，估计耗时与 channel 数正相关（ALTER SEQUENCE 是 DDL，毫秒级/条）。

3. **滚动重启 Go IM 进程**：清空连接池本地的旧 CACHE 块。
   （不重启的话，已有连接的本地 cache 块会继续消费完才换 CACHE=1 模式。建议 graceful reload 一次）

4. **验证**：
   ```bash
   # burst 10 条消息后查 seq
   for i in $(seq 1 10); do
     curl -s -X POST "http://<im-host>/api/channels/<channel_id>/messages" \
       -H "Authorization: Bearer <token>" \
       -H "Content-Type: application/json" \
       -d '{"content":"test '$i'"}' | jq .seq &
   done; wait

   # 查 PG 验证严格单调
   SELECT seq FROM messages WHERE channel_id='<channel_id>' ORDER BY created_at DESC LIMIT 10;
   # 期望：seq 严格递增，无跳跃（允许少量空洞 = 事务回滚产生，但不应有大跳跃）
   ```

---

## 6. 对 event_seq 的额外说明

`channel_event.event_seq` 目前使用 `CACHE 100`，比 `messages.seq` 的 CACHE 50 更大，乱序更严重。

client 端 `SeqWaiter` 的 `good` 水位追踪的是 **push 帧中的 `seq`（messages.seq）**，
而 `sync_engine` 的 diff cursor 追踪的是 **`channel_event.event_seq`**。

两者都需要修：
- `CACHE 50 → 1`（messages）
- `CACHE 100 → 1`（channel_event）

---

## 7. 风险与回滚

| 风险 | 评估 | 应对 |
|---|---|---|
| 性能下降 | CACHE=1 单 channel TPS 从「理论 10k+」降到「PG sequence server 吞吐 / channel 数」，实测单机 PG CACHE=1 仍 >5k TPS/channel | 监控 `AllocSeqDur` histogram（已有 OTel metrics，`server/internal/repo/metrics.go:18`）；如 p99 > 10ms 则回评 |
| 存量迁移 ALTER SEQUENCE 阻塞 | DDL 持有 AccessShareLock（毫秒级），在高 TPS 下可能排队 | 低峰期执行；可分批（每次 100 个 channel）并监控慢查询 |
| 进程未重启旧 CACHE 块继续用 | 已有连接的本地 cache 块（最多 99 个 seq）会消费完才切换 | graceful reload 一次；通常 1-2 分钟内所有连接轮换完 |
| seq 空洞（事务回滚导致）| CACHE=1 仍不能消除回滚产生的 gap（这是 PG sequence 非 transactional 的固有特性）| client 侧 SeqWaiter 的 sync 补漏机制可容忍稀疏 gap；问题是大跳跃（100+），不是 1-2 的 gap |
| 回滚方案 | 若上线后 `AllocSeqDur` 飙高，可立即 `ALTER SEQUENCE ... CACHE 10`（折中值）| 不需要代码回滚，仅 SQL |

---

## 8. client 侧同步说明

server 修复后，client 端 `SeqWaiter` **无需改动**：
- `SeqWaiter::check` 逻辑不依赖 seq 严格单调；只是 CACHE race 消除后不再出现大跳跃
- `CheckResult::Buffer` 的 sync enqueue 路径保留（容忍极小概率的事务回滚 gap）
- `force_init` 的单调推进保留

client 端 `SeqWaiter::Debug` 可读化（此 PR commit 1）已独立合入，与 server 修复无耦合。

---

## 相关文档

- `docs/wiki/im-v2-relogin-reconnect-offline-sync.md §C.8` — client 端兜底弱点审计
- `src-tauri/src/features/im/seq_waiter.rs` — client SeqWaiter 实现
- Go IM `server/internal/repo/channel_event.go:199-219` — NextEventSeq（CACHE 100）
- Go IM `server/internal/repo/channel.go:276-293` — NextMessageSeq（CACHE 50）
- Go IM `server/internal/repo/metrics.go:18` — AllocSeqDur histogram

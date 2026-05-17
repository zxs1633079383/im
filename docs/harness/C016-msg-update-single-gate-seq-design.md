---
id: C016
title: msg_update 类事件采用「server 严格 seq + 单闸门」设计，禁 RMW JSON 数组与多列回写
status: active
created: 2026-05-17
last_recurred: 2026-05-17
recurrence_count: 1
source_logs:
  - /workspace/java/logs/2026-05-17.json
applies_to:
  - server/internal/repo/message.go
  - server/internal/repo/channel.go
  - server/internal/repo/channel_member.go
  - server/internal/http/message.go
  - server/internal/gateway/**/*.go
  - cses-client mirror: src-tauri/src/features/im/handlers_v2/message.rs
  - cses-client mirror: src-tauri/src/features/im/repo_v2/message_repo.rs
inline_target: ~/.claude/rules/golang/coding-style.md  # 晋升后 inline 到「写入路径」节
---

## 1. 触发场景（Trigger）

任何「每条事件至少触发一次落库」的 hot-path 写入。具体 glob：

- `server/internal/repo/message.go` 任何 `Update*` / `SoftDelete` / `AllocSeqAndInsert` 调用链路
- `server/internal/repo/channel_member.go` 任何 `last_read_seq` / `phantom_count` / `mention_*` 字段写入
- `server/internal/http/message.go` `PATCH /messages/:id` / `DELETE /messages/:id` / `POST /channels/:id/read` / `POST /messages/:id/received`
- `server/internal/gateway/types.go` 任何新增 `WSMessageType`（参考 C005）涉及"消息存在性 / 顺序 / 已读位置"
- 关键词：`msg_updated` / `msg_deleted` / `read_sync` / `channel_member_updated` / `phantom_count` / `last_read_seq`
- 跨仓镜像（cses-client）：`handlers_v2/message.rs` `handle_msg_updated` / `handle_msg_deleted` / `handle_read_sync`

> 与 [C001](C001-allocseq-and-insert-only-message-write-path.md) 的边界：C001 管 **INSERT 路径**（消息首次落库 + seq 分配原子性）；本卡管**所有写入路径的 gate 形态**（包括 INSERT / UPDATE / DELETE），并约束 client 侧消费端如何对账。

## 2. 错误模式（Anti-Pattern）

### 2.1 ❌ Server：read-modify-write JSON 数组列

```go
// ❌ 错误：mention_queue JSON 列 RMW
row := tx.Raw("SELECT mention_queue FROM channel_member WHERE ...").Scan(&row)
queue := []Mention{}
json.Unmarshal([]byte(row.MentionQueue), &queue)
queue = append(queue, newItem)
buf, _ := json.Marshal(queue)
tx.Exec("UPDATE channel_member SET mention_queue=? WHERE ...", string(buf))
```

**后果**：每次 push_msg O(N) 解析 + 写整列 → 群越大越慢 → 高频群压测崩在数据库 CPU。

### 2.2 ❌ Server：把整条 WireMessage 序列化进 props TEXT

```go
// ❌ 错误：协议字段不拆列
wireJSON, _ := json.Marshal(wireMsg)
msg := &Message{Props: stringPtr(string(wireJSON))}  // is_urgent / mention_list 都埋在里面
```

**后果**：`WHERE is_urgent = TRUE` 全表扫；schema 演进必须重写 props；C001 invariant 失守。

### 2.3 ❌ Server：业务字段升级时新增 read-modify-write 计数列

```sql
-- ❌ 错误：在 channel_members 加 urgent_count 计数列
ALTER TABLE channel_members ADD COLUMN urgent_count BIGINT DEFAULT 0;
-- 然后 push_msg 时
UPDATE channel_members SET urgent_count = urgent_count + 1 WHERE channel_id=? AND user_id IN (...);
```

**后果**：高并发同 channel 写入串行化 row-lock；与正例 `channel_member_mention` normalized 表设计冲突。

### 2.4 ❌ Server：编辑 / 撤回路径用乐观锁但不返回新 seq / version

```go
// ❌ 错误：UpdateContent 返回旧 *Message，没有递增 version
msg.Content = content
return msg  // 没 version 字段 → client 收到 echo 不知道是哪一次 edit
```

**后果**：同一条消息连续 edit 两次，client 上后到的 echo 可能被前到的旧 echo 覆盖。

### 2.5 ❌ Client：缺 seq 单调闸门 / 缺 DB 幂等约束

```rust
// ❌ 错误：消费端只覆盖落库，不查 seq 单调
pub async fn handle_msg_updated(ctx, msg) {
    let message: Message = decode(msg.data);
    message_repo::upsert(&ctx.write_pool, &message).await;  // 没比 server seq vs local
    emit("imWs:post:updated");                                // 旧 update 也回放
}
```

**后果**：双 socket 过渡期 / Pulsar 兜底 + WS 直推同时到达 → UI 闪烁；旧 echo 覆盖新 echo。

## 3. 正确做法（Required）

### 3.1 设计哲学（一句话）

> **msg_update 类事件**（消息存在性 / 顺序 / 已读位置 / 计数）必须满足：(a) seq 由 server 严格单调发号；(b) 同一逻辑事件跨所有投递通道 seq 数值恒等；(c) 写入复杂度 O(1)（单 INSERT / UPDATE WHERE PK / DELETE WHERE PK）。满足 → **单闸门** = 处理端 seq 单调 + DB 层 PK/UNIQUE/CAS 三选一兜底。否则 → 拆 schema 或回到双闸门。

### 3.2 三类事件的标准模板

#### A. 新消息 / msg_updated / msg_deleted（行级 INSERT/UPDATE）

**Server 必须**：

```go
// ✅ INSERT 走 AllocSeqAndInsert（C001 已强制）
// 同事务 UPDATE channels SET seq=seq+1 RETURNING seq → INSERT messages
seq, err := repo.AllocSeqAndInsert(ctx, tx, msg)

// ✅ UPDATE 走 CAS：sender / 未删 双约束写在 WHERE
res := db.Model(&Message{}).
    Where("id = ? AND sender_id = ? AND deleted = FALSE", msgID, callerID).
    Updates(map[string]any{"content": content, "updated_at": now})
if res.RowsAffected == 0 { return ErrNotFound }

// ✅ SoftDelete 同上 CAS，返回 ErrGone 时上游跳过 fan-out
res := db.Model(&Message{}).
    Where("id = ? AND sender_id = ? AND deleted = FALSE", msgID, callerID).
    Updates(map[string]any{"deleted": true, "deleted_at": now})

// ✅ Broadcast 必须携带完整 message snapshot（含 seq + updated_at），不发 patch
opts.Broadcaster.BroadcastToMembers(msg.ChannelID, EventMsgUpdated, msg)
```

**Client 必须**（cses-client `handlers_v2/message.rs`）：

```rust
// ✅ 落库前查本地副本，按 (seq, updated_at) 单调判定
pub async fn handle_msg_updated(ctx, msg) -> Result<Vec<EmitEvent>> {
    let new_msg: Message = decode(msg.data)?;
    let local = message_repo::get_by_id(&ctx.write_pool, &new_msg.id).await?;

    // 闸门：拒过期 echo（同 id 必须 updated_at 严格更大才覆盖）
    if let Some(local) = local {
        if let (Some(lu), Some(nu)) = (local.updated_at, new_msg.updated_at) {
            if nu <= lu { return Ok(vec![]); }  // 旧 echo，drop
        }
    }

    // upsert SQL 必须用 ON CONFLICT(id) DO UPDATE WHERE updated_at IS NULL OR EXCLUDED.updated_at > messages.updated_at
    message_repo::upsert_if_newer(&ctx.write_pool, &new_msg).await?;
    Ok(vec![("imWs:post:updated".into(), msg.data.clone())])
}
```

#### B. 已读位置 / channel_read（行级 CAS UPDATE）

**Server 必须**：

```sql
-- ✅ CAS-style UPDATE，WHERE 内嵌闸门
UPDATE channel_members
SET last_read_seq    = $new_seq,
    phantom_at_read  = $new_phantom_count
WHERE channel_id = $cid AND user_id = $uid
  AND last_read_seq < $new_seq          -- ← 闸门
RETURNING last_read_seq;
```

**Client 必须**：

```rust
// ✅ 同样 CAS：channel_member_repo::advance_read_seq 内部 WHERE last_read_seq < new_seq
let advanced = channel_member_repo::advance_read_seq(pool, cid, uid, new_seq, &now).await?;
if advanced {
    // 推进后做 O(1) DELETE 清理已读 mention/urgent 队列（normalized 表）
    channel_member_repo::filter_mention_by_read_seq(pool, cid, uid, new_seq).await?;
}
```

#### C. 累计 / 队列 / 列表字段（独立 normalized 表）

**Server 必须**：每个 element 一行，PK 联合主键，append = O(1) INSERT；清理 = 单 SQL DELETE：

```sql
CREATE TABLE channel_member_mention (
    channel_id TEXT NOT NULL,
    user_id    TEXT NOT NULL,
    msg_id     TEXT NOT NULL,
    seq        BIGINT NOT NULL,
    sender_id  TEXT NOT NULL,
    created_at BIGINT NOT NULL,
    PRIMARY KEY (channel_id, user_id, msg_id)        -- ← 幂等 PK
);
CREATE INDEX idx_cm_mention_seq ON channel_member_mention(channel_id, user_id, seq);

-- push_msg：单 INSERT OR DO NOTHING（双闸门退化为 PK 兜底）
INSERT INTO channel_member_mention VALUES (?,?,?,?,?,?)
ON CONFLICT (channel_id, user_id, msg_id) DO NOTHING;

-- 已读推进时单 DELETE
DELETE FROM channel_member_mention
WHERE channel_id=? AND user_id=? AND seq<=?;
```

> 加急 / 强提醒类计数同模板，未来若新增不要在 `channel_members` 加 `urgent_count BIGINT`，必须建对称的 `channel_member_urgent` 表。

### 3.3 备选：双闸门（仅当 server 端 seq 严格契约无法保证时）

- 闸门 A：seq 单调性 + gap detection → 触发 sync_engine（cses-client 已有）
- 闸门 B：DB PK / UNIQUE INSERT OR IGNORE
- 用例：跨节点 Pulsar 兜底通道 + WS 直推可能携带不同序号的同一事件

### 3.4 绝对禁止

- ❌ JSON 数组列存累计 / 队列 / 历史（如 `mention_queue TEXT DEFAULT '[]'`）
- ❌ `serde_json::to_string(entire_wire_obj)` 塞单列做"无损往返"
- ❌ `channel_members` 加 RMW 计数列（必须走 normalized 表）
- ❌ Client `upsert` 不带 `updated_at` / version 单调判定
- ❌ msg_updated broadcast 只发 patch（必须发完整 snapshot，否则 client 无法对账）

## 4. 检查方法（Verification）

### 4.1 自动 grep（必须返回 0 条）

```bash
# ① 没有 JSON 数组列存队列
grep -rEn 'TEXT[[:space:]]+(NOT[[:space:]]+NULL[[:space:]]+)?DEFAULT[[:space:]]+["'\'']\[\]["'\'']' \
    /workspace/golangProject/im/server/migrations/ --include='*.sql'

# ② 没有 serde_json::to_string(WireXxx) 塞 props
grep -rEn 'Props:.*json\.Marshal\(.*Wire' /workspace/golangProject/im/server/internal/ --include='*.go'

# ③ channel_members 没有 RMW 计数列（mention_count / urgent_count）
grep -En '(mention_count|urgent_count)[[:space:]]+BIGINT' /workspace/golangProject/im/server/migrations/*.sql

# ④ msg_updated handler client 端必须有 updated_at 单调判定
grep -En 'fn handle_msg_updated' /Users/mac28/workspace/angular/temp/cses-client/src-tauri/src/features/im/handlers_v2/message.rs -A 25 \
  | grep -E 'updated_at.*(>|nu <=)' || echo "❌ 缺单调判定"
```

### 4.2 CI Gate

- `golangci-lint` 自定义 ruleguard：禁 `channel_members` 表 RMW 计数列模式
- pre-commit hook：`scripts/check-msg-update-gate.sh` 跑 §4.1 全部 grep，任一非 0 → 阻断 commit

### 4.3 单测（白盒）

- **Server**：`server/internal/repo/message_test.go`
  - `TestUpdateContent_ConcurrentEditSameMessage_NoLost` — 1000 goroutine 并发 PATCH 同 msgID，确认最终 updated_at 单调
  - `TestSoftDelete_DoubleDelete_Idempotent` — 连续两次 DELETE 同 msgID，第二次返回 ErrGone 不引发 broadcast
- **Server**：`server/internal/repo/channel_member_test.go`
  - `TestAdvanceReadSeq_CAS_RejectsRegressive` — new_seq < current 时返回 false 不更新
- **Client**：`cses-client/src-tauri/src/features/im/handlers_v2/message_test.rs`
  - `test_handle_msg_updated_drops_stale_echo` — 旧 updated_at 不覆盖本地

### 4.4 集成测试

- `server/tests/integration/m4_msg_update_idempotency_test.go`
  - 模拟 WS 直推 + Pulsar 兜底同时到达 → 验证 DB 最终一行 + client 单次 emit
- `server/tests/integration/m4_read_sync_monotonic_test.go`
  - 并发 5 设备 POST /read 不同 seq 值 → 最终 last_read_seq = max

### 4.5 手工 smoke

```bash
# 同消息高频 edit 压测
scripts/edit-storm.sh --msg-id <id> --rps 100 --duration 30s
# 通过条件：DB updated_at 单调、client 端 emit 数 = 实际 unique edit 数
```

## 5. 复现历史（Recurrence Log）

| # | 日期 | 触发场景 | 引用日志 | 处置 |
|---|---|---|---|---|
| 1 | 2026-05-17 | 用户问"我们 msg_update 怎么设计高性能"——发现 cses-client `handle_msg_updated` 直接 `message_repo::upsert` 无 updated_at 单调闸门；同时讨论 channel_members 是否要加 `urgent_count` 计数列被本约束驳回 | /workspace/java/logs/2026-05-17.json | 立卡 C016 + 给出高性能设计方案文档 |

## 6. 反例与边界（Don't Over-Apply）

- **瞬时聚合态**（`updateUserTyping` 类 / `updateUserStatus` / 输入指示器）→ **不**走 seq 闸门，直接 apply，允许丢；参考 Telegram pts skill §3.1 决策树 + tg-workspace/docs/04 §1
- **`updateMessageID` 类 id 映射** → 内联处理，**必须**最先 apply（client → server id 重命名），是后续所有针对该消息的 update 的前提
- **migration / seed 数据** → 允许 `db.Exec` 直接 INSERT（一次性 + 无并发）
- **测试 fixture** → 允许绕过 gate（不走业务路径）
- **管理后台批改** → 不走 hot-path，可允许 RMW（但仍建议走 CAS 习惯）

## 7. 升级 / 弃用条件（Lifecycle）

**晋升（→ merged）**：
- 连续 30 天零新复现 + §4.1 grep 全部接管 + §4.3 + §4.4 测试常驻 CI 绿
- 把 §3 设计哲学 inline 进 `~/.claude/rules/golang/performance.md §Hot-Path Write Complexity` 并把 §3.2 三类模板 inline 进 `cses-client/src-tauri/CONVENTIONS.md`

**弃用（→ deprecated）**：
- 协议改 Lamport / vector clock / CRDT，seq 概念被替换 → 整体重写本卡
- `messages` / `channel_members` 表彻底拆分 → 边界重画

## 8. 与 Telegram pts 设计的对照

| 维度 | Telegram pts | 本项目 seq |
|---|---|---|
| 单调号来源 | server 端 account / channel pts | server 端 channels.seq |
| 闸门数 | 单闸门（pts 自身 = dedup key） | 单闸门 + DB PK invariant 兜底 |
| gap 处理 | PtsWaiter 1s 等待 → getDifference | sync_engine + GET /api/sync |
| edit 路径 | updateEditMessage → 走 pts → applyEdition | PATCH /messages/:id → CAS UPDATE → broadcast msg_updated |
| reaction / typing | 不走 pts | 不走 seq（属瞬时聚合态） |
| 频道独立计数 | ChannelData._ptsWaiter | channels.seq（已是 channel-local） |
| 本地缓冲队列 | flat_map<ptsKey, MTPUpdate> | sync_engine 本地 buffer + 服务端 sync 接口 |

> tg 详细文档：`/Users/mac28/workspace/tg/tg-workspace/docs/04-消息更新与编辑.md` § 1-7 + `05-序号闸门内部.md` 全文。
> 相关 skill：`tg-update-pts-categorization-audit` / `tg-pts-watermark-1s-gate` / `tg-message-id-remap-first-apply`。

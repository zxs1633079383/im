# msg_update 高性能设计方案（im server + cses-client 客户端）

> **范围**：消息存在性 / 顺序 / 编辑 / 撤回 / 已读位置 / 加急计数 / 成员状态 七类 update 事件的端到端设计。
> **目标**：单条事件 server O(1) 写、client O(1) 写、跨投递通道幂等、O(1) 已读清理。
> **依据**：
> - 本仓 [C001](harness/C001-allocseq-and-insert-only-message-write-path.md)（写入唯一入口）
> - 本仓 [C016](harness/C016-msg-update-single-gate-seq-design.md)（msg_update 单闸门）
> - cses-client `CLAUDE.md §IM 写入性能约束`（Hot-Path O(1)）
> - tg-workspace `docs/04-消息更新与编辑.md` + `05-序号闸门内部.md`（pts 闸门内部）
> - tg-update-pts-categorization-audit skill（分类决策树）

---

## 1. 设计原则（三句话）

1. **server 必保 invariant**：channels.seq 严格单调发号；同一逻辑事件**跨任意投递通道**（WS 直推 / Pulsar 兜底 / 重连 sync）**seq 数值恒等**；事件写入 O(1)（单 INSERT / CAS UPDATE / 联合 PK DELETE 之一）。
2. **client 单闸门即可**：server invariant 已 cover dedup，client 只需做 **(seq | updated_at) 单调比较** + DB 层 PK invariant 兜底（INSERT OR IGNORE / UPSERT WHERE newer），不需要 Telegram 那样的 PtsWaiter 1s 等待队列（gap 由 sync_engine 全量 sync 接口承担）。
3. **不同事件类用不同表 / 不同闸门形态**：消息表 INSERT + CAS UPDATE；已读位置 CAS UPDATE；累计计数 normalized 表 + INSERT OR IGNORE / DELETE WHERE seq<=read_seq。**禁止**用 JSON 数组列 / props 兜底。

---

## 2. 事件分类总表

| 事件 | server 写形态 | client 闸门 | 现状 | gap |
|---|---|---|---|---|
| **push_msg** (新消息) | INSERT messages + UPDATE channels.seq+1（同事务）+ INSERT channel_member_mention/urgent（normalized） | seq 单调（channel-local）+ INSERT OR IGNORE messages PK | ✅ server 已实现（C001）；mention 已 normalized（migration 023）| 加急（urgent）成员视角计数尚未 normalized — 见 §6 待办 |
| **msg_updated** (编辑 / 模板回执 / urgent confirm) | CAS `UPDATE messages WHERE id=? AND ... AND updated_at < new_ts`；broadcast 全量 snapshot | updated_at 单调比较 + UPSERT WHERE newer | ⚠️ server CAS 在但**无 broadcast updated_at gate**；client 端 `handle_msg_updated` **缺单调判定** | 见 §4.2 |
| **msg_deleted** (撤回) | CAS `UPDATE messages SET deleted=TRUE WHERE deleted=FALSE`；ErrGone 短路 fan-out | DELETE WHERE id=? 天然幂等 | ✅ server 已实现 | 无 |
| **channel_read** (已读推进) | CAS `UPDATE channel_members SET last_read_seq=? WHERE last_read_seq < ?`；同事务 DELETE channel_member_mention/urgent WHERE seq<=? | 同 server CAS；advance_read_seq 返回 advanced=true 时清理 normalized 计数表 | ✅ 已实现（v0.7.5 `filter_mention_by_read_seq`）| urgent 表清理路径需对称 |
| **channel_member_updated** (加入 / 移除 / 改昵称 / 角色) | system message INSERT（走 AllocSeqAndInsert，挂在 channel 的 seq 流上）| 同 push_msg | ✅ 走 system message 类型 | 无 |
| **read_sync** (己方其他设备已读同步) | 走 ReadSyncPusher 单事件推送（不入库，靠 client 自己幂等） | 同 channel_read | ✅ 已实现 | 无 |
| **typing / status** (瞬时聚合) | **不**入库、**不**走 seq 闸门 | 直接 apply，允许丢 | — | 按 tg-update-pts-categorization-audit §3.1 决策树归类 |

---

## 3. Wire 协议契约（im server → cses-client）

### 3.1 Envelope 公共字段

所有 update 帧顶层 envelope 携带：

```json
{
  "type": "msg_updated",
  "seq": 12345,                         // ← channel-local seq；channel_read 类用 last_read_seq
  "channel_id": "ch_xxx",
  "ts": 1737100000000,                  // server unix ms，wall clock，仅作辅助比较
  "tracing": { "traceparent": "...", "tracestate": "..." },
  "data": { ... }                       // ← 事件 payload
}
```

- `seq` 必填：消息类带 messages.seq；channel_read 类带 last_read_seq；channel_member_updated 用 system message seq
- `ts` 仅辅助 ordering，**不是闸门依据**（NTP 偏差 / leap second 不可靠）

### 3.2 各事件 payload 形态

| type | data 形态 | 说明 |
|---|---|---|
| `push_msg` | 完整 Message JSON（含 mention_list / visible_to / props）| 与现状一致 |
| `msg_updated` | 完整 Message JSON snapshot（含 updated_at）| **必须发完整 snapshot**，client 不允许 patch；client 用 updated_at 单调比较 |
| `msg_deleted` | `{msg_id, channel_id, deleted_at}` | 不含 content；client 直接标 deleted |
| `channel_read` | `{channel_id, read_seq, phantom_at_read}` | server 端 CAS 通过后才推 |
| `channel_member_updated` | system message JSON（msg_type=System, props.sys_type=member_*）| 走 push_msg 同样 pipe |
| `read_sync` | 同 channel_read | 但只推给 sender 本人其他设备 |

### 3.3 投递通道约束

**关键 invariant**：同一逻辑事件无论走哪条通道，**seq 数值恒等**。

- **WS 直推**（本 pod 在线用户）：gateway hub fan-out
- **Pulsar 兜底**（跨 pod 在线用户）：经 `gateway.CrossPodPush`，msg payload 完全相同
- **重连 sync**（离线用户上线后）：`GET /api/sync?since_seq=N` 返回 messages slice，每条 seq 与原 push 一致

server 端 invariant 必须 grep 守护：

```bash
# 任何 broadcast / cross-pod / sync 路径必须从 *repo.Message 直接序列化，不能在中转层重新计算 seq
grep -rEn 'Seq:\s*[a-z][A-Za-z]*\.(Seq|seq)\s*\+' server/internal/ --include='*.go' || true
```

---

## 4. Server 端实现（im）

### 4.1 push_msg 路径（现状 ✅，对照确认）

```
HTTP POST /channels/:id/messages
    │
    ▼
MessageService.SendMessage
    │
    ▼ (单事务)
MessageRepo.Send
    ├─ 1. idempotency: SELECT WHERE (channel_id, client_msg_id)
    ├─ 2. AllocSeqAndInsert (UPDATE channels.seq + INSERT messages)
    ├─ 3. (visible_to != nil) IncrementPhantomCount
    └─ 4. (mention_list 命中) INSERT channel_member_mention (normalized, O(1) per recipient)
    │
    ▼ (响应后异步 goroutine)
pushToMembers
    ├─ ListMembers → bucketByVisibility
    ├─ visible bucket → BroadcastMessage(real payload)
    └─ phantom bucket → BroadcastMessage(phantomVariant: 仅 seq 骨架)
```

**性能特征**：
- 单条 push_msg：`O(1)` SQL（4 个 UPSERT/INSERT），不随群规模变化
- normalized mention 表：每 mention 命中一行 INSERT，O(命中人数) 而非 O(群规模)
- phantom_count 自增：`IncrementPhantomCount` 用 `UPDATE WHERE NOT IN` 单 SQL，O(群规模) 但只在 visible_to != nil 时触发

### 4.2 msg_updated 路径（**待补强 ⚠️**）

**现状缺陷**：`MessageRepo.UpdateContent` 已 CAS，但 broadcast 时 client 无对账依据。

**改造方案**：

```go
// ✅ server 已 CAS（保留）
res := db.Model(&Message{}).
    Where("id = ? AND sender_id = ? AND deleted = FALSE", msgID, callerID).
    Updates(map[string]any{"content": content, "updated_at": now})

// ✅ 新增：CAS UPDATE 同时 RETURNING updated_at（防 wall clock 倒退）
// PostgreSQL 已默认 RETURNING；GORM 改用 Raw + Scan
err := db.Raw(`
    UPDATE messages
       SET content    = $1,
           updated_at = $2
     WHERE id         = $3
       AND sender_id  = $4
       AND deleted    = FALSE
       AND (updated_at IS NULL OR updated_at < $2)   -- ← 新增 wall clock 单调闸门
    RETURNING *`,
    content, now, msgID, callerID).Scan(&msg).Error

// ✅ broadcast 全量 snapshot（已实现）—— 但要保证 updated_at 字段进入 JSON 出参
opts.Broadcaster.BroadcastToMembers(msg.ChannelID, EventMsgUpdated, msg)
```

**为什么加 `updated_at IS NULL OR updated_at < $2`**：极端场景两个并发 PATCH 几乎同时进 server，Postgres row-lock 串行后第二条 UPDATE 写入的 now() 可能比 first 早（NTP 倒退），加这条 WHERE 保证最终 row 状态对应最大 wall clock。

### 4.3 channel_read 路径（现状 ✅）

```go
// 已实现（service.MarkRead）
seq, err := svc.MarkRead(ctx, channelID, uid)
// 内部 CAS UPDATE channel_members SET last_read_seq=greatest(last_read_seq, c.seq) WHERE ...
```

**配套要求**（cses-client 已对齐）：advance 成功后 `DELETE FROM channel_member_mention WHERE seq <= new_last_read_seq`。**Server 端如果未来加 server-driven mention/urgent 计数**，必须同样在 MarkRead 事务内做 DELETE。

### 4.4 channel_member_updated 路径（现状 ✅）

走 system message：`PostSystemMessage(props={sys_type: "member_joined", target_id: ...})` → 走 `AllocSeqAndInsert` → 跟普通消息一样占 channel.seq → 走 push_msg fan-out。

**优点**：复用同一 sync 通道（cses-client 重连 sync 时不需要 region-specific 路径），seq 闸门统一。

---

## 5. Client 端实现（cses-client）

### 5.1 当前 gap 与补强

`handlers_v2/message.rs::handle_msg_updated` 当前：

```rust
// ⚠️ 现状
if let Err(e) = message_repo::upsert(&ctx.write_pool, &message).await {
    warn!(error = %e, "msg_updated 落库失败");
}
```

**问题**：`upsert` 无单调闸门，旧 echo 会覆盖新 echo。

**补强方案**（C016 §3.2 模板）：

```rust
pub async fn handle_msg_updated(ctx, msg) -> Result<Vec<EmitEvent>> {
    let new_msg: Message = serde_path_to_error::deserialize(&msg.data)
        .map_err(|e| anyhow::anyhow!("msg_updated decode: {e}"))?;

    // ✅ upsert SQL 内嵌闸门，server-side 兜底
    let affected = message_repo::upsert_if_newer(&ctx.write_pool, &new_msg).await?;
    if affected == 0 {
        debug!(message_id=%new_msg.id, "msg_updated stale echo, dropped");
        return Ok(vec![]);  // ← 不 emit，避免 UI 闪烁
    }

    Ok(vec![("imWs:post:updated".into(), msg.data.clone())])
}
```

**SQL 模板**（cses-client repo_v2/message_repo.rs 需新增）：

```rust
pub async fn upsert_if_newer(pool: &Pool<Sqlite>, msg: &Message) -> Result<u64> {
    let res = sqlx::query!(
        r#"
        INSERT INTO messages (id, channel_id, seq, ..., updated_at)
        VALUES (?, ?, ?, ..., ?)
        ON CONFLICT(id) DO UPDATE SET
            content    = excluded.content,
            updated_at = excluded.updated_at,
            ...
        WHERE messages.updated_at IS NULL
           OR excluded.updated_at IS NOT NULL
              AND excluded.updated_at > messages.updated_at
        "#,
        msg.id, msg.channel_id, msg.seq, ..., msg.updated_at
    )
    .execute(pool)
    .await?;
    Ok(res.rows_affected())
}
```

### 5.2 push_msg 路径（已基本对齐）

`handle_push_msg`：
- 现已走 `message_repo::insert`（含 ON CONFLICT DO NOTHING ← C001 客户端镜像）
- mention_list 命中本人时 `INSERT channel_member_mention` 也是 PK 兜底

**新增检查**：seq 闸门交给 sync_engine 兜底（C006 单一入口），push_msg handler 本身不重复 gap detection（这条与 Telegram PtsWaiter 不一样：tg 是 P2P 协议，client 必须自己 gap；这里 server 是中心化的，靠 sync_engine 重连接补齐更省）。

### 5.3 channel_read 路径（已对齐 ✅）

`handle_read_sync` 已含 `advance_read_seq` CAS + `filter_mention_by_read_seq` O(1) DELETE。无 gap。

---

## 6. 待办清单（按优先级）

| # | 任务 | 项目 | 优先级 | 工作量 | 验证 |
|---|---|---|---|---|---|
| 1 | im server: `MessageRepo.UpdateContent` 加 `updated_at` 单调 WHERE 闸门 | im | P0 | 0.5d | `m4_msg_update_idempotency_test.go` |
| 2 | im server: `EventMsgUpdated` broadcast 出参确认含 `updated_at` 字段（JSON tag 检查）| im | P0 | 0.2d | grep `json:"updated_at"` |
| 3 | cses-client: `repo_v2/message_repo.rs` 新增 `upsert_if_newer`；`handle_msg_updated` 切换 | cses-client | P0 | 0.5d | `message_test.rs::test_handle_msg_updated_drops_stale_echo` |
| 4 | cses-client: handle_msg_updated 返回空 vec 时不 emit `imWs:post:updated` | cses-client | P0 | 0.1d | 同上 |
| 5 | im server: 集成测试 `m4_msg_update_idempotency_test.go`（WS + Pulsar 同时到达）| im | P1 | 1d | 200 RPS 不重 / 不丢 |
| 6 | im server: pre-commit hook + CI ruleguard 跑 C016 §4.1 grep | im | P1 | 0.5d | grep 全部 0 |
| 7 | 加急计数若要做 server-side normalized → 新 migration `024_channel_member_urgent` + INSERT/DELETE 对称（参考 mention 表）| im | P2 | 1d | grep `urgent_count BIGINT` 必须 0 |
| 8 | cses-client: 现存 `urgent_queue TEXT '[]'` 类列若有，需在下个 cutover phase 一并 normalize | cses-client | P2 | 1d | 同上 |

---

## 7. 与 Telegram pts 闸门的对照（设计取舍）

| 维度 | Telegram | 本方案 | 取舍理由 |
|---|---|---|---|
| 闸门数 | 1（pts 是 dedup key） | 1（seq 单调 + DB PK invariant 兜底） | server 严格 seq 契约下与 tg 同构 |
| 客户端 gap 处理 | PtsWaiter 1s 等待 + getDifference 拉 diff | **不在 client gap detection**：交给 sync_engine 重连接补齐 | tg 是 P2P，本方案是中心化 server，sync 接口可信度更高，省去 client 状态机 |
| 频道独立 pts | ChannelData._ptsWaiter | channels.seq 天然 channel-local | 同构 |
| edit 路径 | updateEditMessage → 走 pts → HistoryItem::applyEdition | msg_updated → 全量 snapshot → upsert_if_newer | tg 是 patch 风格（pts gate 防乱序）；本方案是 snapshot 风格（updated_at gate 防乱序），两种都成立但 snapshot 模型 client 端逻辑更简单 |
| reaction / typing | 不走 pts | 不走 seq | 同构 |
| updateMessageID（client↔server id 映射）| 内联，必须最先 apply | **本方案用 client_msg_id 作幂等键** + server 落库后回传 message.id；client 用 (channel_id, client_msg_id) 上行重试，server 落库 ON CONFLICT 不重复 | 比 tg 的 random_id ↔ server_id 双向映射简单：只要 server 端 `Send` 的 idempotency 检查在事务内（C001 已是），就不需要单独的 ID 映射 update |

**关键不取**：不引入 PtsWaiter 1s 等待队列。原因：
- tg 是 P2P / 多 DC，必须 client 自己 gap detect；本方案 server 在 K8s 集群里，gap 由 sync 接口（`GET /api/sync?since_seq=N`）兜底更省
- 1s 等待队列会引入 push→可见 的额外延迟，对 IM UX 不利
- 节省 client 状态机复杂度（cses-client `sync_engine` 已是离线同步唯一入口，C006 强约束）

---

## 8. 一句话总结

> server 严格 seq 单调 + 同事件跨通道 seq 恒等 → client 单闸门（updated_at / seq 单调 + DB PK 兜底）即可；累计 / 队列字段全部 normalized，禁 JSON 数组 RMW；msg_updated 必须发 snapshot 不发 patch，gap 不在 client 检测交给 sync_engine 重连补齐。

---

## 9. 参考文档

- [C001](harness/C001-allocseq-and-insert-only-message-write-path.md) — AllocSeqAndInsert 唯一入口
- [C016](harness/C016-msg-update-single-gate-seq-design.md) — msg_update 单闸门 harness
- [C005 (cses-client)](../../../mac28/workspace/angular/temp/cses-client/docs/harness/C006-sync-engine-only-offline-sync-path.md) — sync_engine 唯一入口
- `tg-workspace/docs/04-消息更新与编辑.md` — tg pts 闸门分类
- `tg-workspace/docs/05-序号闸门内部.md` — PtsWaiter 内部
- cses-client `CLAUDE.md §IM 写入性能约束` — Hot-Path O(1) 模板
- skill `tg-update-pts-categorization-audit` — 分类决策树

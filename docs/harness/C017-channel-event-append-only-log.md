---
id: C017
title: channel_event 是事件流水唯一入口；任何 mutation 必须同事务 append 一行
status: active
created: 2026-05-17
last_recurred: 2026-05-17
recurrence_count: 1
source_logs:
  - /workspace/java/logs/2026-05-17.json
applies_to:
  - server/internal/repo/channel_event.go
  - server/internal/repo/message.go
  - server/internal/repo/channel.go
  - server/internal/repo/channel_member.go
  - server/internal/service/sync.go
  - server/internal/http/message.go
  - server/migrations/024_channel_event*.sql
inline_target: ~/.claude/rules/golang/coding-style.md
---

## 1. 触发场景（Trigger）

任何会改 channel 内"消息存在性 / 顺序 / 已读位置 / 反应 / 钉选 / 加急 / 成员关系"状态的写入路径。具体 glob：

- `server/internal/repo/message.go` 任何 `UpdateContent` / `SoftDelete` / `PostSystemMessage` / `AllocSeqAndInsert`
- `server/internal/repo/channel_member.go` 任何 `AdvanceReadSeq` / `IncrementPhantomCount`
- `server/internal/repo/channel.go` 任何 `AddMember` / `RemoveMember` / `SetMemberRole` / `Close`（实际通过 PostSystemMessage 上挂）
- 未来 reaction / pin / forward 等新事件类型
- 关键词：`channel_event` / `ChannelEvent` / `EventTypeNew` / `EventTypeEdit` / `EventTypeDelete` / `EventTypeReaction`

## 2. 错误模式（Anti-Pattern）

### 2.1 ❌ 改 messages 表但不写 channel_event

```go
// ❌ 错误：单独 UPDATE messages，不挂事件
db.Model(&Message{}).Where("id=?", msgID).Updates(map[string]any{"content": content})
// 后果：sync 算法按 event_seq 拉时漏掉这次 edit，client 离线重连看不到变更
```

### 2.2 ❌ 把 channel_event INSERT 放在 messages 事务之外

```go
// ❌ 错误：跨事务，崩溃半路两边不一致
db.Transaction(func(tx *gorm.DB) { tx.Updates(...) })  // commit 1
db.Create(&ChannelEvent{...})                          // commit 2，如崩 → mutation 已生效但 event 缺
```

### 2.3 ❌ channel_event 字段塞进 messages.props

```go
// ❌ 错误：把事件元信息（actor_id / event_type）塞进 message.props
msg.Props = json.Marshal(map[string]any{"last_event_type": "edit", "actor_id": uid})
// 后果：跨消息查事件流必须扫所有 message.props，O(N) 查询；违反 C016 §3.4
```

### 2.4 ❌ event_seq 用 `UPDATE channels SET event_seq=event_seq+1 RETURNING` row-lock 形态

```go
// ❌ 错误：row-lock 形态，万人群高并发塌方
var newSeq int64
db.Raw("UPDATE channels SET event_seq=event_seq+1 WHERE id=? RETURNING event_seq", chID).Scan(&newSeq)
// 后果：单 channel 写吞吐上限 ~500 TPS（受 row-lock + WAL fsync 限制）；万人群 1k QPS 必塌
```

→ 必须用 PG sequence 对象，详见 [C018](C018-pg-sequence-vs-row-lock-seq.md)。

## 3. 正确做法（Required）

### 3.1 设计哲学

> 任何 mutation = `UPDATE/INSERT/DELETE 业务行 + INSERT channel_event(event_seq, type, msg_id, actor_id, payload)` 必须在**同一个事务**内完成。`event_seq` 由 PG sequence 对象分配，单调全局唯一（per channel）。sync cursor 推进只看 channel_event_seq，不看 messages.seq。

### 3.2 标准模板

```go
// ✅ 新消息（Phase P3 切换后）
func (r *gormMessageRepo) Send(ctx context.Context, msg *Message) error {
    return r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
        // 1. 分配 message.seq（消息序号，作 ordering）
        seq, err := r.channel.NextMessageSeq(ctx, tx, msg.ChannelID)
        if err != nil { return err }
        msg.Seq = seq

        // 2. INSERT messages
        if err := tx.Create(msg).Error; err != nil { return err }

        // 3. 分配 event_seq（事件序号，作 sync cursor）
        eventSeq, err := r.channelEvent.NextEventSeq(ctx, tx, msg.ChannelID)
        if err != nil { return err }

        // 4. INSERT channel_event
        return tx.Create(&ChannelEvent{
            ChannelID: msg.ChannelID,
            EventSeq:  eventSeq,
            EventType: EventTypeNew,
            MsgID:     &msg.ID,
            ActorID:   msg.SenderID,
            CreatedAt: time.Now().UnixMilli(),
        }).Error
    })
}

// ✅ Edit
func (r *gormMessageRepo) UpdateContent(ctx, msgID, callerID, content) (*Message, error) {
    var msg *Message
    err := r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
        // 1. CAS UPDATE messages（含 wall-clock 单调闸门，C016 §3.2）
        res := tx.Model(&Message{}).
            Where("id=? AND sender_id=? AND deleted=FALSE", msgID, callerID).
            Updates(map[string]any{"content": content, "updated_at": now})
        if res.RowsAffected == 0 { return ErrNotFound }

        // 2. 重新读 message（拿 channel_id）
        msg, err = r.GetByID(ctx, msgID)
        if err != nil { return err }

        // 3. 分配 event_seq + INSERT channel_event
        eventSeq, err := r.channelEvent.NextEventSeq(ctx, tx, msg.ChannelID)
        if err != nil { return err }
        return tx.Create(&ChannelEvent{
            ChannelID: msg.ChannelID, EventSeq: eventSeq,
            EventType: EventTypeEdit, MsgID: &msgID, ActorID: callerID,
            CreatedAt: time.Now().UnixMilli(),
        }).Error
    })
    return msg, err
}

// ✅ Delete / MarkRead / AddReaction 同模板
```

### 3.3 EventType 枚举锁定

```go
type EventType int16
const (
    EventTypeNew      EventType = 1   // 新消息
    EventTypeEdit     EventType = 2   // 编辑
    EventTypeDelete   EventType = 3   // 撤回
    EventTypeReaction EventType = 4   // 反应（未来）
    EventTypePin      EventType = 5   // 钉选（未来）
    EventTypeReadMark EventType = 6   // 已读推进（己方设备 echo）
    EventTypeMember   EventType = 7   // 成员变化（替代当前 system message + AllocSeqAndInsert）
)
```

新增 EventType **必须**：(1) 在此枚举追加常量；(2) cses-client `handlers_v2` dispatch 表对称新增 case；(3) 集成测试覆盖；(4) 同步本卡 §3.3。

### 3.4 绝对禁止

- ❌ 业务表 UPDATE / INSERT / DELETE 后**不**写 channel_event
- ❌ channel_event INSERT 与业务写不在**同一事务**
- ❌ 用 `UPDATE channels SET event_seq=event_seq+1 RETURNING` row-lock 形态（详见 C018）
- ❌ event_type 用 string 而非 int16 enum（cardinality 控制）

## 4. 检查方法（Verification）

### 4.1 自动 grep（必须返回 0 条）

```bash
# ① messages 表 UPDATE 路径必须伴随 ChannelEvent Create
grep -rEn 'tx\.Model\(&Message\{\}\)\.Where|tx\.Updates' server/internal/repo/message.go -A 8 \
  | grep -B 1 -A 8 'Updates' | grep -L 'channelEvent\|ChannelEvent' || echo "OK"

# ② 跨事务红线：业务 UPDATE 和 channel_event INSERT 不在同 tx
grep -rEn '\.db\.Create\(&ChannelEvent\{' server/internal/ --include='*.go'  # 必须 0 命中（必须 tx.Create）

# ③ EventType 不允许 string
grep -rEn 'EventType\s+string' server/internal/repo/ --include='*.go'  # 必须 0
```

### 4.2 CI Gate

- `golangci-lint` ruleguard 规则：在 `internal/repo/message.go` 内所有 `tx.Updates` 或 `tx.Create(&Message{...})` 后必须出现 `tx.Create(&ChannelEvent{...})` 调用
- pre-commit hook：`scripts/check-channel-event-coverage.sh` exit 0

### 4.3 单测（白盒）

- `server/internal/repo/channel_event_test.go::TestUpdateContent_AlwaysAppendsEvent`
- `server/internal/repo/channel_event_test.go::TestSoftDelete_AlwaysAppendsEvent`
- `server/internal/repo/channel_event_test.go::TestTransactionRollback_NoOrphanEvent`

### 4.4 集成测试

- `server/tests/integration/m4_offline_edit_sync_test.go::TestOfflineEdit_VisibleAfterReconnect`
  - 流程：发消息 → 模拟 client 离线（不收 WS）→ server 端 PATCH 编辑 → client 重连 sync → 验证拿到 edited content
- `server/tests/integration/m4_event_seq_monotonic_test.go::TestEventSeq_ConcurrentMutations`
  - 流程：1000 goroutine 并发新消息 + edit + delete 同 channel → event_seq 严格单调无缺漏

## 5. 复现历史（Recurrence Log）

| # | 日期 | 触发场景 | 引用日志 | 处置 |
|---|---|---|---|---|
| 1 | 2026-05-17 | 用户问"离线 msg_update 怎么解决" → 发现 sync 算法漏拉老消息 edit/delete；综合 Discord + tg 决策方案 B (channel_event append-only) | /workspace/java/logs/2026-05-17.json | 立 C017/C018/C019 + P1-P7 实施 |

## 6. 反例与边界（Don't Over-Apply）

- **瞬时聚合态**（typing / status / SendMessageAction）→ 不入 channel_event（按 [C016 §6](C016-msg-update-single-gate-seq-design.md) 决策）
- **测试 fixture 直接写 messages 表** → 允许跳过 channel_event（不走业务路径）
- **migration / seed 数据** → 允许直接 INSERT 不写 event（一次性，离线场景不存在）

## 7. 升级 / 弃用条件

**晋升（→ merged）**：连续 30 天零新复现 + §4.1 grep 接管 + §4.4 集成测试常驻 CI 绿。inline 到 `~/.claude/rules/golang/coding-style.md §写入路径`。

**弃用**：协议改 vector clock / CRDT，事件流概念被替代。

---
id: C019
title: sync 算法 cursor 字段锁定 event_seq；4 分支 kind + TooLong 阈值 10000
status: active
created: 2026-05-17
last_recurred: 2026-05-17
recurrence_count: 1
source_logs:
  - /workspace/java/logs/2026-05-17.json
applies_to:
  - server/internal/service/sync.go
  - server/internal/http/sync.go
  - server/internal/repo/channel_event.go
  - cses-client mirror: src-tauri/src/features/im/sync_engine.rs
  - cses-client mirror: src-tauri/src/features/im/types_v2.rs
inline_target: ~/.claude/rules/golang/testing.md#sync-协议测试模板im--event-log-ssot-场景来源im-项目-c019-实测落地
inline_staged_at: 2026-05-19            # 规则原文 pre-stage inline 进全局 testing.md（早于 active→merged 标准 30 天观察期）
merge_observation_until: 2026-06-18     # 自 2026-05-19 起连续 30 天零新复现 → status: merged
post_inline_evidence:
  - 27 集成测试 in server/tests/integration/m4_offline_sync_*_test.go
  - tag v0.7.3-test-coverage-100 实测全绿（1173.959s, 0 FAIL）
---

## 1. 触发场景

- `server/internal/service/sync.go` 任何 cursor 处理 / kind 分支判断
- `server/internal/http/sync.go` 任何 wire 字段定义
- cses-client `sync_engine.rs::SyncReq` 任何 cursor 字段
- 关键词：`SyncCursor` / `clientSeq` / `event_seq` / `Kind` / `too_long` / `reset_to`

## 2. 错误模式

### 2.1 ❌ 用 messages.seq 作 cursor（当前实现）

```go
// ❌ 当前 service/sync.go:148-156
clientSeqs := make(map[string]int64)
for _, c := range p.Cursors {
    clientSeqs[c.ID] = c.Seq            // ← 名字暧昧，实质是 messages.seq
}
for chID, serverSeq := range serverSeqs {  // ← serverSeqs 也是 messages.seq
    if clientSeq >= serverSeq { continue }
    ...
}
// 后果：离线时老消息 edit/delete 不推进 messages.seq → sync 跳过 → client 看不到
```

### 2.2 ❌ TooLong 阈值不锁定

```go
// ❌ 错误：魔法数字散落代码
if gap > 10000 { ... }   // 一处
if gap > 5000  { ... }   // 别处复制错了
```

### 2.3 ❌ kind 用 string 没 enum

```go
delta.Kind = "empty"  // ❌ typo 不报错
```

## 3. 正确做法

### 3.1 wire 契约（server / client 都必须遵守）

```go
// SyncCursor v2 (event_seq based)
type SyncCursor struct {
    ID         string `json:"id"`
    EventSeq   int64  `json:"event_seq"`         // ← 新字段名（替代 seq）
}

// SyncEntryKind 锁定 4 值
type SyncEntryKind struct {
    Type    string `json:"type"`    // "empty" | "events" | "slice" | "too_long"
    ResetTo int64  `json:"reset_to,omitempty"`   // 仅 too_long 时带
}

// SyncChannelDelta
type SyncChannelDelta struct {
    ID             string         `json:"id"`
    ServerEventSeq int64          `json:"server_event_seq"`
    Unread         int64          `json:"unread"`
    Events         []ChannelEvent `json:"events,omitempty"`        // ← 替代 messages
    Messages       map[string]*Message `json:"messages,omitempty"` // ← msg_id → snapshot map（去重）
    Kind           *SyncEntryKind `json:"kind"`
    NextCursor     *int64         `json:"next_cursor,omitempty"`   // 仅 slice 时
}
```

### 3.2 算法（参考 tg differenceDone）

```go
const (
    EventLimitPerChannel  = 200    // 单 channel 单批最多事件数
    EventTooLongThreshold = 10000  // gap > 此值触发 too_long（参 tg differenceTooLong + TDLib MAX_CHANNEL_DIFFERENCE×100）
)

type EventKind string
const (
    KindEmpty   EventKind = "empty"
    KindEvents  EventKind = "events"
    KindSlice   EventKind = "slice"
    KindTooLong EventKind = "too_long"
)

func (s *SyncService) Sync(ctx, callerID, p) (SyncResult, error) {
    serverEventSeqs := s.channelEvent.GetMemberChannelEventSeqs(ctx, callerID)
    clientCursors := buildClientMap(p.Cursors)

    results := make([]SyncChannelDelta, 0, len(serverEventSeqs))
    for chID, serverEventSeq := range serverEventSeqs {
        clientEventSeq, known := clientCursors[chID]
        if known && clientEventSeq >= serverEventSeq {
            // kind=empty 也输出（client 知道这个 channel 没新事件）
            results = append(results, SyncChannelDelta{
                ID: chID, ServerEventSeq: serverEventSeq,
                Kind: &SyncEntryKind{Type: string(KindEmpty)},
            })
            continue
        }

        gap := serverEventSeq - clientEventSeq
        if known && gap > EventTooLongThreshold {
            // tg differenceTooLong 模式：client 清表 + 重拉首屏
            results = append(results, SyncChannelDelta{
                ID: chID, ServerEventSeq: serverEventSeq,
                Kind: &SyncEntryKind{Type: string(KindTooLong), ResetTo: serverEventSeq},
            })
            continue
        }

        // 拉事件 + 关联 message snapshot
        events, _ := s.channelEvent.FetchAfter(ctx, chID, callerID, clientEventSeq, EventLimitPerChannel)
        msgs, _ := s.messages.GetByIDsForUser(ctx, callerID, uniqueMsgIDs(events))
        delta := SyncChannelDelta{
            ID: chID, ServerEventSeq: serverEventSeq,
            Events: events, Messages: indexByID(msgs),
        }
        if len(events) == EventLimitPerChannel {
            delta.Kind = &SyncEntryKind{Type: string(KindSlice)}
            last := events[len(events)-1].EventSeq
            delta.NextCursor = &last
        } else {
            delta.Kind = &SyncEntryKind{Type: string(KindEvents)}
        }
        // unread 仍按现有公式从 channel_member 计算
        delta.Unread = computeUnread(...)
        results = append(results, delta)
    }
    return SyncResult{Channels: results}, nil
}
```

### 3.3 client 端 dispatcher（cses-client）

```rust
match event.event_type {
    EventType::New | EventType::Edit => {
        // 从 sync resp 的 messages map 拿 snapshot
        let msg = sync_resp.messages.get(&event.msg_id.unwrap())?;
        message_repo::upsert_if_newer(&pool, msg).await?;
    }
    EventType::Delete => {
        message_repo::mark_deleted(&pool, &event.msg_id.unwrap()).await?;
    }
    EventType::Reaction => reaction_repo::apply(...).await?,  // 未来
    EventType::ReadMark => channel_member_repo::advance_read_seq(...).await?,
    EventType::Member  => channel_member_repo::apply_member_event(...).await?,
    _ => debug!("unknown event_type {}, skip", event.event_type),
}
state.set_event_cursor(channel_id, max_event_seq_in_batch);
```

### 3.4 绝对禁止

- ❌ wire 字段名继续叫 `seq`（必须 `event_seq`，避免与 messages.seq 混淆）
- ❌ TooLong 阈值散写魔法数字（必须 `EventTooLongThreshold` 常量）
- ❌ client 端按 messages.seq 维护本地 cursor（必须 max(channel_event.event_seq)）
- ❌ kind 用 string 字面量（必须 enum）
- ❌ 不返回 messages map 让 client 凭 event 自己再 fetch（破坏 sync 是"自包含批量"原则）

## 4. 检查方法

### 4.1 自动 grep

```bash
# ① wire 字段必须叫 event_seq
grep -rEn 'json:"seq"' server/internal/http/sync.go && echo "❌ 还在用 seq"

# ② 必须有 EventTooLongThreshold 常量
grep -En 'const\s+EventTooLongThreshold\s*=\s*10000' server/internal/service/sync.go || echo "❌ 缺常量"

# ③ kind 必须 enum
grep -rEn 'Kind:\s*&SyncEntryKind\{Type:\s*"' server/internal/ --include='*.go' && echo "❌ 字面量"
```

### 4.2 集成测试

- `m4_sync_event_cursor_test.go::TestSync_OldClientNoCursor_ReturnsFullSnapshot`
- `m4_sync_event_cursor_test.go::TestSync_GapBelowLimit_ReturnsAllEvents`
- `m4_sync_event_cursor_test.go::TestSync_GapAboveLimit_ReturnsSlice_AndRecurses`
- `m4_sync_event_cursor_test.go::TestSync_GapHugeOverThreshold_ReturnsTooLong`

### 4.3 客户端契约测试

- cses-client `sync_engine.rs` integration test：四种 kind 都能正确 dispatch

## 5. 复现历史

| # | 日期 | 触发场景 | 引用日志 | 处置 |
|---|---|---|---|---|
| 1 | 2026-05-17 | 决策方案 B 后必须重写 sync wire 契约（cursor 从 seq → event_seq）| /workspace/java/logs/2026-05-17.json | 立 C019 + Phase P4 + cses-client mirror C009 |

## 6. 反例与边界

- **超大批 sync**（首次冷启动 / 离线 30 天）→ kind=slice 多次递归，单批 ≤ EventLimitPerChannel
- **client 端无 cursor**（新设备首登）→ 仅 channels 中 latest 200 events，不全量历史（历史用 `/messages/around` 按需拉）
- **legacy v1 client**（旧版本仍在用 `seq` 字段）→ Phase P4 阶段需要双轨：v1 wire 名 `seq` 字段保留兼容；v2 新字段 `event_seq`；v3 才下线 v1

## 7. 升级 / 弃用

**晋升**：所有 client 升级到 v2 后下线 v1 字段。

**弃用**：协议改 vector clock。

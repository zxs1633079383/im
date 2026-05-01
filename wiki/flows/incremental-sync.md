---
type: flow
title: 增量同步（POST /api/sync）
status: stable
last_verified: 2026-04-28
sources:
  - server/docs/BACKEND.md#§3.3
  - server/internal/service/sync.go
related:
  - entities/sync-service
  - entities/im-seq-data-source
  - concepts/seq-cursor
  - concepts/ws-event-types
confidence: high
---

# Flow：增量同步 `POST /api/sync`

> 客户端持有 `{channelID → known_seq}` 游标 → 服务端按需返 delta。**取代 Mattermost 的 bitmap+segment**。

## 触发时机

1. **WS pong 提示有 delta** → 客户端主动发 sync
2. **应用启动 / 重连** → 用上次的 cursor 一次性补
3. **用户切到某频道** → 单频道 sync（短回路）

## 请求/响应

### Request

```jsonc
POST /api/sync
{
  "cursor": { "10086": 5230, "10087": 128 },
  "limit_per_channel": 200
}
```

- `cursor` 字段类型 `map[int64]int64`
- 客户端可附带最多 `MaxChannelsPerCall = 500` 个频道
- `limit_per_channel` 可省略，默认 200

### Response

```jsonc
{
  "channels": [
    {
      "channel_id": 10086,
      "msgs": [
        { "id": ..., "seq": 5231, "sender_id": "...", "team_id": "...", "content": "..." },
        ...
      ],
      "last_seq": 5275,
      "has_more": false
    }
  ]
}
```

- `msgs` ASC by seq
- `has_more = true` → 客户端把 cursor 推到本页最后 seq，再发一次

## 服务端处理

```text
SyncService.Sync(ctx, userID, cursor)
  ├─ ChannelStore.ListMembership(userID)         列出加入的频道
  │
  ├─ for each (channelID, knownSeq) in cursor ∩ memberships:
  │     if knownSeq < channels.last_seq:
  │       FetchAfter(channelID, knownSeq, limit_per_channel)
  │
  ├─ 对未在 cursor 但用户已加入的频道：
  │     无 delta，不返回（节省带宽）
  │
  └─ 不返回已退出的频道（即使 cursor 含有）
```

## 与心跳的协作

```text
Client → ping { channel_seqs: {10086: 5230} }
         heartbeat 路径 → ChannelSeqStore.GetMemberChannelSeqs
Client ← pong { channel_seqs: {10086: 5275} }    ← 仅 delta 频道
         │
         └─ Client 决定：差距大 → /api/sync；差距小（如 1–2 条）→ WS 单频道接收
```

详见 [[concepts/ws-event-types]]。

## 边界用例

| 用例 | 行为 |
|------|------|
| `cursor[ch]` 为 0 | 拉前 200 条历史 |
| `cursor[ch]` > server.last_seq | 返空 + 当前 last_seq；客户端自纠 |
| `cursor` 包含已退出频道 | 服务端忽略 |
| `cursor` 完全空 | 无返回（必须先调 GET /api/channels 拿列表） |
| 单频道大量未读（>200） | `has_more=true`，客户端循环 |

## Tauri 端协作

[[entities/im-seq-data-source]] 在 WS pong 触发后调 sync，把 msgs 落本地 sled / sqlite，并更新 `{channelID → last_seq}`。

## 性能与限制

- 每频道单次 200 条：避免单 user 的「巨包」拖累 PG
- 500 channel/call 上限：客户端持频道数 > 500 时分批
- 服务端按 channel 并行 FetchAfter（`errgroup` + 限流）—— 实现细节看 `service/sync.go`

## 测试

- 集成：`v5_single_flows_test.go` V5.1（happy）、`m4_message_send_sync_test.go`
- 单元：`sync_test.go` 用 `SyncChannelStore` mock

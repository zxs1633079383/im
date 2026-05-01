---
type: entity
title: service.SyncService
status: stable
last_verified: 2026-04-28
sources:
  - server/internal/service/sync.go
  - server/docs/BACKEND.md#§3.3
  - docs/ARCHITECTURE.md#§3.2
related:
  - concepts/seq-cursor
  - flows/incremental-sync
  - entities/alloc-seq-and-insert
confidence: high
---

# `service.SyncService`

> `POST /api/sync` 唯一处理者。基于 seq 游标计算 delta，**取代** Mattermost 的 bitmap+segment。

## 接口

```go
type SyncService interface {
    Sync(ctx, userID string, cursor map[int64]int64) (channels []ChannelSync, error)
}
```

## 契约（冻结，前后端 + Rust 必须对齐）

源：`server/docs/BACKEND.md §3.3`

```jsonc
// Request
{
  "cursor": { "10086": 5230, "10087": 128 },     // channelID → 客户端已知 seq
  "limit_per_channel": 200                       // 默认 200，max 由后端定
}

// Response
{
  "channels": [
    {
      "channel_id": 10086,
      "msgs": [...],                              // ASC by seq，最多 limit_per_channel 条
      "last_seq": 5275,                           // 服务端当前 seq
      "has_more": false                           // 是否还有更新页
    }
  ]
}
```

## 关键参数

| 参数 | 值 | 修改要点 |
|------|----|---------|
| `MaxChannelsPerCall` | 500 | 客户端一次最多带 500 个 channel cursor，超出截断 |
| `limit_per_channel` | 200 | 单 channel 单次最多 200 条；客户端通过 `has_more` 翻页 |

## 实现概要

```text
Sync(ctx, userID, cursor)
  ├─ ChannelStore.ListMembership(userID)           列出用户加入的频道
  ├─ for each (channelID, knownSeq) in cursor:
  │    if knownSeq < channel.last_seq:
  │      MessageRepo.FetchAfter(channelID, knownSeq, limit_per_channel)
  │      append to channels[]
  └─ return
```

`SyncChannelStore` 是 [consumer-side small interface](https://www.dolthub.com/blog/2024-09-13-go-interface-design/)：service 只看到自己需要的 channel 子集，便于测试时 mock。位置：`service/sync.go:25` 附近。

## 与心跳的关系

WS 心跳 `ping.channel_seqs` 与 `pong.channel_seqs` **本身不调** SyncService —— 心跳只回「哪些 channel 有 delta」（[[concepts/ws-event-types]]）。客户端拿到 pong 后**主动**发 `POST /api/sync` 拉数据。

## 边界用例

- 客户端 cursor 包含**已退出的频道** → 后端忽略（不返回那条 channel）
- `cursor[channelID] > server.last_seq`（异常）→ 返回空 msgs + 当前 last_seq，客户端应自我修正
- `cursor` 为空 → 不退化为「拉全量」，必须先调 `GET /api/channels` 拿到 channel 列表

## 测试

- 集成：`server/tests/integration/v5_single_flows_test.go` V5.1 增量同步 happy
- M4：`m4_message_send_sync_test.go` 验证 sender_id (TEXT) / team_id 字段透传

## 已知 gap

- 没有 server-push 模式（即使 last_seq 变化也不主动告知）—— 客户端依赖心跳 + sync 拉
- 历史数据 phantom 映射未实现（[[milestones/M5-historical-etl]] 才会做）

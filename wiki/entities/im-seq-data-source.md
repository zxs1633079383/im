---
type: entity
title: ImSeqDataSource (Tauri Rust)
status: stable
last_verified: 2026-04-28
sources:
  - client/src-tauri/src/data_center/im_seq_data_source.rs
  - server/docs/FRONTEND.md
related:
  - entities/im-api-adapter
  - entities/sync-service
  - concepts/seq-cursor
  - concepts/api-flavor-switch
confidence: medium
---

# `ImSeqDataSource` —— Tauri 端 seq 游标持久化

> 替换原来基于 bitmap+segment 的 IMDataSource。负责持久化 `{channelID → seq}`，调 `/api/sync` 增量补拉。

## 文件位置

`client/src-tauri/src/data_center/im_seq_data_source.rs`（约 253 行）

## 数据模型

```rust
struct ChannelCursor {
    channel_id: i64,
    last_seq: i64,
    updated_at: i64,         // ms
}

// 持久化：sled / sqlite（取决于实现）
```

## 关键操作

| 操作 | 触发 | 行为 |
|------|------|-----|
| `init()` | 应用启动 | 从本地存储加载所有 cursor |
| `on_pong(channel_seqs)` | WS pong 含 delta 频道 | 标记需要补拉 |
| `pull_delta()` | pong 后或定时 | 调 `POST /api/sync`，cursor=本地状态 |
| `on_push_msg(msg)` | WS push_msg 到达 | 更新 `last_seq = max(local, msg.seq)`；持久化 |
| `mark_read(channelID, seq)` | 用户读消息 | 单独 last_read_seq 维护（与 last_seq 不同） |

## 与服务端契约的对齐

调 `/api/sync` 时 cursor 字段类型必须是 `map[int64]int64`，limit_per_channel ≤ 200，详见 [[entities/sync-service]] / [[flows/incremental-sync]]。

## 切换开关

Rust feature flag：`im_seq_sync`

```toml
[features]
default = []
im_seq_sync = []
```

启用时 `AppState` 注入 `ImSeqDataSource`；否则继续 `IMDataSource`（旧 bitmap）。前端 `apiFlavor: 'im'` 与 Rust `im_seq_sync` **应当一致**，但二者由不同代码路径切换 —— 验证清单包括两侧对齐检查。

## 与 IPC event 命名

WS 事件名映射（前后端 + Rust 三方对齐）：

| WSMessageType | Rust IPC event |
|---------------|---------------|
| `push_msg` | `imWs:msg:pushed` |
| `msg_updated` | `imWs:msg:updated` |
| `msg_deleted` | `imWs:msg:deleted` |
| `read_sync` | `imWs:read:synced` |

新增类型同步改三处 —— 见 [[concepts/ws-event-types]]。

## 已知 gap

- 没有 cursor 校验：本地 `last_seq` 损坏 / 错位时无 self-heal
- 没有断链补偿：长时间离线后 cursor 距 server.last_seq 过大，`/api/sync` 一次只回 200 条/频道，需要客户端轮询 `has_more`

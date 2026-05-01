---
type: concept
title: WSMessageType V1 锁定（12 + M2 4 = 16）
status: stable
last_verified: 2026-04-28
sources:
  - server/docs/BACKEND.md#§1.1
  - docs/GOAL.md#§4.1
  - server/internal/gateway/types.go
related:
  - decisions/hard-constraints
  - entities/im-seq-data-source
  - entities/hub
confidence: high
---

# WSMessageType V1 锁定（12 + M2 4 = 16）

> 三方契约：Go server `gateway/types.go` + Angular client + Tauri Rust IPC。**新增类型必须升 V2 + 三方同步改动**。

## V1（M1 交付，12 种）

| 类型 | 方向 | 用途 |
|------|------|------|
| `ping` | C→S | 心跳，带 `channel_seqs` |
| `pong` | S→C | 心跳回包，仅含 delta 频道 |
| `send` | C→S | 客户端发消息（HTTP 外的快车道，可选） |
| `send_ack` | S→C | 服务端确认 send，含 seq + server_msg_id |
| `push_msg` | S→C | 服务端推新消息（含 phantom） |
| `push_ack` | C→S | 客户端 ACK，按 push_id 幂等 |
| `sync_resp` | S→C | WS 内嵌 `/sync` 响应（可选） |
| `read_sync` | S→C | 同用户其他设备已读位置推进 |
| `friend_event` | S→C | 好友请求 / 接受 / 拒绝 |
| `channel_event` | S→C | 频道成员变动 |
| `msg_updated` | S→C | M1 新增：消息编辑后推送 |
| `msg_deleted` | S→C | M1 新增：消息撤回后推送 |

## M2 追加（4 种）

| 类型 | 方向 | 用途 |
|------|------|------|
| `announcement_posted` | S→C | 公告发布 |
| `urgent_posted` | S→C | 紧急消息 |
| `approval_updated` | S→C | 审批状态变化 |
| `notification_received` | S→C | 系统通知 |

## V2 候选（M5+，不在验收范围）

| 类型 | 用途 |
|------|------|
| `reaction_updated` | 表情反馈 |
| `typing` | 正在输入 |
| `presence` | 在线状态变化（替换 Mattermost `status_change`） |

## 三方对齐表

| Go const | Angular | Rust IPC |
|----------|---------|----------|
| `gateway.MsgPushMsg` = `"push_msg"` | `'push_msg'` | `imWs:msg:pushed` |
| `MsgUpdated` = `"msg_updated"` | `'msg_updated'` | `imWs:msg:updated` |
| `MsgDeleted` = `"msg_deleted"` | `'msg_deleted'` | `imWs:msg:deleted` |
| `ReadSync` = `"read_sync"` | `'read_sync'` | `imWs:read:synced` |

新增**必须**改三处：
1. `server/internal/gateway/types.go`
2. Angular `core/ws/ws-event-types.ts`
3. Tauri Rust IPC event 名

漏一处 → 客户端 case 漏处理 → 静默丢事件。

## ping/pong 增量设计

```jsonc
// C → ping
{
  "type": "ping",
  "channel_seqs": { "10086": 5230, "10087": 128 }
}

// S → pong
{
  "type": "pong",
  "server_time": 1714291200,
  "channel_seqs": { "10086": 5275 }    // 仅返回有 delta 的频道
}
```

「一个心跳 = 一次增量信号」。客户端拿到 pong 知道 10086 有更新 → 主动 `POST /api/sync` 拉。

## 红线

- ❌ 不准在 M5 前新增 V1 之外的事件
- ❌ 不准把同义事件用不同名字混用（如不能既有 `read_sync` 又有 `read_position_synced`）
- ❌ 不准把 V1 事件的 payload schema 改 breaking change
- ✅ 加字段允许（client 容忍 unknown），删字段不允许

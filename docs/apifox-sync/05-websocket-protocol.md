# 05 WebSocket 协议 — 22 type 完整 payload + 时序

## 1. 连接与握手

```
GET /ws HTTP/1.1
Host: localhost:8080
Upgrade: websocket
Connection: Upgrade
cookieId: 676cc4ccfbbc501161d5cd65
companyId: 6111fb0a202d425d221c53db
userId: 676cc4ccfbbc501161d5cd65          (可选，等于 cookieId)
Sec-WebSocket-Key: …
Sec-WebSocket-Version: 13
```

也支持 query 鉴权：`ws://host/ws?cookieId=...&companyId=...&device=mac-desktop`。

成功返回 `HTTP/1.1 101 Switching Protocols` + 标准 WS 升级 header。

device 参数：未传时 server 生成 `web-<unix_nano>`。建议客户端固定 `device=<machine-id>`，便于跨次连接审计。

## 2. 帧 envelope

每个帧都是 JSON：

```json
{ "type": "<wsmsg_type>", "payload": <json | string> }
```

`payload` 既可以是嵌套 object 也可以是 string（json-in-json，gateway 的 PulsarPushEnvelope 走后者）。处理上 `json.RawMessage` 容忍两种。

## 3. 22 type 总表

### 3.1 客户端 → 服务端 (4 type)

| type | server 响应 | payload | 用途 |
|---|---|---|---|
| `ping` | **无回复**（仅 refresh routing TTL + ChannelSeqs） | `{channel_seqs: {channel_id_str: client_seq}}` | 15s 一次心跳 |
| `send` | `send_ack` + `push_msg` (channel 广播) | SendPayload (见下) | WS 直发消息 |
| `push_ack` | **无回复**（通知 globalACKRegistry） | `{push_id: "…"}` | 客户端 ACK push_msg |
| `sync` | ⚠️ **当前 server-side WS readPump 不 dispatch**，sync 走 HTTP POST /api/sync | SyncPayload | 重连补全 |

### 3.2 服务端 → 客户端 (18 type)

| type | payload | 触发 HTTP |
|---|---|---|
| `pong` | PongPayload | 服务端发起心跳（不是 client ping 的回复） |
| `push_msg` | PushMsgPayload | POST /api/channels/:id/messages 等 |
| `send_ack` | SendACKPayload | client send 帧的回执 |
| `sync_resp` | SyncRespPayload | POST /api/sync 返回时也通过 WS 复发 |
| `read_sync` | ReadSyncPayload | POST /api/channels/:id/read |
| `friend_event` | FriendEventPayload | POST /api/friends/{request,accept,reject} |
| `channel_event` | ChannelEventPayload | POST /api/channels/:id/members |
| `msg_updated` | `{server_msg_id, channel_id, content}` | PATCH /api/messages/:id |
| `msg_deleted` | `{server_msg_id, channel_id}` | DELETE /api/messages/:id |
| `announcement_posted` | `{announcement_id, channel_id, title, content}` | POST /api/announcements |
| `urgent_posted` | `{server_msg_id, channel_id, sender_id, content}` | POST /api/messages/urgent |
| `approval_updated` | `{approval_id, status, actor_id}` | POST /api/approvals/* |
| `notification_received` | `{notification_id, title, content, biz_type}` | POST /api/notifications |
| `reaction_added` | `{server_msg_id, channel_id, user_id, emoji}` | POST /api/messages/:id/reactions |
| `reaction_removed` | `{server_msg_id, channel_id, user_id, emoji}` | DELETE /api/messages/:id/reactions/:emoji |
| `channel_top_updated` | `{channel_id, is_top}` | PATCH /:id/members/:uid `{is_top}` |
| `channel_info_updated` | `{channel_id, name, notice, purpose, orient}` | PATCH /api/channels/:id |
| `channel_closed` | ChannelClosedPayload | DELETE /api/channels/:id |
| `channel_member_updated` | ChannelMemberUpdatedPayload | POST/DELETE members + nickname |
| `schedule_created` | ChannelSchedulePayload | POST /api/messages/scheduled |
| `schedule_canceled` | ChannelSchedulePayload | DELETE /api/messages/scheduled/:id |

> 22 个 server→client type **被 docs/harness/C005 锁定**。新增类型必须走 V2 RFC + 前后端同步改动。

## 4. 关键 payload 详解

### 4.1 PushMsgPayload (push_msg)

```json
{
  "push_id": "p-uuid",
  "type": "NOTICE",             // 仅 msg_type=4 (system) 时填，正常聊天空
  "channel_id": 1001,
  "seq": 425,
  "server_msg_id": 88001,
  "sender_id": "676cc4ccfbbc501161d5cd65",
  "content": "hello",
  "msg_type": 1,
  "visible_to": [],
  "props": "{\"sys_type\":\"join\"}",  // 仅 msg_type=4 时
  "created_at": "2026-05-12T08:30:00Z"
}
```

客户端必须根据 `push_id` 回 `push_ack`，否则服务端 send_failure_tracker 会在阈值内重发。

### 4.2 SendPayload (client→server)

```json
{
  "client_msg_id": "uuid-from-client",
  "channel_id": 1001,
  "content": "hi",
  "msg_type": 1,
  "visible_to": []
}
```

server 响应 send_ack：

```json
{
  "client_msg_id": "uuid-from-client",
  "server_msg_id": 88001,
  "seq": 424,
  "channel_id": 1001
}
```

随后 server 还会 push_msg 广播到 channel 全员（包括发送者其他设备）。

### 4.3 ChannelMemberUpdatedPayload

```json
{
  "channel_id": 1001,
  "change_type": "join",        // join | leave | kick | nickname
  "actor_id": "uA",
  "target_id": "uB",
  "nick_name": "",               // 仅 change_type=nickname
  "members": [
    { "user_id": "uA", "role": 9, "nick_name": "Owner", "is_top": false, "notify_pref": 0 },
    { "user_id": "uB", "role": 1, "nick_name": "", "is_top": false, "notify_pref": 0 }
  ]
}
```

`members` 是变更后**完整** roster，客户端直接整体替换。

## 5. 时序图

### 5.1 双向 send 路径

```
Client (A)                            Gateway A (process)              Pulsar              Gateway B
   │  send {ch:1001,content:"hi"}       │                                  │                      │
   │ ───────────────────────────────►   │ handleSend                       │                      │
   │                                    │  AllocSeqAndInsert (PG tx)       │                      │
   │  send_ack {server_msg_id, seq}     │                                  │                      │
   │ ◄───────────────────────────────   │                                  │                      │
   │                                    │ Hub.broadcastToChannel(ch=1001)  │                      │
   │  push_msg (本 pod 成员)            │                                  │                      │
   │ ◄───────────────────────────────   │                                  │                      │
   │                                    │ CrossPodPush(envelope, uids)     │                      │
   │                                    │ ─────────────────────────────►   │ producer.Send       │
   │                                    │                                  │ ─────────────────►   │
   │                                    │                                  │                      │ push_consumer
   │                                    │                                  │                      │ Hub.PushToUser
   │                                    │                                                          │ push_msg
   │                                                                                               │ ───► Client (A on pod B)
```

### 5.2 重连同步

```
Client                          Gateway
   │ WS connect                    │
   │ ───────────────────────────►  │ authenticate → upgrade 101
   │ HTTP POST /api/sync           │
   │   {channels:[{id,seq},…]}     │
   │ ───────────────────────────►  │ SyncService.Diff
   │  {channels: differences}      │
   │ ◄───────────────────────────  │
   │                               │
   │ (resume normal WS push_msg)   │
```

## 6. 心跳与超时

- 服务端发 ping （`heartbeat.go` 周期 30s），客户端收到回 pong。**对称的反向不存在**：client 发 ping 是「告诉服务端我还活着 + 上报 channel_seqs」，server 不回 pong。
- 服务端的 routing key（Redis `routing:user:<uid>:<gateway_id>:<device_id>`）TTL = **45s**。错过 3 次 ping → 自动从 routing 表清除。该常数与心跳 15s 耦合，改一边必同时改另一边（详见 docs/harness/C004）。

## 7. 已知客户端漂移点（cses-client 迁移）

| 旧 | 新 |
|---|---|
| message_type 字符串（"message" / "post_read"） | 严格 22 个 wsmsg_type |
| 双重 envelope (axios + isWrappedResponse) | 一层 `{status, data, error}` |
| user_id 当 BigInt 用 | string (mm UserID 24-hex) |
| created_at 当 unix ms | RFC3339 |
| post_read 推送 | 改用 read_sync（同账号他设备） |

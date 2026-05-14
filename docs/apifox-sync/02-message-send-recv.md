# 02 消息收发 — 全部 use cases

## 1. 端点 + 业务对照

| use case | 端点 | 触发 WS |
|---|---|---|
| 发送单条消息 | `POST /api/channels/:id/messages` | push_msg (channel 全员) |
| 批量同内容多 channel | `POST /api/messages/batch` | push_msg ×N |
| 转发已有消息 | `POST /api/messages/forward` | push_msg ×N |
| 拉取最近消息 | `GET /api/channels/:id/messages` | — |
| 围绕 anchor 拉上下文 | `GET /api/channels/:id/messages/around` | — |
| 拉某消息之后 | `GET /api/messages/:id/after` | — |
| 标记 channel 已读 | `POST /api/channels/:id/read` | read_sync (同账号他设备) |
| 编辑自己的消息 | `PATCH /api/messages/:id` | msg_updated (channel 全员) |
| 撤回 / 软删消息 | `DELETE /api/messages/:id` | msg_deleted (channel 全员) |
| 消息已读统计 | `GET /api/messages/:id/readers` | — |
| 批量已读数 | `GET /api/messages/read-stats?ids=` | — |
| 消息回复列表 | `GET /api/messages/:id/replies` | — |
| 回复支线树 | `GET /api/messages/:id/replies/branch` | — |
| 模板已收到回执 | `POST /api/messages/:id/received` | — |

## 2. 发送消息（POST /api/channels/:id/messages）

最高频路径，详细 schema。

### 请求

```json
{
  "content": "hello",
  "client_msg_id": "8e2c4f80-...-...",
  "msg_type": 1,
  "visible_to": [],
  "reply_to": null,
  "file_ids": []
}
```

`client_msg_id` 必填，用于客户端幂等（重连重发时同 id 不会被服务端重复插入）。

### 响应

```json
{
  "status": "success",
  "data": {
    "server_msg_id": 88001,
    "seq": 424,
    "channel_id": 1001,
    "created_at": "2026-05-12T08:30:00Z"
  }
}
```

### use case 表

| use case | 入参变化 | 业务说明 |
|---|---|---|
| UC-SEND-01 文本 | `msg_type:1, content:"…"` | 标准聊天 |
| UC-SEND-02 图片 | `msg_type:2, content:"image-meta", file_ids:[12]` | 客户端先 POST /api/files 拿到 file_id |
| UC-SEND-03 文件 | `msg_type:3, content:"file-meta", file_ids:[13]` | 同上 |
| UC-SEND-04 系统提示 | `msg_type:4, content:"…", props:"{...}"` | 服务端内部用，cses-client 不该自己发 |
| UC-SEND-05 私密消息 | `visible_to:["u_a","u_b"]` | 只对 a/b 可见，其他成员看到 phantom |
| UC-SEND-06 回复某消息 | `reply_to: 88000` | 客户端在 UI 上画引用线 |
| UC-SEND-07 重发幂等 | 同一 `client_msg_id` 重试 | 服务端去重返回原始 server_msg_id |
| UC-SEND-08 WS 直发 | 走 WS `send` 帧而非 HTTP POST | 同样的字段集，server 回 send_ack + 触发 push_msg |

## 3. 已读 / 读统计

### 标记 channel 已读：`POST /api/channels/:id/read`

```json
{ "last_read_seq": 425 }
```

服务端落库 channel_members.last_read_seq + 推 `read_sync` 到同用户他设备。

### 批量统计：`GET /api/messages/read-stats?ids=88001,88002`

```json
{
  "status": "success",
  "data": {
    "88001": { "read_count": 5, "total": 7 },
    "88002": { "read_count": 7, "total": 7 }
  }
}
```

### use case 表

| use case | 备注 |
|---|---|
| UC-READ-01 进入 channel | 进入后 last_read_seq 推到最新 |
| UC-READ-02 多设备已读同步 | A 设备 POST read 后，B/C 设备收到 read_sync WS |
| UC-READ-03 已读人列表 | UI 长按消息看 `GET /api/messages/:id/readers` |
| UC-READ-04 批量统计 | 列表上每条消息显示「已读 N」用 read-stats |

## 4. 编辑与撤回

| use case | 触发 | wire |
|---|---|---|
| UC-EDIT-01 编辑自己 | `PATCH /api/messages/:id {content:"新文本"}` | msg_updated WS to channel |
| UC-EDIT-02 编辑超时 | server 拒绝（business rule，默认 5min 内） | 400 error |
| UC-DEL-01 撤回自己 | `DELETE /api/messages/:id` | msg_deleted WS to channel |
| UC-DEL-02 管理员撤回他人 | 同上，按 role 鉴权 | msg_deleted + 系统消息 |
| UC-DEL-03 撤回后再拉 | `GET messages` 不返该条；`GET around` 返回 `{deleted_at:...}` 占位 |

## 5. 转发 / 批量

### 转发：`POST /api/messages/forward`

```json
{
  "message_id": 88001,
  "target_channel_ids": [1001, 1002, 1003]
}
```

- 服务端对每个目标 channel 各执行一次 `AllocSeqAndInsert`
- 每个目标 channel 各推一次 push_msg

### 批量同内容：`POST /api/messages/batch`

```json
{
  "channel_ids": [1001, 1002],
  "content": "周会通知",
  "msg_type": 1,
  "client_msg_id": "broadcast-uuid"
}
```

`client_msg_id` 全局唯一，去重一对多。

| use case | 适用 |
|---|---|
| UC-FWD-01 转发到群 | A 群里的消息分享到 B 群 |
| UC-FWD-02 多目标 | 一键转发到 N 个群 |
| UC-BATCH-01 公告群发 | 行政 / HR 同时通知多个组 |

## 6. 回复（threading）

| 端点 | 用途 |
|---|---|
| `GET /api/messages/:id/replies` | 列出某消息的**直接回复** |
| `GET /api/messages/:id/replies/branch` | 列出某消息的**全部支线树**（递归子回复） |

发回复消息时用 `POST /api/channels/:id/messages` 带 `reply_to: <parent_msg_id>`。

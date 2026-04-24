# HTTP ↔ WebSocket 推送对应矩阵

> 压测、E2E、客户端联调必读。每条 HTTP 请求触发哪些 WS 事件、谁收到、是否跨 pod，全部锁定在这里。

## 矩阵

| # | HTTP | WS 事件 | 接收方 | 跨 pod | 备注 |
|---|------|---------|--------|--------|------|
| 1 | `POST /api/channels/:id/messages` | `push_msg` | 频道所有**在线**成员（含 sender 以外） | ✅ | 最热路径。payload 含 seq/msg_id/content |
| 2 | `POST /api/channels/:id/read` | `read_sync` | **同一用户**的其他在线设备 | ✅ | 单设备压测无法验证 |
| 3 | `DELETE /api/messages/:id` | `msg_deleted` | 频道所有成员 | ✅ | payload 含 msg_id + channel_id |
| 4 | `PATCH /api/messages/:id` | `msg_updated` | 频道所有成员 | ✅ | payload 含 msg_id + content |
| 5 | `POST /api/channels/:id/members` | `channel_event` (type=added) | 被加的用户 | ✅ | 现有成员不收 |
| 6 | `DELETE /api/channels/:id/members/:uid` | `channel_event` (type=removed) | 被移除的用户 | ✅ | |
| 7 | `POST /api/channels/:id/leave` | `channel_event` (type=removed) | 离开者自己（多设备同步） | ✅ | |
| 8 | `PUT /api/channels/:id` | `channel_event` (type=updated) | 所有成员 | ✅ | M3 新增语义 |
| 9 | `POST /api/friends/request` | `friend_event` (type=request) | 被请求方 | ✅ | payload 含 from_user_id |
| 10 | `POST /api/friends/accept` | `friend_event` (type=accepted) | 原请求方 | ✅ | |
| 11 | `POST /api/friends/reject` | `friend_event` (type=rejected) | 原请求方 | ✅ | |
| 12 | `POST /api/announcements` | `announcement_posted` | 频道所有成员 | ✅ | M2 新增 |
| 13 | `POST /api/messages/urgent` | `urgent_posted` | 频道所有成员 | ✅ | M2 |
| 14 | `POST /api/approvals` / `/accept` / `/reject` | `approval_updated` | 发起方 + 审批方 | ✅ | M2 |
| 15 | `POST /api/notifications` (内部调用) | `notification_received` | 指定 user_id | ✅ | M2 |
| 16 | `POST /api/channels/:id/topics` (M3) | 发送 `push_msg` 系统消息到 topic | topic 成员 | ✅ | 复用 push_msg 不升协议 |
| 17 | `GET /api/presence?channel_id=X` (M3) | 无 | — | — | 仅 HTTP，查 Redis routing |

## 无 WS 推送的 HTTP（仅读）

- `GET /api/channels` / `GET /api/channels/:id` / `GET /api/channels/:id/members`
- `GET /api/channels/:id/messages` / `GET /api/channels/:id/messages/around`
- `GET /api/messages/:id/readers` / `GET /api/messages/:id/replies`
- `POST /api/sync`（是**拉取**而非推送触发端）
- `GET /api/friends` / `GET /api/friends/pending`
- `GET /api/announcements` 等查询端点
- `GET /api/presence`

## 压测覆盖建议

| 场景 | 覆盖的矩阵条 | k6 VU 比例建议 |
|------|--------------|----------------|
| 消息热路径 | #1 `push_msg` | 70% VU（10% sender + 60% 纯接收） |
| 编辑/撤回 | #3 #4 | 5% VU |
| 成员变动 | #5 #6 #8 | 5% VU |
| 好友流 | #9 #10 | 5% VU |
| 增量同步 | `POST /sync` | 10% VU（模拟重连拉取） |
| Presence 查询 | `GET /api/presence` | 5% VU |

## 端到端延迟统计点（dashboard 关联）

| Metric | 语义 |
|--------|------|
| `im.fanout.e2e.duration` | HTTP handler 从接到请求到返回（近似端到端） |
| `im.pulsar.producer.send.duration` | 跨 pod 推送的 Pulsar Send 耗时 |
| `im.message.alloc_seq.duration` | 消息入库 + seq 分配（每频道单行锁） |
| `im_push_latency_ms` (k6 自定义) | 压测端观察到的 HTTP→WS push_msg 收到的总时长 |

## 已知非对称性

- **read_sync** 是**同用户多设备**事件。单设备客户端压测无法直接验证，需要每 VU 起两条 WS。
- **channel_event (added)** 只推被加者，**不推**给现有成员。E2E 基线里 G8 的 `ev=false` 是正确现象。
- **topic_created (M3)** 复用 `push_msg` 下沉到 topic channel 本身，**不**升 WS 事件类型（V1 协议不变）。

## V2 候选（未实施）

- `typing`（V2 候选，M3 不做）
- `presence_changed`（V2 候选，当前走 HTTP GET 代替）
- `reaction_updated`（V2 候选）

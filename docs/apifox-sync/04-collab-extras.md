# 04 协作扩展 — 公告 / 审批 / 通知 / 反应 / 加急 / 定时 / 收藏 / 好友 / 快捷回复 / 文件

## 1. 公告（announcement）

| use case | 端点 | WS |
|---|---|---|
| 创建公告 | POST /api/announcements `{channel_id, title?, content}` | announcement_posted to channel |
| 标记已读 | POST /api/announcements/:id/read | — |
| 已读人列表 | GET /api/announcements/:id/acks | — |
| 删除公告 | DELETE /api/announcements/:id | — |
| channel 公告列表 | GET /api/channels/:id/announcements | — |
| 公告详情 | GET /api/announcements/:id | — |

公告 UI 通常在 channel header 显示，点开走 announcement_id 拉详情。

## 2. 审批（approval）

| use case | 端点 | WS approval_updated |
|---|---|---|
| 发起审批 | POST /api/approvals `{channel_id, title, content, approver_ids}` | ✅ status=pending |
| 通过 | POST /api/approvals/:id/approve | ✅ status=approved |
| 拒绝 | POST /api/approvals/:id/reject | ✅ status=rejected |
| 撤销（发起人） | POST /api/approvals/:id/cancel | ✅ status=canceled |
| 我待办 | GET /api/approvals/pending | — |
| 我发起 | GET /api/approvals/mine | — |
| 详情 | GET /api/approvals/:id | — |

approval_updated WS 载荷 `{approval_id, status, actor_id}`，客户端按 approval_id 拉详情，不要拼业务。

## 3. 通知中心（notification）

通用消息推送（非 IM 聊天），由其他业务系统调发：

| use case | 端点 | WS notification_received |
|---|---|---|
| 发通知 | POST /api/notifications `{to_user_ids, title, content, biz_type}` | ✅ |
| 我收到 | GET /api/notifications/received | — |
| 我发送 | GET /api/notifications/sent | — |
| 标记已读 | POST /api/notifications/:id/read | — |

`biz_type` 是业务方自定义（如 `order_pay` / `meeting_invite`），客户端按它分类渲染。

## 4. 反应表情（reaction）

| use case | 端点 | WS |
|---|---|---|
| 加反应 | POST /api/messages/:id/reactions `{emoji:"👍"}` | reaction_added to channel |
| 移除反应 | DELETE /api/messages/:id/reactions/:emoji | reaction_removed to channel |
| 查消息全部反应 | GET /api/messages/:id/reactions | — |

WS payload 含 `server_msg_id` + `emoji` + `user_id`，客户端做 +1 / -1 增量。

## 5. 加急消息（urgent）

| use case | 端点 | WS urgent_posted |
|---|---|---|
| 发加急 | POST /api/messages/urgent `{channel_id, content, client_msg_id}` | ✅ to channel members |
| 我已确认 | POST /api/messages/:id/urgent/confirm | — |
| 撤销加急（发件人） | POST /api/messages/:id/urgent/cancel | — |
| 谁确认了 | GET /api/messages/:id/urgent/confirmations | — |

收件人 UI 收到 urgent_posted 后强制弹窗 + 铃声，按「我知道了」触发 confirm。

## 6. 定时消息（scheduled）

| use case | 端点 | WS schedule_* |
|---|---|---|
| 设定时发送 | POST /api/messages/scheduled `{channel_id, content, scheduled_at: RFC3339}` | schedule_created to 同 userId 全设备 |
| 我的待发 | GET /api/messages/scheduled | — |
| 取消 | DELETE /api/messages/scheduled/:id | schedule_canceled |

到点后 cron job 触发实际发送，按正常 push_msg 走。

## 7. 收藏（favorite）

| use case | 端点 |
|---|---|
| 收藏 | POST /api/favorites/:message_id |
| 取消 | DELETE /api/favorites/:message_id |
| 列收藏 | GET /api/favorites |

## 8. 好友（friend）

| use case | 端点 | WS friend_event |
|---|---|---|
| 发申请 | POST /api/friends/request `{to_user_id, message?}` | event_type=request |
| 接受 | POST /api/friends/accept | event_type=accepted |
| 拒绝 | POST /api/friends/reject | event_type=rejected |
| 列好友 | GET /api/friends | — |
| 待处理申请 | GET /api/friends/pending | — |
| 拉黑 | POST /api/friends/block | — |

friend_event WS payload `{event_type, from_user_id}`。

## 9. 快捷回复（quick reply）

| use case | 端点 |
|---|---|
| 新建 | POST /api/quick-replies `{content, shortcut?}` |
| 列表 | GET /api/quick-replies |
| 修改 | PATCH /api/quick-replies/:id |
| 删除 | DELETE /api/quick-replies/:id |

per-user 私有列表，类似 Slack 的 `/shortcut` 文案模板。

## 10. 文件（file）

| use case | 端点 | 备注 |
|---|---|---|
| 上传 | POST /api/files (multipart) | 返回 `{file_id, url}` 供 POST messages 引用 |
| 下载 / meta | GET /api/files/:id | 返回二进制或 302 OSS |
| 消息附件列表 | GET /api/messages/:id/attachments | 通过 message_id 反查全部 attach |

## 11. 搜索 + 模块 + 设置

| 端点 | 用途 |
|---|---|
| GET /api/search?q=&scope=message&limit= | 全局搜索 messages / channels / users |
| GET /api/modules | 客户端入口模块开关（IM / Approval / Announcement 等） |
| GET /api/settings | per-user 设置 |
| PUT /api/settings | 整体覆盖 settings |

## 12. 模板已收到回执（v0.7.3）

```bash
POST /api/messages/:id/received
```

仅用于 msg_type=4 + props.sys_type=template 类消息。客户端点「我已收到」时调一次，落库到 template_received。无 WS 回声（per-user 静态）。

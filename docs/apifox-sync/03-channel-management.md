# 03 群聊管理 — 全部 use cases

## 1. 端点总览

| 域 | 端点 |
|---|---|
| 基础 CRUD | POST /api/channels · POST /api/channels/dm · GET /api/channels · GET PUT PATCH DELETE /api/channels/:id |
| 成员管理 | POST /api/channels/:id/members · DELETE /:id/members/:uid · GET members · POST /:id/leave · PATCH /:id/members/:uid · PATCH /:id/members/:uid/nickname |
| 管理员治理 | POST /:id/managers/:uid · DELETE managers · GET managers |
| 置顶消息 | POST /:id/pins/:msg_id · DELETE pins · GET pins |
| 话题 | POST /:id/topics · GET topics |
| 在线状态 | GET /api/channels/online-status |

## 2. channel 生命周期

```
                 POST /api/channels {name, type, member_ids}
                                    ↓
                        创建 → channels.deleted_at = NULL
                                    ↓
       PUT  /api/channels/:id        全字段更新（name/purpose/header）
       PATCH /api/channels/:id       局部更新 (governance) → channel_info_updated WS
                                    ↓
       POST /:id/members             加人 → channel_event WS to 新成员
       DELETE /:id/members/:uid      踢人 → channel_member_updated WS
       POST /:id/leave               主动退 → channel_member_updated WS
                                    ↓
       DELETE /api/channels/:id      owner 解散 → channel_closed WS to 全员
                                    ↓
                         channels.deleted_at = now
                         （客户端 dialog.deleteAt > 0 即只读）
```

## 3. 类型与权限

| 字段 | 取值 | 说明 |
|---|---|---|
| `type` | `public` / `private` / `dm` | dm 由 POST /api/channels/dm 自动幂等创建 |
| `permission` | smallint | bit mask: 1=管理员可邀请 / 2=管理员可踢人 ... |
| `orient` | smallint | 0=open 1=closed（governance 字段，v0.7.0+） |
| `role` (成员) | 1=普通 / 2=管理员 / 9=owner | DELETE /managers 不会动 owner role |

## 4. use case 矩阵

### 4.1 创建

| use case | 端点 | body | 备注 |
|---|---|---|---|
| UC-CH-01 普通群 | POST /api/channels | `{name, type:"public"}` | 默认调用者为 owner |
| UC-CH-02 私密群 | POST /api/channels | `{name, type:"private", member_ids:[u1,u2]}` | 创建后调一次 add members |
| UC-CH-03 DM 私聊 | POST /api/channels/dm | `{peer_user_id:"…"}` | 幂等：同对端只建 1 个 |

### 4.2 元信息修改

| use case | 端点 | 触发 WS |
|---|---|---|
| UC-CH-MOD-01 改名/公告/简介 | PUT /api/channels/:id | channel_info_updated |
| UC-CH-MOD-02 governance 局部 | PATCH /api/channels/:id | channel_info_updated (只含变更字段) |
| UC-CH-MOD-03 用户置顶 | PATCH /:id/members/:uid `{is_top:true}` | channel_top_updated (仅同账号) |
| UC-CH-MOD-04 通知偏好 | PATCH /:id/members/:uid `{notify_pref:1}` | 无（per-user 静态） |

### 4.3 成员

| use case | 端点 | 备注 |
|---|---|---|
| UC-CH-MEM-01 加人 | POST /:id/members `{user_ids:[…]}` | channel_event WS to 新成员 + channel_member_updated WS to 老成员 |
| UC-CH-MEM-02 踢人 | DELETE /:id/members/:uid | change_type=kick |
| UC-CH-MEM-03 主动退 | POST /:id/leave | change_type=leave |
| UC-CH-MEM-04 改群昵称 | PATCH /:id/members/:uid/nickname `{nick_name:"小张"}` | change_type=nickname |

`channel_member_updated` 载荷里 `members` 是变更后**完整 roster**，客户端一次性替换本地成员列表，避免按字段 patch。

### 4.4 管理员

| use case | 端点 | 备注 |
|---|---|---|
| UC-CH-MGR-01 设管理员 | POST /:id/managers/:uid | role 1→2 |
| UC-CH-MGR-02 取消管理员 | DELETE /:id/managers/:uid | role 2→1，owner 不可降 |
| UC-CH-MGR-03 列管理员 | GET /:id/managers | role >= 2 的成员 |

### 4.5 置顶消息（per-channel pin）

| use case | 端点 |
|---|---|
| UC-CH-PIN-01 置顶 | POST /:id/pins/:message_id |
| UC-CH-PIN-02 取消置顶 | DELETE /:id/pins/:message_id |
| UC-CH-PIN-03 列置顶 | GET /:id/pins → 按 pinned_at 倒序 |

### 4.6 话题（per-channel topic threads）

| use case | 端点 |
|---|---|
| UC-CH-TOPIC-01 建话题 | POST /:id/topics `{title, content}` |
| UC-CH-TOPIC-02 列话题 | GET /:id/topics |

### 4.7 在线状态

| use case | 端点 |
|---|---|
| UC-CH-ON-01 查多 channel 在线人数 | GET /api/channels/online-status?channel_ids=… |
| UC-CH-ON-02 查多用户在线 | GET /api/presence?user_ids=… |

## 5. 解散

`DELETE /api/channels/:id` → owner 软删 → 推 `channel_closed` 到全员。

客户端必须基于 `payload.deleted_at` 渲染：
- `deleted_at > 0`: 该会话置只读 + 顶部 banner「群聊已解散」
- 不要删本地数据，等用户主动「清理」

## 6. WS 事件汇总（群聊域）

| WS type | 触发 | 范围 |
|---|---|---|
| channel_event | 新成员加入 | 新成员单端 |
| channel_member_updated | join/leave/kick/nickname | channel 全员 |
| channel_info_updated | 元信息变 | channel 全员 |
| channel_top_updated | 用户 is_top 切 | 同 userId 全设备 |
| channel_closed | 解散 | channel 全员 |

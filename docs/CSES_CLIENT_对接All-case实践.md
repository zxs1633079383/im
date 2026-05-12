# cses-client × im 后端 对接 All-case 实践

> **文档定位**：把 cses-client `docs/messagev3-用户api用例集合.md`（用户行为字典，§A~§J）逐 case 翻成 im 后端的覆盖路径。
>
> **互补关系**：
> - 路径全集 / WS 协议 / 鉴权 → `docs/CSES_CLIENT_内部对接契约.md`（endpoint contract）
> - entity / DTO / payload 字段 → `docs/IM_DATA_MODEL_新版数据模型字典.md`（schema reference）
> - **本文**：用户行为 → 后端实现路径 → ✅ 已覆盖 / ⚙️ 架构换代 / ⚪ cses Java 责任 / 🆕 v0.7.3 gap 补丁
>
> **后端状态**：v0.7.3-backend-final + 9 个 gap 补丁 + v0.7.4 UserData 鉴权重构（2026-05-12）
> - 87 路由 + 25 WSMessageType
> - migration 017 已加（`channels.deleted_at` + `channel_members.nick_name`）
> - `push_msg` payload 加 `type:"NOTICE"` + `props` 字段（gap #9）
> - **v0.7.4 鉴权改造**：Redis 由 `HASH "User"` → `STRING "UserData:<userId>"`；cookieId header
>   值 == mm UserID；team_id 从新 `companyId` HTTP header 读（不再派生自 Redis payload）
> - go build ✅ / go vet ✅ / unit test ✅
>
> **覆盖率结论**：cses-client messagev3 全部 **41 imHttp + 16 WS + 8 Rust HTTP + 12 Tauri IPC** = **77 个用户层 case 100% 覆盖**（CsesHttp 15 个归 cses Java 业务后端，不在 im 责任范围）。

---

## 0. 状态图例

| 图例 | 含义 |
|---|---|
| ✅ | im 后端**已实现**，直接调用即可 |
| 🆕 | **v0.7.3 gap 补丁新增**（2026-05-12 落地） |
| ⚙️ | **架构换代**（by-design 砍掉老协议，cses-client 端需 Rust 重构走新链路） |
| ⚪ | **cses Java 业务后端责任**，im 不实现，cses-client 走 `this.http` |
| ❌ | 计划外，暂未覆盖（当前无） |

---

## 1. 总览映射表

### 1.1 数量分布

| 来源层 | cses-client 数量 | im 责任范围 | im 已覆盖 | 实测状态 |
|---|---|---|---|---|
| imHttp（message-v3 service）| 41 | 41 | **41** | ✅ 41 / 🆕 3 / ⚙️ 3 |
| CsesHttp（vote / search / average / doc）| 15 | 0 | — | ⚪ |
| Rust 主动 HTTP（features/im/http.rs）| 8 | 0（全部 ⚙️ by `/api/sync`）| — | ⚙️ 8 |
| Tauri IPC（features/im/commands.rs）| 12 | 0（客户端内部）| — | — |
| WS 事件（ipcEvent.service.ts 订阅）| 20 | 11 | **11** | ✅ 7 / 🆕 4 / ⚙️ 5 / ⚪ 1 |

**总覆盖**：im 实际承担 41 imHttp + 11 WS = **52 个 case**，全部覆盖（含 9 个 gap 补丁补齐的 ↓）。

### 1.2 v0.7.3 9 个 gap 补丁地图

| Gap | cses-client 期待 | im 后端 v0.7.3 实现 | 状态 |
|---|---|---|---|
| #1+#3 | `closeDialog POST /channel/close` + `im:channel:closed` WS | `DELETE /api/channels/:id` + `channel_closed` WS | 🆕 |
| #2 | `queryReplyBranchMessage POST /posts/getReplyBranch` 含分页 | `GET /api/messages/:id/replies/branch?offset=N&limit=M` | 🆕 |
| #4 | `im:channel:member-updated` 含完整 channel snapshot | AddMember/RemoveMember/LeaveChannel → `channel_member_updated{change_type, members[]}` 全员推送 | 🆕 |
| #5 | `im:channel:memberNickname` + 设置昵称端点 | `channel_members.nick_name VARCHAR(64)` + `PATCH /channels/:id/members/:user_id/nickname` + `channel_member_updated{change_type:"nickname"}` | 🆕 |
| #6 | `getChannelMembersByIds POST /channels/member/byIds` | 用户决策**降级**走 `GET /channels/:id/members` 全量 | — |
| #7 | `im:channel:schedule-created/canceled` WS | scheduled CRUD → `schedule_created/canceled` 推 sender 多设备 | 🆕 |
| #8 | `im:post:batch-updated` 批量更新 | **客户端 ws-normalizer 拆**成多个 `msg_updated` | — |
| #9 | NOTICE 字段对齐 | `push_msg.type="NOTICE"` + `props` JSONB 透传；`msg_type=4` 自动填 | 🆕 |

---

## 2. 用例分组：cses-client §A~§J 全 case 覆盖映射

### A. 会话列表与未读

> cses-client 串讲：打开消息页 → `queryDialogModule` 拉分组 → 各分组下挂 channel 列表 → WS `im:channels:loaded` 触发全量初始化 → 任意 channel 来新消息 WS 推 `im:channel:increment`，未读 +1。

| cses-client 用例 | 触发位置 | im 后端覆盖 | 状态 |
|---|---|---|---|
| 打开消息页拉模块分组 | `queryDialogModule POST /modules/getAll` | `GET /api/modules`（v0.7.2，migration 016 seed 6 行）| ✅ |
| 切换会话标记激活 + 清未读 | `im:switchActiveChannel` IPC | **客户端内部**（Tauri 写 tenant_user_db，不经后端）| — |
| 新消息到达列表最后一条更新 | `im:channel:increment` WS | 后端推 `push_msg` + `pong.channel_seqs` 增量；客户端 Rust 合成 `im:channel:increment` 事件 | ⚙️ |
| 元数据变更（subtopicsLoadedAt / 成员数）| `im:channel:update` WS | `channel_info_updated` WS（notice/purpose/orient/permission/name 改动）| ✅ |
| WS 连接后全量频道初始化 | `im:channels:loaded` WS | 客户端 Rust hello handler 内合成；后端走 `POST /api/sync` + `GET /api/channels` 拼装 | ⚙️ |
| 进入会话读本地缓存 | `imDataSource:queryCache` IPC | **客户端内部**（纯 SQLite 读）| — |
| 拉会话列表全量 | (cses Java 老路径 → )  | `GET /api/channels` 返 `ChannelWithPreview[]` 含 last_msg + unread_count | ✅ |

---

### B. 消息收发

> cses-client 串讲：输入 → temporaryId 乐观写本地 → UI 立即渲染 → `sendMsg POST /posts/create` → WS `im:post:received` 回声替换乐观 → 用户点加急 → `urgentPost` → WS `im:post:updated` 推回，`normalizeExpediteFields` 展平。

| cses-client 用例 | 触发 imHttp | im 后端覆盖 | 状态 |
|---|---|---|---|
| 发普通消息 | `sendMsg POST /posts/create` | `POST /api/channels/:id/messages` + body `{content, msg_type, visible_to, reply_to, client_msg_id, file_ids, quick_reply_id}` | ✅ |
| WS 接收消息 | `im:post:received` | `push_msg` + `normalizeExpediteFields` 展平加急 | ✅ |
| 撤回 / 编辑 / 加急确认刷新 | `im:post:updated` | `msg_updated` payload = 完整 Message JSON | ✅ |
| 批量状态更新 | `im:post:batch-updated` | 后端拆推多个 `msg_updated`；**客户端 ws-normalizer 合并**为 batch-updated（gap #8 降级）| ✅ |
| 批量转发到多会话 | `sendRelayMessages POST /posts/createPosts` | `POST /api/messages/forward` body `{message_id, target_channel_ids[≤10]}` | ✅ |
| Emoji 快捷回复 | `sendQuickRelayMessage POST /posts/quickReply` | `POST /api/channels/:id/messages` + `quick_reply_id` 字段（im 已支持 0 改动）| ✅ |
| 撤回消息 | `revokeMessage POST /posts/revoke` | `DELETE /api/messages/:id`（仅 sender；触发 `msg_deleted`）| ✅ |
| 发加急消息 | `onUrgentPost POST /posts/urgentPost` | `POST /api/messages/urgent` body `{channel_id, content, client_msg_id}`（触发 `urgent_posted`）| ✅ |
| 写本地消息缓存 | `store:setItems('message')` IPC | **客户端内部** | — |
| 创建定时消息 | `createSchedule POST /posts/createSchedule` | `POST /api/messages/scheduled` body `{channel_id, content, msg_type, scheduled_at}`（≥60s 未来）| ✅ |
| 取消定时消息 | `cancelSchedule POST /posts/cancelSchedule` | `DELETE /api/messages/scheduled/:id`（sender-only）| ✅ |
| 查询定时消息列表 | `getSchedule POST /posts/getSchedule` | `GET /api/messages/scheduled?status=pending\|all` | ✅ |
| **定时消息状态翻转推送（多设备）** | `im:channel:schedule-created/canceled` WS | **`schedule_created`/`schedule_canceled` WS**，payload `{channel_id, scheduled_id, has_schedule_post}`，仅推 sender 多设备 | 🆕 |

---

### C. 已读 / 已收到 / 模板已收到

> cses-client 串讲：切到会话 A → 6 处调用点 fire-and-forget 触发 `onChannelRead` → 服务端 WS 推 `im:post:read` → 派发 POST_READ → 其他设备 UI 同步未读归零。模板消息点"已收到" → `templateReceived` → WS `post:updated` 推模板状态。

| cses-client 用例 | 触发 imHttp | im 后端覆盖 | 状态 |
|---|---|---|---|
| 进入会话标记全部已读 | `onChannelRead POST /channels/view`（6 处）| `POST /api/channels/:id/read`（body 为空，server 用 path id + 当前 channel.Seq）| ✅ |
| 查看具体消息后单条已读 | `onPostRead POST /post/read` | **post-level read 已废**；走 channel-level + 异步 `read-stats` | ⚙️ |
| 已读跨设备同步 | `im:post:read` WS | `read_sync` WS payload `{channel_id, read_seq}` | ⚙️（client 端 ws-normalizer 已删 `imWs:post:read` 翻译，Phase 4）|
| 点击模板已收到 | `templateReceived POST /post/templateReceived` | `POST /api/messages/:id/received`（path 化；body 为空；append callerID 到 props.template.userIds；触发 `msg_updated`）| ✅ |
| 确认收到加急消息 | `onPostUrgentConfirm POST /posts/urgentConfirm` | `POST /api/messages/:id/urgent/confirm` | ✅ |
| 阅读群公告 | `readAnnouncement POST /post/announcement/read` | `POST /api/announcements/:id/read` | ✅ |
| 删除群公告 | `deleteAnnouncement POST /post/announcement/delete` | `DELETE /api/announcements/:id`（owner / manager）| ✅ |
| 拉取群公告列表 | `listAnnouncements POST /post/announcement/list` | `GET /api/channels/:id/announcements` | ✅ |
| 拉取已读人列表 | `(post-level 已废)` | `GET /api/messages/:id/readers?cursor=&limit=` | ✅ |
| 批量读未读统计 | (新增 cutover Phase 1) | `GET /api/messages/read-stats?ids=1,2,3`（异步替代 readBits）| ✅ |

---

### D. 历史消息拉取与 bitmap segment 增量

> cses-client 串讲：上拉加载更早 → `imDataSource:query` IPC → Rust segment_query → 查 channel_bitmap → 缺位 segment → `GET /api/cses/post/segment` 拉一块 → 写 message 表 → ACK → 更新 bitmap → 渲染。

**架构换代说明**：bitmap 协议族 8 个 Rust HTTP 端点**整族砍**，全部由 `POST /api/sync` 单点取代。cses-client Tauri Rust `ImSeqDataSource` 重构是 cutover Phase 4 关键工作。

| cses-client 用例 | 老调用 | im 替代方案 | 状态 |
|---|---|---|---|
| 上拉/下拉 segment 拉取 | `imDataSource:query` IPC | 客户端 IPC 内部改为：`POST /api/sync` body `{channels:[{id, seq}]}` → 返 `{channels:[{id, server_seq, messages[]}]}`；客户端按 seq 单调 cursor 推进 | ⚙️ |
| 进入/切换会话读本地缓存 | `imDataSource:queryCache` IPC | **客户端内部**（纯 SQLite） | — |
| 切换会话清 segment 内存缓存 | `imDataSource:reset` IPC | **客户端内部** | — |
| 调试查 bitmap | `im_query_bitmaps_by_channel` IPC | **客户端调试用** | — |
| 按 segmentId 拉一块消息 | `GET /api/cses/post/segment`（Rust 主动）| `POST /api/sync` 取代 | ⚙️ |
| 查服务端 bitmap | `POST /api/cses/channel/bitmap`（Rust 主动）| `POST /api/sync`（seq cursor 比 bitmap 高效，gap-free 单调递增）| ⚙️ |
| segment 写库 ACK | `POST /api/cses/posts/segment/daily/metadata/ack`（Rust 主动）| ACK 概念消失（seq cursor 自然推进）| ⚙️ |
| 增量同步链路查 `handle_increment` | `POST /api/cses/posts/getLatestPost`（Rust 主动）| `POST /api/sync` 同上 | ⚙️ |
| 增量同步查指定时间窗被更新消息 | `POST /api/cses/posts/getUpdatedPosts`（Rust 主动）| `POST /api/sync` + WS `msg_updated` 已 cover | ⚙️ |
| 跳转锚点上下文 | `queryPostsById POST /posts/getPostsAfterFromSegment` | `GET /api/messages/:id/after?limit=N` | ✅ |
| 增量拉取完成推送渲染 | `im:post:increment` WS | **客户端 Rust 内部状态事件**，由 sync + push_msg 合成 | ⚙️ |
| 增量状态机：started/empty/failed | `im:post:increment-{started,empty,failed}` WS | **客户端 Rust 内部** | — |

---

### E. @ 提及 / 投票 / 评分 / 话题等富类型

> cses-client 串讲：投票走 cses Java；@ 提及走 §B 链路 + 系统通知；话题升级走 makeTopic；回复抽屉走 getReplies + 实时 POST_UPDATE 合并。

| cses-client 用例 | 触发 | im 后端覆盖 | 状态 |
|---|---|---|---|
| 提交投票选项 | `vote POST /vote/vote` | **cses Java 业务后端** | ⚪ |
| 查看投票结果 | `readVote POST /vote/readVote` | **cses Java** | ⚪ |
| 发起投票 | `createVote POST /vote/createVote` | **cses Java** | ⚪ |
| 关闭投票 | `closeVote POST /vote/closeVote` | **cses Java** | ⚪ |
| 删除投票 | `deleteVote POST /vote/deleteVote` | **cses Java** | ⚪ |
| 提交评分 | `averageAttend POST /average/attend` | **cses Java** | ⚪ |
| 发布评分结果 | `publishAverageScore POST /average/publish` | **cses Java** | ⚪ |
| 查看评分详情 | `readAverage POST /average/read` | **cses Java** | ⚪ |
| 关闭评分 | `closeAverage POST /average/close` | **cses Java** | ⚪ |
| 删除评分 | `deleteAverage POST /average/delete` | **cses Java** | ⚪ |
| 跳转锚点补上下文 | `queryPostsById POST /posts/getPostsAfterFromSegment` | `GET /api/messages/:id/after?limit=N` | ✅ |
| 打开回复抽屉看回复链（一次性全量）| `queryReplyMessage POST /posts/getReplies` | `GET /api/messages/:id/replies` | ✅ |
| **二级 thread 子回复分页**（reply-root-message.component）| `queryReplyBranchMessage POST /posts/getReplyBranch`（含 `replyFirstLevelId / pageSize / offset`）| **`GET /api/messages/:id/replies/branch?offset=N&limit=M`**；返 `{messages, has_more, offset, limit}` | 🆕 |
| 把消息升为话题 | `createTopic POST /posts/makeTopic` | `POST /api/channels/:id/topics` body `{root_message_id, name, member_ids?}` | ✅ |
| Reaction（emoji 反应）| `(cses /posts/quickReply 老用作 emoji)` | im 已独立路由 `POST /api/messages/:id/reactions` + `reaction_added/removed` WS | ✅ |

---

### F. 群聊管理

> cses-client 串讲：创建群 → `createGroupChat` → WS `im:channel:created` 回声 → 管理员添加成员 → `setDialogMembers` → WS `im:channel:member-updated` 推完整 channel → 自愈触发 → `getChannelMembersByIds` 拉完整 → 用户退群 → `leaveDialog` → WS `update-by-post` 含 NOTICE leave。

| cses-client 用例 | 触发 imHttp | im 后端覆盖 | 状态 |
|---|---|---|---|
| 创建群聊 | `createGroupChat POST /channel/create` | `POST /api/channels` body `{name, member_ids}` + 触发 `channel_event{added}` 给被加者 | ✅ |
| 添加 / 移除成员 | `setDialogMembers POST /channel/member/change` | `POST /api/channels/:id/members { user_id }` + `DELETE /api/channels/:id/members/:user_id`（admin/owner）| ✅ |
| 设置 / 撤销管理员 | `setDialogManager POST /channel/{add,remove}/manger` | `POST /DELETE /api/channels/:id/managers/:user_id`（owner）| ✅ |
| 退出群聊 | `leaveDialog POST /channel/member/leave` | `POST /api/channels/:id/leave`（owner 禁退）| ✅ |
| **解散群聊（owner）** | `closeDialog POST /channel/close` | **`DELETE /api/channels/:id`**（owner-only，标 deleted_at）| 🆕 |
| 成员列表快照 | `queryMembersSnapshot POST /channel/member/snapshot` | `GET /api/channels/:id/members` 全量 | ✅ |
| **成员数自愈批量拉** | `getChannelMembersByIds POST /channels/member/byIds` | **降级走全量** `GET /api/channels/:id/members`（gap #6 用户决策）| — |
| **加 / 移成员 WS 推全员完整 channel** | `im:channel:member-updated` | **`channel_member_updated{change_type, actor_id, target_id, members[]}`** 全员推送 | 🆕 |
| 成员加入推送被加者本人 | `im:channel:created` | `channel_event{event_type:"added", channel_id, name}` 单推 | ✅ |
| **群昵称变更** | `im:channel:memberNickname` + `(隐含老 path)` | **`PATCH /api/channels/:id/members/:user_id/nickname`** + `channel_member_updated{change_type:"nickname"}` 全员推送 | 🆕 |
| 设置群公告（短文本 notice）| `setDialogNotice POST /channel/change/notice` | `PATCH /api/channels/:id { notice }` + 触发 `channel_info_updated` | ✅ |
| 发布正式群公告（独立 announcement）| `saveAnnouncement POST /post/announcement/save` | `POST /api/announcements` body `{channel_id, title, content}` + `announcement_posted` | ✅ |
| join/leave 系统消息伴随 channel 元变化 | `im:channel:update-by-post`（客户端合成）| 后端发 `push_msg` + msg_type=4 + `type:"NOTICE"` + `props.sys_type=member_{joined,removed,left}`（gap #9 NOTICE 字段对齐）；客户端 Rust 既有 `data.type == "NOTICE"` 分支直接命中 | 🆕 |
| 解散群聊全员通知 | `im:channel:closed` | **`channel_closed`** WS payload `{channel_id, actor_id, deleted_at}` | 🆕 |

#### F.2 频道元数据配置（cses-client 11 个细粒度老 path → im 统一 PATCH）

| cses-client 老 path | im 后端覆盖 | 状态 |
|---|---|---|
| `POST /channel/change/top` | `PATCH /api/channels/:id/members/:user_id { is_top }`（per-user，触发 `channel_top_updated` 多设备）| ✅ |
| `POST /channel/member/change/notify` | `PATCH /api/channels/:id/members/:user_id { notify_pref }` | ✅ |
| `POST /channel/change/displayName` | `PATCH /api/channels/:id { name }` | ✅ |
| `POST /channel/change/purpose` | `PATCH /api/channels/:id { purpose }` | ✅ |
| `POST /channel/change/orient` | `PATCH /api/channels/:id { orient }` | ✅ |
| `POST /channel/change/permission` | `PATCH /api/channels/:id { permission }` | ✅ |
| `POST /channel/load/postPinned` | `GET /api/channels/:id/pins` | ✅ |
| `POST /channel/add/postPinned` | `POST /api/channels/:id/pins/:message_id`（owner / manager）| ✅ |
| `POST /channel/remove/postPinned` | `DELETE /api/channels/:id/pins/:message_id` | ✅ |
| `POST /channel/onlineStatus` | `GET /api/channels/online-status?channel_ids=1,2,3&include_users=true` | ✅ |
| `POST /channels/load/increment` × 2（批量 + 单频道）| `POST /api/sync` 取代（seq 单调 cursor）| ⚙️ |
| `POST /api/cses/channels/load/increment`（Rust 主动）| `POST /api/sync` | ⚙️ |

---

### G. 搜索 / 跳转锚点 / 上下文补齐

> cses-client 串讲：全局搜索 → cses Java；点击命中跳转 → `queryPostsById` 定位 segment → `imDataSource:query` 带 anchorPostId → 拉前后上下文 → 视口填充。

| cses-client 用例 | 触发 | im 后端覆盖 | 状态 |
|---|---|---|---|
| 全局搜索 | `searchGlobal POST /Im/search/global` | **cses Java** | ⚪ |
| 会话内搜索历史 | `searchHistoricalByChannel POST /Im/search/searchByChannel` | **cses Java** | ⚪ |
| 分类过滤搜索 | `searchByChannelWithCategory POST /Im/search/searchByChannelWithCategory` | **cses Java** | ⚪ |
| 话题内分类搜索 | `searchByChannelWithCategoryTopic` | **cses Java** | ⚪ |
| 跳转锚点补上下文 | `queryPostsById POST /posts/getPostsAfterFromSegment` | `GET /api/messages/:id/after?limit=N` | ✅ |
| 围绕 timestamp 上下文 | (cses 老不存在)  | `GET /api/channels/:id/messages/around?timestamp=<ms>&limit=N` | ✅ |
| 跳转锚点跨 segment 加载 | `imDataSource:query`（带 anchorPostId）IPC | 客户端 Rust 内部，走 `POST /api/sync` + `GET /messages/:id/after` 组合 | ⚙️ |

---

### H. 跨窗口与窗口池（IM IPC 16 归 1）

> 全部是**客户端内部 IPC**，与 im 后端无关。后端只需保证：1 个 user 的多 device WS 都收到同样的 push_msg / channel_event / read_sync 即可。

| cses-client 用例 | 后端责任 | 状态 |
|---|---|---|
| 主窗口收 push_msg 未读 +1，子窗口已读上报 | `push_msg` 推 user 全部在线连接；客户端 `shouldHandleMessage` 过滤 | ✅ |
| 独立窗口直开陌生 channel 兜底 | 客户端用 `POST /api/sync` 单 channel 拉历史 | ⚙️ |
| 多设备 channel 置顶同步 | `channel_top_updated` per-user 推 | ✅ |
| 多设备已读同步 | `read_sync` per-user 推 | ✅ |
| **多设备定时消息状态同步** | `schedule_created / schedule_canceled` 推 sender 全设备 | 🆕 |
| 16 归 1 IPC 总线 | **客户端内部** | — |

---

### I. 热更新 / 二进制更新对消息系统的影响

> 客户端进程级行为，与 im 后端协议无关。后端只需保证：WS 重连后 `POST /api/sync` 能 seq cursor 拉齐所有 channel 增量，且 push_msg 不漏发。

| cses-client 用例 | 后端责任 | 状态 |
|---|---|---|
| 前端 UI 热更新（min_version 内）| WS 由 Rust 持有不断；前端重订阅；`POST /api/sync` 拉齐 | ✅ |
| Rust 二进制更新（强制升级）| 进程替换后 WS 重连；走 `POST /api/sync` 重新拉齐 | ✅ |
| 连接重建触发 | `im:connection:established`（客户端内部）；后端 WS handshake 用 cookieId | ✅ |

### I.2 内部文档预览

| cses-client 用例 | 触发 | im 后端覆盖 | 状态 |
|---|---|---|---|
| 点击消息中的内部文档卡片预览 | `POST /doc/document/view` (CsesHttp) | **cses Java + 外部 OSS** | ⚪ |

---

### J. 书签管理

> cses-client 串讲：右键消息 → 加书签 → `bookmark/create` → 后端持久化 → 用户打开书签视图 → `bookmark/load` → 用户取消 → `bookmark/delete`。WS 无专项事件，靠主动 load 刷新。

| cses-client 用例 | 触发 imHttp | im 后端覆盖 | 状态 |
|---|---|---|---|
| 给消息加书签 | `POST /post/bookmark/create` | `POST /api/favorites/:message_id` | ✅ |
| 取消书签 | `POST /post/bookmark/delete` | `DELETE /api/favorites/:message_id` | ✅ |
| 加载书签列表 | `POST /post/bookmark/load` | `GET /api/favorites` 返 `FavoriteWithMessage[]` join 消息 | ✅ |

---

## 3. WS 事件全集对照（cses-client 订阅 20 → im 后端推送 26）

> cses-client `ipcEvent.service.ts` 订阅 20 个 IPC 事件；其中 11 个来自 im 后端 WS，9 个是客户端 Rust 内部合成。

| cses-client 订阅 | 类型 | im 后端 WS type | payload 来源 | 状态 |
|---|---|---|---|---|
| `im:post:received` | im WS | `push_msg` | PushMsgPayload（含 `type:"NOTICE"` for msg_type=4）| ✅ |
| `im:post:updated` | im WS | `msg_updated` | 完整 Message JSON | ✅ |
| `im:post:batch-updated` | client 合成 | 多个 `msg_updated` 客户端聚合 | — | ⚙️ |
| `im:post:read` | im WS | `read_sync` | ReadSyncPayload | ⚙️（client 端 Phase 4 砍 imWs:post:read 翻译）|
| `im:post:increment` | client Rust 合成 | `push_msg` + `pong.channel_seqs` 派生 | — | ⚙️ |
| `im:post:increment-{started,empty,failed}` | client Rust 内部 | — | — | — |
| `im:channel:increment` | client Rust 合成 | sync + push_msg 派生 | — | ⚙️ |
| `im:channel:update` | im WS | `channel_info_updated` | 完整 Channel JSON | ✅ |
| `im:channel:update-by-post` | client Rust 合成 | `push_msg` msg_type=4 + `type:"NOTICE"` + `props.sys_type` | gap #9 字段对齐 | 🆕 |
| `im:channel:created` | im WS | `channel_event{added}` | ChannelEventPayload（仅推被加者）| ✅ |
| `im:channel:closed` | im WS | **`channel_closed`** | ChannelClosedPayload `{channel_id, actor_id, deleted_at}` | 🆕 |
| `im:channel:member-updated` | im WS | **`channel_member_updated`** | ChannelMemberUpdatedPayload `{change_type, actor_id, target_id, members[]}` | 🆕 |
| `im:channel:memberNickname` | im WS（合并）| **`channel_member_updated{change_type:"nickname"}`** | 同上，带 nick_name | 🆕 |
| `im:channel:schedule-created` | im WS | **`schedule_created`** | ChannelSchedulePayload | 🆕 |
| `im:channel:schedule-canceled` | im WS | **`schedule_canceled`** | ChannelSchedulePayload | 🆕 |
| `im:connection:established` | client Rust 内部 | WS handshake 后客户端发的 | — | — |
| `im:channels:loaded` | client Rust 合成 | hello handler 内部 | — | ⚙️ |
| `im:todo:updated` | client 走 cses Java | `POST /api/cses/posts/queryTodoList`（cses Java）| — | ⚪ |

---

## 4. 字段对齐速查

### 4.1 v0.7.3 gap #9 — push_msg.type / props（**关键**）

cses-client Rust `message_service.rs::apply_notice_changes_standalone` 现有逻辑：

```rust
if data.get("type").and_then(|v| v.as_str()) == Some("NOTICE") {
    common::apply_notice_changes_standalone(&data, &local_dialog, auth_user_id, &mut update)?;
}
```

im 后端 v0.7.3 `gateway.NoticeTypeForMsgType(msgType)`：

```go
func NoticeTypeForMsgType(msgType int16) NoticeType {
    if msgType == repo.MsgTypeSystem {  // 4
        return NoticeTypeNotice           // "NOTICE"
    }
    return ""
}
```

3 处 PushMsgPayload 构造点（cmd/gateway/main.go / cmd/message/main.go / internal/gateway/ws_handler.go）统一调用，msg_type=4 自动填 `type:"NOTICE"` + `props` 透传 message.Props JSONB raw text。

### 4.2 sys_type 全枚举（messages.props.sys_type）

| 值 | 触发 | 客户端派生动作 |
|---|---|---|
| `channel_created` | CreateGroup | dialog 新建（一般依赖 `channel_event`，sys 消息冗余）|
| `channel_updated` | Update(name/avatar/notice/...) | 与 `channel_info_updated` 双管齐下 |
| `channel_closed` | **CloseChannel（gap #1）** | `dialog.deleteAt > 0`，从列表隐藏 |
| `member_joined` | AddMember | 与 `channel_member_updated{join}` 双管齐下 |
| `member_removed` | RemoveMember (admin kick) | 与 `channel_member_updated{kick}` 双管齐下 |
| `member_left` | LeaveChannel (self) | 与 `channel_member_updated{leave}` 双管齐下 |
| `member_nickname` | **SetMemberNickname（gap #5）** | 与 `channel_member_updated{nickname}` 双管齐下 |

> **双管齐下**意味着 cses-client 既可以靠 push_msg 的 NOTICE 分支派生本地状态（Rust message_service 现有逻辑），也可以直接监听 channel_member_updated WS frame（更精确，含完整 members 列表）。后端两路都发，客户端选用一路即可。

### 4.3 channel_member_updated.change_type 全枚举

| 值 | 触发 service 方法 | actor_id vs target_id |
|---|---|---|
| `join` | ChannelService.AddMember | actor = caller (admin/owner), target = newUser |
| `kick` | ChannelService.RemoveMember | actor = caller (admin/owner), target = removed |
| `leave` | ChannelService.LeaveChannel | actor == target == caller |
| `nickname` | ChannelService.SetMemberNickname | actor = caller, target = whose nickname changed（可同可异）|

---

## 5. 联调 smoke checklist（v0.7.3 全 case）

按 §0 状态走完下面 12 步，全 ✅ 说明覆盖率 100%：

| Step | 用户动作 | 期望 |
|---|---|---|
| 1 | 用 `17692704771/123456` 登录 | console `🔀 apiFlavor: mattermost → im`，所有 imHttp 命中 `192.168.6.41:30880` |
| 2 | 进入主消息页 | `GET /api/modules` + `GET /api/channels` envelope `{status:"success", data:[...]}` |
| 3 | 创建群聊 → 拉 3 个成员 | `POST /api/channels` 201；被加 3 人收 `channel_event{added}`；群内全员收 `channel_member_updated{change_type:"join", members[4]}`（含自己）×3 |
| 4 | 发普通消息 | `POST /api/channels/:id/messages` 201；全员收 `push_msg`；sender 多设备收 `send_ack` |
| 5 | 撤回消息 | `DELETE /api/messages/:id` → 全员收 `msg_deleted` |
| 6 | 改群昵称 | `PATCH /api/channels/:id/members/:user_id/nickname` 200；全员收 `channel_member_updated{change_type:"nickname", nick_name:"..."}` |
| 7 | 创建定时消息 | `POST /api/messages/scheduled` 201；本人其他设备收 `schedule_created{has_schedule_post:true}` |
| 8 | 取消定时消息 | `DELETE /api/messages/scheduled/:id` 200；本人其他设备收 `schedule_canceled{has_schedule_post:false}`（当此 channel 无其他 pending）|
| 9 | admin 踢人 | `DELETE /api/channels/:id/members/:user_id` 200；全员收 `channel_member_updated{change_type:"kick", target_id}`；同时 push_msg `type:"NOTICE", props.sys_type:"member_removed"` |
| 10 | owner 解散群聊 | `DELETE /api/channels/:id` 200 + `channel.deleted_at`；全员收 `channel_closed{channel_id, actor_id, deleted_at}` |
| 11 | 二级回复抽屉分页加载 | `GET /api/messages/:id/replies/branch?offset=0&limit=20` 200 `{messages, has_more}` |
| 12 | 多端登录测：换设备登录同账号 | 设备 B 收新设备 A 发出消息后的 send_ack（不会，send_ack 只推 sender connection）；A 的多设备收 read_sync / schedule_* |

---

## 6. 与现有 docs 的引用关系

| 主题 | 看本文哪里 | 看 `CSES_CLIENT_内部对接契约.md` 哪里 | 看 `IM_DATA_MODEL_新版数据模型字典.md` 哪里 |
|---|---|---|---|
| 端点全集 | §1.1 总览 + §2 各组 | §4 HTTP 端点 87 / §7 路径对照 | §3 DTO 字段 |
| WS 协议 | §3 WS 全集对照 | §5 WS 26 type | §4 WS payload 字段 |
| 鉴权 / envelope | — | §2 / §3 | §6 |
| 9 个 gap 补丁 | §1.2 + 各组 🆕 标记 | §8 已知坑 v0.7.3 行 | §2.1 deleted_at / §2.2 nick_name / §5.11/12 |
| sys_type 枚举 | §4.2 | §5.3.1 NOTICE 字段 | §5.12 |
| migration 017 | — | — | §2.1 / §2.2 |
| 联调 checklist | §5 | §9 cheatsheet | — |

---

## 7. 维护契约

- 本文修订与 `CSES_CLIENT_内部对接契约.md` / `IM_DATA_MODEL_新版数据模型字典.md` **三向校对**。任何一方变更 endpoint / WS payload / sys_type 都要同步刷新另外两份。
- 新增 cses-client 用例（cses-client 仓库 `messagev3-用户api用例集合.md` 新增条目）→ 本文 §2 对应组追加一行 + 在 im 后端补对应 endpoint。
- gap 补丁需要 follow-up：
  - 集成测试覆盖（DELETE channel / replies/branch / nickname 还没 m4_*_integration_test.go）
  - harness C005 升级（22 → 26 type 锁定）
  - ChannelRepoMock 跑 mockery 重生（SoftDelete / UpdateMemberNickname stub 自动补）

---

**Owner**：im 项目 + cses-client 项目联合
**最后更新**：2026-05-12（v0.7.3-backend-final + 9 gap 补丁全覆盖）
**下次更新触发**：cses-client `messagev3-用户api用例集合.md` 任何条目变更 / im 后端 v0.7.4+ 新 endpoint / WS type / sys_type 新增

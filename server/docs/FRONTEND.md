# 前端替换路线 — cses-client / message-v3

> 目标：把前端对 Mattermost `/api/cses/*` 的依赖，切换到 im/server（Telegram 风格）的 REST + 精简 WS。
> 范围：`/Users/mac28/workspace/angular/cses-client`（Angular + Tauri Rust），主代码在 `src/pages/message-v3`。

---

## 一、现状（阻碍替换的三堵墙）

### 墙 1：MessageHttpService 硬编码 Mattermost 语义
`src/pages/message-v3/service/messageHttp.service.ts` 当前 54+ 调用全部指向 Mattermost：
- `baseUrl = http://${auth.mattermostHttp}/api/cses`
- 端点形态是 RPC：`/posts/create`、`/channel/change/displayName`、`/post/read` …
- 响应默认走 `{status, data, message}` 包装：`this.isWrappedResponse()` 才 unwrap

### 墙 2：Rust Tauri 侧 IMDataSource 深度绑定 bitmap/segment
`src-tauri/src/websocket/im_handlers.rs` + `src-tauri/src/data_center/`：
- `handle_channel_increment` 解码 `postMapDaily` → 写 `bitmap` 表
- `IMDataSource::query` 管线 = local → bitmap → `POST /post/segment` → pending WS `post_segment`
- `ChannelBitmapIndex` 内存 + SurrealDB 持久化 + empty_segments 缓存

### 墙 3：ipcEvent.service.ts 订阅 17 个 Mattermost 事件
`imWs:post:received/updated/batchUpdated/read/increment`、`imWs:channel:increment/update/memberUpdated/...`、`imWs:todo:updated` 等。这些是 Rust 侧对 Mattermost WebSocket 事件（`posted`、`increment_channel`、`post_segment` 等）的二次包装。

---

## 二、分层替换策略

```
┌─────────────────────────── Angular UI ───────────────────────────┐
│       组件层（消息列表/频道列表/输入框） — 不改                         │
└─────────────────────────┬─────────────────────────────────────────┘
                           │
┌──────────────────────── Service 层 ────────────────────────┐
│ MessageHttpService → 加适配层 ImApiAdapter                  │   ① 墙 1 拆
│ ipcEvent.service.ts → 订阅新事件命名                         │   ③ 墙 3 拆
│ chatContentShared.service.ts → 新分页用 seq 代替 segmentId  │
└──────────────────────┬──────────────────────────────────────┘
                       │ Tauri invoke
┌─────────────────── Rust Tauri 层 ──────────────────────┐
│ IMDataSource → 新实现 ImSeqDataSource                  │   ② 墙 2 拆
│ ws handler → 订阅 push_msg / pong.channel_seqs         │
│ bitmap/segment → 存量保留读路径，写路径停用              │
└────────────────────────────────────────────────────────┘
                       │ HTTP + WS
                       ▼
                    im/server
```

---

## 三、端点映射表（逐条替换）

### 3.1 消息

| 旧 (Mattermost) | 新 (im) | 改动点 |
|----------------|--------|-------|
| `POST /posts/create` | `POST /api/channels/:id/messages` | channel_id 放路径；body 去掉冗余字段 |
| `POST /posts/get` | `GET /api/channels/:id/messages?around_seq=X` | GET + query string |
| `POST /posts/getPostsAfterIndex` | `GET /api/channels/:id/messages?after_seq=X&limit=50` | |
| `POST /posts/getPostsAfterFromSegment` | **作废**。segmentId 概念消失 | 改用 `GET /channels/:id/messages?around_seq=` 或 `GET /channels/:id/messages/around?timestamp=<ms>`（M1 交付） |
| `POST /posts/getLatestPost` | `GET /api/channels/:id/messages?limit=1` | |
| `POST /posts/getUpdatedPosts` | 由 WS `push_msg` 实时推送覆盖 | 不再主动轮询 |
| `POST /post/read` | `POST /api/channels/:id/read` | 语义从「标记单条」变「推进整体游标」 |
| `POST /post/read/list` | `GET /api/messages/:id/readers` (M1 待实现) | 阻塞：等后端 |
| `POST /posts/revoke` | `DELETE /api/messages/:id` (M1 待实现) | 阻塞：等后端 |
| `POST /posts/getReplies`、`/getReplyBranch` | `GET /api/messages/:id/replies` (M1 待实现) | 阻塞：等后端 |
| `POST /posts/createSchedule`/`cancelSchedule`/`getSchedule` | `/api/messages/scheduled/*` (M2 待实现) | 阻塞 |
| `POST /posts/urgentPost`/`urgentConfirm`/`urgentCancel` | `/api/messages/urgent/*` (M2 待实现) | 阻塞 |
| `POST /posts/quickReply` | `POST /api/messages/:id/quick-reply` (M2 待实现) | 阻塞 |
| `POST /posts/makeTopic` | （评估）合并入 `POST /api/messages` 带 topic 字段 | 设计变更 |
| `POST /posts/queryTodoList` | 客户端本地计算 (从 mentionList + urgentPostList) | 下沉到客户端 |

### 3.2 频道

| 旧 | 新 | 改动 |
|----|----|------|
| `POST /channels/load/increment` | `POST /api/sync` | **范式重构**：不再是 increment_channel 推 bitmap，而是客户端主动 POST cursors |
| `POST /channels/view` | `POST /api/channels/:id/read` | 合并到「已读」 |
| `POST /channel/create` / `/createSpecifyOwner` | `POST /api/channels` | owner 字段 body 传 |
| `POST /channel/close` | `DELETE /api/channels/:id` | |
| `POST /channel/change/*` (info/notice/purpose/top/picture/props/orient/displayName/permission) 共 9 个 | `PATCH /api/channels/:id` (M2 待实现) | 阻塞；需要 JSON patch |
| `POST /channel/add/manger`、`/remove/manger` | `POST/DELETE /api/channels/:id/managers/:user_id` (M2 待实现) | 阻塞 |
| `POST /channel/add/postPinned`、`/remove/postPinned` | `POST/DELETE /api/channels/:id/pins/:message_id` (M2 待实现) | 阻塞 |
| `POST /channel/load/(notice/postPinned/admin)` | 合并到 `GET /api/channels/:id` 响应字段 (M2) | 阻塞 |
| `POST /channel/query` | `GET /api/channels?...` | query string 过滤 |
| `POST /channel/onlineStatus` | `GET /api/channels/:id/online` (M2 待评估) | |
| `POST /channel/member/snapshot` | `GET /api/channels/:id/members` | |
| `POST /channel/member/change` | `POST /api/channels/:id/members` + `DELETE .../:user_id` | 拆成两个接口 |
| `POST /channel/member/change/(role/notify)` | `PATCH /api/channels/:id/members/:user_id` (M2 待实现) | 阻塞 |
| `POST /channel/member/leave` | `POST /api/channels/:id/leave` | ✅ 已就绪 |
| `POST /channel/bitmap` | **作废** | 改为 `GET /api/channels/:id/messages?around_seq=` 按需拉 |

### 3.3 收藏 / 书签

| 旧 | 新 |
|----|----|
| `POST /post/bookmark/create` | `POST /api/favorites/:message_id` |
| `POST /post/bookmark/delete` | `DELETE /api/favorites/:message_id` |
| `POST /post/bookmark/load` | `GET /api/favorites` |

### 3.4 搜索（🚫 不迁移，走 Java 远程）

搜索端点**保持现有 Java 调用路径**，不纳入 F2 / F3 替换范围：

| 端点 | 处理方式 |
|------|---------|
| `POST /Im/search/global`、`/searchByChannel`、`/searchByChannelWithCategory`、`/searchByChannelWithCategoryTopic` | **保留**。调用走 Java 搜索服务；前端 `MessageHttpService` 里这组方法**不改 baseUrl**、不切 im |
| `POST /search/(post/user/channel/do)` | 同上 |

> 对应后端 `GET /api/search` / `GET /api/users/search` 仅内部使用，**前端不再对它们发起调用**。

### 3.5 用户 / 好友 / 设置

| 旧 | 新 |
|----|----|
| Mattermost 原生 `/users/me` | `GET /api/me` / `PUT /api/users/me` |
| Mattermost 原生 `/users/search` | `GET /api/users/search` |
| Mattermost 无好友 | `/api/friends/*` 7 个端点（新增能力） |
| Mattermost 原生 settings | `GET/PUT /api/settings` |

### 3.6 企业协作（阻塞 M2 / M3 / M4）

| 模块 | 端点数 | 新后端计划 | 迁移阶段 |
|------|-------|----------|---------|
| 公告 `/post/announcement/*` | 6 | `/api/announcements/*` | M2 / F3 |
| 通知 `/notification/*` | 2 | `/api/notifications` | M2 / F3 |
| 审批 `/post/approval` | 1 | `/api/approvals` | M2 / F3 |
| 紧急消息 `/posts/urgentPost`/`urgentConfirm`/`urgentCancel` | 3 | `/api/messages/urgent/*` | M2 / F3 |
| 定时消息 `/posts/createSchedule`/`cancelSchedule`/`getSchedule` | 3 | `/api/messages/scheduled/*` | M2 / F3 |
| 快捷回复 `/posts/quickReply` | 1 | `/api/messages/:id/quick-reply` | M2 / F3 |
| 模板 `/post/templateReceived` | 1 | `/api/messages/:id/template-received` | M3 |
| 组织 `/groups`、`/modules/getAll`、`/teams/*` | 3 | `/api/modules`、`/api/groups`、`/api/teams/*` | M3 |
| AI Agent `/agents/*` | 6 | `/api/agents/*` | M4 |
| Bot 管理 `/bot-manage/*` | 9 | `/api/bots/*` | M4 |
| Webhook `/webhook/*` | 3 | `/api/webhooks/*` | M4 |

### 3.7 外部服务直连（前端保持现状，不经 im 网关）

| 模块 | 端点 | 服务提供方 | 前端处理 |
|------|------|----------|---------|
| **投票 `/vote/*`** (createVote / vote / readVote / closeVote / deleteVote) | 5 | **Java 远程服务** | 保留现有调用路径，只把鉴权头（cookieId / trace）继续透传；不在 F3 里改造 |
| **搜索 `/Im/search/*`、`/search/*`** | 8+ | **Java 搜索服务** | 保留现有调用路径；不切 im；`MessageHttpService` 中搜索相关方法保持旧 baseUrl |
| **文件上传分片 / 断点续传** | — | 独立对象存储服务 | 前端沿用现有文件上传组件，不受 im 切换影响 |

> **⚠️ 这三块不在 im/server 替换范围内**。前端迁移时跳过，`MessageHttpService` 对应方法保留、不改 baseUrl。

---

## 四、WS 协议映射

### 4.1 事件重命名（严格对齐 BACKEND §1.1 WSMessageType 版本锁定）

**V1（M1 交付，12 种）** — Rust 端 + 前端必须按此 12 种实现：

| Mattermost 事件 | im WSMessageType（V1） | Tauri IPC 事件 | 客户端处理 |
|----------------|-----------------------|---------------|----------|
| `posted` | `push_msg` | `imWs:msg:pushed` | 新消息入列 |
| `post_edited` | **`msg_updated`**（M1） | `imWs:msg:updated` | 覆盖本地 |
| `post_deleted` | **`msg_deleted`**（M1） | `imWs:msg:deleted` | 标记删除 |
| `channel_viewed`、`multiple_channels_viewed` | `read_sync` | `imWs:msg:read-sync` | 同用户其他设备已读同步 |
| `user_added`、`user_removed`（频道） | `channel_event` (eventType=added/removed) | `imWs:channel:member-changed` | 同步本地频道成员 |
| 好友请求/接受/拒绝 | `friend_event` | `imWs:friend:event` | 更新好友列表 |
| `increment_channel` | **不再存在**。改由 `pong.channel_seqs` 触发 | `imWs:sync:triggered` | 心跳看到 delta → 主动 POST /api/sync |
| `post_segment`、`post_lazy`、`post_map_config` | **作废**（segment 体系整条裁掉） | — | 删除对应 Rust 处理 |
| Ping/Pong | `ping` / `pong` | — | 心跳 |
| 消息发送快车道 | `send` / `send_ack` | — | HTTP 外可选 |
| 推送幂等 ACK | `push_ack` | — | 客户端 ACK push_id |
| WS 内嵌同步 | `sync_resp` | `imWs:sync:applied` | 可选快车道 |

**V2（M5 以后视需求再加，3 种候选，不在 M1-M6 范围）：**

| Mattermost 事件 | im WSMessageType（V2 候选） | 当前处理 |
|----------------|--------------------------|---------|
| `post_reaction_added/removed` | `reaction_updated` | M1-M6 走 HTTP + 轮询或 /sync 兜底 |
| `typing` | `typing` | 暂不实现（对主流程无影响） |
| `status_change` | `presence` | 暂不实现 |

> **约束：** Rust 侧常量、前端 `ipcEvent.service.ts` 订阅名、后端 Go 常量（`internal/gateway/types.go`）三处必须严格一致。命名违规 CI 直接拒绝合并。

### 4.2 Ping/Pong 增量

**关键约束（M1 硬性）：** 客户端心跳 **15 秒一次**，服务端 routing TTL **45 秒**；服务端在每次 ping handler 里续期 routing。丢失 3 次心跳（45s 无 ping）后服务端主动 Deregister，新消息不再路由到死 Pod。

```typescript
// 客户端 (新)
const HEARTBEAT_MS = 15000;  // 必须 = 服务端 TTL(45s) / 3，不要改

setInterval(() => {
  if (!ws.isOpen()) return;
  ws.send({
    type: 'ping',
    payload: {
      channel_seqs: this.allChannels().reduce((acc, c) => ({
        ...acc, [String(c.id)]: c.localSeq
      }), {})
    }
  });
}, HEARTBEAT_MS);

ws.on('pong', ({ payload: { channel_seqs, server_time } }) => {
  // channel_seqs 只含 server > client 的频道
  if (channel_seqs && Object.keys(channel_seqs).length) {
    this.triggerSync(Object.keys(channel_seqs).map(Number));
  }
  this.lastPongAt = Date.now();
});

// 断线检测：30 秒未收到 pong → 主动重连
setInterval(() => {
  if (Date.now() - this.lastPongAt > 30000 && ws.isOpen()) {
    ws.close();  // 触发重连逻辑
  }
}, 5000);
```

### 4.3 `triggerSync` 调用合约（对齐 BACKEND §3.3）

```typescript
async triggerSync(channelIds: number[]) {
  // 按后端 max_channels_per_call=500 分批
  for (const batch of chunks(channelIds, 500)) {
    const cursors = batch.map(id => ({
      id,
      seq: this.getLocalSeq(id)  // 0 = 新频道
    }));
    const resp = await this.imApi.sync({ channels: cursors });
    for (const delta of resp.channels) {
      this.saveMessages(delta.id, delta.messages);
      this.updateServerSeq(delta.id, delta.server_seq);
      this.updateUnread(delta.id, delta.unread);
      if (delta.has_more) {
        // 循环 GET /channels/:id/messages?after_seq= 到追平
        await this.fetchRemainingAfter(delta.id, delta.messages);
      }
    }
  }
}
```

---

## 五、Rust Tauri 侧改造

### 5.1 新增 `ImSeqDataSource`

替换 `data_center/data_source.rs` 的 `IMDataSource`：

```rust
// src-tauri/src/data_center/im_seq_data_source.rs (新文件)
pub struct ImSeqDataSource {
    local_db: Arc<SurrealDB>,
    http: Arc<ImHttpClient>,
}

impl ImSeqDataSource {
    // 替代 IMDataSource::query(QueryParams{day, index, ...})
    pub async fn query(&self, p: SeqQueryParams) -> Result<QueryResult> {
        // 1. local: SELECT * FROM messages WHERE channelId=? AND seq BETWEEN ? AND ?
        //                                     ORDER BY seq (direction)
        // 2. 不够? HTTP: GET /api/channels/:id/messages?before_seq=X&limit=50
        //                                  or after_seq / around_seq
        // 3. 入库 + 返回
    }

    // 替代 handle_channel_increment
    pub async fn on_pong(&self, channel_seqs: HashMap<i64, i64>) {
        // 调 /api/sync 一次性拿所有 delta
        let deltas = self.http.sync(self.cursors_for(channel_seqs)).await?;
        for d in deltas.channels {
            self.save_messages(d.messages);
            self.update_channel_seq(d.id, d.server_seq);
        }
    }
}
```

**保留旧 `IMDataSource` 一段时间**：通过 feature flag 切换，灰度期内 Rust 侧双写（新侧 SurrealDB messages 表 + 旧侧保留 bitmap 不读）。

### 5.2 bitmap 表处理

**停用写**：`handle_channel_increment` 不再解码 postMapDaily、不再写 `bitmap:{channelId}&{day}`
**保留读一个月**：供旧缓存消费；一个月后跑迁移脚本清表

### 5.3 ipc 事件命名

```typescript
// 旧
tauriService.on('imWs:post:increment', (_, event) => { ... })

// 新（语义保持，命名可复用）
tauriService.on('imWs:sync:applied', (_, event: { channels: SyncDelta[] }) => { ... })
tauriService.on('imWs:msg:pushed', (_, event: PushMsgPayload) => { ... })
tauriService.on('imWs:msg:read-sync', (_, event: ReadSyncPayload) => { ... })
```

---

## 六、迁移里程碑（严格对齐后端 M1 → M6）

### F0：准备（1 周，与后端 M1 同步启动）
- [ ] `MessageHttpService` 增加 `apiFlavor: 'mattermost' | 'im'` 开关，默认 mattermost
- [ ] 抽 `ImApiAdapter` 类：封装 im REST 所有端点（TS 生成自 backend OpenAPI，见 `/api` skill）
- [ ] Rust 侧新增 `ImHttpClient` 模块 + feature flag `im_seq_sync`
- [ ] 事件命名迁移清单（`ipcEvent.service.ts` 注释标注订阅者）

### F1：聊天核心切换（3 周，等后端 M1 交付）
**与后端 M1 对齐：跨 Pod 推送、撤回 / 编辑 / 线程 / 已读列表 / 按时间跳段同时就位**

- [ ] 发消息 / 拉消息 / 已读切 im REST
- [ ] 撤回 `DELETE /api/messages/:id` + WS `msg_deleted`
- [ ] 编辑 `PATCH /api/messages/:id` + WS `msg_updated`
- [ ] 线程回复 `GET /api/messages/:id/replies`
- [ ] 已读列表 `GET /api/messages/:id/readers`（替代 `/post/read/list`）
- [ ] **按时间跳段回看** `GET /api/channels/:id/messages/around?timestamp=<ms>`（替代 bitmap 跳段，Rust 侧 `ImSeqDataSource::query_around_timestamp`）
- [ ] WS 切 im（`push_msg`、`read_sync`、`channel_event`；`ping/pong` 载荷 `channel_seqs`）
- [ ] Rust 接入 `POST /api/sync`，停止解码 postMapDaily

### F2：频道管理切换（4 周，等后端 M2）
- [ ] 所有频道 CRUD + 成员管理 endpoints（`PATCH /channels/:id`、`/managers`、`/pins`）
- [ ] 成员角色 / 通知属性改动切 `PATCH /api/channels/:id/members/:user_id`
- [ ] 收藏切 `/api/favorites/:message_id`
- [ ] **搜索不切**（保持走 Java，见 §3.4 / §3.7）

### F3：企业协作切换（与 F2 并行末端，2-3 周）
- [ ] 公告 / 通知 / 审批 / 紧急 / 定时 / 快捷回复

### F4：组织 + Bot（跟后端 M3/M4 节奏）
- [ ] 组织结构、部门 / 模块列表
- [ ] Bot / Agent / Webhook UI 切 im

### F5：清理 & 下线（1 周，对齐后端 M6）
- [ ] 移除 `MessageHttpService` 中所有 `/api/cses/*` 调用（**保留 `/vote/*` 调 Java**）
- [ ] 移除 Rust `websocket/channel_processor.rs` 中的 bitmap 解码代码
- [ ] 删除 `bitmap` 表、`postSegmentConfig` local_storage
- [ ] 更新 `AuthData.mattermostHttp` → `imGatewayHttp`
- [ ] 文件上传调用路径保持不变（独立对象存储服务，不走 im）

---

## 七、发布策略（**不回切，全面切换**）

> **本次决策定调：** T5 不做百分比灰度；不保留 Mattermost 回切通道；一次性全量切换，问题走 hotfix。理由见 `OVERALL.md §三` 切流策略。

### 7.1 按模块分阶段切换
每个前端模块独立 feature flag：
```typescript
environment.flags = {
  messagingBackend: 'im' | 'legacy',       // F1 切完 = 'im'
  channelMgmtBackend: 'im' | 'legacy',     // F2 切完 = 'im'
  collabBackend: 'im' | 'legacy',          // F3 切完 = 'im'
  botBackend: 'im' | 'legacy',             // F4 切完 = 'im'
  // 永久 legacy（外部服务）：
  searchBackend: 'java',
  voteBackend: 'java',
  fileUploadBackend: 'external-storage',
}
```
每个 flag 随对应里程碑**单向切换到 im 且不再切回**。flag 保留到 F5 后期一次性删除死代码。

### 7.2 阶段内问题处理
- F1-F4 任一阶段内发现阻塞 bug → 不切下一阶段；在 im 侧热修，**不把 flag 切回 legacy**
- 原因：切回会让切入期的 im 侧已读/消息状态丢失，二次切出时反而产生新不一致

### 7.3 Rust 侧过渡期处理
- bitmap 相关代码**保留读路径 2 周**（供存量缓存消费），写路径 F1 切到 seq 后立即停用
- F5 统一删除 bitmap 解码 / ChannelBitmapIndex / postSegmentConfig / empty_segments 全部代码

### 7.4 客户端升级时的缓存清理
- 客户端检测到 `backend_flavor=im` 首次出现 → 清 SurrealDB `bitmap` 表 + `local_storage:postSegmentConfig`
- **保留** `messages` 表（供首次登录减轻 /sync 拉取量）
- 给用户显式提示："首次登录会重新同步频道列表，未读数可能偏大一次"

---

> **前端今晚验证** = `OVERALL.md §5.4` 的 V6 冒烟（5 条全绿：登录 / 频道列表 / 发消息 / WS 收自己消息 / 无 console 红错）。业务 KPI / e2e 套件等长尾放到 staging 和生产后。本节只列前端侧的具体风险。

## 八、风险清单

| 风险 | 缓解 |
|------|------|
| Rust 侧 `IMDataSource` → `ImSeqDataSource` 改动复杂 | F0 搭骨架；F1 期双实现并存（feature flag），内部 QA 完整跑一轮主流程再对外 |
| 前端 17 个 `imWs:` 事件订阅者分布广 | `ipcEvent.service.ts` 作为唯一派发中枢；F1 期先用 adapter 把新事件映射到旧订阅名；F5 统一删除旧订阅 |
| 已读语义从「postId 粒度」变「channel seq 粒度」 | UI 的「未读红点」「已读人头」重新接 `/api/messages/:id/readers`；首次登录 unread 偏大为预期行为 |
| bitmap 跳段查询能力消失 | 审计用例走 `GET /channels/:id/messages/around?timestamp=<ms>`（M1 后端交付） |
| 全量切换后 im 侧某企业协作模块 bug | hotfix 通道常备；F3/F4 模块上线前在 staging 做一轮全量回归 |
| 客户端 WS 心跳与服务端 TTL 不一致导致推送丢 | 硬编码 15s，注释写明「不要改，对齐 BACKEND §5.3.2」；CI 加测试校验 |

---

## 九、立即可做

### 今晚（等后端 DAY 0 #3 PR-A 合并后立即开工）

> **TS 类型同步方式（本次决策）**：**不从后端 copy 文件**。前端 owner 在 `cses-client` **开新分支**，按 `BACKEND §3.3` 的 Go struct + JSON tag **直接手写 TS 接口**。权威源永远是后端 Go struct；前端改 TS 前要先看后端文档 §3.3。

在新分支里开以下 PR：

1. **新建** `src/app/core/im-api/sync.types.ts`（手写 TS 接口，注释标明对齐 im/server `BACKEND.md §3.3`）
   ```typescript
   export interface SyncCursor      { id: number; seq: number; }
   export interface SyncRequest     { channels: SyncCursor[]; }
   export interface SyncChannelDelta {
     id: number; server_seq: number; unread: number;
     messages?: Message[]; has_more?: boolean;
   }
   export interface SyncResponse    { channels: SyncChannelDelta[]; }
   export const MAX_CHANNELS_PER_CALL = 500;
   ```
2. **新建** `src/app/core/im-api/im-api.adapter.ts`
   - `ImApiAdapter.sync(req: SyncRequest): Promise<SyncResponse>` 签名据 `sync.types.ts`
   - 方法桩：`sendMessage / fetchMessages / fetchAround / markRead / sync`（先 throw `'not implemented'`）
3. **改** `src/pages/message-v3/service/messageHttp.service.ts`
   - 加 `apiFlavor: 'mattermost' | 'im'` 状态（默认 `'mattermost'`，不影响现有行为）
   - 新 `ImApiAdapter` 实例注入；按 flag 路由每个方法的实现
4. **改** `src/pages/message-v3/service/ipcEvent.service.ts`
   - 在 17 个 `imWs:*` 订阅处加注释：列出当前订阅方 + 对应 V1 WSMessageType（见 §4.1 重命名表）
5. **Rust 侧** 新建 `src-tauri/src/http/im_client.rs`
   - 按后端 Go struct 手写 `SyncCursor / SyncRequest / SyncResponse` 等 Rust 结构（`#[derive(Serialize, Deserialize)]`）
   - `ImHttpClient::sync(cursors: Vec<SyncCursor>) -> Result<SyncResponse>`

### 本周（F0 + F1 启动）

- F0 完成 `ImApiAdapter` + feature flag；Rust `ImHttpClient` 跑通 `/api/sync`
- F1 准备：撤回 / 编辑 / 线程 / readers 前端调用点调研（标注在 message-v3 对应 component 的 TODO 注释里）
- 心跳频率/重连逻辑按 `FRONTEND.md §4.2` 硬编码 15s + 30s 断线检测

### 持续

- `ipcEvent.service.ts` 作为事件派发唯一中枢；新事件从这里入，旧事件保留 adapter 转发一段时间
- 每个阶段（F1-F5）一结束立即清理旧事件订阅，避免腐烂

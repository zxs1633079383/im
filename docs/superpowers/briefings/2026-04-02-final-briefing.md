# IM 消息系统 — 全部 12 个 Plan 执行完毕

**状态**: 全部完成
**日期**: 2026-04-02

---

## 总览

| Plan | 内容 | 状态 |
|---|---|---|
| Plan 1 | 项目基础 + 数据库 (PG schema, Go 项目, Tauri+Angular) | Done |
| Plan 2 | 认证系统 (JWT, bcrypt, 登录/注册) | Done |
| Plan 3 | 联系人与好友 (申请/接受/拒绝/屏蔽, 用户搜索) | Done |
| Plan 4 | 频道管理 (群聊/DM创建, 成员管理, 经典IM布局) | Done |
| Plan 5 | 消息写入路径 (HTTP API, Pulsar wrapper, MessageService) | Done |
| Plan 6 | Gateway + 推送 (WebSocket, 心跳, 实时推送, Redis路由) | Done |
| Plan 7 | 同步 + 拉取 (批量sync, 多设备已读, 空洞检测) | Done |
| Plan 8 | 客户端聊天核心 (消息渲染, 回复, 跳转底部, DM创建) | Done |
| Plan 9 | 搜索 (PG全文搜索, 全局搜索页面) | Done |
| Plan 10 | 文件与附件 (上传/下载, 图片预览, 附件消息) | Done |
| Plan 11 | 消息转发与收藏 (转发到多频道, 收藏列表, 右键菜单) | Done |
| Plan 12 | 个人资料与设置 (编辑资料, 通知/主题/语言设置) | Done |

---

## 服务端架构

```
cmd/gateway/     → HTTP + WebSocket 网关 (所有 API 端点 + WS 连接管理)
cmd/message/     → Pulsar 消费者 (消息持久化 + 推送事件发布)
cmd/sync/        → 同步服务 (预留, 当前逻辑在 Gateway 中)

internal/
  auth/          → bcrypt 密码哈希 + JWT 生成/验证
  config/        → YAML + 环境变量配置
  gateway/       → WebSocket Hub, Conn, 心跳, 推送消费者, Redis 路由
  handler/       → HTTP handlers (auth, channel, friend, message, sync, search, file, favorite, profile, settings)
  middleware/    → JWT 验证中间件
  model/         → 领域模型 (User, Channel, Message, Friendship, File, etc.)
  store/         → PG 数据访问 (User, Channel, Message, Friendship, File, Favorite, Search, Routing)
  pulsar/        → Pulsar 客户端封装 (Producer, Consumer)
  testutil/      → 测试辅助 (PG pool + table cleanup)

migrations/
  001_init.up.sql   → 9 张表 + 索引 + 触发器
```

## 客户端架构

```
src/app/
  core/
    auth/        → AuthService (JWT, signal-based)
    channels/    → ChannelService (频道 CRUD, 未读数)
    messages/    → MessageService (发送/拉取/同步, 空洞检测, 乐观更新)
    friends/     → FriendService (好友关系管理)
    ws/          → WebSocketService (连接/重连/心跳/推送)
    db/          → DatabaseService (SQLite 本地存储)
    search/      → SearchService (全文搜索)
    files/       → FileService (文件上传/下载)
    favorites/   → FavoriteService (收藏管理)

  features/
    login/       → 登录页
    register/    → 注册页
    main-layout/ → 经典 IM 布局 (左侧边栏 + 右内容区)
    channel-list/→ 频道列表 (排序, 搜索, 未读徽标)
    chat/        → 聊天窗口 (消息分组, 回复, 右键菜单, 跳转底部)
    contacts/    → 联系人页 (好友/请求/添加)
    channel-settings/ → 频道设置
    create-group/→ 创建群组对话框
    search/      → 全局搜索页
    favorites/   → 收藏列表页
    profile/     → 个人资料编辑
    settings/    → 应用设置
    home/        → 首页占位

  shared/
    guards/      → auth guard
    interceptors/→ JWT interceptor
```

## 消息可靠性保障体系

| 保障层 | 机制 | 实现位置 |
|---|---|---|
| **写入保障** | PG 事务原子 seq 分配 + client_msg_id 幂等 | Plan 1, 5 |
| **推送保障** | WebSocket 推送 + ACK + 1次重试 | Plan 6 |
| **拉取保障 - pong** | 心跳 pong seq diff → 增量拉取 | Plan 6 |
| **拉取保障 - 重连** | batch sync POST /api/sync | Plan 7 |
| **拉取保障 - 空洞** | count 检测 + scroll 触发 + 按需拉取 | Plan 7 |
| **多设备已读** | read_sync WS frame + Hub 广播 | Plan 7 |
| **定向消息** | phantom 占位符，seq 连续，未读减法计算 | Plan 1, 5 |

## API 端点清单

### 认证
- POST /api/auth/register
- POST /api/auth/login
- GET /api/auth/me

### 频道
- POST /api/channels (创建群)
- POST /api/channels/dm (创建/获取 DM)
- GET /api/channels (列表+预览+未读)
- GET /api/channels/{id}
- PUT /api/channels/{id}
- POST /api/channels/{id}/members
- DELETE /api/channels/{id}/members/{userId}
- GET /api/channels/{id}/members
- POST /api/channels/{id}/leave

### 消息
- POST /api/channels/{id}/messages (发送)
- GET /api/channels/{id}/messages (拉取)
- POST /api/channels/{id}/read (已读)
- POST /api/messages/forward (转发)

### 同步
- POST /api/sync (批量同步)

### 好友
- POST /api/friends/request
- POST /api/friends/accept
- POST /api/friends/reject
- GET /api/friends
- GET /api/friends/pending
- POST /api/friends/block

### 搜索
- GET /api/search?q=&type=&channel_id=&limit=
- GET /api/users/search?q=

### 文件
- POST /api/files (上传)
- GET /api/files/{id} (下载)
- GET /api/messages/{id}/attachments

### 收藏
- POST /api/favorites/{message_id}
- DELETE /api/favorites/{message_id}
- GET /api/favorites

### 用户
- PUT /api/users/me (更新资料)
- GET /api/settings
- PUT /api/settings

### WebSocket
- GET /ws?token=xxx&device=yyy

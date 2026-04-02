# Plan 6: Gateway + 推送路径 — 执行简报

**状态**: 完成
**日期**: 2026-04-02

---

## 完成情况

| Task | 内容 | 状态 |
|---|---|---|
| T1 | WS types + config (gateway ID, wire protocol) | Done |
| T2 | Connection Hub (Conn, writePump, registry) | Done |
| T3 | Redis routing (register/deregister/lookup) | Done |
| T4 | WS upgrade handler (JWT auth, readPump) | Done |
| T5 | Heartbeat (15s ping, pong with seq diff) | Done |
| T6 | Push consumer (Pulsar → Hub → WebSocket) | Done |
| T7 | Wire gateway main.go | Done |
| T8 | Client WebSocket service | Done |
| T9 | Client reconnect sync | Done |
| T10 | Integration verification | Done |

## 验证结果

- Go 测试: 57 PASS, 21 SKIP (PG) — 含 10 个新 Hub/Conn 测试
- Go vet: 无问题
- 三个服务编译: 全部通过
- Angular 构建: 成功 (274kB)

## 产出物

### 服务端
- **Gateway types** (`internal/gateway/types.go`): 完整 WS 协议类型 (WSFrame, Ping/Pong, Push, ACK)
- **Conn + Hub** (`internal/gateway/`): 连接管理，knownSeq 跟踪，线程安全注册表，writePump
- **Redis Routing** (`internal/store/routing.go`): user:connections 路由表，2h TTL
- **WS Handler**: GET /ws?token=xxx&device=yyy 升级，JWT 认证，readPump 处理 ACK
- **Heartbeat**: 15s ticker，获取 channel seq diff，推送 pong
- **Push Consumer**: Pulsar 消费推送事件，3s ACK 超时 + 1次重试
- **MessageService 扩展**: pushToMembers 按成员按 Gateway 分发推送事件

### 客户端
- **WebSocketService**: 连接/重连(指数退避)，15s ping with channel_seqs，push_msg/pong/send_ack 分发
- **Push 消息处理**: MessageService 订阅 pushMsg$ 实时更新聊天窗口
- **重连同步**: connected$ 触发 syncAllChannels()，pong 触发增量拉取

## 架构里程碑

三层消息可靠性保障全部就位：
1. **写入保障**: PG 事务原子 seq 分配 + client_msg_id 幂等 (Plan 5)
2. **推送保障**: WebSocket 推送 + ACK + 重试 (Plan 6)
3. **拉取保障**: pong seq diff + 重连 sync + 空洞检测 (Plan 6)

## 下一步

Plan 7: 同步 + 拉取路径 (Sync 协议, 增量拉取, 空洞补齐, 多设备已读同步)

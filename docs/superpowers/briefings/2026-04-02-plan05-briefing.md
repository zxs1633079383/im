# Plan 5: 消息写入路径 — 执行简报

**状态**: 完成
**日期**: 2026-04-02

---

## 完成情况

| Task | 内容 | 状态 |
|---|---|---|
| T1 | Message HTTP handler (send/fetch/read) + 7 tests | Done |
| T2 | Wire message routes in gateway | Done |
| T3 | Pulsar client wrapper (producer/consumer) | Done |
| T4 | MessageService binary (Pulsar consumer) | Done |
| T5 | Client MessageService (optimistic UI) | Done |
| T6 | Client chat window component | Done |
| T7 | Integration verification | Done |

## 验证结果

- Go 测试: 39 PASS, 21 SKIP (PG)
- Go vet: 无问题
- 三个服务编译: 全部通过
- Angular 构建: 成功 (267kB, chat chunk 6.69kB)

## 产出物

### 服务端
- **MessageHandler**: POST send (幂等), GET fetch (after/before/around 三模式), POST read
- **Gateway 路由**: 3 条消息路由已注册
- **Pulsar wrapper** (`internal/pulsar/`): Client, Producer (JSON+partition key), Consumer (blocking loop, ACK/NACK)
- **MessageService** (`cmd/message/`): 完整 Pulsar 消费者，消费 msg.incoming → MessageStore.Send → 发布 delivery event

### 客户端
- **MessageService**: signal-based 消息列表，optimistic send (appendOptimistic/confirmSent/removeOptimistic), fetchMessages (支持 after/before/around), markRead
- **Chat 组件**: 消息气泡 (mine=右蓝/theirs=左灰), phantom 过滤, Enter 发送 (Shift+Enter 换行), 自动滚动到底部

## 架构说明

- HTTP 端点当前直接调用 MessageStore.Send（同步写入），不经过 Pulsar
- MessageService binary 是为 Plan 6 的 WebSocket 推送准备的异步管道
- 消息可靠性的写入层已完成：client_msg_id 幂等 + PG 事务原子 seq 分配

## 下一步

Plan 6: Gateway + 推送路径 (WebSocket 管理, 路由, 心跳, 推送投递, ACK)

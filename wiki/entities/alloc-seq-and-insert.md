---
type: entity
title: MessageRepo.AllocSeqAndInsert
status: stable
last_verified: 2026-04-28
sources:
  - server/internal/repo/message.go:47-70
  - server/internal/repo/message.go:88-130
  - server/docs/BACKEND.md#§4.1
  - docs/GOAL.md#§4.2
related:
  - concepts/seq-cursor
  - flows/send-message
  - entities/sync-service
confidence: high
---

# `MessageRepo.AllocSeqAndInsert`

> 「seq 唯一入口」原则的物理落点。绕开它写 `messages` 表 = 必出 race。

## 接口签名

```go
AllocSeqAndInsert(ctx context.Context, tx *gorm.DB, msg *Message) (int64, error)
```

位置：`server/internal/repo/message.go:49`。

## 语义

在**单条事务**里完成：
1. `UPDATE channels SET last_seq = last_seq + 1 WHERE id = ? RETURNING last_seq`
2. `INSERT INTO messages (... seq=$last_seq, sender_id, team_id, ...) VALUES ...`

返回值：刚分配的 seq（`int64`）。

**关键不变量**：channel 维度 seq **严格单调**，并发写者仍能拿到不同 seq。靠 PG row-level lock + RETURNING，不靠 `SELECT ... FOR UPDATE` + 后续 INSERT 的两段。

## tx 复用规则

`tx != nil` 时复用调用方事务。这是为了让 `Send` 把 4 个步骤放进一个事务：

```text
Send tx
├─ idempotency check        (SELECT id, seq WHERE client_msg_id=...)
├─ AllocSeqAndInsert(tx)    (本函数)
├─ IncrementPhantomCount    (visible_to 模式下给非可见成员 +1 phantom)
└─ commit
```

`tx == nil` 时函数自己 `db.Transaction(...)`。

## 幂等性

通过 `(channel_id, client_msg_id)` 唯一约束 + 短路：上层 `Send` 在调本函数前先 SELECT；命中则跳过 alloc，把已有 `id`/`seq` 回填到 `msg`。

## 调用链

- 主路径：`service.MessageService.SendMessage` → `repo.Send` → 本函数
- 次路径：`repo.PostSystemMessage`（系统消息，如 channel_created）也走本函数
- M2 路径：`announcement` / `urgent` / `approval` 等异步落库都强制走本函数

## 红线（写代码前必读）

1. ❌ **不准**任何代码直接 `db.Create(&Message{})` 或 `INSERT messages` —— 见 [[decisions/hard-constraints]] 第 2 条
2. ❌ **不准**先 SELECT 再 INSERT 的两段写法 —— race window 必复现
3. ❌ **不准**让 `seq` 字段从客户端上传，后端必须自己分配
4. ✅ M4 后 `msg.SenderID` 类型已从 `int64` 改为 `string`（24-char hex mm UserID）—— 见 [[concepts/cookie-id-native]]

## 测试

- 单元：`server/internal/repo/message_test.go` 覆盖 happy / 重复 / 并发
- 集成：`server/tests/integration/m4_message_send_sync_test.go` 全链路验证

## 性能

热路径：单事务内 1 UPDATE RETURNING + 1 INSERT，PG 大约 1ms 量级。瓶颈在 phantom_count 批量 UPDATE（按 visibleTo 集合大小）。

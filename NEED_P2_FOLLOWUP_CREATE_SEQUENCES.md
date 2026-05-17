# NEED P2 follow-up — channel 创建路径必须 CreateChannelSequences

## 现象

P2 把 `IncrementSeq` 切到 `nextval('channel_msg_seq_<channelID>')` 形态（C018 §3.2），
但 channel 创建路径（`ChannelService.CreateGroup` / DM 创建）只调
`s.channels.Create(ctx, ch)`，**没有**调 `channelEvent.CreateChannelSequences(ctx, tx, ch.ID)`，
导致后续给该 channel 发首条消息时报：

```
ERROR: relation "channel_msg_seq_<uuid>" does not exist (SQLSTATE 42P01)
alloc seq: nextval msg: ERROR: ...
```

## 复现

任何「创建 DM 或 group → 发首条消息」的端到端测试都失败：

- `TestM4Sync_HappyPath`
- `TestM4ReactionAdd_HappyPath` / `Remove` / `List`

已在 P2 baseline (`3db81c6`) 验证 → **属 P2 / P1 migration 阶段遗漏，非 P3 引入**。

```bash
# P3 head（c655cec）和 P2 baseline（3db81c6）都跑：
go test -tags=integration -race -count=1 -timeout=90s \
  -run='^TestM4Sync_HappyPath$' ./tests/integration/...
# 两个 commit 都 FAIL，错误一致。
```

## P3 scope 之外 — 故不在本 worktree 修

P3 runbook 白名单仅含：
- `server/internal/repo/message.go`（mutation）
- `server/internal/repo/channel_member.go`
- `server/internal/repo/channel.go`（**仅 signature 调整**）
- `server/internal/service/message.go`
- `server/internal/service/channel.go`（**PostSystemMessage 路径改**）
- `server/internal/http/message.go`（仅依赖注入）

修复需改 `service/channel.go::CreateGroup` 和 DM 创建路径（在 `s.channels.Create(...)`
后调 `s.channelEvent.CreateChannelSequences(ctx, tx, ch.ID)`，同 tx），属
**P2 的 finalization 任务**或独立 P2.1 小 phase。

## 期望修复

1. 给 `service.ChannelService` 增 `channelEvent repo.ChannelEventRepo` 字段 + setter
2. `CreateGroup` 改为 `WithinTx(ctx, fn)` 包 Create + CreateChannelSequences + AddMember(s)
3. DM 创建路径同上
4. `cmd/gateway/main.go` 接线时 `channelSvc.AttachChannelEventRepo(channelEventRepo)`
5. 失败的 3 个 m4 集成测试转绿

## P3 当前如何兜底

- 我的 9 个 mutation→event 集成测试（`message_event_integration_test.go`）走
  **直接 `db.Create(ch)` + `events.CreateChannelSequences(tx, ch.ID)`** 的 raw
  路径，绕过 service 层，所以测试全绿
- P3 wire 改动 `cmd/gateway/main.go` / `m4_harness_test.go` 不会让 m4 测试**更糟**
  （fail 早已存在），但也不会**修复**它们

## 风险

- 任何手工 e2e 测试都会卡在「新建 DM → 发第一条消息」一步
- 离线 sync 想测的「重连拿到 edit / delete」也需要这条路径通；建议优先在
  P4 启动前 / 同步完成

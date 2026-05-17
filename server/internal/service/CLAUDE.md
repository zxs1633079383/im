# CLAUDE.md — server/internal/service/ 业务编排层

> 模块级指令，优先级：用户全局 `~/.claude/CLAUDE.md` > 项目根 `/Users/mac28/workspace/golangProject/im/CLAUDE.md` > 本文件 > 默认行为。
> 写 / 改 / review 本目录 `*.go` 前必须先 `Skill(skill="go-concurrency-patterns")`，再阅读项目根 `CLAUDE.md §1` 与 `docs/harness/README.md` 的 active 条目。

---

## 0. 模块定位

**编排层 (orchestration / use-case layer)**。三件事它做、三件事它不做。

**它做（Required）**：
1. **业务规则编排**：成员校验、权限分支、参数兜底、跨表组合（如「转移群主 = 角色互换 + creator_id 更新 + 系统消息 + 可选退群」一次事务七步走，见 `channel_transfer_owner.go`）。
2. **事务边界**：调 `repo.ChannelRepo.WithinTx(ctx, fn)` / `repo.WithTx` 开 / 提交事务，把同一原子单元的 repo 调用包进同一个 `tx`。
3. **副作用扇出**：调 `gateway.Hub.CrossPodPush` / `CrossPodBroadcast` / `BroadcastMemberEvent`（通过注入的 broadcaster interface），把业务结果广播给 channel 成员或目标用户。

**它不做（Forbidden）**：
1. **不碰 DB driver / SQL 字符串**：不写 `db.Exec("INSERT INTO ...")` / `db.Raw(...)` / 不直接拿 `*gorm.DB`；所有数据访问经 `repo.*Repo` 接口。
2. **不直写 WS 帧**：不 import `gateway/ws_handler.go` 内部、不调 `hub.PushToUser`；只能调 `CrossPodPush` / `CrossPodBroadcast` / `BroadcastMemberEvent`。
3. **不解析 HTTP / 不 bind JSON**：`*gin.Context` / `c.ShouldBindJSON` 是 handler 层职责；service 入参用 `XxxParams` struct，出参用业务对象 + sentinel error。

上接 `server/internal/http/`（handler 把已认证的 `callerID` + body 转成 `XxxParams` 传进来），下叫 `server/internal/repo/`（数据访问）、`server/internal/gateway/`（推送）、`server/cmd/message/`（异步 worker 也用同一组 service 函数）。

---

## 1. 影响范围

| 方向 | 依赖 |
|---|---|
| **上游 caller** | `server/internal/http/*.go`（84 路由对应 handler）；`server/cmd/message/*` 异步 worker；少量集成测试直接 wire service |
| **下游依赖** | `server/internal/repo/*Repo` 接口（DB / Redis）；`server/internal/gateway/Hub`（注入 broadcaster interface）；`server/internal/repo/UrgentRepo` 等子领域 repo |
| **观测** | `service/tracing.go` 暴露 `tracer`；每个 service 方法首句 `ctx, span := tracer.Start(ctx, "XxxService.Method")` + `defer span.End()`；`metrics.go` 暴露 `metrics()` 单例 |
| **测试** | 同包白盒：`*_test.go`（如 `channel_transfer_owner_test.go` / `channel_member_test.go`）；集成测试在 `server/tests/integration/m4_*.go` 复用同 service |
| **WS 协议** | 调 broadcaster 时只能用 C005 锁定的 22 种 type 字符串（`channel_member_updated` / `msg_updated` / `urgent_posted` 等），新增必须走 V2 RFC |

---

## 2. 功能模块清单

| 文件 | 领域 | 关键方法 | 备注 |
|---|---|---|---|
| `message.go` | 消息收发 / 拉取 / 转发 / 编辑 / 软删 | `SendMessage` / `BatchSendMessages` / `FetchMessages` / `FetchAfter` / `FetchAround` / `ForwardMessages` / `EditMessage` / `DeleteMessage` / `MessagesAfter` / `GetReaders` / `GetReplies` | 写入只走 `s.messages.Send(ctx, msg)`（其内部即 `AllocSeqAndInsert`） |
| `message_template.go` | 模板已收到落地 | `MarkTemplateReceived` | C001 复发点 #2（2026-05-01 漏 broadcast 教训） |
| `message_v073.go` | v0.7.3 配套消息接口 | mention / urgent / reaction snapshot 补丁 | 不新增 wire type，扩字段必须 `omitempty`（C005 §3.B） |
| `channel.go` | 群 / DM 创建 / 改名 / 加 / 移除 / 离开 | `CreateGroup` / `CreateOrGetDM` / `ListByUser` / `Update` / `AddMember` / `RemoveMember` / `LeaveChannel` | 每次成员变更通过 `s.postSys + s.fanMemberUpdate` 双管齐下（DB 系统消息 + WS 推送） |
| `channel_governance.go` | 管理员 / 公告 / 通知偏好 | `PatchChannel` / `AddManager` / `RemoveManager` / `ListManagers` | owner-only 权限分支由 `requireOwner` 集中校验 |
| `channel_transfer_owner.go` | 群主转移（C013） | `TransferOwner` | 7 步事务 + 后置 WS 扇出；DM 频道返 `ErrDMNoOwner` |
| `channel_topic.go` / `channel_sysmsg.go` / `channel_v073.go` | 频道置顶 / 系统消息构造 / 成员变更广播 | `BroadcastMemberEvent` interface 注入点 | broadcaster 由 `cmd/gateway` 装配，nil 时静默不广播（测试常用） |
| `read_stats.go` | 批量已读统计 | `GetReadStatsBatch` | 上限 `MaxReadStatsBatch=100`，超出返 `ErrTooManyReadStats` |
| `sync.go` | `/api/sync` 增量同步 | `SyncService.Sync` + `fillDeltaPayload` | v0.7.3 P-7.5 四分支决策（empty/full/slice/too_long），契约锁定 `docs/BACKEND.md §3.3` |
| `urgent.go` | 加急 / 确认 / 取消 | `SendUrgent` / `ConfirmUrgent` / `CancelUrgent` / `ListConfirmations` | 「send + 翻 is_urgent」复合走 sender interface 而非直拼，避免绕开 `AllocSeqAndInsert` |
| `announcement.go` / `approval.go` / `friend.go` / `favorite.go` / `file.go` / `notification.go` / `presence.go` / `quick_reply.go` / `reaction.go` / `search.go` / `settings.go` / `scheduled*.go` | 子领域编排 | 同一套范式：小 interface 注入 + ctx-first + sentinel err | |

---

## 3. SOP（写新功能 / 改老功能的标准流程）

1. **找 spec / harness**：是否有 `docs/harness/C{NNN}`、`server/docs/BACKEND.md` 锚点、`docs/MSG_UPDATE_HIGH_PERF_DESIGN.md` 之类的硬约束？没有 spec 不动手。
2. **画 service 方法签名**：定 `XxxParams` struct（≥5 个参数必须聚合）+ 出参 + sentinel error 集合（顶层 `var (Err... = errors.New(...))`）。
3. **写测试 (RED)**：
   - 单测 `xxx_test.go` 同包白盒，覆盖每条分支（含权限 / 不存在 / 边界）。
   - 集成测试 `server/tests/integration/m4_*_test.go` 走完整链路：handler→service→repo→DB→WS 回声。
4. **实现 (GREEN)**：
   - 首句 `ctx, span := tracer.Start(ctx, "...")` + `defer span.End()`。
   - 校验 → 取 channel/member context → 进事务 → 调 `s.messages.Send` / `repo.WithinTx` → 出事务 → 后置广播。
   - 函数 ≤ 60 行，超出拆 helper（如 `runTransferOwnerTx`）。
5. **重构 / 收敛**：相同的成员校验提到 `requireMember` / `requireOwner`；接口越小越好（consumer-side 声明）。
6. **跑 race**：`go test -race ./internal/service/...` 必须 clean。
7. **跑 harness gate**：见 §4。

---

## 4. Pre-commit 自检清单

```bash
# ① 单元 + 集成 + race detector（必须全绿）
cd server && go test -race -covermode=atomic ./internal/service/...

# ② C001：service 不直接 INSERT messages（必须 0 条）
grep -rEn "INSERT[[:space:]]+INTO[[:space:]]+messages" \
    server/internal/service/ --include='*.go' | grep -v '_test.go'

# ③ C001：service 不绕过 AllocSeqAndInsert（GORM Create Message struct，必须 0 条）
grep -rEn '(db|tx)\.Create\(&?repo\.Message\b' \
    server/internal/service/ --include='*.go' | grep -v '_test.go'

# ④ C001：service 不手算 seq（必须 0 条）
grep -rEn 'MAX\(seq\)' server/internal/service/ --include='*.go'

# ⑤ C002：service 不直接 hub.PushToUser（必须 0 条）
grep -rEn '\.PushToUser\(' server/internal/service/ --include='*.go' | grep -v '_test.go'

# ⑥ C002：service 不自己拼 PulsarPushEnvelope / 不裸用 pulsar.ProducerMessage（必须 0 条）
grep -rEn 'pulsar\.ProducerMessage\{|PulsarPushEnvelope\{' \
    server/internal/service/ --include='*.go' | grep -v '_test.go'

# ⑦ C005：service 不传 MsgType 字符串字面量（必须 0 条；只准引 gateway const）
grep -rEn 'MsgType:[[:space:]]*"[a-z_]+"' \
    server/internal/service/ --include='*.go' | grep -v '_test.go'

# ⑧ context 首参 + 不允许业务层 context.Background()
grep -rEn 'context\.Background\(\)' server/internal/service/ --include='*.go' | grep -v '_test.go'

# ⑨ panic 禁出（业务层）
grep -rEn '^\s*panic\(' server/internal/service/ --include='*.go' | grep -v '_test.go'

# ⑩ 函数 / 文件长度（手工 review）
wc -l server/internal/service/*.go | awk '$1 > 400 {print "⚠️ 文件超 400 行: "$0}'
```

任一条非 0 → 阻断 commit，回去修。

---

## 5. Commit 规范

格式：`<type>(service/<domain>): <中文描述>`。

`<type>` ∈ feat / fix / refactor / perf / test / docs / chore。`<domain>` 必须是本目录文件后缀对应的领域，常见取值：

| domain | 对应文件 |
|---|---|
| `service/message` | `message.go` / `message_template.go` / `message_v073.go` / `read_stats.go` |
| `service/channel` | `channel.go` / `channel_governance.go` / `channel_topic.go` / `channel_sysmsg.go` / `channel_v073.go` |
| `service/channel-transfer-owner` | `channel_transfer_owner.go`（C013 专属） |
| `service/sync` | `sync.go` |
| `service/urgent` | `urgent.go` |
| `service/announcement` / `service/approval` / `service/friend` / ... | 各子领域文件 |

示例：

```
feat(service/channel-transfer-owner): C013 群主转移 7 步事务落地

POST /api/channels/:id/transfer-owner 接通：
- 校验 owner + new_owner is member + 拒 DM
- 单事务：角色互换 + creator_id + 可选 leave + 系统消息
- 后置 fanMemberUpdate(MemberChangeOwnerTransfer)
覆盖：service/channel_transfer_owner.go + 5 集成测试 PASS
harness: docs/harness/C013
```

```
fix(service/message): C001 复发点修复 message_template MarkTemplateReceived 漏 broadcast

模板已收到 update messages.props 后未调 BroadcastUpdated，
导致 msg_updated WS 帧不下发。补 fanout 调用 + 集成测试。
```

更详细规范见项目根 `CLAUDE.md §3` + `~/.claude/rules/common/git-workflow.md`。

---

## 6. 约束规范（硬约束 / 不可降级）

### 6.1 消息写入只能调 `repo.MessageRepo.AllocSeqAndInsert`（C001）

- service 任何「向 messages 表新增行」的路径都必须经 `s.messages.Send(ctx, msg)`（其内部即 `AllocSeqAndInsert`）或 `s.messages.AllocSeqAndInsert(ctx, tx, msg)`（带外部 tx）。
- 禁止 `db.Exec("INSERT INTO messages ...")` / `db.Create(&repo.Message{Seq: x})` / 自算 `SELECT MAX(seq)`。
- 转发 / 批量 / 模板 / 系统消息 / 加急 / 撤回回声 — 全部走同一入口；这是 seq 严格单调 + 跨 pod 推送 + OTel 链路三件套的根契约。

### 6.2 跨 pod 推送只走 `gateway.CrossPodPush` / `CrossPodBroadcast`（C002）

- service 持有 broadcaster interface（如 `ChannelMemberBroadcaster.BroadcastMemberEvent` / `MessageBroadcaster.BroadcastToMembers`），由 `cmd/gateway` 装配；不直接 import `gateway/hub.go`。
- 禁止业务路径调 `hub.PushToUser` —— 异 pod 用户会漏。
- 单用户定向用 `CrossPodPush`，channel 多用户广播用 `CrossPodBroadcast`（一桶一 envelope）。
- 不允许 service 内 `New` 一个 Pulsar producer —— ProducerCache 复用是 `gateway` 内部的事。

### 6.3 WS 事件类型只能用 C005 锁定的 22 种

- V1 12 + M1 2 + M2 4 + v0.7 4 = **22**（详见 `docs/harness/C005 §2`）。
- service 调 broadcaster 时只能传 `gateway.TypeXxx` const 或字符串常量（如 `memberEventName = "channel_member_updated"`），禁裸字符串字面量。
- 新增 type → V2 RFC + 前 / Rust / 后端同步改动 + 集成测试 + harness 升级。`types.go` const 总数 > 22 → CI 直接 fail。
- payload 字段加 `omitempty` 才能向后兼容，必填字段加后无 omitempty = 旧 client 崩。

### 6.4 `context.Context` 首参贯穿（go-concurrency-patterns §1.4）

- 每个 exported 方法首参必须 `ctx context.Context`；下游调用全部传 ctx。
- 业务层禁止 `context.Background()`；只有 `cmd/*` 入口和 worker 启动可以 new root ctx。
- 异步分支（如有）必须 `errgroup.WithContext(ctx)` 或 `select { case <-ctx.Done(): return }`，禁裸 goroutine。
- 下游 IO 必须带 timeout：`ctx, cancel := context.WithTimeout(ctx, 5*time.Second); defer cancel()`。

### 6.5 事务边界由 service 控制

- 复合写入用 `s.channels.WithinTx(ctx, func(tx *gorm.DB) error { ... })`，repo 方法选 `XxxTx(ctx, tx, ...)` 系列复用同一 tx（参考 `channel_transfer_owner.go::runTransferOwnerTx`）。
- 调 `s.messages.AllocSeqAndInsert(ctx, tx, msg)` 时 `tx != nil` 走外部事务；`tx == nil` 时 repo 内部自开。
- 跨表原子操作 = 全部进同一 tx；后置广播必须在 commit 之后（receiver 不能看到 DB 未持久化的状态）。
- C016：所有 hot-path 写复杂度 = O(1)；禁 RMW JSON 数组列；禁把整条 wire 对象 `json.Marshal` 进 `props TEXT`；累计 / 队列字段必须 normalized 表 + 复合 PK + 单 SQL DELETE 清理。

---

## 7. 对应 Harness 映射

| Harness | 触发场景（本目录视角） | 验证手段 |
|---|---|---|
| [C001](../../../docs/harness/C001-allocseq-and-insert-only-message-write-path.md) | 任何 service 写 messages 表（新增 / 转发 / 模板 / 加急 / 撤回回声 / 批量） | §4 grep ②③④ + `go test -race ./internal/service/...` + 集成 `m4_message_sync_test.go` |
| [C002](../../../docs/harness/C002-cross-pod-push-must-go-gateway-crosspodpush.md) | service 任何"写完后通知 channel members / 推单用户"路径 | §4 grep ⑤⑥ + `m4_ws_cross_pod_test.go`（W3 跨 pod / W5 离线跳过） |
| [C005](../../../docs/harness/C005-ws-event-types-locked.md) | service 调 broadcaster 传 `MsgType` / `eventType` 时 | §4 grep ⑦ + `gateway/types_test.go::TestWSMessageType_Locked22` + `scripts/check-ws-types.sh` |
| [C012](../../../docs/harness/C012-id-type-string-migration.md) | 所有 service 入参 / 出参的 `ChannelID` / `MessageID` / `UserID` 全部 `string`（不是 `int64`） | 编译期由类型系统兜；`grep -rEn 'int64.*ID' server/internal/service/` 复查 |
| [C013](../../../docs/harness/C013-owner-transfer-endpoint.md) | 群主转移端点（`channel_transfer_owner.go::TransferOwner`） | 5 个集成测试（Happy / NotOwner / NewOwnerNotMember / DM / AlsoLeave）+ §4 grep + cses-client featdoc 08 联调 |
| [C016](../../../docs/harness/C016-msg-update-single-gate-seq-design.md) | service 任何 edit / softdelete / read advance / 累计字段写入 | §4 grep + `TestUpdateContent_ConcurrentEditSameMessage_NoLost` / `TestAdvanceReadSeq_CAS_RejectsRegressive` + `m4_msg_update_idempotency_test.go` |

冲突时**以更具体者为准**（harness > 项目根 > 用户全局）。

---

## 8. Update / Insert 规则

### 8.1 新增业务流（例：新加邀请码 / 新加置顶 pin）

完整 5 件套，缺一不可：

1. **service 新文件**：`server/internal/service/<domain>.go`，定义 `XxxService` struct + `NewXxxService` 构造函数 + 业务方法。
2. **service 单测**：`server/internal/service/<domain>_test.go`，覆盖每条分支（成功 / 失败 / 权限 / 边界），`-race` clean。
3. **http handler**：`server/internal/http/<domain>.go` 路由注册 + 参数 bind + 调 service + sentinel → HTTP status 映射。
4. **集成测试**：`server/tests/integration/m4_<domain>_test.go`，端到端走完链路（httpexpect v2 → DB → WS 回声）。
5. **harness 检查**：是否触发现有 harness 的 §1（如新加写消息 = 触发 C001；新加群成员变更 = 触发 C002/C005）？如是按 §3 Required 落地、§4 Verification 自检。

### 8.2 改老业务流（例：补字段 / 调权限）

- 看是否影响 wire 兼容：新增 payload 字段必须 `omitempty`；改语义必须升 V2。
- 看是否触动 harness `applies_to` glob：触动则 §4 grep 必须 0 条。
- 看是否影响事务边界：拆 / 合事务前先 review 测试是否覆盖中断 / 回滚分支。

### 8.3 新增 WSMessageType（高门槛）

- 必须走 V2 RFC（项目根 `CLAUDE.md §1.6` + C005 §4 首选 C）。
- 没拍板前 **绝对不允许**直接传新字符串 —— 立马触发 §4 grep ⑦ gate。
- 流程：RFC doc → 用户拍板 → `gateway/types.go` 加 const → service 调 broadcaster 引 const → 前端 `ws-normalizer.ts` + Rust `im_handlers.rs` 同步 → 集成测试 → harness C005 §2 表更新。

### 8.4 新增 / 改 service interface

- 接口在 consumer 端定义（"accept interfaces, return structs"），如 `MsgChannelStore` / `SyncChannelStore` / `messageSender` 都在 `service` 包内声明，repo 端只 implement。
- 接口方法 ≤ 3 个；超过拆。
- 改 interface 必须同步改所有实现 + 所有调用点 + mock（如 service 单测里的 fake）。

---

## 9. 文档关联

| 文档 | 用途 |
|---|---|
| [`server/docs/BACKEND.md §3.3`](../../docs/BACKEND.md) | `/api/sync` 契约 + 四分支决策（empty/full/slice/too_long），改 `sync.go` 必同步改文档 |
| `server/docs/BACKEND.md §4.1` | `AllocSeqAndInsert` 契约（C001 inline 目标） |
| `server/docs/BACKEND.md §5` | 跨 pod 推送架构（C002 inline 目标） |
| `server/docs/BACKEND.md §十一` | OTel 链路 / WS 协议契约（C005 inline 目标，待新建该节） |
| [`docs/harness/C001`](../../../docs/harness/C001-allocseq-and-insert-only-message-write-path.md) | 消息写入唯一入口 |
| [`docs/harness/C002`](../../../docs/harness/C002-cross-pod-push-must-go-gateway-crosspodpush.md) | 跨 pod 推送 |
| [`docs/harness/C005`](../../../docs/harness/C005-ws-event-types-locked.md) | WS 事件 22 种锁定 |
| [`docs/harness/C012`](../../../docs/harness/C012-id-type-string-migration.md) | ID 类型 string 化 |
| [`docs/harness/C013`](../../../docs/harness/C013-owner-transfer-endpoint.md) | 群主转移端点 |
| [`docs/harness/C016`](../../../docs/harness/C016-msg-update-single-gate-seq-design.md) | msg_update 单闸门 / hot-path 写复杂度 O(1) |
| `docs/MSG_UPDATE_HIGH_PERF_DESIGN.md` | msg_update 高性能设计 spec（C016 配套） |
| `~/.claude/skills/go-concurrency-patterns/SKILL.md` | Go 并发 / 资源 / context / 测试纪律的唯一标准 |
| `~/.claude/rules/golang/{coding-style,testing,patterns}.md` | Go 通用规则（function ≤ 60 行 / file ≤ 400 行 / 100% 覆盖率） |
| 项目根 [`CLAUDE.md §1.6`](../../../CLAUDE.md) | 本项目特有约束（AllocSeqAndInsert / CrossPodPush / 22 type / TTL 耦合） |

---

**Owner**：im 后端 service 编排层
**最后更新**：2026-05-17
**下次更新触发**：新增 service 文件 / harness 新条目命中本目录 `applies_to` / Spec 拍板改动事务 / 推送 / WS 契约

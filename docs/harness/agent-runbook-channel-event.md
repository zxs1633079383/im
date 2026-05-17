# Agent Runbook — channel_event 方案 B 落地（autonomous）

> 本 runbook 是为自动化 worktree agent 准备的**自包含**续接指南。Agent 收到任务时是无上下文新 session，必须先读本文 + 本任务对应 phase 节。

## 0. 项目级 ground truth

- **后端 repo**：`/workspace/golangProject/im`，branch `feat-offline-push`，baseline HEAD `74c7780`（Stage 0 准备前）
- **客户端 repo**：`/Users/mac28/workspace/angular/temp/cses-client`，branch `feat/im-reactor-2-offine`
- **依据 harness**：[C016](C016-msg-update-single-gate-seq-design.md) / [C017](C017-channel-event-append-only-log.md) / [C018](C018-pg-sequence-vs-row-lock-seq.md) / [C019](C019-sync-cursor-event-seq.md) + cses-client [C009](../../../../Users/mac28/workspace/angular/temp/cses-client/docs/harness/C009-sync-cursor-event-seq-client.md)
- **方案设计文档**：`docs/MSG_UPDATE_HIGH_PERF_DESIGN.md`（前置阅读，§2 事件分类总表 + §4 server 实现 + §5 client 实现）

## 1. 必读约束（无例外）

1. **Hot-path O(1)**：每个事件写入路径必须 O(1) SQL（单 INSERT / 单 UPDATE WHERE PK），禁 read-modify-write JSON 数组、禁 props 兜底（详见项目 CLAUDE.md §IM 写入性能约束）
2. **C001 / C016 / C017 / C018 / C019** 全部 active 状态，任何代码改动必须通过 §4 grep gate
3. **PG sequence 强制**（C018）：禁 `UPDATE channels SET seq=seq+1 RETURNING`，必须 `nextval('xxx_<id>')`
4. **同事务原则**（C017）：业务表 mutation 与 `INSERT channel_event` 必须在**同一个 tx**
5. **commit message** 用 Conventional Commits 中文 body（参 `~/.claude/rules/common/git-workflow.md §Commit Body 结构化模板`），≥ 3 文件改动必须 5 段结构化 body

## 2. Phase 拆分（dependency DAG）

```
P1 (migration) ── done by main (inline)
   │
   ▼
P2 (channel_event_repo)
   │
   ├─▶ P3 (mutation handlers 切换)
   ├─▶ P4 (sync 算法重写)
   │
P4 wire 稳定
   │
   ├─▶ P5 (cses-client cursor migration)
P3 + P4 done
   │
   └─▶ P7 (集成测试 + 万人群压测)
```

## 3. Per-phase 文件白/黑名单

### Phase P2：channel_event_repo

**worktree 路径**：`worktrees/p2-channel-event-repo/`
**branch**：`feat-p2-channel-event-repo`（基线 main worktree HEAD）
**预估**：1.5d（≤ 1 agent 跑完）

**文件白名单（只能改这些）**：
- `server/internal/repo/channel_event.go`（新建）
- `server/internal/repo/channel_event_test.go`（新建）
- `server/internal/repo/channel.go`（仅加 `NextMessageSeq` 方法 + 改 `IncrementSeq` 实现到 nextval 形态）
- `server/internal/repo/mocks/channel_event_repo_mock.go`（新建，mock for 后续 service 用）

**文件黑名单（禁改）**：
- `/SESSION.md` / `/CLAUDE.md` / `/README.md`（**仅根目录**这 3 个；子目录的 CLAUDE.md 可改，详见 §6 黑名单语义）
- `server/internal/service/*.go`（留给 P3）
- `server/internal/http/*.go`（留给 P3）
- 已有 migration 文件（P1 已落，禁改）

**完成标准**：
- [ ] `ChannelEvent` struct + `ChannelEventRepo` interface 定义
- [ ] `gormChannelEventRepo` 实现：`AppendEvent(tx, event)` / `NextEventSeq(tx, channelID)` / `FetchAfter(channelID, callerID, afterEventSeq, limit)` / `GetMemberChannelEventSeqs(callerID)`
- [ ] `channel.go::NextMessageSeq` 新增（nextval 形态）
- [ ] `channel.go::IncrementSeq` 改实现到 nextval（保留旧函数签名以保持调用方零改动）
- [ ] mocks 跑通
- [ ] cargo test / make test PASS（不引入新依赖前提下）
- [ ] commit chain ≥ 3，每个都 5 段结构化 body

### 跨项目契约协调（NEED_FIX 模式 / 2026-05-17 新增）

任何 phase 的 agent 都遵守：若发现需要另一个仓库改动才能完成本任务：

1. **不要阻塞自己**。在 worktree 根写 `NEED_<OTHER_PROJECT>_FIX.md`，内容：
   - 一句话说明 gap（哪个字段缺 / 哪个 endpoint 行为不对）
   - 期望另一仓库改哪个文件 / 加哪个字段 / 期望返回结构 / 期望函数签名
   - 当前你用的临时 mock/stub（让 main 知道接口落地后你怎么切回真接口）
2. **继续推进**自己 phase 内可做部分（用 mock 占位）
3. **主对话 cron 巡检**会扫 `worktrees/*/NEED_*.md`，命中 → fork 另一仓库的轻量级协调 worktree 修复
4. **修复完**会在你 worktree 内写 `RESOLVED_<gap-id>.md`，你切真接口即可

**反模式**：阻塞 / 退回 main 等 / 把对端 gap 当成 phase 失败。

### Phase P3：mutation handlers 切换

**worktree 路径**：`worktrees/p3-mutation-handlers/`
**branch**：`feat-p3-mutation-handlers`
**前置**：P2 完成 + merge 回 main
**预估**：1d

**文件白名单**：
- `server/internal/repo/message.go`（改 `Send` / `UpdateContent` / `SoftDelete` / `PostSystemMessage` 切到同事务 + AppendEvent）
- `server/internal/repo/channel_member.go`（改 `advance_read_seq` 改 / 加 ReadMark event）
- `server/internal/service/message.go`（接受 channelEvent 依赖）
- `server/internal/service/channel.go`（同上）
- `server/internal/http/message.go`（仅依赖注入调整，不改路由签名）

**黑名单**：
- **仅根目录** `/SESSION.md` / `/CLAUDE.md` / `/README.md` + `server/internal/service/sync.go`（留给 P4）
- 子目录的 CLAUDE.md（如 `docs/CLAUDE.md`、`server/internal/CLAUDE.md` 如果存在）允许改

**完成标准**：
- [ ] 所有 mutation 路径都 INSERT channel_event 同事务
- [ ] §4.1 grep gate 通过（C017）
- [ ] 单测覆盖：每种 EventType 至少 1 个 unit test 验证 channel_event 行真的写入
- [ ] 集成测试 PASS（既有的不退化）

### Phase P4：sync 算法重写

**worktree 路径**：`worktrees/p4-sync-event-cursor/`
**branch**：`feat-p4-sync-event-cursor`
**前置**：P2 完成 + merge 回 main
**预估**：1d

**文件白名单**：
- `server/internal/service/sync.go`（cursor 改 event_seq + 4 kind + 返回 events list + messages map）
- `server/internal/http/sync.go`（wire 字段 event_seq + SyncEntryKind enum）
- `server/internal/repo/channel_event.go`（如缺方法补，与 P2 协调）

**黑名单**：
- 同上 + `server/internal/service/message.go`（留给 P3）

**关键 invariant**：
- v1 wire（`seq` 字段）必须**保留兼容**（旧 cses-client legacy 仍能调）
- v2 wire（`event_seq` 字段）**新增**
- 通过请求体里有无 `event_seq` 字段判断版本：有 → v2 走新算法；无 → v1 走旧算法
- v2 算法返回 events 列表 + msg snapshot map；v1 返回 messages 列表

**完成标准**：
- [ ] v2 端到端 4 kind 集成测试 PASS（empty / events / slice / too_long）
- [ ] v1 legacy 集成测试不退化
- [ ] §4 grep 通过（C019）

### Phase P5：cses-client cursor migration（与 P4 真正并行）

**worktree 路径**：`worktrees/p5-event-cursor/`（在 **cses-client repo** 内，不是 im repo）
**branch**：`feat-p5-event-cursor-migration`
**前置**：~~P4 wire 稳定~~ **改为：P4 启动同时启动**，wire 契约不阻塞 — agent 直接按 C019 §3.1 wire 契约编码，发现 P4 实际实现有差异 → 写 `NEED_IM_SERVER_FIX.md` 不阻塞自己进度
**预估**：1.5d

**文件白名单**（cses-client repo）：
- `src-tauri/src/features/im/sync_engine.rs`（SyncReq / SyncResponse 用 event_seq）
- `src-tauri/src/features/im/types_v2.rs`（SyncCursor / SyncEntryKind / EventType / ChannelEvent 新定义）
- `src-tauri/src/features/im/handlers_v2/message.rs`（push_msg / msg_updated / msg_deleted 各加 event_seq 字段读取 + state.set_event_cursor）
- `src-tauri/src/features/im/handlers_v2/sync.rs`（新建：dispatch_sync_delta）
- `src-tauri/src/features/im/repo_v2/message_repo.rs`（新增 `upsert_if_newer` SQL + `truncate_channel`）
- `src-tauri/src/features/im/repo_v2/channel_repo.rs`（加 event_cursor 字段）
- `src-tauri/src/features/im/tables_v2/*.rs`（如需要 migration 加 channel.event_seq 列）
- `src-tauri/migrations/*.sql` 或 cses-client migration 入口

**黑名单**：
- **仅根目录** `/SESSION.md` / `/CLAUDE.md` / `/README.md`（子目录如 `src-tauri/CLAUDE.md` / `src/pages/message-v3/CLAUDE.md` 允许改）
- `src/**/*.ts`（Angular 层留主对话处理，可能涉及 UI）

**完成标准**：
- [ ] dispatch 按 event_type 分流（不允许 `_ => upsert` 兜底）
- [ ] §4.1 grep gate（C009）通过
- [ ] Rust 单测覆盖所有 EventType case
- [ ] cargo test PASS

### Phase P7：集成测试

**worktree 路径**：`worktrees/p7-integration-tests/`（im repo）
**branch**：`feat-p7-channel-event-integration-tests`
**前置**：P3 + P4 merge 回 main
**预估**：1.5d

**文件白名单**：
- `server/tests/integration/m4_offline_edit_sync_test.go`（新建）
- `server/tests/integration/m4_event_seq_monotonic_test.go`（新建）
- `server/tests/integration/m4_sync_event_cursor_test.go`（新建）
- `server/tests/integration/m4_seq_throughput_test.go`（新建，性能测试）
- `server/tests/integration/helpers/event_test_helper.go`（新建，公共测试 helper）

**完成标准**：
- [ ] 离线 edit → 重连 sync → 拿到 edited content（核心场景）
- [ ] 离线 delete → 重连 sync → 拿到 deleted=true（核心场景）
- [ ] 1000 goroutine 并发 mutation → event_seq 严格单调无缺漏
- [ ] sync 4 kind 全分支覆盖
- [ ] 性能基线：单 channel nextval 吞吐 ≥ 10k TPS

## 4. Agent 工作流（每个 agent 收到任务后）

1. **读本 runbook §0-§1**（5 min）+ **读 §对应 phase 节**（2 min）+ **读 C016/C017/C018/C019 主体**（10 min）
2. **df 预检** ≥ 5GB（单 worktree 阈值）
3. **进入 worktree 路径**，验证 branch 正确
4. **TodoWrite** 建当前 phase 子任务清单
5. **分批 commit**，每批一个独立可 review 的逻辑（参 git-workflow §模块切分铁律），5 段结构化 body
6. **完成时**：
   - 写一份 `worktrees/<phase>/REPORT.md`：列出 commit SHA / 改了哪些文件 / 是否触碰黑名单 / 跑通的测试
   - 主对话据此做 merge 决策
7. **超时（≥ 60 min）卡住**：write progress note + exit 让 main 介入

## 5. 失败兜底

- 若发现 phase 跨依赖（如 P3 中途发现需要 P4 的新接口）→ 写 BLOCKED.md 在 worktree 根 + exit，让 main 协调
- 若发现 harness gate fail → 不绕过，按 harness §3 正确做法修改后再 commit
- 若发现 PG sequence 在 testcontainers 行为异常 → 写 ISSUE.md 让 main 处理 / 可能 fallback row-lock 临时方案但 tag `TEMP_FALLBACK`

## 6. 主对话 merge 流程（Stage 4）

1. 收到所有 phase 完成通知 → 立刻 `CronDelete` 各 phase cron
2. 跑前置冲突分析：`git diff main..<feat-pX-branch> --name-only` 取两两交集
3. 顺序 merge：P2 → P3 → P4 → P5（cses-client repo） → P7
4. 每次 merge 后跑 `make test` 验证不破坏
5. 合并完写 SESSION.md §0 更新 + 删 worktrees
6. 打 tag：`v0.7.x-channel-event-final`

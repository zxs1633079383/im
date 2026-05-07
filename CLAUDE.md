# CLAUDE.md — im 项目级指令

> 这是 im 项目的项目级 Claude 指令，优先级低于用户全局 `~/.claude/CLAUDE.md`、高于默认行为。
> 会话开始请同时加载：`docs/GOAL.md`（目标）、`SESSION.md`（当前状态）、`docs/ARCHITECTURE.md`（文件地图）。

---

## 0. 本项目是什么

- **Telegram 式 IM 服务**，Go 后端 + Angular/Tauri 客户端，用来**替换基于 Mattermost 的 csesapi**（不做流量回切）。
- 整体目标、里程碑、成功标准见 `docs/GOAL.md`。
- 当前所处阶段、分支/tag、下一步决策点见 `SESSION.md`。
- 技术栈、目录地图、文件职能见 `docs/ARCHITECTURE.md`。

### 0.1 cses-client 工作目录（锁定，2026-05-01 用户拍板）

**唯一权威工作目录**：`/Users/mac28/workspace/angular/cses-client`
**唯一权威分支**：`im-backend-switch`（HEAD `7c8a0c972`，与 `origin/im-backend-switch` 同步）

> **历史**：曾尝试切换到 `/Users/mac28/workspace/angular/temp/cses-client` + `tauri-new-im`，但 `tauri-new-im` 是从 `origin/tauri` 直切出来的，**完全没有**任何 cses-client 端的 Phase 0/1 基础设施（apiFlavor 切换、ImApiAdapter、route-table、字段名 imGatewayHttp、ws-normalizer、Rust ImSeqDataSource skeleton、isWrappedResponse 适配），而这些基础设施全部以 14 个有效 commit `e0d037470..7c8a0c972` 形态落在 `im-backend-switch` 上。
>
> **结论**（2026-05-01 用户口头拍板，本节固化）：放弃 temp/cses-client + tauri-new-im 路线，回到 `angular/cses-client` + `im-backend-switch` 继续做 Phase 2-4。所有 SESSION.md / cutover 文档里 "已切到 temp/" 的措辞均作废，本节为准。
>
> **后续提及 cses-client 工作目录**：一律指 `/Users/mac28/workspace/angular/cses-client` + `im-backend-switch`，无歧义。引用旧 `temp/cses-client` 仅作历史 commit 检索用途。

---

## 1. 写 Go 代码的绝对规则（高性能 / 高吞吐 / 高稳定）

**任何对 `server/**/*.go` 的改动，必须按 `~/.claude/skills/go-concurrency-patterns/SKILL.md` 落地。**

触发时机：用户说要写 / 改 / review Go 代码、讨论并发 / 吞吐 / 性能、排查 race 条件——立即调用：

```
Skill(skill="go-concurrency-patterns")
```

下面是该 skill 的**最低底线摘要**（详情仍以 skill 本体为准）：

### 1.1 Goroutine 管控
- 每一个 `go` 都要有"爸爸"：`errgroup.Group` / `sync.WaitGroup` / `context.Context` 三选一兜底，**禁止裸奔 goroutine**。
- 退出条件必须显式：每个 `for` 循环里必须有 `select { case <-ctx.Done(): return }`。

### 1.2 Channel 使用
- 方向显式：函数参数用 `<-chan T` / `chan<- T`，不用裸 `chan T`。
- 缓冲大小要有理由：0 = 握手同步；N = 解释清楚容量选择依据。
- 关闭责任：**永远只由 sender 关闭**；多 sender 用独立 done channel 协调。

### 1.3 同步原语选择
- 默认用 channel 通信；**需要共享状态才动 Mutex**。
- 读多写少 → `sync.RWMutex`；高频 map → `sync.Map`。
- 计数 / 引用切换 → `atomic`；禁止用 Mutex 实现本应是 `atomic.Int64` 的计数。

### 1.4 Context 贯穿
- `context.Context` **必须是每个同步/异步调用的首参**（除显式 CPU-only 工具函数）。
- 业务层**禁止** `context.Background()`；只有 `main` / `cmd/` 入口可以 new root ctx。
- 下游调用都带 timeout/deadline：`ctx, cancel := context.WithTimeout(ctx, 5*time.Second); defer cancel()`。

### 1.5 资源复用
- Pulsar producer / Redis client / OTel tracer：`sync.Once` 单例，不得每次 new。
- 热路径 buffer：`sync.Pool`。
- HTTP client：全局共享 `http.Client` + 显式 `Transport`，不要默认 client（无超时）。

### 1.6 本项目特有约束
- **所有消息写入走 `repo.MessageRepo.AllocSeqAndInsert(ctx, tx, msg)`**。`tx != nil` 复用外部事务；不得绕过直接 INSERT `messages`。
- **所有跨 pod 推送走 `gateway.CrossPodPush(...)`**。不得在 service 里直接拿 `hub.PushToUser`——这会丢异 pod 用户。
- **Pulsar topic 命名 `PushTopicFor(gatewayID, env)`**。本地调试环境**必须**追加 `.{localname}` 后缀避免窜台。
- **Redis routing TTL = 45s**，心跳 15s × 3 容错。动这两个数必须同时动，不能单边。
- **WS 事件类型锁死 V1 12 + M1 2 + M2 4 + v0.7 4 = 22 种**（2026-05-07 用户拍板修正口径，详见 `docs/harness/C005`）。新增类型 → 走 V2 RFC + 前后端同步改动。

### 1.7 错误处理
- `errors.Is` / `errors.As` 判类型；不做字符串匹配。
- `fmt.Errorf("do X: %w", err)` 包一层上下文，不断链。
- 统一 `repo.ErrNotFound` 语义；RowsAffected=0 → ErrNotFound（见 `server/internal/repo/approval.go:107`）。
- **禁止吞错**：任何 `err != nil` 分支要么向上抛，要么记日志+返回友好错误。

### 1.8 测试纪律
- 新功能先写测试（RED → GREEN → REFACTOR）。集成测试走 `server/tests/integration/v5_*.go` 模式。
- `go test -race ./...` 必须 clean。
- 热路径基准：`go test -bench . -benchmem` 给出数字，不写"感觉更快了"。
- httpexpect v2 陷阱：**`GET(path+"?q=v")` 会 URL-encode `?` → 必须用 `.WithQuery("q", "v")`**（已踩过，见 commit `43c05ab`）。

---

## 2. 写前端 (Angular / Tauri Rust) 的规则

- Angular：新接口从 `ImApiAdapter` 走，不直接拼 URL。`apiFlavor` 切换逻辑在 `client/src/app/core/config/api.config.ts`。
- Rust：Tauri 命令要 `#[tauri::command] async`，通过 `AppState` 拿单例 client。持久化走 `im_seq_data_source.rs` 的 seq 游标存储。

---

## 3. Git / 分支 / 提交约束

- 主分支：`main`。功能分支：`feature/im-m{N}` 或 `feature/<topic>`。
- 提交信息：`<type>(<scope>): <subject>`，type ∈ feat/fix/refactor/docs/test/chore/perf/ci。**description / body 必须中文**。
- 小步提交：每个逻辑单元一次 commit。
- PR 前：`make verify-all` 必须绿；CI 必须绿。
- 详见用户全局 `~/.claude/rules/common/git-workflow.md`。

### 3.1 模块划分 + Tag 策略（多 Phase 大改）

跨多文件 / 多 Phase / 多天的大型切换（如 csesapi → im、Mattermost 下线、cses-client cutover Phase 2-4）：

- **一个 Phase = 一个模块 = 一个 tag**：每完成一个独立可验证的模块就立即 tag，方便 review + 回滚定位。
- **Commit scope 必须带模块 + Phase 编号**：`refactor(message-v3/template-received): Phase 2 切 path 到 im REST`。
- **Tag 命名**：`v<base>-phase<N>-<module-slug>` / `v<base>-rc<N>` / `v<base>-final` / `v<base>-client-verified`。
- **每个 tag 必带 message**：覆盖 commit 范围 + 验证状态（lint / test / 手工）。
- **顺序铁律**：模块完成 → commit → tag → 推 tag → 再下一个模块。禁止"先做完再补 tag"。

完整规范见 `~/.claude/rules/common/git-workflow.md` §「模块划分与 Tag 策略」。

### 3.2 cses-client cutover 实战 tag 序列

| Phase | 模块 | tag |
|---|---|---|
| Phase 1 ✅ | im 后端三件套（全局响应包裹 + POST received + GET read-stats）| **`v0.7.3-im-backend-base` @ `9679a36`（2026-05-03 已打，本地，未 push）** |
| Phase 2 | 模板已收到 path 化 | `v0.7.3-phase2-template-received` |
| Phase 3a | onChannelRead 6 处切 | `v0.7.3-phase3-channel-read`（合并 3a/b/c） |
| Phase 3b | 砍 onPostRead + inViewMsgRead | 同上 |
| Phase 3c | Rust handle_post_read 删 | 同上 |
| Phase 4a | message-status 异步 | `v0.7.3-phase4-read-stats-ui`（合并 4a/b/c/d） |
| Phase 4b | 加急弹窗 2 处异步 | 同上 |
| Phase 4c | mention 清理 read_sync | 同上 |
| Phase 4d | 33 处 readBits 清理 | 同上 |
| 验证 | 联调 / smoke / k6 | `v0.7.3-client-verified` |

---

## 4. GitNexus 使用（继承自父级 CLAUDE.md）

本项目被 GitNexus 索引。改代码前必做：

1. **改符号前**：`gitnexus_impact({target: "X", direction: "upstream"})` 看爆炸半径。HIGH / CRITICAL 必须告警用户。
2. **改完**：`gitnexus_detect_changes()` 验证影响范围符合预期。
3. **重命名**：用 `gitnexus_rename({..., dry_run: true})` 预览，禁止 find-replace。
4. **探索不熟的代码**：`gitnexus_query({query: "concept"})` / `gitnexus_context({name: "fn"})`，不要瞎 grep。

如果 GitNexus 提示 stale，终端跑 `npx gitnexus analyze`。

---

## 5. 会话开局快速自检

开局前 30 秒做完：

```bash
git status && git log --oneline -5       # 我在哪条分支，最近改了啥
cat SESSION.md | head -80                 # 上次会话留下什么
ls docs/ server/docs/                     # 找对应小节
```

然后：
- 读 `SESSION.md §3` 的"待用户拍板的分叉"，如果有未决，先让用户选。
- 如果用户给了明确任务：先查 `docs/ARCHITECTURE.md` 找到对应目录，再动手。
- 写 Go 代码前：**Skill(skill="go-concurrency-patterns")**。

---

## 6. 会话收尾检查

收尾前必做：

1. 所有变更 committed（或明确告诉用户为什么没 commit）。
2. 更新 `SESSION.md` §1（分支/commit）、§2（完成项）、§3（新待决）。
3. 如果发现新的重复 pattern / 决策，追加到 `docs/GOAL.md §4 硬约束` 或在 MEMORY 里登记。
4. 如果有 30min+ 耗时的任务，按用户全局规则做 `retro-30min` 复盘并写入 `/workspace/java/logs/{date}.json`。
5. 如果踩到了**新的重复坑**（≥ 3 次复现 / 用户明确说"沉淀下来" / Spec 拍板的硬约束），按 §8 流程沉淀进 `docs/harness/`。

---

## 7. 相关文档索引

| 文档 | 用途 |
|------|------|
| `docs/GOAL.md` | 全局目标 + 里程碑 + 硬约束 |
| `docs/ARCHITECTURE.md` | 技术栈 + 目录 / 文件地图 + 关键数据流 |
| `SESSION.md` | 当前会话状态 + 待决分叉（每次会话更新） |
| **`docs/harness/`** | **踩坑沉淀的可执行契约（带 grep / CI gate / 单测）—— 见 §8** |
| `server/docs/BACKEND.md` | M1–M6 详细契约（§3.3 /api/sync、§4.1 AllocSeqAndInsert、§5 跨 pod 推送、§十一 OTel） |
| `server/Makefile` | `verify-all` / `verify-build` / `verify-unit` / `verify-integration` |
| `~/.claude/skills/go-concurrency-patterns/SKILL.md` | **写 Go 的唯一标准** |

---

## 8. Harness Engineering（`docs/harness/`）

**Harness ≠ 文档**：每条都是**可执行的踩坑契约**，带 grep / CI gate / 单测验证，确保同一坑不再被踩。所有 Go / Angular / Tauri Rust 改动**默认加载** `docs/harness/` 下所有 active 条目。

### 8.1 启动加载

会话开局必读：

```bash
ls docs/harness/                          # 看有哪些条目
cat docs/harness/README.md | head -40     # 看 active 索引
```

任何符合条目 §1 触发场景的改动 → 强制按 §3 Required 落地，按 §4 Verification 自检。

### 8.2 触发条件（**什么情况下要新建 harness**）

满足**任意一条**就要沉淀：

| 触发 | 处置 |
|---|---|
| **≥ 3 次复现**（同一 root cause 在 `/workspace/java/logs/{date}.json` 被记 ≥ 3 次） | 必须沉淀；不再"下次注意" |
| 用户**明确说**"沉淀下来 / 写到 harness / 别再让我说第二遍" | 立即沉淀 |
| Spec / RFC 拍板的**硬约束**（`docs/GOAL.md §4` / `M{N}_SPEC.md §决策点`） | 创建并指向 spec 锚点 |
| **跨项目重复**（≥ 2 个项目 logs/ 出现同类） | 沉淀 + `~/.claude/rules/{lang}/` inline 候选 |
| CI / grep gate 已经能机械检测但还没 harness | 反向沉淀（gate 已就位 → 文档化 why hard fail） |

**禁止触发**：
- ❌ 单次踩坑 + 非用户/Spec 拍板 → SESSION.md "已知债务"即可
- ❌ 纯风格偏好 → 走 `coding-style.md`
- ❌ 临时 workaround → commit message + 代码注释即可

### 8.3 升级 / 弃用条件（Lifecycle）

| 状态 | 进入条件 | 退出条件 |
|---|---|---|
| `drafting` | 刚创建未实战命中 | 第 1 次 active 命中 → `active` |
| `active` | rec ≥ 1 + grep gate 接管 | 见下两栏 |
| `merged` | **30 天零新复现** + grep/CI gate 稳定 + 规则已 inline 进 `~/.claude/rules/{lang}/{file}.md` | 终态（保留作历史） |
| `deprecated` | 场景已不存在（模块删 / 协议换 / lib 消失） | 终态（不删文件，frontmatter `status: deprecated`） |

**晋升 active → merged 的强制 checklist**（README.md §4 同步）：
- [ ] §4 grep 命令存在且 CI 中 `grep ... \| wc -l == 0` 才放行
- [ ] §5 复现日志 ≥ 1 条且最后一条 ≥ 30 天前
- [ ] 规则原文已 inline 进 `~/.claude/rules/{lang}/*.md`
- [ ] frontmatter `status: merged` + `inline_target:` 指向 inline 锚点

**弃用 active → deprecated 的强制 checklist**：
- [ ] 场景源代码已删除（grep 0 条引用）或被替换协议覆盖
- [ ] `docs/harness/log.md` 写一条 `## [YYYY-MM-DD] deprecate | C{NNN} | <原因>`
- [ ] frontmatter `status: deprecated`，文件保留不删

### 8.4 当前在册 harness（详见 `docs/harness/README.md`）

| 编号 | 标题 | 状态 |
|---|---|---|
| C001 | `repo.MessageRepo.AllocSeqAndInsert` 是消息写入唯一入口 | active |
| C002 | 跨 pod 推送必须走 `gateway.CrossPodPush` / `CrossPodBroadcast` | active |
| C003 | Pulsar topic 必须经 `PushTopicFor`，本地必带 USER/HOSTNAME 后缀 | active |
| C004 | routing TTL 45s 与心跳 15s × 3 耦合，改一边必同时改另一边 | active |
| C005 | WSMessageType 锁定 22 种（含 contradiction 待用户拍板）；新增走 V2 RFC | active |
| C006 | httpexpect v2 路径禁拼 `?q=`，必须 `.WithQuery` | active |
| C007 | 全局 responseEnvelope 中间件已生效，handler 禁止再 wrap status/data | active |
| C008 | 76 端点 × 5 case + 16 WS × 6 case 是 100% 覆盖率硬门 | drafting |

### 8.5 引用规范

会话里引用：`harness/C001 §3` / `harness/C008 §4.4 Batch-B`。
PR 描述里引用：`docs/harness/C001-allocseq-and-insert-only-message-write-path.md`。
日志里引用：`logs/{YYYY-MM-DD}.json#L{行号}` —— harness §5 Recurrence Log 的标准格式。

# CLAUDE.md — im 项目级指令

> 这是 im 项目的项目级 Claude 指令，优先级低于用户全局 `~/.claude/CLAUDE.md`、高于默认行为。
> 会话开始请同时加载：`docs/GOAL.md`（目标）、`SESSION.md`（当前状态）、`docs/ARCHITECTURE.md`（文件地图）。

---

## 0. 本项目是什么

- **Telegram 式 IM 服务**，Go 后端 + Angular/Tauri 客户端，用来**替换基于 Mattermost 的 csesapi**（不做流量回切）。
- 整体目标、里程碑、成功标准见 `docs/GOAL.md`。
- 当前所处阶段、分支/tag、下一步决策点见 `SESSION.md`。
- 技术栈、目录地图、文件职能见 `docs/ARCHITECTURE.md`。

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
- **WS 事件类型锁死 V1 12 + M2 4 = 16 种**。新增类型要升 V2 + 前后端同步改动。

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
- 提交信息：`<type>(<scope>): <subject>`，type ∈ feat/fix/refactor/docs/test/chore/perf/ci。
- 小步提交：每个逻辑单元一次 commit。
- PR 前：`make verify-all` 必须绿；CI 必须绿。
- 详见用户全局 `~/.claude/rules/common/git-workflow.md`。

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

---

## 7. 相关文档索引

| 文档 | 用途 |
|------|------|
| `docs/GOAL.md` | 全局目标 + 里程碑 + 硬约束 |
| `docs/ARCHITECTURE.md` | 技术栈 + 目录 / 文件地图 + 关键数据流 |
| `SESSION.md` | 当前会话状态 + 待决分叉（每次会话更新） |
| `server/docs/BACKEND.md` | M1–M6 详细契约（§3.3 /api/sync、§4.1 AllocSeqAndInsert、§5 跨 pod 推送、§十一 OTel） |
| `server/Makefile` | `verify-all` / `verify-build` / `verify-unit` / `verify-integration` |
| `~/.claude/skills/go-concurrency-patterns/SKILL.md` | **写 Go 的唯一标准** |

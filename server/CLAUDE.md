# CLAUDE.md — server/ 模块级指令（Go 后端入口层）

> 本文件仅约束 `server/` 根（cmd 入口、Makefile、go.mod、整体 server 级 SOP）。
> 优先级：用户全局 `~/.claude/CLAUDE.md` > 项目根 `/Users/mac28/workspace/golangProject/im/CLAUDE.md` > **本文件** > 默认行为。
> 与子目录冲突时遵循「更具体者优先」：`server/internal/repo/CLAUDE.md` 等子模块约束覆盖本层。

---

## 0. 模块定位

**是什么**：im Go 后端的**最外壳**——三个 cmd binary（`gateway` / `message` / `sync`）的装配入口 + `Makefile` 构建/验证体系 + `go.mod` 依赖锚点。

**负责**：
- ✅ 进程装配（DI、配置加载、信号处理、graceful shutdown）
- ✅ 构建 / 测试 / lint / vuln / mocks / migrate 的统一 target
- ✅ Tiered Verification（V1 build+lint / V2 unit race / V3 integration）的 gate 入口
- ✅ go.mod 依赖管理 + Go toolchain 版本锁定（当前 `go 1.26.2`）

**不负责**（**严禁**在本层写业务代码）：
- ❌ HTTP handler 逻辑 → `internal/handler/`
- ❌ 业务 service / 仓储 → `internal/service/` / `internal/repo/`
- ❌ Pulsar / Redis / Gateway 接线细节 → `internal/{pulsar,gateway,cache}/`
- ❌ 协议 / wire schema → `internal/types/` + `migrations/`

> cmd 入口里**只能**做：读 config → 构造单例（DB / Redis / Pulsar / Hub）→ 装路由 → 起 server → 等信号。任何 if-else 业务分支出现在 `cmd/*/main.go` 都是味道。

---

## 1. 影响范围

**上游依赖**（本层依赖谁）：

| 依赖 | 来源 | 备注 |
|---|---|---|
| `internal/config` | `server/internal/config/` | Consul KV + viper |
| `internal/handler` / `internal/service` / `internal/repo` | 同上 | 业务装配点 |
| `internal/gateway` / `internal/pulsar` / `internal/cache` | 同上 | 基础设施单例 |
| `migrations/` | `server/migrations/` | `make migrate-up/down` 直接消费 |

**下游影响**（改这里波及谁）：

| 改动 | 波及面 |
|---|---|
| `cmd/gateway/main.go` 装配顺序 | 所有 HTTP 路由 + WS Hub + Pulsar consumer 启动时序 |
| `Makefile` target 重命名 | CI（`.github/workflows/*` + `scripts/check-handler-coverage.sh`）+ 用户本地脚本 |
| `go.mod` minor bump | 全 server 包重新编译；可能触发 testcontainers / pulsar-client-go 行为差异 |
| `cmd/*/main.go` 新增 env var | 需要同步改 `scripts/run-all-dev.sh` + `config.example.yaml` + 部署 chart |

> 改 `cmd/gateway/main.go` 前，**必须** `gitnexus_impact({target: "cmd/gateway/main.go", direction: "upstream"})` 看爆炸半径。

---

## 2. 功能模块清单

### 2.1 cmd binary 入口

| 路径 | 角色 | 启动方式 | 备注 |
|---|---|---|---|
| `server/cmd/gateway/main.go` | **HTTP + WS + sync 业务**主进程 | `make run-dev` / `go run ./cmd/gateway` | 84 路由 + 22 WSMessageType 全部跑在这里；`POST /api/sync` 也在 gateway 进程内 |
| `server/cmd/message/main.go` | Pulsar consumer（**不监听 HTTP**） | `make run-message-dev` / `go run ./cmd/message` | 消费 `incoming-message` topic → `AllocSeqAndInsert` 写库 → 触发 `CrossPodPush`（C001 + C002）|
| `server/cmd/sync/main.go` | 预留 stub | `make run-sync-stub` | **会立即退出**，仅用于验证 binary 可编译；真正的 sync 在 gateway 里 |
| `server/cmd/v4-client/main.go` | 本地联调客户端工具 | `go run ./cmd/v4-client` | 调试用，不上 prod |

> ⚠️ 项目历史上曾有 `cmd/im-server/` 单 binary 设想，**已废弃**。当前真相是 `gateway` + `message` 两进程 + `sync` stub 三 binary（v4-client 是工具）。新人不要去找 `im-server`。

### 2.2 Makefile target 一览（执行频率从高到低）

| Target | 用途 | 何时跑 |
|---|---|---|
| `make verify-all` | V1+V2：`go build ./... && go vet && golangci-lint && go test -race -short ./...` | **每次 commit 前必跑** |
| `make verify-build` | V1 only：build + vet + lint，无测试 | 改了大量文件先确认编译过 |
| `make verify-unit` | V2 only：`go test -race -short ./...` | TDD 循环里的 GREEN 步骤 |
| `make check-handler-coverage` | C008 gate：路由数 + 测试数下限校验 | `verify-integration` 自动前置；新增路由必跑 |
| `make verify-integration` | V3：`-tags=integration` 跑 testcontainers（pg + redis），45m timeout | 加 / 改 handler / service / repo 必跑 |
| `make check` | lint + vuln + test-unit | PR 前可选 |
| `make check-full` | check + test-integration | 推 tag 前必跑 |
| `make build-all` | 编译 gateway / message / sync 三 binary 到 `bin/` | 出 release artifact |
| `make migrate-up` / `migrate-down` / `migrate-create name=xxx` | 数据库 migration | schema 改动必经路径 |
| `make run-dev` / `run-all-dev` / `run-all-dev-windows` | 本地起服务（单窗口聚合 / tmux 多窗口） | 联调 |
| `make run-pre-tunnel` | **危险**：直连 pre 集群 DB | 仅复现 pre-only bug |
| `make mocks` | mockery 生成 mock | 改 interface 后 |
| `make lint` / `make vuln` | golangci-lint / govulncheck | CI 已串联，本地按需 |

### 2.3 go.mod 关键依赖锚点

| 依赖 | 版本 | 改 / 升级前必读 |
|---|---|---|
| `go` | `1.26.2` | 本地 toolchain 不一致 → `mockery` install 会用 `GOTOOLCHAIN=go1.26.2` 兜底 |
| `github.com/gin-gonic/gin` | `v1.11.0` | 改路由 / middleware 形态前同步过 C007 + C009 |
| `github.com/apache/pulsar-client-go` | `v0.18.0` | 与 `internal/pulsar` 单例 + C003 topic 命名耦合 |
| `github.com/gavv/httpexpect/v2` | `v2.16.0` | 集成测试基座；**禁拼 `?q=` 见 C006** |
| `github.com/testcontainers/testcontainers-go` | — | redis module port-mapping race **见 C015**，禁直连 GetMappedPort |
| `github.com/golang-jwt/jwt/v5` | `v5.3.1` | 鉴权 path，配合 C010 `UserData:<userId>` |

---

## 3. SOP — 写代码 → 验证 → commit 工作流

**任何 server/ 改动**必须按以下顺序：

```
0. 开局：cat SESSION.md | head -80  &&  ls docs/harness/
1. 写代码前：Skill(skill="go-concurrency-patterns")  ← 项目根 §1 强制
2. TDD RED：先在 internal/.../*_test.go 加用例 → make verify-unit 看红
3. 写实现 → make verify-unit GREEN
4. 改 handler / 路由 / WSMessageType → make check-handler-coverage（C008 gate）
5. 改 handler / service / repo → make verify-integration（V3 testcontainers）
6. make verify-all 全绿
7. git diff --stat  →  确认改动范围合预期
8. commit（见 §5）
9. 收尾：更新 SESSION.md §1/§2/§3
```

**特殊路径**：

- 只改 `Makefile` / `go.mod` → `make verify-build` 即可，可跳 V2/V3
- 只改 `cmd/*/main.go` 装配 → 必须 `make verify-build` + 启动一次 `make run-dev` 看进程是否能跑 30s 不 panic
- 只改 `migrations/` → `make migrate-up` 在本地 dev DB 跑一次确认成功 + 写对应 `down` 文件
- 升级 `go.mod` 依赖 → `make verify-all` + `make verify-integration` + `make vuln` 三连

---

## 4. Pre-commit 自检清单

### 4.1 必跑命令（按顺序）

```bash
cd /Users/mac28/workspace/golangProject/im/server
make verify-build              # V1：编译 + vet + lint
make verify-unit               # V2：race + short
# 改了 handler/service/repo/migrations 才跑：
make check-handler-coverage    # C008 gate
make verify-integration        # V3：testcontainers（10+ min，看耐心）
```

或一键：

```bash
make verify-all                # = V1 + V2
make check-full                # = verify-all + verify-integration + vuln
```

### 4.2 Grep gate 清单（人工 + CI 双查）

以下 grep 在 server/ 下结果**必须为 0** 才能 commit：

| Gate | 命令 | 对应 harness |
|---|---|---|
| 直 INSERT messages | `grep -rn "INSERT INTO messages" --include="*.go" \| grep -v "AllocSeqAndInsert\|_test.go"` | C001 |
| 绕过 CrossPodPush | `grep -rn "hub.PushToUser\|hub.Broadcast" --include="*.go" \| grep -v "internal/gateway"` | C002 |
| 写死 Pulsar topic | `grep -rn '"push-' --include="*.go" \| grep -v "PushTopicFor\|_test.go"` | C003 |
| 双层 envelope wrap | `grep -rn 'c.JSON.*"status".*"success"' --include="*.go" \| grep -v "response_envelope.go\|_test.go"` | C007 |
| httpexpect 拼 `?` | `grep -rn '\.GET(.*?' --include="*_test.go"` | C006 |
| 测试 helper 漏挂中间件 | `grep -rn "gin.New()" --include="*_test.go" \| grep -v "WithResponseEnvelope\|setupRouter"` | C009 |
| testcontainers 不 retry | `grep -rn "redisContainer.MappedPort\|GetMappedPort" --include="*_test.go" \| grep -v "WithRetry"` | C015 |

### 4.3 失败处置

- `verify-build` 红 → **build-error-resolver** agent
- `verify-unit` 红 → **tdd-guide** agent；不许"改测试让它过"
- `check-handler-coverage` 红 → 看 `scripts/check-handler-coverage.sh` 输出，按 C008 §4 补 1 单测 + 1 集成
- `verify-integration` 红 + 抖动嫌疑 → 走 C015 retry helper；不要直接 skip

---

## 5. Commit 规范

沿用 `~/.claude/rules/common/git-workflow.md` 的 Conventional Commits，**中文 body 强制**：

```
<type>(<scope>): <中文描述 ≤ 50 字>

<可选中文 body：解释 why；每行 ≤ 72 字符>
```

### 5.1 server/ 层常用 scope

| scope | 适用 |
|---|---|
| `cmd/gateway` / `cmd/message` / `cmd/sync` | 装配入口改动 |
| `build` / `makefile` | Makefile target / 构建脚本 |
| `deps` / `gomod` | go.mod 升级 / replace 调整 |
| `migrations` | SQL migration |
| `ci` | scripts/check-*.sh / .github/workflows |

### 5.2 多 Phase 大改 → Phase tag 强制

跨多文件 / 多天的切换（参照 `~/.claude/skills/im-module-phase-tag` + 项目根 §3.1）：

```
refactor(cmd/gateway/bootstrap): Phase 2 拆 DI container 出 main

把 Pulsar / Redis / DB 单例从 main.go 78 行抽到 internal/bootstrap，
保留 graceful shutdown 顺序：cancel(rootCtx) → hub.Drain → pulsar.Close。
```

完成 → `git tag -a v0.7.X-phase2-bootstrap-split -m "..."` → 推 tag → 再下一 Phase。

### 5.3 禁止项

- ❌ `update Makefile` / `wip` / `chore: misc`
- ❌ `--no-verify` 跳 hook（除非用户明示）
- ❌ 一个 commit 混 `feat` + `fix` + `refactor`
- ❌ commit body 写"AI 生成 / Co-authored-by: Claude"

---

## 6. 约束规范（本层强约束）

### 6.1 Go 代码铁律

**任何 `.go` 改动必须先调用**：

```
Skill(skill="go-concurrency-patterns")
```

最低底线摘要（详情见项目根 §1.1–§1.8）：

- **每个 `go` 必须有爸爸**：`errgroup` / `WaitGroup` / `context` 三选一
- **context 是首参**：业务层禁 `context.Background()`，只有 `cmd/*/main.go` 可 new root
- **channel 方向显式 + sender 关闭**：`<-chan T` / `chan<- T`
- **资源单例**：Pulsar producer / Redis client / OTel tracer 走 `sync.Once`
- **错误链不断**：`fmt.Errorf("... %w", err)`；禁字符串匹配
- **测试 race clean**：`go test -race ./...` 是 hard requirement

### 6.2 cmd 入口层专属约束

- ❌ **不许**在 `cmd/*/main.go` 写业务 if-else；判分支 → service 层
- ❌ **不许**直接 `os.Getenv`；走 `internal/config` viper 统一入口
- ❌ **不许**裸 `panic` / `log.Fatal` 跳过 graceful shutdown；用 `signal.NotifyContext` + `errgroup`
- ✅ 单例构造失败 → 把 err 抛出到 main，由 main 决定退出码
- ✅ shutdown 顺序固定：cancel root ctx → drain in-flight → 关 Pulsar producer → 关 DB

### 6.3 Makefile 专属约束

- ✅ 任何新 target 必须 `.PHONY:` 声明
- ✅ env var 必须 inline 在 target 里（不依赖 shell rc）
- ✅ `verify-*` 系列**禁止**改 default goal（`.DEFAULT_GOAL := build-all` 锁死）
- ❌ 不许把 docker / kubectl 调用放进 `verify-*`（整合测试可，verify-build 不行）

### 6.4 go.mod 升级约束

- minor / patch bump → 跑 `make check-full` 全绿后单 commit
- major bump → 先开 RFC，列出 breaking change + 影响面，**禁止单方面升**
- 新增 require → 解释 why（commit body）+ 同步过 `make vuln`

---

## 7. 对应 Harness 映射

| Harness | 触发场景 | 验证手段 |
|---|---|---|
| [C001](../docs/harness/C001-allocseq-and-insert-only-message-write-path.md) | 改 `cmd/message/main.go` 消费逻辑 / 任何写 `messages` 表 | grep `INSERT INTO messages` = 0；集成测试 `TestM*MessageWrite*` |
| [C002](../docs/harness/C002-cross-pod-push-must-go-gateway-crosspodpush.md) | 改 `cmd/gateway/main.go` push 装配 / 跨 pod fanout | grep `hub.PushToUser` 出 `internal/gateway` 外 = 0 |
| [C003](../docs/harness/C003-pulsar-topic-localname-suffix.md) | 改 Pulsar topic 命名 / 本地 dev 启动 | grep 写死 `"push-"` = 0；本地 topic 必有 `{USER}/{HOSTNAME}` 后缀 |
| [C006](../docs/harness/C006-httpexpect-query-encoding.md) | 加 / 改 `server/tests/integration/*_test.go` | grep `.GET(.*?` = 0；必须 `.WithQuery(k, v)` |
| [C007](../docs/harness/C007-response-envelope-no-double-wrap.md) | 加 / 改 handler / middleware | grep handler 内 `c.JSON.*"status"` = 0 |
| [C008](../docs/harness/C008-handler-coverage-gate.md) | 加 / 删 路由 / WSMessageType | `make check-handler-coverage` 必绿；路由数 + 测试数下限校验 |
| [C009](../docs/harness/C009-test-helper-tracks-middleware-shape.md) | 加 / 改 middleware | 所有 `server/tests/**/helper*.go` 同步 `setupRouter` 形态 |
| [C014](../docs/harness/C014-test-coverage-100-percent.md) | 加路由 / WSMessageType | 每条 1 单测 + 1 集成测试；CI 阈值 85% |
| [C015](../docs/harness/C015-testcontainers-redis-port-race.md) | 集成测试新增 redis testcontainer 引用 | 必须用 `tests/integration/testhelper.WithRedisRetry()` 兜底 |

> 完整 16 条索引：`/Users/mac28/workspace/golangProject/im/docs/harness/README.md`。

---

## 8. Update / Insert 规则

### 8.1 新增 Makefile target

1. 写 target 前先想清楚：是 `verify-*` 系列扩展 还是 `run-*` 系列还是独立 utility？
2. `.PHONY: <name>` 必加
3. 同时改 `scripts/check-handler-coverage.sh`（如涉及 gate）+ `.github/workflows/*.yml`（如要 CI 跑）
4. 在本文件 §2.2 表格补一行
5. commit scope = `build` 或 `makefile`

### 8.2 新增 cmd binary

罕见。**用户拍板才能加**。流程：

1. 在 `cmd/<name>/main.go` 创入口（参考 `cmd/sync/main.go` 16 行 stub 起步）
2. `Makefile` 加 `build-<name>` + `run-<name>-dev` target；`build-all` 串上
3. 加 `scripts/run-all-dev.sh` 对应分支（如要纳入 `run-all-dev`）
4. 更新本文件 §2.1 + 项目根 `CLAUDE.md §0.1` 如有部署影响
5. 写一条 harness 候选（drafting）说明该 binary 与 gateway / message 的边界

### 8.3 升级 go.mod 依赖

```bash
cd server
go get -u <module>@<version>     # 或 go mod edit -require=<module>@<version>
go mod tidy
make verify-all                  # V1 + V2
make verify-integration          # V3
make vuln                        # 漏洞扫
git diff go.mod go.sum           # 确认范围
```

commit message 模板：

```
chore(deps): 升 pulsar-client-go v0.18.0 → v0.19.0

修 v0.18.0 producer.SendAsync 在 broker 重启后 callback
不触发的 race（issue #1234）。本地 message-consumer 验证 OK，
集成测试 TestM2PulsarReconnect 通过。
```

---

## 9. 文档关联

| 文档 | 在哪 | 用途 |
|---|---|---|
| 项目根 CLAUDE.md | `/Users/mac28/workspace/golangProject/im/CLAUDE.md` | im 项目全局约束 + Go 铁律 + Phase tag 序列 |
| 目标 | `/Users/mac28/workspace/golangProject/im/docs/GOAL.md` | 全局目标 + 里程碑 + 硬约束 |
| 架构 | `/Users/mac28/workspace/golangProject/im/docs/ARCHITECTURE.md` | 技术栈 + 目录地图 + 关键数据流 |
| 当前会话 | `/Users/mac28/workspace/golangProject/im/SESSION.md` | 当前分支 / tag / 待决分叉 |
| 后端契约 | `/Users/mac28/workspace/golangProject/im/server/docs/BACKEND.md` | M1–M6 详细契约（§3.3 sync / §4.1 AllocSeqAndInsert / §5 跨 pod 推送 / §十一 OTel）|
| Harness 索引 | `/Users/mac28/workspace/golangProject/im/docs/harness/README.md` | C001–C016 全表 |
| Go skill | `~/.claude/skills/go-concurrency-patterns/SKILL.md` | **写 Go 唯一标准** |
| 全局 git-workflow | `~/.claude/rules/common/git-workflow.md` | Conventional Commits + Phase tag |
| 全局 testing | `~/.claude/rules/common/testing.md` | 80% 覆盖率 + TDD 流程 |

---

> 维护：本文件每次大改 server 装配 / Makefile / go.mod major bump 时同步审；最低契约线就是项目根 `CLAUDE.md §1`，子模块（`internal/repo/CLAUDE.md` 等）可加严不可放宽。

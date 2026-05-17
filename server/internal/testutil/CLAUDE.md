# CLAUDE.md — server/internal/testutil 模块级指令（测试公共件）

> 本文件约束 `server/internal/testutil/` 子树（含 `containers/`）：测试 helper 公共件、cookie fixture、testcontainers 启动器。
> 优先级：用户全局 `~/.claude/CLAUDE.md` > 项目根 `/Users/mac28/workspace/golangProject/im/CLAUDE.md` > `server/CLAUDE.md` > **本文件** > 默认行为。
> 与子目录冲突时遵循「更具体者优先」。本目录无更深一级 CLAUDE.md，本文件为最末梢。

---

## 0. 模块定位

**是什么**：im 后端**测试基础设施层**——所有 `_test.go`（含 `internal/**/*_test.go` 与 `tests/integration/*.go`）共用的 helper 集合。**纯测试代码**，不出现在生产 binary 里。

**负责**：
- ✅ **httpexpect v2 包装器**（`httpexpect.go::NewExpect`）——给定 `http.Handler` 返回挂在 `httptest.Server` 上的 `*httpexpect.Expect`，自动 `t.Cleanup(srv.Close)`
- ✅ **Mattermost cookie fixture**（`cookie_fixture.go::CookieFixture` + 常量 `RealUserID` / `RealCookieID` / `RealCompanyID`）——往 Redis `UserData:<userId>` 写 v0.7.4 wire shape JSON，让请求带 cookieId 过 `MattermostCookieResolve` + `CookieRequired` 中间件
- ✅ **deterministic user id 生成器**（`HexUserID(seed)` / `MakeCookieID(seed)` / `MakeUserID(seed)`）——给需要多身份的测试发 24 字符 hex id
- ✅ **testcontainers-go 启动器**（`containers/redis.go` / `containers/postgres.go` / `containers/pulsar.go`）——每条都注册 `t.Cleanup(Terminate)`，redis 还带 `port "6379/tcp" not found` 的 5×200ms retry 兜底（C015）

**不负责**（**严禁**在本层写）：
- ❌ 业务逻辑 / 任何非 test 代码路径（containers/ 文件全部用 `//go:build integration` 隔离，httpexpect.go / cookie_fixture.go 仅由 `_test.go` import）
- ❌ 单测用例本身——helper 只提供基建，测试函数留在调用方包内（`internal/**/*_test.go` 或 `tests/integration/`）
- ❌ wire 协议常量复制——所有 header 名走 `middleware` 包导出（如 `middleware.MMCookieHeader` / `middleware.UserDataKeyPrefix`），不要在 helper 里写死字符串

---

## 1. 影响范围

**上游依赖**（本层依赖谁）：

| 依赖 | 来源 | 备注 |
|---|---|---|
| `internal/middleware` | `MMCookieHeader` / `MMTeamHeader` / `UserDataKeyPrefix` | shape 改动 → 同步改 `CookieFixture` 与 `m4_harness_test.go` |
| `github.com/gavv/httpexpect/v2` v2.16.0 | go.mod | path 禁拼 `?` 见 C006 |
| `github.com/testcontainers/testcontainers-go` v0.35.0 | go.mod | redis port-mapping race 见 C015 |
| `github.com/redis/go-redis/v9` | `redis.UniversalClient` | `IM_REDIS_CLUSTER=true` 时需走 cluster client（C010）|

**下游影响**（改这里波及谁）：

| 改动 | 波及面 |
|---|---|
| `httpexpect.go::NewExpect` 签名 | 所有 `internal/**/*_test.go` 黑盒测试 + `tests/integration/` 196+ 集成测试 |
| `cookie_fixture.go::CookieFixture` 签名 / payload shape | 所有需要鉴权的集成测试；v0.7.4 wire 改动已让 `cookieID==userID`，再改要走 spec |
| `containers/redis.go::StartRedis` retry 上限 / backoff | redis race fail 率（C015）；testcontainers v0.36+ 升级再 verify |
| `containers/postgres.go::StartPostgres` migration loader | 全部 V3 集成测试启动表结构；漏 migration 会让 column 不存在 |
| 新增 helper 函数 | 必须同时改本文件 §2 清单 + 写 helper 自测 |

---

## 2. 功能模块清单

### 2.1 helper 文件

| 路径 | 角色 | 公共导出 | 备注 |
|---|---|---|---|
| `httpexpect.go` | httpexpect v2 包装器 | `NewExpect(t, h) *httpexpect.Expect` | 21 行；建议**永远走这个入口**，不要在测试里手搓 `httptest.NewServer` |
| `cookie_fixture.go` | Mattermost cookie fixture + identity 生成 | `CookieFixture` / `RealUserID` / `RealCookieID` / `RealCompanyID` / `RealOrgID` / `HexUserID` / `MakeCookieID` / `MakeUserID` | v0.7.4 wire: `cookieID == userID`；`requireHex24` 会在 typo 时 `t.Fatal` |
| `containers/redis.go` | testcontainers redis 启动 | `StartRedis(t) string`（URI） | `//go:build integration`；5×200ms retry 兜底 C015 |
| `containers/postgres.go` | testcontainers postgres + 全套 migration | `StartPostgres(t) string`（DSN） | 自动 glob `migrations/*.up.sql` 按字典序灌入 |
| `containers/pulsar.go` | testcontainers pulsar | `StartPulsar(t) string`（broker URL） | 给跨 pod push / consumer 集成测试用 |
| `containers/*_test.go` | helper 自测 | — | 任何改 helper 必须先跑 `go test -tags integration ./internal/testutil/containers/` |

### 2.2 与生产代码的隔离

- `containers/*.go` 全部 `//go:build integration`：默认 `go build ./...` / `go test ./...` 不会编译它们；只有 `-tags integration` 时进
- `httpexpect.go` / `cookie_fixture.go` **没有** build tag：因为单元测试（`internal/**/*_test.go` 不带 integration tag）也要用 cookie fixture / httpexpect helper
- **不许**让生产代码 import 本目录任何东西。若 `internal/foo/foo.go` 出现 `import ".../testutil"` → CI 拒绝（Go 编译会拒不带 `_test.go` 后缀的文件 import 测试基础设施，但要防御写成 `testutil_helper.go` 这种诡异命名）

---

## 3. SOP — 加 / 改 helper 工作流

**任何 testutil/ 改动**必须按以下顺序：

```
0. 开局：cat SESSION.md | head -80  &&  ls docs/harness/
1. 写代码前：Skill(skill="go-concurrency-patterns")
2. helper 改 / 新增：
   a) 写实现 + godoc 注释（必含 example 与 C00X 引用）
   b) 同步加 helper 自测 (server/internal/testutil/<file>_test.go 或 containers/<file>_test.go)
3. 跑单元自测：
     go test ./internal/testutil/...                          # 不带 integration
     go test -tags integration ./internal/testutil/containers/... # 带 integration
4. 改 middleware 形态 → 同步 sweep（C009）：
     grep -rn "MMCookieHeader\|UserDataKeyPrefix\|setupRouter\|gin.New()" \
       server/tests/ server/internal/ --include='*_test.go'
   把 raw 解析 / 旧 shape 的调用点全部对齐
5. 跑全集成测试：
     make verify-integration
6. 改 query 拼接的 helper → 跑 C006 grep（必须 0 条）：
     grep -rEn '(expect|e)\.(GET|POST|PUT|PATCH|DELETE)\("[^"]*\?' \
       server/tests/ server/internal/ --include='*_test.go'
7. 改 testcontainers 启动器 → 跑 C015 grep（必须 0 条直 MappedPort）：
     grep -rEn 'container\.MappedPort\(|\.MappedPort\(ctx,' \
       server/internal/testutil/containers/ | grep -v "_test.go"
8. commit（见 §5）
9. 收尾：SESSION.md §2 加一行 "testutil helper xxx 新增/改"
```

**典型场景速查**：

- 加新 HTTP helper（例如 `LoginAndGetCookie`）→ 放 `cookie_fixture.go` 同一文件；签名首参 `t *testing.T`，自动 `t.Helper()` + `t.Cleanup`
- 加新 testcontainers 模块（例如 `StartNATS`）→ 单独 `containers/nats.go` + `//go:build integration` + 同款 retry 框架
- 改 cookie payload shape（v0.7.5+）→ **必须**先在 `docs/harness/` 写一条 `C0NN-cookie-shape-vX.X` 草案，列举受影响测试数；不允许悄悄改

---

## 4. Pre-commit 自检清单

### 4.1 必跑命令

```bash
cd /Users/mac28/workspace/golangProject/im/server

# A. helper 自测（containers 需 integration tag；其他不需）
go test ./internal/testutil/...
go test -tags integration ./internal/testutil/containers/...

# B. 全集成测试串一遍（改 cookie / httpexpect / 容器启动器 → 必跑）
make verify-integration

# C. race detector（helper 多在 goroutine 上下文里被多 subtest 并发调用）
go test -race -tags integration ./internal/testutil/containers/...
```

### 4.2 Grep gate 清单（必为 0 才能 commit）

| Gate | 命令 | 对应 harness |
|---|---|---|
| httpexpect path 拼 `?` | `grep -rEn '(expect\|e)\.(GET\|POST\|PUT\|PATCH\|DELETE)\("[^"]*\?' server/tests/ server/internal/ --include='*_test.go'` | C006 |
| testcontainers 直调 MappedPort 无 retry | `grep -rEn '\.MappedPort\(' server/internal/testutil/containers/ \| grep -v "_test.go"` | C015 |
| helper raw 解析响应（绕过 unwrap） | `grep -rEn '\.Expect\(\)\.Status\([0-9]+\)\.JSON\(\)\.(Object\|Array)\(\)' server/tests/integration/m4_harness_test.go server/internal/testutil/ --include='*.go'` | C009 |
| cookie fixture 写死 header 字符串（绕过 middleware 包导出） | `grep -rn '"x-mm-cookie"\|"cookieId"' server/internal/testutil/ --include='*.go'` | C009 / 项目根 §1.6 |
| containers 漏挂 Cleanup | `grep -rn "tcredis.Run\|postgres.Run\|pulsar.Run" server/internal/testutil/containers/ \| grep -v "t.Cleanup\|_test.go"` | C015 |

### 4.3 失败处置

- helper 自测红 → 先跑单文件 `go test -v -run TestXxx ./internal/testutil/containers/` 看原始日志，不要 tail
- redis testcontainer 偶发 `port "6379/tcp" not found` → C015 已有 retry，若 5 次仍 fail → 看 Docker daemon 状态，不要把 retry 上限调大盖问题
- cookie fixture 让所有集成测试 401 → 99% 是 middleware shape 漂移；按 §3 step 4 sweep helper

---

## 5. Commit 规范

沿用 `~/.claude/rules/common/git-workflow.md` Conventional Commits，**中文 body 强制**。

### 5.1 本层常用 scope

| scope | 适用 |
|---|---|
| `testutil` | `httpexpect.go` / `cookie_fixture.go` 改动 |
| `testutil/containers` | `containers/redis.go` / `postgres.go` / `pulsar.go` 改动 |
| `testutil/cookie` | 仅 cookie fixture / wire shape 相关 |

### 5.2 示例

```
feat(testutil): 新增 LoginAndGetCookie helper

把 7 个集成测试里重复的"调 /api/auth/me + 拿 cookie"
抽到 testutil.LoginAndGetCookie(t, rdb, userID)，复用
CookieFixture 的 v0.7.4 wire shape；自动 t.Cleanup 清 Redis key。
```

```
fix(testutil/containers): redis 启动加 5×200ms retry 兜底 v0.35.0 race

testcontainers-go v0.35.0 ContainerReady 后偶发返回
`port "6379/tcp" not found`，retry 5 次后稳定（C015）。
误诊耗 5h 教训沉淀在 docs/harness/C015。
```

### 5.3 禁止项

- ❌ `update testutil` / `wip`
- ❌ 一个 commit 既改 helper 又改业务（拆开）
- ❌ `--no-verify` 跳 hook

---

## 6. 约束规范（本层强约束）

### 6.1 httpexpect 路径禁拼 `?q=`（C006）

**绝对禁止**：

```go
// ❌
expect.GET("/api/messages/read-stats?ids=1,2,3").Expect()
expect.GET(fmt.Sprintf("/api/users?q=%s", q)).Expect()
```

**必须用**：

```go
// ✅
expect.GET("/api/messages/read-stats").
    WithQuery("ids", "1,2,3").
    Expect().Status(200)
```

helper 内部如需暴露"带 query 的便利方法"，应在签名上接收 `query map[string]string` 后**内部**链式 `.WithQuery`，绝不接收 raw URL 字符串。

### 6.2 加 / 改 middleware → 同步 sweep helper（C009）

`cookie_fixture.go` 与 `m4_harness_test.go::buildEngine` 是 middleware 形态的**唯一镜像**。任何 `cmd/gateway/main.go::buildRouter` 或 `internal/middleware/*` 改动 PR 必须：

1. 跑 C009 §4.1 grep 找所有 raw 解析点
2. 同步改 helper 让响应解析走 `successBody` / `successBodyArray` / `errorBody`（位于 `m4_harness_test.go`）
3. `make verify-integration` 跑 full log（**禁止 tail**），从 `/tmp/im-integration-*.log` 用 `grep -cE '^--- FAIL:'` 数 fail
4. fail 数 = 0 才能 commit

事故复盘见 C009 附录（commit `ba7a77d` 修 Run #1 ~120 fail）。

### 6.3 testcontainers v0.35.0 race retry（C015）

`StartRedis` / `StartPostgres` / `StartPulsar` **必须**对 `port not found in container` 错误 5×200ms backoff retry。已落地见 `containers/redis.go:46-57`。

- **不许**扩大 retry 范围到其他错误（如 image pull fail / OOM）—— 真错必须立刻 fail
- **不许**把 retry helper 用到生产代码 —— 仅 `containers/` 测试基础设施允许
- **不许**为 race 切换到 docker-compose 双源（破坏测试 isolation）

### 6.4 cookie_fixture 必须与 middleware 形态保持一致（C009）

v0.7.4 wire 关键不变量（强制 `t.Fatal` 校验）：

- `cookieID == userID`（24 字符 hex）—— `CookieFixture` 内 `cookieID != userID` 直接 `t.Fatalf`
- 写入 Redis key = `middleware.UserDataKeyPrefix + userID`（**禁止**在 helper 里写死 `"UserData:"`）
- header 名走 `middleware.MMCookieHeader` / `middleware.MMTeamHeader` 导出常量
- `companyID` 缺省时不 SET organizes 数组，让 `TeamIDFromCtx` 返回空 → C011 路径走 nullable

若新增 helper 涉及 cookie / userId 校验，**必须**复用 `requireHex24` 与现有常量，不许自造正则。

### 6.5 helper 函数风格（Go 公共规则强约束）

- 首参 `t *testing.T`，第二参 context / dependency client
- 入口立即 `t.Helper()`，让失败 stack trace 指回调用点而非 helper
- 创建 resource → 同步 `t.Cleanup(...)`，禁手动 `defer` 让调用方记得清理
- 资源清理 ctx 用独立 `context.WithTimeout(context.Background(), 2*time.Second)`（不要复用 test ctx，因为 t.Cleanup 时 test ctx 已 cancel）
- 函数 ≤ 60 行 / 文件 ≤ 400 行（`~/.claude/rules/golang/coding-style.md`）

---

## 7. 对应 Harness 映射

| Harness | 触发场景（本层视角） | 验证手段 |
|---|---|---|
| [C006](../../../docs/harness/C006-httpexpect-query-encoding.md) | 加 / 改任何 httpexpect 调用便利 helper | §4.2 grep `?` 必须 0 条；所有 query 必走 `.WithQuery` |
| [C009](../../../docs/harness/C009-test-helper-tracks-middleware-shape.md) | 改 middleware shape 后 sweep cookie_fixture / harness helper | §4.2 grep raw `.JSON().Object()` 必 0 条；`make verify-integration` 全绿（full log） |
| [C015](../../../docs/harness/C015-testcontainers-redis-port-race.md) | 加新 testcontainers 启动器 / 升级 testcontainers-go | `containers/*.go` 必须有 `port not found` retry；v0.36+ 升级跑 100 轮验证 |

**间接关联**：
- C010（cookie + companyId header）—— `cookie_fixture.go` 是该 wire shape 的**唯一**测试镜像
- C011（channels.team_id nullable）—— `CookieFixture` `companyID=""` 时不写 organizes，刚好让 `TeamIDFromCtx` 返回空触发 nullable 路径
- C008 / C014（路由 + WS 覆盖矩阵）—— 集成测试调用本层 helper，间接强制 helper 稳定性

---

## 8. Update / Insert 规则

### 8.1 新增 helper 函数（最常见）

1. 写实现：放到对应文件（cookie 相关 → `cookie_fixture.go`；http 相关 → `httpexpect.go`；容器 → `containers/<service>.go`）
2. 加 godoc 注释：必含 Example block + 引用相关 harness（`See docs/harness/C00X.md`）
3. 写自测：同目录 `<file>_test.go`；至少 1 happy + 1 error path
4. 跑 §4.1 自测命令
5. 在本文件 §2.1 表格补一行
6. commit scope = `testutil` 或 `testutil/containers`

### 8.2 新增 testcontainers 模块

罕见。流程：

1. 新文件 `containers/<service>.go` + `//go:build integration`
2. 模板照抄 `redis.go`：`Start<Service>(t *testing.T) string` 返回 URI/DSN；内嵌 retry helper（如该 service 模块也有 port race）
3. 同款 `containers/<service>_test.go`：起容器 → 拉 client → ping → 检查 cleanup
4. 接到调用方 harness（如 `m4_harness_test.go::newM4Env`）
5. 本文件 §2.1 + 项目根 `server/CLAUDE.md §2.3` 补依赖锚点（如有新 go.mod require）

### 8.3 改 cookie wire shape

**高风险**，需要走 spec：

1. 先在 `docs/harness/` 写 `C0NN-cookie-shape-vX.X-migration` 草案
2. 列举受影响测试数（grep `CookieFixture\|RealCookieID\|MMCookieHeader`）
3. 提案给用户拍板
4. 拍板后：改 `cookie_fixture.go::buildUserPayload` + `requireHex24` → 同步改 `middleware/cookie_required.go` 与 `internal/auth/*`
5. 全集成测试跑一遍（C009 sweep 流程）
6. tag `v0.7.X-cookie-vX.X`

### 8.4 升级 testcontainers-go

1. `go get github.com/testcontainers/testcontainers-go@v0.36`
2. `go mod tidy`
3. 跑 100 轮：`for i in $(seq 1 100); do go test -tags integration ./internal/testutil/containers/...; done`
4. 若 0 race fail → 考虑去除 retry helper（C015 deprecation 路径）
5. 仍 race → 保留 retry，更新 C015 §5 复现表 + 报上游 issue

---

## 9. 文档关联

| 文档 | 在哪 | 用途 |
|---|---|---|
| 项目根 CLAUDE.md | `/Users/mac28/workspace/golangProject/im/CLAUDE.md` | im 全局约束 + Go 铁律 + Phase tag |
| server/CLAUDE.md | `/Users/mac28/workspace/golangProject/im/server/CLAUDE.md` | cmd / Makefile / go.mod / 装配入口规范 |
| 后端契约 | `/Users/mac28/workspace/golangProject/im/server/docs/BACKEND.md` | M1–M6 详细 wire 契约 |
| C006 httpexpect query | `/Users/mac28/workspace/golangProject/im/docs/harness/C006-httpexpect-query-encoding.md` | 路径禁拼 `?` 详细版 |
| C009 helper middleware-shape | `/Users/mac28/workspace/golangProject/im/docs/harness/C009-test-helper-tracks-middleware-shape.md` | middleware sweep + full log |
| C015 testcontainers race | `/Users/mac28/workspace/golangProject/im/docs/harness/C015-testcontainers-redis-port-race.md` | redis port-mapping race retry |
| C010 cookie userdata | `/Users/mac28/workspace/golangProject/im/docs/harness/C010-userdata-resolve.md` | v0.7.4 cookie wire shape 来源 |
| Go testing 规则 | `~/.claude/rules/golang/testing.md` | 100% coverage / httpexpect 陷阱 / table-driven |
| Go coding-style | `~/.claude/rules/golang/coding-style.md` | 60 行 / 400 行 / panic 禁用 |
| Skill | `~/.claude/skills/go-concurrency-patterns/SKILL.md` | 写 Go 唯一标准 |

---

> 维护：本文件每次改 cookie wire shape / 升级 testcontainers-go major / 新增 helper 模块时同步审。本目录是测试基础设施层最末梢——下游（`tests/integration/CLAUDE.md`）调用方约束在 `server/tests/CLAUDE.md`。

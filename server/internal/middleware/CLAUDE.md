# CLAUDE.md — middleware 模块（im 项目）

> 本文件是 `server/internal/middleware/` 的模块级指令，优先级低于
> 项目根 `/Users/mac28/workspace/golangProject/im/CLAUDE.md`、用户全局
> `~/.claude/CLAUDE.md`，高于默认行为。
>
> 必读：harness `C007` / `C009` / `C010`（详见 §7 映射）。

---

## 0. 模块定位

middleware 是 **所有 HTTP 请求的横切关注点**，gin engine 在 handler 之前 / 之后跑一遍。
本目录承载 **鉴权 / metrics / CORS / ResponseEnvelope（外层 wrap）** 四类职能：

- **鉴权链**（`MattermostCookieResolve` → `CookieRequired`）：把 `cookieId` header 解析为
  `*MattermostUser`，把 `companyId` header 落到 ctx，再 gate 401。
- **JWT 兜底**（`JWTGin` / `JWTOrCookie`）：M3 遗留路径，仅 `/api/admin/*` + WS 握手保留。
- **可观测**（`metrics()` 单例）：OTel `im.auth.cookie_cache.{hit,miss,size}` 三个 instrument。
- **响应包裹**（`responseEnvelope`，物理实现在 `internal/http/`）：见 `harness/C007`。

**核心特征**：改一个 middleware 的形态（响应 shape / ctx key / header 名 / 顺序）= 影响
**所有 handler 和所有测试**。这就是 §6 约束②（C007）与约束①（C009）存在的原因。

---

## 1. 影响范围

| 受影响位置 | 影响形式 |
|---|---|
| 所有 `server/internal/http/*.go` handler | 鉴权 ctx key（`UserIDKey` / `mmUserCtxKey` / `teamIDCtxKey`）+ 响应 envelope shape |
| 所有 `server/tests/integration/*.go` 集成测试 helper | `seedDM` / `seedGroup` / `successBody` 等 helper 直接解析 envelope 后字段（C009） |
| `server/internal/testutil/cookie_fixture.go` | 构造 `cookieId` + `companyId` header 的真实 fixture（C010） |
| `server/internal/gateway/ws_handler.go::authenticate()` | WS 握手通过 `ResolveCookieID` 复用同一 LRU 缓存（C010） |
| `server/cmd/im-server/main.go::buildRouter` / `cmd/gateway/main.go` | middleware 注册顺序唯一入口，改顺序会改 ctx key 可见性（§6 ④） |
| 登录态 / 鉴权 / metrics 探针 | 所有 401 / 403 / cookie cache hit 率监控均在本模块产出 |

**爆炸半径**：实测 2026-05-07 Run #1 注入 envelope middleware 没扫 helper → 142 集成测试
全部红色（详见 `harness/C009 §2`）。任何改动前先 grep 影响面，不可凭直觉。

---

## 2. 功能模块清单

| 文件 | 职能 | 关键 export |
|---|---|---|
| `cookie_required.go` | M4+ 鉴权 gate；要求前面 middleware 已 set `UserIDKey`，否则 401 | `CookieRequired()` |
| `cookie_required_test.go` | gate 行为白盒测试（注入 / 拒绝 / 空 string） | — |
| `jwt_gin.go` | M3 遗留 JWT 鉴权；`JWTGin` 仅 admin / `JWTOrCookie` 仅 WS | `JWTGin(secret)` / `JWTOrCookie(secret)` / `UserIDKey` / `UsernameKey` 常量 |
| `mattermost_cookie.go` | v0.7.4 鉴权核心；Redis `UserData:<userId>` STRING + companyId header + 30s LRU | `MattermostCookieResolve` / `ResolveCookieID` / `MMUserFromCtx` / `TeamIDFromCtx` / `MMCookieHeader` / `MMTeamHeader` / `UserDataKeyPrefix` / `MattermostUser` |
| `mattermost_cookie_test.go` | 鉴权白盒测试 14 case（hit / miss / LRU / companyId / 边界） | `ResetCookieCacheForTest` |
| `metrics.go` | OTel `im.auth.cookie_cache.{hit,miss,size}` 单例 instrument | `metrics()` 包内 helper |
| `metrics_test.go` | metric 注册 + observable gauge 回调测试 | — |

**禁止新增** 文件而不写配套 `_test.go`：testing rule 要求行覆盖率 100%。

---

## 3. SOP（Standard Operating Procedure）

写 / 改 middleware 必须按下序执行，**不允许跳步**：

```
1. Skill 加载（先）
   └─ Skill(skill="go-concurrency-patterns")  // 写 Go 必须

2. 设计阶段
   ├─ 列出影响的响应字段 / ctx key / header 名（写在 PR description）
   ├─ 列出受影响的 handler / helper / testutil 路径（grep 给出清单）
   └─ 决定 middleware 顺序（§6 ④）

3. 实现阶段（TDD: RED → GREEN → REFACTOR）
   ├─ 先写 _test.go 表驱动用例（成功 / Redis miss / 上下游 timeout / 空值 / cache hit）
   ├─ 再实现 middleware
   └─ go test -race ./internal/middleware/...  全绿

4. Sweep 阶段（C009 强制 — 不可跳）
   ├─ 改 helper：server/tests/integration/m4_harness_test.go
   ├─ 改 fixture：server/internal/testutil/*.go
   ├─ 改 testutil/httpexpect.go（若依赖响应 shape）
   └─ 所有调用方一次性同步（不允许"分两个 PR 修"）

5. 注册阶段
   └─ server/cmd/im-server/main.go::buildRouter
      或 server/cmd/gateway/main.go::buildEngine
      （新增 middleware 必须在唯一入口注册，禁止散落各 handler 内调用）

6. 验证阶段
   ├─ make verify-build   // go build 全绿
   ├─ make verify-unit    // go test -race -cover ./... 全绿，行覆盖率 100%
   ├─ make verify-integration > /tmp/run.log 2>&1   // 输出 full log，禁止 tail
   └─ grep -cE '^--- FAIL:' /tmp/run.log == 0
```

**SOP 4 → 5 之间禁止 commit**：sweep 不完整 commit 会让 helper 在主干红，所有下游
agent / reviewer 都中招。

---

## 4. Pre-commit 自检清单

提交前**逐条**核对，缺一条不许 commit：

### 4.1 鉴权类改动

- [ ] grep 验证鉴权 middleware 都 set `UserIDKey`：
  ```bash
  grep -rEn 'c\.Set\("user_id"|c\.Set\(UserIDKey' server/internal/middleware/ --include='*.go'
  ```
  预期：JWT / cookie / WS 三处都命中；无新增 middleware 漏 set。

- [ ] grep 老 HASH "User" 残留（C010 §4.1 ①）：
  ```bash
  grep -rEn 'HGet\([^)]*"User"' server/internal/ --include='*.go' | grep -v _test.go
  ```
  预期 0 条。

- [ ] grep 老 `ResolvedTeamID` / `MMUserHashKey` 残留：
  ```bash
  grep -rEn '\.ResolvedTeamID\(\)|\bMMUserHashKey\b' server/internal/ --include='*.go'
  ```
  预期 0 条非测试匹配。

- [ ] grep 自创 team header：
  ```bash
  grep -rEn 'GetHeader\("(Company-Id|X-Team-Id|X-Company-Id)"\)' server/internal/ --include='*.go'
  ```
  预期 0 条。必须用 `middleware.MMTeamHeader` 常量。

### 4.2 wrap / 响应类改动

- [ ] handler 无双层 wrap（C007 §4.1）：
  ```bash
  grep -rEn 'gin\.H\{[^}]*"status"\s*:\s*"(success|error)"' \
    server/internal/http/ --include='*.go' \
    | grep -v _test.go | grep -v response_envelope.go
  ```
  预期 0 条。

- [ ] 加 / 改 wrap middleware 后必须跑：
  ```bash
  go test -race ./internal/http/response_envelope_test.go
  ```
  envelope 单测保持 100% 覆盖。

### 4.3 模块单测

- [ ] `go test -race -cover ./internal/middleware/...` 行覆盖率必须 100%（见 §6 约束）。
- [ ] `metrics_test.go` 不漏新 instrument 的注册断言。

### 4.4 C009 sweep（所有 middleware 改动强制）

- [ ] grep 所有 helper / fixture 一致性：
  ```bash
  grep -rEn '\.Expect\(\)\.Status\([0-9]+\)\.JSON\(\)\.(Object|Array)\(\)' \
    server/tests/integration/m4_harness_test.go \
    server/internal/testutil/ \
    --include='*.go'
  ```
  预期 0 条 raw 解析（必须经 `successBody` / `successBodyArray` / `errorBody`）。

- [ ] 集成测试 full log 落 file 验证：
  ```bash
  go test -tags integration -timeout 30m -count=1 ./tests/integration/... \
    > /tmp/im-mw-$(date +%Y%m%d-%H%M).log 2>&1
  grep -cE '^--- FAIL:' /tmp/im-mw-*.log  # 必须 0
  ```
  **禁止用 `| tail -100` 看结果**（C009 §3 Run #1 教训）。

---

## 5. Commit 规范

遵循项目根 `CLAUDE.md §3` + 用户全局 `~/.claude/rules/common/git-workflow.md`，
本模块强制 scope 形态：

```
feat(middleware/<name>): <中文描述 ≤ 50 字>

<可选中文 body：解释 why；附 grep 验证命中数 / 测试覆盖率结果>
```

`<name>` 取值：
- `cookie` — `mattermost_cookie*.go` 改动
- `cookie-required` — `cookie_required*.go` 改动
- `jwt` — `jwt_gin*.go` 改动
- `metrics` — `metrics*.go` 改动
- `chain` — 整链顺序 / 注入点变化（影响多个文件）

### 示例

```
feat(middleware/cookie): 切 UserData:<id> STRING + companyId header

v0.7.4 鉴权改造：废弃 HGet "User" HASH，改 GET UserData:<userId>
STRING；team_id 改从 companyId header 取，不再解析 organizes[]。
LRU cache 30s × 10k 不变，hit 率监控 im.auth.cookie_cache.hit。

验证: go test -race ./internal/middleware/... 14/14 PASS，行覆盖率 100%
harness 复核: C010 §4.1 全部 6 条 grep 命中数 0
```

```
fix(middleware/chain): 修复 envelope 在 CORS 之前导致预检 200 双 wrap

middleware 顺序错位（envelope → cors）让 OPTIONS 预检 200 也被 wrap，
浏览器 preflight 解析失败。改顺序：cors → envelope。
```

**Type 取值集合**：feat / fix / refactor / perf / docs / test / chore / ci / style / revert（同根）。

---

## 6. 约束规范（绝对铁律）

### ① C009 — 加 / 改 middleware 必须同步 sweep 所有 test helper

教训：Run #1 ~120 fail。任何会改变响应 shape / ctx key 的 middleware 改动必须：

```bash
grep -rEn 'engine\.Use|r\.Use|authed\.Use' server/cmd/ server/internal/http/router.go
```

每个 `Use(...)` 之后涉及的 helper / fixture 全扫一遍。不允许"等下个 PR 再修测试"。

### ② C007 — `responseEnvelope` 中间件已生效，handler 禁止再 wrap

- handler 只能 `c.JSON(code, businessData)` 或 `c.AbortWithStatusJSON(40x/5xx, gin.H{"error": "..."})`
- **禁止**在 handler 内写 `gin.H{"status": "success", "data": ...}`
- **禁止**错误路径用 `c.JSON(40x, ...)` 不 abort
- 跳过路径白名单（`shouldSkipEnvelope`）：`/healthz` / `/readyz` / `/metrics`

### ③ C010 — v0.7.4 鉴权 = Redis `UserData:<userId>` STRING + companyId header

- header 名锁定：`MMCookieHeader = "cookieId"` / `MMTeamHeader = "companyId"`
- Redis key 锁定：`UserDataKeyPrefix = "UserData:"`，用 `GET`（非 HGet）
- `MattermostUser` struct 只保留 `ID / Mobile / Name / UserName / UserID`（5 个），
  禁止再加 `CompanyID / OrgID / DeptID` 等字段
- `TeamIDFromCtx(c)` 必须从 `companyId` header 取，**不**从 Redis payload 取
- 本地 dev 启动必须 `IM_REDIS_CLUSTER=true`（pre Redis 实际是 cluster；
  详见 `harness/C010 §4.6`）

### ④ middleware 顺序铁律（注册顺序 = 执行顺序）

唯一注册位置：`server/cmd/im-server/main.go` 或 `cmd/gateway/main.go`。
顺序锁定如下，**改动需用户拍板**：

```
metrics  →  recover  →  cors  →  auth  →  envelope  →  handler
   ↑         ↑          ↑       ↑          ↑              ↑
观测最外     兜底 panic   CORS    鉴权     响应包裹      业务
```

为什么这个顺序：

- `metrics` 最外：要量准请求总时延（含其他 middleware 自身耗时）
- `recover` 第二：panic 必须被兜，但要让 metrics 仍记录失败
- `cors` 第三：preflight `OPTIONS` 走到这就 short-circuit，省后续鉴权
- `auth` 第四：鉴权失败 401 必须在 envelope 之前 abort（abort 后 envelope 仍 wrap，但 handler 不跑）
- `envelope` 第五：handler 之前 register response interceptor，handler 之后统一 wrap
- `handler`：业务

**禁止**：
- ❌ envelope 在 cors 之前（OPTIONS 预检会被 wrap → 浏览器 preflight 失败）
- ❌ auth 在 cors 之前（OPTIONS 也走鉴权 → 401，preflight 失败）
- ❌ metrics 在 recover 之后（panic 不会被 metrics 计入）

### ⑤ Skill 强制加载

写 / 改本模块任意 `.go` 文件**之前**：

```
Skill(skill="go-concurrency-patterns")
```

LRU 缓存是共享状态（`sync.Once` + `sync.Mutex` 内部）、`MattermostCookieResolve` 是
HTTP middleware 实质上的并发热路径，并发约束必须遵守。

### ⑥ 文件 / 函数尺寸

继承用户全局 `~/.claude/rules/golang/coding-style.md`：

- 单文件 ≤ 400 行（`mattermost_cookie.go` 280 行接近上限；继续加内容必须先按职能拆）
- 单函数 ≤ 60 行
- 嵌套深度 ≤ 4 层

---

## 7. 对应 Harness 映射

| harness | 触发场景（在本模块内） | 验证手段 |
|---|---|---|
| **C007** | 改 `responseEnvelope` / 新增 wrap middleware / 改 `shouldSkipEnvelope` | §4.1 4 条 grep + `internal/http/response_envelope_test.go` 8 case |
| **C009** | 任何 `engine.Use(...)` 或 middleware 内响应 shape / ctx key 变化 | §4.1 2 条 grep + `make verify-integration` full log + FAIL 数 == 0 |
| **C010** | 改 `mattermost_cookie.go` / `cookie_required.go` / `MattermostUser` 字段 / Redis key prefix / header 名 | §4.1 6 条 grep + `mattermost_cookie_test.go` 8 case + `cookie_required_test.go` 2 case + `IM_REDIS_CLUSTER=true` ≥ 4 条命中 |

**双向触发**：本模块任何改动**必须**先打开上述三条 harness 全文阅读 §1 Trigger + §3
Required + §4 Verification，再动手。改完按 §4 自检清单逐条 grep。

---

## 8. Update / Insert 规则

### 新增 middleware（如未来加 `RateLimit` / `RequestID` / `TraceLog`）

强制完成下列 8 步，**缺一即不许 commit**：

1. **写实现** — `internal/middleware/<name>.go`，单文件 ≤ 400 行
2. **写单测** — `internal/middleware/<name>_test.go`，行覆盖率 100%
3. **注册位置** — `server/cmd/im-server/main.go::buildRouter` 加入 middleware chain
   （按 §6 ④ 顺序铁律放正确位置）
4. **同步 testutil/httpexpect.go** — 若新 middleware 影响响应 shape / 注入 header，
   集成测试 fixture 必须能复现真实生产 header
5. **同步 `tests/integration/m4_harness_test.go`** — `seedDM` / `seedGroup` / `successBody`
   等 helper 都要确认响应解析正确（C009 §3 sweep）
6. **harness 复核** — 若改动属于 C007 / C009 / C010 触发场景，按 §4 自检清单全过
7. **更新本 CLAUDE.md** — §2 表格加新文件、§6 加新铁律（如果有）、§7 加新 harness 映射
8. **commit + tag** — 按 §5 commit；若是多 Phase 大改的一部分，按项目根 `CLAUDE.md §3.1`
   打 `v<base>-phase<N>-middleware-<slug>` tag

### 修改现有 middleware

按 §3 SOP 全流程跑，重点关注：

- 改 `MattermostUser` 字段 / `MMCookieHeader` 常量值 → C010 触发
- 改 `responseEnvelope` shape / 跳过路径 → C007 触发
- 改 ctx key 名（`UserIDKey` / `mmUserCtxKey` / `teamIDCtxKey`）→ **全局 grep 替换 + sweep handler 所有调用方**
- 改 LRU TTL / 容量 → 同步改 `metrics.go` 的描述文案 + 灌库脚本（C010 §4.6）

### 删除 middleware

- 删除 `JWTGin` / `JWTOrCookie` 前**必须**验证 admin 路径 / WS 握手已迁移
- 删除前 grep 调用方：
  ```bash
  grep -rEn 'JWTGin\(|JWTOrCookie\(' server/ --include='*.go'
  ```
  预期 0 条（或仅注释 / 测试遗留）才允许删

---

## 9. 文档关联

| 文档 | 用途 |
|---|---|
| `/Users/mac28/workspace/golangProject/im/CLAUDE.md` | 项目根总指令；§1.6 项目特有约束、§8 harness 索引 |
| `server/docs/BACKEND.md` | M1–M6 详细契约；§3.3 /api/sync、§4 AllocSeqAndInsert、§十一 OTel 探针 |
| `docs/harness/C007-response-envelope-no-double-wrap.md` | 全局 envelope 中间件已生效，handler 禁止再 wrap |
| `docs/harness/C009-test-helper-tracks-middleware-shape.md` | 加 / 改 middleware 必须 sweep 所有 test helper |
| `docs/harness/C010-userdata-resolve.md` | v0.7.4 鉴权：UserData STRING + companyId header |
| `~/.claude/skills/go-concurrency-patterns/SKILL.md` | 写 Go 并发的唯一标准（写本模块前强制加载） |
| `~/.claude/rules/golang/coding-style.md` | 单文件 ≤ 400 行 / 函数 ≤ 60 行 / 4 层嵌套 |
| `~/.claude/rules/golang/testing.md` | 行覆盖率 100% / 集成测试每路由 1 成功 + 1 失败 |
| `~/.claude/rules/golang/security.md` | Secret / context timeout / gosec 扫描 |
| `server/Makefile` | `verify-all` / `verify-build` / `verify-unit` / `verify-integration` |
| `server/cmd/im-server/main.go` | middleware 注册唯一入口（生产）|
| `server/tests/integration/m4_harness_test.go` | `seed*` helper + `successBody` unwrap，C009 sweep 必经路径 |
| `server/internal/testutil/cookie_fixture.go` | 测试 fixture：v0.7.4 cookieId == userId + companyId |

---

## 10. 会话开局快速自检（middleware 改动专用）

```bash
cd /Users/mac28/workspace/golangProject/im
git status && git log --oneline -5
ls server/internal/middleware/
cat docs/harness/C007-response-envelope-no-double-wrap.md | head -40
cat docs/harness/C009-test-helper-tracks-middleware-shape.md | head -40
cat docs/harness/C010-userdata-resolve.md | head -40
grep -rEn 'engine\.Use|r\.Use|authed\.Use' server/cmd/ server/internal/http/router.go
```

然后调用 `Skill(skill="go-concurrency-patterns")`，再动手。

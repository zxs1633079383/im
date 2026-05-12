# C010 — v0.7.4 鉴权：Redis `UserData:<userId>` STRING + companyId header

```yaml
---
id: C010
title: v0.7.4 鉴权改造（UserData STRING + cookieId == userId + companyId header）
status: active
created: 2026-05-12
last_recurred: 2026-05-12
recurrence_count: 0
source_logs: []
applies_to:
  - server/internal/middleware/mattermost_cookie*.go
  - server/internal/middleware/cookie_required*.go
  - server/internal/gateway/ws_handler.go
  - server/internal/gateway/ws_auth*.go
  - server/internal/testutil/cookie_fixture.go
  - server/scripts/seed-mm-cookies*.sh
inline_target: ~/.claude/rules/golang/security.md  # 晋升后 inline 到 §Secret Management
---
```

## 1. 触发场景（Trigger）

任何下列代码 / 文档 / 脚本改动都必须按本 harness 约束：

- `server/internal/middleware/mattermost_cookie.go` —— `MMCookieHeader` / `MMTeamHeader` /
  `UserDataKeyPrefix` 常量 / `MattermostUser` struct / `lookupMattermostUser` 函数
- `server/internal/gateway/ws_handler.go::authenticate()` —— WS 握手鉴权
- `server/internal/testutil/cookie_fixture.go` —— 测试 fixture helper
- `server/scripts/seed-mm-cookies*.sh` —— 灌库脚本（dev / pre 联调用）
- 关键词：`HGet.*User\b` / `HSET User` / `MMUserHashKey` / `MattermostUser{.*CompanyID` /
  `ResolvedTeamID` —— 这些都是 v0.7.3 以前的老 API，v0.7.4 全部废弃

## 2. 错误模式（Anti-Pattern）

```go
// ❌ 错误 ①：HGet 老 HASH "User"（v0.7.4 已经改用 STRING UserData:<userId>）
raw, _ := rdb.HGet(ctx, "User", fmt.Sprintf("%q", cookieID)).Bytes()

// ❌ 错误 ②：从 Redis payload 取 companyId / orgId 当 team_id
//           （v0.7.4 organizes[] 是多 org 数组，im 不解析；team_id 改从 header 取）
mm := middleware.MMUserFromCtx(c)
teamID := mm.CompanyID                  // 字段已删
teamID := mm.ResolvedTeamID()           // 方法已删
teamID := mm.Organizes[0].CompanyID     // im 故意不暴露 organizes

// ❌ 错误 ③：测试 fixture 仍用 cookieID != userID 的双 id
testutil.CookieFixture(t, rdb, "69eec6dbe6876865ff98945a",  // cookie
    "676cc4ccfbbc501161d5cd65", ...)                          // userId — 与 cookieId 不等
// v0.7.4 要求 cookieID == userID；helper 会 t.Fatal 提示

// ❌ 错误 ④：seed-mm-cookies.sh 仍写 HSET User "\"<id>\"" <json>
redis-cli HSET User "\"676cc4...\"" '{"id":"...","userId":"...",...}'
```

**后果**：
- 错误 ① / ④：cses Java 切到 `SET UserData:<id>` 后，im 走 HGet 全部 miss → 全员 401
- 错误 ②：上线后所有依赖 `TeamIDFromCtx` 的写路径（messages.team_id / channels.team_id /
  approvals.team_id ...）都拿到空串 → 数据无 team 维度 → 跨 team 检索失败
- 错误 ③：单测在 v0.7.4 helper 上 t.Fatal，无声压死 CI
- 安全考虑：cookieId == userId 后**任何人知道你的 userId 就能伪装**，cses Java 必须在
  Redis 写入时通过其他通道（session token / 一次性 mTLS handshake）确保 userId 不可枚举

## 3. 正确做法（Required）

**首选 A**（v0.7.4 鉴权流程）：

```go
// service / handler 拿当前用户：
mm := middleware.MMUserFromCtx(c)           // 含 ID / Mobile / Name / UserName 4 字段
uid := mm.ResolvedUserID()                  // 优先 UserID，fallback ID（新 wire UserID 常为空）

// 拿当前 team_id（v0.7.4 从 header 取）：
teamID := middleware.TeamIDFromCtx(c)       // c.GetHeader("companyId") 直接派生
```

**Redis lookup（仅 middleware 内部）**：

```go
// internal/middleware/mattermost_cookie.go::lookupMattermostUser
key := middleware.UserDataKeyPrefix + cookieID  // "UserData:" + userId
raw, err := rdb.Get(ctx, key).Bytes()
// JSON unmarshal 仅取 id / mobile / name / userName 字段；organizes[] 故意 ignore
```

**测试 fixture**：

```go
cookie := testutil.CookieFixture(t, rdb,
    testutil.RealCookieID,        // == RealUserID == "676cc4ccfbbc501161d5cd65"
    testutil.RealUserID,
    testutil.RealCompanyID)

expect.GET("/api/me").
    WithHeader(middleware.MMCookieHeader, cookie).         // "cookieId"
    WithHeader(middleware.MMTeamHeader, testutil.RealCompanyID).  // "companyId"
    Expect().Status(200)
```

**绝对禁止 C**：
- ❌ `rdb.HGet(ctx, "User", ...)` —— v0.7.4 后 HASH 已废
- ❌ `mm.CompanyID` / `mm.OrgID` / `mm.DeptID` —— 字段已从 MattermostUser 删除
- ❌ `mm.ResolvedTeamID()` —— 方法已删除
- ❌ 绕过 `TeamIDFromCtx`，handler 内 `c.GetHeader("Company-Id")` / `c.GetHeader("X-Team-Id")` 等
  自创 header 名 —— 必须用 `middleware.MMTeamHeader` 常量统一

**实施约束**：
- header 名锁定 `cookieId` + `companyId`（大小写不敏感），用常量 `MMCookieHeader` / `MMTeamHeader`
- Redis key 锁定 `UserData:<userId>` 形态，用常量 `UserDataKeyPrefix`
- `MattermostUser` struct 只保留 4 个顶层字段（id / mobile / name / userName + 兼容用 userId）
- LRU cache 30s TTL × 10k capacity（与 v0.7.3 一致，仅 Redis 命令变化）

## 4. 检查方法（Verification）

### 4.1 自动 grep（必须返回 0 条非测试匹配）

```bash
# ① HGet User 老调用残留：
grep -rEn 'HGet\([^)]*"User"' server/internal/ --include='*.go' | grep -v "_test.go"

# ② 老 MMUserHashKey 常量残留：
grep -rEn '\bMMUserHashKey\b' server/internal/ --include='*.go' | grep -v "_test.go"

# ③ MattermostUser 老字段残留：
grep -rEn '\bMattermostUser\b.*\{.*\b(CompanyID|OrgID|OrgName|DeptID|DeptName|OrgRole|Roles)\b' \
    server/internal/ --include='*.go'

# ④ ResolvedTeamID 老方法残留：
grep -rEn '\.ResolvedTeamID\(\)' server/internal/ --include='*.go'

# ⑤ HSET User 灌库脚本残留：
grep -rEn 'HSET\s+User\s+' server/scripts/

# ⑥ 自创 team header 名残留（必须用 MMTeamHeader 常量）：
grep -rEn 'GetHeader\("(Company-Id|X-Team-Id|X-Company-Id)"\)' server/internal/ --include='*.go'
```

### 4.2 CI Gate

- `make verify-build` —— `go build ./...` 必须 exit 0
- `make verify-unit` —— `go test -race ./internal/middleware/... ./internal/gateway/...` 必须全绿
- 上线前 grep §4.1 全部 6 条命中数 == 0

### 4.3 单测（白盒）

- `internal/middleware/mattermost_cookie_test.go`
  - `TestLookupMattermostUser_Hit` —— GET UserData:<id> 走通 + 字段解析
  - `TestLookupMattermostUser_EmptyValue` —— 空值视为 miss
  - `TestMattermostCookieAuth_CompanyHeader` —— companyId header → TeamIDFromCtx
  - `TestMattermostCookieAuth_CompanyHeaderEmpty` —— 缺 header → empty team
  - `TestMattermostCookieAuth_CompanyHeaderWithoutCookieStillStamps` —— 边界
  - `TestMattermostCookieAuth_LRUCacheHit` —— 5 次请求 Redis 命中 1 次
  - `TestResolveCookieID_ExternalEntry` —— ws_handler 复用同一缓存
  - `TestResolvedUserID_PrefersExplicitField` —— UserID first / ID fallback / nil safe
- `internal/middleware/cookie_required_test.go`
  - `TestCookieRequired_PassesWhenResolverInjected` —— 完整鉴权链路
  - `TestCookieRequired_RejectsInvalidCookie` —— Redis miss → 401
- `internal/gateway/ws_auth_test.go`
  - `TestWsAuth_CookieHeader_Resolves` —— WS 握手走 GET UserData
  - `TestWsAuth_StaleCookie_Refused` —— stale cookie 拒绝降级到 JWT

### 4.4 集成测试

- `tests/integration/` 198 case 全部经 `testutil.CookieFixture` helper；helper 内部已
  switch 到 v0.7.4 形态，**调用方零改动**只要追加 `WithHeader(middleware.MMTeamHeader,
  RealCompanyID)` 即可补 team 上下文

### 4.5 手工 smoke

```bash
# pre 集群灌张立超 cookie（v0.7.4 形态）
IM_REDIS=<pre-redis> server/scripts/seed-mm-cookies.sh

# 验 cookieId + companyId 走通
curl -H 'cookieId: 676cc4ccfbbc501161d5cd65' \
     -H 'companyId: 6111fb0a202d425d221c53db' \
     http://192.168.6.41:30880/api/me
# 期望：200 envelope-wrapped {"id":"676cc4...","mobile":"...","name":"张立超",...}

# 缺 companyId header：仍能拿身份，但 TeamIDFromCtx 返 ""（write 路径 team_id NULL）
curl -H 'cookieId: 676cc4ccfbbc501161d5cd65' \
     http://192.168.6.41:30880/api/me
# 期望：200 + 同样 body；后续 POST /channels 写入的 channel.team_id 为 NULL
```

## 5. 复现历史（Recurrence Log）

| #  | 日期       | 触发场景 | 引用日志 | 处置 |
|----|------------|----------|----------|------|
| 1  | 2026-05-12 | v0.7.4 改造首次落地：cses Java 改 UserData:<id> STRING；im middleware/gateway/testutil/scripts 同步切换 | logs/2026-05-12.json | 9 个 commit + 单测 14 case 全绿；harness C010 立条 |

## 6. 反例与边界（Don't Over-Apply）

- **WS 握手仍接受 `?token=` JWT 老路径** —— 仅 `/api/admin/*` 业务保留，业务路由禁用；JWT
  路径与 cookieId 互斥（cookieId 优先），不被本 harness 约束
- **TeamIDFromCtx 返空串不是错误** —— 公开池 / 个人 DM / 非 org 用户都可能空；handlers
  写 PG 时 `nullIfEmpty(teamID)` 让 GORM 落 NULL
- **`MattermostUser.UserID` 字段保留** —— v0.7.4 新 wire 把 id 写到 `id` 顶层，`userId` 字段
  常为空；但保留字段保 source-compat（旧 fixture / 老 wire 仍可解析）
- **测试 helper `MakeCookieID` / `MakeUserID` 都返回同一函数** —— v0.7.4 collapsed；调用方
  改 `MakeCookieID(seed)` 二参传同值即可，不需要重写测试

## 7. 升级 / 弃用条件（Lifecycle）

**晋升（active → merged）**：
- 连续 30 天无复现（§5 表稳定）
- §4.1 全部 6 条 grep CI gate 接管
- 把核心规则 inline 进 `~/.claude/rules/golang/security.md §鉴权` 节
- 本文件 frontmatter `status: merged`，`inline_target` 指向 inline 锚点

**弃用（active → deprecated）**：
- v1.0+ 鉴权模型再换代（如改 OAuth2 / mTLS / passwordless WebAuthn）
- cookieId == userId 模型回退到独立 session token（不太可能）
- frontmatter `status: deprecated`，保留文件作为历史索引

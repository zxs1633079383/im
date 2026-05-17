# CLAUDE.md — server/internal/auth/ 模块级指令（鉴权工具函数层）

> 本文件仅约束 `server/internal/auth/` 下三个工具文件（jwt.go / password.go / userid.go）。
> 优先级：用户全局 `~/.claude/CLAUDE.md` > 项目根 `/Users/mac28/workspace/golangProject/im/CLAUDE.md` > `server/CLAUDE.md` > **本文件** > 默认行为。
> 与上层冲突时遵循「更具体者优先」：本层加严不放宽（如本层禁止状态字段、禁止 DB 依赖）。

---

## 0. 模块定位

**是什么**：im 鉴权链路上的**纯工具函数库**——三件套：

| 子能力 | 文件 | 一句话 |
|---|---|---|
| JWT 签发 / 校验（HS256） | `jwt.go` | `GenerateToken` + `ValidateToken` + `Claims` |
| 密码 hash / 校验（bcrypt cost=12） | `password.go` | `HashPassword` + `CheckPassword` |
| userId 形态校验（24-char lowercase hex） | `userid.go` | `ValidateUserID` + `MustUserID` + `ErrInvalidUserID` + `UserIDLen` |

**只负责**：
- ✅ 把入参（secret / plaintext / userId 字串）做**无副作用变换**或**校验**
- ✅ 返回 `(value, error)`，错误统一 `fmt.Errorf("...: %w", err)` 包链
- ✅ 100% 单测覆盖（包括错误分支、边界长度、空串、非 hex 字符）

**不负责**（**绝对禁止**在本层引入）：
- ❌ session 存储 / 取数 / Redis 调用 —— 落在 `internal/middleware/cookie_required.go` + `mattermost_cookie.go`（见 C010）
- ❌ DB / GORM / 任何 `*sql.DB` 或 `*gorm.DB` import
- ❌ HTTP / gin / `*gin.Context` 触碰 —— 工具函数不知道 HTTP 存在
- ❌ goroutine / channel / `sync.Mutex` —— 工具函数应**纯函数**、无状态、无并发原语
- ❌ `context.Context` 参数 —— 三个文件全部是 CPU-only 工具，**显式豁免** ctx 首参规则（见项目根 §1.4 例外）
- ❌ 日志 / OTel span —— 调用方在 middleware 层埋点

> 一句话边界：**给我一个字符串，我告诉你它是否合法 / 转换后的值。我不知道用户是谁、连接在哪、Redis 里有什么。**

---

## 1. 影响范围

### 上游（谁依赖我）

| 调用方 | 用我的什么 | 备注 |
|---|---|---|
| `server/internal/middleware/cookie_required.go` | `ValidateUserID` | 校验 cookieId / userId 形态合法（24-hex） |
| `server/internal/middleware/mattermost_cookie.go` | `ValidateUserID` | 解析 `UserData:<userId>` payload 中的 id 字段时复用 |
| `server/internal/middleware/jwt_gin.go`（如启用） | `ValidateToken` + `Claims` | `/api/admin/*` 路径鉴权（业务路由禁 JWT） |
| `server/internal/gateway/ws_auth*.go` | `ValidateToken` | WS 握手 `?token=` 老路径（C010 §6 反例） |
| `cmd/v4-client/main.go` | `GenerateToken` | 本地联调工具签 dev JWT |
| 未来潜在：`internal/handler/admin_*.go` | `HashPassword` / `CheckPassword` | 管理员账号建立 / 改密（当前业务为 cookieId 模型，password 路径未上 prod） |

### 下游（我依赖谁）

| 依赖 | 来源 | 用途 |
|---|---|---|
| `github.com/golang-jwt/jwt/v5` | `go.mod` v5.3.1 | JWT 签发 / 解析 / HS256 |
| `golang.org/x/crypto/bcrypt` | `go.mod` | 密码 hash，cost 12 |
| 标准库 `errors` / `fmt` / `time` | — | 错误包装、过期时间 |

**强约束**：本层**不依赖** Redis / DB / gin / context；任何 PR 引入这些 import → reviewer 必须打回。

---

## 2. 功能模块清单

| 文件 | 行数 | 公开符号 | 职责 | 关键常量 |
|---|---|---|---|---|
| `jwt.go` | 56 | `Claims` / `GenerateToken` / `ValidateToken` | HS256 JWT 签发 + 校验；7 天过期 | `tokenExpiry = 7 * 24h`（unexported） |
| `password.go` | 27 | `HashPassword` / `CheckPassword` | bcrypt hash + 比对 | `bcryptCost = 12`（unexported） |
| `userid.go` | 53 | `UserIDLen` / `ErrInvalidUserID` / `ValidateUserID` / `MustUserID` | 24-char lowercase hex 形态校验 | `UserIDLen = 24` |
| `jwt_test.go` | — | — | 白盒：签发 / 校验 / 错签名算法 / 空 secret / 解析失败 / claim 不匹配 | — |
| `password_test.go` | — | — | 白盒：hash + verify / 空串 / 错密码 / hash 形态 | — |
| `userid_test.go` | — | — | 白盒：合法 / 长度错 / 大写 / 非 hex / 空串 / `MustUserID` panic | — |

**符号细节**：

- `Claims.UserID` —— Mattermost 24-char hex（与 `UserIDLen` 一致）；wire 字段名 `uid`
- `Claims.Username` —— 可选展示名；wire 字段名 `username`
- `Claims.RegisteredClaims` —— jwt v5 标准字段，签发时仅填 `IssuedAt` + `ExpiresAt`
- `ValidateToken` 强制校验 `t.Method.(*jwt.SigningMethodHMAC)`——**只接受 HMAC 系列**，RSA/ECDSA token 一律拒
- `ValidateUserID` —— allocation-free，O(n)；调用方放心放在 hot-path（每个 HTTP handler 入口）

---

## 3. SOP — 改 auth/ 三件套的标准工作流

```
0. 开局：cat SESSION.md | head -80 && ls docs/harness/
1. 写代码前：Skill(skill="go-concurrency-patterns")  ← 项目根 §1 强制（哪怕本层无并发也走一遍 mindset）
2. TDD RED：先在 jwt_test.go / password_test.go / userid_test.go 写新用例 → go test ./internal/auth/... 看红
3. 写实现纯函数 → 单测 GREEN
4. 覆盖率核查：go test -cover ./internal/auth/... → 必须 100.0%
5. race 验证：go test -race ./internal/auth/... → 全绿（应秒过，因为无并发）
6. 调用方接入：middleware / gateway 层直接 import；不要在本层做"假调用方"
7. 集成测试：跑一遍 server/tests/integration/ 里的鉴权链路（TestCookieRequired_* / TestWsAuth_*）
8. make verify-all 全绿
9. commit（见 §5）
```

**特殊路径**：

- 只改注释 / godoc → 跳过 §4–§7，直接 `make verify-build` 即可
- 改 `bcryptCost` 常量 → 必须同步：①团队 RFC 拍板 ②集成测试基准 ③`password_test.go` 显式断言 ④harness 记录
- 改 `tokenExpiry` → 必须同步前端持久化策略 + 集成测试 + 文档
- 新增鉴权方式（SSO / OAuth / WebAuthn） → 走 §8.2 流程，**不允许**塞进现有三个文件

---

## 4. Pre-commit 自检清单

### 4.1 必跑命令（按顺序）

```bash
cd /Users/mac28/workspace/golangProject/im/server

# ① race + 短测：本层应秒过
go test -race ./internal/auth/...

# ② 覆盖率：必须 100%
go test -cover ./internal/auth/... | grep -E "coverage:\s*100\.0%" || { echo "auth/ 覆盖率不足"; exit 1; }

# ③ vet：零告警
go vet ./internal/auth/...

# ④ 全量验证（V1 + V2）
make verify-all

# ⑤ 鉴权链路集成测试
make verify-integration  # 跑 TestCookieRequired_* / TestWsAuth_* / TestM*Auth*
```

### 4.2 Grep gate（结果必须 0 条 / 命中数符合期望）

```bash
# ① bcrypt cost 与团队约定一致（当前 12）：
grep -n "bcrypt.DefaultCost\|bcrypt.MinCost\|bcrypt.MaxCost" server/internal/auth/ \
  | grep -v "_test.go" | wc -l
# 期望：0（必须显式用 bcryptCost = 12 常量，不许走 lib 默认）

grep -nE "bcryptCost\s*=\s*12" server/internal/auth/password.go | wc -l
# 期望：1（且仅 1）

# ② JWT 签发算法锁定 HS256：
grep -n "jwt.SigningMethod" server/internal/auth/jwt.go | grep -v "_test.go"
# 期望：唯一一行 jwt.SigningMethodHS256（GenerateToken 内）

# ③ 禁用 md5 / sha1 / sha256 做密码 hash：
grep -rnE 'md5\.|sha1\.|sha256\.Sum256.*password' server/internal/auth/ | grep -v "_test.go"
# 期望：0（密码 hash 必须 bcrypt）

# ④ JWT secret 必须从配置 / 入参注入，禁硬编码：
grep -rnE '"[A-Za-z0-9_\-]{16,}"' server/internal/auth/jwt.go
# 期望：0（除 godoc 注释外，无任何形如密钥的字面量）

# ⑤ 本层禁 import gin / sql / gorm / redis / context（context 在 v5 RegisteredClaims 里间接出现可豁免）：
grep -rnE '"github.com/gin-gonic|database/sql|gorm.io/|go-redis"' server/internal/auth/
# 期望：0

# ⑥ 本层禁裸 goroutine / channel / mutex（纯函数，无状态）：
grep -rnE '\bgo\s+func|\bchan\s|sync\.Mutex|sync\.RWMutex' server/internal/auth/ | grep -v "_test.go"
# 期望：0
```

### 4.3 失败处置

- 覆盖率 < 100% → 补错误分支测试；本层**不接受**任何覆盖率豁免（不属于 `~/.claude/rules/golang/testing.md §允许豁免` 的 cmd / 平台 stub / 生成代码三类）
- `verify-integration` 红 → 检查是不是 middleware/cookie_required 联动改动忘了 sweep（C009）+ 是不是 Redis cluster 模式（C010 §4.6）
- grep gate ②（HS256 唯一性）失败 → **立即停下**审视 RFC，禁止单方面加 RS256 / ES256 等异构算法

---

## 5. Commit 规范

沿用项目根 §3 + `~/.claude/rules/common/git-workflow.md`：

```
feat(auth): <中文描述 ≤ 50 字>

<可选中文 body：why；每行 ≤ 72 字符>
```

### 5.1 scope 取值

| scope | 适用 |
|---|---|
| `auth` | 三件套通用改动 |
| `auth/jwt` | 仅 jwt.go / jwt_test.go |
| `auth/password` | 仅 password.go / password_test.go |
| `auth/userid` | 仅 userid.go / userid_test.go |

### 5.2 示例

```
feat(auth/jwt): GenerateToken 加 iss / aud 字段防跨服务复用

签发时填 RegisteredClaims.Issuer = "im-gateway"，
ValidateToken 校验 aud 必须等于 "im-client"。
防止 dev JWT 在 prod 被复用。
```

```
fix(auth/userid): ValidateUserID 漏判前导空白导致 cookieId trim 前已通过

补长度判前先 strings.TrimSpace 一次（已通过 26 case 表驱动）。
```

```
test(auth/password): 补 bcrypt cost 12 显式断言 + benchmark 防退化
```

### 5.3 禁止项

- ❌ `update auth` / `wip auth` / `chore: misc`
- ❌ 一个 commit 同时改 jwt + password + userid（拆 3 个 commit）
- ❌ commit body 写"AI 生成 / Co-authored-by: Claude"
- ❌ `--no-verify` 跳 hook（除非用户明示）

---

## 6. 约束规范（本层强约束）

### 6.1 鉴权全链路定位（与 C010 协作边界）

| 层 | 在哪 | 干什么 | 与本层关系 |
|---|---|---|---|
| 工具层（**本层**） | `internal/auth/` | JWT 签 / 验 / 密码 hash / userId 形态校验 | 纯函数，无状态 |
| Middleware 层 | `internal/middleware/cookie_required.go` + `mattermost_cookie.go` | cookieId → Redis `UserData:<userId>` → `MattermostUser` 注入 ctx；companyId header → `TeamIDFromCtx` | **session 状态归这里**，复用本层 `ValidateUserID` |
| WS 握手 | `internal/gateway/ws_auth*.go` | cookieId 优先 / `?token=` JWT fallback（仅 admin） | 复用本层 `ValidateToken` + middleware 的 Redis lookup |

**铁律**：本层**不知道** cookieId / Redis / companyId 的存在；任何"我需要查 Redis 验证一下" / "我需要看 companyId" 的需求 → **去 middleware 层加**，不许下沉到本层。

### 6.2 密码 hash 算法锁定

- ✅ **必须** `bcrypt.GenerateFromPassword([]byte(plaintext), bcryptCost)`，`bcryptCost = 12`
- ❌ **禁止** md5 / sha1 / sha256（无 salt） / plain compare
- ❌ **禁止**走 `bcrypt.DefaultCost`（默认值会随 lib 升级飘移）
- 调整 cost 必须：①RFC 拍板 ②对旧 hash 走 rehash 升级路径 ③同步 benchmark（10ms ~ 250ms 范围内可接受）

### 6.3 JWT 签名算法锁定

- ✅ **唯一**签发：`jwt.SigningMethodHS256`
- ✅ 校验时强制 `t.Method.(*jwt.SigningMethodHMAC)` —— 拒 RSA / ECDSA / `alg: none` 攻击
- ❌ **禁止** RS256 / ES256 / EdDSA 等异构算法（除非走 RFC 拍板 + 同步 keys 管理）
- ❌ **禁止** `alg: none`（v5 lib 默认就拒，但 reviewer 仍要肉眼复查 PR）

### 6.4 secret 来源

- ✅ `GenerateToken(secret, ...)` / `ValidateToken(secret, ...)` —— secret 由**调用方**从 `internal/config` 读取并传入
- ❌ **禁止**在本层 `os.Getenv("JWT_SECRET")` —— 配置入口统一走 viper（见 `server/CLAUDE.md §6.2`）
- ❌ **禁止**写死 fallback secret（哪怕 `"dev-only"`）—— `GenerateToken` 必须在空 secret 时显式 `return "", error`
- ✅ 单测可用字面量 secret（如 `"test-secret"`）

### 6.5 工具函数纯度

- ✅ 三个文件全部**纯函数**（无 package-level mutable state）
- ✅ 入参全部 by-value（string / []byte）；返回 `(value, error)` pair
- ❌ **禁止**包级变量缓存（`var cache map[string]*Claims` 之类）—— 缓存归 middleware 层
- ❌ **禁止** init() 做任何动作（除常量编译 / 正则常量等"不可能失败"动作；本层目前无 init()，保持空）

### 6.6 userId 形态铁律（与 C012 协作）

- ✅ `UserIDLen = 24`，**lowercase hex**（0-9 / a-f）
- ✅ 外部系统传入大写 hex → 调用方（middleware）`strings.ToLower` 一次再过 `ValidateUserID`
- ❌ **禁止**接受大写 hex —— 错误信息明确（C010 §3 测试 fixture 已经验证）
- ❌ **禁止**改 `UserIDLen` 常量 —— Mattermost ObjectId 形态固化 24 字符，任何放宽（28 / 32）都是协议事故
- 注意：C012 把 `postId` / `channelId` 全链路改 string，但 **userId 仍是 24-hex**（不是雪花 ID）

---

## 7. 对应 Harness 映射

| Harness | 触发场景 | 验证手段 |
|---|---|---|
| [C010](../../../docs/harness/C010-userdata-resolve.md) | 改 JWT 签发逻辑 / 改 `Claims` wire 字段 / 改 `ValidateUserID` 行为 / WS 握手 fallback JWT 路径 | ①本层 unit test 100% ②middleware `TestMattermostCookieAuth_*` 全绿 ③`TestWsAuth_CookieHeader_Resolves` + `TestWsAuth_StaleCookie_Refused` 全绿 ④集成测试 `tests/integration/` 198 case 经 `testutil.CookieFixture` 全绿 |

**协作分工**（reviewer 必读）：

- 本层提供 `ValidateUserID(cookieID)` —— middleware 调用前先过形态
- middleware 提供 `lookupMattermostUser(ctx, rdb, cookieID)` —— 拿到 `UserData:<userId>` 后 JSON 解析
- WS 握手 `internal/gateway/ws_auth*.go` —— 复用同一缓存 + 同一形态校验
- 任何"鉴权改造"需求都先看 C010 §3 决定**该改本层还是 middleware**——这是常见踩坑点

---

## 8. Update / Insert 规则

### 8.1 改现有函数签名

不允许"偷偷"改返回类型 / 入参顺序：

1. 先在所有调用方 grep 一遍：`grep -rn "auth.GenerateToken\|auth.ValidateToken\|auth.HashPassword\|auth.CheckPassword\|auth.ValidateUserID\|auth.MustUserID" server/`
2. 同步改完所有调用点 + 新增覆盖测试
3. 单 commit 内完成（不留半切换状态）

### 8.2 新增鉴权方式（SSO / OAuth / WebAuthn）

**禁止直接塞进现有三个文件**。流程：

1. RFC：在 `docs/harness/C{NNN}-<auth-method>.md` 写 drafting harness，列触发场景 + reverse-anti-pattern
2. 新文件：`internal/auth/<method>.go`（如 `oauth.go` / `webauthn.go`）+ `_test.go` 配对
3. 单测覆盖率 100% —— **任何错误分支必须有用例**
4. middleware 接入点：在 `internal/middleware/` 加 `<method>_required.go`，**不**改本层
5. config 字段：在 `internal/config/` 加新 secret / endpoint / issuer 字段，由 middleware 注入
6. 集成测试：在 `server/tests/integration/` 加 `Test<Method>Auth_*` 至少 4 case（成功 / 错签 / 过期 / 缺凭据）
7. 文档：同步 `server/docs/BACKEND.md` 鉴权章节 + 本文件 §0 表格补一行
8. 完成→ harness drafting → active

### 8.3 升级 JWT lib（v5 → v6+）

- 跑 `go get -u github.com/golang-jwt/jwt/v5@<new>` → `go mod tidy`
- 重点验证 `RegisteredClaims` 形态 / `ParseWithClaims` keyfunc 签名是否变
- 全套 §4.1 命令必须绿
- harness C010 §6 反例核对一遍是否还有效

---

## 9. 文档关联

| 文档 | 在哪 | 用途 |
|---|---|---|
| 项目根 CLAUDE.md | `/Users/mac28/workspace/golangProject/im/CLAUDE.md` | im 项目全局约束 + Go 铁律 §1 |
| server 层 CLAUDE.md | `/Users/mac28/workspace/golangProject/im/server/CLAUDE.md` | server/ 装配 + Makefile + go.mod |
| **后端契约** | `/Users/mac28/workspace/golangProject/im/server/docs/BACKEND.md` | **鉴权章节**：cookieId + companyId header 模型；admin JWT 老路径 |
| **Harness C010** | `/Users/mac28/workspace/golangProject/im/docs/harness/C010-userdata-resolve.md` | v0.7.4 鉴权契约（Redis `UserData:<userId>` STRING + companyId header） |
| Harness 总索引 | `/Users/mac28/workspace/golangProject/im/docs/harness/README.md` | C001–C016 全表 |
| Go 并发 skill | `~/.claude/skills/go-concurrency-patterns/SKILL.md` | 写 Go 唯一标准 |
| Go testing rule | `~/.claude/rules/golang/testing.md` | **100% 覆盖率硬约束**（覆盖 common 的 80% 默认） |
| Go security rule | `~/.claude/rules/golang/security.md` | secret 管理 + gosec |
| 全局 git-workflow | `~/.claude/rules/common/git-workflow.md` | Conventional Commits + Phase tag |

---

> 维护：本文件每次签名算法 / hash 算法 / userId 形态 / 三件套新增能力时同步审；最低契约线就是项目根 `CLAUDE.md §1` + harness C010；子模块不存在（本层已是叶子）。

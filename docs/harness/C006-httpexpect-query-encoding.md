# C006 — httpexpect v2 `.GET(path+"?q=v")` 会 URL-encode `?`，必须用 `.WithQuery("q", "v")`

```yaml
---
id: C006
title: httpexpect v2 路径里禁止拼 ?query string，必须用 .WithQuery / .WithQueryString
status: active
created: 2026-05-07
last_recurred: 2026-04-26
recurrence_count: 1
source_logs:
  - logs/2026-04-26.json#L33
applies_to:
  - server/tests/integration/**/*.go
  - server/internal/**/*_test.go
  - server/internal/testutil/**/*.go
inline_target: ~/.claude/rules/golang/testing.md  # 已有"httpexpect v2 陷阱"小节，本 harness 是详细版
---
```

## 1. 触发场景（Trigger）

任何使用 `httpexpect v2`（`github.com/gavv/httpexpect/v2`）写的集成 / 单元测试：

- `server/tests/integration/**/*.go` 调 `expect.GET / POST / PUT / DELETE`
- `server/internal/**/*_test.go` 用 httpexpect 跑黑盒
- `server/internal/testutil/httpexpect_helpers.go`（如有）封装的辅助函数
- 关键词 grep：`httpexpect` / `expect.GET\(.*\?` / `expect.GET.*"?` / `WithQuery`

## 2. 错误模式（Anti-Pattern）

```go
// ❌ 错误 #1：路径里直接拼 ?query
expect.GET("/api/messages/read-stats?ids=1,2,3").
    Expect().Status(200)
// 实际请求路径变成 /api/messages/read-stats%3Fids%3D1%2C2%2C3
// （httpexpect v2 把整段当 path 处理，?  → %3F）
// 服务端 405 Method Not Allowed 或 404，测试报"端点没注册"实际是 client 错的

// ❌ 错误 #2：fmt.Sprintf 拼 query
url := fmt.Sprintf("/api/users/search?q=%s&page=%d", q, page)
expect.GET(url).Expect()...
// 同样 ? 会被 encode

// ❌ 错误 #3：用 url.Values + url.Encode 自己拼
v := url.Values{}
v.Set("ids", "1,2,3")
expect.GET("/api/messages/read-stats?" + v.Encode())  // 还是错，? 仍然 encode
```

**后果**：
1. **测试假阴性**：服务端正常工作，但测试因 path encode 错误返回 404/405 → 误以为 endpoint 没注册 / handler 路径错
2. **整下午定位**：错误信号"路径不存在"看起来像后端 bug，实际是 client 拼接 bug，无效 debug 时间长
3. **测试文件遍布同样错误**：复制粘贴一份错的，蔓延全套 read-stats / search / batch 集成测试

事故链路：
- 2026-04-26 M2 集成测试一片红，路径全报"未找到"。最初怀疑 router 注册顺序 / middleware 拦截，反复加日志 30 分钟后才发现 `?` 被 encode。修法 commit `43c05ab` 全部改 `WithQuery`（13 处）。

## 3. 正确做法（Required）

**首选 A — `.WithQuery(key, value)` 单参数**：

```go
// ✅ 正确
expect.GET("/api/messages/read-stats").
    WithQuery("ids", "1,2,3").
    WithHeader(middleware.MMCookieHeader, cookie).
    Expect().Status(200)
```

**首选 B — `.WithQueryString(rawQuery)` 整串**：

```go
// ✅ 正确：复杂 query 一次性传
expect.GET("/api/users/search").
    WithQueryString("q=zlc&page=2&limit=20").
    Expect()...
```

**首选 C — 多参数链式**：

```go
// ✅ 正确：可读性最高
expect.GET("/api/messages/read-stats").
    WithQuery("ids", "1,2,3").
    WithQuery("include_users", "true").
    Expect()...
```

**绝对禁止 D**：
- ❌ 路径字符串里出现 `?`（除非是 `:wildcard?` 形式的 gin 可选路径占位符，且不会被 httpexpect 处理）
- ❌ `fmt.Sprintf` 拼接含 `?` 的 URL
- ❌ 自己 `url.Values{}.Encode()` 后追加 path

**实施约束**：
- httpexpect 测试**所有** query string 必须经 `WithQuery / WithQueryString` 入口
- 项目规则已 inline 进 `~/.claude/rules/golang/testing.md` § 常见陷阱节，本 harness 为详细版 + 复现日志 + grep gate

## 4. 检查方法（Verification）

### 4.1 自动 grep（必须返回 0 条）

```bash
# ① httpexpect 调用的 path 含 ?
grep -rEn '(expect|e)\.(GET|POST|PUT|PATCH|DELETE)\("[^"]*\?' \
  server/tests/ server/internal/ --include='*_test.go'

# ② fmt.Sprintf 拼 ? + httpexpect
grep -rEn 'fmt\.Sprintf\("[^"]*\?' server/tests/ server/internal/ --include='*_test.go' \
  | xargs -I{} sh -c 'echo {} | grep -l httpexpect && echo {}'  # 启发式

# ③ url.Values{}.Encode() 后立即用于 httpexpect path
grep -rEn 'url\.Values\{|\.Encode\(\)' server/tests/ server/internal/ --include='*_test.go'
```

### 4.2 CI Gate

- `verify-all` 加上 §4.1 grep；非 0 行 → exit 1
- `golangci-lint run` 自定义 forbidigo 规则禁止 `fmt.Sprintf.*\?` 在测试文件中

### 4.3 单测（meta）

- 路径：`server/internal/testutil/httpexpect_helpers_test.go`
- 用例：
  - `TestHTTPExpect_WithQueryEncodesProperly` — `.WithQuery("ids", "1,2,3")` 实际请求 path 是 `/api/...?ids=1%2C2%2C3`
  - `TestHTTPExpect_PathWithQuestionMark_Fails` — 显式验证 `expect.GET("/api?q=v")` 返回 404（防止有人误以为可以这样写）

### 4.4 模板提供

- 路径：`server/internal/testutil/httpexpect_helpers.go`（待补）
- 提供 helper：`func E2E(t *testing.T, h http.Handler) *httpexpect.Expect`
- 文档注释引用本 harness：

```go
// E2E returns an httpexpect.Expect bound to handler h.
//
// Always use .WithQuery / .WithQueryString for query strings — never embed `?`
// in the path argument.  See docs/harness/C006-httpexpect-query-encoding.md.
func E2E(t *testing.T, h http.Handler) *httpexpect.Expect { ... }
```

## 5. 复现历史（Recurrence Log）

| # | 日期       | 触发场景                                                                              | 引用日志                  | 处置                                                                  |
|---|------------|---------------------------------------------------------------------------------------|---------------------------|-----------------------------------------------------------------------|
| 1 | 2026-04-26 | M2 集成测试 13 处用 `expect.GET("/api/...?ids=1,2,3")`，全部报路径 404，30 分钟定位   | logs/2026-04-26.json#L33 | 全部改 `.WithQuery(...)`（commit `43c05ab`）+ 加测试 helper            |

## 6. 反例与边界（Don't Over-Apply）

- ✅ **gin 路由声明**里允许 `?` 作可选路径占位（`:id?`），那是 gin 自己处理，不经 httpexpect
- ✅ **非 httpexpect 的 HTTP 客户端**（如 net/http、resty、go-resty）有各自的 query 处理，不受本约束
- ✅ **生产代码**调外部 API 拼 `?` 是 stdlib 行为，不受本约束（用 `url.URL.RawQuery` 或 `url.Values.Encode()` 是正确的）
- ❌ **不要**为了"美观"用 raw GET 拼字符串绕过 helper（典型反模式：`expect.Request("GET", "/api/x?q=1")` 也是错的）
- ❌ **不要**扩展到 POST body 里的字段编码 —— body 由 `WithJSON / WithForm` 管，不归本 harness

## 7. 升级 / 弃用条件（Lifecycle）

**晋升 → merged**：
- 30 天零新复现 + §4.1 grep gate 在 CI 接管
- inline 已生效（`~/.claude/rules/golang/testing.md` § 常见陷阱），本 harness 保留作详细历史
- 项目根 `CLAUDE.md §1.8` 已有摘要："httpexpect v2 陷阱：`GET(path+"?q=v")` 会 URL-encode `?` → 必须用 `.WithQuery("q","v")`"

**弃用 → deprecated**：
- 切换其他 HTTP 测试框架（如 `httptest.NewRecorder` + 手动断言 / `vektra/mockery + testify/suite`）
- httpexpect v2 升级到 v3 + 修复了该行为（需要时再 verify）
- 整库换 gRPC 不再有 HTTP 测试 → 本条 deprecated

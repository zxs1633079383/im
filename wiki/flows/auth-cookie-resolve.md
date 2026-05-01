---
type: flow
title: Cookie 单栈鉴权解析
status: stable
last_verified: 2026-04-28
sources:
  - server/internal/middleware/mattermost_cookie.go
  - server/docs/M4_SPEC.md
related:
  - concepts/cookie-id-native
  - milestones/M4-cookie-id-native
  - concepts/team-id-derivation
confidence: high
---

# Flow：Cookie 单栈鉴权

> M4 起 IM 唯一的鉴权方式：HTTP cookie `MMAUTHTOKEN` → Redis HASH lookup → 解析为 `MattermostUser`。

## 链路 ASCII

```text
HTTP request
  │ Cookie: MMAUTHTOKEN=abc123def456...
  ▼
middleware.MattermostCookieResolve
  ├─ extract cookieId from cookie
  ├─ cache hit? (LRU 256)
  │    ├─ yes → 返 cached MattermostUser
  │    └─ no  → rdb.HGet("User", `"cookieId"`) → JSON unmarshal
  │           └─ 写入 LRU
  └─ ctx.Set("user", *MattermostUser)
       │
       ▼
middleware.CookieRequired
  ├─ if ctx.user == nil → 401 Unauthorized
  └─ else continue
       │
       ▼
handler 拿 c.MustGet("user").(*MattermostUser)
```

## Redis HASH 协议

```text
HASH key:    "User"
field:       "<cookieId>"        ← 注意：写入时被 cses Java 加引号 → "\"abc123...\""
value:       JSON of MattermostUser
TTL:         由 cses 控制（与 mattermost session 一致）
```

> ⚠️ 已知坑：`HGET "User" "<cookieId>"` 必须**带引号**，否则查不到。Java 端用 `\"` 包了。

## MattermostUser 结构（关键字段）

```go
type MattermostUser struct {
    ID         string  // 24-char hex mm UserID
    Username   string
    CompanyID  string  // → team_id 派生（[[concepts/team-id-derivation]]）
    OrgID      string  // CompanyID 兜底
    // ...
}
```

## LRU 缓存

`MattermostUserCache`（256 capacity）：

- 命中：~10μs（map lookup）
- 未命中：Redis round-trip ≈ 1ms
- TTL 与 Redis 同步（被动失效；显式清除路径未实现）

## 失败语义

| 场景 | 行为 |
|------|------|
| 没带 cookie | `CookieRequired` 抛 401 |
| cookieId 不在 Redis | `HGet` 返空 → `CookieRequired` 抛 401 |
| Redis down | middleware 返 503 + tracing span 记录 |
| JSON unmarshal 失败 | logging + 401（容错而非 panic） |

## WS 鉴权

`GET /ws` upgrade 时同样走 cookie middleware（升级前已鉴权 ✅）。upgrade 失败 → 客户端会重连，可能给 401（cookie 失效）。

详见 [[concepts/cookie-id-native]]。

## Admin 后门

`/api/admin/*` 路由组**保留 JWT** —— admin 工具不依赖 cookie，方便运维。其他路径全 cookie。

## 测试

- 单元：`middleware/mattermost_cookie_test.go`、`cookie_required_test.go`
- 集成：`m4_auth_smoke_test.go` 用 testcontainers Redis + CookieFixture

## 410 Gone（旧 JWT 路径）

`/api/auth/login` 等老 JWT 端点返 `410 Gone` + 提示文案，强制客户端切 cookie auth。

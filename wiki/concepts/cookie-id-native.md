---
type: concept
title: M4 Cookie-ID Native（删 users 表）
status: stable
last_verified: 2026-04-28
sources:
  - docs/GOAL.md#§4.7
  - server/docs/M4_SPEC.md
  - SESSION.md
related:
  - milestones/M4-cookie-id-native
  - concepts/team-id-derivation
  - flows/auth-cookie-resolve
confidence: high
---

# M4 Cookie-ID Native

> 「im 不维护本地 users 表」。所有用户身份从 `Redis HASH "User"` 解析，业务表 user FK 全部 `TEXT` 存 mm UserID。

## 目标

把 IM 的「用户表」彻底外置给 cses Java 后端：

| 维度 | M4 之前 | M4 之后 |
|------|--------|--------|
| 本地 users 表 | 存在 | **删除** |
| user FK 类型 | `int64`（im 自增） | `TEXT`（24-char hex mm UserID） |
| 用户信息来源 | im 本地表 | Redis HASH "User"（cses 写入） |
| 鉴权 | JWT | Cookie 单栈（`MMAUTHTOKEN` cookie → cookieId） |
| Admin 后门 | 同 JWT | `/api/admin/*` 保留 JWT |

## Redis HASH 结构

```text
HASH "User"
  field: cookieId              (e.g. "abc123def456...")
  value: serialized MattermostUser JSON
```

字段示例：

```jsonc
{
  "Id":         "5p74yjbb1bywxyo3pf6dtsxoqr",   // 24-char mm UserID
  "Username":   "zhanglichao",
  "CompanyID":  "comp-001",                      // → team_id
  "OrgID":      null
}
```

## 鉴权链路

```text
HTTP request
  ├─ middleware.MattermostCookieResolve
  │    └─ rdb.HGet("User", cookieId) → MattermostUser
  ├─ middleware.CookieRequired
  │    └─ 抛 401 if user == nil
  └─ handler 拿 ctx.User
```

详见 [[flows/auth-cookie-resolve]]。

## 业务表改动

```sql
-- migrations/014_m4_cookie_id_native.up.sql
ALTER TABLE channels ALTER COLUMN creator_id TYPE TEXT;
ALTER TABLE messages ALTER COLUMN sender_id  TYPE TEXT NOT NULL;
ALTER TABLE messages ADD COLUMN team_id TEXT;
ALTER TABLE messages ADD COLUMN visible_to TEXT[];
ALTER TABLE channel_members ALTER COLUMN user_id TYPE TEXT;
ALTER TABLE friendships
  ALTER COLUMN requester_id TYPE TEXT,
  ALTER COLUMN addressee_id TYPE TEXT;
DROP TABLE users;                               -- 关键：彻底删
```

## LRU 用户缓存

M4 同期上 `MattermostUserCache`（256 LRU）：把热用户从 Redis 缓到内存，热路径少一次 round-trip。

## 410 Gone

`/api/auth/login` / `/api/auth/refresh` 等 JWT-era 端点 → 返 `410 Gone`，提示「Use cookie auth」。

## 为什么这么做

1. **避免双栈用户表** → 无 sync 任务、无 race
2. **cses 永远是 source of truth** → im 切换可逐步上线
3. **删表减负** → 少 1 张表 + 5 条 FK 约束

## 替代

考虑过：
- 维护本地 users + cron 同步 → 复杂、有 lag
- 调 cses HTTP → 每次 round-trip 太重
- **采用：Redis HASH 共享 + 本地 LRU** ✅

## 当前状态

- ✅ tag `v0.6.0-m4-cookie-id-native`：Go 全量级联完成
- ✅ tag `v0.6.1-m4-pre-deployed`：migration 014 dev DB dry-run + im_pre 落地
- ⏳ Tauri 客户端联调中（v0.7.2 阶段）

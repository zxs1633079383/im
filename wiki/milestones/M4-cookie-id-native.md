---
type: milestone
title: M4 — 用户身份模型重构（Cookie-ID Native）
status: stable
last_verified: 2026-04-28
sources:
  - docs/GOAL.md#§3
  - server/docs/M4_SPEC.md
  - SESSION.md
related:
  - concepts/cookie-id-native
  - concepts/team-id-derivation
  - flows/auth-cookie-resolve
confidence: high
---

# M4 — Cookie-ID Native

| 字段 | 值 |
|------|----|
| Tag（核心）| `v0.6.0-m4-cookie-id-native` |
| Tag（部署）| `v0.6.1-m4-pre-deployed` |
| Tag（最新）| `v0.7.2-no-mattermost`（后端清完 mm 死代码） |
| 状态 | ✅ 后端完成，⏳ Tauri 客户端联调 |

## 一句话目标

**删 `users` 表 → 全部 user FK 改 mm UserID (TEXT) → 加 `team_id` 列 → 鉴权只信 cookieId**。详见 [[concepts/cookie-id-native]]。

## 关键产出

### Schema cutover（migration 014）

```sql
-- 主要改动
ALTER TABLE channels        ALTER creator_id TYPE TEXT;
ALTER TABLE messages        ALTER sender_id TYPE TEXT NOT NULL;
ALTER TABLE messages        ADD COLUMN team_id TEXT;
ALTER TABLE messages        ADD COLUMN visible_to TEXT[];
ALTER TABLE channel_members ALTER user_id TYPE TEXT;
ALTER TABLE friendships     ALTER {requester,addressee}_id TYPE TEXT;
DROP TABLE users;
```

### 鉴权重写

- 新 `MattermostCookieResolve` middleware（[[flows/auth-cookie-resolve]]）
- LRU 缓存 `MattermostUserCache` 256 cap
- 旧 JWT 端点 `/api/auth/login` 等 → `410 Gone`
- 保留 `/api/admin/*` 走 JWT 后门

### 全量级联

repo / service / handler / gateway 全部改 mm UserID（TEXT）；单测重建；6 个 happy-path 集成测试（`m4_*_test.go`）。

### 部署

- pre-7g 镜像在 K8s `im-v2/im-gateway` NodePort 30880
- migration 014 在 `im_pre` 库实落（force 13 清 dirty → up 14）
- 张立超 cookie 冒烟 200 验通过

## v0.7.x 收尾系列

| Tag | 内容 |
|-----|------|
| `v0.7.0-cses-cutover` | cses 后端 cutover 完成 |
| `v0.7.2-no-mattermost` | 后端清 mm 死代码、镜像演进 |
| 期望 `v0.7.3-client-verified` | Tauri client 联调通过 |

## 联调步骤（详见 SESSION.md §0）

1. 进 cses-client 仓库 → `git pull` → `yarn start`（= `tauri:dev`）
2. 用 `17692704771/123456` 登录
3. console 期待 `🔀 [MessageHttpService] apiFlavor: mattermost → im` + `routedToIm: true`
4. Network 验：`imHttp.*` 命中 30880，`this.http.*` 命中 3399
5. 跑 `ws-smoke-zhanglichao + v070-smoke + v072-smoke` 三件套

## 与下一里程碑

[[milestones/M5-historical-etl]] 处理历史数据迁移（M4 改完 schema 后才能写 ETL 脚本）。

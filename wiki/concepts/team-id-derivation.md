---
type: concept
title: team_id 派生规则
status: stable
last_verified: 2026-04-28
sources:
  - docs/GOAL.md#§4.8
  - server/docs/M4_SPEC.md
related:
  - milestones/M4-cookie-id-native
  - concepts/cookie-id-native
confidence: high
---

# `team_id` 派生规则

> M4 后所有业务表 user FK 改 mm UserID（TEXT），其中 `team_id` 仅在频道创建时刻冻结一次，messages 透传。

## 派生顺序

```text
team_id = MattermostUser.CompanyID
        ?? MattermostUser.OrgID
        ?? NULL
```

- 优先 `CompanyID`（公司 id，对应 mm 多租户上下文）
- `CompanyID` 空 → `OrgID` 兜底
- 都空 → `NULL`（无 org 用户，常见于内部测试账户）

## 冻结时机

**仅在频道创建时刻冻结一次**：

```text
POST /api/channels (body: name, type, ...)
  ├─ resolveCookie → MattermostUser
  ├─ team_id = derive(user)        ← 冻结 ✅
  └─ INSERT channels (team_id=...)
```

之后：
- `messages.team_id` denormalize **自当时 channels.team_id**
- `channels.team_id` 不再变（即使创建者切换公司，频道归属不变）
- 跨 team 邀请用户加入频道：业务允许（消息照常落到 channel.team_id）

## 为什么这么定

1. **可枚举**：team_id 是冷字段，让运营查报表 / 跨 team 隔离方便
2. **不可变**：避免「用户切公司 → 历史消息 team 也变」的语义崩坏
3. **denormalize**：消息查询不再 JOIN channels，性能 + 简化 sync 协议

## 数据库列

| 表 | 列 | 类型 |
|----|----|----|
| `channels` | `team_id` | `TEXT NULL` |
| `messages` | `team_id` | `TEXT NULL` |

均为可空 —— `NULL` 表示「无 org 用户场景」。

## 与 [[concepts/cookie-id-native]] 的关系

M4 之前 user FK 是 `int64`，没有 team 概念。M4 把：
1. user FK 全部 → `TEXT`（mm UserID 24-char hex）
2. `users` 本地表删除 → 用户身份从 Redis Hash 解析
3. 新增 `team_id` 列 → 仅 `messages` / `channels` 落地

## 测试

集成测试 `m4_channel_create_dm_test.go` / `m4_channel_create_group_test.go` 验证 team_id denormalize 路径。

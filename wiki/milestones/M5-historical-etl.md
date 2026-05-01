---
type: milestone
title: M5 — 历史数据 ETL
status: drafting
last_verified: 2026-04-28
sources:
  - docs/GOAL.md#§3
related:
  - milestones/M4-cookie-id-native
  - milestones/M6-mattermost-decom
  - concepts/seq-cursor
confidence: medium
---

# M5 — 历史数据 ETL

| 字段 | 值 |
|------|----|
| Tag | （未开始） |
| 状态 | 🗓 TODO（M4 完成后开） |

## 范围

把旧 Mattermost csesapi 历史数据迁到 im 后端：

- 历史 `Posts` → `messages`，分配 `migration_sort_key`
- 历史 `Channels` → 保留 ID，team_id 派生（[[concepts/team-id-derivation]]）
- 历史 `ChannelMembers` → `channel_members`
- 历史 phantom（已读 / 未读） → 用 `last_read_seq` 重建

## 关键算法（已冻结）

`migration_sort_key` = MongoDB ObjectId 风格的 64-bit：
- 高 48 bit：毫秒时间戳
- 低 16 bit：同毫秒自增

```
| 48 bit ms timestamp | 16 bit counter |
```

仅用于历史数据**排序**（让客户端重排），**不替代 `seq`**（[[concepts/seq-cursor]]）。

## 不在范围

- 历史用户表 → 不迁，已统一从 Redis HASH 解析（[[concepts/cookie-id-native]]）
- 历史 phantom 映射不做 1:1 重建 → 第一次同步时按当前规则重新计算

## 风险

- 数据量：未知，需先 sample 估算
- DB 压力：批量 INSERT 走 streaming + 批量 commit，避免单大事务
- 服务可用性：ETL 期间 im 仍正常工作（双写 / 离线导入二选一）

## 计划（草稿）

待 M4 客户端联调通过后再细化。先做 sample 数据 ETL → 验证 sort_key 重排正确 → 再全量。

## 与下一里程碑

[[milestones/M6-mattermost-decom]] 是切流 + 下线 Mattermost；M5 是数据搬完为前提。

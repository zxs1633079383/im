---
type: decision
title: 8 条硬约束（写在石头上）
status: stable
last_verified: 2026-04-28
sources:
  - docs/GOAL.md#§4
related:
  - concepts/seq-cursor
  - concepts/cross-pod-push
  - concepts/routing-ttl
  - concepts/ws-event-types
  - concepts/cookie-id-native
  - concepts/team-id-derivation
  - decisions/no-traffic-rollback
confidence: high
---

# 8 条硬约束 — 写在石头上

> 来自 `docs/GOAL.md §4`。**改任一条都需要在 [[log]] 里写 schema-change**，并同步更新代码、测试、文档三方。

## 1. WSMessageType V1 锁定（12 + M2 4 = 16）

V1 12 种：ping / pong / send / send_ack / push_msg / push_ack / sync_resp / read_sync / friend_event / channel_event / msg_updated / msg_deleted

M2 追加 4 种：announcement_posted / urgent_posted / approval_updated / notification_received

**新增类型必须升 V2**（[[concepts/ws-event-types]]）。

## 2. AllocSeqAndInsert 是 seq 唯一入口

```go
AllocSeqAndInsert(ctx, tx *gorm.DB, msg) (int64, error)
```

`tx != nil` 复用外部事务。**禁止任何绕行写 `messages` 的路径**。M4 后 `msg.SenderID` 类型从 int64 → string。详见 [[entities/alloc-seq-and-insert]]。

## 3. Pulsar topic 命名

```text
persistent://im/push/msg.push.{gatewayID}
本地调试自动追加 .{localname} 避免窜台
```

详见 [[concepts/cross-pod-push]]。

## 4. Redis routing TTL = 45s, heartbeat = 15s × 3

数字耦合，不可单边动。详见 [[concepts/routing-ttl]]。

## 5. 不做流量回切

只能向前修复，不留 feature flag 回 Mattermost。详见 [[decisions/no-traffic-rollback]]。

## 6. 历史数据 phantom 映射暂不实现

先保证现有数据正确，历史迁移延后到 [[milestones/M5-historical-etl]]。

## 7. M4 起：im 不维护本地 users 表

所有用户身份从 `Redis HASH "User"` 解析。业务表 user FK 全部 `TEXT` 存 mm UserID。鉴权只信 cookieId（`/api/admin/*` 保留 JWT 后门）。详见 [[concepts/cookie-id-native]]。

## 8. team_id 派生约定

```text
team_id = MattermostUser.CompanyID
        ?? MattermostUser.OrgID
        ?? NULL
```

仅在 channels 创建时刻冻结一次；messages.team_id denormalize 自 channels.team_id。详见 [[concepts/team-id-derivation]]。

---

## 改约束的流程

1. 在 [[log]] 写 `## [YYYY-MM-DD] schema-change | <constraint>`
2. 改本页对应条目
3. 改对应 concept 页 + 代码 + 测试
4. 跑 `make verify-all`
5. 升 tag（不可向前兼容的话）

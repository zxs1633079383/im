---
type: milestone
title: M6 — 下线 Mattermost / csesapi
status: drafting
last_verified: 2026-04-28
sources:
  - docs/GOAL.md#§3
related:
  - milestones/M5-historical-etl
  - decisions/no-traffic-rollback
confidence: medium
---

# M6 — 下线 Mattermost

| 字段 | 值 |
|------|----|
| Tag | （未开始） |
| 状态 | 🔜 M5 后启动 |

## 范围

- cses Java 后端切到「永久下发 imGatewayHttp/Ws」 → 100% 流量到 im
- 监控观察期：1–2 周
- Mattermost 服务器集群下线 + 数据清理
- csesapi（mm 插件）代码归档

## 不在范围（外部服务，永远不归 im）

按 [[decisions/hard-constraints]]，下列路径继续走 Java：

- `/vote/*` —— Java 投票服务
- `/Im/search/*` —— Java 搜索服务
- 文件分片 / 断点续传 —— 外部对象存储
- 模板消息 `/post/templateReceived` —— Java
- 组织架构 `/modules`、`/groups`、`/teams/*` —— Java

## 验收标准

- 所有 ImApiAdapter endpoint 走 im 后端无 fallback
- 不再有 `apiFlavor: 'mattermost'` 路径
- M5 数据迁移完成 + 抽样验证一致性
- 7×24 监控告警零异常 1 周

## 不留回切

[[decisions/no-traffic-rollback]] 是写在石头上的：**M6 启动后不留 mattermost feature flag 回切**。出问题只能向前修复。

## 后续

M6 完成后 IM 项目进入「持续维护期」：
- 加 V2 WS 事件类型（reaction / typing / presence）
- 完善 Bot / Agent / Webhook（M0 不在范围的 P3 模块）
- 性能调优（OTel 指标驱动）

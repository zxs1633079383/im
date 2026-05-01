---
type: milestone
title: M2 — 企业协作
status: stable
last_verified: 2026-04-28
sources:
  - docs/GOAL.md#§3
  - server/docs/BACKEND.md#§六
related:
  - milestones/M1-core-message-sync
  - concepts/ws-event-types
confidence: high
---

# M2 — 企业协作

| 字段 | 值 |
|------|----|
| Tag | `v0.2.0-m2-complete` |
| 状态 | ✅ 完成 |

## 范围

7 个企业模块 + 4 个新 WS 事件类型：

| 模块 | 路径 | WS 事件 |
|------|------|--------|
| 公告 | `/api/announcements/*` | `announcement_posted` |
| 频道治理 | `/api/channels/:id/governance/*` | （复用 channel_event） |
| 紧急消息 | `/api/messages/urgent/*` | `urgent_posted` |
| 审批 | `/api/approvals/*` | `approval_updated` |
| 系统通知 | `/api/notifications` | `notification_received` |
| 定时消息 | `/api/messages/scheduled` | （定时到点投递走 push_msg） |
| 快捷回复 | `/api/quick-replies/*` | （无 push） |

## 关键设计

- **审批状态机**：用 SQL `WHERE status = ?` 守卫做乐观并发，避免 SELECT FOR UPDATE
- **定时消息**：`scheduled_worker.go` 后台轮询 → `repo.Deliver` 复用 [[entities/alloc-seq-and-insert]]
- **公告/紧急** 走专表，不污染 `messages`
- **新增 WS 事件** 同步登记三方（[[concepts/ws-event-types]]）

## 测试覆盖

- 集成：`v5_m2_*_test.go` 每子模块独立测
- broadcast：`v5_m2_broadcast_test.go` 验跨 pod fan-out

## 与下一里程碑

[[milestones/M3-stability-cluster]] 聚焦稳定性 + 集群韧性，不再加业务模块。

---
type: milestone
title: M1 — 核心消息与同步
status: stable
last_verified: 2026-04-28
sources:
  - docs/GOAL.md#§3
  - server/docs/BACKEND.md#§六
related:
  - entities/alloc-seq-and-insert
  - entities/sync-service
  - entities/cross-pod-push
  - concepts/seq-cursor
  - concepts/ws-event-types
confidence: high
---

# M1 — 核心消息与同步

| 字段 | 值 |
|------|----|
| Tag | `v0.1.0-m1-complete` |
| 状态 | ✅ 完成 |

## 范围

- auth（JWT 时代）
- channel CRUD + 成员管理
- message send / fetch / 编辑 / 撤回
- `/api/sync` 增量同步契约（[[entities/sync-service]]）
- WebSocket V1 12 种事件（[[concepts/ws-event-types]]）
- 跨 pod 推送骨架（[[entities/cross-pod-push]]）

## 关键产出

| 模块 | 重点 |
|------|------|
| `repo.MessageRepo.AllocSeqAndInsert` | seq 单调原子分配（[[entities/alloc-seq-and-insert]]） |
| `service.SyncService` | cursor → delta 增量返回 |
| `gateway.Hub` + `Routing` + `ProducerCache` | 跨 pod 三层 fallback |
| WSMessageType V1 锁定 | 12 种事件三方对齐 |

## 性能特性

- 单条发消息 p99 ≈ 5–10ms（含跨 pod）
- `/api/sync` 单频道 200 条返回 < 50ms
- 心跳 15s × 3 容错（[[concepts/routing-ttl]]）

## 测试覆盖

- 单元：repo / service / handler / gateway 各层 100%（豁免 main + 生成代码）
- 集成：V5.1–V5.10 单接口 + G1–G10 模块组连续流

## 与下一里程碑的衔接

[[milestones/M2-enterprise-collab]] 在 M1 协议骨架上加 4 种 WS 事件 + 7 个企业模块（公告、紧急、审批等）。

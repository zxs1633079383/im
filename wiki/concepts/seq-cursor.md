---
type: concept
title: 频道级单调 seq 游标
status: stable
last_verified: 2026-04-28
sources:
  - docs/GOAL.md#§1
  - server/docs/BACKEND.md#§3.3
related:
  - entities/alloc-seq-and-insert
  - entities/sync-service
  - flows/incremental-sync
confidence: high
---

# 频道级单调 seq 游标

> Telegram 风格的同步基石。**每频道一个 `last_seq`**，客户端只持 `{channel_id → seq}`，时间复杂度与频道数解耦。

## 取代了什么

旧 Mattermost csesapi 的同步：

| 老 | 痛点 |
|----|------|
| bitmap 按天/段拆分 | 客户端维护代价随频道数 × 时间爆炸 |
| segmentId 索引 | 同毫秒多消息排序错乱 |
| `increment_channel` 独立事件 | 协议臃肿，70+ WSEvent 常量 |

## 设计要点

1. **每频道一个单调计数器** `channels.last_seq`（int64，monotonic per-channel）
2. **客户端持久化** `{channel_id → known_seq}` —— Tauri 端见 [[entities/im-seq-data-source]]
3. **服务端原子分配** —— `UPDATE channels SET last_seq=last_seq+1 RETURNING ...` 单条 SQL，由 [[entities/alloc-seq-and-insert]] 唯一负责
4. **心跳即增量信号** —— `ping.channel_seqs` ↔ `pong.channel_seqs`（仅 delta 频道）
5. **WS push_msg 携带 seq** —— 客户端直接 `last_seq = max(local, msg.seq)`，无需查询

## 关键不变量

- **per-channel 严格单调**：并发写者必拿不同 seq
- **不可回退**：seq 只能增，删除消息用 `deleted_at` 软删，**不**回收 seq
- **不可越界写**：客户端不上传 seq，后端唯一分配

## 历史 seq 的处理（M5 之后）

`migration_sort_key` = 48 bit 毫秒 + 16 bit 同毫秒自增（MongoDB ObjectId 风格）。仅用于历史数据 ETL 排序，**不替代** `seq`。详见 [[milestones/M5-historical-etl]]。

## 与 phantom 的关系

`messages.visible_to TEXT[]` 用来支持「定向消息」：sender 之外的成员看不见原文，但 `phantom_count` += 1，用户看到「您有未读」徽标。phantom 与 seq 在同一事务内 bump，由 [[entities/alloc-seq-and-insert]] 调用 `IncrementPhantomCount` 完成。

## 性能数据（理论）

- 单 alloc：1 row UPDATE + 1 INSERT，PG ≈ 1ms
- 同 channel QPS 上限：取决于 PG 行锁吞吐（`pg_locks` 监控）
- 集群可拆 partition by channel_id，但 M1–M6 不实施

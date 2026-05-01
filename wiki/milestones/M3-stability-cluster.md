---
type: milestone
title: M3 — 稳定性与集群韧性
status: stable
last_verified: 2026-04-28
sources:
  - docs/GOAL.md#§3
  - server/docs/BACKEND.md#§六
related:
  - milestones/M1-core-message-sync
  - milestones/M2-enterprise-collab
  - milestones/M4-cookie-id-native
confidence: high
---

# M3 — Topic/Presence + 集群韧性

| 字段 | 值 |
|------|----|
| Tag | `v0.3.2-m3-dm-index` |
| 状态 | ✅ 完成（全链路 100% 可靠基线）|

## 范围

### 业务

- **Topic**：频道下子群聊（楼中楼）
- **Presence**：在线状态广播（替换 mm `status_change`）
- `ImApiAdapter` Angular 端 M1 接口完整覆盖

### 集群

- Redis Cluster（取代单节点）
- 9 项 OTel metric（详见 BACKEND §11）
- HPA 3 → 17 pod 弹性
- E2E 13/13 通过

### 修复

- `Conn.Push` race 条件（commit `v0.3.1-m3-racefix-pool300`）
- PG 连接池对齐 HikariCP（300 connections）
- `FindDM` 反向索引（DM 查找 N→1）

## 子标签

| Tag | 节点 |
|-----|------|
| `v0.3.0-m3-pre-deployed` | 预部署完成 |
| `v0.3.1-m3-racefix-pool300` | Conn race + pool 调优 |
| `v0.3.2-m3-dm-index` | DM 反向索引 |
| `v0.4.0-m3-sysmsg-broadcast` | 系统消息广播 |
| `v0.4.1-m3-markoffline-cleanup` | markOffline 路径清理（仍是骨架） |
| `v0.4.2-m3-mm-cookie-bridge` | Mattermost cookie 桥（M4 准备） |
| `v0.5.0-config-consul` | Consul 配置中心 |
| `v0.5.1-cookie-auth` | Cookie 单栈起航 |
| `v0.5.2-m4-foundation` | M4 foundation 分支基础 |

## 关键债务

- `client message.service.ts` 37 URI 切换 backlog → 推到 [[milestones/M4-cookie-id-native]] 完工后再切（避免双重改造）

## 与下一里程碑

[[milestones/M4-cookie-id-native]] 重构身份模型。M3 是稳定性基线，M4 是数据模型基线。

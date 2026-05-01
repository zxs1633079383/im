---
type: decision
title: 不留 Mattermost 回切的 feature flag
status: stable
last_verified: 2026-04-28
sources:
  - docs/GOAL.md#§4.5
related:
  - concepts/api-flavor-switch
  - milestones/M6-mattermost-decom
  - decisions/hard-constraints
confidence: high
---

# 不留 Mattermost 回切

> 写在石头上的第 5 条硬约束。**只能向前修复**，不允许任何 feature flag / 配置开关把流量切回 Mattermost。

## 决策

- M6 切流后**不**保留「if im_failed → fallback to mattermost」路径
- `apiFlavor` 仅作迁移期双栈（[[concepts/api-flavor-switch]]），M6 后不再下发 mattermost 值
- 旧 csesapi 代码归档（github 保留），但不部署、不维护

## 为什么

| 反方理由 | 回应 |
|---------|------|
| 「万一 im 出问题怎么办」 | 客户端 cursor + 心跳兜底（[[flows/cross-pod-failure]]） + im 自身 K8s 韧性（HPA + PDB） |
| 「保险起见留个开关」 | 双栈维护成本 = 持续负债；测试矩阵爆炸 |
| 「mattermost 已经稳定多年」 | 旧栈痛点是结构性的（bitmap 复杂度、跨 pod 缺失），不是 bug 数量 |

## 替代方案：向前修复策略

- **客户端**：保留旧版本 APK / 安装包（用户重装可临时回滚客户端）
- **后端**：im 多版本并行部署（蓝绿），出 bug 切回上一版 im 而非 mattermost
- **数据**：im 单一 source of truth，出问题修 im，不复制数据回 mattermost

## 心理预期

「敢删」是这个项目的内核：
- 删 `users` 表（[[concepts/cookie-id-native]]）
- 删 mattermost 死代码（v0.7.2 系列）
- 删 strangler-fig 旧 mux（[[decisions/strangler-fig-collapsed]]）

每次删都减少一份认知负担。回切等于反向累积负担。

## 如果真的不行怎么办

按概率从高到低：
1. **客户端 bug** → 推新 APK（最常见）
2. **服务端 bug** → 切回上一个 im 镜像（HPA + 滚动）
3. **数据库迁移坏** → 走备份恢复（PG 增量备份）
4. **极端情况** → 客户端切回旧版连旧 mattermost（用户体感与新版不一致，但能用）

第 4 条**不是**我们要主动规划的回切机制，而是「最坏情况下的毛细兜底」，不写进代码。

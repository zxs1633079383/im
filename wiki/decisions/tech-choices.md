---
type: decision
title: 技术选型理由
status: stable
last_verified: 2026-04-28
sources:
  - docs/ARCHITECTURE.md#§1
  - server/docs/TECH.md
related:
  - decisions/hard-constraints
confidence: medium
---

# 技术选型理由

> 「为什么是这个栈而不是另一个」。M0 决策，本页只记录理由 + 关键 tradeoff。

## Go 后端

| 选型 | 替代品 | 选它的理由 |
|------|--------|----------|
| **Gin** | gRPC、go-kit、原生 net/http | 薄路由、生态成熟、零学习成本；HTTP/REST 是替换 mm 的契约 |
| **GORM + pgx** | 原生 sqlx、ent | UPDATE RETURNING 在 PG 上原子 alloc seq；pgx 性能优于 lib/pq |
| **PostgreSQL 15** | MySQL、MongoDB | row-level lock + RETURNING 让 [[entities/alloc-seq-and-insert]] 单条 SQL 实现 |
| **Apache Pulsar** | Kafka、Redis Streams | 持久化 + partition key 顺序 + topic by gatewayID 让推送 O(1) |
| **Redis** | etcd、单 Hash 自建 | 路由表 TTL + HASH 高效 lookup；与 mm 共享 cookie session |
| **gorilla/websocket** | gobwas/ws、原生 | Hub 实现成熟、心跳支持完整 |
| **OpenTelemetry + Jaeger** | 自建 metrics | 标准，9 项 metric 跨子系统对齐 |
| **testcontainers-go + httpexpect v2** | 内存 mock、独立测试 db | 真容器无 mock 偏差 |

### 关键 tradeoff

- **GORM 而不是 sqlx** → 牺牲一点性能换可读性 + reflective 自动 ORM
- **Pulsar 而不是 Kafka** → 多租户 topic 隔离 + tiered storage 更适合 IM
- **PG 而不是分布式 DB** → seq 单调依赖 row lock，分布式 DB 难保证

## 前端

| 选型 | 替代品 | 理由 |
|------|--------|-----|
| **Angular 17 standalone** | React、Vue | 与 cses-client 已有栈一致，沉没成本 |
| **Tauri 2** | Electron | 包小（1/10）、Rust 性能好、安全模型清晰 |

## CI / 部署

| 选型 | 理由 |
|------|------|
| **Distroless Docker** | 安全 + 镜像小 |
| **K8s + HPA + PDB** | 弹性 + 韧性，配合 Pulsar 跨 pod 推送 |
| **Consul** | M3 后期引入，配置中心 |

## 测试基线

- 行覆盖率 **100%**（不是默认 80%，详见用户全局 Go testing 规则）
- testcontainers 真 PG / Redis（不 mock）
- httpexpect v2 跑全链路
- `-race` 必须 clean

## 何时偏离

只有以下场景允许偏离选型：
1. 性能瓶颈被 OTel 指标证明 + 替换方案有 benchmark 支持
2. 安全漏洞 + 上游不修
3. 长期维护成本 > 重写成本

否则**先用**当前栈解决。

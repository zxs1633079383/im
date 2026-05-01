---
type: registry
title: Raw Sources 登记表
status: stable
last_verified: 2026-04-28
---

# Raw Sources — 不可变源登记

> 本文件登记 wiki 编译用的所有源文档与代码锚点。**Wiki 内容必须可追溯到这里的某一行**。
> 每条记录：路径 + 一句话用途 + 是否已编译入 wiki。

---

## 1. 战略文档（docs/）

| 路径 | 行数 | 用途 | 编译状态 |
|------|------|------|---------|
| `docs/GOAL.md` | 82 | 全局目标 + 8 硬约束 + M1–M6 范围 | ✅ 已编译 → [[milestones/*]], [[decisions/hard-constraints]] |
| `docs/ARCHITECTURE.md` | 230 | 技术栈 + 目录地图 + 关键数据流 | ✅ 已编译 → [[entities/*]], [[flows/send-message]] |
| `docs/HTTP_WS_MAP.md` | 67 | HTTP 与 WS 事件映射表 | ⏳ 部分编译 → [[concepts/ws-event-types]] |
| `docs/E2E_REPORT.md` | 54 | E2E 测试报告 | ⏳ 未编译 |

## 2. 后端契约（server/docs/）

| 路径 | 行数 | 用途 | 编译状态 |
|------|------|------|---------|
| `server/docs/BACKEND.md` | 1058 | M1–M6 详细契约（§3.3 /api/sync, §4.1 AllocSeqAndInsert, §5 跨 pod, §11 OTel） | ✅ 部分编译（首 200 行） |
| `server/docs/EXECUTION_FLOWS.md` | 1143 | 执行流详图 | ⏳ 未编译（next ingest） |
| `server/docs/FRONTEND.md` | 431 | 前端切换 F0–F5 + WS rename 表 | ⏳ 未编译 |
| `server/docs/OVERALL.md` | 479 | 后前端整合路线图 T0–T6 | ⏳ 未编译 |
| `server/docs/TECH.md` | 587 | 技术栈总览 | ⏳ 未编译 |
| `server/docs/M4_SPEC.md` | 377 | M4 cookie 单栈详细 spec | ⏳ 未编译 |
| `server/docs/v0.6.3-test-plan.md` | 92 | v0.6.3 测试计划 | ⏳ 未编译 |

## 3. 关键代码锚点（必读，用作 entity 页的引用）

| 锚点 | 实体 | wiki 页 |
|------|------|--------|
| `server/internal/repo/message.go:79` | `MessageRepo` interface | [[entities/alloc-seq-and-insert]] |
| `server/internal/repo/message.go:88-130` | `Send` 事务结构 | [[flows/send-message]] |
| `server/internal/gateway/cross_pod_push.go:54-117` | `CrossPodBroadcast` / `CrossPodPush` | [[entities/cross-pod-push]] |
| `server/internal/gateway/producer_cache.go` | `ProducerCache` 256-LRU | [[entities/producer-cache]] |
| `server/internal/gateway/topic.go` | `PushTopicFor` 命名 | [[concepts/cross-pod-push]] |
| `server/internal/gateway/routing.go` | Redis routing 实现 | [[entities/routing]] |
| `server/internal/gateway/hub.go` | Hub 内存映射 | [[entities/hub]] |
| `server/internal/service/sync.go` | SyncService.Sync | [[entities/sync-service]] |
| `server/internal/service/scheduled_worker.go` | 后台轮询 | （未编译） |
| `server/cmd/gateway/main.go` | 入口装配 | [[milestones/M1-core-message-sync]] |
| `server/migrations/014_*.sql` | M4 schema cutover | [[milestones/M4-cookie-id-native]] |
| `client/src/app/core/config/api.config.ts` | apiFlavor 开关 | [[concepts/api-flavor-switch]] |
| `client/src/app/core/messages/message.service.ts` | 消息聚合 + 双栈分流 | [[entities/im-api-adapter]] |
| `client/src-tauri/src/data_center/im_seq_data_source.rs` | seq 游标本地持久化 | [[entities/im-seq-data-source]] |

## 4. SESSION 与会话状态（动态、不冻结）

| 路径 | 用途 |
|------|------|
| `SESSION.md` | 会话快照（每次会话末更新）—— **不**编译入 wiki，wiki 只引用其中已稳定的事实 |
| `/workspace/java/logs/{YYYY-MM-DD}.json` | 用户全局 prompt 历史（hook 写入） |

## 5. Tag 锚点（git）

| Tag | 含义 |
|-----|------|
| `v0.1.0-m1-complete` | M1 收尾 |
| `v0.2.0-m2-complete` | M2 收尾 |
| `v0.3.2-m3-dm-index` | M3 DM 反向索引完工 |
| `v0.4.2-m3-mm-cookie-bridge` | Mattermost cookie 桥 |
| `v0.5.1-cookie-auth` | Cookie 单栈鉴权 |
| `v0.6.0-m4-cookie-id-native` | M4 native（删 users 表完成）|
| `v0.6.1-m4-pre-deployed` | M4-pre 部署 + 集成测试 |
| `v0.7.2-no-mattermost` | 后端清掉 Mattermost 死代码 |

## 6. 编译进度计

- 已编译入 wiki：`GOAL.md` 全 + `ARCHITECTURE.md` 全 + `BACKEND.md` 首 200 行 + 3 个核心 .go 锚点
- 未编译总量：约 3500 行 `server/docs/` + 全部代码 + 全部 migration
- 下次 ingest 优先级：`EXECUTION_FLOWS.md` > `M4_SPEC.md` > `FRONTEND.md` > `TECH.md`

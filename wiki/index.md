# im Wiki — Index（内容目录）

> 这是 LLM 维护的 IM 项目知识图谱**唯一目录**。每行一句话+路径。新增/重命名页面必须同步更新这里。
> Schema 见 [[CLAUDE]]，更新日志见 [[log]]，源文档登记见 [[raw/sources]]。

最近一次摄入：**2026-05-01** — Phase 1 im 后端落地完成（3 commits 441ba37 / 66c2a67 / ee32f7f；envelope + received + read-stats 三件均绿）

---

## 🧩 Entities — 具体组件

- [[entities/alloc-seq-and-insert]] — `MessageRepo.AllocSeqAndInsert`：seq 单调原子分配 + insert 的唯一入口
- [[entities/cross-pod-push]] — `Hub.CrossPodPush` / `CrossPodBroadcast`：本 pod 命中 → Redis 路由 → Pulsar 兜底三路径
- [[entities/producer-cache]] — `gateway.ProducerCache`：256-LRU、`onEvict` 关 producer 防泄漏
- [[entities/sync-service]] — `service.SyncService`：seq 游标 → delta 批量回拉，`/api/sync` 唯一处理者
- [[entities/routing]] — `gateway.Routing`：Redis 路由表 `userID → gateway_id`，TTL 45s
- [[entities/hub]] — `gateway.Hub`：本 pod `userID → []*Conn` 内存映射 + WS push
- [[entities/im-api-adapter]] — Angular `ImApiAdapter`：HTTP 双栈分流、apiFlavor 切换
- [[entities/im-seq-data-source]] — Tauri `ImSeqDataSource`：seq 游标本地持久化（替换原 IMDataSource）

## 💡 Concepts — 抽象思想

- [[concepts/seq-cursor]] — 频道级单调 seq 游标（取代 bitmap+segment）
- [[concepts/cross-pod-push]] — 跨 pod 推送的三层 fallback 设计
- [[concepts/routing-ttl]] — TTL 45s + 心跳 15s × 3 容错的耦合数字
- [[concepts/ws-event-types]] — V1 12 + M2 4 = 16 锁定事件，新增需升 V2
- [[concepts/api-flavor-switch]] — `apiFlavor: 'mattermost' \| 'im'` 双栈共存策略
- [[concepts/team-id-derivation]] — `team_id = CompanyID ?? OrgID ?? NULL`，仅频道创建时刻冻结
- [[concepts/cookie-id-native]] — M4 起鉴权只信 cookieId，无本地 users 表

## 🔀 Flows — 端到端数据流

- [[flows/send-message]] — POST `/api/channels/:id/messages` 全链路（含跨 pod 兜底）
- [[flows/incremental-sync]] — `POST /api/sync` 契约 + cursor 协议
- [[flows/auth-cookie-resolve]] — Mattermost cookie → Redis Hash → 用户身份解析
- [[flows/cross-pod-failure]] — Pulsar Send 失败 → markOffline 骨架 + 退路设计

## 🗓 Milestones — 里程碑

- [[milestones/M1-core-message-sync]] — auth + channel + message + sync + WS + 跨 pod（✅ `v0.1.0-m1-complete`）
- [[milestones/M2-enterprise-collab]] — 公告/治理/紧急/审批/通知/定时/快捷回复（✅ `v0.2.0-m2-complete`）
- [[milestones/M3-stability-cluster]] — Topic/Presence + Redis Cluster + HPA 3→17（✅ `v0.3.2-m3-dm-index`）
- [[milestones/M4-cookie-id-native]] — 删 users 表 + cookieId 鉴权（✅ `v0.6.0-m4-cookie-id-native`，M4-pre 部署完成）
- [[milestones/M5-historical-etl]] — `migration_sort_key` 历史数据迁移（🗓 TODO）
- [[milestones/M6-mattermost-decom]] — 全量切换 + 监控观察期（🔜）

## 🔒 Decisions — 硬约束 + 决策

- [[decisions/hard-constraints]] — 8 条写在石头上的约束（GOAL.md §4 提炼）
- [[decisions/tech-choices]] — Gin + GORM + Pulsar + Redis + OTel 选型理由
- [[decisions/no-traffic-rollback]] — 不留 Mattermost 回切的 feature flag
- [[decisions/strangler-fig-collapsed]] — Phase 7.8 strangler-fig 收官，仅 `GET /ws` 走 legacy

## 🪞 Comparisons — 对账 / 比较

- [[comparisons/csesapi-vs-im-coverage]] — Mattermost csesapi ~125 路由 vs im /api ~80 的全集对账与三类裁切

## 🧬 Syntheses — 深度合成回答

- [[syntheses/min-cost-mattermost-cutover]] — 最低成本切换路线图 v2（直接 rewrite + bitmap 砍 + im 后端 0 改动）

## 📚 Raw 源登记

- [[raw/sources]] — 所有不可变源文档 + 代码锚点的注册表

---

## 🆘 快速导航

| 我想…… | 去看 |
|--------|-----|
| 改 Go 代码 | 项目根 `CLAUDE.md` → `Skill(go-concurrency-patterns)` → 然后 `[[entities/...]]` 找具体组件 |
| 写一个新接口 | `server/docs/BACKEND.md` → `[[concepts/seq-cursor]]` → `[[entities/alloc-seq-and-insert]]` |
| 排查推送丢失 | `[[flows/cross-pod-failure]]` → `[[entities/routing]]` → `[[entities/producer-cache]]` |
| 接手这个项目 | `docs/GOAL.md` + `SESSION.md` + 本文件 + `[[milestones/M4-cookie-id-native]]` |
| 给 wiki 加内容 | `[[CLAUDE]]` §3.1 ingest 流程 |

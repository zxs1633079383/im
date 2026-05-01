# Wiki Log — 摄入 / 矛盾 / 巡检的append-only 日志

> 一条 = 一次有意义的 wiki 变更。前缀格式：`## [YYYY-MM-DD] <op> | <title>`。
> 可解析的 op 值：`ingest`、`update`、`contradiction`、`lint`、`schema-change`、`supersede`。

---

## [2026-04-28] ingest | 项目 wiki 初始化（M0–M4 编译）

**Trigger**: 用户请求 `/llm-wiki-knowledge 构建我们im项目的wiki`。

**Sources consumed**:
- `docs/GOAL.md`（230 行）— 全局目标 + 8 条硬约束 + M1–M6 范围
- `docs/ARCHITECTURE.md`（230 行）— 技术栈 + 目录地图 + 关键数据流
- `server/docs/BACKEND.md`（首 200 行）— 设计原则 + WSMessageType V1 锁定 + 进程入口 + 心跳协议
- `server/internal/repo/message.go`（首 120 行）— `MessageRepo` 接口 + `AllocSeqAndInsert` 语义 + `Send` 事务结构
- `server/internal/gateway/cross_pod_push.go`（首 120 行）— `CrossPodBroadcast` / `CrossPodPush` 实现
- `SESSION.md`（首 100 行）— 当前进度（v0.7.2 联调 + 三仓库状态）

**Pages created** (24)：
- 1 schema：`CLAUDE.md`
- 1 index：`index.md`
- 1 log：`log.md`
- 1 sources registry：`raw/sources.md`
- 8 entities：alloc-seq-and-insert / cross-pod-push / producer-cache / sync-service / routing / hub / im-api-adapter / im-seq-data-source
- 7 concepts：seq-cursor / cross-pod-push / routing-ttl / ws-event-types / api-flavor-switch / team-id-derivation / cookie-id-native
- 4 flows：send-message / incremental-sync / auth-cookie-resolve / cross-pod-failure
- 6 milestones：M1 / M2 / M3 / M4 / M5 / M6
- 4 decisions：hard-constraints / tech-choices / no-traffic-rollback / strangler-fig-collapsed

**Key insights**（新会话也要知道的）：
1. seq 单调原子保证依赖**单条 SQL** `UPDATE channels SET seq=seq+1 RETURNING ...`，不是 `SELECT FOR UPDATE` 二段。
2. 跨 pod 推送的「routing miss → markOffline」是**骨架位**，真删除路径未落地（producer.Send 失败时只 logging，没有清 Redis 路由）。
3. M4 之后 `users` 表已删，所有用户身份从 `Redis HASH "User"` 解析；`messages.{sender_id, team_id}` 是**唯一冷冻字段**。
4. WSMessageType V1 + M2 = 16 种锁定，新增必升 V2 + 三方同步（Go server / Angular client / Tauri Rust）。
5. `apiFlavor` 切换由 cses Java 后端下发的 `imGatewayHttp` / `imGatewayWebSocket` 字段触发，不是客户端 environment.ts。

**Followups**（建议下次 ingest 时处理）：
- [ ] 编译 `server/docs/EXECUTION_FLOWS.md`（1143 行）→ 拆成 5–8 个 `flows/*` 页面
- [ ] 编译 `server/docs/M4_SPEC.md`（377 行）→ 增强 `[[milestones/M4-cookie-id-native]]`
- [ ] 编译 `server/docs/TECH.md`（587 行）→ 增强 `[[decisions/tech-choices]]`
- [ ] 编译 `server/docs/FRONTEND.md`（431 行）→ 新建 `flows/client-cutover.md`
- [ ] 摄入 `server/docs/regression/` 下的回归用例 → 新建 `entities/test-suite.md`
- [ ] 给所有 entities 加 `gitnexus context` 验证（运行 `Skill(gitnexus-knowledge)` 后）

**Quality bar**: 本次摄入 **不**做完整代码 grep 验证，所有 `confidence: high` 标记仅当源是 GOAL.md / BACKEND.md / 已读代码文件；其他页 `confidence: medium`。下次 lint 应跑 grep 复核。

---

## [2026-04-28] schema-change | 创建 wiki/CLAUDE.md

确立页面类型 7 种、frontmatter 必含字段、矛盾处理流程。优先级：低于项目根 CLAUDE.md，高于 default 行为。

---

## [2026-04-30] contradiction | "csesapi 切换路线" v1 两处错误纠正

**Trigger**：会话中给出第一版"最低成本切换 Mattermost"分析后，用户即时纠正：
1. bitmap 协议早就废了，新版 im 有自己的同步机制（[[concepts/seq-cursor]]）—— v1 却建议 mapper 翻译 `/posts/getPostsAfterFromSegment` → `after?seq=`
2. snapshot 协议也废了 —— 现在 userSnapshot 业务实际只要 `userId + companyId(=teamId)`，不需要分页 snapshot 协议
3. 客户端"翻译表 vs 直接 rewrite"路线选错 —— 加翻译层 = 永远把 cses-shape 留在客户端代码里，是反向开倒车

**两端**：
- ❌ v1（错）：`syntheses/min-cost-mattermost-cutover` 第一版（**未落盘**，仅在会话里）建议 `route-table.ts` 加 ~37 条 cses-shape → im REST 映射 + DTO mapper ~150 行 + bitmap 调用 mapper 翻译
- ✅ v2（对）：[[syntheses/min-cost-mattermost-cutover]] 当前版 —— **直接 rewrite** 37 条 imHttp 调用面，bitmap 整族**砍掉**（由 [[entities/im-seq-data-source]] + `POST /api/sync` 接管），翻译层 0 引入

**真相验证**：
- bitmap 死的依据：[[concepts/seq-cursor]] + `docs/GOAL.md §1 痛点对照表` + 现 csesapi 仍存的 `post_segment{,_config,_daily_metadata}.go` 6 条接口在新版 im **0 实现**
- 翻译表反模式依据：与 [[decisions/strangler-fig-collapsed]] 后端"不留兼容层"哲学对称——前端也不应留
- snapshot 废弃依据：用户当面确认 userSnapshot 业务只用 `userId + companyId`

**影响页面更新**：
- [[entities/im-api-adapter]] —— 加 §历史误解 节，把"route-table.ts 必须先登记"改成"路径直接对齐 im REST，路由不再做翻译"
- 新增 [[comparisons/csesapi-vs-im-coverage]] —— 125 → 80 全集对账
- 新增 [[syntheses/min-cost-mattermost-cutover]] —— v2 路线图（标 status: stable，frontmatter 在文件顶部）

**沉淀的判定原则**（避免下次再错）：
- 看到旧系统协议（bitmap / segment / snapshot）时，先问"新系统有没有同义机制"，而不是默认要"翻译"
- 看到"加翻译表"念头时，反问"翻译层活多久"——如果是永久的，就是技术债
- 客户端 rewrite 是会话内最便宜的事，生产风险由 [[decisions/no-traffic-rollback]] 兜底

---

## [2026-04-30] ingest | csesapi 全集 + min-cost 路线图沉淀

**Sources consumed**:
- `/Users/mac28/workspace/golangProject/mattermost/server/channels/csesapi/*.go`（19 文件，~125 路由 grep）
- `/Users/mac28/workspace/golangProject/im/server/internal/http/*.go`（22 文件，~80 路由 grep）
- `client/src/pages/message-v3/service/message.service.ts`（52 调用面 grep）
- `docs/HTTP_WS_MAP.md` + `docs/GOAL.md §3` + `SESSION.md §0`（v0.7.2 联调状态）

**Pages created** (2)：
- [[comparisons/csesapi-vs-im-coverage]] —— 125 csesapi 路由按子模块分类 + 三类裁切（im 实现 / 砍 / 留 cses Java）
- [[syntheses/min-cost-mattermost-cutover]] —— v2 路线图（直接 rewrite + bitmap 砍 + im 后端 0 改动）

**Pages updated** (2)：
- [[index]] —— 加 §🪞 Comparisons + §🧬 Syntheses 两个章节
- [[entities/im-api-adapter]] —— 加 §历史误解 节，状态改 stable（修正后稳定）

**Key insights**：
1. csesapi 全集 ~125 路由，分布在 19 个 .go 文件；im /api 已挂 ~80 路由
2. 三类裁切量：im 已覆盖 ~50（直接替换）/ 砍 13（bitmap 整族）/ 留 cses Java ~60（GOAL §3 + Bot/Agent）
3. im 后端**新增 endpoint = 0**，仅可能加 1 个字段（quickReply 关联），且已支持
4. 客户端 rewrite ~37 处，**净代码量减少**（rewrite 比原调用更短 + 死代码清理）
5. Tauri Rust 0~1 行 cfg 改动（确认 `im_seq_sync` feature flag 默认开）

**Followups**：
- [x] 让 cses-client-dev agent 实地核对 37 条 rewrite 的入参/返回 shape（精确到字段）—— 2026-05-01 跑完 Phase 0 audit
- [x] message.service.ts:712 `/channels/load/increment` 调用源 audit —— 确认是 dialog-item 触发的话题增量，非 dead code
- [ ] Tauri Rust audit `channel_processor.rs` 是否还有残留 bitmap segment 调用路径（合并到 Phase 5 readBits 清理）
- [ ] M6 收尾时把 [[milestones/M6-mattermost-decom]] 与本 synthesis 互链

---

## [2026-05-01] ingest | Phase 0 audit + cutover 工程方案

**Trigger**：用户拍板 3 个核心决策（已读机制 1c / 模板数据模型 2a / 模板 WS 同步 3b）+ 全面下线方案需求。

**Sources consumed**:
- cses-client-dev agent Phase 0 audit 报告（33 处 readBits 三类分类 + 已读链路活性 + 加急/状态条现状）
- `client/src/pages/message-v3/components/message-status/message-status.component.ts`（同步 readBits 算法）
- `client/src/pages/message-v3/components/message-template-content/...`（applyReceivedLocally）
- `server/internal/http/message.go`（既有 MarkRead seq 游标实现 + EditMessage 触发 msg_updated 模式）
- `server/internal/repo/channel.go:229 MarkRead` + `server/internal/service/message.go:160 MarkRead`
- `server/internal/gateway/types.go:43 TypeMsgUpdated`
- `server/migrations/`（最新 016，确认无需 017）

**Pages updated** (3)：
- [[syntheses/min-cost-mattermost-cutover]] —— 加 §8 v3 升级，指向工程手册
- [[log]] —— 本条
- [[index]] —— 摄入日期更新（待）

**Pages created** (1, raw 层)：
- `docs/CSES_CLIENT_CUTOVER.md` —— **工程可执行手册**，4 工作日 phase 计划 / im 后端 ~125 行新增 / cses-client 37 条 rewrite 详单 / Tauri Rust 删 100 行 / 12 节完整契约。位置在 raw 层（不在 wiki/syntheses）的理由：方案是工程文档（PR 进度、tag、命令、回滚），非 LLM 知识合成；wiki 只需要索引指针

**Key insights**：
1. 决策 1c "**不建新表**"：直接 JOIN `channel_members.last_read_seq >= message.seq` 算 read-stats，精度 100%，im 后端 0 migration
2. 33 处 readBits 实测分类：删 12 / 改 15 / 保留 6（加急弹窗有 2 处重复实现，必须同步改）
3. 用户说"现在已经没有这个逻辑了" = `inViewMsgRead → onPostRead` 整链路在 im 切换后**运行时永不触发**（readBits 始终空 → unReadCheck 过滤后空 → 不调），可放心整段删
4. 隐性 bug 2 个被 audit 发现：mention 清理依赖 readBits → 静默失效；entryUnreadCount 同样
5. WS V1+M2=16 锁定 0 改动：模板已收到广播复用 `msg_updated`（已存在 EventMsgUpdated）

**Followups**：
- [x] Phase 1 开工时核对全局响应包裹改动是否影响 22 个 handler 文件 + 6 个集成测试断言 —— 实测：测试 buildEngine 没走 envelope 中间件，断言无需改（仅生产 imhttp.New() 路径生效）
- [ ] Phase 4 完工后给 [[entities/im-api-adapter]] 加 v0.7.3 状态更新条
- [ ] 切换全完后开 [[milestones/M6-mattermost-decom]] 观察期文档

---

## [2026-05-01] ingest | Phase 1 im 后端落地完成

**Trigger**：用户拍板「先完善 im 后端，cses-client 前端+Rust 交给团队自己做」+ 3 个核心决策（已读 1c / 模板 2a / WS 3b）。

**Sources consumed**:
- `docs/CSES_CLIENT_CUTOVER.md` Phase 1 §3.1-§3.5
- `server/internal/http/router.go` + 全 22 个 handler c.JSON 模式
- `server/internal/repo/{errors,message,models}.go` + `service/message.go`
- `server/tests/integration/m4_harness_test.go` buildEngine 路径

**Pages updated** (2)：
- [[index]] —— 摄入日期更新到 2026-05-01
- 本 log

**Pages created** (0)：
- 新增的工程文件全部在 raw 层（server/internal/http|repo|service/...），不进 wiki 体系

**Code shipped** (3 commits)：
- `441ba37` feat(http): 加全局响应包裹中间件统一信封格式（response_envelope.go + 100% 单测）
- `66c2a67` feat(message): 新增 POST /api/messages/:id/received 模板已收到端点
- `ee32f7f` feat(message): 新增 GET /api/messages/read-stats 批量读统计端点

**Key insights**：
1. **架构选型纠正**：原计划替换 22 文件 359 处 c.JSON，实测改用 gin 中间件方案 = handler 0 改动 + 单一信封定义点 + 0 业务回归。response_envelope.go 152 行替代了 ~200 行的散点替换。
2. **集成测试 buildEngine 不走 envelope**：测试断言无需改（仍断裸字段），但意味着 envelope 行为只能由单元测试覆盖。response_envelope_test.go 215 行做到 10 函数 100% 行覆盖。
3. **决策 1c "不建新表" 落地**：read-stats 端点用一条 CTE+JOIN+FILTER aggregates SQL 直接算 read/unread，0 migration 0 ETL，性能由 channel_members PK (user_id, channel_id) + last_read_seq 索引保证。
4. **WS V1+M2=16 锁定 0 破坏**：模板已收到广播复用 EventMsgUpdated（既有 line 43）。
5. **新增 Go 代码总量 ~1187 行**（含 ~470 行测试 + ~670 行业务），符合方案预算 ~125 行业务（实际偏多 是因为做了独立文件 separation of concerns 而非塞进既有大文件，且响应包裹比原计划复杂）。

**下一步移交**：
- cses-client 前端团队接手 docs/CSES_CLIENT_CUTOVER.md Phase 2/3/4：
  - Phase 2 模板已收到客户端切 path → POST /api/messages/:id/received
  - Phase 3 onChannelRead 6 处 path 切换 + 砍 onPostRead/inViewMsgRead dead code
  - Phase 4 message-status / 加急弹窗 readBits → 异步 GET /api/messages/read-stats 重构
- Tauri Rust 团队接手 docs/CSES_CLIENT_CUTOVER.md §5：删 handle_post_read（~100 行）

**Followups**：
- [ ] Phase 2-4 完成后回到 wiki 加 ingest 条目
- [ ] 性能基线 k6 跑 GET /api/messages/read-stats P95 ≤ 100ms 验证

# im — Global Goal

> 一句话：**用 Telegram 式的高性能 IM 架构 (im) 全量替换 csesapi（基于 Mattermost 的现有后端），双端不做流量回切、一次性切换。**

---

## 1. 为什么重做

| 痛点（旧：csesapi on Mattermost） | 新方案（im） |
|--|--|
| 客户端同步依赖 bitmap + segment，时间复杂度随频道数爆炸 | **seq 单调计数器**：每频道一个 `last_seq`，客户端只存 `{channel_id → seq}` 游标 |
| 推送跨 pod 缺失，本地 hub 命中即发，其他 pod 用户被静默丢弃 | **跨 pod 推送**：Redis 路由 + Pulsar 投递 + Producer LRU 缓存 |
| 历史 seq 缺序，同毫秒多消息排序错乱 | **MongoDB ObjectId 式 migration_sort_key**：48 bit 毫秒 + 16 bit 同毫秒自增 |
| 业务耦合在 Mattermost 插件层，调优受限 | **自有 Go 服务**，Gin + GORM + PostgreSQL + Pulsar + Redis，全栈可控 |

---

## 2. 成功标准（验证收敛）

替换成功的判定只有一条命令：

```bash
cd server && make verify-all   # ≈ 90 min
```

它内部跑：
1. `verify-build`   — 全量编译 + lint + gosec
2. `verify-unit`    — 所有 `*_test.go`（repo/service/handler/gateway 单元）
3. `verify-integration` — testcontainers 全量 V5 场景
   - V5.1–V5.10 单接口
   - G1–G10 模块组连续流（比如"发消息 → 接收方已读 → 发送方收到 read_sync"算一组）
   - M1 + M2 补覆盖场景（readers / 详情页 / 权限边界）
4. （可选）`scripts/v4-cluster-test.sh` — K8s 集群韧性（3 副本 + 5 种故障注入 + k6 150k WS）

全绿 + CI 绿 + V6 smoke 7/7 = "可以替换"。

---

## 3. 里程碑（详见 `server/docs/BACKEND.md §六`）

| M  | 名称 | 范围 | 状态 |
|----|------|------|------|
| M1 | 核心消息与同步 | auth / channel / message / sync / WS / 跨 pod 推送骨架 | ✅ 完成，tag `v0.1.0-m1-complete` |
| M2 | 企业协作 | 公告 / 治理 / 紧急 / 审批 / 通知 / 定时 / 快捷回复 | ✅ 完成，tag `v0.2.0-m2-complete` |
| M3 | cses-client 切换 + 后端稳定性 + V4 集群韧性 | Topic 子群聊 + Presence；Redis Cluster + 9 OTel metric；Conn.Push race fix + PG 池对齐 HikariCP + FindDM 反向索引；HPA 3→17 pod 弹性 + E2E 13/13；`ImApiAdapter` M1 完整覆盖 | ✅ 全链路 100% 可靠基线（tag `v0.3.2-m3-dm-index`） |
| M4 | **用户身份模型重构** — 删 users 表 / 全部 user FK 改 mm UserID (TEXT) / 加 team_id 列 / 鉴权只信 cookieId | spec + migration 014 + Cookie 单栈 + LRU 缓存 + repo/service/handler/gateway 全量级联 + auth 410 Gone + 单测重建 | ✅ tag `v0.6.0-m4-cookie-id-native` + `v0.6.1-m4-pre-deployed` |
| **v0.7** | **集成测试全集 + harness + WS push hook 完备** | Phase 1 三件套 + Batch-A/B/C/D/E 198 集成测试 + C001-C009 active harness + channel_info/top push hook + envelope 契约 + C008 CI gate | ✅ tag 链 `v0.7.3-im-backend-base`/`harness-base`/`batch-b-tests`/`batch-b-envelope`/`batch-c-tests`/`batch-d-tests`/`batch-e-tests`，**im 后端 production-ready**；单元测试 100% 行覆盖率列 TODO（不阻塞）|
| M5 | 历史数据 ETL | `migration_sort_key` 算法已冻结，迁移脚本待写 | 🗓 TODO（cses-client cutover 完后开）|
| M6 | 下线 Mattermost / csesapi | 全量切换 + 监控观察期 | 🔜 M5 后启动 |

### 不在范围（外部服务，不归 im 拥有）

- `/vote/*` —— 走 Java 投票服务
- `/Im/search/*` —— 走 Java 搜索服务
- 文件分片上传 / 断点续传 —— 外部对象存储
- **模板消息** `/post/templateReceived` —— 走 Java
- **组织架构 / 群模块 / 团队** `/modules`、`/groups`、`/teams/*` —— 走 Java
- Bot / Agent / Webhook 设置 UI —— 延后到 M6（im 实现，非本期）

---

## 4. 硬约束（写在石头上）

1. **WSMessageType 锁定 22 种**（2026-05-07 用户拍板修正口径，详见 `docs/harness/C005`）：V1 12 种（ping / pong / send / send_ack / push_msg / push_ack / sync / sync_resp / read_sync / friend_event / channel_event / msg_updated）+ M1 2 种（msg_deleted / 与 V1 共用 msg_updated）+ M2 4 种（announcement_posted / urgent_posted / approval_updated / notification_received）+ v0.7 4 种（reaction_added / reaction_removed / channel_top_updated / channel_info_updated）。22 中 18 server→client 在 v0.7.3 集成测试中有 active happy path。新增类型需升 V2 RFC + 前后端同步改动。
2. **`AllocSeqAndInsert(ctx, tx *gorm.DB, msg)` 是 seq 唯一入口**：`tx != nil` 复用外部事务，禁止任何绕行写 `messages` 的路径。M4 后 `msg.SenderID` 类型从 int64 → string (mm UserID)。
3. **Pulsar topic 命名**：`persistent://im/push/msg.push.{gatewayID}`；本地调试自动追加 `.{localname}` 后缀避免窜台。
4. **Redis routing TTL = 45s, heartbeat = 15s（3× 容错）**。
5. **不做流量回切**：只能向前修复，不留 feature flag 回 Mattermost。
6. **历史数据 phantom 映射暂不实现**：先保证现有数据正确，历史迁移延后到 M5。
7. **M4 起：im 不维护本地 users 表，所有用户身份信息从 `Redis HASH "User"` 解析**。业务表 user FK 全部 `TEXT` 存 mm UserID（24-char hex MongoDB ObjectId）；im 只在 `messages.{sender_id, team_id}` 冷冻，其他表是 live 关系跟随 cses 侧。鉴权只信 cookieId（admin JWT 后门保留 `/api/admin/*`）。
8. **team_id 来源约定**：`MattermostUser.CompanyID` 优先，空则 `OrgID` 兜底，仍空 = NULL（无 org 用户）。仅在 `channels` 创建时刻冻结一次；`messages.team_id` denormalize 自当时 `channels.team_id`。

---

## 5. 当前总览（2026-05-07 后端 production-ready）

- Backend 功能覆盖率 ≈ **csesapi 的 75%**（剩下 25% = Bot/Agent/Webhook + Templates/Organization + 外部 vote/search/file-chunking）。
- **84 路由 + 22 WSMessageType 落地**；198 集成测试（Batch-A 12 + B 130 + C 27 + D 20 + E 6 + WS ref 1 + push hook 2）／全绿 611s／C008 active CI gate（routes ≥84 / tests ≥190 / family 启发式 grep）。
- 9 条 active harness：C001 AllocSeqAndInsert 唯一入口 / C002 CrossPodPush / C003 PushTopicFor / C004 routing TTL 45s + 心跳 15s × 3 / C005 WS 22 type 锁定 / C006 httpexpect ?-encode / C007 envelope 不双重 wrap / C008 handler-coverage CI gate / C009 middleware-shape sweep。
- 单元测试 100% 行覆盖率列 TODO（service 0.3% / repo 2.9% / gateway 6.7% / http 3.4%；集成测试已间接覆盖 happy path，单测主要补错误分支）。**用户决策不阻塞**。
- tag 链（push origin）：`v0.1.0-m1-*` → `v0.2.0-m2-complete` → `v1.0.0` → `v0.3.0-m3-*` × 6 → `v0.4.0-m3-*` × 3 → `v0.5.0/1/2-*` → `v0.6.0-m4-cookie-id-native` → `v0.6.1-m4-pre-deployed` → **`v0.7.3-{im-backend-base, harness-base, batch-b-tests, batch-b-envelope, batch-c-tests, batch-d-tests, batch-e-tests}`**（当前 HEAD `1caac17`）。
- 下一阶段：cses-client cutover Phase 2-4（前端 onChannelRead / dead code / readBits 异步重构）+ pre 部署 v1.0.0-pre-7h（用户人工触发）。

动态信息请看 `SESSION.md`。

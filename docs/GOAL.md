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
| M3 | cses-client 切换 + 后端稳定性 + V4 集群韧性 | Topic 子群聊 + Presence；Redis Cluster + 9 OTel metric；Conn.Push race fix + PG 池对齐 HikariCP + FindDM 反向索引；HPA 3→17 pod 弹性 + E2E 13/13；`ImApiAdapter` M1 完整覆盖 | ✅ 全链路 100% 可靠基线（tag `v0.3.2-m3-dm-index`）；client `message.service.ts` 37 URI 切换 backlog（M4 完工后再切，避免双重改造）|
| M4 | **用户身份模型重构** — 删 users 表 / 全部 user FK 改 mm UserID (TEXT) / 加 team_id 列 / 鉴权只信 cookieId | spec + migration 014 + Cookie 单栈 + LRU 缓存 + repo/service/handler/gateway 全量级联 + auth 410 Gone + 单测重建 | ✅ tag `v0.6.0-m4-cookie-id-native`；本地 build + go test ./... 全绿；migration 014 / 集成 testcontainers / e2e-pre / 性能基线 仍待跑（详见 SESSION.md §3）|
| M5 | 历史数据 ETL | `migration_sort_key` 算法已冻结，迁移脚本待写 | 🗓 TODO（M4 完成后开） |
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

1. **WSMessageType V1 锁定 12 种**：ping / pong / send / send_ack / push_msg / push_ack / sync_resp / read_sync / friend_event / channel_event / msg_updated / msg_deleted。M2 追加 4 种：announcement_posted / urgent_posted / approval_updated / notification_received。新增类型需升 V2。
2. **`AllocSeqAndInsert(ctx, tx *gorm.DB, msg)` 是 seq 唯一入口**：`tx != nil` 复用外部事务，禁止任何绕行写 `messages` 的路径。M4 后 `msg.SenderID` 类型从 int64 → string (mm UserID)。
3. **Pulsar topic 命名**：`persistent://im/push/msg.push.{gatewayID}`；本地调试自动追加 `.{localname}` 后缀避免窜台。
4. **Redis routing TTL = 45s, heartbeat = 15s（3× 容错）**。
5. **不做流量回切**：只能向前修复，不留 feature flag 回 Mattermost。
6. **历史数据 phantom 映射暂不实现**：先保证现有数据正确，历史迁移延后到 M5。
7. **M4 起：im 不维护本地 users 表，所有用户身份信息从 `Redis HASH "User"` 解析**。业务表 user FK 全部 `TEXT` 存 mm UserID（24-char hex MongoDB ObjectId）；im 只在 `messages.{sender_id, team_id}` 冷冻，其他表是 live 关系跟随 cses 侧。鉴权只信 cookieId（admin JWT 后门保留 `/api/admin/*`）。
8. **team_id 来源约定**：`MattermostUser.CompanyID` 优先，空则 `OrgID` 兜底，仍空 = NULL（无 org 用户）。仅在 `channels` 创建时刻冻结一次；`messages.team_id` denormalize 自当时 `channels.team_id`。

---

## 5. 当前总览

- Backend 功能覆盖率 ≈ **csesapi 的 75%**（剩下 25% = Bot/Agent/Webhook + Templates/Organization + 外部 vote/search/file-chunking）。
- 76 endpoints 全部有集成测试；9 类 WS 事件全部有 fan-out 断言。M4 完工后将增至每端点 5 case + 96 WS 用例。
- CI: M3 main 分支绿；M4 foundation 分支单测绿（auth / middleware / testutil），cascade 进行中。
- tag：`v0.1.0-m1-verified` / `v0.1.0-m1-complete` / `v0.2.0-m2-complete` / `v1.0.0` / `v0.3.0-m3-pre-deployed` / `v0.3.1-m3-racefix-pool300` / `v0.3.2-m3-dm-index` / `v0.4.0-m3-sysmsg-broadcast` / `v0.4.1-m3-markoffline-cleanup` / `v0.4.2-m3-mm-cookie-bridge` / `v0.5.0-config-consul` / `v0.5.1-cookie-auth` / `v0.5.2-m4-foundation` / **`v0.6.0-m4-cookie-id-native`**（当前 HEAD）。
- 下个 tag：`v0.6.1-m4-pre-deployed`（migration 014 跑通 + pre-7 image + e2e 13/13）。

动态信息请看 `SESSION.md`。

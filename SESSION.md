# SESSION — im 项目会话续接

> 这份文档是"会话快照"：每次会话结束前更新一次，下次会话开局只要先读它 + `docs/GOAL.md` + `CLAUDE.md` 就能无缝接着干。
> **更新原则**：事实先写（分支/tag/commit），决策次之，待办最后。过时信息必须删除，不留历史沉积。

Last updated: 2026-04-24

---

## 1. 当前分支 & Tag 全景

| 分支 | 用途 | 最新 commit | 状态 |
|------|------|-------------|------|
| `main` | 稳定主干 | `60ddd3f` Merge cloud-native migration | 稳定 |
| `feature/im-m1-replacement` | M1 核心消息 | 已合并 | ✅ CI 绿 |
| `feature/im-m2` | M2 企业协作 | `226963d` test(integration): friend accept/reject + single add-member push | ✅ CI 绿 |
| `im-backend-switch` (client) | 双端适配 | `85c2b3c34` | ✅ V6 smoke 7/7 |
| `worktree-agent-a961b4cc` | 本 worktree | （本次 doc 落地分支） | 进行中 |

### Tags
- `v0.1.0-m1-verified`
- `v0.1.0-m1-complete`
- `v0.2.0-m2-complete`
- `v1.0.0`

---

## 2. 已完成（截至今天）

### Backend
- **M1**：auth / friends / channel / messages / sync / WS / 跨 pod 推送骨架 + ProducerCache + routing TTL — 全绿。
- **M2**：announcements / channel governance / urgent / approvals / notifications / scheduled / quick replies — 全绿。
- 74 个 HTTP endpoint 全量集成测试覆盖。
- 9 类 WS 事件全部有 fan-out 断言（push_msg / send_ack / read_sync / friend_event / channel_event / msg_updated / msg_deleted / announcement_posted / urgent_posted / approval_updated / notification_received）。
- OTel 三 tracer 全量埋点（service / repo / gateway / Pulsar producer）。
- V4 集群韧性脚本已就绪（`scripts/v4-*.sh`、`v4-load.js`、`cmd/v4-client/`），未落 pre 环境。
- V6 端到端烟测 7/7 通过。

### Frontend
- Angular：`apiFlavor: 'mattermost' | 'im'` 开关 + `ImApiAdapter` 6 个方法已接（login/sync/sendMessage/fetchMessages/markRead/listChannels），其余 4 个为 stub。
- Tauri Rust：`im_client.rs` (~390 行) + `im_seq_data_source.rs` (~253 行) 骨架，`im_seq_sync` feature flag 控制启用。

### 决策冻结点
1. 不切回流量，全量替换。
2. M1 不依赖历史数据 phantom 映射，先跑现有数据。
3. ETL 排序用 MongoDB ObjectId 风格的 `migration_sort_key`（48b ms + 16b 同毫秒自增）。
4. `/vote/*`、`/Im/search/*`、文件分片上传 = 外部服务，im 不拥有。
5. WS V1 12 种类型锁定；新增要升 V2。
6. Pulsar topic 本地调试必须 `.{localname}` 后缀。
7. `AllocSeqAndInsert(ctx, tx, msg)` 是 seq 唯一入口；`tx != nil` 复用外部事务。

---

## 3. 进行中 / 未决

### 进行中
- 本会话：落地 4 份文档（ARCHITECTURE / GOAL / SESSION / CLAUDE），worktree 分支待 commit。

### 待用户拍板的分叉
- **A. Merge PR 到 main**：把 `feature/im-m1-replacement` + `feature/im-m2` 合回 main。
- **B. V4 落 pre 环境**：用 `scripts/v4-prepare.sh` + `kubectl`，跑 k6 150k WS 压测。
- **C. 启动 M3**：Templates / Organization + 前端 F1 真正默认切 `apiFlavor=im`。
- **D. 启动 M5 ETL**：历史数据迁移脚本 + `migration_sort_key` 实现。

### 已知债务
- `cross_pod_push.go` 里 `markOffline(userID)` 还是骨架位，Pulsar `producer.Send` 失败后真正从 routing 摘除用户未实现。
- Angular `ImApiAdapter` 还有 4 个 stub 方法未接（具体清单见 `client/src/app/core/im-api/im-api.adapter.ts`）。

---

## 4. 下次会话开局推荐顺序

1. 读 `CLAUDE.md`（2 min）——知道要按 `/go-concurrency-patterns` 写代码。
2. 读 `docs/GOAL.md`（3 min）——知道全局目标和里程碑。
3. 读本 `SESSION.md`（2 min）——知道当前在哪。
4. 读 `docs/ARCHITECTURE.md` 对应小节（按需）——找要改的文件。
5. 跑 `git status` + `git log -5` ——校验 SESSION 与仓库一致（如不一致，以仓库为准并更新本文件）。
6. 如果要跑验证：`cd server && make verify-all`。
7. 向用户确认 §3 的分叉选哪条，然后开干。

---

## 5. 会话快速命令

```bash
# 查看当前分支状态
git status && git log --oneline -10

# 切到 M2 分支（最新主力）
git checkout feature/im-m2

# 启动本地 infra（Postgres/Redis/Pulsar）
docker compose up -d

# 跑迁移
DATABASE_URL="postgres://im:im@localhost:15432/im?sslmode=disable" make migrate-up

# 启 gateway
make build && ./bin/gateway

# 跑全量验证
cd server && make verify-all

# 客户端烟测
cd client && node scripts/v6-smoke.mjs
```

---

## 6. 更新本文件的规则

- 每次会话**结束前**：至少更新 §1（分支/commit）、§2（新增完成项）、§3（新待决）。
- §4 一般不变；§5 按需补新命令。
- 过时决策**删掉**，不留历史；需要追溯请查 git log。
- 文档目标：让下一次会话 10 分钟内恢复全部上下文。

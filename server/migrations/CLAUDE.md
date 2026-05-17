# CLAUDE.md — server/migrations/ 模块级指令（DB Schema 演进层）

> 本文件仅约束 `server/migrations/` 下的所有 `.up.sql` / `.down.sql`。
> 优先级：用户全局 `~/.claude/CLAUDE.md` > 项目根 `/Users/mac28/workspace/golangProject/im/CLAUDE.md` > `server/CLAUDE.md` > **本文件** > 默认行为。
> 任何 schema 改动**必经本目录**，不存在"先在生产改、后补 migration"的合法路径。

---

## 0. 模块定位

**是什么**：PostgreSQL schema 的**单源事实层**。每一对 `<NNN>_<topic>.up.sql` + `<NNN>_<topic>.down.sql` 文件是 `golang-migrate` 顺序递增编号的不可变契约；编号一旦推到 main，**绝不允许**回头改文件内容（修复只能向前写新编号）。

**负责**：
- ✅ 表 / 列 / 索引 / 约束 / FK 的 DDL 演进
- ✅ 数据回填 (`UPDATE ... WHERE`) / seed 数据（如 `016_m4_modules` 的 6 行 module 种子）
- ✅ trigger / function（如 `trg_*_updated_at` + `update_updated_at()` plpgsql）
- ✅ 与 Go `repo.*` 模型一一对齐（字段名 / 类型 / nullable / 默认值）

**不负责**（**严禁**）：
- ❌ ORM AutoMigrate（GORM 的 `db.AutoMigrate` 在本项目永久禁用；schema 漂移即事故）
- ❌ 业务逻辑（migration 里**禁止** `IF EXISTS user_xxx THEN ...` 之类的业务分支）
- ❌ 手改生产 / pre / dev DB schema 而不写 migration（即便是 `ADD INDEX` 也必须经 migration）
- ❌ 跨 migration 文件互相引用（每个文件自包含，单独 apply 必须能完成它声明的语义）

---

## 1. 影响范围

### 1.1 上游（依赖 migration 的方）

| 上游 | 关系 |
|---|---|
| `server/internal/repo/*.go` | GORM struct field 必须与本目录最新 schema **字段名 / 类型 / nullable 完全对齐**；新增列必须有对应 repo struct 改动同 PR |
| `server/internal/service/*.go` | 任何 SELECT / INSERT / UPDATE 列清单必须存在于当前 head migration 后的 schema |
| `server/cmd/gateway/main.go` / `cmd/message/main.go` | 启动期通过 `golang-migrate` lib 或 `make migrate-up` 把 schema 推到 head；不到 head 拒绝起服务 |
| `server/tests/integration/*_test.go` | testcontainers postgres 容器启动 → 自动 apply `migrations/` 全部 up.sql；migration 红 → 集成测试集体红 |

### 1.2 下游（被 migration 影响的方）

| 下游 | 关系 |
|---|---|
| **PostgreSQL** | dev (`192.168.6.66:32432/im_dev`) / pre / prod 三套集群；本目录是它们 schema 的唯一来源 |
| **docker-compose 启动顺序** | `docker-compose.yml` 必须保证 `postgres` healthcheck 通过后才起 `gateway` / `message`；启动脚本里串 `make migrate-up` |
| **CI / GitHub Actions** | `.github/workflows/*.yml` 的集成测试 job 先跑 `make migrate-up` 再跑 `make verify-integration`；migration 文件名 / 编号变动必触发 CI 全跑 |
| **cses-client / Java cses 桥接** | 不直连 DB，但 wire schema 跟 `docs/IM_DATA_MODEL_新版数据模型字典.md` 对齐，而后者跟本目录对齐 |
| **prod 滚动发布** | C012 风格的大改（PK 类型变更）**不可逆**，需 SRE 单独立项 + 数据备份 + 滚动停服窗口 |

---

## 2. 功能模块清单（当前 active migration 全表）

> 编号严格递增、不允许 gap、不允许复用。下表对应 `ls server/migrations/*.up.sql` 当前真相（HEAD `bfec05e` 之前的全部 23 对文件）。

| 编号 | 主题 | 关键改动 | 状态 |
|---|---|---|---|
| **001** | `init` | M0 初始：users / channels / channel_members / messages / friendships / files / message_attachments / message_favorites / user_settings + 5 个核心索引 + `update_updated_at()` trigger | applied |
| **002** | `m1_message_lifecycle` | M1 软删除 / edit / readers：messages 加 `deleted` / `deleted_at` / `updated_at`；`idx_messages_channel_created` / `idx_channel_members_chid_lastreadseq` / `idx_messages_reply_to` | applied |
| **003** | `m2_channel_governance` | M2 群治理：channel 公告 / 简介 / 头像 / 权限 / orient / picture_url；`channel_managers` 管理员表 | applied |
| **004** | `m2_announcements` | M2 公告体系：`announcements` 表（id + channel_id + creator + scope） | applied |
| **005** | `m2_urgent` | M2 加急：`urgent_messages` 表（message_id + receiver + ack / read 状态机） | applied |
| **006** | `m2_approvals` | M2 审批：`approvals` 表（initiator / approver / status / decided_at） | applied |
| **007** | `m2_notifications` | M2 通知：`notifications` 表（user_id + type + payload + read_at） | applied |
| **008** | `m2_scheduled_messages` | M2 定时消息：`scheduled_messages` 表（schedule_at + status） | applied |
| **009** | `m2_quick_replies` | M2 快捷回复：`quick_replies` 表（user_id + label + content） | applied |
| **010** | `m3_topic_channels` | M3 子群聊：channels 加 `root_id` / `root_message_id` + partial unique；`channel_topics` 表 | applied |
| **011** | `m3_dm_lookup_index` | M3 DM 反向查找：`idx_channels_dm_lookup` 优化 1v1 查找 | applied |
| **012** | `m3_message_props` | M3 messages 加 `props TEXT NOT NULL DEFAULT '{}'`（仅业务扩展，协议字段不进 props） | applied |
| **013** | `m3_user_mm_shadow` | M3 user 影子：把 users.id 扩展为支持 mm UserID 长度 | applied |
| **014** | `m4_userid_text` | M4 用户 ID 全链 TEXT：users.id / channels.creator_id / channel_members.user_id / messages.sender_id 全转 TEXT；`channels.team_id TEXT NULL` 落地（**C011**）；users 接 mm UserID 24-hex | applied |
| **015** | `m4_reactions_top` | M4 reaction + 置顶：`message_reactions`（PK message_id+user_id+emoji）/ channels.is_top / channel_members.is_top | applied |
| **016** | `m4_modules` | M4 业务模块：`modules` 表 + 6 行 seed（MEETING_CHAT / 审批 / 任务 / 成果导向 / 切换公司 / 文档） | applied |
| **017** | `v073_channel_close_and_member_nickname` | v0.7.3 gap：channels.deleted_at（软关停）+ channel_members.nick_name（per-(user,channel) 群昵称） | applied |
| **018** | `id_string_prep_drop_fks` | C012 P-A 第 1/5：删所有指向 messages/channels/announcements 等的 FK 约束（解耦准备） | applied |
| **019** | `id_string_alter_columns` | C012 P-A 第 2/5：`ALTER COLUMN id TYPE TEXT USING id::text` 全部 BIGSERIAL PK + 对应 FK 列 | applied |
| **020** | `id_string_default_ulid` | C012 P-A 第 3/5：所有 ID 列默认值改 `gen_random_ulid()` 或保留 nextval 转 text | applied |
| **021** | `id_string_recreate_fks` | C012 P-A 第 4/5：重建所有 FK 约束（类型已对齐 TEXT） | applied |
| **022** | `id_string_indices` | C012 P-A 第 5/5：B-tree / hash 索引补偿（VARCHAR PK 性能补偿） | applied |
| **023** | `v074_message_mention_list` | v0.7.4 mention：messages 加 `mention_list TEXT[]` + GIN 索引（**禁** JSON 数组，必须 PG 原生数组） | applied |

> **下一个可用编号**：**024**。新增加急广播 / cancel-broadcast / channel_member_mention normalized 表等改动从 024 起编号，单调递增，禁止占用历史编号。

---

## 3. SOP — schema 改动工作流

```
0. 开局：cat SESSION.md | head -80  &&  cat docs/GOAL.md | head -120  &&  ls server/migrations/
1. 改字段前先查 GOAL §4 硬约束 + docs/IM_DATA_MODEL_新版数据模型字典.md 当前字段定义
2. 评估「热路径写入」复杂度（root performance.md §Hot-Path）：
   - 单写 O(1)？ 是 → 单表 / 单列即可
   - 累计 / 队列 / 多值？ → normalized 子表（PK + index），绝不允许 JSON 数组列
3. 生成新 migration 编号（顺序递增 + zero-pad 3 位）：
     make migrate-create name=<scope>_<topic>     # 见 §8.1
4. 写 up.sql：
   - 单一职责（一个文件干一件事，禁止把 5 个无关 ALTER 塞一起）
   - 大表 ADD COLUMN NOT NULL DEFAULT → 评估锁表风险（PG ≥ 11 fast-path，但仍要在 commit body 说明）
   - 列 / 表 / 索引命名遵循现有惯例：snake_case，索引前缀 `idx_<table>_<columns>`
5. 写 down.sql：对称撤销 up.sql（DROP COLUMN / DROP TABLE / DROP INDEX）。
   - C012 风格不可逆改动：down 仍要写，**注释明确标 "irreversible in prod"**，仅用于本地 reset
6. 本地跑 migrate up + down 各一次：
     make migrate-up                               # up 到 head
     make migrate-down N=1                         # 撤销最新一条
     make migrate-up                               # 再 up 一次
   全程 0 报错 → 来回稳。
7. 同步改 server/internal/repo/<table>.go 的 GORM struct（字段 / tag / nullable）
8. 跑 repo 单测：make verify-unit
9. 跑集成测试：make verify-integration（testcontainers 会重新 apply 全部 migration）
10. commit（见 §5）
11. 收尾：更新 docs/IM_DATA_MODEL_新版数据模型字典.md 对应 §2 entity 行 + SESSION.md
```

**特殊路径**：

- 只加索引（不动列） → step 7-8 可跳；step 9 仍跑
- 数据回填（`UPDATE ... SET col = ...`） → 必须在同一个 migration 文件里加，独立成 commit；大表回填先确认锁表时长
- 改既有表的列默认值 → up.sql 用 `ALTER COLUMN ... SET DEFAULT`；down.sql 改回旧默认值
- C012 风格"全链 ID 类型迁移" → **必须**拆 5 个独立 migration（参见 018–022），不许塞一个文件

---

## 4. Pre-commit 自检清单

### 4.1 编号 / 配对（5 项强制）

```bash
cd /Users/mac28/workspace/golangProject/im/server/migrations

# 1. 编号无 gap 无重复（all up.sql 的前 3 位序号必须连续递增）
ls *.up.sql | sort | awk -F_ '{print $1}' | uniq -d            # 必须空
seq -f "%03g" 1 $(ls *.up.sql | wc -l) > /tmp/expected
ls *.up.sql | sort | awk -F_ '{print $1}' > /tmp/actual
diff /tmp/expected /tmp/actual                                   # 必须空

# 2. up + down 一一配对
for f in *.up.sql; do
  base="${f%.up.sql}"
  [ -f "${base}.down.sql" ] || echo "❌ 缺 ${base}.down.sql"
done                                                            # 必须无输出

# 3. 重大改动跑 down → up 来回（手工）
make migrate-up && make migrate-down N=1 && make migrate-up    # 0 错

# 4. grep 列名 / 表名验证 ORM 模型同步
grep -rn "channels\.team_id\|messages\.mention_list" \
  ../internal/repo/ --include="*.go"                            # 必须能命中

# 5. 热路径表 normalized 自检（root performance.md §Hot-Path）
grep -En "TEXT[[:space:]]+(NOT[[:space:]]+NULL[[:space:]]+)?DEFAULT[[:space:]]+['\"]\[\]['\"]" \
  *.up.sql                                                      # 必须 0 条
```

### 4.2 Harness 联动 grep（CI gate，**0 条**）

| Gate | 命令 | 对应 harness |
|---|---|---|
| team_id 不许 NOT NULL | `grep -rEn "team_id\s+TEXT\s+NOT\s+NULL" *.up.sql` | C011 |
| 没残留 BIGSERIAL / BIGINT id（白名单 seq / *_at_unix） | `grep -rEn 'id\s+BIGSERIAL\|id\s+BIGINT' *.up.sql \| grep -v "_seq\|_at_unix"` | C012 |
| 没 RMW 计数列 mention_count / urgent_count BIGINT | `grep -En '(mention_count\|urgent_count)\s+BIGINT' *.up.sql` | C016 |
| 没 JSON 数组列存队列 | 见 §4.1 第 5 项 | C016 + root performance.md |
| 没 DROP COLUMN 直接干掉历史列 | `grep -rEn '^[^-]*DROP COLUMN' *.up.sql`（需 review，原则上禁） | §6.6 |

### 4.3 失败处置

- 编号 gap → 立即 rename 补齐，禁止跳号
- up + down 不对称 → 补 down.sql（即便是"irreversible in prod"也要写一份本地用）
- 来回跑失败 → 看 PG 日志，可能 down.sql 漏 DROP INDEX / DROP CONSTRAINT；补全
- ORM 模型不同步 → 当 PR 内补 `server/internal/repo/*.go` struct 改动，**禁止**留 TODO

---

## 5. Commit 规范

沿用 Conventional Commits + 中文 body 强制（项目根 §3 + 全局 git-workflow.md）。

### 5.1 模板

```
feat(migration/<NNN>): <中文描述 ≤ 50 字>

为什么改 / 是否破坏性 / 是否需要 SRE 滚动停服 / 与哪个 harness 关联。
单行 ≤ 72 字符。
```

### 5.2 示例

```
feat(migration/024): 加 channel_member_mention normalized 表

把 mention 通知从 RMW JSON 数组改 normalized 子表，复合 PK
(channel_id, user_id, msg_id) 保证 push_msg 时 O(1) INSERT，
已读推进时单 DELETE WHERE seq <= ?。对应 C016 §3.2 模板 C。
```

```
feat(migration/025): messages 加 updated_at 单调列 + msg_updated CAS

把 messages.updated_at 从 nullable 改 NOT NULL DEFAULT NOW()，
配合 C016 msg_updated handler 的 CAS WHERE updated_at < ?。
不破坏性：现有 NULL 值在 up.sql 里用 created_at 回填。
```

### 5.3 禁止项

- ❌ `chore: add migration` / `wip: schema` / `update sql`
- ❌ 一个 commit 混 2 个 migration 编号（每条独立 commit）
- ❌ commit body 不解释 why 只描述 what
- ❌ 改既有已 merged 的 migration 文件（哪怕只是改注释），唯一例外：rename + 同步 PR 改 .down.sql

---

## 6. 约束规范（本层强约束）

### 6.1 `channels.team_id` 必须 TEXT NULL（**C011**）

任何 migration 不许把它改成 `NOT NULL`。空 companyId 是合法的"无 team scope"。partial unique index 可加，schema 约束不行。

### 6.2 所有自增数字 PK 全 TEXT（**C012**）

`postId` / `channelId` / `announcementId` / `approvalId` / `reactionId` / 等等：列类型 `TEXT`，Go 侧 `string`。**例外**：`seq`（BIGINT 单调 cursor，不是身份 ID）/ `created_at_unix` 类时间戳 / `users.id`（已是 mm UserID 24-hex TEXT）。新增表必须从设计阶段就走 TEXT，不允许"先 BIGINT 再迁移"。

### 6.3 热路径累计 / 队列字段禁 JSON 数组（**C016 + root performance.md §Hot-Path**）

任何「每条事件至少触发一次落库」的累计 / 队列 / 多值字段：

- ❌ `mention_queue TEXT NOT NULL DEFAULT '[]'`（RMW O(N²) 整体爆炸）
- ❌ `serde_json::to_string(entire_wire_obj)` 塞单列做"无损往返"
- ❌ `channel_members` 加 `urgent_count BIGINT` 之类 RMW 计数列
- ✅ normalized 子表：复合 PK `(channel_id, user_id, msg_id)` + `(channel_id, user_id, seq)` 索引；append = O(1) `INSERT ON CONFLICT DO NOTHING`；清理 = 单 SQL `DELETE WHERE seq <= ?`

**例外**：协议级稳定字段（`is_urgent` / `mention_list TEXT[]` 用 PG 原生数组 + GIN 索引）允许走列；业务自由扩展进 `props TEXT`，且仅业务扩展。

### 6.4 `msg_update` 必须有 `updated_at` 单调列 + CAS（**C016**）

`messages` / `channels` / `channel_members` 等被 update 的表必须：

- 有 `updated_at TIMESTAMPTZ`（已有 trigger `trg_*_updated_at` 维护）
- service 层 UPDATE 走 CAS：`WHERE id = ? AND updated_at < ?`（旧 echo 不覆盖新）
- broadcast 必须携带完整 snapshot（不发 patch），client 端 `ON CONFLICT(id) DO UPDATE WHERE EXCLUDED.updated_at > old.updated_at`

### 6.5 大表 `ADD COLUMN NOT NULL DEFAULT` 谨慎评估锁

PG ≥ 11 对常量 DEFAULT 有 fast-path（无重写）；但**函数 / volatile DEFAULT**（如 `NOW()` / `gen_random_ulid()`）会触发全表重写 + AccessExclusiveLock。`messages` 表 > 10M 行时锁 10+ min。规避：

- 先 `ADD COLUMN ... NULL`（无锁）
- 后台 backfill UPDATE 分批跑（单独 migration）
- 再 `ALTER COLUMN ... SET NOT NULL`（PG 12+ 支持 `NOT VALID` + 后续 `VALIDATE`）

commit body 必须显式声明锁表风险评估。

### 6.6 禁止 `DROP COLUMN`（先 deprecate 后 drop）

历史列删除路径：

1. Migration N：在 commit body 标 `column <name> deprecated since vX.Y, drop in vN+M`
2. 业务代码停写 + 停读（同 PR 验证 grep = 0）
3. Migration N+M（M ≥ 1 个 sprint）：才允许 `DROP COLUMN`

**例外**：从未上 prod 的列（仅本地 dev 跑过）可同一个 migration 加 + 删（视为 typo 修复）。

---

## 7. 对应 Harness 映射

| Harness | 触发场景 | 验证手段 |
|---|---|---|
| [C011](../../docs/harness/C011-channels-team-id-nullable-no-main-flow-block.md) | 改 `channels` / `messages` 表 `team_id` 字段 | grep `team_id\s+TEXT\s+NOT\s+NULL` = 0；`migrate up` 后 `\d channels` 显示 `team_id text nullable` |
| [C012](../../docs/harness/C012-id-type-string-migration.md) | 任何新增表 / 改既有 id 列 / 关联 FK | grep `id\s+BIGSERIAL` = 0（白名单 seq）；`make migrate-reset && make migrate-up` 全过；`scripts/check-id-types.sh` 退 0 |
| [C016](../../docs/harness/C016-msg-update-single-gate-seq-design.md) | 改 `messages` / `channel_members` 的累计 / 队列 / update 路径 | grep `'\[\]'` 默认值 = 0；grep `(mention\|urgent)_count\s+BIGINT` = 0；`updated_at` 列存在；CAS WHERE 模板在 repo 层（白盒单测 `TestUpdateContent_ConcurrentEditSameMessage_NoLost`）|
| **root performance.md §Hot-Path** | 任何新写入路径设计 | 设计前 checklist 6 项过；commit body 解释 O(1) 写复杂度证据 |

---

## 8. Update / Insert 规则 — 新增表 / 列的完整流程

```
1. migration up + down 写好（本目录 §3 SOP）
   ↓
2. server/internal/repo/<table>.go 改 GORM struct（字段名 / 类型 / tag / nullable）
   ↓
3. server/internal/repo/<table>.go 加 / 改 repo 方法（按 §6.4 CAS 模板）
   ↓
4. server/internal/repo/<table>_test.go 加单测（覆盖新字段读 / 写 / 边界）
   ↓
5. server/internal/service/<domain>.go 调用 repo 新方法
   ↓
6. server/internal/http/<family>.go handler 接 service（如涉及对外 endpoint）
   ↓
7. server/internal/gateway/types.go 改 WS payload struct（如涉及推送，参考 C005）
   ↓
8. server/tests/integration/<scope>_test.go 加集成测试（C014 100% 入口覆盖）
   ↓
9. 更新 docs/IM_DATA_MODEL_新版数据模型字典.md §2 entity 表（必须）
   ↓
10. 更新 server/docs/BACKEND.md §4 schema 小节（必须）
   ↓
11. make verify-all + make verify-integration 全绿
   ↓
12. commit + tag（如属 Phase 节点，按项目根 §3.1）
```

### 8.1 新建 migration 文件

```bash
cd /Users/mac28/workspace/golangProject/im/server
make migrate-create name=v074_add_channel_member_mention
# 自动生成 024_v074_add_channel_member_mention.up.sql + .down.sql 空文件
# 编辑后 make migrate-up 验证
```

### 8.2 删表 / 删列流程

按 §6.6：先 deprecate（marker commit）→ 一个 sprint 缓冲 → 再 drop（独立 migration）。**绝不**单一 migration 完成"加 + 删"（除非该列从未上 prod）。

### 8.3 不可逆 / 数据迁移类（C012 风格）

- 拆 5 个独立 migration（drop fks → alter columns → default → recreate fks → indices），各自独立 commit
- 每个 down.sql 写"irreversible in prod"注释
- commit body 必须包含：是否需要 SRE 停服窗口 + 数据备份策略 + 回滚路径（向前修复）
- 推 prod 前需 RFC（不在本目录决策，但本目录是执行落地点）

---

## 9. 文档关联

| 文档 | 在哪 | 用途 |
|---|---|---|
| 数据模型字典 | `/Users/mac28/workspace/golangProject/im/docs/IM_DATA_MODEL_新版数据模型字典.md` | 每个 entity 的字段表（JSON 名 / 类型 / nullable / 默认）；schema 改后**必同步** |
| 后端契约 | `/Users/mac28/workspace/golangProject/im/server/docs/BACKEND.md` §4 | 业务契约层 schema 文档（M1–M6 表设计原因 / 索引选择）；schema 改后**必同步** |
| C011 | `/Users/mac28/workspace/golangProject/im/docs/harness/C011-channels-team-id-nullable-no-main-flow-block.md` | team_id 必须 TEXT NULL |
| C012 | `/Users/mac28/workspace/golangProject/im/docs/harness/C012-id-type-string-migration.md` | 全链 ID TEXT 迁移 5-step Phase |
| C016 | `/Users/mac28/workspace/golangProject/im/docs/harness/C016-msg-update-single-gate-seq-design.md` | msg_update 单闸门 + normalized 表设计 |
| 性能 | `~/.claude/rules/common/performance.md` §Hot-Path Write Complexity | schema 设计阶段就走最优形态，禁"先 ship 再优化" |
| 项目根 | `/Users/mac28/workspace/golangProject/im/CLAUDE.md` §1.6 + §8 | im 项目硬约束 + harness 索引 |
| server 层 | `/Users/mac28/workspace/golangProject/im/server/CLAUDE.md` | server/ 装配层 SOP + Makefile target 表 |
| Harness 索引 | `/Users/mac28/workspace/golangProject/im/docs/harness/README.md` | C001–C016 全表 + 模板 + lifecycle |

---

> 维护：每次新增 / 修改 migration 同步审本文件 §2 表格；新增 harness（C017+）若涉及 schema 必须同步 §7 表。最低契约线是项目根 + root performance.md，本目录可加严不可放宽。

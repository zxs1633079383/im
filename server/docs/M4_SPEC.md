# M4 — 用户身份模型重构 Spec

> 状态：draft, 待用户拍板锁死 / Owner: backend / 起草日期：2026-04-27
> 前置文档：`docs/GOAL.md` §3 里程碑 M4、`SESSION.md` §3 "M4：用户身份模型重构"
> 关联 migration：014（本期）；保留 013 的 `mm_user_id` 列**只为兼容期内卸载**，014 的最后一步会一并删除
>
> **先决条件**：本期不做流量回切（决策冻结点 1），所以**不需要 dual-write、不需要数据回填**。`im_pre` / `im_dev` 视同空库，TRUNCATE → re-migrate。

---

## 1. 目标与非目标

### 1.1 目标
1. **删 `users` 表**，im 不再"开户"。所有 user 字段改为存 Mattermost UserID（24-char hex，MongoDB ObjectId 风格）。
2. **删 `mm_user_id` 影子映射** 与 lazy-upsert 链路，cookieId 中间件只负责 **parse + 注入 ctx**，不写 PG。
3. **加 `team_id TEXT NULL`** 到 `channels` / `messages`（来源：`MattermostUser.CompanyID`，无公司用户允许 NULL）。
4. **`messages` 表上"冷冻" `(sender_id, team_id)`**：发送那一刻把 sender 的 mm UserID + 当时所属 team_id 记到行里，**即使 mm 那边之后改了 user.companyId / 删了用户，历史消息显示与归属不变**。
   - 适用范围：**仅 `messages` 表**（`messages.team_id` denormalize 自当时 channels.team_id）。
   - **其他表是 live 关系**，不冻结：`channel_members.user_id` / `friendships.{requester,addressee}_id` / `files.uploader_id` / `approvals.*_user_id` 都是实时引用，mm 删用户后这些行的语义跟随 cses 侧（im 不做引用完整性）。
   - **channels.team_id**：channel 创建时刻冻结（创建者 team 当时是什么就是什么），creator 后来换公司不改 channel 归属。
   - im 不复制 name/email/avatar/orgRole 等 MM 画像字段；前端拿 user_id 自己从 MM Redis / cses 取（详见附录 A）。
5. **鉴权统一只信 cookieId**（`MattermostCookieAuth` → `CookieRequired`）。JWT 路径退役（保留 admin-only 后门，见 §6.2）。
6. **全量回归测试集合改造**：每个 HTTP 端点 5 case + 每种 WSMessageType 在两种 team_id 状态下的发送 / 接收 / ACK。

### 1.2 非目标（明确不做）
- ❌ Dual-write / 双跑 BIGINT 与 TEXT 两套字段（不回切，没意义）
- ❌ 历史数据 backfill（pre/dev 视同空库，TRUNCATE 即可；prod 还没切）
- ❌ Mattermost 反向迁移路径（不可逆）
- ❌ M5 历史 ETL（独立里程碑，本期不动）
- ❌ 修改 WS V1+M2 的 16 种事件类型（锁定，新增升 V2，决策冻结点 4）

---

## 2. 数据模型

### 2.1 字段类型对照

| 表 | 字段 | M3 类型 | M4 类型 | 说明 |
|----|------|--------|---------|------|
| `users` | 整表 | — | **DROP** | 不再开户；cookieId 即身份 |
| `channels` | `creator_id` | `BIGINT REFERENCES users(id)` | `TEXT NOT NULL` | mm UserID |
| `channels` | `team_id` | — | `TEXT NULL` | **新增**，来源 MMUser.CompanyID |
| `channel_members` | `user_id` | `BIGINT REFERENCES users(id)` | `TEXT NOT NULL` | mm UserID，PK 仍为 `(user_id, channel_id)` |
| `messages` | `sender_id` | `BIGINT REFERENCES users(id)` | `TEXT NOT NULL` | mm UserID |
| `messages` | `team_id` | — | `TEXT NULL` | **新增**，写入时从 channels 复制（denormalize for query） |
| `messages` | `visible_to` | `BIGINT[]` | `TEXT[]` | mm UserID 数组 |
| `friendships` | `requester_id` / `addressee_id` | `BIGINT REFERENCES users(id)` | `TEXT NOT NULL` | mm UserID；UNIQUE 索引保留 |
| `files` | `uploader_id` | `BIGINT REFERENCES users(id)` | `TEXT NOT NULL` | mm UserID |
| `message_favorites` | `user_id` | `BIGINT REFERENCES users(id)` | `TEXT NOT NULL` | mm UserID，PK `(user_id, message_id)` |
| `user_settings` | `user_id` | `BIGINT PK REFERENCES users(id)` | `TEXT PRIMARY KEY` | mm UserID |
| `announcements` | `creator_id` 等 | `BIGINT REFERENCES users(id)` | `TEXT NOT NULL` | mm UserID |
| `urgent_*` / `approvals` / `notifications` / `scheduled_messages` / `quick_replies` | 任何 `*_user_id` / `*_id REFERENCES users(id)` | `BIGINT` | `TEXT NOT NULL` | mm UserID |
| `urgent_acks.user_id` | — | 同上 | mm UserID |

> **取消所有 `REFERENCES users(id)`**。im 不再有 users 表权威；mm 数据外部存活，im 不做引用完整性。

### 2.2 ID 生成与外部协议
- **Caller-supplied**：所有写路径的 user 字段从 `c.Get(UserIDKey).(string)` 取，不接受请求 body 里的 `user_id`（除非显式管理动作，如"踢人"）。
- **格式校验**：进入 service 层前必须 `len(uid) == 24 && isHex(uid)`，否则 400。统一在 handler 入口校验。
- **空字符串**：禁止。`""` 视同未鉴权，返回 401。

### 2.3 team_id 来源与默认值
- 优先级：`MattermostUser.CompanyID` → `MattermostUser.OrgID` → NULL（无公司用户）
- 单组织时 cses 写入约定 `companyId == orgId`（参考 SESSION.md §3 "参考真实 cookie 档案 schema"），所以一般取 `CompanyID` 即可。
- **写入时点**：
  - 创建 channel：`channels.team_id = caller.team_id`（DM 时 = 发起方的 team_id；跨 team DM 暂不支持，下游 fail-fast）
  - 发送 message：`messages.team_id = channels.team_id`（denormalize，便于 message 查询不 JOIN channels）
- **读取时过滤**：列表类端点（`GET /api/channels`、`GET /api/messages`）默认按 `team_id = caller.team_id OR team_id IS NULL` 过滤；明确传 `?include_other_teams=1` 才放开（管理员场景，见 §6.2）。

---

## 3. 鉴权链路

### 3.1 中间件链（变化）

**M3 现状**（`v0.5.1`）：
```
authedAPI.Use(MattermostCookieAuth(rdb, users, log))   // lazy upsert + 注入 UserIDKey(int64)
authedAPI.Use(JWTOrCookie(secret))                      // 双栈：JWT 或 cookie 已注入即过
```

**M4 目标**：
```
authedAPI.Use(MattermostCookieResolve(rdb, log))       // 仅 parse + 注入 *MattermostUser + UserIDKey(string)
authedAPI.Use(CookieRequired())                        // cookieId 缺失 → 401
```

- `MattermostCookieResolve`：去掉 `users repo.UserRepo` 参数（不再 upsert）；命中后 `c.Set(UserIDKey, mmUser.UserID)`（**string**）+ `c.Set("im_team_id", mmUser.CompanyID)`。
- `CookieRequired`：取代 `JWTOrCookie`。仅检查 `c.Get(UserIDKey)`，无即 401。
- `JWTGin` / `JWTOrCookie` **保留代码**但不再挂在 `authedAPI` 链路上。仅 `/api/admin/*`（如有）选挂（见 §6.2）。

### 3.2 cookieId Redis lookup 缓存策略
- **本地 LRU**：进程内 `hashicorp/golang-lru/v2`，capacity = 10_000，TTL = 30s（cookieId 短命，超时强制刷）。
- Key：cookieId 字符串（已是 24-hex，无需 hash）。Value：`*MattermostUser`。
- 缓存命中 → 直接 c.Set（不打 Redis）。
- Miss / TTL 过期 → 走原 200ms HGET 流程，写回 LRU。
- **失效**：现阶段不做主动 invalidation；30s TTL 自然过期足够，cookieId 失效后第二次请求自动重 lookup 命中 redis.Nil → 401。
- **指标**：埋 OTel counter `im.auth.cookie_cache.{hit,miss}` 看命中率，<70% 则上调 capacity / TTL。

### 3.3 unauth 路径
- `/api/auth/register` / `/api/auth/login`：**整组删除**。前端不再走 im 注册登录。
- `/api/health`：保留 unauth。
- `/metrics` / `/debug/*`：保留（Prometheus / pprof）。

---

## 4. 代码层影响

### 4.1 类型签名
- `repo.User` 模型：删除（或仅作 in-memory snapshot，不持久化）。
- `repo.UserRepo` 接口：删除（连 mocks）。
- `repo.MessageRepo.AllocSeqAndInsert(ctx, tx, msg)`：`msg.SenderID` 由 `int64` 改 `string`。
- 所有 repo 方法签名：参数 / 返回值里的 user-id 类型从 `int64` 全部改 `string`。
- 所有 service 层 params struct：同样改 string。
- handler 入口 `userIDFromCtx(c)`：返回 `(string, bool)`，校验 24-hex。

### 4.2 并发与上下文（保持 `go-concurrency-patterns` 规则）
- cookie 中间件 LRU 单例 `sync.Once` 初始化。
- 所有 cookie lookup 走原 200ms timeout（保留）。
- 缓存读用 lru.Cache 自带的并发安全；不要用裸 map+RWMutex 重新发明。

### 4.3 ID 校验工具
- 新增 `internal/auth/userid.go`：
  ```go
  // ValidateUserID returns nil iff s is exactly 24 lowercase-hex chars.
  func ValidateUserID(s string) error { ... }
  // MustUserID panics on invalid — only for tests.
  func MustUserID(s string) string { ... }
  ```
- 在 handler 入口、service 入口、AllocSeqAndInsert 入口都调一次（fail-fast）。

### 4.4 文件清单（影响）
- migration：新增 `014_m4_userid_text.{up,down}.sql`（DROP users、所有 FK 改 TEXT、加 team_id 列）；M3 的 013 由 014 一并 cleanup。
- middleware：改 `mattermost_cookie.go` / 新增 `cookie_required.go` / 删 `jwt_gin.go` 在 router 链中的引用。
- handler：17 个文件全改 `userIDFromCtx` 返回类型；约 76 处调用点。
- repo：13+ 个文件签名改造。
- service：所有 *Params struct 字段改 string。
- tests：`v5_*_test.go` 全部 user fixture 从 int64 → mm UserID；新增 `m4_*_test.go` 系列覆盖 5 case。
- ImApiAdapter（cses-client）：调用方仍然只传 cookie；无需新增 user_id 字段（im 自己从 cookie 解出）。

---

## 5. Migration 014 策略

### 5.1 路径选择：DROP & RECREATE（不做 in-place ALTER）
理由：
- BIGINT → TEXT 的 `USING` cast 没有语义合理的转换（int64 user id 已无对应 mm id）。
- 13 个 FK 一一 drop / recreate 太脆。直接重建表更短、更可读。
- **pre/dev 是测试库**，无生产数据需保留。

### 5.2 014.up.sql 步骤（顺序锁死）
```sql
-- 0. 安全闸：仅在测试环境跑（CI 强校验 IM_ENV != "prod"）
-- 1. 删 trigger
DROP TRIGGER IF EXISTS trg_users_updated_at ON users;
DROP TRIGGER IF EXISTS trg_user_settings_updated_at ON user_settings;
DROP TRIGGER IF EXISTS trg_friendships_updated_at ON friendships;
DROP TRIGGER IF EXISTS trg_channels_updated_at ON channels;

-- 2. 删依赖 users 的所有表（CASCADE 会带 FK）
DROP TABLE IF EXISTS user_settings, message_favorites, message_attachments,
                     friendships, files, messages, channel_members, channels,
                     announcements, announcement_reads, urgent_messages,
                     urgent_acks, approvals, notifications, scheduled_messages,
                     quick_replies, channel_managers
                     CASCADE;
DROP TABLE IF EXISTS users CASCADE;

-- 3. 重建 — 用 mm UserID 作为 user 维度 FK 替代
CREATE TABLE channels (
    id          BIGSERIAL    PRIMARY KEY,
    type        SMALLINT     NOT NULL,
    name        VARCHAR(100) NOT NULL DEFAULT '',
    avatar_url  TEXT         NOT NULL DEFAULT '',
    seq         BIGINT       NOT NULL DEFAULT 0,
    creator_id  TEXT         NOT NULL,
    team_id     TEXT         NULL,
    root_id     BIGINT       NULL REFERENCES channels(id),  -- M3 Topic
    root_message_id BIGINT   NULL REFERENCES messages(id),
    created_at  TIMESTAMPTZ  NOT NULL DEFAULT NOW(),
    updated_at  TIMESTAMPTZ  NOT NULL DEFAULT NOW()
);
CREATE INDEX idx_channels_team ON channels(team_id) WHERE team_id IS NOT NULL;
CREATE INDEX idx_channels_creator ON channels(creator_id);
-- ... 其他表同理（详见实现 PR）
```

### 5.3 014.down.sql
- 还原到 M3 schema（重建 users 表 + 所有 FK 回到 BIGINT）。
- **down 仅作"语法 sanity check"用**，不期望回滚（生产不回切）。

### 5.4 数据 backfill
- **pre/dev**：跑完 014.up 之后即空库；新数据从 cookie 来。
- **prod**：M4 只在替换那一刻一次性切；`im` 在 prod 还没接 → 不存在 backfill。

---

## 6. 兼容期与 admin 后门

### 6.1 兼容期内 v0.5.1 客户端
- v0.5.1 cses-client 已经在带 cookieId header（`v0.4.2-m3-mm-cookie-bridge` 起）。M4 不动客户端协议，**仅删服务端 lazy-upsert 与 JWT 注册路径**。
- 若 cses-client 仍尝试 POST `/api/auth/register`：返回 410 Gone + 错误体 `"register endpoint retired in M4; cookie auth only"`。

### 6.2 admin / 工具脚本后门
- 保留 `JWTGin` 实现，但只挂在 `/api/admin/*`（如有）；`JWT_ADMIN_SECRET` 与原 `JWT_SECRET` 物理隔离（环境变量分开）。
- 所有 `make verify-*` / load 脚本 / e2e harness：用 fixture cookieId 而非 JWT。
  - 提供 `scripts/seed-mm-cookies.sh`：在 Redis HASH `User` 写入测试 cookieId → mm 用户 JSON 的样例数据。
  - e2e harness 读 `tests/fixtures/mm-cookies.json` 拿测试 cookieId，请求时带 `cookieId: <id>`。

---

## 7. 测试需求（用户：单接口 + 测试用例集 全量回归）

### 7.1 HTTP 端点 5 种 case（76 个端点都要覆盖）

| Case | 描述 | 期望 |
|------|------|------|
| C1 success | 合法 cookieId + Redis 命中 + team_id present | 2xx |
| C2 cookie missing | 无 `cookieId` header | 401 + "missing auth" |
| C3 cookie invalid | cookieId 在 Redis 不存在 / 解析失败 | 401 + "missing auth" |
| C4 team_id null | mm 用户的 CompanyID 为空 | 200 + 路径根据语义（公共池可见/私域不可见） |
| C5 team_id mismatch | 资源所属 team_id != caller team_id 且非 admin | 403 / 资源不可见 |

- 测试位置：新增 `server/tests/integration/m4_<scope>_test.go`，每个 scope = 一个 handler 文件。
- Helper：`testutil.CookieFixture(t, mmID, teamID)` → 写 Redis HASH "User" + 返回 cookie header。
- 现有 `v5_*_test.go` 全部改造：fixture 从 `int64(1)` → `testutil.MustUserID("...24hex...")`。

### 7.2 WS 路径
- 16 种 WSMessageType（V1 12 + M2 4）× 2 种 team_id 状态（present / null）× 3 种动作（send / receive / ack）= 96 用例最小集。
- 复用 `tests/integration/ws_*_test.go` 体例，新增 `m4_ws_*_test.go`。

### 7.3 覆盖率门槛
- `internal/middleware/mattermost_cookie.go` + `cookie_required.go` + `auth/userid.go`：**100% 行覆盖 + 100% 分支**（按 `~/.claude/rules/golang/testing.md` 硬要求）。
- 其他文件：随 M3 现状（package 90%+，整体 80%+）。
- `internal/repo` 单测覆盖率：从 2.1% 拉到 **15%+**（趁 schema 重构补 sqlmock）。

### 7.4 性能基线
- M4 完工后跑一次 `pre-7` 压测（脚本不变），P95 应 ≤ pre-6（多 1 次 LRU lookup，期望 < 5ms 增量）。
- 缓存命中率 metric 应 ≥ 90%（VU=300 时同 cookie 反复命中）。

---

## 8. 执行顺序（建议双周冲刺）

### Week 1
1. **Day 1**：spec 锁死 + 测试 fixture 体系准备（`testutil.CookieFixture` + `seed-mm-cookies.sh`）。
2. **Day 2**：migration 014.up/down + 在 `im_dev` 跑通；CI 加 `IM_ENV != prod` guard。
3. **Day 3**：`internal/auth/userid.go` + 改 `MattermostCookieResolve` + 新 `CookieRequired` 中间件 + 单测 100%。
4. **Day 4–5**：`internal/repo` 13 个文件签名改造 + sqlmock 单测拉到 15%。

### Week 2
5. **Day 6–7**：`internal/service` 改造（user-id 类型 string）；该层无 DB 依赖，纯类型推导。
6. **Day 8**：17 个 handler `userIDFromCtx` 返回类型 string + 76 个调用点；测试一起改。
7. **Day 9**：m4_*_test.go 集合（76 端点 × 5 case + 96 WS 用例）；e2e-pre.mjs 跑通。
8. **Day 10**：性能基线 pre-7 压测 + Grafana cookie LRU 命中率 panel + tag `v0.6.0-m4-cookie-id-native` + SESSION.md 更新。

---

## 9. 决策点（**需用户拍板才动代码**）

| # | 决策点 | 默认提议 | 备选 |
|---|--------|---------|------|
| D1 | user-id Go 类型 | `string`（最朴素） | 引入 `type UserID string` 别名（更类型安全，但 76 处改造工程量+20%） |
| D2 | team_id 来源优先级 | `CompanyID` → `OrgID` → NULL | 直接用 `OrgID`（`organizes[]` 多组织场景如何选还需 cses 协议确认） |
| D3 | 列表过滤 team_id 默认行为 | `team_id = caller.team_id OR team_id IS NULL` | 严格 `team_id = caller.team_id`（NULL 资源管理员才能看） |
| D4 | JWT 注册登录端点处理 | 直接 410 Gone | 软退役（保留实现 + 日志告警，1 个版本后再删） |
| D5 | LRU TTL | 30s | 5 min（命中率↑、cookieId 失效感知↓）/ 60s 折中 |
| D6 | LRU 缓存 backend | `hashicorp/golang-lru/v2` | `freecache` / 自建 sync.Map + ring buffer |
| D7 | Migration 014 形态 | DROP & RECREATE 整库 | in-place ALTER（保留数据，工程量×3） |
| D8 | `visible_to BIGINT[]` 改 `TEXT[]` | 改 | 留 BIGINT[]（hash mm-id 后强转，不推荐——丢可读性） |
| D9 | admin JWT 通道 | 保留 `/api/admin/*` 仅挂 JWTGin | 完全删 JWT，admin 也走特殊 cookieId |
| D10 | cookie 缺失 warn → 删 | 删（M4 后是 hard 401，不再需要 migration warn） | 保留再观察一版 |

---

## 10. 风险与回滚

| 风险 | 影响 | 缓解 |
|------|------|------|
| cookieId Redis 抖动导致大面积 401 | 全站不可用 | LRU 缓存 30s + 200ms 超时限流 + 加 OTel alert "cookie miss rate > 20%" |
| mm UserID 在某些 fixture 不是 24-hex（旧 mock 数据） | 测试大面积失败 | 切换前先全量 grep `int64(1)` 等魔法数字替换为 `MustUserID` 生成 |
| team_id 推断错误（CompanyID vs OrgID） | 跨 team 资源可见性错误 | D2 决策 + 单测 C4/C5 case 强制覆盖 |
| 14.up 在 prod 误跑（虽然 prod 还没接） | 数据丢失 | CI guard `IM_ENV != prod`；migration 工具加 `--allow-destructive` 显式开关 |
| LRU 内存泄漏 | OOM | capacity=10_000 上限封顶；TTL 强制驱逐；OTel gauge `im.auth.cookie_cache.size` |

回滚：`014.down.sql` 重建 users 表 + BIGINT FK，但**生产不回切是冻结决策**，down 仅作 dev 重置用。

---

## 11. 最后验证环节（M4 收尾门禁）

> 全部 8 项**必须**绿，缺一不打 tag、不更新 SESSION.md。门禁脚本 `make verify-m4` 串起前 6 项。

### 11.1 本地构建 & 静态检查
- `cd server && make verify-all` 全绿（build + vet + race unit + integration testcontainers）
- `golangci-lint run ./...` 零告警
- `grep -rn "int64.*UserID\|int64.*sender_id\|int64.*user_id" server/internal/` 应只剩**测试 helper 注释**或被显式标注的兼容代码

### 11.2 Schema 漂移检查
- `migrate -path migrations -database "$IM_DEV_DSN" version` → 应到 014
- `migrate -path migrations -database "$IM_DEV_DSN" down 1 && up 1` 跑得通（down/up 互逆 sanity）
- `psql -c "\d+ messages"` 验证：`sender_id TEXT NOT NULL`、`team_id TEXT NULL`、`visible_to TEXT[]`，**没有任何 `BIGINT REFERENCES users`**
- `psql -c "\dt users"` → 表不存在
- `psql -c "\d users"` → relation does not exist（migration 014 已删）

### 11.3 测试集合
- `go test -race ./...` 全绿
- `go tool cover -func=coverage.out | awk '$3 != "100.0%" && /middleware\/(mattermost_cookie|cookie_required)|auth\/userid/'` → 空（鉴权 3 文件 100% 覆盖）
- `go test ./internal/repo/... -cover` → ≥ 15%
- `m4_*_test.go` 系列（76 端点 × 5 case）全绿
- `m4_ws_*_test.go`（96 用例最小集）全绿
- 现有 `v5_*_test.go` 改造后全绿（user fixture 类型已切 string）

### 11.4 集成 / e2e
- `kubectl apply -f deploy/k8s/rendered/` rollout 成功（im-v2 ns 3/3 Running）
- `IM_GATEWAY=http://localhost:38080 node scripts/e2e-pre.mjs` → **13/13 PASS**（同 M3 标准）
- pod logs：0 panic / 0 RESTARTS（`kubectl -n im-v2 get pods -w` 跑 10 min 观察）
- `scripts/e2e-teardown.sh` 跑得通（PG 清空 + Redis `im-new:*` 清 + Pulsar topic 清）

### 11.5 性能基线（pre-7）
- `TARGET_VUS=300 scripts/apply-k6.sh` 跑完
- 对比 `server/docs/benchmark/2026-04-24-1407-summary.md`（pre-6 基线）：
  - `action_ok` ≥ 99%（不退化）
  - `send P95` ≤ 400ms（pre-6 = 375ms，多 1 次 LRU lookup 容许 +25ms 噪声）
  - `http_failed` = 0%
  - SLOW SQL = 0
- 留档 `server/docs/benchmark/$(date +%Y-%m-%d-%H%M)-pre-7-summary.md`

### 11.6 观测验证
- Grafana `im-v2-main` dashboard 新增 panel：
  - `im.auth.cookie_cache.hit_rate` ≥ 90%（VU=300 时）
  - `im.auth.cookie_cache.size` 稳定在 capacity 内（不爬升 OOM）
- OTel alert：`cookie_miss_rate > 20% for 5min` 配置就位（artillery 故意打错 cookie 验证 alert 能触发）
- `kubectl -n im-v2 logs -l app=im-gateway --tail=200 | grep -i "lazy.upsert\|UpsertByMattermostID\|users table"` → 应无匹配（验证 lazy-upsert 链路彻底死）

### 11.7 客户端冒烟（cses-client）
- `cd /Users/mac28/workspace/angular/cses-client && node scripts/v6-smoke.mjs` → **7/7 PASS**
- 手测 4 条核心：sendMessage / markRead / revokeMessage / queryIncrementTopics（cookie 鉴权下跑通）
- v0.5.1 客户端**不需要重发**（M4 只删服务端注册登录端点 + 改库结构，cookie 协议不变）

### 11.8 安全 & 防误炸
- CI 在 PR 上跑 `IM_ENV=ci` 时 migration 014 必须跑得通（CI db）
- CI 在 `IM_ENV=prod` 时 migration tool **拒绝** 014（必须 `--allow-destructive` 显式给）
- `grep -rn "/api/auth/register\|/api/auth/login" server/internal/` → 应只剩 410 Gone 兜底 handler
- `grep -rn "JWTGin\|JWTOrCookie" server/internal/` → 仅出现在 `/api/admin/*` 路径（D9）和退役死代码注释里

### 11.9 文档同步（gate 8 通过后）
- 见 §12 文档更新清单

---

## 12. 文档更新清单（M4 完工时一并刷新）

- `docs/GOAL.md` §3 → M4 标"已完成"
- `SESSION.md` §1（tag `v0.6.0-m4-cookie-id-native`）/ §2（M4 完成项） / §3（移除 M4 段，加 M5 ETL 待决）
- `server/docs/BACKEND.md` §X 鉴权链 + §3.3 sync / §4.1 AllocSeqAndInsert（参数类型）
- `server/docs/ARCHITECTURE.md` 数据模型表 + 鉴权流程图
- `docs/HTTP_WS_MAP.md` 加一列 "team_id 过滤行为"
- 新文档 `docs/AUTH_COOKIE.md`：cookie middleware + LRU + Redis HASH 协议详解（替代散落在 commit message 里的笔记）

---

## 附录 A — `MattermostUser` 字段在 im 持久化规则

> "冷冻"严格只指 `messages` 表的发送时刻快照（`sender_id` + `team_id`）。其他表是 live 关系。

| 字段 | 在 messages 表冻结？ | 在其他表 | 来源 / 用途 |
|------|------------|--------|-------------|
| `userId` / `id` | ✅ `messages.sender_id` 冻结 | live 引用（channel_members / friendships / files / approvals 等的 user_id 列） | 所有外键 |
| `companyId` | ✅ `messages.team_id` 冻结（denormalize 自当时 channels） | `channels.team_id` 创建时刻冻结一次 | 资源归属 + 列表过滤 |
| `orgId` | ⚪ 仅作 companyId 兜底 | 同左 | D2 决策点 |
| `name` / `userName` / `mobile` / `email` / `avatar` | ❌ 不存 | ❌ 不存 | 前端从 MM Redis / cses 取 |
| `roles` / `permissions` / `orgRole` | ❌ 不存 | ❌ 不存 | 鉴权走 cses，im 不持久化 |
| `deptId` / `deptName` / `openId` / `unionId` | ❌ 不存 | ❌ 不存 | im 完全不感知 |
| `mattermostHttp` / `mattermostWebSocket` | ❌ 不存 | ❌ 不存 | 客户端配置项 |

**指导原则**：im 只关心 *who* 和 *which team*。其余画像数据 MM 是权威源，im 不复制。**冷冻 ≠ 冗余存档**：仅 `messages` 行需要冻结历史归属，其他实时关系跟随 mm 删改。

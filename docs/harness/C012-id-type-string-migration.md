---
id: C012
title: postId / channelId 等所有自增数字 ID 全链改 string（BIGINT → TEXT / int64 → string）
status: active
created: 2026-05-13
last_recurred: 2026-05-14
recurrence_count: 1
source_logs:
  - 客户端 worktree feat/im-reactor-2 用户拍板（2026-05-13）
  - C012 P-E 整体完成: 2026-05-14（P-A/B/C/D/E 五阶段累计 5 commits + 1 docs commit）
applies_to:
  - server/migrations/*.sql
  - server/internal/repo/**/*.go
  - server/internal/service/**/*.go
  - server/internal/http/**/*.go
  - server/internal/gateway/**/*.go
  - server/cmd/**/*.go
  - server/tests/integration/**/*.go
inline_target: server/docs/CSES_CLIENT_内部对接契约.md §12（升级 ImMessage / ImChannel id 字段定义）
---

# C012 — postId / channelId 等所有自增数字 ID 全链改 string

> **用户拍板**：2026-05-13，cses-client feat/im-reactor-2 worktree。
> 客户端 worktree 镜像：`docs/harness/C005-id-type-string-client-downstream.md`
> Spec 责任方：im 后端 owner；执行方：autonomous im 后端 agent + cses-client agent 协同。

## 1. 触发场景（Trigger）

**适用**：当前后端**所有**以 `BIGSERIAL / BIGINT` 作为主键 / 外键的实体——`messages` / `channels` / `announcements` / `approvals` / `notifications` / `urgent_*` / `scheduled_messages` / `favorites` / `channel_pins` / `channel_topics` / `reactions` / `message_attachments` / `friend_requests` 等。

**不适用**：
- `users` 表 id（M4 起已经是 `TEXT`，mm UserID 24-hex）
- `team_id` / `company_id`（已 TEXT，C010/C011）
- `seq` 列（保留 `BIGINT`，仍是单调自增 cursor，不是身份 ID）
- `created_at` / `updated_at` 等时间戳

## 2. 错误模式（Anti-Pattern / 现状）

```sql
-- ❌ 现状 migrations/001_init.up.sql
CREATE TABLE messages (
    id           BIGSERIAL    PRIMARY KEY,
    channel_id   BIGINT       NOT NULL REFERENCES channels(id),
    ...
);

-- ❌ 现状 migrations/004_m2_announcements.up.sql
CREATE TABLE announcements (
    id          BIGSERIAL PRIMARY KEY,
    channel_id  BIGINT    NOT NULL REFERENCES channels(id) ON DELETE CASCADE,
    ...
);
```

```go
// ❌ 现状 internal/repo/announcement.go
type Announcement struct {
    ID        int64 `gorm:"primaryKey;autoIncrement"`
    ChannelID int64 `gorm:"column:channel_id;not null"`
    ...
}

// ❌ 现状 internal/repo/favorite.go
func (r *gormFavoriteRepo) Add(ctx context.Context, userID string, messageID int64) error {
```

```go
// ❌ 现状 internal/http/message.go
func parseMessageID(c *gin.Context) (int64, bool) {
    id, err := strconv.ParseInt(c.Param("id"), 10, 64)
    if err != nil { ... }
    return id, true
}
```

**问题**：
- 公告 / 模板 / 加急等业务消息在不同 endpoint 类型不一致：`/api/messages/:id`（int64 path）↔ `/api/announcements/:id`（int64 path）↔ 前端传输想用 string
- 跨 sharding / 跨集群 ID 冲突无法用 snowflake-style ULID 替代
- 客户端 wire 层已经在 `props.template.userIds[]` 等结构里混杂 string ID，类型不一致需要 client 端反复 parse

## 3. 正确做法（Required）

### 3.1 DB 层（5 个新 migration）

新建 **5 个递增 migration**：

| 序号 | 文件 | 内容 |
|---|---|---|
| 034 | `034_id_string_prep_drop_fks.up.sql` | 删所有指向 messages/channels/announcements/approvals 等的 FOREIGN KEY 约束（先解耦） |
| 035 | `035_id_string_alter_columns.up.sql` | `ALTER COLUMN id TYPE TEXT USING id::text` 对每个 BIGSERIAL PK + 每个相关 FK 列 |
| 036 | `036_id_string_default_ulid.up.sql` | 给所有原 BIGSERIAL PK 加 `DEFAULT gen_random_ulid()`（或保留 nextval 转 text，由 service 决定）|
| 037 | `037_id_string_recreate_fks.up.sql` | 重建所有 FK 约束（types 已对齐 TEXT） |
| 038 | `038_id_string_indices.up.sql` | 加 hash index 替代部分 B-tree（VARCHAR PK 性能补偿） |

**对应 down.sql 必须存在** —— 但**明确标 "irreversible in prod"**：down 仅用于本地 reset，prod 走前向修复。

### 3.2 Go 层（约 60 个文件）

按依赖顺序改：

```
repo/   (5+ 文件 id 字段 int64 → string)
  → service/   (调用方对齐)
    → http/   (handler ParseInt 删除，直接读 c.Param("id") string)
      → gateway/   (WS payload id 字段类型)
        → cmd/   (worker / async job)
```

**统一规则**：
- `type X struct { ID int64 }` → `type X struct { ID string }`
- `func ... (id int64) ...` → `func ... (id string) ...`
- handler 删除 `strconv.ParseInt(c.Param("id"), 10, 64)`，直接用 `c.Param("id")`
- 校验：`if id == "" { return 400 }` 替代旧 numeric 校验
- ULID 生成统一走 `pkg/id.NewULID()`（新增 helper）

### 3.3 Wire / JSON 层

```go
// ❌ 旧
type Message struct {
    ID         int64  `json:"id"`
    ChannelID  int64  `json:"channel_id"`
    SenderID   string `json:"sender_id"`
}

// ✅ 新
type Message struct {
    ID         string `json:"id"`
    ChannelID  string `json:"channel_id"`
    SenderID   string `json:"sender_id"`
}
```

**不允许**用 `json:",string"` tag 做中间态——彻底改 Go 类型，避免半 string 半 int64 的中间形态。

### 3.4 测试层（198 集成测试全部要过）

每个 test fixture 改：

```go
// ❌ 旧
msg := &repo.Message{ID: 1, ChannelID: 1}

// ✅ 新
msg := &repo.Message{ID: "01HXYZ...", ChannelID: "01HABC..."}
```

新建 `internal/testutil/id_fixture.go`：

```go
package testutil

func TestChannelID(suffix string) string { return "01TEST_CHANNEL_" + suffix }
func TestMessageID(suffix string) string { return "01TEST_MESSAGE_" + suffix }
```

### 3.5 Phase 划分（强制顺序）

| Phase | 范围 | gate | 估时 |
|---|---|---|---|
| **P-A** | 034-038 migration 文件 + 本地 docker reset 跑通 + `make migrate-up` 0 错 | DB schema 全 TEXT；任一表 `\d` 出来 id 是 `text` 类型 | 4h |
| **P-B** | `internal/repo/` 5+ 文件改 + repo 单测改（mockery 重生）| `go test ./internal/repo/... -count=1` 全绿 | 6h |
| **P-C** | `internal/service/` 全部调用方 + service 单测 | `go test ./internal/service/... -count=1` 全绿 | 6h |
| **P-D** | `internal/http/` + `internal/gateway/` handler / WS payload + 集成测试 | `go test -tags integration ./tests/integration/... -count=1` **198 case 全绿** | 8h |
| **P-E** | `cmd/gateway/main.go` + `cmd/message/main.go` 异步 worker + `make` 全跑 | `make` 0 错 + `make test` 全绿 | 4h |

**严禁跳序**：P-B 不通不允许进 P-C。

### 3.6 跨仓协同（cses-client 端）

**前置同步**：本 spec commit 后，立刻在 cses-client worktree 触发 [`C005-id-type-string-client-downstream`](../../../angular/temp/cses-client/docs/harness/C005-id-type-string-client-downstream.md) 的 agent autonomous 跑：
- Angular `ImMessage.id` / `ImChannel.id` `number → string`
- Rust `types_v2.rs` 13 entity ID 字段 + 22 push handler payload
- cses-client 集成测试 fixture

**串行 gate**：im 后端跑完 P-E 之后，cses-client agent 才被允许开始 — 否则 client 改完 backend 还没 ready 会一直 404。

## 4. 检查方法（Verification）

### 4.1 自动 grep（必须 0 条）

```bash
# 1. 后端 DB schema 无 BIGINT id 引用（白名单：seq / created_at_unix 等非 ID 列）
grep -rEn 'id\s+BIGSERIAL|id\s+BIGINT' server/migrations/*.up.sql \
  | grep -v "_seq\|created_at_unix\|expires_at_unix" \
  | wc -l   # 必须 = 0

# 2. Go repo 无 ID int64 字段
grep -rEn 'ID\s+int64|ChannelID\s+int64|MessageID\s+int64|AnnouncementID\s+int64' \
  server/internal/repo/ \
  | wc -l   # 必须 = 0

# 3. handler 无 strconv.ParseInt(c.Param("id"))
grep -rn 'strconv.ParseInt.*c\.Param' server/internal/http/ \
  | wc -l   # 必须 = 0
```

### 4.2 CI gate

`server/Makefile` 加 target：

```makefile
.PHONY: check-id-types
check-id-types:
	@bash scripts/check-id-types.sh
```

`scripts/check-id-types.sh` 跑上述 3 个 grep，任一非 0 → exit 1。

### 4.3 测试覆盖

- `make migrate-reset && make migrate-up`：5 个新 migration 顺序跑通 0 错
- `make test`：包含 198 集成测试 + ≥ 80 单测，全绿
- `cargo test`（cses-client）：93 IM v2 单测全绿
- `yarn test`（cses-client）：Jest 集成 spec 全绿

## 5. 复现历史（Recurrence Log）

| # | 日期 | 触发场景 | 引用日志 | 处置 |
|---|------|---------|---------|------|
| 1 | 2026-05-13 | 用户拍板 "全面替换 数据库+业务代码+测试类"，feat/im-reactor-2 worktree 拍板会话 | 客户端 SESSION.md §0 | **本 harness 创建**；spec drafting → autonomous agent 执行 |

## 6. 反例与边界（Don't Over-Apply）

- ❌ **不要顺手改 `seq`**：seq 是单调 cursor，BIGINT 是性能最优选；不是身份 ID
- ❌ **不要改 mm UserID**：已经是 TEXT；本 spec 不动 users 表
- ❌ **不要改第三方 token / file uuid**：file.id 在 OSS 侧已经是 string；本 spec 只关心**自增数字 PK**
- ❌ **不允许混合形态**：不允许某些表 id TEXT、某些表 id BIGINT —— P-A 必须把所有自增 id 表一次性改完
- ❌ **不允许保留 int64 fallback handler**：handler 必须 100% 不再 ParseInt，违反规则 §3.2

## 7. 升级 / 弃用条件（Lifecycle）

- **升级 active**：P-A 跑通后 status 改 active；P-E 跑通后追第 2 条 source_log
- **升级 merged**：连续 30 天无回归 + CI gate 接管 + 规则 inline 进 `~/.claude/rules/golang/coding-style.md` ID 字段类型小节
- **弃用 deprecated**：如果未来引入分布式 ID 方案（snowflake / ULID）后端层完全统一，本 harness 进 deprecated

## 8. 性能 / 不可逆风险声明（FYI，不阻塞）

- VARCHAR PK 比 BIGINT PK：B-tree 索引膨胀 2-3x，JOIN 在大表（messages > 10M 行）慢 30-50%
- ULID（26 char Crockford base32）比 BIGINT（8 byte）存储多 18 byte/行；messages 表 10M 行多 180MB
- **不可逆**：prod 跑 P-A migration 之后回不来；prod 部署必须配套数据备份 + 滚动停服计划（由 SRE 单独立项，不在本 spec 范围）

---

**Owner**：im 项目 + cses-client 项目联合
**最后更新**：2026-05-13（drafting，等 autonomous 执行批准）
**下次更新触发**：Phase A migration 跑通 / Phase E 全绿 / 用户决策变更

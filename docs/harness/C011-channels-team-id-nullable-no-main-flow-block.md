# C011 — channels.team_id 必须 TEXT NULL，company 缺省不阻塞主流程

```yaml
---
id: C011
title: channels.team_id 必须 TEXT NULL，companyId 缺省时不阻塞主流程
status: active
created: 2026-05-12
last_recurred: 2026-05-12
recurrence_count: 1
source_logs:
  - logs/2026-05-12.json
applies_to:
  - server/migrations/*.sql
  - server/internal/repo/channel.go
  - server/internal/repo/message.go
  - server/internal/middleware/mattermost_cookie.go
  - server/internal/service/channel.go
inline_target: ~/.claude/rules/golang/coding-style.md
---
```

## 1. 触发场景（Trigger）

- 任何 `server/migrations/*_*.up.sql` 里对 `channels` / `messages` 表 `team_id` 字段的 `ADD COLUMN` / `DROP COLUMN` / `ALTER COLUMN`
- `internal/repo/channel.go` 任何含 `team_id` 的 `SELECT` / `INSERT` 拼接
- `internal/repo/message.go` 任何 `team_id` 字段 INSERT
- `internal/middleware/mattermost_cookie.go::TeamIDFromCtx` 与 `stampTeamHeader` 改动
- 关键词：`team_id` / `companyId header` / `TeamIDFromCtx` / 单测在 channel/message 创建路径

## 2. 错误模式（Anti-Pattern）

### 2.1 把 team_id 设为 NOT NULL（违反契约）

```sql
-- ❌ 错误：
ALTER TABLE channels ADD COLUMN team_id TEXT NOT NULL;
```

**后果**：客户端没传 `companyId` header 时，service 层拿到空串想插入 `NULL` → PG 报 `23502: null value in column "team_id"`，整条 POST /api/channels 链路 5xx 中断。dev / 联调集群 cookie fixture 通常带 companyId，但**单元测试 / cse-client 跳过 cookie 解析的灰度场景**会立刻塌方。

### 2.2 service 层手动校验非空（重复防御）

```go
// ❌ 错误：
teamID := middleware.TeamIDFromCtx(c)
if teamID == "" {
    c.JSON(400, gin.H{"error": "companyId required"})
    return
}
```

**后果**：阻塞合法的"无租户"客户端（admin tool / system scheduled job / 跨租户消息搜索）。spec 设定就是 `companyId == ""` 视为「无 team scope」，DB 写为 SQL NULL（参考 `internal/middleware/mattermost_cookie.go:158-165`）。

### 2.3 SELECT 时 COALESCE("" / 0) 替代 NULL（数据语义错位）

```go
// ❌ 错误：
rows.Scan(&ch.TeamID)  // TeamID 是 string，rows 给 NULL → "" 也行，但
// ❌ 然后用 ch.TeamID == "default" 这种业务判断，把 NULL 当 sentinel 是坑
```

## 3. 正确做法（Required）

### 3.1 DDL 锁死 NULL

```sql
-- ✅ 正确（migrations/014_m4_userid_text.up.sql:70）：
team_id  TEXT  NULL,
```

任何后续 migration 改动必须**保持 NULL**。如果业务必须强制 team scope，应该用 partial unique index + service 校验而不是动 schema。

### 3.2 Repo 层接受 `*string` 或显式区分 ""

```go
// ✅ 正确：用 sql.NullString 或 *string，让 service 决定写 NULL 还是 ""
ch.TeamID = pgtype.Text{String: middleware.TeamIDFromCtx(c), Valid: middleware.TeamIDFromCtx(c) != ""}
```

实际项目用 `string` + 业务约定 `"" == NULL`（注意：写 PG 时 `string("")` ≠ `NULL`，repo 层必须用 `NULLIF(:team_id, '')` 或 driver `pgtype` 控制）。

### 3.3 Middleware 不预校验 companyId

```go
// ✅ 正确（mattermost_cookie.go:163-165）：
func stampTeamHeader(c *gin.Context) {
    c.Set(teamIDCtxKey, c.GetHeader(MMTeamHeader))  // 空字符串也允许
}
```

下游 `TeamIDFromCtx` 返回 `""` 是合法值，业务自己决定是否需要。

### 3.4 Schema 一致性检查（dev / pre 集群自检）

任何启动失败的 `column c.team_id does not exist (SQLSTATE 42703)` 必须按以下顺序排查：

1. `migrate version` 看是否 dirty / 未到 17
2. 如 dirty：`DROP SCHEMA public CASCADE; CREATE SCHEMA public;` + `make migrate-up`（dev/pre 只）
3. **重启 gateway**（pgx 有 prepared statement cache，schema 变后旧 plan 报 `cached plan must not change result type (SQLSTATE 0A000)`）

## 4. 检查方法（Verification）

### 4.1 自动 grep（必须返回 0 条）

```bash
# ① 禁止把 team_id 设为 NOT NULL
grep -rEn "team_id\s+TEXT\s+NOT\s+NULL" server/migrations/ --include='*.sql'

# ② 禁止在 service 层硬 reject 空 companyId（除非该 endpoint 显式标注必填）
grep -rEn 'TeamIDFromCtx.*==\s*""\s*\)\s*\{[^}]*c\.JSON\(' server/internal/ --include='*.go'

# ③ 禁止把 team_id 字段从 channels / messages 表删掉
grep -rEn "DROP COLUMN.*team_id" server/migrations/ --include='*.sql'
```

### 4.2 CI Gate

- migrations apply 测试：`make migrate-up && make migrate-down 1 && make migrate-up` 必须 clean，期间不报 `column.*team_id.*does not exist`
- 集成测试 `tests/integration/v0_*.go` 中至少一条用例**故意不传 companyId**，断言 POST /api/channels 仍然 200（team_id 落 NULL）

### 4.3 单测（白盒）

- `server/internal/repo/channel_test.go::TestCreate_TeamIDNullable`：companyId="" → 写入 → SELECT 回返 `team_id IS NULL`
- `server/internal/middleware/mattermost_cookie_test.go::TestTeamHeader_EmptyAllowed`：companyId header 缺失 → ctx 里 teamID == "" → 下游可读到空串

### 4.4 集成测试

- `tests/integration/m4_channel_test.go::TestM4ChannelCreate_NoTeamScope`：POST /api/channels 不带 companyId → 200 + DB team_id 为 NULL → GET /api/channels 仍能看到

### 4.5 手工 smoke

```bash
# 验证 schema 状态
PGPASSWORD=one.2013 psql -h 192.168.6.66 -p 32432 -U postgres -d im_dev \
  -c "SELECT column_name, is_nullable, data_type FROM information_schema.columns
      WHERE table_name='channels' AND column_name='team_id';"
# expect: team_id | YES | text
```

## 5. 复现历史（Recurrence Log）

| #  | 日期       | 触发场景                                                                                                                                                  | 引用日志                  | 处置 |
|----|------------|-----------------------------------------------------------------------------------------------------------------------------------------------------------|---------------------------|------|
| 1  | 2026-05-12 | apifox sync 流程做 WS E2E 烟测时，dev PG 卡在 v1 dirty（schema_migrations.version=1, dirty=true）。`channels` 表是 M1/M2 era 无 team_id，gateway 报 `column c.team_id does not exist`，挡住所有 `/api/channels` 路径。 | logs/2026-05-12.json     | DROP SCHEMA public CASCADE → make migrate-up → 重启 gateway → E2E 全过。team_id 字段创建时已是 TEXT NULL（migration 014 line 70）。规则写进本 harness。|

## 6. 反例与边界（Don't Over-Apply）

- ✅ team_id 在 **业务 SQL 索引**里可以加（如 `CREATE INDEX ON channels (team_id) WHERE team_id IS NOT NULL`），用于多租户查询性能；这不违反"列可空"
- ✅ 跨租户 admin endpoint 在 service 层显式断言 `teamID != ""` 是允许的（业务正当性，不是 schema 约束）
- ❌ 不适用于：messages 表（同样需保持 team_id NULL，规则同 channels）—— 把 messages 也涵盖进本 harness §1 applies_to

## 7. 升级 / 弃用条件（Lifecycle）

**晋升（→ merged）**：
- 30 天零新复现 + grep gate 接进 CI
- 规则 inline 进 `~/.claude/rules/golang/coding-style.md §schema 约束` 节
- frontmatter `status: merged`, `inline_target` 指向 inline 锚点

**弃用（→ deprecated）**：
- 项目下线 team / 租户概念（极不可能；现在 cookieId/companyId 是核心鉴权三件套之一）
- `team_id` 字段被 schema 重构成结构化的 tenants 外键（届时重建 harness 描述新约束）

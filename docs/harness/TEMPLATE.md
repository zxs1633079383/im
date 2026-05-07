# C{NNN} — {一句话标题}

```yaml
---
id: C{NNN}
title: {一句话标题}
status: drafting | active | merged | deprecated
created: YYYY-MM-DD
last_recurred: YYYY-MM-DD
recurrence_count: N
source_logs:
  - logs/YYYY-MM-DD.json#L{line}
applies_to:
  - server/**/*.go
  - client/src/app/**/*.ts
inline_target: ~/.claude/rules/golang/coding-style.md  # （可选）晋升后 inline 到的位置
---
```

## 1. 触发场景（Trigger）

> 哪些代码路径 / 文件 glob 会加载本约束？写**具体的 path glob + 关键词**，不写"所有 Go 代码"。

例：
- `server/internal/repo/message.go` 任何 `INSERT INTO messages` 或 `tx.Create(&Message{})` 调用
- 任何 `service/**/*.go` 里直接拼 `messages` 表 SQL 的写入路径
- 关键词：`AllocSeqAndInsert` / `messages.seq` / `lastInsertID` 出现的文件

## 2. 错误模式（Anti-Pattern）

> 反面教材代码 + 后果。**必须**给出具体能 grep 到的代码片段，不要伪代码。

```go
// ❌ 错误：绕过 AllocSeqAndInsert 直接写
db.Exec("INSERT INTO messages (...) VALUES (...)")

// ❌ 错误：service 层手动算 seq
maxSeq, _ := repo.GetChannelMaxSeq(ctx, chID)
db.Create(&Message{Seq: maxSeq + 1, ...})  // 并发下 seq 重复
```

**后果**：
- 列出 1-3 条**具体后果**：race / 数据丢失 / 性能塌方 / 安全漏洞
- 引用真实事故链路（commit hash / issue 编号）

## 3. 正确做法（Required）

> 给出可复制的代码 + 实施约束。

**首选 A**（推荐）：
```go
// ✅ 正确：所有写入走 AllocSeqAndInsert
err := svc.repo.AllocSeqAndInsert(ctx, tx, &repo.Message{...})
```

**备选 B**（仅特殊场景）：
说明什么情况下可以走备选（如系统消息批量写 + 已加事务）。

**绝对禁止 C**：
- ❌ 直接 `db.Exec("INSERT INTO messages ...")`
- ❌ 跳过 tx 参数手工开事务

**实施约束**：
- 任何对 `messages` 表的 INSERT 必须经 `AllocSeqAndInsert`
- `tx != nil` 时复用外部事务，不允许 service 内部嵌套 BEGIN

## 4. 检查方法（Verification）

> **每一项**都要可机械执行。空话不算 verification。

### 4.1 自动 grep（必须返回 0 条）

```bash
# ① 没有绕过 AllocSeqAndInsert 的直接 INSERT messages：
grep -rEn "INSERT\s+INTO\s+messages" server/internal/ --include='*.go'

# ② service 层没有手算 seq：
grep -rEn "GetChannelMaxSeq.*\+\s*1" server/internal/service/ --include='*.go'
```

### 4.2 CI Gate

- `golangci-lint run` 自定义 ruleguard 规则（路径：`.golangci.yml` § ruleguard.file）
- pre-commit hook：`scripts/check-message-write-path.sh` 必须 exit 0

### 4.3 单测（白盒）

- 路径：`server/internal/repo/message_test.go`
- 用例命名：`TestAllocSeqAndInsert_Concurrent10kSameChannel`
- 验证：1000 goroutine × 10 写入 → seq 单调 + 无重复 + 100% 行覆盖

### 4.4 集成测试

- 路径：`server/tests/integration/m4_message_sync_test.go`
- 用例：`TestM4MessageSendThenSync` 验证客户端送达后 `POST /sync` 拉回的 seq 与 DB 一致

### 4.5 手工 smoke（可选）

- 命令：`scripts/seq-monotonicity-smoke.sh`
- 通过条件：连发 100 条 → seq 严格 +1 递增

## 5. 复现历史（Recurrence Log）

> 每次踩到 → 在表里加一行；时间倒序也行，但不许跳号。

| #  | 日期       | 触发场景                                                                  | 引用日志                  | 处置                                  |
|----|------------|---------------------------------------------------------------------------|---------------------------|---------------------------------------|
| 1  | 2026-04-23 | M3 性能优化时一度想直接 batch INSERT 跳过 AllocSeqAndInsert，被 race 打回 | logs/2026-04-23.json#L42  | 立即回滚，AllocSeqAndInsert 锁死      |
| 2  | 2026-05-01 | Phase 1 review 中发现 `service/message_template.go` 直接 update `props` 漏过 broadcast | logs/2026-05-01.json#L88  | 加 `BroadcastUpdated` 调用 + 集成测试 |

## 6. 反例与边界（Don't Over-Apply）

> 哪些场景**不适用**本约束？避免约束过度泛化导致僵化。

- 例：`migrations/*.sql` 里的 seed 数据 INSERT 允许直接写（迁移期一次性，无并发）
- 例：测试代码（`tests/integration/`）允许用 `db.Exec` 直接构造 fixture（不走业务路径）
- 例：M5 历史 ETL（`cmd/etl/`）允许 batch INSERT，但**必须**用 `migration_sort_key` 占位 seq 字段（参考 `docs/GOAL.md §4 决策点 6`）

## 7. 升级 / 弃用条件（Lifecycle）

**晋升（→ merged）**：
- 连续 1 个月（30 天）零新复现 + 自动化 grep/CI gate 接管
- 把规则 inline 进 `~/.claude/rules/golang/coding-style.md §写入路径` 节
- 本文件 frontmatter `status: merged`，`inline_target` 指向 inline 锚点

**弃用（→ deprecated）**：
- 场景已不存在（如 `messages` 表被拆 / seq 协议换成 snowflake / `AllocSeqAndInsert` 函数被删）
- frontmatter `status: deprecated`，保留文件作为历史索引不再加载

---

## 附录：编号规则

- 编号严格递增：下一条 = 当前最大 + 1
- 删除条目用 `status: deprecated` 不删除文件，编号不复用
- 同主题多条用 sub-编号：`C012a` `C012b`（极少用）

# C008 — 每个 handler 文件每条路由必须有 1 成功 + 1 失败集成测试

```yaml
---
id: C008
title: 76 端点 × 5 case + 16 WS × 6 case 集成测试是 100% 覆盖率的硬门
status: drafting
created: 2026-05-07
last_recurred: 2026-05-07
recurrence_count: 1
source_logs:
  - logs/2026-05-07.json#L1
applies_to:
  - server/internal/http/**/*.go
  - server/internal/gateway/**/*.go
  - server/tests/integration/m4_*_test.go
  - server/tests/integration/m4_ws_*_test.go
inline_target: ~/.claude/rules/golang/testing.md  # 已存在覆盖率 100% 要求，本 harness 落地具体 endpoint × case 矩阵
---
```

## 1. 触发场景（Trigger）

- 任何 `server/internal/http/*.go` 中**新增 `authed.{GET,POST,PUT,PATCH,DELETE}` 路由**的 PR
- 任何 `server/internal/gateway/types.go` **新增 `WSMessageType` 常量**的 PR
- 任何 `make verify-integration` 跑下来**集成测试用例数下降**的 commit
- `~/.claude/rules/golang/testing.md` 要求 100% 覆盖率，但当前 `internal/http` 行覆盖率 = 3.4%（2026-05-07 实测）

## 2. 错误模式（Anti-Pattern）

```go
// ❌ 错误 #1：新增路由不写集成测试
authed.POST("/messages/:id/received", handler)  // 单测 mock service ≠ 集成测试

// ❌ 错误 #2：只写 happy path
func TestM4TemplateReceived(t *testing.T) {
    // 只验 200，没验 401 / 403 / 404 / 400
}

// ❌ 错误 #3：依靠"反正 e2e harness 会跑到"做借口
// e2e 是端到端冒烟，不是接口契约证明，无法保证 100% 覆盖
```

**后果**：
1. **回归无网兜**：handler 改一个字段、改一个错误码，单测 mock 不到的就靠 QA 手测发现 → 下次 release 才暴露
2. **覆盖率假象**：`internal/http` 当前 3.4% 行覆盖（2026-05-07 实测），CI 没卡死覆盖率门禁，新代码只跑得过编译
3. **client 联调爆炸**：cses-client cutover 时若后端某个边界没测到（如 `team_id null` / `cookie expired`），客户端只能在联调阶段才发现

## 3. 正确做法（Required）

**首选 A — 端点 × 5 case 矩阵**：

每个 `authed.*` 路由必须有 5 个集成测试用例（来自 `server/docs/M4_SPEC.md §11.3`）：

| Case | 验证项 | HTTP 期望 |
|---|---|---|
| **C1** Happy Path | 正常入参 + 有权限 cookieId | 2xx + 业务字段正确 |
| **C2** Cookie Missing | 不带 `cookieId` header | 401 + `error: "missing auth"` |
| **C3** Cookie Invalid | `cookieId` Redis 不存在 | 401 + `error: "invalid cookie"` |
| **C4** Forbidden | 有效 cookieId 但越权（不在 channel members / 非创建者） | 403 + `error: "forbidden"` |
| **C5** Bad Request | 路径 ID 非法 / body 字段缺失 / 超限制 | 400 + `error: "invalid ..."` |

**首选 B — WS 类型 × 6 case 矩阵**：

每个 `WSMessageType` 必须有 6 个集成测试用例（来自 `M4_SPEC.md §11.4` 16 type × 2 team × 3 action = 96，简化版 6 case）：

| Case | 验证项 |
|---|---|
| **W1** 同 pod 单收件人收到 | 客户端 A 发，A1 收 |
| **W2** 同 pod 多收件人扇出 | 客户端 A 发，B/C/D 全收 |
| **W3** 跨 pod 经 Pulsar 转发 | A 在 pod1，B 在 pod2，B 收 |
| **W4** 异 team 过滤（D3 决策） | team_id 不匹配 → 不下发 |
| **W5** 离线用户跳过 | 用户无 routing key → 不发 Pulsar |
| **W6** push_id ACK 闭环 | 客户端发 push_ack → server 清未确认队列 |

**模板**：

```go
// server/tests/integration/m4_template_received_test.go
func TestM4TemplateReceived_C1_HappyPath(t *testing.T) { ... }
func TestM4TemplateReceived_C2_CookieMissing(t *testing.T) { ... }
func TestM4TemplateReceived_C3_CookieInvalid(t *testing.T) { ... }
func TestM4TemplateReceived_C4_NotChannelMember(t *testing.T) { ... }
func TestM4TemplateReceived_C5_NotTemplateMessage(t *testing.T) { ... }
```

**绝对禁止 C**：
- ❌ 一个 `Test*Suite(t)` 里跑十几个 sub-test 但只断言 `200` —— sub-test 也算 5 case，断言必须区分
- ❌ 用 mock 替代 PG / Redis —— 集成测试必须用 testcontainers 或真实 pre 集群
- ❌ 共享 fixture 跨用例不清理 —— 每个 case 独立 `t.Cleanup(teardown)`

## 4. 检查方法（Verification）

### 4.1 端点清单 vs 测试覆盖矩阵脚本

```bash
# scripts/check-handler-coverage.sh（待创建）
#
# 1. 列出所有 authed.{GET,POST,PUT,PATCH,DELETE} 路由
# 2. 对每条路由扫 server/tests/integration/ 下是否有 5 个 _C[1-5]_* 测试函数
# 3. 任何缺口 → exit 1，报告缺哪条路由的哪个 case

ROUTES=$(grep -rEn 'authed\.(GET|POST|PUT|PATCH|DELETE)' server/internal/http/ \
  --include='*.go' | grep -v '_test.go' | wc -l)
TESTS=$(grep -rE 'func TestM4.*_C[1-5]_' server/tests/integration/ --include='*.go' | wc -l)
EXPECTED=$((ROUTES * 5))
[ "$TESTS" -ge "$EXPECTED" ] || { echo "missing: $((EXPECTED - TESTS)) cases"; exit 1; }
```

### 4.2 行覆盖率 100% 门禁

```bash
go test -race -covermode=atomic -coverprofile=coverage.out -tags integration ./...
go tool cover -func=coverage.out | awk '$3 != "100.0%" && $1 !~ /coverage:ignore/ { print; missing++ } END { exit (missing > 0) }'
```

> **当前现状**（2026-05-07 实测，必须列出来不掩盖）：
> - `internal/auth` 90.9% — 接近达标
> - `internal/middleware` 66.1%
> - `internal/observability` 81.6%
> - `internal/config` 32.3%
> - `internal/gateway` **6.7%**
> - `internal/http` **3.4%**
> - `internal/repo` **2.9%**
> - `internal/service` **0.3%**
> 距离 100% 还差 ~38000 行未覆盖语句

### 4.3 路由清单检查（防止漏端点）

```bash
# 期望：路由数 ≥ 76（M4 spec §11.3 基线，v0.7.x 后扩到 ~85）
ROUTE_COUNT=$(grep -rEn 'authed\.(GET|POST|PUT|PATCH|DELETE)' server/internal/http/ --include='*.go' \
  | grep -v '_test.go' | wc -l)
[ "$ROUTE_COUNT" -ge 76 ] || { echo "route count regressed: $ROUTE_COUNT < 76"; exit 1; }
```

### 4.4 100% 覆盖落地分批清单

按 cses-client 业务调用频率优先级（来自 `temp/cses-client/docs/messagev3api统计.md`）排序：

| 批次 | endpoint family | 路由数 | 集成测试缺口 | 估时 |
|---|---|---|---|---|
| **Batch-A** 已完成（happy path） | auth / channel-create-DM/Group / message-sync / friend / topic / template-received / read-stats | 7 family | 12 测试函数 ✅ | — |
| **Batch-B** 高频核心 | message (send/list/edit/delete/read) / channel-governance / favorite / urgent | 26 路由 | 130 case | 3 day |
| **Batch-C** 中频治理 | announcement / approval / notification / quick_reply / scheduled / reaction | 25 路由 | 125 case | 2.5 day |
| **Batch-D** WS 集成（**当前 0**）| 16 WSMessageType × 6 case | — | 96 case | 2 day |
| **Batch-E** 边角 | search / settings / module / presence / file / sync | 11 路由 | 55 case | 1 day |
| **合计** | | **~78 路由 + 16 WS** | **+406 case** | **~8.5 day** |

### 4.5 race detector + benchmark

- `go test -race -tags integration ./...` 必须 clean
- 热路径（message send / sync / push）必须有 `Benchmark*`，PR 必须贴 ns/op + B/op + allocs/op 数字

## 5. 复现历史（Recurrence Log）

| # | 日期       | 触发场景                                                                                  | 引用日志                  | 处置                                                                  |
|---|------------|-------------------------------------------------------------------------------------------|---------------------------|-----------------------------------------------------------------------|
| 1 | 2026-05-07 | 用户提问"100% 覆盖所有接口"，实测 `internal/http` 仅 3.4% 行覆盖、集成只 12 测试函数      | logs/2026-05-07.json#L1   | 创建本 harness（drafting），按 §4.4 5 个 batch 分批落地                |

## 6. 反例与边界（Don't Over-Apply）

- ✅ `cmd/` 下的 `main.go` 启动装配代码：用 `// coverage:ignore` 豁免（参考 `~/.claude/rules/golang/testing.md`）
- ✅ 平台特定 stub（`//go:build windows`）：豁免
- ✅ 生成代码（`*.pb.go` / `*_gen.go`）：豁免
- ❌ **不要**用单元测试 + 高 mock 度替代集成测试 —— mock 测不到 PG / Redis / Pulsar 的真实约束
- ❌ **不要**把 `_C[1-5]_` 矩阵泛化到 cmd/* 入口（启动装配）和纯库函数（数学计算）
- ❌ **不要**为豁免 100% 在每个文件加 `// coverage:ignore` —— 豁免必须在 PR 描述里单独列出理由

## 7. 升级 / 弃用条件（Lifecycle）

**晋升 → active**（drafting → active）：
- Batch-B 落地完成（130 case 进 `tests/integration/`）→ 自动化 `scripts/check-handler-coverage.sh` 已接入 `make verify-integration` → 状态 `active`

**晋升 → merged**：
- 5 个 batch 全部落地 + 行覆盖率 100%（除豁免）+ `scripts/check-handler-coverage.sh` 在 CI 上 30 天零失败
- inline 进 `~/.claude/rules/golang/testing.md` § 接口组覆盖（已存在该节，本 harness 是项目侧具体落地清单）

**弃用 → deprecated**：
- 测试框架彻底换（如换 `testify/suite` + `dockertest`）→ 本文件归档，新建 C{NNN}-replacement
- handler 改架构（如全部走 gRPC，不再有 HTTP 路由）→ 本约束不再适用

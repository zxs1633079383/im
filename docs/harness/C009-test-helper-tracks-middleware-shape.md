# C009 — 加 / 改 middleware 必须扫描所有 test helper 是否依赖旧响应 shape

```yaml
---
id: C009
title: 注入 / 修改 middleware（envelope / auth / logging 等）后必须 sweep 所有 test helper / fixture，否则同一 helper 会让所有调用方测试同时炸
status: active
created: 2026-05-07
last_recurred: 2026-05-07
recurrence_count: 1
source_logs:
  - logs/2026-05-07.json#L1  # 本次会话 Run #1 全 fail
applies_to:
  - server/tests/integration/m4_harness_test.go
  - server/internal/testutil/**/*.go
  - server/cmd/gateway/main.go    # 生产 middleware 注入点
  - server/internal/http/router.go
inline_target: ~/.claude/rules/golang/testing.md  # § "中间件演进" 候选小节
---
```

## 1. 触发场景（Trigger）

任何会改变 HTTP / WS 响应 shape 的 middleware 改动：

- `cmd/gateway/main.go::buildRouter` 或 `tests/integration/m4_harness_test.go::buildEngine` 新增 / 删除 / 重排 `engine.Use(...)`
- `internal/http/response_envelope.go` / `internal/middleware/*.go` 修改包裹 / 解包逻辑
- 任何会让"未改业务代码 + 没改测试断言"但响应 body 字段位置变化的修改
- 关键词 grep：`engine.Use(` / `r.Use(` / `authed.Use(` / `responseEnvelope` / `WriteHeader`

## 2. 错误模式（Anti-Pattern）

```go
// 时间线：
// T0: harness 写好 helper（用 raw shape 解析）
func (e *m4env) seedDM(ownerCookie, peerID string) int64 {
    dm := e.expect.POST("/api/channels/dm").
        WithJSON(...).
        Expect().Status(201).JSON().Object()  // ← 解析 raw {id:1,...}
    return int64(dm.Value("id").Number().Raw())
}

// T1: 注入 envelope middleware 改 response shape
engine.Use(imhttp.ResponseEnvelope())  // 现在响应是 {data:{id:1,...},status:"success"}

// T2: 跑测试 → 142 个调用 seedDM 的测试**全部炸**
//      错误信号 "expected key id"，看似业务代码出 bug，实际是 helper 没追上 shape
```

**后果**：
1. **大面积假阳性**：fail 数量 = 调用 helper 的测试数（本次 Run #1 ~120 fail），看起来像生产 bug 出大事，实际只是 helper 一处遗漏
2. **CI / 调试时间巨亏**：每个 fail 的 stack trace 都指向 helper 内部 `m4_harness_test.go:226`，但 root cause 是 middleware 加注入。开发者要逐个追 stack 才能定位真正起因
3. **truncated 输出陷阱**：`go test ... | tail -100` 通常只能捕到末尾几个 fail，前面 100+ fail 全部丢失，初看以为只挂了 2 个测试（本次 Run #1 实际碰到）

事故链路：
- 2026-05-07 Run #1：买 envelope middleware 注入到 buildEngine + 改 13 测试文件断言 + 没扫 seedDM/seedGroup helper → `tail -100` 只显示末尾 2 fail，看起来只是 2 个测试有问题。诊断 5 分钟才发现 helper 是 root cause（commit `ba7a77d` Run #2 修复全绿）

## 3. 正确做法（Required）

**首选 A — middleware-shape 影响清单（PR checklist）**：

任何注入 / 修改 middleware 的 PR 描述必须列：

```markdown
## Middleware shape 影响 sweep
- [ ] 影响的响应字段：__________（如 `body → {status,data,error}` 包一层 wrap）
- [ ] 扫过的 helper / fixture（grep 路径全列）：
      - [ ] `tests/integration/m4_harness_test.go::seed*` 已扫
      - [ ] `tests/integration/*::decodeXxx*` 已扫
      - [ ] `internal/testutil/**/*.go` 已扫
- [ ] 全集成测试跑一遍验证（不带 `tail`，full log 落 file）：
      `go test -tags integration ./tests/integration/... > /tmp/run.log 2>&1`
- [ ] FAIL 数 = 0；PR 描述贴最后 `ok ... <duration>` 一行
```

**首选 B — sweep 命令（机械执行，不靠人脑记）**：

```bash
# ① 找所有 test helper 中 raw 解析 .JSON().Object() / .JSON().Array() 的位置
grep -rEn '\.Expect\(\)\.Status\([0-9]+\)\.JSON\(\)\.(Object|Array)\(\)' \
  server/tests/integration/m4_harness_test.go \
  server/internal/testutil/ \
  --include='*.go'

# ② 找所有 test helper 中假设字段位于顶层（.Value(...)）的位置
grep -rEn 'func \(e \*m4env\)|func \w+Fixture' \
  server/tests/integration/ server/internal/testutil/ \
  --include='*.go'
```

每改 middleware 必须先跑 ① 看哪些 helper 会因 shape 改动而错位 → 同步改 helper → 再改业务代码 / 中间件。

**首选 C — Run #1 教训固化为 verification**：

```bash
# 测试输出禁止用 tail / head 截断，full log 落文件
go test -tags integration -timeout 30m ./tests/integration/... \
  > /tmp/im-integration-$(date +%Y%m%d-%H%M).log 2>&1

# 失败统计必须从 full log 拿，不能从 truncated tail 看
grep -cE '^--- FAIL:' /tmp/im-integration-*.log
```

**绝对禁止 D**：
- ❌ 加 middleware 后只看 `go vet ./...` / `go build ./...` 就 commit —— 编译不报 shape 不匹配，必须跑测试
- ❌ 用 `go test ... | tail -100` 看结果（fail 信号被 tail 截断风险高）
- ❌ "刚才那个 helper 应该没事吧" —— 凭直觉跳过 sweep
- ❌ 把 helper 的 raw 解析改成"宽松匹配"（如 try-both-shapes）—— 那只会让下次 shape 改时更难定位

**实施约束**：
- middleware 改动 PR 必须含 sweep checklist（PR template 候选项）
- 测试 helper 集中放 `m4_harness_test.go` / `internal/testutil/`，不允许散落各 `_test.go`
- 任何 helper 内部解析响应的 chain 必须复用 harness 提供的 unwrap helper（`successBody` / `successBodyArray` / `errorBody`），不允许 raw `.JSON().Object()`

## 4. 检查方法（Verification）

### 4.1 自动 grep（必须返回 0 条）

```bash
# ① harness / testutil 内 raw 解析 chain
grep -rEn '\.Expect\(\)\.Status\([0-9]+\)\.JSON\(\)\.(Object|Array)\(\)' \
  server/tests/integration/m4_harness_test.go \
  server/internal/testutil/ \
  --include='*.go'
# 预期 0 条 — helper 必须用 successBody / successBodyArray / errorBody

# ② 测试 helper 直接调 .Value(...) 而不经 unwrap helper
grep -rEn '^func \(e \*m4env\)' server/tests/integration/m4_harness_test.go -A 10 \
  | grep -E '\.JSON\(\)|\.Value\("[a-z_]+"\)' \
  | grep -v 'successBody\|successBodyArray\|errorBody'
# 预期 0 条
```

### 4.2 CI Gate

- `verify-all` 加上 §4.1 grep；非 0 行 → exit 1
- middleware 注入点（`engine.Use(...)`）变更触发 GitHub Actions diff 提示「请确认 §4.1 grep + sweep checklist 已过」

### 4.3 单测（meta，验证 helper 自身）

- 路径：`server/tests/integration/m4_harness_helper_test.go`（待新建）
- 用例：
  - `TestHarnessHelpers_seedDMReturnsValidChannelID` — 起 testcontainer，调 seedDM，断言返回 int64 > 0（防止 helper 没拿到 id 时返回 0）
  - `TestHarnessHelpers_successBodyOnRawResponse_Fails` — 构造 raw response（未经 envelope）调 successBody，断言测试 fail（确保 helper 不是宽松匹配）

### 4.4 集成测试 full-log 验证

- `make verify-integration` 必须输出到 file 不可 tail：

```makefile
verify-integration:
	go test -tags integration -timeout 30m ./tests/integration/... \
		> $(LOG_DIR)/integration-$(shell date +%Y%m%d-%H%M).log 2>&1
	@grep -cE '^--- FAIL:' $(LOG_DIR)/integration-*.log | grep -q '^0$$' \
		|| { echo "FAIL count > 0, see log"; exit 1; }
```

### 4.5 Run #1 教训复盘表（防遗忘）

| 信号 | Run #1 看到 | 真实情况 | 教训 |
|---|---|---|---|
| `tail -100` 输出末尾 2 fail | 以为只挂 2 测试 | 实际 ~120 fail，前面被 tail 截断 | full log 落 file |
| stack trace 指向 helper L226 | 以为 helper 自身有 bug | 是 middleware shape 变了，helper 没追上 | sweep helper 是最重要 step |
| go build / vet 全绿 | 以为代码 OK | 编译不报 shape 错位 | shape 错位是运行时错，必须跑测试 |

## 5. 复现历史（Recurrence Log）

| # | 日期       | 触发场景                                                                              | 引用日志                  | 处置                                                                  |
|---|------------|---------------------------------------------------------------------------------------|---------------------------|-----------------------------------------------------------------------|
| 1 | 2026-05-07 | Run #1 注入 envelope middleware 后没扫 seedDM/seedGroup，~120 fail 被 `tail -100` 截到末尾仅显示 2 fail | logs/2026-05-07.json#L1 | Run #2 修 helper 用 successBody 包裹 → 142 case 全绿（commit `ba7a77d`，tag `v0.7.3-batch-b-envelope`） |

## 6. 反例与边界（Don't Over-Apply）

- ✅ **不影响响应 shape 的 middleware 改动**（如 metrics 计数 / OTel tracing wrapper）：不需要 §3 sweep checklist，但仍建议跑一遍集成测试
- ✅ **业务代码改动**（handler / service / repo）：本约束不管，那是普通业务回归测试范围
- ✅ **测试代码内部重构**（不改任何 helper 入口签名）：不需要 sweep
- ❌ **不要**为了"helper 兼容旧 shape"在 helper 里写 if-else 分支判断 wrap 与否 —— shape 是契约，必须单一
- ❌ **不要**把约束扩展到非 HTTP middleware（如 gRPC interceptor / Pulsar consumer middleware）—— 它们有各自的 helper 模式，需要独立 harness

## 7. 升级 / 弃用条件（Lifecycle）

**晋升 → merged**：
- 30 天零新复现 + §4.1 grep 在 CI 接管 + §4.2 makefile verify-integration 强制 full log
- inline 进 `~/.claude/rules/golang/testing.md` § 中间件演进与 helper 同步节（候选新增小节）
- inline 进项目根 `CLAUDE.md §1.8 测试纪律`（已有"httpexpect ?-encode 陷阱"小节，本条作为相邻新条目）

**弃用 → deprecated**：
- 测试框架改 testify/suite + 自动 lint helper shape（lint 接管 §4.1 grep）
- envelope 协议演进消失（如改 protobuf gRPC，shape 由 codec 处理）→ 本条主要场景失效

---

## 附录：本会话固化的最佳实践（commit ba7a77d）

```go
// ✅ harness 内的 helper 必须经 unwrap helper
func (e *m4env) seedDM(ownerCookie, peerID string) int64 {
    e.t.Helper()
    dm := successBody(e.expect.POST("/api/channels/dm").
        WithHeader(middleware.MMCookieHeader, ownerCookie).
        WithJSON(map[string]any{"peer_id": peerID}).
        Expect().Status(201))
    return int64(dm.Value("id").Number().Raw())
}

// ✅ 集成测试运行命令（落 full log）
go test -tags integration -timeout 30m -count=1 ./tests/integration/... \
    > /tmp/im-batch-b-run2.log 2>&1
```

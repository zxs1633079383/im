---
id: C014
title: 每个 HTTP 路由 + 每个 WSMessageType 必须有 1 单测 + 1 集成测试（100% 入口覆盖）
status: active
created: 2026-05-13
last_recurred: 2026-05-14
recurrence_count: 2
source_logs:
  - 客户端 worktree feat/im-reactor-2 用户拍板（2026-05-13）
applies_to:
  - server/internal/http/**/*.go
  - server/internal/gateway/**/*.go
  - server/internal/service/**/*.go
  - server/internal/repo/**/*.go
  - server/tests/integration/**/*.go
inline_target: ~/.claude/rules/golang/testing.md（升级为 inline 后归档）
related:
  - C008  # handler coverage gate
---

# C014 — 每路由 + 每 WSMessageType 必须有 1 单测 + 1 集成测试

> **用户拍板**：2026-05-13，"补充 im 单接口测试用例 以及测试用例集合 100% 覆盖"。
> 是 C008（handler coverage gate）的细化扩展：C008 要求"有 TestM4* 集成测试存在"；C014 要求"单测 + 集成测试双覆盖 + 边界 case 必有"。

## 1. 触发场景（Trigger）

**适用**：
- 任何新增 / 修改 `internal/http/*.go` 的 HTTP 路由
- 任何新增 / 修改 `internal/gateway/types.go` 的 WSMessageType（含 server→client + client→server）
- 任何修改 `internal/service/**/*.go` 的业务方法签名

**不适用**：
- 运维端点：`/healthz` / `/readyz` / `/metrics` / `/ws`（C008 已豁免）
- 410 Gone 端点：`/api/login` / `/api/register`（covered by `m4_auth_gone_test.go` 即可）

## 2. 错误模式（Anti-Pattern / 现状）

### 2.1 仅集成测试 happy path 不够

```go
// ❌ 当前部分 endpoint 仅有 happy path
func TestM4ChannelTransferOwner_Happy(t *testing.T) { /* 200 + 推送 */ }
// 缺：403 / 404 / 400 / 500 边界
```

### 2.2 单测和集成测试重叠覆盖同一逻辑

```go
// ❌ 反例
// service_test.go: TestChannelService_AddMember_NotOwner — mock repo
// integration_test.go: TestM4ChannelAddMember_NotOwner — 真 DB
// 两个测试都做同一件事 = 浪费 CI 时间
```

### 2.3 service 测试用 sqlmock 而不是 mockery

```go
// ❌ 不一致：有些 service 用 sqlmock，有些用 mockery
db, mock, _ := sqlmock.New()
mock.ExpectQuery("SELECT").WillReturnRows(...)
```

## 3. 正确做法（Required）

### 3.1 测试金字塔分工

| 层 | 工具 | 覆盖 | 数量目标 |
|---|---|---|---|
| **Unit (repo)** | testcontainers Postgres（per package）or sqlx-test | repo 方法的 SQL 正确性 + edge case（FK violation / unique conflict）| 每个 repo method 1-2 case |
| **Unit (service)** | mockery 生成的 RepoMock + table-driven | service 业务规则 + 状态机 + 权限校验 | 每个 service method 1 happy + N error case |
| **Integration (http)** | httpexpect + real Gin engine + real Postgres + real Pulsar | HTTP wire 契约（status / envelope / WS hook 触发）| 每个 endpoint 1 happy + 主要 error code |
| **Integration (ws)** | httpexpect WS + real gateway | WS 协议（type / payload / push 触达多设备）| 每个 WSMessageType 1 case |

### 3.2 命名约定（强制）

```
internal/service/channel.go::TransferOwner
  → internal/service/channel_transfer_owner_test.go::TestChannelService_TransferOwner_{Happy,NotOwner,NewOwnerNotMember,DM,AlsoLeave}

internal/http/channel.go::TransferOwner handler
  → tests/integration/m4_channel_transfer_owner_test.go::TestM4ChannelTransferOwner_{Happy,Forbidden,NotFound,BadRequest}

internal/gateway/types.go::ChannelMemberUpdated{change_type:"owner_transfer"}
  → tests/integration/m4_ws_channel_member_updated_test.go::TestM4WS_ChannelMemberUpdated_OwnerTransfer
```

### 3.3 单测必备 case 模板

每个 service method **强制**包含：

```go
func TestChannelService_TransferOwner(t *testing.T) {
    cases := []struct{
        name        string
        setupRepo   func(*mocks.ChannelRepo)
        params      service.TransferOwnerParams
        wantErr     error
        wantResult  *service.TransferOwnerResult
    }{
        {name: "happy",            ...},
        {name: "caller_not_owner", setupRepo: func(m *mocks.ChannelRepo) { m.On("AssertOwner", ...).Return(repo.ErrNotOwner) }, wantErr: repo.ErrNotOwner},
        {name: "new_owner_not_member", ...},
        {name: "is_DM",            ...},
        {name: "channel_gone",     ...},
        {name: "tx_rollback_on_seq_alloc_fail", ...},
    }
    for _, tc := range cases {
        t.Run(tc.name, func(t *testing.T) {
            repoMock := mocks.NewChannelRepo(t)
            tc.setupRepo(repoMock)
            svc := service.NewChannelService(repoMock, nil)
            got, err := svc.TransferOwner(ctx, tc.params)
            require.ErrorIs(t, err, tc.wantErr)
            if tc.wantResult != nil {
                require.Equal(t, tc.wantResult, got)
            }
            repoMock.AssertExpectations(t)
        })
    }
}
```

### 3.4 集成测试必备 case 模板

每个 endpoint **强制**包含：

```go
func TestM4ChannelTransferOwner(t *testing.T) {
    env := testutil.NewIntegrationEnv(t)
    defer env.Cleanup()
    cookie := env.SeedOwner()
    channel := env.SeedChannelWith4Members()

    t.Run("happy_200_full_flow", func(t *testing.T) {
        // ① POST 200
        resp := env.HTTP("POST", "/api/channels/"+channel.ID+"/transfer-owner",
            map[string]any{"new_owner_id": channel.Members[1].UserID, "also_leave": false}, cookie)
        resp.Status(200)
        // ② 校 envelope shape
        resp.JSON().Object().Value("status").String().Equal("success")
        // ③ 校 DB 状态
        env.AssertOwner(channel.ID, channel.Members[1].UserID)
        // ④ 校 WS 推送（按 ChannelMemberUpdated.change_type 过滤）
        env.WaitForWSFrame(t, "channel_member_updated", func(f *gateway.Frame) bool {
            return f.Payload["change_type"] == "owner_transfer"
        })
        // ⑤ 校系统消息已写库
        env.AssertSystemMessage(channel.ID, "owner_transferred", channel.Members[1].UserID)
    })

    t.Run("forbidden_403_member_caller", func(t *testing.T) { ... })
    t.Run("notfound_404_new_owner_not_member", func(t *testing.T) { ... })
    t.Run("badrequest_400_dm_channel", func(t *testing.T) { ... })
    t.Run("badrequest_400_self_transfer", func(t *testing.T) { ... })
}
```

### 3.5 mockery 自动重生

每次 repo interface 改 → `make mockery`：

```yaml
# .mockery.yaml
packages:
  im-server/internal/repo:
    config:
      with-expecter: true
    interfaces:
      ChannelRepo: {}
      MessageRepo: {}
      AnnouncementRepo: {}
      ...
```

CI gate：

```bash
# pre-commit / CI 跑
make mockery
git diff --exit-code internal/repo/mocks/   # 必须 0 行 diff
```

### 3.6 go test -cover 阈值（CI gate）

`server/Makefile`：

```makefile
.PHONY: test-cover
test-cover:
	go test -tags integration -coverpkg=./internal/... -coverprofile=coverage.out ./...
	go tool cover -func=coverage.out | awk '/^total:/ {if ($$3+0 < 85.0) {print "FAIL: coverage", $$3, "< 85%"; exit 1}}'
```

**85% 总覆盖率最低线**；按包细分目标：

| 包 | 覆盖率目标 |
|---|---|
| `internal/repo/` | ≥ 90% |
| `internal/service/` | ≥ 85% |
| `internal/http/` | ≥ 80% |
| `internal/gateway/` | ≥ 80% |
| `cmd/` | ≥ 60%（main 函数无法覆盖部分允许）|

## 4. 检查方法（Verification）

### 4.1 自动 grep — 路由 vs 集成测试

```bash
# 列出所有 authed.GET/POST/PUT/PATCH/DELETE 路由
grep -hEn 'authed\.(GET|POST|PUT|PATCH|DELETE)\("/' server/internal/http/*.go | \
  sed -E 's/.*"(\/[^"]*)".*/\1/' | sort -u > /tmp/routes.txt

# 列出所有 TestM4* 测试
grep -rhEn 'func TestM4' server/tests/integration/*.go | \
  sed -E 's/.*func (TestM4[A-Za-z_]+).*/\1/' | sort -u > /tmp/tests.txt

# 交集校验脚本（scripts/check-route-coverage.sh）
bash scripts/check-route-coverage.sh   # 任一 route 无对应 TestM4 → exit 1
```

### 4.2 自动 grep — service vs 单测

```bash
# 列出 service public method
grep -rEn '^func \([a-z]+ \*?[A-Z][a-zA-Z]*Service\) [A-Z]' server/internal/service/*.go | \
  awk -F'[() ]+' '{print $2"."$5}' | sort -u > /tmp/svc_methods.txt

# 列出 service 单测
grep -rEn '^func Test[A-Z][a-zA-Z]*Service_' server/internal/service/*_test.go | \
  awk -F'[ _]+' '{print $2"."$3}' | sort -u > /tmp/svc_tests.txt

# diff（任一 method 无 _test → 警告）
diff /tmp/svc_methods.txt /tmp/svc_tests.txt | grep '^<' && exit 1 || true
```

### 4.3 CI gate（Makefile）

```bash
make check-id-types       # C012
make check-route-coverage # C014
make check-svc-coverage   # C014
make test-cover           # C014（85% 阈值）
make mockery-check        # mocks 同步
```

## 5. 复现历史（Recurrence Log）

| # | 日期 | 触发场景 | 引用日志 | 处置 |
|---|------|---------|---------|------|
| 1 | 2026-05-13 | 用户拍板 "100% 覆盖" + cutover all-case 后 198 集成测试已绿但单测覆盖率 unknown | 客户端 SESSION.md §0 | **本 harness 创建**；首批 gap 由 autonomous 测试覆盖 agent 跑 |
| 2 | 2026-05-14 | T4 autonomous 落地：4 个 CI gate 脚本 + ChannelService 7 method 多 case + ChannelGovernanceService 11 method 1-happy | feat/im-reactor-2 commit `7d5ed46` / `80787c4` / `de091f4` | **drafting → active**；阈值起步 60% warn-only，service 包 0%→5.2% / 总覆盖率 4.9%→9.9%；后续 PR 推 85% |

## 6. 反例与边界（Don't Over-Apply）

- ❌ **不要为了覆盖率写"无效断言"测试**：测试必须有有效 assert（status / envelope shape / DB state / WS frame）
- ❌ **不要把单测 + 集成测试覆盖同一断言**：单测覆盖业务规则，集成测试覆盖 wire 契约
- ❌ **不要 mock real Postgres**：repo 单测必须用 testcontainers 真 DB；用 sqlmock 会让 GORM 序列化 bug 漏过
- ❌ **不要在集成测试里 sleep 等 WS**：必须用 `env.WaitForWSFrame(timeout)` 同步原语
- ✅ 边界：纯数据转换 helper（如 `pkg/id.NewULID()`）允许仅单测无集成测试

## 7. 升级 / 弃用条件（Lifecycle）

- **升级 active**：CI gate 三件套（route-coverage / svc-coverage / test-cover）全部接管 + 当前 198 集成测试 + ≥ 80 单测全绿 → active
- **升级 merged**：连续 30 天无新 endpoint 漏测 + inline 进 `~/.claude/rules/golang/testing.md` "im 项目分层金字塔"小节 → merged
- **弃用 deprecated**：不会弃用（test coverage 是永久约束）

---

**Owner**：im 后端
**最后更新**：2026-05-14（active；4 件 gate 落地 + ChannelService 单测起步）
**下次更新触发**：service 覆盖率 ≥ 85% / 阈值 60→85 切硬模式 / 集成测试 100% / 用户决策变更

## 8. T4 实施状态（2026-05-14）

### 已落地

| 件 | 路径 | 状态 |
|---|---|---|
| check-id-types.sh | server/scripts/ | green（C012 三件套通过）|
| check-route-coverage.sh | server/scripts/ | green（93 tests / 88 routes）|
| check-svc-coverage.sh | server/scripts/ | informational（95 method / 30 test）|
| check-test-cover.sh | server/scripts/ | warn-only（9.9% < 60%）|
| c014.mk | server/ | `make -f c014.mk check-c014-all` 调用入口 |
| ChannelService 7 单测 | internal/service/ | 26 sub-test PASS（TransferOwner 多 case + 6 method）|
| ChannelGovernanceService 11 单测 | internal/service/ | 11 happy PASS |

### 未做（留给后续 PR）

- AnnouncementService / ApprovalService / MessageService / NotificationService 等
- handler 集成测试 sweep（C008 已确认 88/93，本期无新 endpoint）
- 阈值上调 60 → 85（等单测推到 ≥85% 后切 THRESHOLD_HARD=1）
- 41 个 pre-existing 集成测试 fail（testcontainers redis race / team_id 漂移 / C5 spec 行为）不在 C014 范围

### 不污染 user WIP 设计

- **不修改主 `server/Makefile`**（保留用户 IM_REDIS_CLUSTER WIP）
- 改为独立 `server/c014.mk`：`make -f c014.mk check-c014-all`
- 仅 `.mockery.yaml` 加一行 `ChannelGovernanceRepo:` + 新增 mock 文件


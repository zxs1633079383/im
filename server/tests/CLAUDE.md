# CLAUDE.md — server/tests 模块级指令（集成测试层）

> 本文件约束 `server/tests/`（当前只有 `integration/` 一个子目录）：端到端集成测试，覆盖 84 路由 + 22 WSMessageType。
> 优先级：用户全局 `~/.claude/CLAUDE.md` > 项目根 `/Users/mac28/workspace/golangProject/im/CLAUDE.md` > `server/CLAUDE.md` > **本文件** > 默认行为。
> 与子目录冲突时遵循「更具体者优先」。本目录无更深一级 CLAUDE.md。

---

## 0. 模块定位

**是什么**：im 后端**端到端集成测试**——黑盒视角验证 HTTP wire 契约 + WS 推送 + DB 状态 + Pulsar 跨 pod 转发。**CI gate**：必须全绿才能 merge 主干。

**负责**：
- ✅ **84 路由 × 1 happy path 最低**（关键 family 5 case 矩阵 C1-C5）—— C008 锁底线
- ✅ **22 WSMessageType × 1 case 最低**（关键 type 6 case 矩阵 W1-W6）—— C008 / C014
- ✅ **wire 契约证明**：status code / envelope shape / 字段拼写 / 时间戳 / 排序
- ✅ **DB 状态断言**：写完后查表，不靠 service mock
- ✅ **WS 推送同步原语**：`env.WaitForWSFrame` 同步等推送帧，禁 `time.Sleep` 蒙
- ✅ **跨 pod 转发**：起 2 个 Pulsar 实例 / 1 个共享 broker，验证 `CrossPodPush` 转发与 ACK 闭环

**不负责**（**严禁**在本层写）：
- ❌ 业务实现 / handler / service / repo —— 集成测试**只**调 HTTP，不 import handler 函数直调
- ❌ 单测覆盖业务规则 —— 那是 `internal/service/*_test.go` 的职责（C014 单测 + 集成测试分工）
- ❌ mock 真实依赖 —— 集成测试**必须**用 testcontainers 真 PG / Redis / Pulsar（C014 §6 反例）；用 sqlmock / mockery 会让 GORM 序列化 bug 漏过
- ❌ 启动 / 装配代码 —— 装配走 `m4_harness_test.go::buildEngine`（与 `cmd/gateway/main.go` 镜像）

---

## 1. 影响范围

**上游依赖**（本层依赖谁）：

| 依赖 | 来源 | 备注 |
|---|---|---|
| `internal/http/router.go` + 所有 handler | `server/internal/http/` | 路由变 → 集成测试必须同步加 |
| `internal/gateway/types.go::WSMessageType` | `server/internal/gateway/` | 新 type → `m4_ws_*_test.go` 必须加 W1 case |
| `internal/middleware/*` | `server/internal/middleware/` | shape 改 → `m4_harness_test.go::buildEngine` 同步 + C009 sweep |
| `internal/testutil` + `internal/testutil/containers` | `server/internal/testutil/` | helper 唯一来源 |
| `migrations/*.up.sql` | `server/migrations/` | PG 启动时全量灌入；漏 column → 集成测试启动即崩 |
| `github.com/gavv/httpexpect/v2` | go.mod | 路径禁拼 `?` 见 C006 |

**下游影响**（改这里波及谁）：

| 改动 | 波及面 |
|---|---|
| 集成测试用例数下降 | C008 `check-handler-coverage.sh` exit 1，CI 卡死；merge 前必须查到原因 |
| 测试随机 fail 率上升 | C015 testcontainers race 嫌疑；按 §6.5 排查 |
| 加新 endpoint 漏写集成测试 | C008 grep 一一对应表会报新增路由无 `TestM4*` 入口 |
| 新 WSMessageType 漏 W1 case | 同上，且 cses-client 联调阶段才暴露 |

---

## 2. 功能模块清单

### 2.1 测试文件结构

集成测试**一律命名** `m4_<scope>_test.go`，对应 `TestM4<Scope>_<Case>` 函数：

| 文件 | endpoint family | 路由数 ≈ | case 矩阵 | 状态 |
|---|---|---|---|---|
| `m4_harness_test.go` | **基座**：testcontainer 启动 + Gin engine 装配 + seed* helper（seedDM/seedGroup/seedUser） | — | — | active |
| `m4_auth_smoke_test.go` | 鉴权冒烟（cookie missing / invalid） | — | C2-C3 共用 | active |
| `m4_channel_dm_test.go` / `m4_channel_group_test.go` / `m4_channel_governance_test.go` / `m4_channel_transfer_owner_test.go` | channel 创建 / 治理 / owner transfer | ~12 | 5-case | active |
| `m4_message_write_test.go` / `m4_message_read_test.go` / `m4_message_sync_test.go` / `m4_template_received_test.go` / `m4_read_stats_test.go` | message send / list / edit / delete / read / template / read-stats | ~9 | 5-case（Batch-B） | active |
| `m4_friend_test.go` / `m4_favorite_urgent_test.go` / `m4_topic_test.go` / `m4_scheduled_test.go` / `m4_notification_quickreply_test.go` / `m4_approval_test.go` | friend / favorite / urgent / topic / scheduled / notification / quick reply / approval | ~30 | 1 happy（Batch-A/C） | active |
| `m4_announcement_reaction_test.go` | announcement / reaction 合并 | ~8 | 1 happy（Batch-C） | active |
| `m4_batch_e_test.go` | module / sync / presence / settings | ~6 | 1 happy（Batch-E） | active |
| `m4_ws_fixture_test.go` | WS 公共 fixture（建连 / 等帧 / 多 device） | — | — | active |
| `m4_ws_channel_friend_events_test.go` / `m4_ws_governance_events_test.go` / `m4_ws_message_events_test.go` / `m4_ws_reaction_heartbeat_test.go` | 22 WSMessageType happy path + 2 push hook | 22 type | W1 happy | active |

**合计**：≈ 84 路由 + 22 WSMessageType + 2 push hook，≈195 测试函数（2026-05-07 收尾，C008 §4.4 落地清单）。

### 2.2 build tag 与执行

- 所有文件必须 `//go:build integration` 头（与 `internal/testutil/containers/` 一致）
- 跑命令：`make verify-integration`（内含 `-tags=integration -timeout 30m -race`）
- 单文件：`go test -tags integration -v -run TestM4ChannelTransferOwner ./tests/integration/...`
- **禁止**用 `tail -100` 看输出（C009 教训）—— full log 落 `/tmp/im-integration-*.log` 再 `grep -cE '^--- FAIL:'`

### 2.3 CI gate 关联

| Gate | 脚本 | 触发 |
|---|---|---|
| 路由 + 测试函数下限（MIN_ROUTES=84 / MIN_TESTS=190） | `scripts/check-handler-coverage.sh` | `make check-handler-coverage` |
| Route ↔ TestM4* 一一对应 | `scripts/check-route-coverage.sh` | `make check-route-coverage` |
| service method ↔ 单测 | `scripts/check-svc-coverage.sh` | `make check-svc-coverage`（informational）|
| 总覆盖率 ≥ 85% | `make test-cover`（C014 §3.6） | warn-only → 单测 ≥85% 后切 hard |

---

## 3. SOP — 加 / 改集成测试工作流

**任何 tests/ 改动**必须按以下顺序：

```
0. 开局：cat SESSION.md | head -80  &&  ls docs/harness/
1. 写代码前：Skill(skill="go-concurrency-patterns")
2. 加新 endpoint → 必须**同时**：
   a) handler 实现：server/internal/http/<file>.go
   b) 路由注册：server/internal/http/router.go::authed.<METHOD>(...)
   c) service 单测：server/internal/service/<svc>_test.go（C014 单测层）
   d) 集成测试：server/tests/integration/m4_<scope>_test.go::TestM4<Scope>_Happy（C014 集成层）
3. 测试 RED → 实现 → GREEN：
     go test -tags integration -v -run TestM4<Scope>_Happy ./tests/integration/...
4. 改 middleware → C009 sweep：
     grep -rEn '\.Expect\(\)\.Status\([0-9]+\)\.JSON\(\)\.(Object|Array)\(\)' \
       server/tests/integration/m4_harness_test.go server/internal/testutil/
   把 raw 解析的 helper 调用点全部对齐 successBody/successBodyArray/errorBody
5. 跑 C008 gate：
     make check-handler-coverage
   exit 1 → 加路由忘了写 TestM4*；按提示补
6. 跑全集成测试（full log，禁 tail）：
     make verify-integration > /tmp/im-integration-$(date +%Y%m%d-%H%M).log 2>&1
     grep -cE '^--- FAIL:' /tmp/im-integration-*.log
   FAIL 数必须 = 0
7. race detector：
     go test -race -tags integration -count=1 ./tests/integration/...
8. 跑 C006 grep（必须 0 条）：
     grep -rEn '(expect|e)\.(GET|POST|PUT|PATCH|DELETE)\("[^"]*\?' server/tests/
9. 更新 server/scripts/check-handler-coverage.sh 的 MIN_ROUTES / MIN_TESTS baseline（如增量超出当前阈值）
10. commit（见 §5）+ 更新 SESSION.md §2
```

**特殊路径**：加新 WSMessageType 必须同步改 `gateway/types.go` + `m4_ws_*_test.go` + `docs/harness/C005`（22 锁定门）；改 testcontainers helper 见 `testutil/CLAUDE.md §3 / §8.2`；随机 fail 先查 C015 retry 兜底。

---

## 4. Pre-commit 自检清单

### 4.1 必跑命令（按顺序）

```bash
cd /Users/mac28/workspace/golangProject/im/server

# A. C008 路由 + 测试函数下限
make check-handler-coverage

# B. C014 route ↔ test 一一对应
make check-route-coverage

# C. 全集成测试（full log，禁 tail）
make verify-integration > /tmp/im-integration-$(date +%Y%m%d-%H%M).log 2>&1
grep -cE '^--- FAIL:' /tmp/im-integration-*.log    # 必须输出 0

# D. race detector
go test -race -tags integration -count=1 -timeout 30m ./tests/integration/... \
  > /tmp/im-integration-race.log 2>&1
grep -cE '^--- FAIL:' /tmp/im-integration-race.log # 必须输出 0

# E. 覆盖率（warn-only 当前；切 hard 见 C014 §8）
make test-cover
```

### 4.2 Grep gate 清单（必为 0 才能 commit）

| Gate | 命令 | 对应 harness |
|---|---|---|
| httpexpect 路径拼 `?` | `grep -rEn '(expect\|e)\.(GET\|POST\|PUT\|PATCH\|DELETE)\("[^"]*\?' server/tests/ --include='*_test.go'` | C006 |
| helper raw 解析响应 | `grep -rEn '\.Expect\(\)\.Status\([0-9]+\)\.JSON\(\)\.(Object\|Array)\(\)' server/tests/integration/m4_harness_test.go --include='*.go'` | C009 |
| testcontainers 直 MappedPort | `grep -rEn '\.MappedPort\(' server/tests/integration/ \| grep -v '_via_helper'` | C015 |
| sleep 等 WS 帧 | `grep -rEn 'time\.Sleep' server/tests/integration/ --include='*_test.go' \| grep -i 'ws\|frame\|push'` | C014 §6 |
| 业务字段写死字符串绕过 const | `grep -rn '"channel_member_updated"\|"msg_send"' server/tests/integration/ \| grep -v gateway\.WS` | C005 |
| 跳过测试不解释 | `grep -rn 't\.Skip\(' server/tests/integration/ --include='*_test.go'` | C014 |

### 4.3 失败处置

- `check-handler-coverage` exit 1 → 新加路由忘写 TestM4* / 某 family 完全没覆盖
- `verify-integration` FAIL > 0 → **绝对不许** `tail -100`（C009 教训）；落 file + `grep '^--- FAIL:'` 数 + 看完整 stack
- redis race fail（`port "6379/tcp" not found`）→ C015 retry 应已兜底；仍 fail 查 `containers/redis.go` retry
- WS 测试 timeout → 99% 是 `hub.PushToUser` 漏跨 pod（C002）/ envelope 双 wrap（C007）/ `MMTeamHeader` 漏 → `TeamIDFromCtx` 空 → C010 鉴权失败

---

## 5. Commit 规范

沿用 `~/.claude/rules/common/git-workflow.md` Conventional Commits，**中文 body 强制**。

### 5.1 本层常用 scope

| scope | 适用 |
|---|---|
| `integration/<family>` | 单 family 集成测试加 / 改（如 `integration/channel`、`integration/message`、`integration/ws-message`）|
| `integration/harness` | `m4_harness_test.go` / `m4_ws_fixture_test.go` 基座改动 |
| `integration/batch-X` | Batch-A/B/C/D/E 批次落地 |

### 5.2 示例

```
test(integration/channel): 补 TransferOwner 4 个边界 case

加 403_forbidden / 404_new_owner_not_member / 400_dm_channel /
400_self_transfer，与 happy_200_full_flow 合并到 5-case 矩阵
（C008 + C014）。WS frame 用 env.WaitForWSFrame 同步等
ChannelMemberUpdated{change_type:"owner_transfer"}。
```

```
test(integration/ws-message): 补齐 22 WSMessageType 的 W1 happy path

最后 2 个 type（channel_info_updated / channel_top_updated）
push hook 顺势补齐，对齐 docs/harness/C005 锁定的 22 种事件。
跑 make verify-integration 197/197 PASS。
```

### 5.3 禁止项

- ❌ `update integration tests` / `add tests` / `wip`
- ❌ 一个 commit 既加 happy path 又加边界 case（拆开，每 case 一 commit）
- ❌ `t.Skip("flaky")` 不写 issue 链接（要么定位 race 修掉，要么删测试）
- ❌ `--no-verify` 跳 hook

---

## 6. 约束规范（本层强约束）

### 6.1 84 路由 + 22 WSMessageType 每个必有 TestM4 集成测试（C008）

每个 `authed.<METHOD>("/api/...")` 路由 → 至少 1 个 `TestM4<Scope>_<Case>` 函数。
每个 `gateway.WSMessageType` 常量 → 至少 1 个 `TestM4WS_<TypeName>_<Case>` 函数。

CI gate（`server/scripts/check-handler-coverage.sh`）卡：

- 路由数 ≥ MIN_ROUTES（当前 84）
- TestM4* 函数数 ≥ MIN_TESTS（当前 190）
- 每个 endpoint family（announcement/approval/channel/favorite/friend/message/notification/presence/quick_reply/reaction/scheduled/urgent/module/sync/settings）至少 1 个对应 TestM4*

加新 endpoint 但没加测试 → CI 直接拒。

### 6.2 单测 + 集成测试双覆盖（C014）

| 层 | 工具 | 覆盖 | 数量 |
|---|---|---|---|
| **单测** | mockery 生成 RepoMock + table-driven | service 业务规则 / 状态机 / 权限 | 每 method 1 happy + N error |
| **集成** | httpexpect + real Gin + real PG + real Pulsar | HTTP wire 契约 + WS 推送 + DB 状态 | 每 endpoint 1 happy + 主要 error code |

**绝对禁止**：
- ❌ 把单测和集成测试覆盖同一 assertion（service 测业务规则；集成测 wire 契约）
- ❌ 单测用 sqlmock 替代 testcontainers PG（GORM 序列化 bug 会漏过）
- ❌ "覆盖率写法"：写一堆无 assert 的测试凑覆盖率分子

### 6.3 httpexpect v2 路径禁拼 `?q=`（C006）

```go
// ❌ 禁止
expect.GET("/api/messages/read-stats?ids=1,2,3").Expect()

// ✅ 必须
expect.GET("/api/messages/read-stats").
    WithQuery("ids", "1,2,3").
    WithHeader(middleware.MMCookieHeader, cookie).
    WithHeader(middleware.MMTeamHeader, testutil.RealCompanyID).
    Expect().Status(200)
```

详细见 C006；本层是 C006 的最大调用方。

### 6.4 middleware 形态对齐（C009）

`m4_harness_test.go::buildEngine` 是 `cmd/gateway/main.go::buildRouter` 的**唯一**镜像。任何中间件改动 PR 必须：

1. 同步改 `buildEngine`
2. 同步 sweep helper（seedDM / seedGroup / seedUser 等）让 response 解析走 unwrap helper（`successBody` / `successBodyArray` / `errorBody`），不许 raw `.JSON().Object()`
3. 全集成测试跑 full log（**禁 tail**），FAIL = 0 才 commit

Run #1 ~120 fail 教训：`tail -100` 只看到末尾 2 fail，前面 ~120 fail 全丢，误以为只挂 2 个测试。

### 6.5 testcontainers v0.35.0 race retry（C015）

集成测试用到 redis / postgres / pulsar 必须经 `testutil.containers.Start*`（已带 retry 兜底）。

- **禁止**绕过 helper 直接 `tcredis.Run` + `MappedPort`
- **禁止**改 retry 上限大于 5 / backoff 大于 200ms 来掩盖 race
- **禁止**因 race 切换到 docker-compose 双源（破坏测试 isolation，FX1 教训 5+ 小时误诊）

random fail 排查顺序：
1. 看 `containers/redis.go::StartRedis` retry helper 是否还在
2. 看 testcontainers-go go.mod 版本是否被改
3. 看 Docker daemon 是否有资源（df -h / docker system df）
4. **最后**才怀疑业务代码 race

---

## 7. 对应 Harness 映射

| Harness | 触发场景（本层视角） | 验证手段 |
|---|---|---|
| [C006](../../docs/harness/C006-httpexpect-query-encoding.md) | 任何新加 httpexpect 调用 | §4.2 grep `?` 必 0 条；所有 query 必走 `.WithQuery` |
| [C008](../../docs/harness/C008-handler-coverage-gate.md) | 加 / 改路由 / WSMessageType | `make check-handler-coverage` 必绿；MIN_ROUTES=84 / MIN_TESTS=190 |
| [C009](../../docs/harness/C009-test-helper-tracks-middleware-shape.md) | 改 middleware / harness helper shape | full log FAIL=0；禁 tail；helper 走 successBody/successBodyArray/errorBody |
| [C014](../../docs/harness/C014-test-coverage-100-percent.md) | 加路由 / WSMessageType | 每条 1 单测 + 1 集成测试；CI 阈值 85%（warn-only → hard）|
| [C015](../../docs/harness/C015-testcontainers-redis-port-race.md) | 集成测试随机 fail / 升级 testcontainers-go | retry helper 在；5 轮跑 0 race fail |

**间接关联**：C001（seq 连续性断言）/ C002（跨 pod push 验证）/ C003（本地 topic 后缀）/ C005（22 WS 锁定）/ C007（envelope 单层）/ C010 + C011（cookie + team_id nullable 双路径）。

---

## 8. Update / Insert 规则

### 8.1 新增 endpoint = 必加 1 单测 + 1 集成测试

强制顺序：

1. **handler** 实现：`server/internal/http/<file>.go`
2. **路由注册**：`server/internal/http/router.go::authed.<METHOD>(...)`
3. **service 单测**：`server/internal/service/<svc>_test.go::TestXxxService_<Method>_<Case>`（mockery + table-driven）
4. **集成测试**：`server/tests/integration/m4_<scope>_test.go::TestM4<Scope>_<Case>`（C008 矩阵 + C014 wire 契约）
5. **更新 `server/scripts/check-handler-coverage.sh`** MIN_ROUTES / MIN_TESTS baseline（如增量超阈值）
6. **更新 `server/docs/BACKEND.md` §对应小节** 把新 endpoint 写进契约
7. **更新 `docs/E2E_REPORT.md`** 把新增 case 数累加到统计行
8. **commit scope** = `integration/<family>` + Phase tag（如果属于多 Phase 切换）

### 8.2 新增 WSMessageType

更高门槛（C005 22 锁定）：

1. 先开 RFC（`docs/RFC/`）说明 type 名称 / payload shape / 是否影响 cses-client
2. 用户拍板
3. 改 `internal/gateway/types.go` + `internal/handler/ws/*.go`
4. 加集成测试 `m4_ws_<type>_test.go::TestM4WS_<TypeName>_Happy`
5. 更新 `docs/harness/C005` 复现表 + 22 → 23 计数
6. 更新 `server/docs/BACKEND.md §对应小节`
7. cses-client `feat/im-reactor-2-offine` 分支同步加 handler

### 8.3 改 router_coverage 表

`server/docs/BACKEND.md` 与 `docs/E2E_REPORT.md` 维护 router ↔ TestM4 对应表。**新增 / 删除路由必须同步两处**，否则下次 review 会找不到 endpoint owner。

### 8.4 升级 httpexpect / testcontainers-go

升级前后各跑 100 轮 `make verify-integration` 对比 race fail 率。httpexpect v3 可能修复 `?` encoding → 考虑 deprecate C006；testcontainers v0.36+ 可能修复 redis port race → 考虑 deprecate C015。

---

## 9. 文档关联

| 文档 | 在哪 | 用途 |
|---|---|---|
| 项目根 CLAUDE.md | `/Users/mac28/workspace/golangProject/im/CLAUDE.md` | im 全局约束 |
| server/CLAUDE.md | `server/CLAUDE.md` | cmd / Makefile / go.mod 装配 |
| testutil/CLAUDE.md | `server/internal/testutil/CLAUDE.md` | helper / cookie / containers 上游 |
| 后端契约 | `server/docs/BACKEND.md` | M1–M6 wire 契约 |
| E2E 报告 | `docs/E2E_REPORT.md` | 集成测试 case 累计 + Batch 清单 |
| Harness | `docs/harness/C006 / C008 / C009 / C014 / C015` | 本层全部上游约束 |
| Go testing 规则 | `~/.claude/rules/golang/testing.md` | 100% / 表驱动 / httpexpect 陷阱 |
| Skill | `~/.claude/skills/go-concurrency-patterns/SKILL.md` | 写 Go 唯一标准 |

---

> 维护：本文件每次新增 endpoint family / 新加 WSMessageType / 调 MIN_ROUTES MIN_TESTS baseline / 升级 httpexpect 或 testcontainers-go major 时同步审。本目录是集成测试层最末梢，下游就是 cses-client 联调与 prod 部署。

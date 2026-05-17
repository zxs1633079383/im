# CLAUDE.md — `server/internal/http/` HTTP Handler 模块指令

> 这是 im 项目 **HTTP handler 层**的模块级指令，优先级介于项目根 `CLAUDE.md` 与用户全局 `~/.claude/CLAUDE.md` 之间。
> 任何 `server/internal/http/**/*.go` 改动必须先扫读本文 + `docs/harness/{C006,C007,C008,C011,C013,C014}.md` + `~/.claude/skills/go-concurrency-patterns/SKILL.md`。

---

## 0. 模块定位

本模块是 im 后端 **REST API 唯一入口**（gin engine），职责严格收敛为三件事：

1. **参数绑定 / 校验**：路径 `:id` / query `?ids=` / body JSON → 业务结构体；非法 → 4xx
2. **service 调用 + 错误码翻译**：`errors.Is(err, service.ErrXxx)` → HTTP status code
3. **响应包装**：`c.JSON(code, businessData)` 交给全局 `responseEnvelope` 中间件统一 wrap

**严禁**：
- ❌ handler 内直接写业务规则（如成员校验、seq 分配、状态机）—— 全部下沉到 `internal/service/`
- ❌ handler 内直接执 SQL / 调 repo —— 必须经 service 层（除少数无业务的纯转发场景，且需 review 时同意）
- ❌ handler 内 spawn goroutine 不带 `context.Background()` 兜底（参考 `message.go::pushToMembers`）
- ❌ handler 内 wrap 任何 `{"status":"success","data":...}` shape —— 全局中间件已接管（C007）

---

## 1. 影响范围

| 方向 | 依赖 |
|---|---|
| 上游 | Angular `cses-client` (`ImApiAdapter`) / Tauri Rust client / Apifox 接口契约 |
| 下游 | `internal/service/*`（业务）/ `internal/repo/*`（间接，禁直调）/ `internal/middleware/*`（auth / cookie / team header）|
| 同层 | `gateway/*` 推送 hook（`MessagePusher` / `MessageEventBroadcaster`）经 `MessageRouteOpts` 注入 |
| 注册口 | **`router.go::New(Config)`** 唯一入口；所有 `authed.{GET,POST,PUT,PATCH,DELETE}` 必须在某个 `RegisterXxxRoutes` 函数里挂上，且最终被 `cmd/gateway/main.go` 调用注册 |

**改动雷区**：
- 改路由命名 → 必须同步 cses-client `ImApiAdapter` + `docs/HTTP_WS_MAP.md` + Apifox（走 `/apifox-sync-interface` skill）
- 改 envelope 字段 → 改 `response_envelope.go` 一处，全 22 handler 受影响（C007）
- 改 middleware 链 → 必须 sweep `tests/integration/` 全部 helper（C009 教训）

---

## 2. 功能模块清单

| 文件 | 端点 family | 路由数 | 维护语境 |
|---|---|---:|---|
| `router.go` | gin engine 装配 + CORS + envelope middleware 注册 + Legacy 兜底 | — | **路由唯一注册入口**，新增 endpoint 必须经 `Register*Routes` 挂载 |
| `response_envelope.go` | 全局响应包装中间件 + skip list (`/healthz`/`/readyz`/`/metrics`/`/ws`) | — | C007 SoT；handler 改动**绝对不动这里** |
| `response_envelope_test.go` | envelope 白盒单测（8 case，100% 覆盖） | — | improve only — 加 skip 路径 / 加 wrap shape 时同步补 case |
| `auth.go` / `m4_auth_test.go` | `/api/login` `/api/register` 410 Gone（已迁 csesapi） | 2 | fix only |
| `message.go` | message send / list / read / edit / delete / forward / batch / after / around / readers / replies | 11 | **add/improve/fix 高频区**，热路径，所有改动必带 benchmark |
| `message_template.go` | `POST /messages/:id/received`（模板已收到） | 1 | improve only — 受 cutover Phase 2 锁定 |
| `read_stats.go` | `GET /messages/read-stats?ids=...` 批量已读统计 | 1 | improve only — 替代 cses readBits bitmap，C006 重灾区 |
| `channel.go` | 频道 CRUD / member 列表 / 置顶 / 静音 | ~12 | add/improve/fix |
| `channel_close.go` | 群解散 `POST /channels/:id/close` | 1 | improve only |
| `channel_governance.go` | 群规则 / 入群审批 / 邀请权限 | ~8 | add |
| `channel_member_nickname.go` | `PATCH /channels/:id/members/:uid/nickname` | 1 | improve only |
| `channel_topic.go` | `PATCH /channels/:id/topic` | 1 | improve only |
| `channel_transfer_owner.go` | **`POST /channels/:id/transfer-owner`（C013 独立端点）** | 1 | add — 群主转移**唯一**入口 |
| `announcement.go` | 频道公告 CRUD + 已读 | ~5 | improve |
| `approval.go` | 入群 / 加好友审批流 | ~7 | improve |
| `urgent.go` | 加急消息发送 / 取消 / 已读上报 | ~5 | improve（v0.7 加 `EventUrgentCancelled`） |
| `search.go` | 频道 / 消息 / 用户搜索 | 4 | add（cses Java 拥有，迁后补 4 endpoint） |
| `sync.go` | `GET /api/sync` 增量同步（cursor / per-channel）| ~3 | improve — 新增 Kind/NextCursor 字段（feat/im-reactor-2 P-7.5） |
| `friend.go` | 好友关系 / 黑名单 | ~6 | improve |
| `favorite.go` | 收藏 / 取消收藏 | 2 | improve |
| `notification.go` | 通知设置 / 列表 / 已读 | ~5 | improve |
| `presence.go` | 在线状态 / 设备列表 | ~3 | improve |
| `quick_reply.go` | 快捷回复模板 | ~4 | improve |
| `reaction.go` | 消息表情回应 | ~4 | improve |
| `reply_branch.go` | 回复分支线 | ~2 | improve |
| `scheduled.go` | 定时消息 | ~5 | improve |
| `settings.go` | 用户设置 | ~3 | improve |
| `module.go` | 工作台模块开关 | 1 | improve |
| `file.go` | 文件上传 / 下载 / meta | 3 | add — 等 cses 文件存储下线后补 |
| `metrics.go` | `/metrics` Prometheus 端点（skip envelope） | 1 | fix only |

> 累计 **~84 active 路由**，与 `docs/harness/C008 §4.4` 落地清单一致；新增必须同步更新 `MIN_ROUTES`（`server/scripts/check-handler-coverage.sh`）。

---

## 3. SOP — 新增 / 修改 endpoint 标准流程

```
1. 定义路由：选 endpoint family → 决定 method + path → 检查是否已存在（避免与 PATCH /members 冲突，C013 教训）
2. 写 handler 方法：在对应 *.go 文件里加 func（≤ 60 行，参数 ≤ 5）
   - userIDFromCtx(c) 拿 caller
   - pathInt64(c, "id") / c.ShouldBindJSON(&in) 绑参
   - 业务调 svc.XxxYyy(c.Request.Context(), params)
   - switch errors.Is 翻译 4xx/5xx
   - c.JSON(code, businessData)  // ← 不 wrap status/data
3. 注册到 router.go：在 RegisterXxxRoutes(authed *gin.RouterGroup, svc, opts) 里挂 authed.METHOD(path, handler)
   - 若是新 family → 新建 RegisterXxxRoutes + 在 cmd/gateway/main.go 调用
4. service 层方法：internal/service/<family>.go 加 Xxx(ctx, params) → 返 (result, error)；定义 sentinel error
5. 单测 (handler 层)：风格参考 m4_auth_test.go，gin.New() + ResponseEnvelope() 拼测试 engine，httpexpect 跑
6. 集成测试 (端到端)：tests/integration/m4_<family>_<endpoint>_test.go
   - 命名 TestM4<Endpoint>_<Case>（C008/C014 grep gate 必识别）
   - 至少 1 happy + 关键 family 走 5 case 矩阵（C1 happy / C2 cookie missing / C3 cookie invalid / C4 forbidden / C5 bad request）
   - 用 httpexpect v2 — query 必走 .WithQuery（C006）
   - 断言 envelope shape：JSON().Object().Value("status").IsEqual("success").Value("data")...
7. 更新文档：docs/HTTP_WS_MAP.md + Apifox sync（/apifox-sync-interface skill）+ cses-client ImApiAdapter
8. Pre-commit 自检：见 §4
9. Commit：feat(http/<family>): xxx（中文 body 写 why；scope 必带 endpoint family + Phase 编号）
```

---

## 4. Pre-commit 自检清单

```bash
# 4.1 单测 + 集成测试 + race
go test -race -tags integration ./internal/http/...
go test -race -tags integration ./tests/integration/...

# 4.2 C006 — httpexpect 路径禁拼 ?q=
grep -rEn '(expect|e)\.(GET|POST|PUT|PATCH|DELETE)\("[^"]*\?' \
  server/tests/ server/internal/ --include='*_test.go' | wc -l
# 期望 = 0；非 0 → 改 .WithQuery / .WithQueryString

# 4.3 C007 — handler 内禁手动 wrap envelope
grep -rEn 'gin\.H\{[^}]*"status"\s*:\s*"(success|error)"' \
  server/internal/http/ --include='*.go' | grep -v '_test.go' \
  | grep -v 'response_envelope.go' | wc -l
# 期望 = 0
grep -rEn 'gin\.H\{[^}]*"data"\s*:' server/internal/http/ --include='*.go' \
  | grep -v '_test.go' | grep -v 'response_envelope.go' | wc -l
# 期望 = 0

# 4.4 C008 — 路由 ↔ TestM4 一一对应
bash server/scripts/check-handler-coverage.sh
# MIN_ROUTES=84 / MIN_TESTS=190；任一退化 → exit 1

# 4.5 C011 — team_id NOT NULL 不许出现
grep -rEn "team_id\s+TEXT\s+NOT\s+NULL" server/migrations/ | wc -l
# 期望 = 0；同时 service 不许硬 reject 空 companyId

# 4.6 C013 — 群主转移端点存在
grep -n 'transfer-owner' server/internal/http/*.go | wc -l
# 期望 ≥ 1

# 4.7 C014 — service 单测覆盖
bash server/scripts/check-route-coverage.sh
bash server/scripts/check-svc-coverage.sh
# 任一新增 endpoint 漏单测 → 警告（warn-only 阶段）

# 4.8 vet + lint
go vet ./internal/http/...
golangci-lint run ./internal/http/...
```

任一项不过 → **不许 commit**。

---

## 5. Commit 规范

格式：`feat(http/<endpoint-family>): <中文描述>`

- `<type>` ∈ `feat / fix / refactor / perf / test / docs / chore`
- `<scope>` **必须**含 endpoint family（`message` / `channel` / `channel-transfer-owner` / `announcement` / `read-stats` / `sync` …），跨 family 改动 → 拆 commit
- 大 Phase 切换额外带 Phase 编号：`refactor(http/message-v3/channel-read): Phase 3a onChannelRead 6 处切 im`
- 中文 body 写 **why**（动机 / 决策 / 踩过的坑），不写 what（diff 已经说了 what）
- 禁止：`update xxx` / `modify` / `wip` / 无 body 单行 commit

示例：

```
feat(http/channel-transfer-owner): 新增群主转移独立端点（C013）

featdoc 08 line 366-375 强需求 owner 退群必须先选新群主。
不复用 PATCH /members（语义混乱）+ 不并到 leave（未来纯转移场景）。
端点：POST /channels/:id/transfer-owner body {new_owner_id, also_leave}。
5 case 集成测试 + service 7 sub-test 全绿。
```

---

## 6. 约束规范（硬约束）

1. **C007 禁手动 wrap envelope**：handler 内**禁止**写 `gin.H{"status":"success",...}` / `gin.H{"data":...}`。全局 `responseEnvelope()` 中间件已统一接管；错误路径用 `c.AbortWithStatusJSON(40x, gin.H{"error":"..."})` 不要走 `c.JSON(40x, ...)`。
2. **C006 httpexpect 必走 `.WithQuery`**：测试里**禁止**在 path 字符串拼 `?q=v` / `fmt.Sprintf("/api/...?...")` —— `?` 会被 URL-encode，端点 404。
3. **C008 + C014 路由 = 1 单测 + 1 集成测试**：每路由至少 1 happy（C008 grep gate），关键 family 走 5 case 矩阵（C014）；CI gate 由 `check-handler-coverage.sh` 卡死。
4. **C013 群主转移独立端点**：`POST /api/channels/:id/transfer-owner` 是唯一入口，**禁止**塞到 `PATCH /members/:uid` 或 `POST /channels/:id/leave` body 里。DM 频道（type=1）必 400，self transfer 必 400。
5. **C011 `team_id` 缺失不阻塞主流程**：handler 拿 `teamIDFromCtx(c)`，空串是合法值，下游 service 自行决定写 NULL 还是用作过滤；**禁止**在 handler 内 `if teamID == "" { c.JSON(400, ...) }` 主动 reject。
6. **路径 `:id` 全 string（C012）**：feat/im-reactor-2 P-E 后所有 entity ID 是 `string`，handler 用 `c.Param("id")` 直接拿；旧 `pathInt64` helper 留给 seq / numeric 字段（如 `after_seq`）不要混用。
7. **错误路径 `AbortWithStatusJSON`**：4xx/5xx 错误**必须** abort，否则后续 middleware 继续跑 → envelope 与 status 分裂。

---

## 7. 对应 Harness 映射

| Harness | 触发场景 | 验证手段 |
|---|---|---|
| **C006** `httpexpect-query-encoding` | 写 `tests/integration/*_test.go` 用 query string | `grep -rEn '(expect\|e)\.(GET\|POST)\("[^"]*\?'` 必为 0；测试用 `.WithQuery("k","v")` |
| **C007** `response-envelope-no-double-wrap` | 改 `internal/http/*.go` 任何 `c.JSON` | 4 条 grep（`"status":"success"` / `"data":` / `c.JSON(40x,` / `c.Header("Content-Type"`）必为 0；envelope 白盒单测 100% |
| **C008** `handler-coverage-gate` | 新增 `authed.METHOD` 路由 / 新增 WSMessageType | `scripts/check-handler-coverage.sh`：MIN_ROUTES=84 + MIN_TESTS=190 + family 启发式 |
| **C011** `channels-team-id-nullable` | handler / service 处理 `teamID` | grep `team_id TEXT NOT NULL` = 0；grep handler 内 `TeamIDFromCtx() == ""` 主动 reject = 0；集成测试 `TestM4ChannelCreate_NoTeamScope` |
| **C013** `owner-transfer-endpoint` | 群主转移 / `PATCH /members` 改动 | grep `transfer-owner` ≥ 1；5 集成测试 PASS；`PATCH /members` 内禁出现 `role == "owner"` 分支 |
| **C014** `test-coverage-100-percent` | 改 `service/*.go` public method / 新增 route | `check-route-coverage.sh` + `check-svc-coverage.sh` + `check-test-cover.sh`（85% 阈值；当前 warn-only） |

---

## 8. Update / Insert 规则

**新增 endpoint 完整 checklist**（少一项不许合）：

- [ ] handler 文件：在对应 family `.go` 加 func（或新建 family 文件 ≤ 400 行）
- [ ] router 注册：在 `Register<Family>Routes(authed, svc, opts)` 里 `authed.METHOD(path, handler)`；新 family 必须在 `cmd/gateway/main.go` 调用注册
- [ ] service 方法：`internal/service/<family>.go` 加 `Xxx(ctx, params) (result, error)` + sentinel error
- [ ] repo 方法（如需）：`internal/repo/<entity>.go`；消息写入**必经** `AllocSeqAndInsert`（项目根 §1.6）
- [ ] **1 单测**（handler 层，参考 `m4_auth_test.go` 风格）
- [ ] **1 集成测试**（`tests/integration/m4_<family>_<endpoint>_test.go`，命名 `TestM4...` 让 C008 grep 识别）
- [ ] 关键 family（message/channel/urgent/announcement/approval/favorite） → **5 case 矩阵**（happy / cookie missing / cookie invalid / forbidden / bad request）
- [ ] 更新 `docs/HTTP_WS_MAP.md`：路由全表追加一行
- [ ] Apifox sync：调 `/apifox-sync-interface` skill 推到对应目录（状态置「已发布」）
- [ ] cses-client 同步：`ImApiAdapter` 加方法 + `feat/im-reactor-2-offine` 分支编辑（项目根 §0.1）
- [ ] 更新 `MIN_ROUTES` / `MIN_TESTS`（`server/scripts/check-handler-coverage.sh`）若已超基线

**修改既有 endpoint**：

- 改路径 / 方法 → 上述 client / Apifox / HTTP_WS_MAP 三件套全同步；旧路径若仍要兼容 → handler 双注册 + commit body 写迁移窗口
- 改 request / response shape → bump 客户端 `ImApiAdapter` 同步；集成测试加 shape 断言；envelope 字段不动（C007）
- 改错误码 → service sentinel + handler switch 同步；集成测试加错误 case
- **禁止 silent 改动**：任何字段改动必有 commit body 解释 + cses-client 配套 PR

---

## 9. 文档关联

| 文档 | 关系 |
|---|---|
| `docs/HTTP_WS_MAP.md` | 路由全表 + 22 WSMessageType — 新增 endpoint **必同步** |
| `server/docs/BACKEND.md` | M1–M6 详细契约（§3.3 `/api/sync` / §4.1 `AllocSeqAndInsert` / §5 跨 pod 推送 / §响应契约 待补） |
| `docs/CSES_CLIENT_内部对接契约.md` | cses-client 端调用契约（C013 inline target） |
| `docs/harness/C006-httpexpect-query-encoding.md` | httpexpect path query 编码陷阱 |
| `docs/harness/C007-response-envelope-no-double-wrap.md` | 全局响应包装契约 |
| `docs/harness/C008-handler-coverage-gate.md` | 路由 ↔ 集成测试覆盖闸门 |
| `docs/harness/C011-channels-team-id-nullable-no-main-flow-block.md` | team_id NULL 容忍 |
| `docs/harness/C013-owner-transfer-endpoint.md` | 群主转移独立端点 |
| `docs/harness/C014-test-coverage-100-percent.md` | 单测 + 集成测试双覆盖 |
| `server/scripts/check-handler-coverage.sh` | C008 grep gate 实现 |
| `server/scripts/check-route-coverage.sh` / `check-svc-coverage.sh` / `check-test-cover.sh` | C014 gate 实现 |
| `~/.claude/skills/go-concurrency-patterns/SKILL.md` | 写 Go 唯一标准（goroutine / ctx / channel） |
| `~/.claude/rules/golang/testing.md` | 100% 覆盖率 + 5 case 矩阵 inline 目标 |
| 项目根 `/Users/mac28/workspace/golangProject/im/CLAUDE.md` | §1 Go 规则 / §3 commit / §8 harness 索引 |

---

## 10. 历史教训速查

| 教训 | 来源 commit / harness | 一句话 |
|---|---|---|
| handler 双层 wrap envelope → 客户端解析错乱 | C007 / `66c2a67` | 全局中间件接管后 handler **只**传业务对象 |
| httpexpect `GET("/api/x?q=v")` → 404 | C006 / `43c05ab` | 13 处一次性改 `.WithQuery` |
| `internal/http` 行覆盖 3.4% → 客户端联调爆炸 | C008 / `7d5ed46` | 5 batch 落 ~195 集成测试 + CI gate |
| middleware 注入忘改 test helper → Run #1 ~120 fail | C009 | 改 middleware 链必 sweep tests/integration |
| dev PG schema dirty + team_id 缺失 → POST /channels 5xx | C011 | DDL TEXT NULL 锁死 + handler 不预 reject |
| 群主转移塞 `PATCH /members` 语义混乱 | C013 / `a8a36e4` | 独立端点 `POST /channels/:id/transfer-owner` |
| service 0.3% 覆盖率 → 单测 + 集成测试金字塔 | C014 / `7d5ed46`/`80787c4`/`de091f4` | 4 件 gate 脚本 + ChannelService 7 method 单测起步 |

---

**最后更新**：2026-05-17（与 feat-offline-push 分支对齐；84 路由 baseline；C013 + C014 active）
**下次更新触发**：新增 endpoint family / harness 新增 / cses-client cutover Phase 推进 / coverage 阈值上调 60→85

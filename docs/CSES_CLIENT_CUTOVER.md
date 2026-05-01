# cses-client 全面下线 Mattermost · 切换方案

> 一份**可执行**的工程方案。读完它，前端、后端、Rust、QA、PM 各自知道做什么、什么时候做、出问题怎么办。
>
> 当前状态：im 后端 `v0.7.2-no-mattermost` 已部署 pre-7g。
>
> **cses-client 工作目录（2026-05-01 起）**：`/Users/mac28/workspace/angular/temp/cses-client` branch `tauri-new-im` HEAD `836b899ab`。
> 旧的 `/Users/mac28/workspace/angular/cses-client` + `im-backend-switch` 分支 + `7c8a0c972` 是更早期的探路工作，本方案 Phase 2-4 全部在新工作目录上落地。
>
> **Phase 1 状态：✅ 已完成**（2026-05-01）—— 全局响应包裹 + POST `/api/messages/:id/received` + GET `/api/messages/read-stats` 三件均已落地，commit `441ba37` `66c2a67` `ee32f7f` + 文档 commit `65f0763` 已 push origin/main。go vet / golangci-lint / go test -race -short / go test -tags integration 全绿。Phase 2/3/4 待 cses-client 团队在 `temp/cses-client tauri-new-im` 分支上接手。
>
> 关联：wiki 入口 [comparisons/csesapi-vs-im-coverage](../wiki/comparisons/csesapi-vs-im-coverage.md) + [syntheses/min-cost-mattermost-cutover](../wiki/syntheses/min-cost-mattermost-cutover.md)。

---

## 0. 一句话目标

把 cses-client `message-v3` 模块全部 cses-shape HTTP 调用 + bitmap 残留 + readBits 位图依赖 **直接 rewrite** 到新版 im RESTful API，**不留翻译层**、**不留 feature flag 回切**、**WS V1+M2=16 类型锁定不破**，预计 **4 工作日** 完工。

---

## 1. 决策依据（已拍板，不再讨论）

| # | 项 | 决策 |
|---|---|---|
| 1 | rewrite 路线 | **直接 rewrite，不加翻译表/mapper**（与后端 strangler-fig 收官对称） |
| 2 | bitmap 协议（13 条 csesapi）| **整族砍掉**，由 Tauri Rust `ImSeqDataSource` + `POST /api/sync` 接管 |
| 3 | 客户端切换信号 | apiFlavor 由 cses Java 登录响应 `imGatewayHttp` 字段下发，**保留**（已实现）|
| 4 | 已读未读机制 | **C 混合**：seq 游标管 channel-level 未读（已实现）+ 新增**批量** `GET /api/messages/read-stats?ids=...` 端点供 UI 异步查询。**不建新表**，直接 JOIN `channel_members.last_read_seq` |
| 5 | 模板"已收到"数据模型 | **A 复用** `message.props.template.userIds[]`（与 cses 现状对齐）|
| 6 | 模板"已收到" WS 同步 | **B 复用 `msg_updated`** 事件，服务端写完后广播整条消息（不破坏 16 锁定）|
| 7 | snapshot 协议 / 历史已读精度 | 先放着，后续单独讨论平稳迁移 |

---

## 2. 全集统计（来自 Phase 0 audit）

| 维度 | 值 |
|---|---|
| csesapi 全集 | ~125 路由 |
| → im 已有 RESTful 覆盖 | ~50 |
| → 砍掉（bitmap 协议）| 13 |
| → 留 cses Java（GOAL §3 排除）| ~60 |
| **im 后端新增 endpoint** | **2**（`POST /api/messages/:id/received` + `GET /api/messages/read-stats`）|
| im 后端 schema migration | **0** |
| im 后端净 Go 代码改动 | ~125 行 |
| cses-client message.service.ts 待 rewrite | 37 条 |
| cses-client `readBits` 引用 | 33 处（删 12 / 改 15 / 保留 6）|
| Tauri Rust 待删 handler | 1 个（`handle_post_read`，~100 行）|
| 隐性 bug 顺手修 | 2 个（mention 清理失效 + entryUnreadCount 失效） |
| 总工期 | ~4 工作日 |

---

## 3. im 后端改动详单（Phase 1）✅ 完成

| Phase | 状态 | commit | 文件 |
|---|---|---|---|
| 3.1 全局响应包裹 | ✅ 完成 | `441ba37` | `internal/http/response_envelope.go` (新, 100% 单测) + `router.go` (+1 行 `r.Use(responseEnvelope())`) |
| 3.2 POST /api/messages/:id/received | ✅ 完成 | `66c2a67` | `repo/{errors,message,message_props}.go` + `service/message_template.go` + `http/message_template.go` + 集成测试 |
| 3.3 GET /api/messages/read-stats (批量) | ✅ 完成 | `ee32f7f` | `repo/read_stats.go` + `service/read_stats.go` + `http/read_stats.go` + 集成测试 |
| 3.4 既有 endpoint 不变 | ✅ 一对一替换路径已确认 | — | 见 §3.4 表 |
| 3.5 WS 协议 0 改动 | ✅ 复用 EventMsgUpdated | — | 不破坏 V1+M2=16 锁定 |

### 3.1 全局响应包裹（统一）

**目标**：所有 handler 的响应统一格式 `{ status: "success" | "error", data?: T, error?: string }`，前端 interceptor 不再走 `isWrappedResponse` 判断。

**实现位置**：`server/internal/http/router.go` 加一个全局 ResponseWriter 中间件，或在 `internal/http/util.go` 新增 helper：

```go
func writeOK(c *gin.Context, code int, data any) {
    c.JSON(code, gin.H{"status": "success", "data": data})
}
func writeErr(c *gin.Context, code int, msg string) {
    c.JSON(code, gin.H{"status": "error", "error": msg})
}
```

**收口范围**：替换全部 22 个 `internal/http/*.go` 文件里的 `c.JSON(200, X)` / `c.JSON(40x, gin.H{"error": ...})`。约 200 处替换。

> **风险**：现有 6 个 happy-path 集成测试断言响应字段，会全部红。同步改测试断言（~20 处）。

### 3.2 新 endpoint #1：`POST /api/messages/:id/received`

**职责**：模板消息「已收到」按钮写入 `messages.props.template.userIds[]`。

**实现**（`internal/http/message.go`）：

```go
authed.POST("/messages/:id/received", func(c *gin.Context) {
    uid, _ := userIDFromCtx(c)
    msgID, _ := pathInt64(c, "id")

    msg, err := svc.MarkTemplateReceived(c.Request.Context(), msgID, uid)
    switch {
    case errors.Is(err, repo.ErrNotFound): writeErr(c, 404, "message not found"); return
    case errors.Is(err, repo.ErrForbidden): writeErr(c, 403, "not a template message"); return
    case err != nil: log.Error(...); writeErr(c, 500, "internal error"); return
    }

    // 决策 6：复用 msg_updated 广播
    if opts.Broadcaster != nil {
        opts.Broadcaster.BroadcastToMembers(msg.ChannelID, EventMsgUpdated, msg)
    }
    writeOK(c, 200, msg)
})
```

**Service 实现**（`internal/service/message.go`）：
- 读 message → 校验 `msg.props.template != nil`（非模板消息拒绝）→ append uid 到 `userIds[]` (去重) → `UPDATE messages SET props = $1 WHERE id = $2`

**约 30 行 Go**。

### 3.3 新 endpoint #2：`GET /api/messages/read-stats`（批量）

**职责**：UI 异步查询 N 条消息的「谁已读 / 谁未读」，替代 cses 端的 readBits 位图。

**实现**：

```go
authed.GET("/messages/read-stats", func(c *gin.Context) {
    uid, _ := userIDFromCtx(c)
    idsRaw := c.Query("ids")  // "1,2,3"
    ids := parseInt64List(idsRaw)
    if len(ids) == 0 || len(ids) > 100 {  // 单次最多 100 条防爆炸
        writeErr(c, 400, "ids required, max 100"); return
    }

    stats, err := svc.GetReadStatsBatch(c.Request.Context(), uid, ids)
    if err != nil { ... }

    writeOK(c, 200, gin.H{"stats": stats})
})
```

**Service 实现**：
```go
type ReadStat struct {
    MessageID    int64    `json:"messageId"`
    TotalMembers int      `json:"totalMembers"`
    ReadCount    int      `json:"readCount"`
    UnreadCount  int      `json:"unreadCount"`
    UnreadUserIDs []string `json:"unreadUserIds"`  // 截断到前 50，超出加 hasMore
    HasMoreUnread bool    `json:"hasMoreUnread"`
}
```

**Repo SQL**（一条 SQL 算所有）：
```sql
WITH msg AS (
    SELECT id, channel_id, seq FROM messages WHERE id = ANY($1::bigint[])
)
SELECT
    msg.id,
    COUNT(cm.user_id) AS total_members,
    COUNT(*) FILTER (WHERE cm.last_read_seq >= msg.seq) AS read_count,
    array_agg(cm.user_id) FILTER (WHERE cm.last_read_seq < msg.seq)
        OVER (PARTITION BY msg.id ORDER BY cm.user_id) AS unread_users
FROM msg
JOIN channel_members cm ON cm.channel_id = msg.channel_id
GROUP BY msg.id, msg.seq;
```

**约 80 行 Go**（含 SQL 调度 + 截断 + 校验）。

### 3.4 既有 endpoint 不变（确认）

以下都已实现，rewrite 时直接对齐即可：

| cses 旧 path | im 新 path |
|---|---|
| `/channel/create` | `POST /api/channels` (DM 用 `/api/channels/dm`) |
| `/channel/:id/change/displayName` 等 9 条 change/* | `PUT/PATCH /api/channels/:id` |
| `/channel/:id/{add,remove}/manger` | `POST/DELETE /api/channels/:id/managers/:user_id` |
| `/channel/:id/{add,remove,load}/postPinned` | `POST/DELETE/GET /api/channels/:id/pins/:message_id` |
| `/channel/:id/onlineStatus` | `GET /api/channels/online-status` |
| `/channel/:id/member/leave` | `POST /api/channels/:id/leave` |
| `/post/announcement/{save,read,delete,list,acceptList}` | im announcement.go 7 路由 |
| `/post_bookmark/{create,delete,load}` | `POST/DELETE/GET /api/favorites/:message_id` |
| `/posts/{createPosts,revoke,getReplies,urgentPost,...}` | im message/urgent/scheduled/quick_reply 各路由 |
| `/channels/view`（onChannelRead）| `POST /api/channels/:id/read` |

### 3.5 WS 协议（0 改动）

- V1 12 + M2 4 = 16 锁定不动
- `EventMsgUpdated` 已存在 (`gateway/types.go:43`)，决策 6 直接复用
- 砍掉 cses 老的 `post_read` WS 事件（im 后端不再发，Rust 客户端 handler 删除）

---

## 4. cses-client (Angular) 改动详单（Phase 2-4）

### 4.1 messageHttp.service.ts 收口（Phase 1 接近尾声同步）

| 改动 | 行 |
|---|---|
| baseUrl `/api/cses` → `/api` | `:36` |
| 字段名 `mattermostHttp` → `imGatewayHttp` | `:36` |
| `AuthData` 接口补 `imGatewayHttp?: string` 显式声明 | `core/services/auth/auth.service.ts:19` |
| 删 `isWrappedResponse` 分支，改成统一信封 `response.status === 'success' ? response.data : throw` | `:106-118` |

约 30 行净改动。

### 4.2 message.service.ts 37 处 rewrite（Phase 2-3）

按 §3.4 对照表批量改 path + body shape。每条一个 commit，commit message 形如：
```
refactor(message-v3): rewrite createPost from /posts/createPosts to im REST
```

**关键 5 处特殊处理**：

| 调用 | 改法 |
|---|---|
| `:341 templateReceived` | path 改 `/api/messages/:id/received`，body 砍 channelId（im 用 path id）|
| `:540 getPostsAfterFromSegment` | rewrite 成 `GET /api/messages/:id/after`（保留 UI 跳转功能） |
| `:712 /channels/load/increment` | **整段砍**（dead code，由 Rust ImSeqDataSource 接管） |
| `:218 /channel/member/snapshot` | rewrite 成 `GET /api/channels/:id/members` 全量 |
| `:418 pathMap[type]` | join → `POST /api/channels/:id/managers/:user_id`，leave → `DELETE` 同 path（顺手把 `manger` 拼写改回 `manager`）|

### 4.3 已读未读改造（Phase 3-4）

**Phase 3：channel-level 已读切换**

| 文件 | 改动 |
|---|---|
| `message.service.ts:196 onPostRead` | **整段废弃**（dead code，im 后端无对应端点）|
| `message.service.ts:205 onChannelRead` | path `/channels/view` → `/api/channels/:id/read`，body 改 |
| 6 处调用 onChannelRead | dialog-header / messageDialog ×2 / chat-search-layout / chat-content / chat-content-standalone |
| `chat-content-base.component.ts:374, 388-416` `onScrolling` 调 `inViewMsgRead` 整链 | **整段砍**（dead code）|

**Phase 4：read-stats UI 异步重构**

| 文件 | 改动 | 风险 |
|---|---|---|
| `message-status.component.ts:30-65 readResult` | `computed` 同步算法 → `signal<ReadStatsResult \| null>(null)` + `effect` 异步加载 + 批量合并请求（同一屏可见消息一次拉）| 中（UI 短暂空白）|
| `message.component.ts:380-413` 加急弹窗 | `OPEN_EXPEDITED_USERS` 事件处理前插入 `GET /api/messages/read-stats?ids=<msgId>` 异步调用，再开弹窗 | 高（弹窗速度从 0ms 变 ~150ms，需要加 loading 态）|
| `messageContainerShared.service.ts:504-527` 加急弹窗复制 | 同上同步改 | 高 |

### 4.4 模板已收到对账（Phase 2）

`message-template-content.component.ts:111`：
- path 改 `templateReceived({ postId, channelId })` → `templateReceived(postId)`（path 化）
- 保留 `applyReceivedLocally()` 作为 optimistic（点击立刻变）
- 等服务端 `msg_updated` WS 回声时，`MessageDialogServer` 已自动更新 props.template.userIds → 组件 OnPush 触发刷新 → optimistic 与服务端状态自然对账

约 5 行改动。

### 4.5 隐性 bug 修复（Phase 4 顺手）

| bug | 文件 | 修法 |
|---|---|---|
| **@mention 清理失效** | `messageWindowGlabal.service.ts:162` | `@CoreEventListener(POST_READ)` → 改成订阅 `read_sync` WS 事件（im 已有），按 channel 维度判断 mention 是否还在未读窗口 |
| **entryUnreadCount 失效** | `chat-content.component.ts:385-386` | 同上，或评估是否废弃此 UI 入口 |

### 4.6 readBits 残留清理（Phase 4 收尾）

按 audit 清单（删 12 / 改 15 / 保留 6）：

1. **先动 message-status**（独立组件，验证最快）
2. **再动 chatContentShared.unReadCheck**（删整个函数）
3. **再动加急 2 处**（同步修）
4. **再动 messageWindowGlabal mention 清理**
5. **最后删 type.d.ts / event.type.ts 字段声明**（TS 编译器帮你找出所有遗漏）

---

## 5. Tauri Rust 改动详单（Phase 3 顺手）

| 改动 | 文件 | 说明 |
|---|---|---|
| 删 `handle_post_read` | `src-tauri/src/websocket/im_handlers.rs:60, 2278-2374` | 整段约 100 行；im 后端不再发 `post_read` WS 事件 |
| 删 `imWs:post:read` IPC 事件订阅 | `src/pages/message-v3/service/ipcEventHandler.service.ts:126, 278` | -10 行 |
| `MessageEventDispatch.POST_READ` 5 处订阅 | 详见 audit | 4 处随 onPostRead 链路一起废；mention 清理那 1 处改 read_sync |
| `read_bits` Rust 字段透传 | `im_types.rs:190`、`im_handlers.rs:215, 559` | 标 `@deprecated` 保留，字段恒为 `null`，前端兼容 |
| 增量同步 ImSeqDataSource | 0 改动 | feature flag `im_seq_sync` 已默认开 |

---

## 6. Phase 划分 + 依赖图

```
                    ┌─────────────────────┐
                    │ Phase 1: im 后端     │ 1 day
                    │ - 全局响应包裹       │
                    │ - POST received     │
                    │ - GET read-stats    │
                    └────┬───────────┬─────┘
                         │           │
              ┌──────────┘           └────────────┐
              ▼                                   ▼
    ┌──────────────────┐              ┌────────────────────┐
    │ Phase 2: 模板已收到│ 0.5 day      │ Phase 3: 已读切换 │ 0.5 day
    │ - msg.svc path    │              │ - onChannelRead 6 处 │
    │ - optimistic 对账 │              │ - 砍 onPostRead    │
    └──────────────────┘              │ - 砍 inViewMsgRead │
                                       │ - Rust handle_post_read 删 │
                                       └─────────┬──────────┘
                                                 │
                                                 ▼
                                       ┌────────────────────┐
                                       │ Phase 4: read-stats UI│ 2 days
                                       │ - message-status 异步 │
                                       │ - 加急 2 处异步       │
                                       │ - mention 清理 read_sync │
                                       │ - 33 处 readBits 清理 │
                                       └────────────────────┘
```

并行机会：Phase 2 与 Phase 3 后期可并行（不同 component 不冲突）。

---

## 7. 验证清单

### 7.1 单元 / 集成（每个 Phase 末跑）

```bash
# im 后端
cd /Users/mac28/workspace/golangProject/im/server
go test -race ./internal/...
make verify-all   # 含 6 个 happy-path 集成测试

# cses-client
cd /Users/mac28/workspace/angular/temp/cses-client
yarn lint && yarn test
```

### 7.2 grep 验证（cses-shape 残留归零）

```bash
cd /Users/mac28/workspace/angular/temp/cses-client/src

# 应为 0
grep -rn "imHttp\.\(post\|get\|put\|delete\)\(['\"]/posts/" .
grep -rn "imHttp\.\(post\|get\|put\|delete\)\(['\"]/channel/" .
grep -rn "imHttp\.\(post\|get\|put\|delete\)\(['\"]/post/" .

# 应 ≥ 37
grep -rn "/api/channels\|/api/messages\|/api/announcements" .

# readBits 真实业务逻辑应为 0（字段透传声明可保留）
grep -rn "readBits\[" .  # 位图按位访问全部清掉
grep -rn "readBits ===" .  # 位图比较全部清掉
```

### 7.3 联调端到端

```bash
cd /Users/mac28/workspace/angular/temp/cses-client
yarn start  # = tauri:dev

# 用 17692704771/123456 登录，DevTools console 期望:
# 🔀 [MessageHttpService] apiFlavor: mattermost → im
# 🔄 [LOGIN] reconnectMainWs ... routedToIm: true

# Network 期望:
# 所有 imHttp.* 命中 192.168.6.41:30880/api/{channels,messages,announcements,...}
# 0 处 404 / 0 处 ImEndpointNotMappedError
# 所有响应包格式 {status: "success", data: ...}
```

### 7.4 性能基线（Phase 4 完）

```bash
cd /Users/mac28/workspace/golangProject/im/server
TARGET_VUS=300 scripts/apply-k6.sh
# 目标: action_ok ≥ 99% / send P95 ≤ 400ms
# 重点观察: GET /api/messages/read-stats P95 ≤ 100ms（影响 UI 体验）
```

### 7.5 烟囱测试

```bash
node scripts/v072-smoke.mjs
node scripts/ws-smoke-zhanglichao.mjs
node scripts/v070-smoke.mjs
# 三件套必须 7/7 / 全绿
```

---

## 8. 回滚策略

按 [decisions/no-traffic-rollback](../wiki/decisions/no-traffic-rollback.md) **不留 feature flag 回 Mattermost**。"回滚" = git revert + 重发布。

| 失败场景 | 回滚动作 |
|---|---|
| im 后端单 endpoint 报错 | 在 cses Java 端临时把 `imGatewayEnabled` 关掉，apiFlavor 回 mattermost（cses-client 自动切回）|
| cses-client UI 大面积闪烁 | git revert 客户端 PR，发布上一版客户端 |
| WS 协议不兼容 | 排查 `EventMsgUpdated` payload 是否丢字段；不允许临时改 V1 16 锁定 |

---

## 9. 已知风险与缓解

| # | 风险 | 概率 | 缓解 |
|---|---|---|---|
| 1 | message-status 切异步导致 N+1 请求爆炸 | 高 | Phase 4 强制批量端点 + 同屏可见消息 ids 合并 + RxJS bufferTime 200ms |
| 2 | 加急弹窗从 0ms 变 ~150ms 体感差 | 中 | 加 loading 态 + 预拉（消息进入可见区时预查 read-stats 缓存）|
| 3 | 6 个集成测试响应断言全红 | 必现 | Phase 1 同步改测试断言 |
| 4 | 隐性 bug 修复改动量超估 | 中 | 把 mention 清理 / entryUnreadCount 列为 Phase 4 选做项；如超时挪到 v0.7.4 |
| 5 | snapshot 历史精度问题（已离群成员未读不算）| 低 | 已与用户确认延后处理 |
| 6 | Phase 1 全局响应包裹影响 22 个 handler 文件 | 中 | 用 sed/AST 批量替换 + diff review；建议派 cses-client-dev agent 跑 review |

---

## 10. 时间表

| Day | Phase | 产出 | tag |
|---|---|---|---|
| Day 1 | Phase 1 im 后端 | 全局响应包裹 + 2 个新 endpoint | im server `v0.7.3-rc1` |
| Day 2 上午 | Phase 2 模板已收到 | client 切 path + msg_updated 对账 | client `v0.7.3-rc1` |
| Day 2 下午 | Phase 3 已读切换 | onChannelRead 6 处 + 砍 dead code + Rust handler 删 | client `v0.7.3-rc2` |
| Day 3-4 | Phase 4 read-stats UI 重构 | message-status 异步 + 加急 2 处 + mention 修 + readBits 清理 | client `v0.7.3-final` |
| Day 4 下午 | 验证 | grep 归零 + 联调 + smoke + k6 性能基线 | im `v0.7.3-client-verified` |

**收尾里程碑**：tag `v0.7.3-csesapi-decom-ready`，进入 [milestones/M6-mattermost-decom](../wiki/milestones/M6-mattermost-decom.md) 观察期。

---

## 11. 文档维护契约

切换过程中每动一处 cses-client 代码，按 cses-client 项目根 `CLAUDE.md` + `popup-window-branch-creator` skill：

- 改 `src/pages/message-v3/` 下文件 → 同步更新最近的 `claude.md`
- 跨组件影响（事件流 / 状态机 / 接口契约 / 存储结构）→ 同步更新 `docs/` 下对应 markdown
- im 项目 wiki 新增/修改的对账或合成 → log.md 追条目

切换完成后，本方案文档移到 `docs/archive/` 并在 `wiki/milestones/M6-mattermost-decom.md` 加 backlink。

---

## 12. 关联文档

- [docs/GOAL.md](GOAL.md) §3 不在范围 / §4 硬约束
- [docs/HTTP_WS_MAP.md](HTTP_WS_MAP.md) — HTTP ↔ WS 推送对应矩阵
- [docs/ARCHITECTURE.md](ARCHITECTURE.md) — 文件地图
- [server/docs/BACKEND.md](../server/docs/BACKEND.md) — M1–M6 详细契约
- [wiki/comparisons/csesapi-vs-im-coverage.md](../wiki/comparisons/csesapi-vs-im-coverage.md) — 125 vs 80 路由对账
- [wiki/syntheses/min-cost-mattermost-cutover.md](../wiki/syntheses/min-cost-mattermost-cutover.md) — 最低成本路线 v2 (本方案是 v3 升级)
- [wiki/concepts/seq-cursor.md](../wiki/concepts/seq-cursor.md) — 替代 bitmap 的同步机制
- [wiki/concepts/api-flavor-switch.md](../wiki/concepts/api-flavor-switch.md) — 双栈切换信号
- [wiki/decisions/no-traffic-rollback.md](../wiki/decisions/no-traffic-rollback.md) — 不留回切

---

**Owner**：im 项目 + cses-client 项目联合
**Reviewer 候选**：用户本人 + cses-client-dev agent（落地前过一次）
**Status**：Phase 1 ✅ DONE | Phase 2-4 ⏳ cses-client 团队接手

---

## 13. Phase 1 完成证据（2026-05-01）

| 验证项 | 命令 | 结果 |
|---|---|---|
| build | `go build ./...` | ✅ |
| vet | `go vet ./...` | ✅ 0 告警 |
| 单元 + race | `go test -race -short ./...` | ✅ 全绿 |
| 集成 + race | `go test -race -tags integration ./...` | ✅ 全绿 (~55s, 含 7 个 m4 happy-path + 3 read-stats + 2 template received) |
| envelope 单测覆盖 | `go tool cover -func ... \| grep response_envelope` | ✅ 全 10 函数 100% |
| golangci-lint (新代码) | `golangci-lint run ./internal/http/...` | ✅ 0 告警 |

新增/修改文件（10 个新 + 4 个改）：
```
A  server/internal/http/response_envelope.go              (152 行)
A  server/internal/http/response_envelope_test.go         (215 行, 100% cov)
A  server/internal/http/message_template.go               (66 行)
A  server/internal/http/read_stats.go                     (90 行)
A  server/internal/repo/message_props.go                  (109 行)
A  server/internal/repo/read_stats.go                     (97 行)
A  server/internal/service/message_template.go            (66 行)
A  server/internal/service/read_stats.go                  (43 行)
A  server/tests/integration/m4_template_received_test.go  (110 行)
A  server/tests/integration/m4_read_stats_test.go         (143 行)
M  server/internal/http/message.go                        (+12 行 register call)
M  server/internal/http/router.go                         (+1 行 Use)
M  server/internal/repo/errors.go                         (+5 行 sentinel)
M  server/internal/repo/message.go                        (+9 行 interface)
```
合计：**+1187 行 / -2 行**，0 行业务回归风险。


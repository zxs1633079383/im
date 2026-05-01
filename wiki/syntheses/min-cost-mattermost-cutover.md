---
type: synthesis
title: 最低成本切换 Mattermost csesapi 路线图（v2）
status: stable
last_verified: 2026-04-30
sources:
  - comparisons/csesapi-vs-im-coverage
  - docs/HTTP_WS_MAP.md
  - docs/GOAL.md
  - SESSION.md (2026-04-27 v0.7.2 联调)
  - client/src/pages/message-v3/service/message.service.ts (52 调用面)
  - server/internal/http/*.go (~80 路由)
related:
  - comparisons/csesapi-vs-im-coverage
  - concepts/seq-cursor
  - concepts/api-flavor-switch
  - entities/im-api-adapter
  - entities/im-seq-data-source
  - milestones/M6-mattermost-decom
confidence: high
---

# 最低成本 Mattermost 切换路线图（v2）

> v1 已被 contradiction 标记 superseded（见 [[log]] 2026-04-30）。
> v2 核心修正：**直接 rewrite 客户端调用面，不加翻译表**；**bitmap 协议整族砍掉，由 [[entities/im-seq-data-source]] 接管**。

---

## TL;DR

| 维度 | v1（错） | v2（对） |
|---|---|---|
| 客户端策略 | route-table 映射 cses-shape → im REST | **直接 rewrite** message.service.ts 调用面 |
| bitmap 调用 | mapper 翻译成 `after?seq=` | **删除前端 HTTP 调用，由 Rust ImSeqDataSource + WS 接管** |
| 翻译层 | ImApiAdapter 内 path map + DTO mapper | **0 翻译层，cses-shape 在前端代码里彻底消失** |
| im 后端改动 | 0 endpoint + 0~3 字段 | **0 endpoint + 0~1 字段** |
| 客户端净改动 | ~150 行 mapper | ~37 处 rewrite，**净代码减少**（删除多于新增）|

---

## 1. 三个核心决策

### 1.1 直接 rewrite，不加翻译表

**为什么不要翻译表**：

| 路线 | 长期负担 |
|---|---|
| 路由翻译层 | cses-shape 永远活在客户端代码里；新人看到 `/posts/createPosts` 还要去 mapper 查实际打哪儿；技术债复利 |
| **直接 rewrite** | cses-shape 在 git diff 里一次性消失；新人看到 `/api/channels/:id/messages` 就是真路径；与 [[decisions/strangler-fig-collapsed]] 哲学一致 |

**改造单元**：一个 message.service.ts 方法对应一次 PR-grain commit，commit message 形如：
```
refactor(message-v3): rewrite createPost from /posts/createPosts to im REST

cses-shape /posts/createPosts → im /api/channels/:id/messages
body: {channelId, message, ...} → {content, sender_id, ...}
组件层零感知（返回值结构经 service 内部归一）
```

### 1.2 bitmap 整族砍

**清单**（[[comparisons/csesapi-vs-im-coverage]] §2.2 13 条）：客户端**不再有任何**这些 HTTP 调用。增量同步由：

```
Rust ImSeqDataSource
  ├─ 启动 → POST /api/sync { cursors: {ch_id → last_seq} } 拉 delta
  ├─ WS push_msg → 写入本地 store + emit 'im/message'
  └─ 断网重连 → POST /api/sync 续拉

Angular 业务组件
  └─ 订阅 IPC 'im/message' / 'msg_updated' / 'msg_deleted' 事件
     → 0 主动 HTTP 拉取
```

**snapshot 协议同步废弃**：`/channel/{id}/member/snapshot` 不再需要——客户端已确认 userSnapshot 业务实际只要 `userId + companyId(=teamId)` 两个字段，从 [[concepts/cookie-id-native]] 的 `Redis HASH "User"` 解析就够。

### 1.3 im 后端 0 改动

既有 ~80 条 RESTful 路由 + WS 16 事件覆盖：
- 客户端可触达面 95%+
- 缺口字段 0~1 个（`quick_reply_id`，已支持）
- 缺口 endpoint **0 个**

→ 本期切换的工程量**全部在客户端（Angular + Tauri Rust）**，im 后端只需保持 v0.7.2 镜像不动。

---

## 2. 落到文件的精确改动面

### 2.1 Angular 客户端

| 文件 | 改动 | 量级 |
|---|---|---|
| `src/pages/message-v3/service/message.service.ts` | rewrite 37 条 imHttp 调用 → im RESTful path + body | ~37 处方法体 |
| `src/pages/message-v3/service/messageHttp.service.ts:36` | baseUrl `\`http://${imGatewayHttp}/api/cses\`` → `\`http://${imGatewayHttp}/api\`` | 1 行 |
| `src/pages/message-v3/service/messageHttp.service.ts` | 去掉 `mattermostHttp` 字段引用 | ~5 行死代码清理 |
| 子 service（channelConfig / messageSend / dialogDataSource 等） | 调用面已收口在 message.service，**不动** | 0 |
| component / template | 0 触达 | 0 |

**净代码 = -约 50 行**（rewrite 比原调用更短，加上死代码清理）。

### 2.2 Tauri Rust

| 文件 | 改动 | 量级 |
|---|---|---|
| `src-tauri/src/data_source/im_seq_data_source.rs` | 确认 feature flag `im_seq_sync` 在 prod 默认开 | 0~1 行 cfg 改动 |
| `src-tauri/src/websocket/channel_processor.rs` | 砍掉残留 bitmap segment 调用路径（如有）| 待 audit |

### 2.3 im 后端 (Go)

**0 改动**。继续用 v0.7.2-no-mattermost 镜像。

---

## 3. 前端 review TODO（用户拍板）

| # | 项 | 触达 | 期望结论 |
|---|---|---|---|
| 1 | `message.service.ts:712 imHttp.post('/channels/load/increment', { channelId, timestamp })` | 哪个 component / 子 service 触发？是历史 polyfill 还是 active 调用 | polyfill → 砍；active → 改成订阅 `im/message` IPC 事件 |
| 2 | `message.service.ts:218 imHttp.post('/channel/member/snapshot')` | 是否还有调用方依赖 snapshot 分页协议 | 全部确认只用 `userId + companyId` → 替换为 `GET /api/channels/:id/members` 全量 + 客户端缓存 |
| 3 | `message.service.ts:540 imHttp.post('/posts/getPostsAfterFromSegment', { postId })` | 是否真的从 component 触发；还是只在 IMDataSource 启动恢复时调 | IMDataSource 用 → 已被 ImSeqDataSource 替代，砍前端调用；component 用 → rewrite 成 `GET /api/messages/:id/after` |
| 4 | `pathMap[type]` (line 418) | 多类型聚合路径，需要枚举所有 type 值确认对应 im REST | 同时 rewrite |
| 5 | `imHttp.post('/post/templateReceived')` (line 341) | 此调用应该走 cses Java 而不是 im（GOAL §3 排除）| 改回 `this.http` |

---

## 4. 验证清单（切换完成的判定）

```bash
# 客户端层
cd /Users/mac28/workspace/angular/temp/cses-client    # ⭐ 注意 temp/ 前缀
grep -rn "imHttp\.\(post\|get\|put\|delete\)\(['\"]/posts" src/  # 应为 0（cses-shape 全死）
grep -rn "imHttp\.\(post\|get\|put\|delete\)\(['\"]/channel/" src/  # 应为 0
grep -rn "/api/channels\|/api/messages\|/api/announcements" src/    # 应 ≥ 37（im REST 已落地）
yarn lint && yarn test

# 后端层（不动）
cd /Users/mac28/workspace/golangProject/im/server
git status   # 应干净
make verify-all   # 仍绿

# E2E 联调
yarn start  # cses-client tauri:dev
# DevTools console: apiFlavor: im → routedToIm: true
# Network: 所有 imHttp.* 命中 192.168.6.41:30880/api/{channels,messages,announcements,...}
# 0 处 404 / ImEndpointNotMappedError
```

---

## 5. 风险与回滚

| 风险 | 概率 | 缓解 |
|---|---|---|
| rewrite 漏改某条调用 → 404 | 中 | 每条 PR-grain commit 自带 grep 验证；联调期 console 直观看到 404 |
| im 后端缺字段（如 `quick_reply_id`）| 低 | 已确认支持；运行期发现可加 |
| Rust ImSeqDataSource 回放断点续传 bug | 中 | 现已稳定（v0.7.2 联调跑通）；feature flag 兜底切回 IMDataSource |
| **不留回切**（[[decisions/no-traffic-rollback]]） | — | 与本路线图一致；切坏只能向前修 |

---

## 6. 时序与里程碑

| 阶段 | 工作 | tag |
|---|---|---|
| 当前 | Phase 1 im 后端落地（commit `441ba37`/`66c2a67`/`ee32f7f`/`65f0763`）+ cses-client 工作目录切到 `/Users/mac28/workspace/angular/temp/cses-client` branch `tauri-new-im` HEAD `836b899ab`（旧 `im-backend-switch`/`7c8a0c972` 仅作历史 commit 引用）| `v0.7.2-no-mattermost` + im 后端 main `c9995bf` |
| 下一步 | 客户端 37 条 rewrite + 5 条 review TODO | `v0.7.3-client-direct-rewrite` |
| 之后 | 全量 e2e + k6 → 性能基线 | `v0.7.4-perf-baseline` |
| 收尾 | [[milestones/M6-mattermost-decom]] | `v1.0.0-csesapi-decom` |

---

## 7. 历史决定参照

- [[decisions/no-traffic-rollback]] —— 本路线图与之一致：rewrite 一次到位
- [[decisions/strangler-fig-collapsed]] —— 后端已收官，前端这一波就是对称收官
- [[concepts/seq-cursor]] —— bitmap 砍的理论基础
- [[concepts/api-flavor-switch]] —— v2 仍保留 apiFlavor 字段，但 `apiFlavor=im` 后客户端代码里**不再存在 cses-shape**，只剩 RESTful path

---

## 8. v3 升级 → 工程可执行手册

本 synthesis 是路线图（why + what）。v3 升级把已读未读机制（决策 1c：seq 游标 + 批量 read-stats，不建新表）+ 模板已收到（决策 2a + 决策 3b 复用 msg_updated）+ 隐性 bug 修复（mention 清理 / entryUnreadCount 失效）全部纳入，落到工程可执行手册：

→ **[docs/CSES_CLIENT_CUTOVER.md](../../docs/CSES_CLIENT_CUTOVER.md)**（4 工作日 phase 划分 / im 后端改动详单 / cses-client 37 条 rewrite 表 / Tauri Rust 删除清单 / 验证 grep 命令 / k6 性能基线 / 回滚策略 / 时间表）

工程团队按 docs 文档开干即可；本 synthesis 保留作为决策依据归档。

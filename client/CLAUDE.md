# CLAUDE.md — `client/` 模块（im 仓库内桌面客户端原型）

> 模块级指令，优先级**低于**项目根 `/Users/mac28/workspace/golangProject/im/CLAUDE.md`、**高于**默认行为。
> 任何对 `client/**` 的改动必须先读这份；再回溯到项目根 §2「写前端规则」与 `docs/harness/` 对应条目。

---

## 0. 模块定位

- **是什么**：im 仓库内自带的桌面客户端原型 — Angular 21 业务层 + Tauri 2 Rust shell（IPC bridge + 本地持久化 + 单实例 + 增量同步 seq 游标）。
- **不是什么**：本目录**不是** cses-client cutover 项目的工作目录。
  - cutover 工作目录在仓库外：`/Users/mac28/workspace/angular/temp/cses-client` + 分支 `feat/im-reactor-2-offine`（HEAD `cfd9be31d`，im 仓库根 `CLAUDE.md §0.1` 拍板锁定）。
  - 本目录是 im 后端配套的**全新原型客户端**，与外部 cses-client 独立演进，**禁止互相 cherry-pick**。
- **当前状态**：Angular CLI 21.2.6 scaffold（`ng serve` 起 `http://localhost:4200`），Tauri 2 + `tauri-plugin-sql`，`src-tauri/src/lib.rs` 仅 17 行 setup（plugin_sql + plugin_log）。属于**冷启动期**，下文规范是**前置约束**，避免一开手就违 harness。
- **业务契约源**：所有 REST / WS 字段定义以 `server/internal/http/` + `server/internal/gateway/types.go` 为唯一权威；客户端只能映射，不得自创。

---

## 1. 影响范围（上下游）

| 方向 | 依赖 | 备注 |
|------|------|------|
| 上游 REST | `server/internal/http/` 84 路由（C008） | 通过 `ImApiAdapter` 调用，不直接拼 URL |
| 上游 WS | `server/internal/gateway/types.go` 22 种 `WSMessageType`（C005） | 经 `ws-normalizer.ts` 翻译到 `imWs:*` 内部事件 |
| 上游契约 | `docs/CSES_CLIENT_内部对接契约.md`、`docs/HTTP_WS_MAP.md`、`docs/CSES_CLIENT_对接All-case实践.md` | 字段名 / 类型 / 路由表的唯一定义处 |
| 下游 | macOS / Windows / Linux desktop（Tauri bundler） | Tauri 2 build target，由 `src-tauri/tauri.conf.json` 描述 |
| 持久化 | SQLite via `@tauri-apps/plugin-sql` | 本地缓存消息 / channel / seq 游标 |
| 跨仓镜像 | C012 镜像见 cses-client `docs/harness/C005-id-type-string-client-downstream.md`；C016 镜像见 cses-client `handlers_v2/message.rs` | 本目录新写代码**直接按 string id + updated_at 单调闸门**落地，避免重蹈外部 client 的迁移代价 |

---

## 2. 功能模块清单

### 2.1 Angular（`src/app/`）

| 路径 | 职责 |
|------|------|
| `core/im/` | IM 协议核心：`ImApiAdapter`（REST 出口）、WS 连接管理、ws-normalizer 翻译表 |
| `core/api/` | HTTP client 抽象 + envelope 解包 + 错误归一 |
| `core/config/` | `api.config.ts` 等环境配置（包含 `apiFlavor` 切换逻辑，见根 §2） |
| `core/ws/` | WebSocket 连接 / 心跳 / sync 重放，22 种 type dispatch |
| `core/db/` | SQLite 本地缓存（messages / channels / channel_members / seq 游标） |
| `core/auth/` | token / userId / companyId header（C010） |
| `core/channels/` `core/messages/` `core/friends/` `core/favorites/` `core/files/` `core/search/` `core/i18n/` `core/theme/` | 各业务域 service + 类型 |
| `features/` | 路由级页面：`channel-list` / `channel-settings` / `chat` / `contacts` / `create-group` / `favorites` / `home` / `login` / `main-layout` / `profile` / `register` / `search` / `settings` |
| `shared/` | 跨 feature 复用组件 / pipe / directive |

### 2.2 Tauri Rust（`src-tauri/src/`）

| 路径 | 职责 |
|------|------|
| `main.rs` | 入口，调 `lib::run()` |
| `lib.rs` | Tauri Builder setup：注册 `tauri_plugin_sql` + `tauri_plugin_log`；后续 `#[tauri::command]` 命令注册点 |
| `features/im/`（待建） | IM handler：`handle_msg_updated` / `handle_msg_deleted` / `handle_read_sync` 等（参考 cses-client `handlers_v2/`，但必须按 C016 单调闸门落地） |
| `features/persistence/`（待建） | `im_seq_data_source.rs` — channel seq 游标持久化（根 §2 强制） |
| `state.rs`（待建） | `AppState`：HTTP/WS client / SQLite pool 单例 |

> Tauri 命令注册示例（首次添加时按此形态）：
> ```rust
> tauri::Builder::default()
>   .manage(AppState::new())
>   .invoke_handler(tauri::generate_handler![cmd_send_message, cmd_read_channel])
>   .run(...)
> ```

---

## 3. SOP（标准工作流）

1. **确认契约**：开始前读 `docs/HTTP_WS_MAP.md` 找对应 endpoint / WSMessageType；如缺则去 `server/internal/http/`、`server/internal/gateway/types.go` 取地。
2. **Angular 业务侧**：
   - 新接口 → 在 `core/im/im-api.adapter.ts` 添加方法 → 在 `core/<domain>/` service 调用 → component 注入 service 用，不直接 `HttpClient.get(...)`。
   - 新 WS 事件 → 必须先确认 type 在 22 种锁定集（C005），否则走 V2 RFC。
3. **Tauri Rust 侧**：
   - 新命令 → `#[tauri::command] async fn xxx(state: tauri::State<AppState>, ...) -> Result<...>` → 在 `lib.rs invoke_handler!` 注册 → 前端 `import { invoke } from '@tauri-apps/api/core'` 封装一层。
   - 新表 → 通过 `tauri-plugin-sql` migration 文件落地；id 列必须 TEXT（C012）。
4. **本地验证**：
   - Angular：`npm run lint && npm test`（Vitest，根目录 `package.json scripts.test = "ng test"`）。
   - Rust：`cd src-tauri && cargo check && cargo test`。
   - 桌面端 dev：`npm run tauri dev`（首次需 `npm i -g @tauri-apps/cli` 或用 `npx`）。
5. **联调**：im 后端默认起在 `:8080`；WS 走 `/ws?token=...&companyId=...`（C010）。

---

## 4. Pre-commit 自检清单

每次 `git commit` 前必跑（任一红 → 不提交）：

- [ ] `cd client && npm run lint`
- [ ] `cd client && npm test`（Vitest 全绿）
- [ ] `cd client && grep -rEn "HttpClient\.(get|post|put|patch|delete)\\(['\"]/?api/" src/app/ | grep -v 'im-api.adapter'` → **必须 0 条**（拼 URL 必走 ImApiAdapter）
- [ ] `cd client/src-tauri && cargo check && cargo test`
- [ ] `cd client && grep -rE "(case|match)\\s+['\"]([a-z_]+)['\"]" src/app/core/ws/ src-tauri/src/` 取出的字面量 type **必须**全在 22 种锁定集（C005）
- [ ] `cd client && grep -rEn ":\\s*number" src/app/core/im/types/ src/app/core/messages/ src/app/core/channels/` 取出的 `id` / `channelId` / `messageId` 等**必须 0 条**（C012：id 用 string）
- [ ] msg_updated / msg_deleted handler 路径 grep：`grep -rE 'handle_msg_updated|handle_msg_deleted' src-tauri/src/` → 函数体必须含 `updated_at` 单调判定（C016）
- [ ] WS handler 任何 `upsert` 调用前必须有「旧 echo drop」分支（C016 §3.2 A）

---

## 5. Commit 规范

沿用项目根 §3 Conventional Commits（中文 body）；**scope 必须带 `client/` 前缀子模块**：

| Scope 示例 | 适用 |
|---|---|
| `feat(client/im-ws): xxx` | WS 连接 / dispatch / normalizer |
| `feat(client/im-api): xxx` | ImApiAdapter / REST 调用 |
| `feat(client/chat-ui): xxx` | features/chat 视图 |
| `feat(client/tauri-bridge): xxx` | `#[tauri::command]` + invoke_handler |
| `feat(client/tauri-db): xxx` | SQLite migration / repo |
| `fix(client/seq-cursor): xxx` | `im_seq_data_source.rs` 持久化 bug |
| `refactor(client/id-string): xxx` | C012 客户端镜像变更 |
| `test(client/handlers): xxx` | Rust handler 单测 / Vitest spec |

**禁止**：
- ❌ scope 用 `client` 不带子模块（颗粒度不够）
- ❌ 一次 commit 跨 Angular + Rust 两栈（拆开）
- ❌ body 写英文 / 写"AI 生成"

---

## 6. 约束规范（硬约束清单）

### 6.1 Angular 侧

1. **新接口必走 `ImApiAdapter`**（项目根 §2）。`HttpClient.get('/api/...')` 直接出现在 component / 普通 service 里 = 违规；adapter 统一处理 envelope 解包（C007）+ companyId header（C010）+ 错误归一。
2. **`apiFlavor` 切换逻辑只在 `core/config/api.config.ts`**；其他地方读不改写。
3. **id 字段类型 string**（C012 镜像）：`ImMessage.id`、`ImChannel.id`、`ImAnnouncement.id` 等所有 entity id `string` 不用 `number`；`number → string` 不允许 fallback。
4. **WS 事件类型限定 22 种**（C005）：`ws-normalizer.ts` switch case 字面量必须与 server `types.go` 1:1 对齐；未知 type 默认 drop（不要 console.error 也不要重试）。
5. **不要在组件里直接监听 raw WS frame**：所有 WS 消费经 `core/ws/` 内的 `imWs:*` 事件总线。

### 6.2 Tauri Rust 侧

1. **命令必须 `#[tauri::command] async`**（项目根 §2）；同步命令会阻塞 webview 主线程。
2. **`AppState` 单例**：HTTP client / WS client / SQLite pool 在 `AppState::new()` 构造一次，命令通过 `tauri::State<AppState>` 注入。**禁止**在 command 里 `reqwest::Client::new()` 这种每次新建。
3. **seq 游标持久化**走 `im_seq_data_source.rs`（项目根 §2）；不要散在各个 handler 自己 `tauri-plugin-sql` 直写。
4. **`handle_msg_updated` / `handle_msg_deleted` 必须 `updated_at` 单调判定**（C016 §3.2 A）：本地 `local.updated_at >= new.updated_at` → drop echo，禁 raw `upsert`；SQL 用 `ON CONFLICT(id) DO UPDATE WHERE EXCLUDED.updated_at > messages.updated_at`。
5. **绝对禁止 JSON 数组列 RMW**（C016 §2.1）：累计 / 队列 / 列表用 normalized 表 + 联合 PK；append = 单 INSERT，清理 = 单 DELETE WHERE。
6. **`serde_json::to_string(整条 wire)` 塞单列**（C016 §2.2）= 违规；协议字段拆独立列。
7. **id 列 TEXT，不用 INTEGER**（C012）；migration 文件里 `id TEXT PRIMARY KEY` 强制。

### 6.3 Tauri-Angular IPC

1. 前端 invoke 必须封装到 `core/tauri/` 的 typed wrapper，不允许 component 直接 `import { invoke } from '@tauri-apps/api/core'`。
2. invoke 参数 / 返回类型与 Rust `#[tauri::command]` 签名必须 1:1（用 TS interface 对应 Rust struct）。

---

## 7. 对应 Harness 映射

| Harness | 触发场景 | 客户端验证手段 |
|---|---|---|
| **C005**（WS 22 种锁定） | 改 `core/ws/` dispatch / `ws-normalizer.ts` / Rust `handlers_v2` match arm | `grep -rE "case '[a-z_]+'" src/app/core/ws/` 取出的字面量 ⊂ 22 种集合 |
| **C012**（id 全链 string） | 新建 entity 类型 / 新 SQLite 表 / 新 Tauri command 参数 | `grep -rE ":\s*number" src/app/core/<domain>/types/` 在 id/channelId/messageId 字段必须 0；Rust `pub id: i64` 必须 0 |
| **C016**（msg_update 单闸门） | `handle_msg_updated` / `handle_msg_deleted` / `handle_read_sync` / 任何 message_repo `upsert` | grep `handle_msg_updated` 函数体含 `updated_at` 比较；SQL upsert 含 `EXCLUDED.updated_at > ` 条件 |

**引用规范**（沿用根 §8.5）：会话引用 `harness/C016 §3.2 A`；PR body 引用 `docs/harness/C016-msg-update-single-gate-seq-design.md`。

---

## 8. Update / Insert 规则（新增模式）

### 8.1 新增 endpoint 调用

```
1. 确认 endpoint 已在 server/internal/http/ 注册且有集成测试（C008）
2. docs/HTTP_WS_MAP.md 找到 path + req/resp schema
3. core/im/im-api.adapter.ts 添加方法（id 字段 string，C012）
4. core/<domain>/types/ 添加 req/resp interface
5. core/<domain>/<domain>.service.ts 业务封装
6. component 注入 service 调用
7. 写 Vitest spec：mock adapter 验证调用入参 / 错误分支
```

### 8.2 新增 WSMessageType

```
1. 先确认是否已在 22 种锁定集（C005 §2）
   ├─ 是 → 直接走步骤 3
   └─ 否 → 必须先经后端 V2 RFC 拍板（docs/RFC/ws-v2.md），客户端不允许"先行实现"
2. RFC 通过 → server types.go 加 const + payload struct
3. client/src/app/core/ws/ws-normalizer.ts 加翻译条目（"new_type" → "imWs:xxx"）
4. 业务侧 service 订阅 `imWs:xxx` 事件
5. Rust 侧：src-tauri/src/features/im/handlers_v2/ 加 handle_<new_type> 函数
6. lib.rs（或 dispatch 表）注册 match arm
7. 单测：Rust handler test + Angular service spec
```

### 8.3 新增 Tauri command

```
1. src-tauri/src/features/<domain>/commands.rs 写 #[tauri::command] async fn
   - 参数：state: tauri::State<AppState>, 业务入参（typed struct）
   - 返回：Result<RespStruct, AppError>
2. lib.rs invoke_handler! 数组追加注册
3. client/src/app/core/tauri/<domain>.bridge.ts 加 typed wrapper：
   export async function cmdXxx(args: XxxReq): Promise<XxxResp> {
     return invoke<XxxResp>('cmd_xxx', args);
   }
4. service / component 调用 bridge，不直接 invoke
5. 单测：Rust cargo test + 前端 mock @tauri-apps/api 测 bridge
```

### 8.4 新增本地 SQLite 表

```
1. src-tauri/migrations/<N>_<slug>.sql：id 列 TEXT PRIMARY KEY（C012）
2. 不在 channel_member 加 RMW 计数列；累计字段建独立 normalized 表（C016 §3.2 C）
3. 关联消息表的 updated_at 字段 BIGINT NOT NULL（msec），所有 upsert 必须基于该列单调（C016）
4. tauri-plugin-sql 配置 migration 序号
5. repo_v2 层封装 + 单测
```

---

## 9. 文档关联

| 文档 | 用途 |
|------|------|
| `docs/CSES_CLIENT_内部对接契约.md` | REST / WS 字段定义、错误码、envelope（**唯一权威**） |
| `docs/HTTP_WS_MAP.md` | 84 路由 + 22 WSMessageType 一览表 |
| `docs/CSES_CLIENT_对接All-case实践.md` | 业务对接实战 case（典型场景代码） |
| `docs/harness/C005-ws-event-types-locked.md` | WS 22 种锁定 + V2 RFC 触发条件 |
| `docs/harness/C012-id-type-string-migration.md` | id 全链 string；本目录新写代码直接遵守，避免追溯成本 |
| `docs/harness/C016-msg-update-single-gate-seq-design.md` | msg_update 单闸门设计；客户端 `handle_msg_updated` 必装 |
| `server/internal/gateway/types.go` | WSMessageType 常量真源 |
| `server/internal/http/` | 84 路由真源 |
| 项目根 `CLAUDE.md §2` | 「写前端规则」原文 |
| 项目根 `CLAUDE.md §0.1` | cses-client 工作目录锁定（外部，不混本目录） |

---

**最后更新**：2026-05-17（初版）
**下次触发更新**：① 任何 harness 新增 / 弃用 ② `src-tauri/src/lib.rs` 从 17 行扩展到 features/ 出现 ③ `apiFlavor` 切换策略变化 ④ 双栈 build pipeline 接入 CI。

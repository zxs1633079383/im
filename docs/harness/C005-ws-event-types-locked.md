# C005 — WS 事件类型锁定 V1 12 + M1 2 + M2 4 + v0.7 4 = **22**；新增必须升 V2 + 前后端同步签发契约

```yaml
---
id: C005
title: WSMessageType 锁定 22 种（V1 12 + M1 2 + M2 4 + v0.7 4），新增 / 字段变更必须走 V2 升级流程
status: active
created: 2026-05-07
last_recurred: 2026-04-30
recurrence_count: 2
source_logs:
  - logs/2026-04-22.json#L65
  - logs/2026-04-30.json#L40
applies_to:
  - server/internal/gateway/types.go
  - server/internal/gateway/cross_pod_push.go
  - server/internal/gateway/push_consumer.go
  - server/internal/gateway/ws_handler.go
  - client/src/app/core/services/ws-normalizer.ts
  - src-tauri/src/websocket/im_handlers.rs
inline_target: server/docs/BACKEND.md#§十一  # WS 协议契约节（待补）
---
```

## 1. 触发场景（Trigger）

任何会动 WS 协议表面的代码：

- `server/internal/gateway/types.go` 的 `WSMessageType` 常量定义
- `server/internal/gateway/cross_pod_push.go` 投递 envelope 时使用的 `MsgType` 字段
- 任何 service / cmd 调用 `CrossPodPush` 时传入新的 `MsgType` 字符串
- 客户端 `ws-normalizer.ts` 的 9 种 `imWs:*` 翻译表
- Rust `im_handlers.rs` 的 dispatch match arm
- 关键词 grep：`WSMessageType` / `TypePush` / `TypeMsg` / `MsgType:` / 字符串字面量 `"push_msg"` / `"msg_updated"` / 等

## 2. 锁定的 22 种事件（V1 12 + M1 2 + M2 4 + v0.7 4）

> **2026-05-07 用户拍板（选项 A）**：把原"V1+M2=16"措辞升级为"V1+M1+M2+v0.7=**22**"，这是最准确的事实陈述（与 `types.go` 实测一致）。新增任何 type 必须走 V2 RFC。
>
> 项目根 `CLAUDE.md §1.6` 仍写"V1 12 + M2 4 = 16 锁定"措辞 → **待修正**，下次 governance commit 同步。

### V1（12 种，v0.1 GA）

| Type | 方向 | 用途 |
|---|---|---|
| `ping` | client→server | 客户端心跳（15s 间隔） |
| `send` | client→server | 客户端发消息 |
| `push_ack` | client→server | 确认收到 push_msg |
| `sync` | client→server | 重连后请求增量同步 |
| `pong` | server→client | 服务端心跳响应 + 携带 channel_seqs diff |
| `push_msg` | server→client | 服务端推送新消息 |
| `send_ack` | server→client | 服务端确认 send 入库成功 |
| `sync_resp` | server→client | sync 请求的响应 |
| `read_sync` | server→client | 同用户在另一设备标已读 |
| `friend_event` | server→client | 好友请求 / 接受 / 拒绝 |
| `channel_event` | server→client | 用户被拉入新 channel |
| `msg_updated` | server→client | 消息编辑（实际于 M1 加入但占 V1 槽位） |

### M1 增量（2 种，与 V1 同发布周期）

| Type | 方向 | 用途 |
|---|---|---|
| `msg_deleted` | server→client | 消息撤回 / 软删除 |
| （`msg_updated` 列入 V1 表） | — | — |

> 注：`msg_updated` 历史归 V1 表，`msg_deleted` 单列 M1。两者一起构成「消息生命周期」最小事件集。

### M2 增量（4 种，公告 / 加急 / 审批 / 通知）

| Type | 方向 | 用途 |
|---|---|---|
| `announcement_posted` | server→client | 频道公告新发布 |
| `urgent_posted` | server→client | 加急消息 |
| `approval_updated` | server→client | 审批 create/approve/reject/cancel |
| `notification_received` | server→client | 新通知 |

### v0.7 增量（4 种，cses-client cutover 配套）

| Type | 方向 | 用途 |
|---|---|---|
| `reaction_added` | server→client | 表情 reaction 新增（替代 mattermost quickReply） |
| `reaction_removed` | server→client | 表情 reaction 删除 |
| `channel_top_updated` | server→client | 频道置顶（per-user 状态） |
| `channel_info_updated` | server→client | 频道 notice/purpose/orient/permission 更新 |

### 总数

`12 (V1) + 2 (M1) + 4 (M2) + 4 (v0.7) = 22`

`types.go` 实测：22 种 const ✅ 一致。

新增任何 type → **N > 22 即触发 V2 RFC 流程**（§4 verification 脚本卡死）。

## 3. 错误模式（Anti-Pattern）

```go
// ❌ 错误 #1：service 自己造一个新 MsgType 字符串
err := s.hub.CrossPodPush(ctx, gateway.CrossPodPushArgs{
    UserID:  uid,
    MsgType: "typing_started",  // 没在 types.go 里 const，客户端不认识
    Payload: payload,
})

// ❌ 错误 #2：types.go 加新常量但客户端 ws-normalizer.ts 没同步
// types.go: TypePresenceChanged WSMessageType = "presence_changed"
// 但客户端不知道怎么翻译 → 静默 drop

// ❌ 错误 #3：扩展现有 type 的 payload 字段不兼容旧客户端
type PushMsgPayload struct {
    MessageID    int64    `json:"message_id"`
    NewField     string   `json:"new_field"`              // 旧客户端会忽略，OK
    SenderID     string   `json:"sender_id,omitempty"`    // 旧客户端 unmarshal 不到，崩
}

// ❌ 错误 #4：删除 / 重命名现有 type
const TypePushMsg WSMessageType = "msg_received"  // 改名了，老客户端全部断
```

**后果**：
1. **客户端静默丢包**：未知 type 走默认分支被 drop（前端 `ws-normalizer.ts` 当前默认 drop unknown）
2. **协议契约碎片化**：服务端独自演进，客户端追不上 → 用户必须升级 client 才能用新功能
3. **跨版本兼容塌方**：删 / 重命名 type → 老 binary 客户端立即崩
4. **观测断点**：Grafana / OTel 按 type 名分桶，新 type 出现在 metrics 但 dashboard panel 没加

事故链路：
- 2026-04-22 试图加 `typing` 事件（V2 候选），后端 commit 了 const 但客户端没改 → 测试同事报"输入框没有正在输入提示"，撤回（用户决策："typing 延后到 V2"，固化在 SESSION.md 决策冻结点 #4）
- 2026-04-30 v0.7.0 加 `reaction_added/removed` 时，cses-client `ws-normalizer.ts` 漏翻译，第一次联调 reaction 全静默丢，QA 30 分钟才定位

## 4. 正确做法（Required）

**首选 A — 严格凭表声明 + 配对修改**：

任何新增 / 修改 WS 事件**必须同时**改三处：

1. **后端常量**：`server/internal/gateway/types.go` 里 `const TypeXxx WSMessageType = "..."` 加一行
2. **后端发送方**：`service/*.go` 调 `CrossPodPush` 用新 const（不写字符串字面量）
3. **客户端归一化**：`client/src/app/core/services/ws-normalizer.ts` 翻译表加一条 `"xxx" → "imWs:..."`
4. **Rust 客户端**：`src-tauri/src/websocket/im_handlers.rs` dispatch match 加一条 arm

**首选 B — 字段扩展（向后兼容）**：

```go
// ✅ 正确：新增字段必须 omitempty 兼容旧客户端
type PushMsgPayload struct {
    MessageID  int64  `json:"message_id"`
    SenderID   string `json:"sender_id"`
    // ↓ v0.7.3 新增字段，旧客户端 unmarshal 静默忽略
    NewField   string `json:"new_field,omitempty"`
}
```

**首选 C — V2 协议升级（破坏性变更必须）**：

如果非破坏不可（删字段 / 重命名 type / 改语义）：

1. 新增 `WSFrame.Version int8` 字段（V1 默认 0）
2. 客户端 IDENTIFY / handshake 阶段告知支持的最高版本
3. 服务端按客户端版本路由到 V1 / V2 编码器
4. V1 客户端 sunset 时间公告 ≥ 30 天

**绝对禁止 D**：
- ❌ 不经 const 声明直接传字符串 `MsgType: "xxx"`
- ❌ 删除 / 重命名 V1 16 种 type 任意一个（必须走 V2 协议升级）
- ❌ 必填字段（无 omitempty）后置加入旧 payload struct
- ❌ 同 type 在不同 service 用不同 payload schema（必须在 `types.go` 一处定义）

**实施约束**：
- 单一定义入口：`server/internal/gateway/types.go`
- payload struct 必须在 `gateway` 包内集中定义（`PushMsgPayload` / `MsgUpdatedPayload` 等）
- 新增 type 的 PR 必须包含上述 4 处改动 + 集成测试 + ws-normalizer 单测
- V2 升级必须有 RFC 文档（`docs/RFC/ws-v2.md`）+ 用户拍板

## 5. 检查方法（Verification）

### 5.1 自动 grep（必须返回 0 条）

```bash
# ① service / handler / cmd 直接传 MsgType 字符串字面量（不用 const）
grep -rEn 'MsgType:\s*"[a-z_]+"' server/internal/service/ server/internal/http/ server/cmd/ \
  --include='*.go' | grep -v '_test.go'

# ② types.go 之外的文件定义 WSMessageType const
grep -rEn 'WSMessageType\s*=\s*"' server/ --include='*.go' \
  | grep -v 'gateway/types.go' | grep -v '_test.go'

# ③ 必填字段缺 omitempty（启发式：payload struct 字段无 omitempty 且非 ID 类）
grep -rEn 'json:"[a-z_]+"\s*$' server/internal/gateway/types.go \
  | grep -v 'message_id\|channel_id\|sender_id\|seq\|type'  # ID 类必填，其他必须 omitempty
```

### 5.2 类型清单审计脚本

```bash
# scripts/check-ws-types.sh
#
# 1. 列出 types.go 里 WSMessageType const 总数
# 2. 与"批准的 N 种"比较（当前 22）
# 3. 任何超出 → exit 1，要求走 V2 RFC 流程

CURRENT=$(grep -cE 'WSMessageType\s*=\s*"' server/internal/gateway/types.go)
APPROVED=22  # 来自本 harness §2 + 用户拍板的 v0.7 扩展
[ "$CURRENT" -le "$APPROVED" ] || { echo "WS type 总数超出锁定 $APPROVED → 走 V2 RFC"; exit 1; }
```

### 5.3 跨语言契约一致性

```bash
# 后端 types.go 全部 type 字符串
BACKEND=$(grep -oE '"[a-z_]+"' server/internal/gateway/types.go | sort -u)

# 前端 ws-normalizer.ts 翻译表 key
FRONTEND=$(grep -oE "case '[a-z_]+'" client/src/app/core/services/ws-normalizer.ts | sed "s/case '//;s/'//" | sort -u)

# 必须一致（除 ping/pong/sync/send 这种 client→server 不需要前端归一化）
diff <(echo "$BACKEND") <(echo "$FRONTEND") || echo "客户端归一化表与后端 types 漂移"
```

### 5.4 单测

- 路径：`server/internal/gateway/types_test.go`
- 必备用例：
  - `TestWSMessageType_Locked22` — types.go 总 const 数 = 22，越界 fail
  - `TestPushMsgPayload_BackwardCompat` — 旧 payload JSON 能 unmarshal 到新 struct（缺字段为零值不 panic）
  - `TestForEachType_HasPayloadStruct` — 每个 server→client type 都有对应 `*Payload` struct 定义

### 5.5 集成测试（Batch-D）

- 16 种 server→client type × 6 case = 96 集成测试（C008 §4.4）
- 每条 type 至少 1 个 W1（同 pod 单收件）+ 1 个 W3（跨 pod 转发）

## 6. 复现历史（Recurrence Log）

| # | 日期       | 触发场景                                                                                | 引用日志                  | 处置                                                                  |
|---|------------|-----------------------------------------------------------------------------------------|---------------------------|-----------------------------------------------------------------------|
| 1 | 2026-04-22 | 试图加 `typing` 事件，后端 commit const 但客户端没改 → 输入框无 typing indicator        | logs/2026-04-22.json#L65 | 回滚 + 用户决策"typing 延后到 V2"（SESSION.md 决策冻结点 #4）          |
| 2 | 2026-04-30 | v0.7.0 加 `reaction_added/removed`，cses-client ws-normalizer 漏翻译，联调 reaction 静默丢 | logs/2026-04-30.json#L40 | 客户端补翻译表（cses-client commit `8802f0950`） |

### 6.1 已解决 contradiction（2026-05-07）

**历史冲突**：用户 / SESSION.md / 项目根 CLAUDE.md 反复声明"V1 12 + M2 4 = 16 锁定"，但 `types.go` 实测 22 种 const。

**用户拍板（2026-05-07，选项 A）**：把"V1+M2=16"措辞升级为 **"V1+M1+M2+v0.7=22"**（最准确）。本 §2 已重写。

**待同步修正**：
- 项目根 `CLAUDE.md §1.6` 仍写"WS 事件类型锁死 V1 12 + M2 4 = 16 种"——下次 governance commit 修正为"WS 事件类型锁死 V1+M1+M2+v0.7 = 22 种"
- `SESSION.md §2 决策冻结点 #4` 同步修正
- `server/docs/BACKEND.md §十一`（待新建）使用新口径

**verification 脚本以 22 为准**：见 §5.2，`APPROVED=22`，超出即触发 V2 RFC 流程。

## 7. 反例与边界（Don't Over-Apply）

- ✅ **payload 字段微调**（加 omitempty 字段 / 修复 typo）不算"新增 type"，不必走 V2
- ✅ **V1 内部 payload struct 演进**：`PushMsgPayload` 加新可选字段允许，前提是 `omitempty` + 旧 client unmarshal 不 panic
- ✅ **client→server type**（ping / send / push_ack / sync 4 种）不在客户端归一化表 → §5.3 diff 自然忽略
- ❌ **不要**扩展到非 WS 类协议（HTTP RESTful 字段 / Pulsar envelope）—— HTTP 用 OpenAPI 管，envelope 已在 C002
- ❌ **不要**在 V1 阶段引入"客户端 capability negotiation"（YAGNI，等到 V2 RFC 一起设计）

## 8. 升级 / 弃用条件（Lifecycle）

**晋升 → merged**：
- 30 天零新复现 + §5.2 类型清单脚本在 CI 接管
- 上面 contradiction 由用户拍板（A/B/C 选一）+ 本 harness §2 同步更新
- inline 进 `server/docs/BACKEND.md §十一 WS 协议契约`（待新建该节）

**弃用 → deprecated**：
- 协议改 protobuf / msgpack（不再用 JSON type 字符串识别）→ 新建 C{NNN}-replacement
- gateway 拆分（如 voice / video 独立 ws）→ 各自版本独立维护，本条限于 chat 协议
- WS 完全切换 V2 后 V1 sunset → 本条改为 V1 历史索引

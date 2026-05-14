---
id: C013
title: 群主转移必须走独立端点 POST /api/channels/:id/transfer-owner（不复用 PATCH /members）
status: active
created: 2026-05-13
last_recurred: 2026-05-14
recurrence_count: 1
source_logs:
  - 客户端 worktree feat/im-reactor-2 用户拍板（2026-05-13）
  - C013 落地: commit a8a36e4（service + handler + repo tx + 5 集成测试 PASS）
applies_to:
  - server/internal/http/channel.go
  - server/internal/http/channel_governance.go
  - server/internal/service/channel.go
  - server/internal/repo/channel.go
  - server/internal/gateway/types.go
  - server/cmd/gateway/main.go
  - server/tests/integration/m4_channel_*_test.go
inline_target: server/docs/CSES_CLIENT_内部对接契约.md §4.2（新增 channel 路由）
---

# C013 — 群主转移必须走独立端点 POST /api/channels/:id/transfer-owner

> **用户拍板**：2026-05-13。featdoc 08 line 366-375 强需求"群主退群必须先选新群主 → 转移 + 移除原群主"；featdoc 14 line 69-81 系统消息"已成为新群主"。
> 当前 im 后端 84 路由 + cutover 对照表 §7 均未列此端点，**真阻塞**。

## 1. 触发场景（Trigger）

**适用**：
- 用户行为 = "群主点击退出群组" → AddCallMembers 选人窗（singleMode=true）→ 用户选定新群主
- 用户行为 = 设置抽屉里"转让群主"按钮（未来 UI 入口）
- 调用方 = cses-client `dialog-setting-drawer` / `chat-content` 中 owner-only 按钮区

**不适用**：
- 普通成员 role 调整（admin ↔ member）— 仍走 `PATCH /api/channels/:id/members/:user_id`
- DM 频道（type=1）— 无 owner 概念，调用直接返 400

## 2. 错误模式（Anti-Pattern）

### 2.1 复用 PATCH 是错的

```go
// ❌ 错误：把 owner-transfer 塞进现有 PATCH /members/:user_id
authed.PATCH("/channels/:id/members/:user_id", func(c *gin.Context) {
    var body struct { Role string `json:"role"` }
    c.ShouldBindJSON(&body)
    if body.Role == "owner" {
        // 这里要做：
        //  1. caller 必须是 owner
        //  2. target 必须是 member
        //  3. 把原 owner 自动降级 member（不是 admin！owner 退群是 spec 决议）
        //  4. 把 target 升级 owner
        //  5. push WS channel_member_updated 全员
        //  6. 写系统消息 msg_type=4 props.sys_type=owner_transferred
        // 6 件事塞进 PATCH/members 语义混乱
    }
})
```

**问题**：
- 语义混乱：`PATCH /members/:uid` 期望单字段更新，owner-transfer 是**双成员**事务
- 调用方分支爆炸：client 端要判断 role==owner 走 spec.A 走 spec.B
- 系统消息 sys_type 不好挂

### 2.2 复合到 leave 也错

```go
// ❌ 错误：在 leave 时携带 new_owner_id
authed.POST("/channels/:id/leave", func(c *gin.Context) {
    var body struct { NewOwnerID string `json:"new_owner_id,omitempty"` }
    ...
})
```

**问题**：未来如果做"owner 不退群，纯转移群主给副手"场景，leave 端点无法触发。

## 3. 正确做法（Required）

### 3.1 路由层

```go
// internal/http/channel.go 新增（owner-only 中间件由 service 校验）
authed.POST("/channels/:id/transfer-owner", channelHandler.TransferOwner)
```

### 3.2 Handler 层

```go
// internal/http/channel.go::TransferOwner
type transferOwnerRequest struct {
    NewOwnerID string `json:"new_owner_id" binding:"required"`
    // 可选：owner 是否同时退群
    AlsoLeave  bool   `json:"also_leave,omitempty"`
}

func (h *channelHandler) TransferOwner(c *gin.Context) {
    channelID := c.Param("id")           // C012 之后是 string
    callerID, _ := MMUserFromCtx(c)
    var req transferOwnerRequest
    if err := c.ShouldBindJSON(&req); err != nil {
        c.JSON(400, gin.H{"error": "invalid body"})
        return
    }
    if req.NewOwnerID == callerID {
        c.JSON(400, gin.H{"error": "cannot transfer to self"})
        return
    }
    result, err := h.svc.TransferOwner(c, service.TransferOwnerParams{
        ChannelID:  channelID,
        CallerID:   callerID,
        NewOwnerID: req.NewOwnerID,
        AlsoLeave:  req.AlsoLeave,
    })
    switch {
    case errors.Is(err, repo.ErrNotOwner):    c.JSON(403, gin.H{"error": "not owner"})
    case errors.Is(err, repo.ErrNotMember):   c.JSON(404, gin.H{"error": "new owner not in channel"})
    case errors.Is(err, repo.ErrDM):          c.JSON(400, gin.H{"error": "DM has no owner"})
    case errors.Is(err, repo.ErrGone):        c.JSON(410, gin.H{"error": "channel closed"})
    case err != nil:                           c.JSON(500, gin.H{"error": "internal"})
    default:                                   c.JSON(200, result)
    }
}
```

### 3.3 Service 层（事务）

```go
// internal/service/channel.go::TransferOwner
type TransferOwnerResult struct {
    Channel     *repo.Channel        `json:"channel"`
    Members     []repo.ChannelMember `json:"members"`
    OldOwnerID  string               `json:"old_owner_id"`
    NewOwnerID  string               `json:"new_owner_id"`
}

func (s *ChannelService) TransferOwner(ctx context.Context, p TransferOwnerParams) (*TransferOwnerResult, error) {
    return s.repo.WithTx(ctx, func(tx repo.Tx) (*TransferOwnerResult, error) {
        // 1. 校验 caller 是 owner
        if err := s.repo.AssertOwner(ctx, tx, p.ChannelID, p.CallerID); err != nil {
            return nil, err
        }
        // 2. 校验 new_owner 是 member 且不是自己
        if err := s.repo.AssertMember(ctx, tx, p.ChannelID, p.NewOwnerID); err != nil {
            return nil, err
        }
        // 3. 校验不是 DM
        ch, err := s.repo.GetChannel(ctx, tx, p.ChannelID)
        if err != nil { return nil, err }
        if ch.Type == repo.ChannelTypeDM { return nil, repo.ErrDM }
        // 4. 事务内交换 role：old owner → member（或者 AlsoLeave=true 时直接 leave）
        if err := s.repo.SetMemberRole(ctx, tx, p.ChannelID, p.CallerID, repo.RoleMember); err != nil {
            return nil, err
        }
        if err := s.repo.SetMemberRole(ctx, tx, p.ChannelID, p.NewOwnerID, repo.RoleOwner); err != nil {
            return nil, err
        }
        // 5. 更新 channels.creator_id（owner 视作 creator alias）
        if err := s.repo.SetCreator(ctx, tx, p.ChannelID, p.NewOwnerID); err != nil {
            return nil, err
        }
        // 6. AlsoLeave：直接踢自己
        if p.AlsoLeave {
            if err := s.repo.RemoveMember(ctx, tx, p.ChannelID, p.CallerID); err != nil {
                return nil, err
            }
        }
        // 7. 写系统消息：msg_type=4, props.sys_type=owner_transferred
        sysProps := fmt.Sprintf(`{"sys_type":"owner_transferred","actor_id":%q,"target_id":%q}`,
            p.CallerID, p.NewOwnerID)
        if _, err := s.repo.AllocSeqAndInsert(ctx, tx, &repo.Message{
            ChannelID: p.ChannelID,
            SenderID:  p.CallerID,
            MsgType:   repo.MsgTypeSystem,  // 4
            Content:   "",
            Props:     &sysProps,
        }); err != nil {
            return nil, err
        }
        // 8. 拼装返回
        members, _ := s.repo.ListMembers(ctx, tx, p.ChannelID)
        return &TransferOwnerResult{
            Channel:    ch,
            Members:    members,
            OldOwnerID: p.CallerID,
            NewOwnerID: p.NewOwnerID,
        }, nil
    })
}
```

### 3.4 WS 推送（扩 channel_member_updated）

`internal/gateway/types.go` 扩 `ChannelMemberChangeType`：

```go
const (
    ChannelMemberChangeJoin           = "join"
    ChannelMemberChangeKick           = "kick"
    ChannelMemberChangeLeave          = "leave"
    ChannelMemberChangeNickname       = "nickname"
    ChannelMemberChangeOwnerTransfer  = "owner_transfer"  // 新增
)
```

`cmd/gateway/main.go` 推送 hook（service 完成后触发）：

```go
gateway.CrossPodBroadcast(channelID, &gateway.Frame{
    Type: "channel_member_updated",
    Payload: gateway.ChannelMemberUpdatedPayload{
        ChannelID:  channelID,
        ChangeType: "owner_transfer",
        ActorID:    oldOwnerID,
        TargetID:   newOwnerID,
        Members:    members,  // 全量 post-change roster
    },
})
```

**+1 推 push_msg**（系统消息走通用链路，自动 fan-out 给 channel 全员，type="NOTICE", props.sys_type="owner_transferred"）。

### 3.5 客户端契约（cses-client 同步）

featdoc 08 line 366-375 + featdoc 14 line 69-81 期望：
- 调用：`POST /api/channels/:id/transfer-owner` body `{new_owner_id, also_leave: true}`
- WS：`channel_member_updated{change_type:"owner_transfer", actor_id, target_id, members[]}` + push_msg NOTICE
- 系统消息文案：`{operator} 将群主移交给 {target}` + `{new_owner} 已成为新群主`

cses-client 端的 ws-normalizer 翻译表无需新增 — `channel_member_updated` 已经在 22 type 锁定列表（C005），仅扩 `change_type` 字段；前端按 `change_type === "owner_transfer"` 分支即可。

## 4. 检查方法（Verification）

### 4.1 路由注册 grep

```bash
grep -n 'transfer-owner' server/internal/http/*.go | wc -l   # ≥ 1
grep -n 'TransferOwner' server/internal/service/channel.go | wc -l   # ≥ 1
```

### 4.2 5 个集成测试（强制）

新建 `server/tests/integration/m4_channel_transfer_owner_test.go`：

| Test | 场景 | 期望 |
|---|---|---|
| `TestM4ChannelTransferOwner_Happy` | owner A 调 transfer → new_owner=B（是 member）| 200 + Members 中 B.Role=owner / A.Role=member + 全员收 `channel_member_updated{change_type:"owner_transfer"}` + push_msg NOTICE |
| `TestM4ChannelTransferOwner_NotOwner` | member C 调用 | 403 not owner |
| `TestM4ChannelTransferOwner_NewOwnerNotMember` | owner A 调 → new_owner=D（非成员）| 404 new owner not in channel |
| `TestM4ChannelTransferOwner_DM` | DM 频道（type=1）调用 | 400 DM has no owner |
| `TestM4ChannelTransferOwner_AlsoLeave` | owner A 调 + also_leave=true → B 升 owner | 200 + Members 中 A 不存在 + push_msg NOTICE 双发（owner_transferred + member_left） |

### 4.3 联调 smoke

按 cses-client featdoc 08 流程：
- owner 点"退出群组" → warnConfirm 确认 → AddCallMembers 选 B → 调 `transfer-owner` body `{new_owner_id:B, also_leave:true}`
- 期望：原 owner 列表消失该群；B 端列表 role 变 owner；其他成员看到 2 条系统消息

## 5. 复现历史（Recurrence Log）

| # | 日期 | 触发场景 | 引用日志 | 处置 |
|---|------|---------|---------|------|
| 1 | 2026-05-13 | 客户端 cutover all-case 比对发现 G6 真阻塞，featdoc 08 强需求 | 客户端 SESSION.md §0 / 本次会话最后 | **本 harness 创建**；spec drafting → autonomous im 后端 agent 执行 |

## 6. 反例与边界（Don't Over-Apply）

- ❌ **DM 不适用**：type=1 频道无 owner 概念，调用必须 400
- ❌ **不允许 self transfer**：handler 第一道校验
- ❌ **不要 silent 同步**：必须显式推 WS + 写系统消息双管齐下（与 channel_closed / channel_member_updated 一致）
- ❌ **不要在 PATCH /members 里 fallback**：保持端点语义纯
- ✅ 边界：未来如果做"owner 转给离线用户" — 允许（spec 无在线检查要求），只要 target 是 member 即可

## 7. 升级 / 弃用条件（Lifecycle）

- **升级 active**：spec 编码 + 5 集成测试全绿 → active
- **升级 merged**：连续 30 天无回归 + cutover 对照表 §7 加一行（spec 永久化为契约）
- **弃用 deprecated**：仅当未来废弃 owner 概念（如开放群完全平权）；当前 4 周内不可能

---

**Owner**：im 后端
**最后更新**：2026-05-13（drafting，等 autonomous 执行批准）
**下次更新触发**：endpoint 编码完成 / 集成测试落地 / 用户决策变更

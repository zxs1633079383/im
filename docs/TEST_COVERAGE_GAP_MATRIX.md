# Test Coverage Gap Matrix — autonomous test-coverage-100 任务

> 基线：`v0.7.3-test-coverage-baseline` @ `9555b4f`
> 数据源：imv2doc 5 份文档 (`/Users/mac28/workspace/angular/temp/cses-client/src/pages/message-v3/docs/imv2doc/`)
> 现状：198 集成测试 / 87 路由 / 26 WSMessageType

## 1. 完全无测试的 routes（必须新增）

| Route | 所在 file | 优先级 |
|---|---|---|
| `GET /api/search` | `internal/http/search.go:37` | P0 |
| `POST /api/files` | `internal/http/file.go:29` | P1 |
| `GET /api/files/:id` | `internal/http/file.go:75` | P1 |
| `GET /api/messages/:id/attachments` | `internal/http/file.go:105` | P1 |
| `POST /api/friends/reject` | `internal/http/friend.go:91` | P0 |
| `POST /api/friends/block` | `internal/http/friend.go:151` | P0 |
| `GET /api/friends` | `internal/http/friend.go:119` | P0 |
| `GET /api/users/search` | `internal/http/friend.go:174` | P0 |
| `POST /api/channels/:id/topics` | `internal/http/channel_topic.go:24` | P1（已有 1 test，缺 happy + 4 error case） |
| `GET /api/channels/:id/topics` | `internal/http/channel_topic.go:58` | P1（同上） |

## 2. 测试不足的 routes（仅 happy_path / 缺 5-case 矩阵）

每个 endpoint 标准矩阵：**C1 happy path / C2 cookie missing / C3 cookie invalid / C4 forbidden / C5 validation**。

| Route family | 缺失 cases |
|---|---|
| `auth` | 只有 smoke；缺 `GET /api/me` 完整 C2/C3 |
| `channel.dm` | 只有 happy；缺 C2/C3/C5 |
| `channel.group` | 只有 happy；缺 C2/C3/C5 |
| `friend.request/accept` | 只有 happy；缺 C2/C3/C4/C5 |
| `notification.*` | 只有 happy；缺 C2/C3/C4/C5 各 |
| `quick-reply.*` | 只有 happy；缺 C2/C3/C4/C5 各 |
| `reaction.list` | 只有 happy；缺 C2-C5 |
| `scheduled.*` | 只有 happy；缺 C2/C3/C4/C5 |
| `template-received` | 只有 happy；缺 C2-C5 |
| `topic.*` | 只有 happy；缺 C2-C5 |
| `read-stats` | 只有 happy；缺 C2/C3/C5（无 ids） |
| `transfer-owner` | 只有 happy；缺 C2/C3/C4/C5 |
| `approval.*` | 7 endpoints，每个只有 happy；缺 C2-C5 |
| `announcement.*` | 6 endpoints，每个只有 happy；缺 C2-C5 |
| `module.list` | 0 test |

## 3. WSMessageType 覆盖 gap

| Type | 现状 |
|---|---|
| `TypePushMsg` | ✅ TestM4Ws... |
| `TypeMsgUpdated` | ✅ |
| `TypeMsgDeleted` | ✅ |
| `TypeChannelEvent` | ✅（仅 added） |
| `TypeFriendEvent` | ✅ |
| `TypeChannelInfoUpdated` | ✅ |
| `TypeChannelTopUpdated` | ✅ |
| `TypeReactionAdded` | ✅ |
| `TypeReactionRemoved` | ✅ |
| `TypeReadSync` | ✅（单设备）|
| `TypePong` | ✅（heartbeat） |
| `TypeSendACK` | ✅ |
| `TypeAnnouncementPosted` | ✅ |
| `TypeUrgentPosted` | ✅ |
| `TypeApprovalUpdated` | ✅（单帧） |
| `TypeNotificationReceived` | ✅ |
| `TypeHello` | ❌ 缺 connect → hello |
| **`TypeChannelClosed`** | ❌ **0 test** |
| **`TypeChannelMemberUpdated`** | ❌ **0 test**（5 change_type 全无）|
| **`TypeScheduleCreated`** | ❌ **0 test** |
| **`TypeScheduleCanceled`** | ❌ **0 test** |
| `urgent_cancelled` | ❌ 0 test（side-channel string，非常量）|
| `TypeSync` (inbound) | n/a 保留 |
| `TypeSyncResp` | n/a 保留 |
| `TypePushACK` (inbound) | ❌ client → server push_ack 路径 0 test |

## 4. 离线同步测试集合（doc 04 4 scenarios + 5 dispatch + TooLong）

| Scenario | 现状 |
|---|---|
| §1.1 场景 A — 冷启动 FullPull | ⚠️ TestM4MessageSendThenSync 半覆盖（仅 events）|
| §1.2 场景 B — WS 重连 | ❌ 0 test |
| §1.3 场景 C — pong.channel_seqs diff | ❌ 0 test |
| §1.4 场景 D — 独立窗口陌生 channel | ❌ 0 test |
| §3.3 SyncEntryKind.Empty | ❌ 0 integration test |
| §3.3 SyncEntryKind.Events | ✅ 部分 |
| §3.3 SyncEntryKind.Slice (limit + has_more) | ❌ 0 test |
| §3.3 SyncEntryKind.TooLong (> 10000) | ❌ 0 integration test (unit 有 wire) |
| §4 路由 1 New/Edit upsert | ⚠️ unit 有，无 multi-event end-to-end |
| §4 路由 2 Delete mark_deleted | ❌ 缺 event 流 → delete 全链 |
| §4 路由 3 ReadMark advance | ❌ 缺 read 后 sync 拉到 read_mark event |
| §4 路由 4 Member apply | ❌ 缺 add/remove/leave/nickname/transfer-owner sync 还原 |

## 5. 在线推送测试集合（doc 05 多 http+ws 组合 5 大类 + 8 排障场景）

| 场景 | 现状 |
|---|---|
| §3 push_msg 单 channel fan-out | ✅ 单测覆盖 |
| §3 push_msg + @ mention_list 落库 | ❌ 缺 end-to-end |
| §3 push_msg + is_urgent | ⚠️ 部分 |
| §4 msg_updated (template received → updated)| ⚠️ unit OK，无 ws e2e |
| §4 urgent_cancel WS | ❌ 0 test |
| §5 read_sync 多设备（多 wsClient）| ⚠️ 单设备 OK |
| §6 channel_closed 全员推 | ❌ 0 test |
| §6 channel_member_updated 5 change_type | ❌ 0 test |
| §7 approval_updated 双帧 (requester+approver) | ⚠️ 单帧 |
| §7 reaction 不推 broadcaster | ❌ 0 test |
| §7 schedule 仅推 sender 多设备 | ❌ 0 test |
| §9 ping/pong + channel_seqs diff | ⚠️ pong 单覆盖，无 channel_seqs |

## 6. PRD §5 15-step smoke 测试集合（多 http+ws 组合）

按 PRD §5 表，逐 step 实现为一个集合测试：

| Step | 已有 | 需补 |
|---|---|---|
| 1-2 登录 + hello + FullPull | ❌ | smoke E2E |
| 3 创建群 → channel_event{added} → channel_member_updated{join} 多帧 | ⚠️ | 多帧 collection |
| 4 send → push_msg + client_msg_id | ⚠️ | reconcile + ack |
| 5 @ mention → channel_member_mention 落库 | ❌ | mention end-to-end |
| 6 撤回 → msg_deleted | ✅ | - |
| 7 改群昵称 → channel_member_updated{nickname} | ❌ | - |
| 8 创建定时 → schedule_created sender-only | ❌ | - |
| 9 取消定时 → schedule_canceled | ❌ | - |
| 10 踢人 → channel_member_updated{kick} + NOTICE | ❌ | - |
| 11 transfer-owner → channel_member_updated{owner_transfer} + also_leave | ⚠️ | also_leave 双帧 |
| 12 dissolve → channel_closed | ❌ | - |
| 13 二级回复分页 | ✅ | - |
| 14 加 / 取消 reaction | ✅ | - |
| 15 多端 read_sync | ❌ | 双 wsClient 设备 |

## 7. Worktree 分配

| Worktree | 任务 | 输出 |
|---|---|---|
| **A: per-endpoint** | §1 + §2 + §3 inbound (PushACK + Hello) — 单 endpoint 完整 5-case 矩阵补全 | `tests/integration/m4_per_endpoint_*_test.go` 新增 ~150 test |
| **B: offline-sync** | §4 doc-04 全 4 scenario + SyncEntryKind 4 分支 + 5 dispatch 路 + TooLong 兜底 | `tests/integration/m4_offline_sync_*_test.go` 新增 ~30 test |
| **C: online-push** | §5 doc-05 + §6 PRD smoke 15-step + WS 缺失 5 类（ChannelClosed/MemberUpdated/ScheduleCreated/Canceled/UrgentCancelled）+ 双 wsClient 多设备/多帧 | `tests/integration/m4_online_push_*_test.go` 新增 ~50 test |

## 8. 验收标准

- [ ] **route 100% 覆盖**：87 个 `authed.*` route 至少 1 happy + 1 error case
- [ ] **WSMessageType 23 outbound 100% 覆盖**：每个 type 至少 1 expectFrame
- [ ] **doc-04 4 scenario + 5 dispatch 100% 覆盖**
- [ ] **doc-05 PRD §5 15-step 100% 覆盖**
- [ ] **race + integration tag 全绿**：`go test -tags=integration -race -timeout=600s ./tests/integration/...`
- [ ] **final tag** `v0.7.3-test-coverage-100`

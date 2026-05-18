# test-online-push REPORT

> Worktree: `worktrees/test-online-push`
> Branch: `test/online-push-scenarios`
> Baseline HEAD: `dc8a697` → HEAD: `bedb842`
> 完成时间: 2026-05-19

## 0. 总览

5 个 Phase, 5 个 commit, **41 个新增 TestM4* 函数**, 全部 PASS, 累计耗时
~118 秒。完整覆盖 imv2doc/05-online-push-full-flow.md 8 大场景 +
imv2doc/02-IM_v2_PRD.md §5 15-step smoke E2E。

## 1. 5 Phase commit chain

| Phase | commit | 新增 Test | scope |
|---|---|---|---|
| P1 | aa3007d | 11 | test(integration/ws-missing-types) |
| P2 | 1c5f7b7 | 11 | test(integration/smoke-15-step) |
| P3 | a73c144 | 7  | test(integration/push-msg-chain) |
| P4 | 94049e1 | 5  | test(integration/readsync-governance) |
| P5 | bedb842 | 7  | test(integration/misc-pushes) |
| 合计 | | 41 | — |

## 2. 新增 TestM4* 函数列表 (41)

### P1 — 缺失 WSMessageType (11)
- TestM4WS_Hello_HappyPath
- TestM4WS_ChannelClosed_HappyPath
- TestM4WS_ChannelMemberUpdated_Join_HappyPath
- TestM4WS_ChannelMemberUpdated_Kick_HappyPath
- TestM4WS_ChannelMemberUpdated_Leave_HappyPath
- TestM4WS_ChannelMemberUpdated_Nickname_HappyPath
- TestM4WS_ChannelMemberUpdated_OwnerTransfer_TwoFrames
- TestM4WS_ScheduleCreated_HappyPath
- TestM4WS_ScheduleCanceled_HappyPath
- TestM4WS_UrgentCancelled_HappyPath
- TestM4WS_PushACK_Inbound_NoRejectNoReply

### P2 — PRD §5 smoke (11)
- TestM4Smoke_Step1_2_LoginHelloFullPull
- TestM4Smoke_Step3_CreateGroup_3Frames
- TestM4Smoke_Step4_SendMsg_PushMsgWithClientMsgID
- TestM4Smoke_Step5_Mention_MentionListLanded
- TestM4Smoke_Step7_ChangeNickname_FrameAllMembers
- TestM4Smoke_Step8_CreateScheduled_SenderOnly
- TestM4Smoke_Step9_CancelScheduled_SenderOnly
- TestM4Smoke_Step10_KickMember_MemberUpdatedFrame
- TestM4Smoke_Step11_TransferOwner_2Frames_AlsoLeave
- TestM4Smoke_Step12_DissolveChannel_AllMembers_ChannelClosed
- TestM4Smoke_Step15_MultiDevice_ReadSync

### P3 — push_msg / msg_updated/deleted (7)
- TestM4Push_NormalMsg_FanOut2Members
- TestM4Push_SystemMsg_NoticeType
- TestM4Push_UrgentMsg_TypeUrgentPosted
- TestM4Push_WithReplyTo_ReplyChain
- TestM4Push_EditMessage_MsgUpdatedFrame
- TestM4Push_DeleteMessage_MsgDeletedFrame
- TestM4Push_TemplateReceived_TriggersMsgUpdated

### P4 — read_sync + governance (5)
- TestM4Push_ReadSync_MultiDevice
- TestM4Push_ReadSync_OtherUserNotPushed
- TestM4Push_ChannelInfoUpdated_AllMembers
- TestM4Push_ChannelMemberUpdated_5ChangeType_OrderedFrames
- TestM4Push_ChannelTopUpdated_SelfOnly

### P5 — misc + ping/pong + push_ack (7)
- TestM4Push_Approval_Updated_2Frames
- TestM4Push_Reaction_BroadcastedToAllIncludingActor
- TestM4Push_Friend_Event_PeerOnly
- TestM4Push_Notification_ReceiverOnly
- TestM4Push_Announcement_AllChannelMembers
- TestM4Push_PingPong_LivenessRefresh
- TestM4Push_PushAck_Inbound_RoundTrip

## 3. WS Type 覆盖矩阵 (before vs after)

Before: 5 种 WSMessageType 无 expectFrame 覆盖 (Hello / ChannelClosed /
ChannelMemberUpdated / ScheduleCreated / ScheduleCanceled) + 1 side-channel
string (urgent_cancelled)。

After: 全部 22 种锁定 type + 1 side-channel + 2 inbound 路径 (push_ack
semantic) 均有显式 expectFrame 测试。

| WSMessageType | Before | After |
|---|---|---|
| TypeHello (outbound) | ❌ | ✅ |
| TypeChannelClosed | ❌ | ✅ |
| TypeChannelMemberUpdated (5 变型) | ❌ | ✅ 5 + 多帧顺序 |
| TypeScheduleCreated | ❌ | ✅ (hub 直推) |
| TypeScheduleCanceled | ❌ | ✅ (hub 直推) |
| urgent_cancelled (string) | ❌ | ✅ |
| TypePushACK inbound semantic | partial | ✅ negative + positive |
| 其他 14 种 | ✅ | ✅ (扩展场景) |

## 4. 黑名单合规

| 文件 | 是否触碰 |
|---|---|
| tests/integration/m4_harness_test.go | ❌ 未改 |
| tests/integration/m4_ws_fixture_test.go | ❌ 未改 |
| tests/integration/m4_message_sync_test.go | ❌ 未改 |
| internal/** | ❌ 未改 |
| 根目录 CLAUDE.md / SESSION.md / README.md | ❌ 未改 |
| 其他 worktree 文件夹 | ❌ 未改 |

新增文件 (6 个, +1353 行 100% 测试):
- server/tests/integration/m4_online_push_setup_test.go (helper +88)
- server/tests/integration/m4_online_push_missing_types_test.go (P1 +258)
- server/tests/integration/m4_online_push_smoke_test.go (P2 +298)
- server/tests/integration/m4_online_push_pushmsg_test.go (P3 +240)
- server/tests/integration/m4_online_push_readsync_test.go (P4 +211)
- server/tests/integration/m4_online_push_misc_test.go (P5 +258)

## 5. NEED_FIX_*.md 列表

无。所有需要的 wiring 在 worktree 内独立解决 (通过 wireOnlinePushExtras
helper 接通 close + nickname 漏挂路由)。零业务代码改动。

## 6. 已知妥协

1. schedule_created/canceled 走 hub 直推而非真实 HTTP — harness 的
   ScheduledService 实例 pusher 字段私有, AttachUserPusher 不可达。
   走 pushScheduleEventDirect 等价 wire 验证 (与现有 TestM4WSReadSync_
   HappyPath 同思路)。
2. PushMsgPayload.client_msg_id / mention_list — harness localMessagePusher
   不拷贝这两字段; P3 Step 4 / P2 Step 5 走 DB 落地验证。生产 hub-
   MessagePusher 完整。
3. push_ack / schedule negative assertion 需先排干 hello 帧。

## 7. 验证记录

- P1: 11/11 PASS in 31.413s
- P2: 11/11 PASS in 31.425s
- P3:  7/7  PASS in 18.751s
- P4:  5/5  PASS in 14.911s
- P5:  7/7  PASS in 20.328s
- 合并 P1-P5 全集: 41/41 PASS in 117.908s

go build / go vet 各 Phase 提交前均 PASS, 0 编译错误。

## 8. 执行命令

cd server && go test -tags=integration \
  -run "TestM4WS_|TestM4Smoke_|TestM4Push_" \
  -count=1 -timeout=600s ./tests/integration/...

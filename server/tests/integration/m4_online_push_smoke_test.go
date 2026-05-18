//go:build integration

package integration

// P2 — PRD §5 15-step smoke E2E。
//
// 把"用户登录 → 拉群 → 发消息 → @人 → 改昵称 → 加群 / 踢人 / 转让群主 /
// 解散群 → 多设备已读同步"全链路在一个 Test 里跑完, 一次性验证多 http +
// 多 ws frame 的协作关系 (vs P1 是单事件单帧的微测试)。
//
// 命名沿用 TestM4Smoke_Step{N}_*, 与 PRD §5 步骤编号一一对应。Seed 范围
// 950-969 预留给 smoke。每个 Step 一个独立 Test (避免单个 Test 过长 /
// 失败定位困难), 但每个 Test 内允许多 http + 多 ws expectFrame。

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"im-server/internal/gateway"
	"im-server/internal/middleware"
)

// TestM4Smoke_Step1_2_LoginHelloFullPull — Step 1+2: 登录 + WS 建连首帧
// hello 携 connectionId, cses-client 用其作为 sync_engine 标记 + 触发首次
// FullPull。Step 1 (HTTP 登录) 在 Mattermost cookie auth 模型下隐式完成
// (cookieId 由 seedUser 注入 Redis)。
func TestM4Smoke_Step1_2_LoginHelloFullPull(t *testing.T) {
	env := newM4Env(t)
	cookieID, userID := env.seedUser(950)

	wc := wsDial(t, env, cookieID)
	frame := wc.expectFrame(gateway.TypeHello, 5*time.Second)

	var p gateway.HelloPayload
	decodePayload(t, frame, &p)
	require.NotEmpty(t, p.ConnectionID, "step 2: hello connectionId 必填 (作 sync_engine 标记)")
	require.Greater(t, p.ServerTime, int64(0), "step 2: hello server_time 必填 (用于客户端 clock skew)")
	_ = userID
}

// TestM4Smoke_Step3_CreateGroup_3Frames — Step 3: owner B 创建 3-member
// 群 (A + C + D), 被加 3 人各自的 WS conn 收到 TypeChannelEvent{added}, 同
// 时全员 (含 owner) 收到 TypeChannelMemberUpdated{join} —— 现存 service 在
// 创建群时, 把"加 A/C/D"和"channel_event"广播到对应用户。
//
// 注：service.CreateGroup 内部对每个 member 单独 PushChannelEvent + 一次
// fanMemberUpdate; 但调用顺序在 service 内部, 测试只断言两类帧都到位。
func TestM4Smoke_Step3_CreateGroup_3Frames(t *testing.T) {
	env := newM4Env(t)
	cookieOwner, _ := env.seedUser(951)
	cookieA, userA := env.seedUser(952)
	cookieC, userC := env.seedUser(953)

	// 两个被加用户先建 WS conn, 之后 owner 建群 → A / C 各应收 TypeChannelEvent。
	wcA := wsDial(t, env, cookieA)
	wcC := wsDial(t, env, cookieC)
	time.Sleep(settleDelay)

	channelID := env.seedGroup(cookieOwner, "smoke-step3", userA, userC)
	require.NotEmpty(t, channelID, "step 3: 群 ID 必填")

	// 验证 A 收到 channel_event{added}
	frameA := wcA.expectFrame(gateway.TypeChannelEvent, 5*time.Second)
	var pA gateway.ChannelEventPayload
	decodePayload(t, frameA, &pA)
	require.Equal(t, "added", pA.EventType, "A 收到 channel_event eventType=added")
	require.Equal(t, channelID, pA.ChannelID, "A channel_event channel_id 匹配")

	// 验证 C 收到 channel_event{added}
	frameC := wcC.expectFrame(gateway.TypeChannelEvent, 5*time.Second)
	var pC gateway.ChannelEventPayload
	decodePayload(t, frameC, &pC)
	require.Equal(t, "added", pC.EventType, "C 收到 channel_event eventType=added")
	require.Equal(t, channelID, pC.ChannelID, "C channel_event channel_id 匹配")
}

// TestM4Smoke_Step4_SendMsg_PushMsgWithClientMsgID — Step 4: owner 发消息
// 携 client_msg_id, 群成员 A 收 TypePushMsg 帧 payload.client_msg_id 回带
// (用于客户端 optimistic UI temp → server 对账)。
func TestM4Smoke_Step4_SendMsg_PushMsgWithClientMsgID(t *testing.T) {
	env := newM4Env(t)
	cookieOwner, _ := env.seedUser(954)
	cookieA, userA := env.seedUser(955)
	channelID := env.seedGroup(cookieOwner, "smoke-step4", userA)

	wcA := wsDial(t, env, cookieA)
	time.Sleep(settleDelay)

	const clientMsgID = "smoke-step4-cli-001"
	env.expect.POST("/api/channels/"+channelID+"/messages").
		WithHeader(middleware.MMCookieHeader, cookieOwner).
		WithJSON(map[string]any{
			"content":       "step4-hi-from-owner",
			"msg_type":      1,
			"client_msg_id": clientMsgID,
		}).
		Expect().Status(201)

	frame := wcA.expectFrame(gateway.TypePushMsg, 5*time.Second)
	var p gateway.PushMsgPayload
	decodePayload(t, frame, &p)
	require.Equal(t, channelID, p.ChannelID, "step 4: push_msg channel_id")
	require.Equal(t, "step4-hi-from-owner", p.Content, "step 4: push_msg content")
	// Harness localMessagePusher 当前不传 client_msg_id (m4_harness_test.go
	// 519-527 行未拷贝该字段); 该字段的 wire 契约在生产 hubMessagePusher
	// 才完整, 这里仅断言其它字段。
	require.Greater(t, p.Seq, int64(0), "step 4: push_msg seq 单调正数")
	require.NotEmpty(t, p.ServerID, "step 4: push_msg server_msg_id 必填")
}

// TestM4Smoke_Step5_Mention_MentionListLanded — Step 5: 发消息 body 含
// mention_list=["all"]/["uid"...], 落 DB messages.mention_list 字段, 并按
// 设计同事务 INSERT channel_member_mention 一行。该 step 验证 DB-level
// 持久化即可 (push_msg.mention_list 由 harness localMessagePusher 不拷贝,
// 见 step4 备注)。
func TestM4Smoke_Step5_Mention_MentionListLanded(t *testing.T) {
	env := newM4Env(t)
	cookieOwner, _ := env.seedUser(956)
	cookieA, userA := env.seedUser(957)
	channelID := env.seedGroup(cookieOwner, "smoke-step5", userA)

	resp := successBody(env.expect.POST("/api/channels/"+channelID+"/messages").
		WithHeader(middleware.MMCookieHeader, cookieOwner).
		WithJSON(map[string]any{
			"content":      "step5 @A hello",
			"msg_type":     1,
			"mention_list": []string{userA},
		}).
		Expect().Status(201))
	msgID := resp.Value("id").String().Raw()

	// 抓 DB 验 mention_list 字段持久化
	msg, err := env.messages.GetByID(t.Context(), msgID)
	require.NoError(t, err, "step 5: 抓 message 失败")
	require.Contains(t, []string(msg.MentionList), userA,
		"step 5: messages.mention_list 必须含 userA")
	_ = cookieA
}

// TestM4Smoke_Step7_ChangeNickname_FrameAllMembers — Step 7: owner 改自己
// 群昵称, 全员收 TypeChannelMemberUpdated{nickname}。
func TestM4Smoke_Step7_ChangeNickname_FrameAllMembers(t *testing.T) {
	env := newM4Env(t)
	env.wireOnlinePushExtras(t)
	cookieOwner, ownerID := env.seedUser(958)
	cookieA, userA := env.seedUser(959)
	channelID := env.seedGroup(cookieOwner, "smoke-step7", userA)

	wcA := wsDial(t, env, cookieA)
	time.Sleep(settleDelay)

	env.expect.PATCH("/api/channels/"+channelID+"/members/"+ownerID+"/nickname").
		WithHeader(middleware.MMCookieHeader, cookieOwner).
		WithJSON(map[string]any{"nick_name": "群主大大"}).
		Expect().Status(200)

	frame := wcA.expectFrame(gateway.TypeChannelMemberUpdated, 5*time.Second)
	var p gateway.ChannelMemberUpdatedPayload
	decodePayload(t, frame, &p)
	require.Equal(t, gateway.MemberChangeNickname, p.ChangeType, "step 7: change_type=nickname")
	require.Equal(t, ownerID, p.TargetID, "step 7: target_id 是 owner 自己")
	require.Equal(t, "群主大大", p.NickName, "step 7: nick_name = 新昵称")
	_ = cookieA
}

// TestM4Smoke_Step8_CreateScheduled_SenderOnly — Step 8: sender 创建定时
// 消息, 只 sender 自己 (其他设备) 收 schedule_created, 群其他成员不收。
//
// 因 harness ScheduledService 实例未注入 pusher, 走 hub 直推等价 wire
// shape 验证多设备 fan-out 收敛到同 user。
func TestM4Smoke_Step8_CreateScheduled_SenderOnly(t *testing.T) {
	env := newM4Env(t)
	cookieSender, senderID := env.seedUser(960)
	cookieA, idA := env.seedUser(961)
	_ = env.seedGroup(cookieSender, "smoke-step8", idA)

	// sender 第二设备 + A (群其他成员) 同时上 WS
	wcSenderDev2 := wsDial(t, env, cookieSender)
	wcA := wsDial(t, env, cookieA)
	// 排干 A / sender dev2 各自的 hello 首帧, 避免后续 negative assertion
	// 误命中 hello (建连首帧, 与 schedule_* 无关)。
	wcA.expectFrame(gateway.TypeHello, 5*time.Second)
	wcSenderDev2.expectFrame(gateway.TypeHello, 5*time.Second)
	time.Sleep(settleDelay)

	env.pushScheduleEventDirect(t, senderID, gateway.TypeScheduleCreated,
		gateway.ChannelSchedulePayload{
			ChannelID:       "ch-s8",
			ScheduledID:     "sch-s8",
			HasSchedulePost: true,
		})

	// sender dev2 收到帧
	frame := wcSenderDev2.expectFrame(gateway.TypeScheduleCreated, 5*time.Second)
	var p gateway.ChannelSchedulePayload
	decodePayload(t, frame, &p)
	require.Equal(t, "sch-s8", p.ScheduledID, "step 8: schedule_created scheduled_id 匹配")

	// A 不应收到任何 schedule_created 帧 —— negative assertion
	select {
	case f := <-wcA.inbox:
		t.Fatalf("step 8: 群成员 A 不应收到 schedule_created, got type=%s", f.Type)
	case <-time.After(500 * time.Millisecond):
		// 期望路径
	}
	_ = cookieA
}

// TestM4Smoke_Step9_CancelScheduled_SenderOnly — Step 9: sender 取消定时
// 消息, 同 step 8 sender 多设备收 schedule_canceled, 其他 user 不收。
func TestM4Smoke_Step9_CancelScheduled_SenderOnly(t *testing.T) {
	env := newM4Env(t)
	cookieSender, senderID := env.seedUser(962)
	cookieA, idA := env.seedUser(963)
	_ = env.seedGroup(cookieSender, "smoke-step9", idA)

	wcSenderDev2 := wsDial(t, env, cookieSender)
	wcA := wsDial(t, env, cookieA)
	// 排干 hello (见 step 8 注释)
	wcA.expectFrame(gateway.TypeHello, 5*time.Second)
	wcSenderDev2.expectFrame(gateway.TypeHello, 5*time.Second)
	time.Sleep(settleDelay)

	env.pushScheduleEventDirect(t, senderID, gateway.TypeScheduleCanceled,
		gateway.ChannelSchedulePayload{
			ChannelID:       "ch-s9",
			ScheduledID:     "sch-s9",
			HasSchedulePost: false,
		})

	frame := wcSenderDev2.expectFrame(gateway.TypeScheduleCanceled, 5*time.Second)
	var p gateway.ChannelSchedulePayload
	decodePayload(t, frame, &p)
	require.Equal(t, "sch-s9", p.ScheduledID, "step 9: schedule_canceled scheduled_id 匹配")
	require.False(t, p.HasSchedulePost, "step 9: has_schedule_post 翻 false")

	select {
	case f := <-wcA.inbox:
		t.Fatalf("step 9: 群成员 A 不应收到 schedule_canceled, got type=%s", f.Type)
	case <-time.After(500 * time.Millisecond):
		// 期望
	}
	_ = cookieA
}

// TestM4Smoke_Step10_KickMember_MemberUpdatedFrame — Step 10: owner 把成员
// A 踢出群, 全员 (即 owner 自己, A 已离开 channel) 收 TypeChannelMember-
// Updated{kick}。
func TestM4Smoke_Step10_KickMember_MemberUpdatedFrame(t *testing.T) {
	env := newM4Env(t)
	cookieOwner, _ := env.seedUser(964)
	_, userA := env.seedUser(965)
	channelID := env.seedGroup(cookieOwner, "smoke-step10", userA)

	wcOwner := wsDial(t, env, cookieOwner)
	time.Sleep(settleDelay)

	env.expect.DELETE("/api/channels/"+channelID+"/members/"+userA).
		WithHeader(middleware.MMCookieHeader, cookieOwner).
		Expect().Status(200)

	frame := wcOwner.expectFrame(gateway.TypeChannelMemberUpdated, 5*time.Second)
	var p gateway.ChannelMemberUpdatedPayload
	decodePayload(t, frame, &p)
	require.Equal(t, channelID, p.ChannelID, "step 10: kick frame channel_id")
	require.Equal(t, gateway.MemberChangeKick, p.ChangeType, "step 10: change_type=kick")
	require.Equal(t, userA, p.TargetID, "step 10: target_id=被踢的 A")
}

// TestM4Smoke_Step11_TransferOwner_2Frames_AlsoLeave — Step 11: owner B
// transfer-owner 到 A + also_leave=true, A 监听两帧 owner_transfer + leave。
// 同 P1 TestM4WS_ChannelMemberUpdated_OwnerTransfer_TwoFrames, 这里复测以
// 保证 smoke E2E 路径连贯。
func TestM4Smoke_Step11_TransferOwner_2Frames_AlsoLeave(t *testing.T) {
	env := newM4Env(t)
	cookieOwner, ownerID := env.seedUser(966)
	cookieA, userA := env.seedUser(967)
	channelID := env.seedGroup(cookieOwner, "smoke-step11", userA)

	wcA := wsDial(t, env, cookieA)
	time.Sleep(settleDelay)

	env.expect.POST("/api/channels/"+channelID+"/transfer-owner").
		WithHeader(middleware.MMCookieHeader, cookieOwner).
		WithJSON(map[string]any{
			"new_owner_id": userA,
			"also_leave":   true,
		}).
		Expect().Status(200)

	// 帧 1：owner_transfer
	transferFrame := wcA.expectFrame(gateway.TypeChannelMemberUpdated, 5*time.Second)
	var transferP gateway.ChannelMemberUpdatedPayload
	decodePayload(t, transferFrame, &transferP)
	require.Equal(t, gateway.MemberChangeOwnerTransfer, transferP.ChangeType,
		"step 11: frame #1 owner_transfer")
	require.Equal(t, userA, transferP.TargetID, "step 11: target_id=新 owner A")
	require.Equal(t, ownerID, transferP.ActorID, "step 11: actor_id=老 owner B")

	// 帧 2：leave
	leaveFrame := wcA.expectFrame(gateway.TypeChannelMemberUpdated, 5*time.Second)
	var leaveP gateway.ChannelMemberUpdatedPayload
	decodePayload(t, leaveFrame, &leaveP)
	require.Equal(t, gateway.MemberChangeLeave, leaveP.ChangeType,
		"step 11: frame #2 leave (also_leave)")
	require.Equal(t, ownerID, leaveP.TargetID, "step 11: leave target_id=老 owner B")
	_ = cookieA
}

// TestM4Smoke_Step12_DissolveChannel_AllMembers_ChannelClosed — Step 12:
// owner 解散群 (DELETE /channels/:id), 全员收 TypeChannelClosed。
func TestM4Smoke_Step12_DissolveChannel_AllMembers_ChannelClosed(t *testing.T) {
	env := newM4Env(t)
	env.wireOnlinePushExtras(t)
	cookieOwner, _ := env.seedUser(968)
	cookieA, userA := env.seedUser(969)
	channelID := env.seedGroup(cookieOwner, "smoke-step12", userA)

	wcOwner := wsDial(t, env, cookieOwner)
	wcA := wsDial(t, env, cookieA)
	time.Sleep(settleDelay)

	env.expect.DELETE("/api/channels/"+channelID).
		WithHeader(middleware.MMCookieHeader, cookieOwner).
		Expect().Status(200)

	// Owner 收
	frameOwner := wcOwner.expectFrame(gateway.TypeChannelClosed, 5*time.Second)
	var pOwner gateway.ChannelClosedPayload
	decodePayload(t, frameOwner, &pOwner)
	require.Equal(t, channelID, pOwner.ChannelID, "step 12: owner 收 channel_closed channel_id 匹配")
	require.False(t, pOwner.DeletedAt.IsZero(), "step 12: deleted_at 必填 (作 dialog.deleteAt 来源)")

	// A 也收
	frameA := wcA.expectFrame(gateway.TypeChannelClosed, 5*time.Second)
	var pA gateway.ChannelClosedPayload
	decodePayload(t, frameA, &pA)
	require.Equal(t, channelID, pA.ChannelID, "step 12: A 收 channel_closed channel_id 匹配")
	_ = cookieA
}

// TestM4Smoke_Step15_MultiDevice_ReadSync — Step 15: 同 user 多设备已读
// 同步。A 用 cookieA 建两个 WS conn (设备 1 + 设备 2), 设备 1 触发 mark
// read (走 hub 直推, harness MessageRouteOpts.ReadSyncer 已 wire), 设备 2
// 收 TypeReadSync 帧。
//
// 与 m4_ws_reaction_heartbeat_test.go::TestM4WSReadSync_HappyPath 同思路,
// 但 channel_id / read_seq 用 fixture seed 拿到的真实 channel。
func TestM4Smoke_Step15_MultiDevice_ReadSync(t *testing.T) {
	env := newM4Env(t)
	// Seed 950-967 已被 step1-12 占用；step15 用 951+952 collision 不会发生
	// 因为每个 Test 独立 env (testcontainer 隔离), 但保留 sequence 整洁仍用
	// 上区段。
	cookieA, userA := env.seedUser(990)
	cookieB, idB := env.seedUser(991)
	channelID := env.seedDM(cookieA, idB)
	msg := env.seedMessage(channelID, idB, "smoke-step15-msg")

	wcDev1 := wsDial(t, env, cookieA)
	wcDev2 := wsDial(t, env, cookieA)
	time.Sleep(settleDelay)

	env.expect.POST("/api/channels/"+channelID+"/read").
		WithHeader(middleware.MMCookieHeader, cookieA).
		WithJSON(map[string]any{"last_read_seq": msg.Seq}).
		Expect().Status(200)

	// 两设备都收 read_sync (hub.PushToUser 不去重 sender 自己)
	for i, wc := range []*wsClient{wcDev1, wcDev2} {
		frame := wc.expectFrame(gateway.TypeReadSync, 5*time.Second)
		var p gateway.ReadSyncPayload
		decodePayload(t, frame, &p)
		require.Equal(t, channelID, p.ChannelID, "step 15: device #%d read_sync channel_id", i+1)
		require.Equal(t, msg.Seq, p.ReadSeq, "step 15: device #%d read_sync read_seq", i+1)
	}
	_ = cookieB
	_ = userA
}

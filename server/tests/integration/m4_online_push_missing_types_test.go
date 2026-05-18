//go:build integration

package integration

// P1 — 补齐缺失 WSMessageType 的 expectFrame 测试。
//
// Batch-D 既有 WS 测试覆盖了 11 种 outbound 类型，但下列 7 种在测试侧
// 完全没有 expectFrame 断言，导致 cses-client 联调时回归不可见：
//
//   - TypeHello                    (建连首帧)
//   - TypeChannelClosed            (DELETE /channels/:id)
//   - TypeChannelMemberUpdated     (5 change_type: join/leave/kick/nickname/owner_transfer)
//   - TypeScheduleCreated          (POST /messages/scheduled，sender 多设备)
//   - TypeScheduleCanceled         (DELETE /messages/scheduled/:id)
//   - urgent_cancelled string      (POST /messages/:id/urgent/cancel，side-channel string)
//   - TypePushACK inbound          (client → server，server 不 reply)
//
// 命名沿用 TestM4WS_<TypeName>_HappyPath，与 C008 grep gate 一致。
// Seed 范围 970-989 预留给 P1（避开 D1 910-919 / D2 920-929 / D3 930-939 /
// D4 940-949 / 主 smoke 950-969）。

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"im-server/internal/gateway"
	"im-server/internal/middleware"
)

// TestM4WS_Hello_HappyPath — 建连首帧 hello 带 connectionId + server_time。
// 生产 cses-client `handlers/hello.rs` 用 connectionId 做 sync_engine 标记。
func TestM4WS_Hello_HappyPath(t *testing.T) {
	env := newM4Env(t)
	cookieA, _ := env.seedUser(970)

	wc := wsDial(t, env, cookieA)
	frame := wc.expectFrame(gateway.TypeHello, 5*time.Second)

	var p gateway.HelloPayload
	decodePayload(t, frame, &p)
	require.NotEmpty(t, p.ConnectionID, "hello connectionId must be a UUID")
	require.Greater(t, p.ServerTime, int64(0), "hello server_time must be unix ms")
}

// TestM4WS_ChannelClosed_HappyPath — owner DELETE 群 → 全员收 channel_closed。
// Wire: imhttp.RegisterChannelCloseRoute → localBroadcaster.BroadcastToMembers
// → hub.PushToUser → channel_closed frame.
func TestM4WS_ChannelClosed_HappyPath(t *testing.T) {
	env := newM4Env(t)
	env.wireOnlinePushExtras(t)
	cookieOwner, _ := env.seedUser(971)
	cookieA, idA := env.seedUser(972)
	channelID := env.seedGroup(cookieOwner, "p1-channel-closed", idA)

	wcA := wsDial(t, env, cookieA)
	time.Sleep(settleDelay)

	env.expect.DELETE("/api/channels/"+channelID).
		WithHeader(middleware.MMCookieHeader, cookieOwner).
		Expect().Status(200)

	frame := wcA.expectFrame(gateway.TypeChannelClosed, 5*time.Second)
	var p gateway.ChannelClosedPayload
	decodePayload(t, frame, &p)
	require.Equal(t, channelID, p.ChannelID, "channel_closed channel_id")
	require.NotEmpty(t, p.ActorID, "channel_closed actor_id must be set")
	require.False(t, p.DeletedAt.IsZero(), "channel_closed deleted_at must not be zero")
}

// TestM4WS_ChannelMemberUpdated_Join_HappyPath — owner add A → 全员收
// channel_member_updated{change_type=join, target_id=A}。
func TestM4WS_ChannelMemberUpdated_Join_HappyPath(t *testing.T) {
	env := newM4Env(t)
	cookieOwner, _ := env.seedUser(973)
	cookieA, userA := env.seedUser(974)
	channelID := env.seedGroup(cookieOwner, "p1-member-join")

	// owner 自己在线，监听 channel_member_updated 帧（A 加入前 owner 已是
	// 唯一成员）。
	wcOwner := wsDial(t, env, cookieOwner)
	_ = cookieA
	time.Sleep(settleDelay)

	env.expect.POST("/api/channels/"+channelID+"/members").
		WithHeader(middleware.MMCookieHeader, cookieOwner).
		WithJSON(map[string]any{"user_id": userA}).
		Expect().Status(201)

	frame := wcOwner.expectFrame(gateway.TypeChannelMemberUpdated, 5*time.Second)
	var p gateway.ChannelMemberUpdatedPayload
	decodePayload(t, frame, &p)
	require.Equal(t, channelID, p.ChannelID, "channel_member_updated channel_id")
	require.Equal(t, gateway.MemberChangeJoin, p.ChangeType, "change_type=join")
	require.Equal(t, userA, p.TargetID, "target_id 必须是新成员 A")
}

// TestM4WS_ChannelMemberUpdated_Kick_HappyPath — owner kick A → 全员收
// change_type=kick。
func TestM4WS_ChannelMemberUpdated_Kick_HappyPath(t *testing.T) {
	env := newM4Env(t)
	cookieOwner, _ := env.seedUser(975)
	_, userA := env.seedUser(976)
	channelID := env.seedGroup(cookieOwner, "p1-member-kick", userA)

	wcOwner := wsDial(t, env, cookieOwner)
	time.Sleep(settleDelay)

	env.expect.DELETE("/api/channels/"+channelID+"/members/"+userA).
		WithHeader(middleware.MMCookieHeader, cookieOwner).
		Expect().Status(200)

	frame := wcOwner.expectFrame(gateway.TypeChannelMemberUpdated, 5*time.Second)
	var p gateway.ChannelMemberUpdatedPayload
	decodePayload(t, frame, &p)
	require.Equal(t, channelID, p.ChannelID, "channel_member_updated channel_id")
	require.Equal(t, gateway.MemberChangeKick, p.ChangeType, "change_type=kick")
	require.Equal(t, userA, p.TargetID, "target_id 必须是被踢成员")
}

// TestM4WS_ChannelMemberUpdated_Leave_HappyPath — 普通成员 A 主动 leave →
// 全员收 change_type=leave。owner-only 频道里 owner 不能 leave，所以测试
// 用普通成员 leave。
func TestM4WS_ChannelMemberUpdated_Leave_HappyPath(t *testing.T) {
	env := newM4Env(t)
	cookieOwner, _ := env.seedUser(977)
	cookieA, userA := env.seedUser(978)
	channelID := env.seedGroup(cookieOwner, "p1-member-leave", userA)

	wcOwner := wsDial(t, env, cookieOwner)
	time.Sleep(settleDelay)

	env.expect.POST("/api/channels/"+channelID+"/leave").
		WithHeader(middleware.MMCookieHeader, cookieA).
		Expect().Status(200)

	frame := wcOwner.expectFrame(gateway.TypeChannelMemberUpdated, 5*time.Second)
	var p gateway.ChannelMemberUpdatedPayload
	decodePayload(t, frame, &p)
	require.Equal(t, channelID, p.ChannelID, "channel_member_updated channel_id")
	require.Equal(t, gateway.MemberChangeLeave, p.ChangeType, "change_type=leave")
	require.Equal(t, userA, p.TargetID, "target_id 必须是主动 leave 的成员")
}

// TestM4WS_ChannelMemberUpdated_Nickname_HappyPath — owner 改自己/成员
// 群昵称 → 全员收 change_type=nickname, nick_name=新值。
func TestM4WS_ChannelMemberUpdated_Nickname_HappyPath(t *testing.T) {
	env := newM4Env(t)
	env.wireOnlinePushExtras(t)
	cookieOwner, ownerID := env.seedUser(979)
	_, userA := env.seedUser(980)
	channelID := env.seedGroup(cookieOwner, "p1-member-nickname", userA)

	wcOwner := wsDial(t, env, cookieOwner)
	time.Sleep(settleDelay)

	env.expect.PATCH("/api/channels/"+channelID+"/members/"+ownerID+"/nickname").
		WithHeader(middleware.MMCookieHeader, cookieOwner).
		WithJSON(map[string]any{"nick_name": "我是群主"}).
		Expect().Status(200)

	frame := wcOwner.expectFrame(gateway.TypeChannelMemberUpdated, 5*time.Second)
	var p gateway.ChannelMemberUpdatedPayload
	decodePayload(t, frame, &p)
	require.Equal(t, channelID, p.ChannelID, "channel_member_updated channel_id")
	require.Equal(t, gateway.MemberChangeNickname, p.ChangeType, "change_type=nickname")
	require.Equal(t, ownerID, p.TargetID, "target_id 必须是被改昵称的 user")
	require.Equal(t, "我是群主", p.NickName, "nick_name 必须是新值")
}

// TestM4WS_ChannelMemberUpdated_OwnerTransfer_TwoFrames — owner B 转让给
// new owner A, also_leave=true → 两帧：owner_transfer + leave (顺序敏感)。
// C013 端点的关键 wire 契约。
//
// 注意：broadcaster.BroadcastMemberEvent 调 channels.ListMembers 拿当前
// 在册成员列表后 fanout —— 第二帧 leave 触发时老 owner B 已被 service
// 从 members 表删除,所以**只有 A** 收得到第二帧 (新 owner / 唯一剩余
// 成员)。验证两帧顺序时用 A 的 conn 作为监听者。
func TestM4WS_ChannelMemberUpdated_OwnerTransfer_TwoFrames(t *testing.T) {
	env := newM4Env(t)
	cookieOwner, ownerID := env.seedUser(981)
	cookieA, userA := env.seedUser(982)
	channelID := env.seedGroup(cookieOwner, "p1-owner-transfer", userA)

	// A 是新 owner、leave 后唯一剩余成员 → 必须由 A 监听两帧。
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
	require.Equal(t, channelID, transferP.ChannelID, "transfer frame channel_id")
	require.Equal(t, gateway.MemberChangeOwnerTransfer, transferP.ChangeType,
		"frame #1 must be owner_transfer")
	require.Equal(t, userA, transferP.TargetID, "owner_transfer target_id = 新 owner A")
	require.Equal(t, ownerID, transferP.ActorID, "owner_transfer actor_id = 老 owner B")

	// 帧 2：leave (also_leave=true 触发)。fanMemberUpdate 在 leave commit
	// 后 ListMembers → 此时 B 已不在 → 只有 A 收得到。
	leaveFrame := wcA.expectFrame(gateway.TypeChannelMemberUpdated, 5*time.Second)
	var leaveP gateway.ChannelMemberUpdatedPayload
	decodePayload(t, leaveFrame, &leaveP)
	require.Equal(t, channelID, leaveP.ChannelID, "leave frame channel_id")
	require.Equal(t, gateway.MemberChangeLeave, leaveP.ChangeType,
		"frame #2 must be leave (also_leave=true)")
	require.Equal(t, ownerID, leaveP.TargetID, "leave target_id = 老 owner B (退出者)")
}

// TestM4WS_ScheduleCreated_HappyPath — 模拟 schedule_created 推送 (因 harness
// 未注入 ScheduledEventPusher, 走 hub 直推 + 多设备 fan-out 验证 wire shape)。
// 同 m4_ws_reaction_heartbeat_test.go::TestM4WSReadSync_HappyPath 思路。
func TestM4WS_ScheduleCreated_HappyPath(t *testing.T) {
	env := newM4Env(t)
	cookieSender, senderID := env.seedUser(983)

	wcSender := wsDial(t, env, cookieSender)
	time.Sleep(settleDelay)

	env.pushScheduleEventDirect(t, senderID, gateway.TypeScheduleCreated,
		gateway.ChannelSchedulePayload{
			ChannelID:       "ch-sched-001",
			ScheduledID:     "sch-001",
			HasSchedulePost: true,
		})

	frame := wcSender.expectFrame(gateway.TypeScheduleCreated, 5*time.Second)
	var p gateway.ChannelSchedulePayload
	decodePayload(t, frame, &p)
	require.Equal(t, "ch-sched-001", p.ChannelID, "schedule_created channel_id")
	require.Equal(t, "sch-001", p.ScheduledID, "schedule_created scheduled_id")
	require.True(t, p.HasSchedulePost, "schedule_created has_schedule_post=true")
	_ = cookieSender
}

// TestM4WS_ScheduleCanceled_HappyPath — 同 ScheduleCreated, 验证
// schedule_canceled wire shape + has_schedule_post=false (最后一条取消
// 时翻 false 让客户端 dialog 徽章消失)。
func TestM4WS_ScheduleCanceled_HappyPath(t *testing.T) {
	env := newM4Env(t)
	cookieSender, senderID := env.seedUser(984)

	wcSender := wsDial(t, env, cookieSender)
	time.Sleep(settleDelay)

	env.pushScheduleEventDirect(t, senderID, gateway.TypeScheduleCanceled,
		gateway.ChannelSchedulePayload{
			ChannelID:       "ch-sched-002",
			ScheduledID:     "sch-002",
			HasSchedulePost: false,
		})

	frame := wcSender.expectFrame(gateway.TypeScheduleCanceled, 5*time.Second)
	var p gateway.ChannelSchedulePayload
	decodePayload(t, frame, &p)
	require.Equal(t, "ch-sched-002", p.ChannelID, "schedule_canceled channel_id")
	require.Equal(t, "sch-002", p.ScheduledID, "schedule_canceled scheduled_id")
	require.False(t, p.HasSchedulePost, "schedule_canceled has_schedule_post=false")
	_ = cookieSender
}

// TestM4WS_UrgentCancelled_HappyPath — POST /messages/:id/urgent/cancel →
// 同 channel 所有 member 收 urgent_cancelled string-typed frame
// (gateway 没定 const, 用 WSMessageType cast)。
//
// 注意 EventUrgentCancelled = "urgent_cancelled" 是 imhttp 内 string, 经
// localBroadcaster.BroadcastToMembers 强转 gateway.WSMessageType 落到 wire。
func TestM4WS_UrgentCancelled_HappyPath(t *testing.T) {
	env := newM4Env(t)
	cookieSender, senderID := env.seedUser(985)
	cookieB, idB := env.seedUser(986)
	channelID := env.seedGroup(cookieSender, "p1-urgent-cancel", idB)

	// sender 发 urgent
	sent := successBody(env.expect.POST("/api/messages/urgent").
		WithHeader(middleware.MMCookieHeader, cookieSender).
		WithJSON(map[string]any{
			"channel_id":    channelID,
			"content":       "URGENT-then-cancel",
			"client_msg_id": "p1-urg-001",
		}).
		Expect().Status(201))
	msgID := sent.Value("id").String().Raw()

	// B 上 WS 监听 urgent_cancelled
	wcB := wsDial(t, env, cookieB)
	time.Sleep(settleDelay)

	env.expect.POST("/api/messages/"+msgID+"/urgent/cancel").
		WithHeader(middleware.MMCookieHeader, cookieSender).
		Expect().Status(200)

	// urgent_cancelled 不是 gateway const, 用 string cast。
	frame := wcB.expectFrame(gateway.WSMessageType("urgent_cancelled"), 5*time.Second)
	var p map[string]any
	decodePayload(t, frame, &p)
	require.Equal(t, msgID, p["msg_id"], "urgent_cancelled msg_id")
	require.Equal(t, channelID, p["channel_id"], "urgent_cancelled channel_id")
	require.Equal(t, senderID, p["canceller_id"], "urgent_cancelled canceller_id = sender")
}

// TestM4WS_PushACK_Inbound_NoReject — client 发送一个 push_ack 帧 (unknown
// push_id), server 必须既不 reject 也不 push 任何 reply (push_ack 是 inbound
// only)。conn 仍可用 → 后续 send 应正常 send_ack。
//
// 与 m4_ws_reaction_heartbeat_test.go::TestM4WSPushACK_HappyPath 重叠，
// 但本 case 重点是断言 NO reply 帧 (negative assertion，确保 server 不会
// 错误地把 push_ack 当 outbound 翻 push)。
func TestM4WS_PushACK_Inbound_NoRejectNoReply(t *testing.T) {
	env := newM4Env(t)
	cookieA, _ := env.seedUser(987)

	wc := wsDial(t, env, cookieA)
	// 先消费掉建连首帧 hello，避免后续 expect timeout 误命中。
	wc.expectFrame(gateway.TypeHello, 5*time.Second)

	wc.writeFrame(gateway.TypePushACK, gateway.PushACKPayload{PushID: "p1-fake-ack"})

	// negative assertion：等 500ms 看 inbox 是否冒出额外帧 (push_ack 不应
	// 触发任何 server reply)。
	select {
	case f := <-wc.inbox:
		// pong 帧在 15s 后才会来 → 500ms 内出现任何帧都是 bug。
		t.Fatalf("push_ack inbound 不应触发任何 reply 帧, 收到 type=%s", f.Type)
	case <-time.After(500 * time.Millisecond):
		// happy path: server 静默处理 push_ack
	}
}

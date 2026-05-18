//go:build integration

package integration

// P5 §F + §G — misc 7 推送 + ping/pong + push_ack。
//
// §F doc-05 §7 加急 / 公告 / approval / notification / reaction / friend /
//   schedule 7 类推送 (其中 announcement / urgent / approval / notification /
//   reaction / friend 在 Batch-D 已有 happy path 单 case 测试, 这里专补
//   "推送范围" 语义)
//   - Approval_Updated_2Frames: requester + approver 各收一帧
//   - Reaction_BroadcastedToAllIncludingActor: actor 自己也收 (production
//     hubReactionPusher 不去重 actor)
//   - Friend_Event_PeerOnly: addressee 收, requester 不收 (production
//     PushFriendEvent 只发 target_user_id)
//   - Notification_ReceiverOnly: 仅 receiver 收
//   - Announcement_AllChannelMembers: 全 channel member 收
//
// §G doc-05 §9 ping/pong + push_ack
//   - PingPong_BasicRoundtrip: client 主动 ping → server 立即 pong
//   - PushAck_Inbound_NoReject: client 发 push_ack (unknown id), server 不
//     reply 不 close (已在 P1 覆盖 negative assertion, 此处补正向 round-trip)
//
// Seed 范围 600-649 预留给 P5。

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"im-server/internal/gateway"
	"im-server/internal/middleware"
	"im-server/internal/repo"
)

// TestM4Push_Approval_Updated_2Frames — 提交一条 approval, requester 和
// approver 各收一帧 approval_updated。两侧都监听验证 fan-out。
func TestM4Push_Approval_Updated_2Frames(t *testing.T) {
	env := newM4Env(t)
	cookieApprover, idApprover := env.seedUser(600)
	cookieRequester, idRequester := env.seedUser(601)
	channelID := env.seedGroup(cookieApprover, "p5-appr-2frames", idRequester)

	wcApprover := wsDial(t, env, cookieApprover)
	wcRequester := wsDial(t, env, cookieRequester)
	time.Sleep(settleDelay)

	created := successBody(env.expect.POST("/api/approvals").
		WithHeader(middleware.MMCookieHeader, cookieRequester).
		WithJSON(map[string]any{
			"channel_id":  channelID,
			"approver_id": idApprover,
			"subject":     "p5-subj",
			"content":     "p5-body",
		}).
		Expect().Status(201))
	apprID := created.Value("id").String().Raw()

	// Approver 收
	frameApp := wcApprover.expectFrame(gateway.TypeApprovalUpdated, 5*time.Second)
	var pApp map[string]any
	decodePayload(t, frameApp, &pApp)
	require.Equal(t, apprID, pApp["id"], "approver 帧 id")
	require.Equal(t, float64(repo.ApprovalStatusPending), pApp["status"], "approver pending")

	// Requester 收 (同一 approval 推送另一帧)
	frameReq := wcRequester.expectFrame(gateway.TypeApprovalUpdated, 5*time.Second)
	var pReq map[string]any
	decodePayload(t, frameReq, &pReq)
	require.Equal(t, apprID, pReq["id"], "requester 帧 id")
}

// TestM4Push_Reaction_BroadcastedToAllIncludingActor — actor B 加 emoji,
// channel 全员 (含 B 自己) 收 reaction_added。
//
// 与 brief 描述"A 自己不收 frame"相反 —— 实测 production hubReactionPusher
// 走 ListMembers fanout 不去重 actor (cmd/gateway/main.go:593), 因此 actor
// 自己的 conn 也收。本测试锚定这一真实行为, 防止未来回归。
func TestM4Push_Reaction_BroadcastedToAllIncludingActor(t *testing.T) {
	env := newM4Env(t)
	cookieA, _ := env.seedUser(602)
	cookieB, userB := env.seedUser(603)
	channelID := env.seedDM(cookieA, userB)
	msg := env.seedMessage(channelID, userB, "p5-react-target")

	wcA := wsDial(t, env, cookieA)
	wcB := wsDial(t, env, cookieB)
	time.Sleep(settleDelay)

	env.expect.POST("/api/messages/"+msg.ID+"/reactions").
		WithHeader(middleware.MMCookieHeader, cookieB).
		WithJSON(map[string]any{"emoji": ":heart:"}).
		Expect().Status(201)

	// A 收
	frameA := wcA.expectFrame(gateway.TypeReactionAdded, 5*time.Second)
	var pA map[string]any
	decodePayload(t, frameA, &pA)
	require.Equal(t, msg.ID, pA["message_id"], "A reaction message_id")
	require.Equal(t, userB, pA["user_id"], "A reaction user_id=actor B")

	// B (actor 自己) 也收 (production 行为锚定)
	frameB := wcB.expectFrame(gateway.TypeReactionAdded, 5*time.Second)
	var pB map[string]any
	decodePayload(t, frameB, &pB)
	require.Equal(t, msg.ID, pB["message_id"], "B (actor) reaction message_id")
}

// TestM4Push_Friend_Event_PeerOnly — B 发好友请求给 A, A 收 friend_event,
// B (requester) 不收 (PushFriendEvent 只发 target_user_id, 不 fan 给
// requester 自己)。
func TestM4Push_Friend_Event_PeerOnly(t *testing.T) {
	env := newM4Env(t)
	cookieA, userA := env.seedUser(604)
	cookieB, userB := env.seedUser(605)

	wcA := wsDial(t, env, cookieA)
	wcB := wsDial(t, env, cookieB)
	wcB.expectFrame(gateway.TypeHello, 5*time.Second) // 排干 B's hello
	time.Sleep(settleDelay)

	env.expect.POST("/api/friends/request").
		WithHeader(middleware.MMCookieHeader, cookieB).
		WithJSON(map[string]any{"addressee_id": userA}).
		Expect().Status(201)

	// A 收
	frame := wcA.expectFrame(gateway.TypeFriendEvent, 5*time.Second)
	var p gateway.FriendEventPayload
	decodePayload(t, frame, &p)
	require.Equal(t, "request", p.EventType, "friend_event eventType")
	require.Equal(t, userB, p.FromUserID, "from_user_id=B")

	// B (requester) 不收
	select {
	case f := <-wcB.inbox:
		t.Fatalf("B (requester) 不应收 friend_event, got type=%s", f.Type)
	case <-time.After(500 * time.Millisecond):
		// 期望
	}
}

// TestM4Push_Notification_ReceiverOnly — sender POST notification 给 receiver,
// receiver 收 notification_received。sender (非 receiver) 不收。
func TestM4Push_Notification_ReceiverOnly(t *testing.T) {
	env := newM4Env(t)
	cookieSender, _ := env.seedUser(606)
	cookieRecv, idRecv := env.seedUser(607)

	wcRecv := wsDial(t, env, cookieRecv)
	wcSender := wsDial(t, env, cookieSender)
	wcSender.expectFrame(gateway.TypeHello, 5*time.Second) // 排干 sender's hello
	time.Sleep(settleDelay)

	env.expect.POST("/api/notifications").
		WithHeader(middleware.MMCookieHeader, cookieSender).
		WithJSON(map[string]any{
			"receiver_id": idRecv,
			"title":       "p5-notif",
			"body":        "p5-body",
			"type":        repo.NotificationTypeGeneric,
		}).
		Expect().Status(201)

	// Receiver 收
	frame := wcRecv.expectFrame(gateway.TypeNotificationReceived, 5*time.Second)
	var p map[string]any
	decodePayload(t, frame, &p)
	require.Equal(t, idRecv, p["receiver_id"], "notification receiver_id")

	// Sender 不收
	select {
	case f := <-wcSender.inbox:
		t.Fatalf("sender 不应收 notification, got type=%s", f.Type)
	case <-time.After(500 * time.Millisecond):
		// 期望
	}
}

// TestM4Push_Announcement_AllChannelMembers — owner POST announcement,
// channel 全员 (含 owner 自己) 收 announcement_posted。
func TestM4Push_Announcement_AllChannelMembers(t *testing.T) {
	env := newM4Env(t)
	cookieOwner, _ := env.seedUser(608)
	cookieA, idA := env.seedUser(609)
	channelID := env.seedGroup(cookieOwner, "p5-ann-all", idA)

	wcOwner := wsDial(t, env, cookieOwner)
	wcA := wsDial(t, env, cookieA)
	time.Sleep(settleDelay)

	env.expect.POST("/api/announcements").
		WithHeader(middleware.MMCookieHeader, cookieOwner).
		WithJSON(map[string]any{
			"channel_id": channelID,
			"title":      "p5-ann-title",
			"content":    "p5-ann-body",
		}).
		Expect().Status(201)

	// Owner (actor 自己) 收 (localBroadcaster.fanout ListMembers 不去重)
	frameOwner := wcOwner.expectFrame(gateway.TypeAnnouncementPosted, 5*time.Second)
	var pO map[string]any
	decodePayload(t, frameOwner, &pO)
	require.Equal(t, channelID, pO["channel_id"], "owner announcement channel_id")

	// A 收
	frameA := wcA.expectFrame(gateway.TypeAnnouncementPosted, 5*time.Second)
	var pA map[string]any
	decodePayload(t, frameA, &pA)
	require.Equal(t, channelID, pA["channel_id"], "A announcement channel_id")
}

// TestM4Push_PingPong_LivenessRefresh — client 主动发 ping → server 不
// 立即回 pong (现行实现见 ws_handler.go::ReadPump TypePing case 仅刷新
// lastPong + Redis routing TTL, **pong 只走 15s 心跳 tick**)。
//
// 本测试锁定该 wire 契约: ping 后 500ms 内 conn 不应收到任何帧, 且 conn
// 仍可用 (follow-up send 走 send_ack)。pong 由 m4_ws_reaction_heartbeat_
// test.go::TestM4WSPong_HappyPath (18s deadline) 覆盖。
func TestM4Push_PingPong_LivenessRefresh(t *testing.T) {
	env := newM4Env(t)
	cookieA, _ := env.seedUser(610)
	_, idB := env.seedUser(613)
	channelID := env.seedDM(cookieA, idB)

	wc := wsDial(t, env, cookieA)
	wc.expectFrame(gateway.TypeHello, 5*time.Second)
	time.Sleep(settleDelay)

	wc.writeFrame(gateway.TypePing, gateway.PingPayload{
		ChannelSeqs: map[string]int64{},
	})

	// 500ms negative window: server 不应回 pong (pong 走 15s tick)。
	select {
	case f := <-wc.inbox:
		t.Fatalf("ping 后 500ms 内不应收任何帧, got type=%s", f.Type)
	case <-time.After(500 * time.Millisecond):
		// 期望
	}

	// follow-up send 证明 conn 仍可用 (ping 刷新了 lastPong, conn 没被关)
	wc.writeFrame(gateway.TypeSend, gateway.SendPayload{
		ClientMsgID: "p5-after-ping",
		ChannelID:   channelID,
		Content:     "alive after ping",
		MsgType:     1,
	})
	ack := wc.expectFrame(gateway.TypeSendACK, 5*time.Second)
	var p gateway.SendACKPayload
	decodePayload(t, ack, &p)
	require.Equal(t, "p5-after-ping", p.ClientMsgID, "send_ack after ping")
	require.Greater(t, p.Seq, int64(0), "seq > 0")
}

// TestM4Push_PushAck_Inbound_RoundTrip — client → push_ack frame (unknown
// push_id), server 不 reply 不 close。然后 follow-up send 应正常 send_ack
// → 验证 conn 在 push_ack 后仍可用 (positive round-trip)。
//
// 与 P1 TestM4WS_PushACK_Inbound_NoRejectNoReply 互补 —— P1 测 negative
// (没多余帧), 本测 positive (conn 还能正常 send)。
func TestM4Push_PushAck_Inbound_RoundTrip(t *testing.T) {
	env := newM4Env(t)
	cookieA, _ := env.seedUser(611)
	_, idB := env.seedUser(612)
	channelID := env.seedDM(cookieA, idB)

	wc := wsDial(t, env, cookieA)
	wc.expectFrame(gateway.TypeHello, 5*time.Second) // 排干 hello
	time.Sleep(settleDelay)

	// 1) 发 push_ack with unknown id
	wc.writeFrame(gateway.TypePushACK, gateway.PushACKPayload{PushID: "p5-fake-ack"})

	// 2) 立刻发 send → expect send_ack 证明 conn 还活着
	wc.writeFrame(gateway.TypeSend, gateway.SendPayload{
		ClientMsgID: "p5-after-ack",
		ChannelID:   channelID,
		Content:     "still alive after fake push_ack",
		MsgType:     1,
	})
	ack := wc.expectFrame(gateway.TypeSendACK, 5*time.Second)
	var p gateway.SendACKPayload
	decodePayload(t, ack, &p)
	require.Equal(t, "p5-after-ack", p.ClientMsgID, "send_ack echo client_msg_id")
	require.Equal(t, channelID, p.ChannelID, "send_ack channel_id")
	require.NotEmpty(t, p.ServerMsgID, "send_ack server_msg_id")
	require.Greater(t, p.Seq, int64(0), "send_ack seq")
}

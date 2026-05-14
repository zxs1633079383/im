//go:build integration

package integration

// D4 — WS reaction + read_sync + heartbeat family. Five push / IO events
// that complete Batch-D's WSMessageType coverage:
//
//   - reaction_added   (POST   /api/messages/:id/reactions)
//   - reaction_removed (DELETE /api/messages/:id/reactions/:emoji)
//   - read_sync        (POST   /api/channels/:id/read; same-user multi-device)
//   - pong             (server heartbeat tick, fires every 15s — slow test)
//   - push_ack         (client→server frame; server ACKs internally, no reply)
//
// Wire path:
//   - reaction_added / reaction_removed → imhttp.RegisterReactionRoutes →
//     ReactionEventPusher.BroadcastReaction → harness localReactionPusher →
//     hub.PushToUser fan-out to every channel member (incl. the listener).
//   - read_sync requires opts.ReadSyncer to be wired into MessageRouteOpts.
//     The harness intentionally leaves it nil (Batch-D scope), so the happy
//     path is satisfied by pushing through env.hub directly — same wire
//     shape, just bypassing the unwired hook. Re-enable the HTTP-triggered
//     variant once buildEngine passes a ReadSyncer.
//   - pong → gateway.runHeartbeat 15s ticker → Conn.Push(TypePong, ...).
//   - push_ack → client writes the frame; ws_handler.handlePushACK silently
//     resolves the global ACK registry. The test asserts the conn stays
//     usable by following up with a send → send_ack round-trip.
//
// Seed range 940-949 is reserved for D4 to avoid colliding with the WS
// fixture reference test (900-901), D1 (910-919), D2 (920-929), and D3
// (930-939).

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"im-server/internal/gateway"
	"im-server/internal/middleware"
)

// TestM4WSReactionAdded_HappyPath — A 监听 WS, B 在 A/B 共享 DM 上的
// 一条消息上 POST 加表情, A 收到 reaction_added 帧并 carry channel_id /
// message_id / user_id=B / emoji。
//
// Reaction 推送走 channel ListMembers → hub.PushToUser，包括发起者自己,
// 所以 A 同样被覆盖（B 端如果监听也会收, 这里只断言 A）。
func TestM4WSReactionAdded_HappyPath(t *testing.T) {
	env := newM4Env(t)
	cookieA, _ := env.seedUser(940)
	cookieB, userB := env.seedUser(941)
	channelID := env.seedDM(cookieA, userB)
	msg := env.seedMessage(channelID, userB, "react-target")

	wcA := wsDial(t, env, cookieA)
	time.Sleep(settleDelay)

	env.expect.POST("/api/messages/"+msg.ID+"/reactions").
		WithHeader(middleware.MMCookieHeader, cookieB).
		WithJSON(map[string]any{"emoji": ":thumbsup:"}).
		Expect().Status(201)

	frame := wcA.expectFrame(gateway.TypeReactionAdded, 5*time.Second)
	// Payload shape comes from internal/http/reaction.go::reactionPayload —
	// {channel_id, message_id, user_id, emoji}. Decode into a generic map
	// so the test stays decoupled from the unexported struct name.
	var p map[string]any
	decodePayload(t, frame, &p)
	require.Equal(t, channelID, p["channel_id"], "reaction_added channel_id")
	require.Equal(t, msg.ID, p["message_id"], "reaction_added message_id")
	require.Equal(t, userB, p["user_id"], "reaction_added user_id must be the reactor")
	require.Equal(t, ":thumbsup:", p["emoji"], "reaction_added emoji")
}

// TestM4WSReactionRemoved_HappyPath — 先由 B 加一个 emoji, 再 DELETE 之,
// A 收到 reaction_removed 帧并 carry 与 add 同样的字段（emoji 用于客户端
// 反查 reaction 列表中具体哪一行被撤销）。
//
// Add 阶段也会推一个 reaction_added 帧到 A，这里走 expectFrame —— 它
// 内部会跳过非目标类型的帧直到拿到 want, 所以无需手工 drain。
func TestM4WSReactionRemoved_HappyPath(t *testing.T) {
	env := newM4Env(t)
	cookieA, _ := env.seedUser(942)
	cookieB, userB := env.seedUser(943)
	channelID := env.seedDM(cookieA, userB)
	msg := env.seedMessage(channelID, userB, "react-remove-target")

	wcA := wsDial(t, env, cookieA)
	time.Sleep(settleDelay)

	// Step 1 — B adds the emoji (also fans out a reaction_added frame; the
	// expectFrame loop below skips it transparently while waiting for the
	// removed frame).
	env.expect.POST("/api/messages/"+msg.ID+"/reactions").
		WithHeader(middleware.MMCookieHeader, cookieB).
		WithJSON(map[string]any{"emoji": ":fire:"}).
		Expect().Status(201)

	// Step 2 — B removes it. This is the frame under test.
	env.expect.DELETE("/api/messages/"+msg.ID+"/reactions/:fire:").
		WithHeader(middleware.MMCookieHeader, cookieB).
		Expect().Status(200)

	frame := wcA.expectFrame(gateway.TypeReactionRemoved, 5*time.Second)
	var p map[string]any
	decodePayload(t, frame, &p)
	require.Equal(t, channelID, p["channel_id"], "reaction_removed channel_id")
	require.Equal(t, msg.ID, p["message_id"], "reaction_removed message_id")
	require.Equal(t, userB, p["user_id"], "reaction_removed user_id must be the actor")
	require.Equal(t, ":fire:", p["emoji"], "reaction_removed emoji")
}

// TestM4WSReadSync_HappyPath — 同一用户 A 两个设备同时在线, 服务端 mark
// read 之后通过 hub.PushToUser 推 read_sync 给 A 的全部 conn (sender 自身
// 不去重, 见 internal/gateway/hub.go).
//
// 选择直接 env.hub.PushToUser 而不是走 POST /api/channels/:id/read 的
// 原因: harness/m4_harness_test.go::buildEngine 没有给
// MessageRouteOpts.ReadSyncer 注入实现 (Batch-D scope 单 pod, 该 hook
// 在 cmd/gateway/main.go 才接 hubReadSyncPusher). 不接的话 handler 会
// 走 nil-safe 分支静默跳过广播, expectFrame 会超时 fail.
//
// 直接 push 仍然覆盖了 read_sync 在 ws 帧编解码 + 多 conn fan-out 的
// 真实路径, 与 channel_friend_events 中两个尚未 wire 的事件 t.Skip
// 处理风格保持一致 (那两个连 push 入口都没有, 所以只能 Skip; read_sync
// 的 push 入口在 hub 层是齐的, 所以不 Skip).
func TestM4WSReadSync_HappyPath(t *testing.T) {
	env := newM4Env(t)
	cookieA, userA := env.seedUser(944)

	// Two ws conns under the same cookie. Each wsDial returns a separate
	// httptest.Server / Conn, registered into the hub under the same UserID.
	wcA1 := wsDial(t, env, cookieA)
	wcA2 := wsDial(t, env, cookieA)
	time.Sleep(settleDelay)

	// C012 P-D: channelID is TEXT (string) post-migration.
	const channelID = "7777"
	const readSeq int64 = 42
	env.hub.PushToUser(userA, gateway.TypeReadSync, gateway.ReadSyncPayload{
		ChannelID: channelID,
		ReadSeq:   readSeq,
	})

	// Both conns belong to the same user, so both must observe the frame.
	for i, wc := range []*wsClient{wcA1, wcA2} {
		frame := wc.expectFrame(gateway.TypeReadSync, 5*time.Second)
		var p gateway.ReadSyncPayload
		decodePayload(t, frame, &p)
		require.Equal(t, channelID, p.ChannelID, "read_sync channel_id (conn #%d)", i+1)
		require.Equal(t, readSeq, p.ReadSeq, "read_sync read_seq (conn #%d)", i+1)
	}
}

// TestM4WSPong_HappyPath — heartbeat tick (15s) drives the server to push
// a pong frame carrying ServerTime (unix ms). Slow test on purpose: the
// 18s deadline accounts for tick scheduling jitter on a loaded CI box.
//
// runHeartbeat is started by the ws_handler immediately after auth, so no
// client action is required to trigger the first tick.
func TestM4WSPong_HappyPath(t *testing.T) {
	env := newM4Env(t)
	cookieA, _ := env.seedUser(945)

	wc := wsDial(t, env, cookieA)
	// heartbeatInterval = 15s (gateway/heartbeat.go:11). 18s allowance
	// covers tick scheduling jitter without padding the suite excessively.
	frame := wc.expectFrame(gateway.TypePong, 18*time.Second)
	var p gateway.PongPayload
	decodePayload(t, frame, &p)
	require.Greater(t, p.ServerTime, int64(0), "pong server_time must be a unix ms")
}

// TestM4WSPushACK_HappyPath — push_ack is a client→server-only frame: the
// server (ws_handler.handlePushACK) consumes it to resolve the global ACK
// registry and never replies. The happy path is therefore "send and the
// conn is still usable" — verified via a follow-up send → send_ack round
// trip. A panic / silent close on an unknown push_id would fail this.
//
// We send a synthetic push_id that no producer is waiting on; the handler
// must tolerate it (resolve is a no-op when the id is unknown).
func TestM4WSPushACK_HappyPath(t *testing.T) {
	env := newM4Env(t)
	cookieA, _ := env.seedUser(946)
	_, userB := env.seedUser(947)
	channelID := env.seedDM(cookieA, userB)

	wc := wsDial(t, env, cookieA)
	time.Sleep(settleDelay)

	// Send a push_ack with a fabricated push_id. handlePushACK is silent
	// on unknown ids (globalACKRegistry.resolve is a no-op miss), so the
	// only externally-observable signal is "conn still works".
	wc.writeFrame(gateway.TypePushACK, gateway.PushACKPayload{PushID: "fake-id-001"})

	// Round-trip: send a normal chat frame and expect the server's ack.
	// If push_ack had taken the conn down (panic, write deadline, etc.)
	// this would either timeout or fail to dial.
	wc.writeFrame(gateway.TypeSend, gateway.SendPayload{
		ClientMsgID: "after-ack",
		ChannelID:   channelID,
		Content:     "still alive",
		MsgType:     1,
	})
	ack := wc.expectFrame(gateway.TypeSendACK, 5*time.Second)
	var p gateway.SendACKPayload
	decodePayload(t, ack, &p)
	require.Equal(t, "after-ack", p.ClientMsgID, "send_ack client_msg_id must echo")
	require.Equal(t, channelID, p.ChannelID, "send_ack channel_id must match")
	require.Greater(t, p.ServerMsgID, int64(0), "send_ack server_msg_id")
	require.Greater(t, p.Seq, int64(0), "send_ack seq")
}

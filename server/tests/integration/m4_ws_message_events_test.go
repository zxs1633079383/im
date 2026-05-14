//go:build integration

package integration

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"im-server/internal/gateway"
	"im-server/internal/middleware"
)

// D1 — WS message events family. Three server→client push events that fire
// when a peer mutates a shared channel's message state through HTTP:
//
//   - push_msg     (POST   /api/channels/:id/messages)
//   - msg_updated  (PATCH  /api/messages/:id)
//   - msg_deleted  (DELETE /api/messages/:id)
//
// Each test:
//   1. seeds two users (A listener, B trigger) and a shared DM channel,
//   2. dials A's WS conn,
//   3. waits ~100ms for ws register + Redis routing write to settle,
//   4. has B issue the HTTP mutation,
//   5. asserts A receives the expected WS frame within 5s.
//
// Seed range 910-919 is reserved for D1 to avoid colliding with the WS
// fixture reference test (900-901) and the other Batch-D agents.

// settleDelay gives the WS handler time to register the new conn in
// gateway.Hub and (for production) write the routing entry into Redis before
// the HTTP-triggered fan-out attempts a hub.PushToUser lookup. 100ms is
// empirically enough for the single-pod harness without padding the suite.
const settleDelay = 100 * time.Millisecond

// TestM4WSPushMsg_HappyPath — A 监听 WS, B HTTP 发消息到共享 DM,
// A 收到 push_msg 帧并 carry 正确 channel_id / content / sender_id.
func TestM4WSPushMsg_HappyPath(t *testing.T) {
	env := newM4Env(t)
	cookieA, _ := env.seedUser(910)
	cookieB, peerB := env.seedUser(911)
	channelID := env.seedDM(cookieA, peerB)

	wcA := wsDial(t, env, cookieA)
	time.Sleep(settleDelay)

	env.expect.POST("/api/channels/"+channelID+"/messages").
		WithHeader(middleware.MMCookieHeader, cookieB).
		WithJSON(map[string]any{"content": "hi-from-B", "msg_type": 1}).
		Expect().Status(201)

	frame := wcA.expectFrame(gateway.TypePushMsg, 5*time.Second)
	var p gateway.PushMsgPayload
	decodePayload(t, frame, &p)
	require.Equal(t, channelID, p.ChannelID, "push_msg channel_id")
	require.Equal(t, "hi-from-B", p.Content, "push_msg content")
	require.Equal(t, peerB, p.SenderID, "push_msg sender_id must be B")
	require.Greater(t, p.Seq, int64(0), "push_msg seq")
	require.Greater(t, p.ServerID, int64(0), "push_msg server_msg_id")
}

// TestM4WSMsgUpdated_HappyPath — B PATCH 自己发的消息, A 收到 msg_updated
// 帧并 carry 更新后的 content.
func TestM4WSMsgUpdated_HappyPath(t *testing.T) {
	env := newM4Env(t)
	cookieA, _ := env.seedUser(912)
	cookieB, peerB := env.seedUser(913)
	channelID := env.seedDM(cookieA, peerB)
	msg := env.seedMessage(channelID, peerB, "before-edit")

	wcA := wsDial(t, env, cookieA)
	time.Sleep(settleDelay)

	env.expect.PATCH("/api/messages/"+msg.ID).
		WithHeader(middleware.MMCookieHeader, cookieB).
		WithJSON(map[string]any{"content": "after-edit"}).
		Expect().Status(200)

	frame := wcA.expectFrame(gateway.TypeMsgUpdated, 5*time.Second)
	// msg_updated payload is the full repo.Message snapshot. Decode into a
	// generic map so the test stays decoupled from struct field naming.
	var snap map[string]any
	decodePayload(t, frame, &snap)
	require.Equal(t, msg.ID, snap["id"], "msg_updated id")
	require.Equal(t, channelID, snap["channel_id"], "msg_updated channel_id")
	require.Equal(t, "after-edit", snap["content"], "msg_updated content")
	require.Equal(t, peerB, snap["sender_id"], "msg_updated sender_id")
}

// TestM4WSMsgDeleted_HappyPath — B DELETE 自己发的消息, A 收到 msg_deleted
// 帧并 carry msg_id + channel_id (handler 实际 payload shape, 见
// internal/http/message.go:446).
func TestM4WSMsgDeleted_HappyPath(t *testing.T) {
	env := newM4Env(t)
	cookieA, _ := env.seedUser(914)
	cookieB, peerB := env.seedUser(915)
	channelID := env.seedDM(cookieA, peerB)
	msg := env.seedMessage(channelID, peerB, "to-revoke")

	wcA := wsDial(t, env, cookieA)
	time.Sleep(settleDelay)

	env.expect.DELETE("/api/messages/"+msg.ID).
		WithHeader(middleware.MMCookieHeader, cookieB).
		Expect().Status(200)

	frame := wcA.expectFrame(gateway.TypeMsgDeleted, 5*time.Second)
	var p map[string]any
	decodePayload(t, frame, &p)
	require.Equal(t, msg.ID, p["msg_id"], "msg_deleted msg_id")
	require.Equal(t, channelID, p["channel_id"], "msg_deleted channel_id")
	// deleted_at is a timestamp string emitted by the handler — assert
	// presence rather than exact value (clock-dependent).
	require.NotEmpty(t, p["deleted_at"], "msg_deleted deleted_at must be set")
}

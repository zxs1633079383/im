//go:build integration

package integration

// Batch-D channel + friend WS events — server→client push frames the M4
// gateway emits when:
//
//   - a user is added to a group channel              → channel_event
//   - a friend request is sent to the user            → friend_event
//   - channel meta (notice / purpose / orient) flips  → channel_info_updated  (declared, NOT yet emitted)
//   - caller pins / unpins a channel for themselves   → channel_top_updated   (declared, NOT yet emitted)
//
// channel_info_updated and channel_top_updated are declared on
// internal/gateway/types.go (lines 59-65) but no handler currently emits
// them — `grep -rn "TypeChannelInfoUpdated\|TypeChannelTopUpdated"` returns
// only the const declarations themselves. Until the channel governance
// PATCH handler grows the matching push hook, both happy paths stay
// behind a t.Skip stub so coverage tracking sees the test name.
//
// Seed range 920-929 is reserved for D2.

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"im-server/internal/gateway"
	"im-server/internal/middleware"
)

// TestM4WSChannelEvent_HappyPath — A is online via WS; B (owner) adds A to
// a group via POST /api/channels/:id/members; A receives a channel_event
// frame carrying eventType="added", channel_id, and name.
//
// Wire path: imhttp.RegisterChannelRoutes → ChannelEventPusher.
// PushChannelEvent → harness localChannelEventPusher → hub.PushToUser →
// A's WS conn (TypeChannelEvent + ChannelEventPayload).
func TestM4WSChannelEvent_HappyPath(t *testing.T) {
	env := newM4Env(t)
	cookieA, userA := env.seedUser(920)
	cookieB, _ := env.seedUser(921)

	// B creates a group with no other members, then will add A — so A is
	// definitively the recipient of the push, not part of the create
	// fan-out (which fires before A's WS connects).
	chID := env.seedGroup(cookieB, "wsd-channel-event-happy")

	wcA := wsDial(t, env, cookieA)
	time.Sleep(100 * time.Millisecond)

	successBody(env.expect.POST("/api/channels/"+pathInt64s(chID)+"/members").
		WithHeader(middleware.MMCookieHeader, cookieB).
		WithJSON(map[string]any{"user_id": userA}).
		Expect().Status(201)).
		Value("status").IsEqual("added")

	frame := wcA.expectFrame(gateway.TypeChannelEvent, 5*time.Second)
	var p gateway.ChannelEventPayload
	decodePayload(t, frame, &p)
	require.Equal(t, "added", p.EventType, "channel_event must carry event_type=added")
	require.Equal(t, chID, p.ChannelID, "channel_event channel_id must match")
	require.Equal(t, "wsd-channel-event-happy", p.Name, "channel_event name must match")
}

// TestM4WSFriendEvent_HappyPath — A is online via WS; B sends a friend
// request to A via POST /api/friends/request; A receives a friend_event
// frame carrying event_type="request" and from_user_id=B.
//
// Wire path: imhttp.RegisterFriendRoutes → FriendEventPusher.PushFriendEvent
// → harness localFriendEventPusher → hub.PushToUser → A's WS conn
// (TypeFriendEvent + FriendEventPayload).
func TestM4WSFriendEvent_HappyPath(t *testing.T) {
	env := newM4Env(t)
	cookieA, userA := env.seedUser(922)
	cookieB, userB := env.seedUser(923)

	wcA := wsDial(t, env, cookieA)
	time.Sleep(100 * time.Millisecond)

	env.expect.POST("/api/friends/request").
		WithHeader(middleware.MMCookieHeader, cookieB).
		WithJSON(map[string]any{"addressee_id": userA}).
		Expect().Status(201)

	frame := wcA.expectFrame(gateway.TypeFriendEvent, 5*time.Second)
	var p gateway.FriendEventPayload
	decodePayload(t, frame, &p)
	require.Equal(t, "request", p.EventType, "friend_event must carry event_type=request")
	require.Equal(t, userB, p.FromUserID, "friend_event from_user_id must be requester")
	_ = userA // kept for symmetry / readability; recipient identity already asserted by frame delivery.
}

// TestM4WSChannelInfoUpdated_HappyPath — declared but not yet wired.
//
// gateway.TypeChannelInfoUpdated ("channel_info_updated") is reserved on
// internal/gateway/types.go:64 for the v0.7 channel-governance push
// (notice / purpose / orient / permission flips). Today PATCH
// /api/channels/:id only mutates the row and returns 200 — no
// hub.PushToUser fan-out happens, so wiring a happy-path assertion would
// hang at expectFrame. Skip until the handler gains the push hook.
//
// Reproduce the absence:
//
//	grep -rn 'TypeChannelInfoUpdated' --include='*.go' .
//	# only the const declaration on types.go matches.
func TestM4WSChannelInfoUpdated_HappyPath(t *testing.T) {
	t.Skip("channel_info_updated declared on gateway/types.go:64 but not yet emitted by any handler; PATCH /api/channels/:id only mutates and returns 200 — no hub.PushToUser. Re-enable once the channel-governance push hook lands.")
}

// TestM4WSChannelTopUpdated_HappyPath — declared but not yet wired.
//
// gateway.TypeChannelTopUpdated ("channel_top_updated") is reserved on
// internal/gateway/types.go:61 for the v0.7 per-user channel-pin push.
// Today PATCH /api/channels/:id/members/:user_id with {"is_top": true}
// updates channel_members.is_top and returns {"status":"updated"} — no
// hub.PushToUser fan-out happens. Skip until the governance handler
// gains the matching push hook.
//
// Reproduce the absence:
//
//	grep -rn 'TypeChannelTopUpdated' --include='*.go' .
//	# only the const declaration on types.go matches.
func TestM4WSChannelTopUpdated_HappyPath(t *testing.T) {
	t.Skip("channel_top_updated declared on gateway/types.go:61 but not yet emitted by any handler; PATCH /api/channels/:id/members/:user_id only mutates and returns 200 — no hub.PushToUser. Re-enable once the per-user pin push hook lands.")
}

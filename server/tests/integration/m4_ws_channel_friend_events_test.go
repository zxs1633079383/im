//go:build integration

package integration

// Batch-D channel + friend WS events — server→client push frames the M4
// gateway emits when:
//
//   - a user is added to a group channel              → channel_event
//   - a friend request is sent to the user            → friend_event
//   - channel meta (notice / purpose / orient) flips  → channel_info_updated
//   - caller pins / unpins a channel for themselves   → channel_top_updated
//
// channel_info_updated and channel_top_updated push hooks landed in the
// channel-governance handler on 2026-05-07; both tests now exercise the
// real wire path end-to-end (no more t.Skip).
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

// TestM4WSChannelInfoUpdated_HappyPath — A is a member of a group; owner B
// PATCHes /api/channels/:id to flip the notice; A receives a
// channel_info_updated frame carrying the refreshed channel snapshot.
//
// Wire path: imhttp.RegisterChannelGovernanceRoutes → broadcaster
// (MessageEventBroadcaster) → harness localBroadcaster → hub.PushToUser →
// A's WS conn (TypeChannelInfoUpdated + repo.Channel JSON).
func TestM4WSChannelInfoUpdated_HappyPath(t *testing.T) {
	env := newM4Env(t)
	cookieA, userA := env.seedUser(924)
	cookieB, _ := env.seedUser(925)
	chID := env.seedGroup(cookieB, "wsd-info-updated", userA)

	wcA := wsDial(t, env, cookieA)
	time.Sleep(100 * time.Millisecond)

	env.expect.PATCH("/api/channels/" + pathInt64s(chID)).
		WithHeader(middleware.MMCookieHeader, cookieB).
		WithJSON(map[string]any{"notice": "after-patch"}).
		Expect().Status(200)

	frame := wcA.expectFrame(gateway.TypeChannelInfoUpdated, 5*time.Second)
	var snap map[string]any
	decodePayload(t, frame, &snap)
	require.Equal(t, chID, snap["id"], "channel_info_updated id must match")
	require.Equal(t, "after-patch", snap["notice"], "channel_info_updated must carry new notice")
}

// TestM4WSChannelTopUpdated_HappyPath — caller pins their own membership row
// (PATCH /api/channels/:id/members/:user_id with is_top=true); a parallel
// WS conn of the same user receives a channel_top_updated frame for
// multi-device sidebar sync.
//
// Wire path: imhttp.RegisterChannelGovernanceRoutes → userPusher
// (UserEventPusher) → harness localUserEventPusher → hub.PushToUser →
// caller's other WS conn (TypeChannelTopUpdated + channelTopPayload).
func TestM4WSChannelTopUpdated_HappyPath(t *testing.T) {
	env := newM4Env(t)
	cookie, userID := env.seedUser(926)
	_, peer := env.seedUser(927)
	chID := env.seedGroup(cookie, "wsd-top-updated", peer)

	// Two WS conns for the same user (different devices). The PATCH below
	// is initiated via HTTP (acts like "device 1"); the listener (wcA2) is
	// the second device that should observe the sync.
	wcA2 := wsDial(t, env, cookie)
	time.Sleep(100 * time.Millisecond)

	env.expect.PATCH("/api/channels/"+pathInt64s(chID)+"/members/"+userID).
		WithHeader(middleware.MMCookieHeader, cookie).
		WithJSON(map[string]any{"is_top": true}).
		Expect().Status(200)

	frame := wcA2.expectFrame(gateway.TypeChannelTopUpdated, 5*time.Second)
	var p struct {
		ChannelID int64 `json:"channel_id"`
		IsTop     bool  `json:"is_top"`
	}
	decodePayload(t, frame, &p)
	require.Equal(t, chID, p.ChannelID, "channel_top_updated channel_id")
	require.True(t, p.IsTop, "channel_top_updated is_top must be true")
}

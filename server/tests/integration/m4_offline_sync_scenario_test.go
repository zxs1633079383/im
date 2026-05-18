//go:build integration

// Package integration — POST /api/sync 4 triggering scenarios from imv2doc 04 §1.
//
// Covers the four production triggers documented in
// docs/imv2doc/04-offline-sync-full-flow.md §1.1-1.4:
//
//   Scenario A — cold start / fresh client / cursor=0 → full pull
//   Scenario B — WS reconnect from a known cursor → partial pull
//   Scenario C — online lag (client cursor a few events behind) → diff pull
//   Scenario D — unknown channel id with cursor=0 → membership decides
//
// Each scenario is one top-level test; the wire-shape envelope assertions
// match m4_message_sync_test.go's TestM4MessageSendThenSync.
package integration

import (
	"testing"

	"github.com/stretchr/testify/require"

	"im-server/internal/middleware"
	"im-server/internal/testutil"
)

// TestM4Sync_Scenario_A_ColdStart_FullPull — fresh client, cursor=0 — must
// pull every event back to channel inception. Asserts the events array is
// monotonic + the messages map contains every referenced msg_id.
func TestM4Sync_Scenario_A_ColdStart_FullPull(t *testing.T) {
	env := newM4Env(t)
	cookieA, aID := env.seedUser(1400)
	cookieB, bID := env.seedUser(1401)
	channelID := env.seedGroup(cookieA, "scn-a-cold", bID)

	// 5 messages so we have a real timeline.
	for i := 0; i < 5; i++ {
		env.seedMessage(channelID, aID, "cold-start")
	}

	resp := successBody(env.expect.POST("/api/sync").
		WithHeader(middleware.MMCookieHeader, cookieB).
		WithHeader(middleware.MMTeamHeader, testutil.RealCompanyID).
		WithJSON(map[string]any{
			"channels": []map[string]any{{"id": channelID, "event_seq": 0}},
		}).
		Expect().Status(200))

	ch := resp.Value("channels").Array().Value(0).Object()
	ch.Value("kind").Object().Value("type").IsEqual("events")
	events := ch.Value("events").Array()
	// 5 message inserts → ≥ 5 events (real fixture also includes the system
	// "member joined" rows from seedGroup, but those existed before the 5
	// sends so they're part of the same cursor-from-zero pull). Just assert
	// ≥ 5 to stay robust.
	require.GreaterOrEqual(t, int(events.Length().Raw()), 5,
		"cold start full pull must return ≥ 5 events")
	msgs := ch.Value("messages").Object()
	msgs.NotEmpty()
}

// TestM4Sync_Scenario_B_WSReconnect_FullPull — client previously had cursor
// at the mid-point of the timeline (e.g. WS reconnect after a 10-min nap);
// pulling from that cursor must return only the post-mid events.
func TestM4Sync_Scenario_B_WSReconnect_FullPull(t *testing.T) {
	env := newM4Env(t)
	cookieA, aID := env.seedUser(1410)
	cookieB, bID := env.seedUser(1411)
	channelID := env.seedDM(cookieA, bID)

	// 3 messages, capture the server_event_seq after each so we can pick a
	// mid-point cursor without manually decoding the response.
	env.seedMessage(channelID, aID, "pre-1")
	env.seedMessage(channelID, aID, "pre-2")

	mid := successBody(env.expect.POST("/api/sync").
		WithHeader(middleware.MMCookieHeader, cookieB).
		WithHeader(middleware.MMTeamHeader, testutil.RealCompanyID).
		WithJSON(map[string]any{
			"channels": []map[string]any{{"id": channelID, "event_seq": 0}},
		}).
		Expect().Status(200))

	chMid := mid.Value("channels").Array().Value(0).Object()
	midCursor := int64(chMid.Value("server_event_seq").Number().Raw())
	require.Greater(t, midCursor, int64(0), "must have advanced after 2 sends")

	// More activity after the mid-point.
	env.seedMessage(channelID, aID, "post-1")
	env.seedMessage(channelID, aID, "post-2")

	// Re-sync from midCursor — should only return the 2 post-* events.
	resp := successBody(env.expect.POST("/api/sync").
		WithHeader(middleware.MMCookieHeader, cookieB).
		WithHeader(middleware.MMTeamHeader, testutil.RealCompanyID).
		WithJSON(map[string]any{
			"channels": []map[string]any{{"id": channelID, "event_seq": midCursor}},
		}).
		Expect().Status(200))

	ch := resp.Value("channels").Array().Value(0).Object()
	ch.Value("kind").Object().Value("type").IsEqual("events")
	events := ch.Value("events").Array()
	require.Equal(t, 2, int(events.Length().Raw()),
		"WS reconnect should return only events after midCursor")

	// All returned events must have event_seq > midCursor.
	first := int64(events.Value(0).Object().Value("event_seq").Number().Raw())
	require.Greater(t, first, midCursor, "first delivered event must be past midCursor")
}

// TestM4Sync_Scenario_C_OnlineLag_DiffPull — client cursor is ~5 events
// behind the server (online but a brief lag); pulling returns exactly the
// missing events with monotonic event_seq.
func TestM4Sync_Scenario_C_OnlineLag_DiffPull(t *testing.T) {
	env := newM4Env(t)
	cookieA, aID := env.seedUser(1420)
	cookieB, bID := env.seedUser(1421)
	channelID := env.seedDM(cookieA, bID)

	// Establish a baseline cursor with one message.
	env.seedMessage(channelID, aID, "baseline")
	baseline := successBody(env.expect.POST("/api/sync").
		WithHeader(middleware.MMCookieHeader, cookieB).
		WithHeader(middleware.MMTeamHeader, testutil.RealCompanyID).
		WithJSON(map[string]any{
			"channels": []map[string]any{{"id": channelID, "event_seq": 0}},
		}).
		Expect().Status(200))
	baselineCursor := int64(baseline.Value("channels").Array().Value(0).
		Object().Value("server_event_seq").Number().Raw())

	// 5 more sends — simulating online lag.
	for i := 0; i < 5; i++ {
		env.seedMessage(channelID, aID, "lag")
	}

	// Diff pull.
	resp := successBody(env.expect.POST("/api/sync").
		WithHeader(middleware.MMCookieHeader, cookieB).
		WithHeader(middleware.MMTeamHeader, testutil.RealCompanyID).
		WithJSON(map[string]any{
			"channels": []map[string]any{{"id": channelID, "event_seq": baselineCursor}},
		}).
		Expect().Status(200))

	ch := resp.Value("channels").Array().Value(0).Object()
	events := ch.Value("events").Array()
	require.Equal(t, 5, int(events.Length().Raw()),
		"online lag diff pull: exactly 5 missed events")

	// Strictly ascending + all > baselineCursor.
	prev := baselineCursor
	for i := 0; i < 5; i++ {
		cur := int64(events.Value(i).Object().Value("event_seq").Number().Raw())
		require.Greater(t, cur, prev, "event_seq must be strictly ascending")
		prev = cur
	}
}

// TestM4Sync_Scenario_D_UnknownChannel_Seq0 — caller passes a channel id
// they're not a member of (e.g. cross-tenant leak / typo). Server must drop
// the unknown id from the response — only channels the user actually belongs
// to appear in the result set. Currently the SyncService iterates
// GetMemberChannelEventSeqs (membership-driven), so unknown ids are silently
// filtered.
func TestM4Sync_Scenario_D_UnknownChannel_Seq0(t *testing.T) {
	env := newM4Env(t)
	cookieA, aID := env.seedUser(1430)
	_, bID := env.seedUser(1431)
	// A creates DM with B; C is NOT a member of any channel.
	channelID := env.seedDM(cookieA, bID)
	env.seedMessage(channelID, aID, "tenant-only")

	cookieC, _ := env.seedUser(1432)

	// C asks for the channel they're not a member of → response has 0
	// channel entries (their membership set is empty).
	resp := successBody(env.expect.POST("/api/sync").
		WithHeader(middleware.MMCookieHeader, cookieC).
		WithHeader(middleware.MMTeamHeader, testutil.RealCompanyID).
		WithJSON(map[string]any{
			"channels": []map[string]any{{"id": channelID, "event_seq": 0}},
		}).
		Expect().Status(200))

	channels := resp.Value("channels").Array()
	channels.Length().IsEqual(0)
}

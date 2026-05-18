//go:build integration

// Package integration — POST /api/sync SyncEntryKind 4-branch coverage.
//
// Covers the 4 wire-shape kinds documented in C019 §3.1 / imv2doc 04 §3.3:
//
//   kind.type ∈ "empty" | "events" | "slice" | "too_long"
//
// Each branch is exercised by an independent top-level test (so a failure
// pinpoints the exact decision-tree leaf in service.SyncService.fillDeltaPayload):
//
//   - Kind_Empty:           client cursor caught up to server → no events
//   - Kind_Events_Multi:    cursor < server, gap ≤ EventLimitPerChannel
//   - Kind_Slice_Saturated: gap ≥ EventLimitPerChannel (=200) → slice + NextCursor
//   - Kind_TooLong_Reset:   gap > EventTooLongThreshold (=10000) → reset_to=server
//
// All four tests share the same wire-shape assertion pattern: drill through
// successBody → data.channels[0] → kind.{type, reset_to?}, NextCursor, events.
package integration

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"gorm.io/gorm"

	"im-server/internal/middleware"
	"im-server/internal/repo"
	"im-server/internal/testutil"
)

// TestM4Sync_Kind_Empty — client cursor already at-or-past server_event_seq
// for the channel; expect kind.type="empty", events absent, server_event_seq
// echoed back so the client can confirm sync state.
//
// Setup: A creates DM with B + sends 1 message (advances seq + event log
// once). B calls /api/sync with cursor=server_event_seq → kind=empty.
func TestM4Sync_Kind_Empty(t *testing.T) {
	env := newM4Env(t)
	cookieA, _ := env.seedUser(1300)
	_, bID := env.seedUser(1301)
	channelID := env.seedDM(cookieA, bID)
	env.seedMessage(channelID, testutil.HexUserID(1300), "advance-the-cursor")

	// First call: cursor=0 → fetch the events so we learn server_event_seq.
	prime := successBody(env.expect.POST("/api/sync").
		WithHeader(middleware.MMCookieHeader, cookieA).
		WithHeader(middleware.MMTeamHeader, testutil.RealCompanyID).
		WithJSON(map[string]any{
			"channels": []map[string]any{{"id": channelID, "event_seq": 0}},
		}).
		Expect().Status(200))

	chFirst := prime.Value("channels").Array().Value(0).Object()
	serverSeq := chFirst.Value("server_event_seq").Number().Raw()
	require.Greater(t, serverSeq, float64(0), "server_event_seq must advance after a send")

	// Second call: cursor=server_event_seq → no gap, kind=empty.
	caught := successBody(env.expect.POST("/api/sync").
		WithHeader(middleware.MMCookieHeader, cookieA).
		WithHeader(middleware.MMTeamHeader, testutil.RealCompanyID).
		WithJSON(map[string]any{
			"channels": []map[string]any{{"id": channelID, "event_seq": int64(serverSeq)}},
		}).
		Expect().Status(200))

	chSecond := caught.Value("channels").Array().Value(0).Object()
	chSecond.Value("kind").Object().Value("type").IsEqual("empty")
	chSecond.Value("server_event_seq").Number().IsEqual(serverSeq)
	// Empty kind must omit events + next_cursor — assert via Path().
	chSecond.NotContainsKey("next_cursor")
}

// TestM4Sync_Kind_Events_Multi — 3 messages on a fresh DM, client cursor=0;
// gap is well within EventLimitPerChannel (200), so kind=events and the
// channel_event rows are delivered as an ASC array.
func TestM4Sync_Kind_Events_Multi(t *testing.T) {
	env := newM4Env(t)
	cookieA, aID := env.seedUser(1310)
	cookieB, bID := env.seedUser(1311)
	channelID := env.seedDM(cookieA, bID)

	// 3 distinct messages → 3 channel_event rows (EventTypeNew).
	env.seedMessage(channelID, aID, "msg1")
	env.seedMessage(channelID, aID, "msg2")
	env.seedMessage(channelID, aID, "msg3")

	resp := successBody(env.expect.POST("/api/sync").
		WithHeader(middleware.MMCookieHeader, cookieB).
		WithHeader(middleware.MMTeamHeader, testutil.RealCompanyID).
		WithJSON(map[string]any{
			"channels": []map[string]any{{"id": channelID, "event_seq": 0}},
		}).
		Expect().Status(200))

	ch := resp.Value("channels").Array().Value(0).Object()
	ch.Value("id").IsEqual(channelID)
	ch.Value("kind").Object().Value("type").IsEqual("events")
	events := ch.Value("events").Array()
	events.Length().IsEqual(3)
	// Each event must reference a message + carry EventTypeNew (=1).
	events.Value(0).Object().Value("event_type").IsEqual(1)
	events.Value(0).Object().Value("event_seq").Number().Gt(0)
	// event_seq strictly ascending across the array.
	first := events.Value(0).Object().Value("event_seq").Number().Raw()
	second := events.Value(1).Object().Value("event_seq").Number().Raw()
	third := events.Value(2).Object().Value("event_seq").Number().Raw()
	require.Less(t, first, second, "event_seq must be strictly ascending (1<2)")
	require.Less(t, second, third, "event_seq must be strictly ascending (2<3)")
}

// TestM4Sync_Kind_Slice_Saturated — 201 messages on a fresh channel; cursor=0
// returns the first 200 with kind=slice + next_cursor = max(event_seq) of the
// returned slice (continuation cursor for the next /sync call).
//
// Slow but bounded: 201 inserts × ~1ms each + 1 sync round trip ≈ 1s.
func TestM4Sync_Kind_Slice_Saturated(t *testing.T) {
	env := newM4Env(t)
	cookieA, aID := env.seedUser(1320)
	cookieB, bID := env.seedUser(1321)
	channelID := env.seedDM(cookieA, bID)

	// EventLimitPerChannel = 200 → seed 201 to saturate.
	const total = 201
	for i := 0; i < total; i++ {
		env.seedMessage(channelID, aID, "saturate")
	}

	resp := successBody(env.expect.POST("/api/sync").
		WithHeader(middleware.MMCookieHeader, cookieB).
		WithHeader(middleware.MMTeamHeader, testutil.RealCompanyID).
		WithJSON(map[string]any{
			"channels": []map[string]any{{"id": channelID, "event_seq": 0}},
		}).
		Expect().Status(200))

	ch := resp.Value("channels").Array().Value(0).Object()
	ch.Value("kind").Object().Value("type").IsEqual("slice")
	events := ch.Value("events").Array()
	events.Length().IsEqual(200) // EventLimitPerChannel saturation

	// next_cursor must be present and equal to the last delivered event_seq.
	cursor := ch.Value("next_cursor").Number().Raw()
	last := events.Value(199).Object().Value("event_seq").Number().Raw()
	require.Equal(t, last, cursor, "next_cursor == max(events.event_seq)")
}

// TestM4Sync_Kind_TooLong_Reset — synthesise a channel where the
// channel_event_seq exceeds EventTooLongThreshold (=10000) above the client
// cursor. C019 §3.2 mandates kind=too_long{reset_to=server_event_seq}, no
// events / messages — the client must reset local state and re-fetch the
// first screen via /messages.
//
// We don't want to actually INSERT 10000+ messages (slow), so we seed 2
// messages and then bump the per-channel PG sequence via SELECT setval(...)
// so the next AppendEvent fires at seq>10000 (no actual message INSERT
// needed; the gap math reads serverEventSeq from channel_event MAX).
func TestM4Sync_Kind_TooLong_Reset(t *testing.T) {
	env := newM4Env(t)
	cookieA, aID := env.seedUser(1330)
	cookieB, bID := env.seedUser(1331)
	channelID := env.seedDM(cookieA, bID)

	// One real message so channel_event has at least one row to anchor MAX.
	env.seedMessage(channelID, aID, "anchor")

	// Bump the per-channel event sequence past EventTooLongThreshold so the
	// next AppendEvent records event_seq > 10000. The sequence name follows
	// the channel_event.go::CreateChannelSequences convention; sanitizeID
	// strips non [A-Za-z0-9_-] so a hex channel id passes through unchanged.
	seqName := "channel_event_seq_" + channelID
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	require.NoError(t,
		env.db.WithContext(ctx).Exec(
			`SELECT setval('"`+seqName+`"', 10100, true)`,
		).Error,
		"setval bump per-channel event sequence",
	)

	// Append a synthetic channel_event row at seq=10101 directly via repo
	// (bypassing the message write path — we just need the high-water mark
	// to exceed TooLongThreshold).
	channelEventRepo := repo.NewChannelEventRepo(env.db)
	err := env.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		seq, err := channelEventRepo.NextEventSeq(ctx, tx, channelID)
		if err != nil {
			return err
		}
		require.GreaterOrEqual(t, seq, int64(10101), "seq bumped past 10000")
		return channelEventRepo.AppendEvent(ctx, tx, &repo.ChannelEvent{
			ChannelID: channelID,
			EventSeq:  seq,
			EventType: repo.EventTypeNew,
			ActorID:   aID,
			CreatedAt: time.Now().UnixMilli(),
		})
	})
	require.NoError(t, err, "synth event append to push gap > 10000")

	// B has been in the channel since seedDM (cursor=0). Sync with cursor=0
	// → gap > EventTooLongThreshold → kind=too_long, reset_to=server.
	resp := successBody(env.expect.POST("/api/sync").
		WithHeader(middleware.MMCookieHeader, cookieB).
		WithHeader(middleware.MMTeamHeader, testutil.RealCompanyID).
		WithJSON(map[string]any{
			"channels": []map[string]any{{"id": channelID, "event_seq": 0}},
		}).
		Expect().Status(200))

	ch := resp.Value("channels").Array().Value(0).Object()
	kind := ch.Value("kind").Object()
	kind.Value("type").IsEqual("too_long")
	resetTo := kind.Value("reset_to").Number().Raw()
	serverSeq := ch.Value("server_event_seq").Number().Raw()
	require.Equal(t, serverSeq, resetTo, "reset_to == server_event_seq")
	require.Greater(t, serverSeq, float64(10000), "server_event_seq must be past threshold")

	// too_long must NOT carry events / messages — pure protocol signal.
	ch.NotContainsKey("events")
	ch.NotContainsKey("next_cursor")
}

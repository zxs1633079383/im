//go:build integration

// Package integration — POST /api/sync TooLong recovery flow.
//
// Covers the imv2doc 04 §7 "client recovery from too_long" scenario:
//
//   1. Client cursor=0, gap > EventTooLongThreshold (=10000)
//      → server returns kind=too_long{reset_to=N}
//   2. Client clears local state, sets cursor=N
//   3. Client re-calls /sync with cursor=N
//      → kind=empty (because cursor == server)
//
// Step 3 is what proves the recovery contract: after the client adopts
// `reset_to` as their new cursor, the next /sync must NOT loop on too_long
// or surface events from beyond the threshold gap — that would defeat the
// whole purpose of the protocol signal.
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

// TestM4Sync_TooLong_Then_Recover_From_ResetTo — full recovery cycle.
//
// Setup: synthetic gap > EventTooLongThreshold so kind=too_long fires on
// the first call. Verify reset_to == server_event_seq. Re-sync with
// cursor=reset_to and assert kind=empty (zero events).
//
// (Same gap-synth technique as TestM4Sync_Kind_TooLong_Reset — bump the
// per-channel PG sequence rather than INSERT 10000+ real rows.)
func TestM4Sync_TooLong_Then_Recover_From_ResetTo(t *testing.T) {
	env := newM4Env(t)
	cookieA, aID := env.seedUser(1800)
	cookieB, bID := env.seedUser(1801)
	channelID := env.seedDM(cookieA, bID)

	// Anchor message so channel_event has a real row to query MAX from.
	env.seedMessage(channelID, aID, "anchor-before-bump")

	// Bump the sequence past the TooLong threshold (10000) so the next
	// AppendEvent records seq > 10000 → cursor=0 → gap > 10000 → too_long.
	seqName := "channel_event_seq_" + channelID
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	require.NoError(t,
		env.db.WithContext(ctx).Exec(
			`SELECT setval('"`+seqName+`"', 15000, true)`,
		).Error,
		"bump per-channel event sequence past TooLong threshold",
	)

	// Synth a channel_event row at seq=15001 directly via repo so the
	// MAX(event_seq) read at /sync time returns a value > 10000.
	channelEventRepo := repo.NewChannelEventRepo(env.db)
	err := env.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		seq, err := channelEventRepo.NextEventSeq(ctx, tx, channelID)
		if err != nil {
			return err
		}
		return channelEventRepo.AppendEvent(ctx, tx, &repo.ChannelEvent{
			ChannelID: channelID,
			EventSeq:  seq,
			EventType: repo.EventTypeNew,
			ActorID:   aID,
			CreatedAt: time.Now().UnixMilli(),
		})
	})
	require.NoError(t, err, "append synth row to advance server cursor")

	// Step 1 — client cursor=0 (legacy / never-synced) → too_long.
	first := successBody(env.expect.POST("/api/sync").
		WithHeader(middleware.MMCookieHeader, cookieB).
		WithHeader(middleware.MMTeamHeader, testutil.RealCompanyID).
		WithJSON(map[string]any{
			"channels": []map[string]any{{"id": channelID, "event_seq": 0}},
		}).
		Expect().Status(200))

	ch := first.Value("channels").Array().Value(0).Object()
	kind := ch.Value("kind").Object()
	kind.Value("type").IsEqual("too_long")
	resetTo := int64(kind.Value("reset_to").Number().Raw())
	serverSeq := int64(ch.Value("server_event_seq").Number().Raw())
	require.Equal(t, serverSeq, resetTo,
		"reset_to must equal server_event_seq (C019 §3.2)")
	require.Greater(t, serverSeq, int64(10000),
		"server_event_seq must be past TooLong threshold")

	// Step 2 — client adopts resetTo (clears local rows, sets cursor=resetTo).
	// Simulated by passing event_seq=resetTo on the second call.

	// Step 3 — re-sync with cursor=resetTo. cursor >= server → kind=empty.
	second := successBody(env.expect.POST("/api/sync").
		WithHeader(middleware.MMCookieHeader, cookieB).
		WithHeader(middleware.MMTeamHeader, testutil.RealCompanyID).
		WithJSON(map[string]any{
			"channels": []map[string]any{{"id": channelID, "event_seq": resetTo}},
		}).
		Expect().Status(200))

	chRecover := second.Value("channels").Array().Value(0).Object()
	chRecover.Value("kind").Object().Value("type").IsEqual("empty")
	chRecover.Value("server_event_seq").Number().IsEqual(serverSeq)
	// empty kind: no events array, no next_cursor.
	chRecover.NotContainsKey("events")
	chRecover.NotContainsKey("next_cursor")
}

// TestM4Sync_TooLong_NewActivity_AfterRecover — after the TooLong recovery
// cycle, new mutations on the channel must show up as events (kind=events)
// in subsequent /sync calls — proves the cursor is not stuck.
func TestM4Sync_TooLong_NewActivity_AfterRecover(t *testing.T) {
	env := newM4Env(t)
	cookieA, aID := env.seedUser(1810)
	cookieB, bID := env.seedUser(1811)
	channelID := env.seedDM(cookieA, bID)
	env.seedMessage(channelID, aID, "anchor")

	// Push gap past threshold.
	seqName := "channel_event_seq_" + channelID
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	require.NoError(t,
		env.db.WithContext(ctx).Exec(
			`SELECT setval('"`+seqName+`"', 15000, true)`,
		).Error)

	channelEventRepo := repo.NewChannelEventRepo(env.db)
	require.NoError(t, env.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		seq, err := channelEventRepo.NextEventSeq(ctx, tx, channelID)
		if err != nil {
			return err
		}
		return channelEventRepo.AppendEvent(ctx, tx, &repo.ChannelEvent{
			ChannelID: channelID,
			EventSeq:  seq,
			EventType: repo.EventTypeNew,
			ActorID:   aID,
			CreatedAt: time.Now().UnixMilli(),
		})
	}))

	// Step 1: get reset_to.
	first := successBody(env.expect.POST("/api/sync").
		WithHeader(middleware.MMCookieHeader, cookieB).
		WithHeader(middleware.MMTeamHeader, testutil.RealCompanyID).
		WithJSON(map[string]any{
			"channels": []map[string]any{{"id": channelID, "event_seq": 0}},
		}).
		Expect().Status(200))
	resetTo := int64(first.Value("channels").Array().Value(0).Object().
		Value("kind").Object().Value("reset_to").Number().Raw())

	// Step 2: A sends new message via real HTTP — must appear in B's next /sync.
	env.expect.POST("/api/channels/"+channelID+"/messages").
		WithHeader(middleware.MMCookieHeader, cookieA).
		WithHeader(middleware.MMTeamHeader, testutil.RealCompanyID).
		WithJSON(map[string]any{"content": "post-recover", "msg_type": 1}).
		Expect().Status(201)

	// Step 3: B re-syncs from resetTo → kind=events, 1 new event delivered.
	resp := successBody(env.expect.POST("/api/sync").
		WithHeader(middleware.MMCookieHeader, cookieB).
		WithHeader(middleware.MMTeamHeader, testutil.RealCompanyID).
		WithJSON(map[string]any{
			"channels": []map[string]any{{"id": channelID, "event_seq": resetTo}},
		}).
		Expect().Status(200))

	ch := resp.Value("channels").Array().Value(0).Object()
	ch.Value("kind").Object().Value("type").IsEqual("events")
	events := ch.Value("events").Array()
	events.Length().Ge(1)
	// The new event's event_seq must be > resetTo (cursor moved forward).
	first0 := int64(events.Value(0).Object().Value("event_seq").Number().Raw())
	require.Greater(t, first0, resetTo,
		"first new event after recover must be past resetTo cursor")
}

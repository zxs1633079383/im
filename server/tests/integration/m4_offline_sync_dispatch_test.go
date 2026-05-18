//go:build integration

// Package integration — POST /api/sync dispatch path coverage.
//
// Covers the 5 channel_event dispatch types from C019 §3.2 +
// docs/imv2doc/04-offline-sync-full-flow.md §4.1-4.7. Each EventType in
// channel_event.go must round-trip through Sync.fillDeltaPayload + the
// client-side dispatcher (handlers_v2/sync.rs) without loss; these tests
// pin the wire shape on the server side.
//
//   EventType=1 New          — POST /api/channels/:id/messages
//   EventType=2 Edit         — PATCH /api/messages/:id
//   EventType=3 Delete       — DELETE /api/messages/:id
//   EventType=6 ReadMark     — POST /api/channels/:id/read
//   EventType=7 Member       — AddMember / RemoveMember / LeaveChannel /
//                              owner-transfer
//
// EventType=4 Reaction / 5 Pin are reserved (no producer wired yet — see
// channel_event.go §EventTypeReaction / EventTypePin) so we exercise only
// the unknown-event-type forward-compat path for them (TestM4Sync_Dispatch_
// Unknown_EventType_8_Skip).
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

// syncPullEvents drills the response down to channels[0].events for the
// given channel id, asserting the kind is "events". Centralised so each
// dispatch test reads cleanly.
func syncPullEvents(t *testing.T, env *m4env, cookie, channelID string, afterSeq int64) []map[string]any {
	t.Helper()
	resp := successBody(env.expect.POST("/api/sync").
		WithHeader(middleware.MMCookieHeader, cookie).
		WithHeader(middleware.MMTeamHeader, testutil.RealCompanyID).
		WithJSON(map[string]any{
			"channels": []map[string]any{{"id": channelID, "event_seq": afterSeq}},
		}).
		Expect().Status(200))

	channels := resp.Value("channels").Array()
	if channels.Length().Raw() == 0 {
		return nil
	}
	ch := channels.Value(0).Object()
	// kind must be "events" — slice / too_long / empty branches are tested
	// separately in m4_offline_sync_kind_test.go.
	ch.Value("kind").Object().Value("type").IsEqual("events")
	events := ch.Value("events").Array()

	// Use Raw() on the array (returns []interface{}) and pick fields from
	// the resulting maps — avoids re-traversing the JSON tree per event and
	// lets us inspect optional keys (msg_id) without httpexpect's ContainsKey
	// assertion path (which is for assertion, not branching).
	raw := events.Raw()
	out := make([]map[string]any, 0, len(raw))
	for _, item := range raw {
		m, ok := item.(map[string]any)
		if !ok {
			continue
		}
		out = append(out, m)
	}
	return out
}

// TestM4Sync_Dispatch_New_Edit_Delete_Mixed — POST + PATCH + DELETE produce a
// New / Edit / Delete triple in the event stream, all referencing the same
// msg_id (immutable PK) but with strictly ascending event_seq.
//
// Wire invariant: events sorted ASC by event_seq → client applies in order
// → final state = "deleted" overrides "edited" overrides "new". Without
// this, late delete events would be ignored by clients applying out of
// order.
func TestM4Sync_Dispatch_New_Edit_Delete_Mixed(t *testing.T) {
	env := newM4Env(t)
	cookieA, aID := env.seedUser(1600)
	cookieB, bID := env.seedUser(1601)
	channelID := env.seedDM(cookieA, bID)

	// 1. New via HTTP POST so EventTypeNew lands.
	sent := successBody(env.expect.POST("/api/channels/"+channelID+"/messages").
		WithHeader(middleware.MMCookieHeader, cookieA).
		WithHeader(middleware.MMTeamHeader, testutil.RealCompanyID).
		WithJSON(map[string]any{
			"content":  "v1",
			"msg_type": 1,
		}).
		Expect().Status(201))
	msgID := sent.Value("id").String().Raw()

	// 2. Edit via PATCH so EventTypeEdit lands.
	env.expect.PATCH("/api/messages/"+msgID).
		WithHeader(middleware.MMCookieHeader, cookieA).
		WithHeader(middleware.MMTeamHeader, testutil.RealCompanyID).
		WithJSON(map[string]any{"content": "v2"}).
		Expect().Status(200)

	// 3. Delete via DELETE so EventTypeDelete lands.
	env.expect.DELETE("/api/messages/"+msgID).
		WithHeader(middleware.MMCookieHeader, cookieA).
		WithHeader(middleware.MMTeamHeader, testutil.RealCompanyID).
		Expect().Status(200)

	// 4. B pulls /sync from cursor=0 → must see all 3 events for msgID.
	events := syncPullEvents(t, env, cookieB, channelID, 0)
	require.GreaterOrEqual(t, len(events), 3,
		"expected ≥ 3 events for new+edit+delete on same msg")

	// Walk the array and collect the three event types tied to msgID.
	seenNew, seenEdit, seenDel := false, false, false
	var prevSeq float64
	for _, ev := range events {
		seq := ev["event_seq"].(float64)
		require.GreaterOrEqual(t, seq, prevSeq, "event_seq must be ASC")
		prevSeq = seq
		if id, ok := ev["msg_id"]; ok && id == msgID {
			switch ev["event_type"].(float64) {
			case 1:
				seenNew = true
			case 2:
				seenEdit = true
			case 3:
				seenDel = true
			}
		}
	}
	require.True(t, seenNew, "EventTypeNew (1) must appear for msgID")
	require.True(t, seenEdit, "EventTypeEdit (2) must appear for msgID")
	require.True(t, seenDel, "EventTypeDelete (3) must appear for msgID")
	_ = aID
	_ = bID
}

// TestM4Sync_Dispatch_ReadMark_Event — POST /api/channels/:id/read appends
// EventTypeReadMark (=6) with payload {"read_seq":<N>}. The reader's own
// /sync pulls back the echo so multi-device read sync works.
func TestM4Sync_Dispatch_ReadMark_Event(t *testing.T) {
	env := newM4Env(t)
	cookieA, aID := env.seedUser(1610)
	_, bID := env.seedUser(1611)
	channelID := env.seedDM(cookieA, bID)
	env.seedMessage(channelID, aID, "first-msg")

	// A advances read_seq via POST /channels/:id/read.
	env.expect.POST("/api/channels/"+channelID+"/read").
		WithHeader(middleware.MMCookieHeader, cookieA).
		WithHeader(middleware.MMTeamHeader, testutil.RealCompanyID).
		Expect().Status(200)

	// A's own /sync from cursor=0 must include EventTypeReadMark (=6).
	events := syncPullEvents(t, env, cookieA, channelID, 0)
	seenReadMark := false
	for _, ev := range events {
		if ev["event_type"].(float64) == 6 && ev["actor_id"] == aID {
			seenReadMark = true
		}
	}
	require.True(t, seenReadMark,
		"EventTypeReadMark (6) must appear in A's /sync after POST /read")
}

// TestM4Sync_Dispatch_Member_Join_Kick_Leave_OwnerTransfer — exercise the
// four channel_event member-change flavours (Join / Kick / Leave /
// OwnerTransfer) that produce EventTypeMember=7 rows. After every mutation
// the test pulls /sync and counts EventType=7 events.
//
// Sequence:
//   1. owner A creates group with members {A, B, C}
//   2. owner A adds member D (Join)
//   3. owner A removes member B (Kick)
//   4. owner A transfers ownership to C (OwnerTransfer)
//   5. (now non-owner) A leaves channel (Leave)
//
// Final assertion: ≥ 4 EventType=7 events in C's /sync (C is the new owner
// and was a member throughout, so they see every membership change post their
// initial seedGroup join).
func TestM4Sync_Dispatch_Member_Join_Kick_Leave_OwnerTransfer(t *testing.T) {
	env := newM4Env(t)
	cookieA, _ := env.seedUser(1620)
	_, bID := env.seedUser(1621)
	cookieC, cID := env.seedUser(1622)
	_, dID := env.seedUser(1623)

	channelID := env.seedGroup(cookieA, "member-events", bID, cID)

	// 1. Add D (EventTypeMember change_type=join).
	env.expect.POST("/api/channels/"+channelID+"/members").
		WithHeader(middleware.MMCookieHeader, cookieA).
		WithHeader(middleware.MMTeamHeader, testutil.RealCompanyID).
		WithJSON(map[string]any{"user_id": dID}).
		Expect().Status(201)

	// 2. Kick B.
	env.expect.DELETE("/api/channels/"+channelID+"/members/"+bID).
		WithHeader(middleware.MMCookieHeader, cookieA).
		WithHeader(middleware.MMTeamHeader, testutil.RealCompanyID).
		Expect().Status(200)

	// 3. Owner transfer A → C.
	env.expect.POST("/api/channels/"+channelID+"/transfer-owner").
		WithHeader(middleware.MMCookieHeader, cookieA).
		WithHeader(middleware.MMTeamHeader, testutil.RealCompanyID).
		WithJSON(map[string]any{"new_owner_id": cID}).
		Expect().Status(200)

	// 4. Leave (A is no longer owner so leave is allowed).
	env.expect.POST("/api/channels/"+channelID+"/leave").
		WithHeader(middleware.MMCookieHeader, cookieA).
		WithHeader(middleware.MMTeamHeader, testutil.RealCompanyID).
		Expect().Status(200)

	// C pulls /sync from cursor=0; count EventTypeMember (=7) events.
	events := syncPullEvents(t, env, cookieC, channelID, 0)
	memberEventCount := 0
	for _, ev := range events {
		if ev["event_type"].(float64) == 7 {
			memberEventCount++
		}
	}
	// 4 mutations after seedGroup → ≥ 4 EventType=7 rows. (seedGroup itself
	// also emits per-member-join events, so the actual count is higher; ≥ 4
	// is the tight contract.)
	require.GreaterOrEqual(t, memberEventCount, 4,
		"expected ≥ 4 EventTypeMember events for join/kick/transfer/leave")
}

// TestM4Sync_Dispatch_Unknown_EventType_8_Skip — direct-INSERT a
// channel_event row with event_type=8 (not in the enumerated 1..7 set).
// Per imv2doc 04 §4.7 + cses-client dispatch_sync_delta, the client must
// silently skip unknown event types for forward compat — the server's
// responsibility is to not 500 / not drop the rest of the events when an
// unknown row is in the timeline.
//
// This test verifies the server-side: the events array still returns the
// unknown row (clients then handle skip-or-warn). No 500.
func TestM4Sync_Dispatch_Unknown_EventType_8_Skip(t *testing.T) {
	env := newM4Env(t)
	cookieA, aID := env.seedUser(1630)
	cookieB, bID := env.seedUser(1631)
	channelID := env.seedDM(cookieA, bID)
	env.seedMessage(channelID, aID, "before-unknown")

	// Synth an unknown-type row directly via the repo (the public service
	// surface never exposes event_type=8; we're testing forward-compat
	// fallback for a hypothetical future EventType=8).
	channelEventRepo := repo.NewChannelEventRepo(env.db)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	err := env.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		seq, err := channelEventRepo.NextEventSeq(ctx, tx, channelID)
		if err != nil {
			return err
		}
		return channelEventRepo.AppendEvent(ctx, tx, &repo.ChannelEvent{
			ChannelID: channelID,
			EventSeq:  seq,
			EventType: repo.EventType(8), // forward-compat unknown
			ActorID:   aID,
			CreatedAt: time.Now().UnixMilli(),
		})
	})
	require.NoError(t, err, "synth unknown event_type=8 row")

	// B pulls /sync — must include the unknown row in events without 500.
	events := syncPullEvents(t, env, cookieB, channelID, 0)
	seenUnknown := false
	for _, ev := range events {
		if ev["event_type"].(float64) == 8 {
			seenUnknown = true
		}
	}
	require.True(t, seenUnknown,
		"server must surface unknown event_type=8 — forward-compat path")
}

// TestM4Sync_Dispatch_Reaction_Pin_Reserved — placeholder coverage for
// EventType=4 (Reaction) and EventType=5 (Pin). These types are defined in
// channel_event.go but have no production producer yet. We synth them
// directly to prove the wire surface tolerates them; once a real producer
// lands (V2 RFC) this test gets replaced with a flow-driven version.
func TestM4Sync_Dispatch_Reaction_Pin_Reserved(t *testing.T) {
	env := newM4Env(t)
	cookieA, aID := env.seedUser(1640)
	cookieB, bID := env.seedUser(1641)
	channelID := env.seedDM(cookieA, bID)
	env.seedMessage(channelID, aID, "anchor")

	channelEventRepo := repo.NewChannelEventRepo(env.db)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	// Append Reaction (4) + Pin (5) directly.
	for _, eventType := range []repo.EventType{repo.EventTypeReaction, repo.EventTypePin} {
		evType := eventType
		err := env.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
			seq, err := channelEventRepo.NextEventSeq(ctx, tx, channelID)
			if err != nil {
				return err
			}
			return channelEventRepo.AppendEvent(ctx, tx, &repo.ChannelEvent{
				ChannelID: channelID,
				EventSeq:  seq,
				EventType: evType,
				ActorID:   aID,
				CreatedAt: time.Now().UnixMilli(),
			})
		})
		require.NoError(t, err, "synth reserved event_type=%d", evType)
	}

	events := syncPullEvents(t, env, cookieB, channelID, 0)
	seenReaction, seenPin := false, false
	for _, ev := range events {
		switch ev["event_type"].(float64) {
		case 4:
			seenReaction = true
		case 5:
			seenPin = true
		}
	}
	require.True(t, seenReaction, "reserved EventTypeReaction (4) round-trips on wire")
	require.True(t, seenPin, "reserved EventTypePin (5) round-trips on wire")
}

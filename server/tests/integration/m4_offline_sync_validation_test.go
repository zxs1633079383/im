//go:build integration

// Package integration — POST /api/sync boundary / validation / auth cases.
//
// Covers the rejection / silent-skip rules from C019 §3.1 + sync.go HTTP layer:
//
//   Validation: too-many channels (>500), negative event_seq, blank channel
//   id, non-member channel.
//   Auth:       missing cookieId, invalid cookieId.
//
// These tests intentionally exercise paths that should NOT reach the service
// layer — they validate the handler / middleware gating. Each one stands
// alone so a regression is pinpointed to a single check.
package integration

import (
	"strings"
	"testing"

	"im-server/internal/middleware"
	"im-server/internal/testutil"
)

// TestM4Sync_Validation_TooManyChannels — request body with > 500 cursors
// → 400 "too many channels" (service.MaxChannelsPerCall = 500).
func TestM4Sync_Validation_TooManyChannels(t *testing.T) {
	env := newM4Env(t)
	cookie, _ := env.seedUser(1500)

	cursors := make([]map[string]any, 0, 501)
	for i := 0; i < 501; i++ {
		// Use distinct synthetic channel ids — even if they don't exist, the
		// handler must reject the batch BEFORE iterating them.
		cursors = append(cursors, map[string]any{
			"id":        testutil.HexUserID(2000 + i),
			"event_seq": 0,
		})
	}

	resp := errorBody(env.expect.POST("/api/sync").
		WithHeader(middleware.MMCookieHeader, cookie).
		WithHeader(middleware.MMTeamHeader, testutil.RealCompanyID).
		WithJSON(map[string]any{"channels": cursors}).
		Expect().Status(400))

	resp.Value("error").String().NotEmpty()
	// Contract: error message contains "too many channels" per
	// internal/http/sync.go:86.
	errStr := resp.Value("error").String().Raw()
	if !strings.Contains(errStr, "too many channels") {
		t.Fatalf("expect 'too many channels' in error msg; got %q", errStr)
	}
}

// TestM4Sync_Validation_NegativeEventSeq — event_seq < 0 is nonsensical;
// service treats it as "fetch everything > -1" so the handler today returns
// 200 with events (same as cursor=0). This test pins the current accepted
// behaviour so any future tightening (handler-side validation) is a
// conscious wire change.
//
// Wire shape: negative cursor silently behaves as cursor=0 because
// channel_event.event_seq starts at 1 (PG sequence); WHERE event_seq > -1
// returns the whole tail.
func TestM4Sync_Validation_NegativeEventSeq(t *testing.T) {
	env := newM4Env(t)
	cookieA, aID := env.seedUser(1510)
	cookieB, bID := env.seedUser(1511)
	channelID := env.seedDM(cookieA, bID)
	env.seedMessage(channelID, aID, "neg-cursor")

	resp := successBody(env.expect.POST("/api/sync").
		WithHeader(middleware.MMCookieHeader, cookieB).
		WithHeader(middleware.MMTeamHeader, testutil.RealCompanyID).
		WithJSON(map[string]any{
			"channels": []map[string]any{{"id": channelID, "event_seq": -1}},
		}).
		Expect().Status(200))

	ch := resp.Value("channels").Array().Value(0).Object()
	// Behaviour: negative cursor → events kind (same as cursor=0).
	ch.Value("kind").Object().Value("type").IsEqual("events")
	ch.Value("events").Array().Length().Ge(1)
}

// TestM4Sync_Validation_MissingChannelID — empty string id is filtered out
// by the membership-driven loop (no membership row has channel_id="" so it
// silently drops). Verify the call still succeeds with the user's actual
// channels in the response.
func TestM4Sync_Validation_MissingChannelID(t *testing.T) {
	env := newM4Env(t)
	cookieA, aID := env.seedUser(1520)
	_, bID := env.seedUser(1521)
	channelID := env.seedDM(cookieA, bID)
	env.seedMessage(channelID, aID, "real-channel")

	// Mix one valid id with one empty string id; the empty one is silently
	// dropped (membership-driven), and the response still contains the
	// caller's real channels.
	resp := successBody(env.expect.POST("/api/sync").
		WithHeader(middleware.MMCookieHeader, cookieA).
		WithHeader(middleware.MMTeamHeader, testutil.RealCompanyID).
		WithJSON(map[string]any{
			"channels": []map[string]any{
				{"id": "", "event_seq": 0},
				{"id": channelID, "event_seq": 0},
			},
		}).
		Expect().Status(200))

	channels := resp.Value("channels").Array()
	// At least the real one is present.
	channels.Length().Ge(1)
}

// TestM4Sync_Validation_NotMemberChannel — caller passes a channel id they
// have no membership for; same drop-silently semantics as Scenario D —
// channel entry simply omitted from the response.
//
// (Distinct from Scenario D in that we use a real existing channel id, not
// an invented one — proves the filter is membership-based, not "channel
// exists".)
func TestM4Sync_Validation_NotMemberChannel(t *testing.T) {
	env := newM4Env(t)
	cookieA, aID := env.seedUser(1530)
	_, bID := env.seedUser(1531)
	cookieC, _ := env.seedUser(1532)
	channelID := env.seedDM(cookieA, bID)
	env.seedMessage(channelID, aID, "private-dm")

	// C is not in this DM. Asking for it returns 0 channels.
	resp := successBody(env.expect.POST("/api/sync").
		WithHeader(middleware.MMCookieHeader, cookieC).
		WithHeader(middleware.MMTeamHeader, testutil.RealCompanyID).
		WithJSON(map[string]any{
			"channels": []map[string]any{{"id": channelID, "event_seq": 0}},
		}).
		Expect().Status(200))

	resp.Value("channels").Array().Length().IsEqual(0)
}

// TestM4Sync_Auth_CookieMissing — no cookieId header at all → 401 from
// middleware.CookieRequired (the handler never executes).
func TestM4Sync_Auth_CookieMissing(t *testing.T) {
	env := newM4Env(t)
	cookieA, _ := env.seedUser(1540)
	_, bID := env.seedUser(1541)
	channelID := env.seedDM(cookieA, bID)

	env.expect.POST("/api/sync").
		WithHeader(middleware.MMTeamHeader, testutil.RealCompanyID).
		WithJSON(map[string]any{
			"channels": []map[string]any{{"id": channelID, "event_seq": 0}},
		}).
		Expect().Status(401)
}

// TestM4Sync_Auth_CookieInvalid — cookieId points at a Redis key that
// doesn't exist (CookieFixture not called for this id) → 401.
func TestM4Sync_Auth_CookieInvalid(t *testing.T) {
	env := newM4Env(t)
	cookieA, _ := env.seedUser(1550)
	_, bID := env.seedUser(1551)
	channelID := env.seedDM(cookieA, bID)

	bogus := testutil.MakeCookieID(9999)
	env.expect.POST("/api/sync").
		WithHeader(middleware.MMCookieHeader, bogus).
		WithHeader(middleware.MMTeamHeader, testutil.RealCompanyID).
		WithJSON(map[string]any{
			"channels": []map[string]any{{"id": channelID, "event_seq": 0}},
		}).
		Expect().Status(401)
}

// TestM4Sync_Validation_InvalidJSON — body is malformed JSON → 400
// "invalid JSON" from c.ShouldBindJSON path.
func TestM4Sync_Validation_InvalidJSON(t *testing.T) {
	env := newM4Env(t)
	cookie, _ := env.seedUser(1560)

	env.expect.POST("/api/sync").
		WithHeader(middleware.MMCookieHeader, cookie).
		WithHeader(middleware.MMTeamHeader, testutil.RealCompanyID).
		WithHeader("Content-Type", "application/json").
		WithBytes([]byte("{not valid json")).
		Expect().Status(400)
}

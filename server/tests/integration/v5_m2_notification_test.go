//go:build integration

package integration

import (
	"fmt"
	"testing"
)

// TestM2_NotificationSendReceive: alice sends to bob, bob sees it in received,
// alice sees it in sent, and the WS push fired once for bob.
func TestM2_NotificationSendReceive(t *testing.T) {
	env := newV5Env(t)
	_, aliceTok := env.CreateUserAndToken("m2nt_alice", "m2nt_a@x.com")
	bobID, bobTok := env.CreateUserAndToken("m2nt_bob", "m2nt_b@x.com")

	env.userPush.Reset()
	obj := env.httpExpect.POST("/api/notifications").
		WithHeader("Authorization", bearer(aliceTok)).
		WithJSON(map[string]any{
			"receiver_id": bobID,
			"title":       "hello",
			"body":        "world",
			"type":        0,
		}).
		Expect().Status(201).JSON().Object()
	obj.Value("receiver_id").Number().IsEqual(bobID)
	notifID := int64(obj.Value("id").Number().Raw())

	// Push recorded for bob.
	events := env.userPush.Snapshot()
	if len(events) != 1 || events[0].UserID != bobID || events[0].EventType != "notification_received" {
		t.Fatalf("unexpected push events: %+v", events)
	}

	// Bob sees it in received.
	got := env.httpExpect.GET("/api/notifications/received").
		WithHeader("Authorization", bearer(bobTok)).
		Expect().Status(200).JSON().Object()
	got.Value("notifications").Array().Length().IsEqual(1)
	got.Value("notifications").Array().Value(0).Object().
		Value("id").Number().IsEqual(notifID)

	// Alice sees it in sent.
	sent := env.httpExpect.GET("/api/notifications/sent").
		WithHeader("Authorization", bearer(aliceTok)).
		Expect().Status(200).JSON().Object()
	sent.Value("notifications").Array().Length().IsEqual(1)
}

// TestM2_NotificationMarkRead: marking read removes it from the unread-only
// list but keeps it in the full list.
func TestM2_NotificationMarkRead(t *testing.T) {
	env := newV5Env(t)
	_, aliceTok := env.CreateUserAndToken("m2nmr_alice", "m2nmr_a@x.com")
	bobID, bobTok := env.CreateUserAndToken("m2nmr_bob", "m2nmr_b@x.com")

	obj := env.httpExpect.POST("/api/notifications").
		WithHeader("Authorization", bearer(aliceTok)).
		WithJSON(map[string]any{
			"receiver_id": bobID,
			"title":       "status",
			"body":        "check",
		}).
		Expect().Status(201).JSON().Object()
	notifID := int64(obj.Value("id").Number().Raw())

	// Unread list has 1.
	env.httpExpect.GET("/api/notifications/received?unread_only=true").
		WithHeader("Authorization", bearer(bobTok)).
		Expect().Status(200).JSON().Object().
		Value("notifications").Array().Length().IsEqual(1)

	// Mark read.
	env.httpExpect.POST(fmt.Sprintf("/api/notifications/%d/read", notifID)).
		WithHeader("Authorization", bearer(bobTok)).
		Expect().Status(200)

	// Unread list now empty.
	env.httpExpect.GET("/api/notifications/received?unread_only=true").
		WithHeader("Authorization", bearer(bobTok)).
		Expect().Status(200).JSON().Object().
		Value("notifications").Array().Length().IsEqual(0)

	// Full list still has 1.
	env.httpExpect.GET("/api/notifications/received").
		WithHeader("Authorization", bearer(bobTok)).
		Expect().Status(200).JSON().Object().
		Value("notifications").Array().Length().IsEqual(1)

	// Re-mark read is idempotent 200.
	env.httpExpect.POST(fmt.Sprintf("/api/notifications/%d/read", notifID)).
		WithHeader("Authorization", bearer(bobTok)).
		Expect().Status(200)
}

// TestM2_NotificationListFilters: unread_only filter + someone-else's
// notification stays private.
func TestM2_NotificationListFilters(t *testing.T) {
	env := newV5Env(t)
	_, aliceTok := env.CreateUserAndToken("m2nlf_alice", "m2nlf_a@x.com")
	bobID, bobTok := env.CreateUserAndToken("m2nlf_bob", "m2nlf_b@x.com")
	carolID, carolTok := env.CreateUserAndToken("m2nlf_carol", "m2nlf_c@x.com")

	// Alice sends to bob twice, to carol once.
	for i := 0; i < 2; i++ {
		env.httpExpect.POST("/api/notifications").
			WithHeader("Authorization", bearer(aliceTok)).
			WithJSON(map[string]any{
				"receiver_id": bobID,
				"title":       fmt.Sprintf("b%d", i),
			}).
			Expect().Status(201)
	}
	env.httpExpect.POST("/api/notifications").
		WithHeader("Authorization", bearer(aliceTok)).
		WithJSON(map[string]any{
			"receiver_id": carolID,
			"title":       "c1",
		}).
		Expect().Status(201)

	// Bob sees 2 in his inbox.
	env.httpExpect.GET("/api/notifications/received").
		WithHeader("Authorization", bearer(bobTok)).
		Expect().Status(200).JSON().Object().
		Value("notifications").Array().Length().IsEqual(2)

	// Carol sees 1.
	env.httpExpect.GET("/api/notifications/received").
		WithHeader("Authorization", bearer(carolTok)).
		Expect().Status(200).JSON().Object().
		Value("notifications").Array().Length().IsEqual(1)

	// Alice sent 3.
	env.httpExpect.GET("/api/notifications/sent").
		WithHeader("Authorization", bearer(aliceTok)).
		Expect().Status(200).JSON().Object().
		Value("notifications").Array().Length().IsEqual(3)

	// Bogus receiver_id → 422.
	env.httpExpect.POST("/api/notifications").
		WithHeader("Authorization", bearer(aliceTok)).
		WithJSON(map[string]any{
			"receiver_id": 999999,
			"title":       "ghost",
		}).
		Expect().Status(422)

	// Missing title → 422.
	env.httpExpect.POST("/api/notifications").
		WithHeader("Authorization", bearer(aliceTok)).
		WithJSON(map[string]any{
			"receiver_id": bobID,
		}).
		Expect().Status(422)
}

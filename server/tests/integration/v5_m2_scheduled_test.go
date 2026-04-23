//go:build integration

package integration

import (
	"context"
	"fmt"
	"testing"
	"time"

	"im-server/internal/repo"
)

// TestM2_ScheduledCreateCancel: create a pending scheduled message, cancel it,
// ensure it won't deliver.
func TestM2_ScheduledCreateCancel(t *testing.T) {
	env := newV5Env(t)
	_, aliceTok := env.CreateUserAndToken("m2sc_alice", "m2sc_a@x.com")
	bobID, _ := env.CreateUserAndToken("m2sc_bob", "m2sc_b@x.com")

	chID := env.CreateGroup(aliceTok, "m2 sched", bobID)

	future := time.Now().Add(2 * time.Minute).Format(time.RFC3339)
	obj := env.httpExpect.POST("/api/messages/scheduled").
		WithHeader("Authorization", bearer(aliceTok)).
		WithJSON(map[string]any{
			"channel_id":   chID,
			"content":      "future ping",
			"scheduled_at": future,
		}).
		Expect().Status(201).JSON().Object()
	obj.Value("status").Number().IsEqual(0) // pending
	smID := int64(obj.Value("id").Number().Raw())

	// Cancel.
	env.httpExpect.DELETE(fmt.Sprintf("/api/messages/scheduled/%d", smID)).
		WithHeader("Authorization", bearer(aliceTok)).
		Expect().Status(200)

	// Pending list now empty for alice.
	env.httpExpect.GET("/api/messages/scheduled").WithQuery("status", "pending").
		WithHeader("Authorization", bearer(aliceTok)).
		Expect().Status(200).JSON().Object().
		Value("scheduled").Array().Length().IsEqual(0)

	// Re-cancel returns 409 (no longer pending).
	env.httpExpect.DELETE(fmt.Sprintf("/api/messages/scheduled/%d", smID)).
		WithHeader("Authorization", bearer(aliceTok)).
		Expect().Status(409)
}

// TestM2_ScheduledDelivery: create a scheduled row via HTTP, then invoke
// scheduledSvc.Deliver() directly (bypassing the worker poll cadence) and
// verify the message lands in the channel and the scheduled row transitions
// to delivered.
func TestM2_ScheduledDelivery(t *testing.T) {
	env := newV5Env(t)
	aliceID, aliceTok := env.CreateUserAndToken("m2sd_alice", "m2sd_a@x.com")
	bobID, _ := env.CreateUserAndToken("m2sd_bob", "m2sd_b@x.com")

	chID := env.CreateGroup(aliceTok, "m2 sched-deliver", bobID)

	future := time.Now().Add(2 * time.Minute).Format(time.RFC3339)
	obj := env.httpExpect.POST("/api/messages/scheduled").
		WithHeader("Authorization", bearer(aliceTok)).
		WithJSON(map[string]any{
			"channel_id":   chID,
			"content":      "hello future",
			"scheduled_at": future,
		}).
		Expect().Status(201).JSON().Object()
	smID := int64(obj.Value("id").Number().Raw())

	// Fetch the full row via the scheduledSvc — then deliver immediately,
	// bypassing the worker poll cadence.
	ctx := context.Background()
	rows, err := env.scheduledSvc.List(ctx, aliceID, -1, 10, 0)
	if err != nil {
		t.Fatalf("list scheduled: %v", err)
	}
	var target *repo.ScheduledMessage
	for i := range rows {
		if rows[i].ID == smID {
			target = &rows[i]
			break
		}
	}
	if target == nil {
		t.Fatalf("scheduled id %d not found in list of %d rows", smID, len(rows))
	}
	if _, err := env.scheduledSvc.Deliver(ctx, target); err != nil {
		t.Fatalf("deliver: %v", err)
	}

	// The scheduled row should now be status=1 (delivered).
	env.httpExpect.GET("/api/messages/scheduled").
		WithHeader("Authorization", bearer(aliceTok)).
		Expect().Status(200).JSON().Object().
		Value("scheduled").Array().Value(0).Object().
		Value("status").Number().IsEqual(1)

	// The channel now has one message with the scheduled content.
	msgs := env.httpExpect.GET(fmt.Sprintf("/api/channels/%d/messages", chID)).
		WithHeader("Authorization", bearer(aliceTok)).
		Expect().Status(200).JSON().Object()
	msgs.Value("messages").Array().Length().IsEqual(1)
	msgs.Value("messages").Array().Value(0).Object().
		Value("content").String().IsEqual("hello future")
}

// TestM2_ScheduledPermission: scheduled_at too close (<60s) → 422;
// non-member → 403; non-sender cancel → 403.
func TestM2_ScheduledPermission(t *testing.T) {
	env := newV5Env(t)
	_, aliceTok := env.CreateUserAndToken("m2sp_alice", "m2sp_a@x.com")
	bobID, bobTok := env.CreateUserAndToken("m2sp_bob", "m2sp_b@x.com")
	_, carolTok := env.CreateUserAndToken("m2sp_carol", "m2sp_c@x.com")

	chID := env.CreateGroup(aliceTok, "m2 sched-perm", bobID)

	// Too close → 422.
	tooSoon := time.Now().Add(5 * time.Second).Format(time.RFC3339)
	env.httpExpect.POST("/api/messages/scheduled").
		WithHeader("Authorization", bearer(aliceTok)).
		WithJSON(map[string]any{
			"channel_id":   chID,
			"content":      "ping",
			"scheduled_at": tooSoon,
		}).
		Expect().Status(422)

	// Non-member → 403.
	future := time.Now().Add(2 * time.Minute).Format(time.RFC3339)
	env.httpExpect.POST("/api/messages/scheduled").
		WithHeader("Authorization", bearer(carolTok)).
		WithJSON(map[string]any{
			"channel_id":   chID,
			"content":      "sneak",
			"scheduled_at": future,
		}).
		Expect().Status(403)

	// Alice creates OK.
	obj := env.httpExpect.POST("/api/messages/scheduled").
		WithHeader("Authorization", bearer(aliceTok)).
		WithJSON(map[string]any{
			"channel_id":   chID,
			"content":      "ping",
			"scheduled_at": future,
		}).
		Expect().Status(201).JSON().Object()
	smID := int64(obj.Value("id").Number().Raw())

	// Bob tries to cancel Alice's → 403.
	env.httpExpect.DELETE(fmt.Sprintf("/api/messages/scheduled/%d", smID)).
		WithHeader("Authorization", bearer(bobTok)).
		Expect().Status(403)
}

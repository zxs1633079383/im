//go:build integration

package integration

import (
	"fmt"
	"testing"
)

// TestM2_ApprovalCreateApprove: bob requests, alice (owner/approver) approves.
func TestM2_ApprovalCreateApprove(t *testing.T) {
	env := newV5Env(t)
	aliceID, aliceTok := env.CreateUserAndToken("m2ap_alice", "m2ap_a@x.com")
	bobID, bobTok := env.CreateUserAndToken("m2ap_bob", "m2ap_b@x.com")

	// Alice creates group (she's owner), bob joins.
	chID := env.CreateGroup(aliceTok, "m2 approval", bobID)

	env.userPush.Reset()
	obj := env.httpExpect.POST("/api/approvals").
		WithHeader("Authorization", bearer(bobTok)).
		WithJSON(map[string]any{
			"channel_id":  chID,
			"approver_id": aliceID,
			"subject":     "vacation",
			"content":     "one week",
		}).
		Expect().Status(201).JSON().Object()
	obj.Value("status").Number().IsEqual(0) // pending
	obj.Value("requester_id").Number().IsEqual(bobID)
	obj.Value("approver_id").Number().IsEqual(aliceID)
	approvalID := int64(obj.Value("id").Number().Raw())

	// Expect push to both requester + approver.
	events := env.userPush.Snapshot()
	if len(events) != 2 {
		t.Fatalf("expected 2 push events on create, got %d: %+v", len(events), events)
	}

	// Alice approves.
	env.userPush.Reset()
	decided := env.httpExpect.POST(fmt.Sprintf("/api/approvals/%d/approve", approvalID)).
		WithHeader("Authorization", bearer(aliceTok)).
		WithJSON(map[string]string{"note": "ok"}).
		Expect().Status(200).JSON().Object()
	decided.Value("status").Number().IsEqual(1) // approved
	decided.Value("decision_note").String().IsEqual("ok")

	// Both sides notified.
	events = env.userPush.Snapshot()
	if len(events) != 2 {
		t.Fatalf("expected 2 push events on approve, got %d: %+v", len(events), events)
	}
}

// TestM2_ApprovalReject: rejection sets status=2 and records note.
func TestM2_ApprovalReject(t *testing.T) {
	env := newV5Env(t)
	aliceID, aliceTok := env.CreateUserAndToken("m2apr_alice", "m2apr_a@x.com")
	bobID, bobTok := env.CreateUserAndToken("m2apr_bob", "m2apr_b@x.com")

	chID := env.CreateGroup(aliceTok, "m2 approval reject", bobID)

	obj := env.httpExpect.POST("/api/approvals").
		WithHeader("Authorization", bearer(bobTok)).
		WithJSON(map[string]any{
			"channel_id":  chID,
			"approver_id": aliceID,
			"subject":     "budget",
			"content":     "need $1000",
		}).
		Expect().Status(201).JSON().Object()
	approvalID := int64(obj.Value("id").Number().Raw())

	decided := env.httpExpect.POST(fmt.Sprintf("/api/approvals/%d/reject", approvalID)).
		WithHeader("Authorization", bearer(aliceTok)).
		WithJSON(map[string]string{"note": "too high"}).
		Expect().Status(200).JSON().Object()
	decided.Value("status").Number().IsEqual(2) // rejected
	decided.Value("decision_note").String().IsEqual("too high")
}

// TestM2_ApprovalCancelPending: requester can cancel while pending.
func TestM2_ApprovalCancelPending(t *testing.T) {
	env := newV5Env(t)
	aliceID, aliceTok := env.CreateUserAndToken("m2apc_alice", "m2apc_a@x.com")
	bobID, bobTok := env.CreateUserAndToken("m2apc_bob", "m2apc_b@x.com")

	chID := env.CreateGroup(aliceTok, "m2 approval cancel", bobID)

	obj := env.httpExpect.POST("/api/approvals").
		WithHeader("Authorization", bearer(bobTok)).
		WithJSON(map[string]any{
			"channel_id":  chID,
			"approver_id": aliceID,
			"subject":     "wfh",
			"content":     "tomorrow",
		}).
		Expect().Status(201).JSON().Object()
	approvalID := int64(obj.Value("id").Number().Raw())

	// Bob cancels.
	cancelled := env.httpExpect.POST(fmt.Sprintf("/api/approvals/%d/cancel", approvalID)).
		WithHeader("Authorization", bearer(bobTok)).
		Expect().Status(200).JSON().Object()
	cancelled.Value("status").Number().IsEqual(3) // cancelled

	// Approving after cancel returns 409.
	env.httpExpect.POST(fmt.Sprintf("/api/approvals/%d/approve", approvalID)).
		WithHeader("Authorization", bearer(aliceTok)).
		WithJSON(map[string]string{"note": "late"}).
		Expect().Status(409)
}

// TestM2_ApprovalPermissions: only approver may approve, only requester may
// cancel, requester-to-approve returns 403.
func TestM2_ApprovalPermissions(t *testing.T) {
	env := newV5Env(t)
	aliceID, aliceTok := env.CreateUserAndToken("m2app_alice", "m2app_a@x.com")
	bobID, bobTok := env.CreateUserAndToken("m2app_bob", "m2app_b@x.com")
	_, carolTok := env.CreateUserAndToken("m2app_carol", "m2app_c@x.com")

	chID := env.CreateGroup(aliceTok, "m2 approval perms", bobID)

	// Carol tries to create in a channel she's not a member of — 403.
	env.httpExpect.POST("/api/approvals").
		WithHeader("Authorization", bearer(carolTok)).
		WithJSON(map[string]any{
			"channel_id":  chID,
			"approver_id": aliceID,
			"subject":     "sneak",
			"content":     "?",
		}).
		Expect().Status(403)

	// Bob submits — OK.
	obj := env.httpExpect.POST("/api/approvals").
		WithHeader("Authorization", bearer(bobTok)).
		WithJSON(map[string]any{
			"channel_id":  chID,
			"approver_id": aliceID,
			"subject":     "ticket",
			"content":     "buy",
		}).
		Expect().Status(201).JSON().Object()
	approvalID := int64(obj.Value("id").Number().Raw())

	// Bob tries to approve his own — 403 (not approver).
	env.httpExpect.POST(fmt.Sprintf("/api/approvals/%d/approve", approvalID)).
		WithHeader("Authorization", bearer(bobTok)).
		WithJSON(map[string]string{"note": ""}).
		Expect().Status(403)

	// Alice tries to cancel someone else's — 403 (not requester).
	env.httpExpect.POST(fmt.Sprintf("/api/approvals/%d/cancel", approvalID)).
		WithHeader("Authorization", bearer(aliceTok)).
		Expect().Status(403)

	// Bob tries to create with a non-manager approver (bob as approver) — 403.
	env.httpExpect.POST("/api/approvals").
		WithHeader("Authorization", bearer(bobTok)).
		WithJSON(map[string]any{
			"channel_id":  chID,
			"approver_id": bobID, // bob is not manager
			"subject":     "loopback",
			"content":     "self",
		}).
		Expect().Status(403)
}

// TestM2_ApprovalListMine: requester sees their own requests; approver sees
// the pending queue.
func TestM2_ApprovalListMine(t *testing.T) {
	env := newV5Env(t)
	aliceID, aliceTok := env.CreateUserAndToken("m2aplm_alice", "m2aplm_a@x.com")
	bobID, bobTok := env.CreateUserAndToken("m2aplm_bob", "m2aplm_b@x.com")

	chID := env.CreateGroup(aliceTok, "m2 aplm", bobID)

	// Bob files two.
	for i := 0; i < 2; i++ {
		env.httpExpect.POST("/api/approvals").
			WithHeader("Authorization", bearer(bobTok)).
			WithJSON(map[string]any{
				"channel_id":  chID,
				"approver_id": aliceID,
				"subject":     fmt.Sprintf("s%d", i),
				"content":     fmt.Sprintf("c%d", i),
			}).
			Expect().Status(201)
	}

	// mine list for bob returns 2.
	mine := env.httpExpect.GET("/api/approvals/mine").
		WithHeader("Authorization", bearer(bobTok)).
		Expect().Status(200).JSON().Object()
	mine.Value("approvals").Array().Length().IsEqual(2)

	// pending list for alice returns 2.
	pending := env.httpExpect.GET("/api/approvals/pending").
		WithHeader("Authorization", bearer(aliceTok)).
		Expect().Status(200).JSON().Object()
	pending.Value("approvals").Array().Length().IsEqual(2)

	// pending list for bob returns 0 (he's not an approver).
	bobPending := env.httpExpect.GET("/api/approvals/pending").
		WithHeader("Authorization", bearer(bobTok)).
		Expect().Status(200).JSON().Object()
	bobPending.Value("approvals").Array().Length().IsEqual(0)

	_ = bobID
}

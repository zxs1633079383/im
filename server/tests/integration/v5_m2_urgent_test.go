//go:build integration

package integration

import (
	"fmt"
	"strconv"
	"testing"
	"time"
)

// TestM2_UrgentSendConfirm: send urgent, recipient confirms, sender can list
// confirmations.
func TestM2_UrgentSendConfirm(t *testing.T) {
	env := newV5Env(t)
	_, aliceTok := env.CreateUserAndToken("m2ur_alice", "m2ur_a@x.com")
	bobID, bobTok := env.CreateUserAndToken("m2ur_bob", "m2ur_b@x.com")

	chID := env.CreateGroup(aliceTok, "m2 urgent", bobID)

	env.broadcasts.Reset()
	obj := env.httpExpect.POST("/api/messages/urgent").
		WithHeader("Authorization", bearer(aliceTok)).
		WithJSON(map[string]any{
			"channel_id":    chID,
			"content":       "EMERGENCY",
			"client_msg_id": "m2ur-1",
		}).
		Expect().Status(201).JSON().Object()
	obj.Value("is_urgent").Boolean().IsEqual(true)
	msgID := int64(obj.Value("id").Number().Raw())

	// Broadcast includes the urgent_posted event.
	events := waitForBroadcastCount(t, env.broadcasts, 1, 2*time.Second)
	foundUrgent := false
	for _, e := range events {
		if e.EventType == "urgent_posted" && e.ChannelID == chID {
			foundUrgent = true
		}
	}
	if !foundUrgent {
		t.Fatalf("urgent_posted event not broadcast; got %+v", events)
	}

	// Bob confirms.
	env.httpExpect.POST(fmt.Sprintf("/api/messages/%d/urgent/confirm", msgID)).
		WithHeader("Authorization", bearer(bobTok)).
		Expect().Status(200)

	// Alice lists confirmations.
	confs := env.httpExpect.GET(fmt.Sprintf("/api/messages/%d/urgent/confirmations", msgID)).
		WithHeader("Authorization", bearer(aliceTok)).
		Expect().Status(200).JSON().Object()
	confs.Value("confirmations").Array().Length().IsEqual(1)
	confs.Value("confirmations").Array().Value(0).Number().IsEqual(bobID)
}

// TestM2_UrgentCancelPermission: sender can cancel; random member cannot.
func TestM2_UrgentCancelPermission(t *testing.T) {
	env := newV5Env(t)
	_, aliceTok := env.CreateUserAndToken("m2ucp_alice", "m2ucp_a@x.com")
	bobID, bobTok := env.CreateUserAndToken("m2ucp_bob", "m2ucp_b@x.com")

	chID := env.CreateGroup(aliceTok, "m2 ucp", bobID)

	obj := env.httpExpect.POST("/api/messages/urgent").
		WithHeader("Authorization", bearer(aliceTok)).
		WithJSON(map[string]any{
			"channel_id":    chID,
			"content":       "urgent!",
			"client_msg_id": "m2ucp-1",
		}).
		Expect().Status(201).JSON().Object()
	msgID := int64(obj.Value("id").Number().Raw())

	// Bob tries to cancel — 403 (not sender, not manager).
	env.httpExpect.POST(fmt.Sprintf("/api/messages/%d/urgent/cancel", msgID)).
		WithHeader("Authorization", bearer(bobTok)).
		Expect().Status(403)

	// Sender alice cancels.
	env.httpExpect.POST(fmt.Sprintf("/api/messages/%d/urgent/cancel", msgID)).
		WithHeader("Authorization", bearer(aliceTok)).
		Expect().Status(200)

	// After cancellation the urgent flag is cleared — confirming now returns
	// 422 "not urgent".
	env.httpExpect.POST(fmt.Sprintf("/api/messages/%d/urgent/confirm", msgID)).
		WithHeader("Authorization", bearer(bobTok)).
		Expect().Status(422)
}

// TestM2_UrgentMultiUserConfirmList: two confirmations land in the list in order.
func TestM2_UrgentMultiUserConfirmList(t *testing.T) {
	env := newV5Env(t)
	_, aliceTok := env.CreateUserAndToken("m2umc_alice", "m2umc_a@x.com")
	bobID, bobTok := env.CreateUserAndToken("m2umc_bob", "m2umc_b@x.com")
	carolID, carolTok := env.CreateUserAndToken("m2umc_carol", "m2umc_c@x.com")

	chID := env.CreateGroup(aliceTok, "m2 umc", bobID, carolID)

	obj := env.httpExpect.POST("/api/messages/urgent").
		WithHeader("Authorization", bearer(aliceTok)).
		WithJSON(map[string]any{
			"channel_id":    chID,
			"content":       "to all",
			"client_msg_id": "m2umc-1",
		}).
		Expect().Status(201).JSON().Object()
	msgID := int64(obj.Value("id").Number().Raw())

	// Two recipients confirm.
	env.httpExpect.POST(fmt.Sprintf("/api/messages/%d/urgent/confirm", msgID)).
		WithHeader("Authorization", bearer(bobTok)).
		Expect().Status(200)
	env.httpExpect.POST(fmt.Sprintf("/api/messages/%d/urgent/confirm", msgID)).
		WithHeader("Authorization", bearer(carolTok)).
		Expect().Status(200)

	// Idempotent re-confirm — still 200.
	env.httpExpect.POST(fmt.Sprintf("/api/messages/%d/urgent/confirm", msgID)).
		WithHeader("Authorization", bearer(bobTok)).
		Expect().Status(200)

	confs := env.httpExpect.GET(fmt.Sprintf("/api/messages/%d/urgent/confirmations", msgID)).
		WithHeader("Authorization", bearer(aliceTok)).
		Expect().Status(200).JSON().Object()
	arr := confs.Value("confirmations").Array()
	arr.Length().IsEqual(2)
	// bob confirmed first — order preserved.
	if int64(arr.Value(0).Number().Raw()) != bobID ||
		int64(arr.Value(1).Number().Raw()) != carolID {
		t.Fatalf("confirmation order unexpected: %v", arr.Raw())
	}

	_ = strconv.FormatInt(msgID, 10)
}

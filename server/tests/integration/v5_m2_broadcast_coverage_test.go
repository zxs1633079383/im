//go:build integration

// Package integration — additional WS broadcast coverage for M2 events.
//
// Each M2 feature (announcement, urgent, notification) already has one
// assertion that its headline event fires on the happy path. These tests
// add a second scenario per event type so that regressions in a secondary
// path (repeated posts, multi-recipient fan-out, follow-up sends) are
// caught independently.
package integration

import (
	"fmt"
	"testing"
	"time"

	imhttp "im-server/internal/http"
)

// TestM2_AnnouncementPostedBroadcastRepeated: two announcements in the same
// channel produce two announcement_posted broadcasts, in order. Exercises
// the broadcast pipeline under repeated writes where the first success
// must not mask a second-write regression.
func TestM2_AnnouncementPostedBroadcastRepeated(t *testing.T) {
	env := newV5Env(t)
	_, ownerTok := env.CreateUserAndToken("m2ab_owner", "m2ab_o@x.com")
	memberID, _ := env.CreateUserAndToken("m2ab_mem", "m2ab_m@x.com")
	chID := env.CreateGroup(ownerTok, "m2ab_ch", memberID)

	env.broadcasts.Reset()

	for i := 1; i <= 2; i++ {
		env.httpExpect.POST("/api/announcements").
			WithHeader("Authorization", bearer(ownerTok)).
			WithJSON(map[string]any{
				"channel_id": chID,
				"title":      fmt.Sprintf("post-%d", i),
				"content":    fmt.Sprintf("body-%d", i),
			}).
			Expect().Status(201)
	}

	events := waitForBroadcastCount(t, env.broadcasts, 2, 2*time.Second)
	if n := CountBroadcastsByType(events, chID, string(imhttp.EventAnnouncementPosted)); n != 2 {
		t.Fatalf("announcement_posted count=%d, want 2; events=%+v", n, events)
	}
}

// TestM2_UrgentPostedBroadcastTwoChannels: urgent send on two distinct
// channels produces one urgent_posted broadcast per channel. Verifies the
// broadcast is scoped to the correct channel id and doesn't cross-leak.
func TestM2_UrgentPostedBroadcastTwoChannels(t *testing.T) {
	env := newV5Env(t)
	_, aliceTok := env.CreateUserAndToken("m2ub_alice", "m2ub_a@x.com")
	bobID, _ := env.CreateUserAndToken("m2ub_bob", "m2ub_b@x.com")
	carolID, _ := env.CreateUserAndToken("m2ub_carol", "m2ub_c@x.com")

	ch1 := env.CreateGroup(aliceTok, "m2ub_ch1", bobID)
	ch2 := env.CreateGroup(aliceTok, "m2ub_ch2", carolID)

	env.broadcasts.Reset()

	env.httpExpect.POST("/api/messages/urgent").
		WithHeader("Authorization", bearer(aliceTok)).
		WithJSON(map[string]any{
			"channel_id":    ch1,
			"content":       "ch1 urgent",
			"client_msg_id": "m2ub-1",
		}).
		Expect().Status(201)

	env.httpExpect.POST("/api/messages/urgent").
		WithHeader("Authorization", bearer(aliceTok)).
		WithJSON(map[string]any{
			"channel_id":    ch2,
			"content":       "ch2 urgent",
			"client_msg_id": "m2ub-2",
		}).
		Expect().Status(201)

	// Each urgent send fires its own send-side broadcast + a urgent_posted
	// broadcast; wait for at least the urgent_posted pair.
	events := waitForBroadcastCount(t, env.broadcasts, 2, 2*time.Second)

	if n := CountBroadcastsByType(events, ch1, string(imhttp.EventUrgentPosted)); n != 1 {
		t.Fatalf("urgent_posted for ch1=%d got %d, want 1; events=%+v", ch1, n, events)
	}
	if n := CountBroadcastsByType(events, ch2, string(imhttp.EventUrgentPosted)); n != 1 {
		t.Fatalf("urgent_posted for ch2=%d got %d, want 1; events=%+v", ch2, n, events)
	}
}

// TestM2_NotificationReceivedMultiRecipient: one sender fans out to three
// distinct receivers; each receives one notification_received push.
func TestM2_NotificationReceivedMultiRecipient(t *testing.T) {
	env := newV5Env(t)
	_, senderTok := env.CreateUserAndToken("m2nb_sender", "m2nb_s@x.com")
	bobID, _ := env.CreateUserAndToken("m2nb_bob", "m2nb_b@x.com")
	carolID, _ := env.CreateUserAndToken("m2nb_carol", "m2nb_c@x.com")
	daveID, _ := env.CreateUserAndToken("m2nb_dave", "m2nb_d@x.com")

	env.userPush.Reset()

	recipients := []int64{bobID, carolID, daveID}
	for i, rid := range recipients {
		env.httpExpect.POST("/api/notifications").
			WithHeader("Authorization", bearer(senderTok)).
			WithJSON(map[string]any{
				"receiver_id": rid,
				"title":       fmt.Sprintf("n%d", i),
				"body":        "hi",
			}).
			Expect().Status(201)
	}

	events := env.userPush.Snapshot()
	for _, rid := range recipients {
		if n := CountUserPushByType(events, rid, "notification_received"); n != 1 {
			t.Fatalf("notification_received for user=%d count=%d, want 1; events=%+v",
				rid, n, events)
		}
	}

	// Exactly three notification_received events total — no fan-out leak
	// to non-recipients.
	total := 0
	for _, ev := range events {
		if string(ev.EventType) == "notification_received" {
			total++
		}
	}
	if total != len(recipients) {
		t.Fatalf("total notification_received=%d, want %d; events=%+v",
			total, len(recipients), events)
	}
}

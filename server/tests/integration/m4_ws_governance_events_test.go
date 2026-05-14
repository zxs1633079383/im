//go:build integration

package integration

import (
	"strconv"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"im-server/internal/gateway"
	"im-server/internal/middleware"
	"im-server/internal/repo"
)

// D3 — WS governance events family. Four server→client push events triggered
// by HTTP-driven mutations on shared state:
//
//   - announcement_posted   (POST /api/announcements           — broadcast to channel members)
//   - urgent_posted         (POST /api/messages/urgent         — broadcast to channel members)
//   - approval_updated      (POST /api/approvals               — push to requester + approver)
//   - notification_received (POST /api/notifications           — push to receiver)
//
// Each test:
//   1. seeds the actors (and a group channel where applicable),
//   2. dials the listener's WS conn,
//   3. waits ~100ms for ws register + Redis routing write to settle,
//   4. has the trigger user issue the HTTP mutation,
//   5. asserts the listener receives the expected WS frame within 5s.
//
// Seed range 930-939 is reserved for D3 to avoid colliding with the WS
// fixture reference test (900-901), D1 message events (910-919) and the
// other Batch-D agents.
//
// The push payload for every event in this family is the full repo row
// serialised through gin's JSON encoder. Tests decode into map[string]any
// so they stay decoupled from struct field nomenclature while still asserting
// the load-bearing identifiers (id, channel_id, sender / receiver fields).
//
// settleDelay is defined in m4_ws_message_events_test.go (same package) —
// reuse it here so all WS tests share one cadence.

// TestM4WSAnnouncementPosted_HappyPath — owner POSTs an announcement to a
// group; A (a member of that group) receives an announcement_posted frame
// carrying the full repo.Announcement row.
func TestM4WSAnnouncementPosted_HappyPath(t *testing.T) {
	env := newM4Env(t)
	cookieOwner, _ := env.seedUser(930)
	cookieA, idA := env.seedUser(931)
	channelID := env.seedGroup(cookieOwner, "ws-ann-posted", idA)

	wcA := wsDial(t, env, cookieA)
	time.Sleep(settleDelay)

	created := successBody(env.expect.POST("/api/announcements").
		WithHeader(middleware.MMCookieHeader, cookieOwner).
		WithJSON(map[string]any{
			"channel_id": channelID,
			"title":      "ws-ann-title",
			"content":    "ws-ann-body",
		}).
		Expect().Status(201))
	annID := created.Value("id").String().Raw()

	frame := wcA.expectFrame(gateway.TypeAnnouncementPosted, 5*time.Second)
	var p map[string]any
	decodePayload(t, frame, &p)
	require.Equal(t, annID, p["id"], "announcement_posted id")
	require.Equal(t, channelID, p["channel_id"], "announcement_posted channel_id")
	require.Equal(t, "ws-ann-title", p["title"], "announcement_posted title")
	require.Equal(t, "ws-ann-body", p["content"], "announcement_posted content")
}

// TestM4WSUrgentPosted_HappyPath — owner POSTs an urgent message to a group;
// A (member of the same group) receives an urgent_posted frame carrying the
// full repo.Message row, with is_urgent=true.
func TestM4WSUrgentPosted_HappyPath(t *testing.T) {
	env := newM4Env(t)
	cookieOwner, _ := env.seedUser(932)
	cookieA, idA := env.seedUser(933)
	channelID := env.seedGroup(cookieOwner, "ws-urgent-posted", idA)

	wcA := wsDial(t, env, cookieA)
	time.Sleep(settleDelay)

	sent := successBody(env.expect.POST("/api/messages/urgent").
		WithHeader(middleware.MMCookieHeader, cookieOwner).
		WithJSON(map[string]any{
			"channel_id":    channelID,
			"content":       "URGENT-via-WS",
			"client_msg_id": "cli-urgent-ws-d3",
		}).
		Expect().Status(201))
	msgID := sent.Value("id").String().Raw()

	frame := wcA.expectFrame(gateway.TypeUrgentPosted, 5*time.Second)
	var p map[string]any
	decodePayload(t, frame, &p)
	require.Equal(t, msgID, p["id"], "urgent_posted id")
	require.Equal(t, channelID, p["channel_id"], "urgent_posted channel_id")
	require.Equal(t, "URGENT-via-WS", p["content"], "urgent_posted content")
	require.Equal(t, true, p["is_urgent"], "urgent_posted is_urgent must be true")
}

// TestM4WSApprovalUpdated_HappyPath — submitter (group member) files an
// approval against approver (group owner). pushApprovalUpdated fans the
// approval_updated event to BOTH parties — we listen on approver's conn for
// determinism (production also pushes to submitter, exercised by the broader
// approval HTTP tests). Payload is the full repo.Approval row.
func TestM4WSApprovalUpdated_HappyPath(t *testing.T) {
	env := newM4Env(t)
	cookieApprover, idApprover := env.seedUser(934)
	cookieSubmitter, idSubmitter := env.seedUser(935)
	channelID := env.seedGroup(cookieApprover, "ws-approval-updated", idSubmitter)

	wcApprover := wsDial(t, env, cookieApprover)
	time.Sleep(settleDelay)

	created := successBody(env.expect.POST("/api/approvals").
		WithHeader(middleware.MMCookieHeader, cookieSubmitter).
		WithJSON(map[string]any{
			"channel_id":  channelID,
			"approver_id": idApprover,
			"subject":     "ws-approval-subj",
			"content":     "ws-approval-body",
		}).
		Expect().Status(201))
	apprID := created.Value("id").String().Raw()

	frame := wcApprover.expectFrame(gateway.TypeApprovalUpdated, 5*time.Second)
	var p map[string]any
	decodePayload(t, frame, &p)
	require.Equal(t, apprID, p["id"], "approval_updated id")
	require.Equal(t, channelID, p["channel_id"], "approval_updated channel_id")
	require.Equal(t, idSubmitter, p["requester_id"], "approval_updated requester_id")
	require.Equal(t, idApprover, p["approver_id"], "approval_updated approver_id")
	require.Equal(t, float64(repo.ApprovalStatusPending), p["status"], "approval_updated status must be pending")
}

// TestM4WSNotificationReceived_HappyPath — sender POSTs a notification
// targeted at the receiver; receiver gets a notification_received frame
// carrying the full repo.Notification row. Single-target push (no broadcast).
func TestM4WSNotificationReceived_HappyPath(t *testing.T) {
	env := newM4Env(t)
	cookieSender, _ := env.seedUser(936)
	cookieRecv, idRecv := env.seedUser(937)

	wcRecv := wsDial(t, env, cookieRecv)
	time.Sleep(settleDelay)

	created := successBody(env.expect.POST("/api/notifications").
		WithHeader(middleware.MMCookieHeader, cookieSender).
		WithJSON(map[string]any{
			"receiver_id": idRecv,
			"title":       "ws-notif-title",
			"body":        "ws-notif-body",
			"type":        repo.NotificationTypeGeneric,
		}).
		Expect().Status(201))
	notifID := created.Value("id").String().Raw()

	frame := wcRecv.expectFrame(gateway.TypeNotificationReceived, 5*time.Second)
	var p map[string]any
	decodePayload(t, frame, &p)
	require.Equal(t, notifID, p["id"], "notification_received id")
	require.Equal(t, idRecv, p["receiver_id"], "notification_received receiver_id")
	require.Equal(t, "ws-notif-title", p["title"], "notification_received title")
	require.Equal(t, "ws-notif-body", p["body"], "notification_received body")
}

// _ keeps the strconv import live in case future cases need to format ids
// into URL paths (none of the four current cases do — they only POST/GET
// fixed routes and read ids out of the response body).
var _ = strconv.FormatInt

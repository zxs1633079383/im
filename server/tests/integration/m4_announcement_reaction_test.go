//go:build integration

// Package integration — Batch-C announcement + reaction happy-path coverage.
//
// 9 endpoints × 1 happy path each = 9 tests. Each top-level test calls
// newM4Env(t) for a fresh Postgres + Redis testcontainer pair; routes are
// wired centrally by buildEngine (see m4_harness_test.go), so test bodies
// only assert the response envelope shape and a single field.
//
// Seed range 500-599 is reserved for this file to avoid colliding with
// G1-G4 (100-499) and earlier Batch-A series.
//
// The contract scope is shallow on purpose — these tests assert "the route
// is wired, returns the documented 2xx envelope, and at least one
// response field looks reasonable". Deep behavioural assertions belong
// in unit tests for the underlying service.
package integration

import (
	"strconv"
	"testing"

	"github.com/gin-gonic/gin"

	"im-server/internal/middleware"
)

// seedAnnouncement creates a group owned by ownerCookie, then posts an
// announcement to it via the HTTP layer. Returns (channelID, announcementID).
// Used by the read/acks/delete/get tests so they share the same setup spine.
func seedAnnouncement(env *m4env, ownerCookie, name string) (int64, int64) {
	env.t.Helper()
	channelID := env.seedGroup(ownerCookie, name)
	body := successBody(env.expect.POST("/api/announcements").
		WithHeader(middleware.MMCookieHeader, ownerCookie).
		WithJSON(map[string]any{
			"channel_id": channelID,
			"title":      "ann-title",
			"content":    "ann-body",
		}).
		Expect().Status(201))
	annID := int64(body.Value("id").Number().Raw())
	return channelID, annID
}

// init guarantees gin runs in test mode for this file's tests even when the
// per-test newM4Env hasn't been called yet (gin's mode is process-global).
func init() { gin.SetMode(gin.TestMode) }

// ---------------------------------------------------------------------------
// Announcement endpoints — 6 happy paths
// ---------------------------------------------------------------------------

// TestM4AnnouncementCreate_HappyPath — group owner creates an announcement.
// Asserts envelope + id > 0 + title round-trips.
func TestM4AnnouncementCreate_HappyPath(t *testing.T) {
	env := newM4Env(t)

	cookieOwner, _ := env.seedUser(500)
	channelID := env.seedGroup(cookieOwner, "ann-create")

	body := successBody(env.expect.POST("/api/announcements").
		WithHeader(middleware.MMCookieHeader, cookieOwner).
		WithJSON(map[string]any{
			"channel_id": channelID,
			"title":      "hello",
			"content":    "world",
		}).
		Expect().Status(201))

	body.Value("id").Number().Gt(0)
	body.Value("title").String().IsEqual("hello")
	body.Value("content").String().IsEqual("world")
	body.Value("channel_id").Number().IsEqual(float64(channelID))
}

// TestM4AnnouncementRead_HappyPath — a member acks the announcement.
// Endpoint returns {"status":"acked"} via the success envelope.
func TestM4AnnouncementRead_HappyPath(t *testing.T) {
	env := newM4Env(t)

	cookieOwner, _ := env.seedUser(502)
	_, annID := seedAnnouncement(env, cookieOwner, "ann-read")

	body := successBody(env.expect.POST("/api/announcements/"+strconv.FormatInt(annID, 10)+"/read").
		WithHeader(middleware.MMCookieHeader, cookieOwner).
		Expect().Status(200))
	body.Value("status").String().IsEqual("acked")
}

// TestM4AnnouncementAcksList_HappyPath — manager+ lists ack rows after the
// owner has acked their own announcement.
func TestM4AnnouncementAcksList_HappyPath(t *testing.T) {
	env := newM4Env(t)

	cookieOwner, ownerID := env.seedUser(504)
	_, annID := seedAnnouncement(env, cookieOwner, "ann-acks")

	// Owner acks first so the list has at least one row.
	env.expect.POST("/api/announcements/"+strconv.FormatInt(annID, 10)+"/read").
		WithHeader(middleware.MMCookieHeader, cookieOwner).
		Expect().Status(200)

	body := successBody(env.expect.GET("/api/announcements/"+strconv.FormatInt(annID, 10)+"/acks").
		WithHeader(middleware.MMCookieHeader, cookieOwner).
		Expect().Status(200))
	acks := body.Value("acks").Array()
	acks.Length().IsEqual(1)
	acks.Value(0).Object().Value("user_id").String().IsEqual(ownerID)
}

// TestM4AnnouncementDelete_HappyPath — creator soft-deletes their announcement.
// Endpoint returns {"status":"deleted"} via the envelope.
func TestM4AnnouncementDelete_HappyPath(t *testing.T) {
	env := newM4Env(t)

	cookieOwner, _ := env.seedUser(506)
	_, annID := seedAnnouncement(env, cookieOwner, "ann-delete")

	body := successBody(env.expect.DELETE("/api/announcements/"+strconv.FormatInt(annID, 10)).
		WithHeader(middleware.MMCookieHeader, cookieOwner).
		Expect().Status(200))
	body.Value("status").String().IsEqual("deleted")
}

// TestM4AnnouncementListByChannel_HappyPath — owner lists announcements
// for a channel that has at least one entry.
func TestM4AnnouncementListByChannel_HappyPath(t *testing.T) {
	env := newM4Env(t)

	cookieOwner, _ := env.seedUser(508)
	channelID, annID := seedAnnouncement(env, cookieOwner, "ann-list")

	body := successBody(env.expect.GET("/api/channels/"+strconv.FormatInt(channelID, 10)+"/announcements").
		WithHeader(middleware.MMCookieHeader, cookieOwner).
		Expect().Status(200))
	arr := body.Value("announcements").Array()
	arr.Length().Gt(0)
	arr.Value(0).Object().Value("id").Number().IsEqual(float64(annID))
}

// TestM4AnnouncementGet_HappyPath — owner fetches the announcement they
// just created and verifies the round-tripped fields.
func TestM4AnnouncementGet_HappyPath(t *testing.T) {
	env := newM4Env(t)

	cookieOwner, _ := env.seedUser(510)
	_, annID := seedAnnouncement(env, cookieOwner, "ann-get")

	body := successBody(env.expect.GET("/api/announcements/"+strconv.FormatInt(annID, 10)).
		WithHeader(middleware.MMCookieHeader, cookieOwner).
		Expect().Status(200))
	body.Value("id").Number().IsEqual(float64(annID))
	body.Value("title").String().NotEmpty()
}

// ---------------------------------------------------------------------------
// Reaction endpoints — 3 happy paths
// ---------------------------------------------------------------------------

// TestM4ReactionAdd_HappyPath — sender reacts to their own DM message.
// Service requires the caller to be a member of the message's channel; the
// owner of a freshly seeded DM trivially is.
func TestM4ReactionAdd_HappyPath(t *testing.T) {
	env := newM4Env(t)

	cookieA, idA := env.seedUser(520)
	_, idB := env.seedUser(521)
	channelID := env.seedDM(cookieA, idB)
	msg := env.seedMessage(channelID, idA, "react-add")

	body := successBody(env.expect.POST("/api/messages/"+strconv.FormatInt(msg.ID, 10)+"/reactions").
		WithHeader(middleware.MMCookieHeader, cookieA).
		WithJSON(map[string]any{"emoji": ":thumbsup:"}).
		Expect().Status(201))
	body.Value("status").String().IsEqual("ok")
}

// TestM4ReactionRemove_HappyPath — add a reaction, then remove it. Both
// 2xx; the remove leg is what's under test.
func TestM4ReactionRemove_HappyPath(t *testing.T) {
	env := newM4Env(t)

	cookieA, idA := env.seedUser(522)
	_, idB := env.seedUser(523)
	channelID := env.seedDM(cookieA, idB)
	msg := env.seedMessage(channelID, idA, "react-remove")

	env.expect.POST("/api/messages/"+strconv.FormatInt(msg.ID, 10)+"/reactions").
		WithHeader(middleware.MMCookieHeader, cookieA).
		WithJSON(map[string]any{"emoji": ":fire:"}).
		Expect().Status(201)

	body := successBody(env.expect.DELETE("/api/messages/"+strconv.FormatInt(msg.ID, 10)+"/reactions/:fire:").
		WithHeader(middleware.MMCookieHeader, cookieA).
		Expect().Status(200))
	body.Value("status").String().IsEqual("ok")
}

// TestM4ReactionList_HappyPath — list reactions on a message after one has
// been added. Response data is a top-level JSON array (not wrapped under
// "items"), so successBodyArray is the right helper.
func TestM4ReactionList_HappyPath(t *testing.T) {
	env := newM4Env(t)

	cookieA, idA := env.seedUser(524)
	_, idB := env.seedUser(525)
	channelID := env.seedDM(cookieA, idB)
	msg := env.seedMessage(channelID, idA, "react-list")

	env.expect.POST("/api/messages/"+strconv.FormatInt(msg.ID, 10)+"/reactions").
		WithHeader(middleware.MMCookieHeader, cookieA).
		WithJSON(map[string]any{"emoji": ":heart:"}).
		Expect().Status(201)

	arr := successBodyArray(env.expect.GET("/api/messages/"+strconv.FormatInt(msg.ID, 10)+"/reactions").
		WithHeader(middleware.MMCookieHeader, cookieA).
		Expect().Status(200))
	arr.Length().IsEqual(1)
	arr.Value(0).Object().Value("emoji").String().IsEqual(":heart:")
	arr.Value(0).Object().Value("user_id").String().IsEqual(idA)
}

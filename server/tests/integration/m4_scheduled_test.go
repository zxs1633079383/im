//go:build integration

// Package integration — Batch-C C4 scheduled message happy paths.
//
// Routes wired centrally by buildEngine (m4_harness_test.go). Each endpoint
// has one happy path; failure branches are covered by service unit tests.
//
// Seed range: 800-899 (C4-owned).
package integration

import (
	"testing"
	"time"

	"im-server/internal/middleware"
)

// TestM4ScheduledCreate_HappyPath — POST /api/messages/scheduled by a
// channel member with scheduled_at >= 60s future returns 201 + the persisted
// scheduled message id.
func TestM4ScheduledCreate_HappyPath(t *testing.T) {
	env := newM4Env(t)
	cookieOwner, _ := env.seedUser(800)
	_, peerID := env.seedUser(801)
	channelID := env.seedGroup(cookieOwner, "c4-sched-create", peerID)

	scheduledAt := time.Now().Add(2 * time.Minute).UTC().Format(time.RFC3339Nano)
	data := successBody(env.expect.POST("/api/messages/scheduled").
		WithHeader(middleware.MMCookieHeader, cookieOwner).
		WithJSON(map[string]any{
			"channel_id":   channelID,
			"content":      "scheduled hello",
			"msg_type":     1,
			"scheduled_at": scheduledAt,
		}).
		Expect().Status(201))
	data.Value("id").Number().Gt(0)
	data.Value("channel_id").String().IsEqual(channelID)
}

// TestM4ScheduledCancel_HappyPath — sender DELETE on a pending scheduled row
// returns 200 with the cancelled record (or empty body — handler shape).
func TestM4ScheduledCancel_HappyPath(t *testing.T) {
	env := newM4Env(t)
	cookieOwner, _ := env.seedUser(810)
	_, peerID := env.seedUser(811)
	channelID := env.seedGroup(cookieOwner, "c4-sched-cancel", peerID)

	scheduledAt := time.Now().Add(2 * time.Minute).UTC().Format(time.RFC3339Nano)
	create := successBody(env.expect.POST("/api/messages/scheduled").
		WithHeader(middleware.MMCookieHeader, cookieOwner).
		WithJSON(map[string]any{
			"channel_id":   channelID,
			"content":      "to-cancel",
			"msg_type":     1,
			"scheduled_at": scheduledAt,
		}).
		Expect().Status(201))
	id := create.Value("id").String().Raw()

	env.expect.DELETE("/api/messages/scheduled/"+id).
		WithHeader(middleware.MMCookieHeader, cookieOwner).
		Expect().Status(200)
}

// TestM4ScheduledList_HappyPath — GET /api/messages/scheduled returns the
// caller's queue inside data.scheduled[].
func TestM4ScheduledList_HappyPath(t *testing.T) {
	env := newM4Env(t)
	cookieOwner, _ := env.seedUser(820)
	_, peerID := env.seedUser(821)
	channelID := env.seedGroup(cookieOwner, "c4-sched-list", peerID)

	scheduledAt := time.Now().Add(2 * time.Minute).UTC().Format(time.RFC3339Nano)
	env.expect.POST("/api/messages/scheduled").
		WithHeader(middleware.MMCookieHeader, cookieOwner).
		WithJSON(map[string]any{
			"channel_id":   channelID,
			"content":      "for-list",
			"msg_type":     1,
			"scheduled_at": scheduledAt,
		}).
		Expect().Status(201)

	data := successBody(env.expect.GET("/api/messages/scheduled").
		WithHeader(middleware.MMCookieHeader, cookieOwner).
		Expect().Status(200))
	data.Value("scheduled").Array().Length().Gt(0)
}

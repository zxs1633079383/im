//go:build integration

package integration

import (
	"context"
	"strconv"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"

	"im-server/internal/auth"
	imhttp "im-server/internal/http"
	"im-server/internal/middleware"
	"im-server/internal/repo"
	"im-server/internal/service"
	"im-server/internal/testutil"
	"im-server/internal/testutil/containers"
)

// TestBroadcast_OneBatchPerChannel locks in the performance invariant
// introduced by the routing + Pulsar batching refactor: one HTTP POST to
// /api/channels/:id/messages must yield exactly one MessagePusher
// BroadcastMessage invocation carrying the full channel member set, not
// one PushMessage per member.
//
// The PushRecorder harness still flattens batches into per-user events for
// other group tests; here we assert on BatchSnapshot() to pin the count.
func TestBroadcast_OneBatchPerChannel(t *testing.T) {
	dsn := containers.StartPostgres(t)
	db, err := repo.Open(repo.Config{DSN: dsn})
	require.NoError(t, err)
	t.Cleanup(func() {
		if sqlDB, e := db.DB(); e == nil {
			_ = sqlDB.Close()
		}
	})

	users := repo.NewUserRepo(db)
	channels := repo.NewChannelRepo(db)
	messages := repo.NewMessageRepo(db, channels)

	// Seed a 5-member channel.
	ctx := context.Background()
	mkUser := func(n string) *repo.User {
		u := &repo.User{Username: n, Email: n + "@x.com", PasswordHash: "x", DisplayName: n, Status: repo.UserStatusActive}
		require.NoError(t, users.Create(ctx, u))
		return u
	}
	owner := mkUser("owner-b")
	m2 := mkUser("m2-b")
	m3 := mkUser("m3-b")
	m4 := mkUser("m4-b")
	m5 := mkUser("m5-b")

	ownerTok, err := auth.GenerateToken(integrationSecret, owner.ID, owner.Username)
	require.NoError(t, err)

	gin.SetMode(gin.TestMode)
	r := gin.New()
	authedAPI := r.Group("/api")
	authedAPI.Use(middleware.JWTGin(integrationSecret))

	channelSvc := service.NewChannelService(channels, users, nil) // no sys-msg emission
	imhttp.RegisterChannelRoutes(authedAPI, channelSvc, nil)

	pushes := &PushRecorder{}
	messageSvc := service.NewMessageService(messages, channels, nil)
	imhttp.RegisterMessageRoutes(authedAPI, messageSvc, imhttp.MessageRouteOpts{
		Pusher: pushes,
	})

	e := testutil.NewExpect(t, r)

	chID := int64(e.POST("/api/channels").
		WithHeader("Authorization", "Bearer "+ownerTok).
		WithJSON(map[string]any{
			"name":       "broadcast-bench",
			"member_ids": []int64{m2.ID, m3.ID, m4.ID, m5.ID},
		}).
		Expect().Status(201).JSON().Object().
		Value("id").Number().Raw())

	pushes.Reset() // discard anything from create-group fan-out

	e.POST("/api/channels/"+strconv.FormatInt(chID, 10)+"/messages").
		WithHeader("Authorization", "Bearer "+ownerTok).
		WithJSON(map[string]any{"content": "hello", "client_msg_id": "bench-1"}).
		Expect().Status(201)

	// pushToMembers runs in an unbounded goroutine; wait for it.
	waitForPushCount(t, pushes, 5, 2*time.Second)

	batches := pushes.BatchSnapshot()
	require.Len(t, batches, 1,
		"broadcast (visible_to=nil) must collapse to exactly ONE BroadcastMessage invocation; got %d", len(batches))
	require.Equal(t, chID, batches[0].ChannelID)
	require.ElementsMatch(t, []int64{owner.ID, m2.ID, m3.ID, m4.ID, m5.ID}, batches[0].UserIDs)
}

// TestBroadcast_DirectedMessageSplitsIntoTwoBatches pins the directed-message
// bucketing: visible recipients share the real payload in one batch, phantom
// recipients share a stripped payload in another. Still O(1) calls per
// sender (2 not N), not per-member.
func TestBroadcast_DirectedMessageSplitsIntoTwoBatches(t *testing.T) {
	dsn := containers.StartPostgres(t)
	db, err := repo.Open(repo.Config{DSN: dsn})
	require.NoError(t, err)
	t.Cleanup(func() {
		if sqlDB, e := db.DB(); e == nil {
			_ = sqlDB.Close()
		}
	})

	users := repo.NewUserRepo(db)
	channels := repo.NewChannelRepo(db)
	messages := repo.NewMessageRepo(db, channels)

	ctx := context.Background()
	mk := func(n string) *repo.User {
		u := &repo.User{Username: n, Email: n + "@x.com", PasswordHash: "x", DisplayName: n, Status: repo.UserStatusActive}
		require.NoError(t, users.Create(ctx, u))
		return u
	}
	sender := mk("snd")
	a := mk("a")
	b := mk("b")
	c := mk("c")
	senderTok, _ := auth.GenerateToken(integrationSecret, sender.ID, sender.Username)

	gin.SetMode(gin.TestMode)
	r := gin.New()
	authedAPI := r.Group("/api")
	authedAPI.Use(middleware.JWTGin(integrationSecret))
	imhttp.RegisterChannelRoutes(authedAPI, service.NewChannelService(channels, users, nil), nil)
	pushes := &PushRecorder{}
	imhttp.RegisterMessageRoutes(authedAPI,
		service.NewMessageService(messages, channels, nil),
		imhttp.MessageRouteOpts{Pusher: pushes})
	e := testutil.NewExpect(t, r)

	chID := int64(e.POST("/api/channels").
		WithHeader("Authorization", "Bearer "+senderTok).
		WithJSON(map[string]any{
			"name":       "directed",
			"member_ids": []int64{a.ID, b.ID, c.ID},
		}).
		Expect().Status(201).JSON().Object().Value("id").Number().Raw())

	pushes.Reset()

	// Send a message visible only to {a}. b + c should land in the phantom bucket.
	e.POST("/api/channels/"+strconv.FormatInt(chID, 10)+"/messages").
		WithHeader("Authorization", "Bearer "+senderTok).
		WithJSON(map[string]any{
			"content":       "secret",
			"client_msg_id": "dir-1",
			"visible_to":    []int64{a.ID},
		}).
		Expect().Status(201)

	waitForPushCount(t, pushes, 4, 2*time.Second)

	batches := pushes.BatchSnapshot()
	require.Len(t, batches, 2,
		"directed message must split into 1 visible batch + 1 phantom batch; got %d", len(batches))

	// Identify buckets by payload content — visible keeps the content, phantom strips it.
	var visibleBatch, phantomBatch PushBatch
	for _, b := range batches {
		if b.Msg.Content == "secret" {
			visibleBatch = b
		} else {
			phantomBatch = b
		}
	}
	require.ElementsMatch(t, []int64{sender.ID, a.ID}, visibleBatch.UserIDs,
		"visible bucket must contain sender + visible_to targets")
	require.ElementsMatch(t, []int64{b.ID, c.ID}, phantomBatch.UserIDs,
		"phantom bucket must contain everyone excluded from visible_to")
	require.Equal(t, repo.MsgTypePhantom, phantomBatch.Msg.MsgType,
		"phantom recipients must see msg_type=MsgTypePhantom")
	require.Empty(t, phantomBatch.Msg.Content,
		"phantom recipients must not see the real content")
}

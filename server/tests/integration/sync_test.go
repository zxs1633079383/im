//go:build integration

package integration

import (
	"context"
	"strconv"
	"testing"

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

// TestSync_FullFlow exercises the Phase 7.5 Gin sync slice end-to-end against
// a real Postgres: register two users → create DM → send 5 messages → call
// /api/sync with last_seq=2 → verify only the unseen messages 3-5 come back,
// alongside the right server_seq and unread count.
func TestSync_FullFlow(t *testing.T) {
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
	files := repo.NewFileRepo(db)

	alice := &repo.User{
		Username: "alice", Email: "a@x.com", PasswordHash: "x",
		DisplayName: "Alice", Status: repo.UserStatusActive,
	}
	bob := &repo.User{
		Username: "bob", Email: "b@x.com", PasswordHash: "x",
		DisplayName: "Bob", Status: repo.UserStatusActive,
	}
	require.NoError(t, users.Create(context.Background(), alice))
	require.NoError(t, users.Create(context.Background(), bob))

	aliceTok, err := auth.GenerateToken(integrationSecret, alice.ID, alice.Username)
	require.NoError(t, err)
	bobTok, err := auth.GenerateToken(integrationSecret, bob.ID, bob.Username)
	require.NoError(t, err)

	gin.SetMode(gin.TestMode)
	r := gin.New()
	authedAPI := r.Group("/api")
	authedAPI.Use(middleware.JWTGin(integrationSecret))
	imhttp.RegisterChannelRoutes(authedAPI, service.NewChannelService(channels, users), nil)
	imhttp.RegisterMessageRoutes(authedAPI,
		service.NewMessageService(messages, channels, files),
		imhttp.MessageRouteOpts{},
	)
	imhttp.RegisterSyncRoutes(authedAPI, service.NewSyncService(channels, messages), nil)

	e := testutil.NewExpect(t, r)

	// alice creates a DM with bob.
	chID := int64(e.POST("/api/channels/dm").
		WithHeader("Authorization", "Bearer "+aliceTok).
		WithJSON(map[string]int64{"peer_id": bob.ID}).
		Expect().Status(201).JSON().Object().
		Value("id").Number().Raw())
	require.NotZero(t, chID)
	chPath := strconv.FormatInt(chID, 10)

	// alice sends 5 messages back-to-back. Each gets a fresh client_msg_id so
	// the idempotency guard doesn't collapse them into one row.
	for i := 1; i <= 5; i++ {
		e.POST("/api/channels/"+chPath+"/messages").
			WithHeader("Authorization", "Bearer "+aliceTok).
			WithJSON(map[string]any{
				"content":       "msg-" + strconv.Itoa(i),
				"client_msg_id": "uuid-" + strconv.Itoa(i),
			}).
			Expect().Status(201)
	}

	// bob calls /api/sync with last_seq=2 — must receive msgs 3, 4, 5 only.
	resp := e.POST("/api/sync").
		WithHeader("Authorization", "Bearer "+bobTok).
		WithJSON(map[string]any{
			"channels": []any{
				map[string]any{"id": chID, "seq": 2},
			},
		}).
		Expect().Status(200).JSON().Object()

	chans := resp.Value("channels").Array()
	chans.Length().IsEqual(1)
	d := chans.Value(0).Object()
	d.Value("id").Number().IsEqual(chID)
	d.Value("server_seq").Number().IsEqual(5)
	// bob has read nothing yet → unread spans the full server_seq.
	d.Value("unread").Number().IsEqual(5)
	d.NotContainsKey("has_more") // small gap (3 < threshold)

	msgs := d.Value("messages").Array()
	msgs.Length().IsEqual(3)
	msgs.Value(0).Object().Value("seq").Number().IsEqual(3)
	msgs.Value(0).Object().Value("content").IsEqual("msg-3")
	msgs.Value(1).Object().Value("seq").Number().IsEqual(4)
	msgs.Value(2).Object().Value("seq").Number().IsEqual(5)

	// Sanity: a fully-caught-up cursor (seq=5) returns an empty deltas array.
	e.POST("/api/sync").
		WithHeader("Authorization", "Bearer "+bobTok).
		WithJSON(map[string]any{
			"channels": []any{map[string]any{"id": chID, "seq": 5}},
		}).
		Expect().Status(200).JSON().Object().
		Value("channels").Array().Length().IsEqual(0)
}

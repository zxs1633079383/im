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

// TestMessage_FullFlow exercises the Phase 7.4 Gin message slice end-to-end
// against a real Postgres: register two users → create DM → send a message →
// fetch it back → mark read → verify state. Mirrors the channel integration
// test's structure.
func TestMessage_FullFlow(t *testing.T) {
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

	// Seed two users.
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
	// Channel routes are needed to spin up the DM.
	imhttp.RegisterChannelRoutes(authedAPI, service.NewChannelService(channels, users, nil), nil)
	// Message routes — pusher/syncer left nil; the gateway hub isn't wired here.
	imhttp.RegisterMessageRoutes(authedAPI,
		service.NewMessageService(messages, channels, files),
		imhttp.MessageRouteOpts{},
	)

	e := testutil.NewExpect(t, r)

	// alice creates a DM with bob.
	chID := int64(e.POST("/api/channels/dm").
		WithHeader("Authorization", "Bearer "+aliceTok).
		WithJSON(map[string]int64{"peer_id": bob.ID}).
		Expect().Status(201).JSON().Object().
		Value("id").Number().Raw())
	require.NotZero(t, chID)
	chPath := strconv.FormatInt(chID, 10)

	// alice sends a message.
	msgObj := e.POST("/api/channels/"+chPath+"/messages").
		WithHeader("Authorization", "Bearer "+aliceTok).
		WithJSON(map[string]any{"content": "hello bob", "client_msg_id": "uuid-1"}).
		Expect().Status(201).JSON().Object()
	msgObj.Value("content").IsEqual("hello bob")
	seq := int64(msgObj.Value("seq").Number().Raw())
	require.Equal(t, int64(1), seq, "first message in fresh DM gets seq=1")

	// Idempotent re-send returns 201 with the same seq (no duplicate row).
	e.POST("/api/channels/"+chPath+"/messages").
		WithHeader("Authorization", "Bearer "+aliceTok).
		WithJSON(map[string]any{"content": "hello bob", "client_msg_id": "uuid-1"}).
		Expect().Status(201).JSON().Object().
		Value("seq").Number().IsEqual(1)

	// bob fetches messages — sees the one alice sent.
	bobMsgs := e.GET("/api/channels/"+chPath+"/messages").
		WithHeader("Authorization", "Bearer "+bobTok).
		Expect().Status(200).JSON().Object().
		Value("messages").Array()
	bobMsgs.Length().IsEqual(1)
	bobMsgs.Value(0).Object().Value("content").IsEqual("hello bob")

	// bob marks the channel read at seq=1.
	e.POST("/api/channels/"+chPath+"/read").
		WithHeader("Authorization", "Bearer "+bobTok).
		Expect().Status(200).JSON().Object().
		Value("seq").Number().IsEqual(1)

	// after_seq=1 returns nothing (empty array, not null).
	e.GET("/api/channels/"+chPath+"/messages").
		WithQuery("after_seq", 1).
		WithHeader("Authorization", "Bearer "+bobTok).
		Expect().Status(200).JSON().Object().
		Value("messages").Array().Length().IsEqual(0)

	// A non-member (alice creates a *new* user "carol" never added to the DM)
	// gets 403 on send.
	carol := &repo.User{
		Username: "carol", Email: "c@x.com", PasswordHash: "x",
		DisplayName: "Carol", Status: repo.UserStatusActive,
	}
	require.NoError(t, users.Create(context.Background(), carol))
	carolTok, err := auth.GenerateToken(integrationSecret, carol.ID, carol.Username)
	require.NoError(t, err)
	e.POST("/api/channels/"+chPath+"/messages").
		WithHeader("Authorization", "Bearer "+carolTok).
		WithJSON(map[string]any{"content": "intruder"}).
		Expect().Status(403)
}

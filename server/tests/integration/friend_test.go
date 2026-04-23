//go:build integration

package integration

import (
	"context"
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

func TestFriend_FullFlow(t *testing.T) {
	dsn := containers.StartPostgres(t)
	db, err := repo.Open(repo.Config{DSN: dsn})
	require.NoError(t, err)
	t.Cleanup(func() {
		if sqlDB, e := db.DB(); e == nil {
			_ = sqlDB.Close()
		}
	})

	users := repo.NewUserRepo(db)
	friends := repo.NewFriendshipRepo(db)

	// Seed two users so we can exercise the request → accept → list flow.
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
	require.NotZero(t, alice.ID)
	require.NotZero(t, bob.ID)

	aliceTok, err := auth.GenerateToken(integrationSecret, alice.ID, alice.Username)
	require.NoError(t, err)
	bobTok, err := auth.GenerateToken(integrationSecret, bob.ID, bob.Username)
	require.NoError(t, err)

	gin.SetMode(gin.TestMode)
	r := gin.New()
	authedAPI := r.Group("/api")
	authedAPI.Use(middleware.JWTGin(integrationSecret))
	// nil pusher: the WebSocket hub is not wired in this test.
	imhttp.RegisterFriendRoutes(authedAPI, service.NewFriendService(friends, users), nil)

	e := testutil.NewExpect(t, r)

	// alice → bob friend request.
	e.POST("/api/friends/request").
		WithHeader("Authorization", "Bearer "+aliceTok).
		WithJSON(map[string]int64{"addressee_id": bob.ID}).
		Expect().Status(201).JSON().Object().
		Value("status").IsEqual("pending")

	// Re-sending the same request collapses to 409 (unique-violation).
	e.POST("/api/friends/request").
		WithHeader("Authorization", "Bearer "+aliceTok).
		WithJSON(map[string]int64{"addressee_id": bob.ID}).
		Expect().Status(409)

	// bob sees the inbound pending request.
	pending := e.GET("/api/friends/pending").
		WithHeader("Authorization", "Bearer "+bobTok).
		Expect().Status(200).JSON().Array()
	pending.Length().IsEqual(1)
	friendshipID := int64(pending.Value(0).Object().Value("id").Number().Raw())
	require.NotZero(t, friendshipID)

	// alice cannot accept her own outbound request → 404.
	e.POST("/api/friends/accept").
		WithHeader("Authorization", "Bearer "+aliceTok).
		WithJSON(map[string]int64{"friendship_id": friendshipID}).
		Expect().Status(404)

	// bob accepts.
	e.POST("/api/friends/accept").
		WithHeader("Authorization", "Bearer "+bobTok).
		WithJSON(map[string]int64{"friendship_id": friendshipID}).
		Expect().Status(200).JSON().Object().
		Value("status").IsEqual("accepted")

	// Both directions now show one friend.
	e.GET("/api/friends").
		WithHeader("Authorization", "Bearer "+aliceTok).
		Expect().Status(200).JSON().Array().Length().IsEqual(1)
	e.GET("/api/friends").
		WithHeader("Authorization", "Bearer "+bobTok).
		Expect().Status(200).JSON().Array().Length().IsEqual(1)

	// Search excludes the caller; alice queries "bob" and gets bob.
	e.GET("/api/users/search").
		WithQuery("q", "bob").
		WithHeader("Authorization", "Bearer "+aliceTok).
		Expect().Status(200).JSON().Array().Length().IsEqual(1)

	// Block flips the row; ListFriends now returns 0 for alice.
	e.POST("/api/friends/block").
		WithHeader("Authorization", "Bearer "+aliceTok).
		WithJSON(map[string]int64{"user_id": bob.ID}).
		Expect().Status(200).JSON().Object().
		Value("status").IsEqual("blocked")
	e.GET("/api/friends").
		WithHeader("Authorization", "Bearer "+aliceTok).
		Expect().Status(200).JSON().Array().Length().IsEqual(0)
}

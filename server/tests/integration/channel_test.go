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

// TestChannel_FullFlow exercises the Phase 7.3 Gin channel slice end-to-end
// against a real Postgres: create group → add member → list channels → list
// members → leave. Mirrors the friend integration test's structure.
func TestChannel_FullFlow(t *testing.T) {
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

	// Seed three users so we can exercise add/remove/leave.
	alice := &repo.User{
		Username: "alice", Email: "a@x.com", PasswordHash: "x",
		DisplayName: "Alice", Status: repo.UserStatusActive,
	}
	bob := &repo.User{
		Username: "bob", Email: "b@x.com", PasswordHash: "x",
		DisplayName: "Bob", Status: repo.UserStatusActive,
	}
	carol := &repo.User{
		Username: "carol", Email: "c@x.com", PasswordHash: "x",
		DisplayName: "Carol", Status: repo.UserStatusActive,
	}
	require.NoError(t, users.Create(context.Background(), alice))
	require.NoError(t, users.Create(context.Background(), bob))
	require.NoError(t, users.Create(context.Background(), carol))

	aliceTok, err := auth.GenerateToken(integrationSecret, alice.ID, alice.Username)
	require.NoError(t, err)
	bobTok, err := auth.GenerateToken(integrationSecret, bob.ID, bob.Username)
	require.NoError(t, err)

	gin.SetMode(gin.TestMode)
	r := gin.New()
	authedAPI := r.Group("/api")
	authedAPI.Use(middleware.JWTGin(integrationSecret))
	// nil pusher: the hub isn't wired in this test.
	imhttp.RegisterChannelRoutes(authedAPI, service.NewChannelService(channels, users), nil)

	e := testutil.NewExpect(t, r)

	// alice creates a group with bob pre-seeded as a member.
	chID := int64(e.POST("/api/channels").
		WithHeader("Authorization", "Bearer "+aliceTok).
		WithJSON(map[string]any{
			"name":       "team",
			"member_ids": []int64{bob.ID},
		}).
		Expect().Status(201).JSON().Object().
		Value("id").Number().Raw())
	require.NotZero(t, chID)

	// alice's channel list should show the new group.
	e.GET("/api/channels").
		WithHeader("Authorization", "Bearer "+aliceTok).
		Expect().Status(200).JSON().Array().Length().IsEqual(1)

	// alice adds carol.
	e.POST("/api/channels/"+strconv.FormatInt(chID, 10)+"/members").
		WithHeader("Authorization", "Bearer "+aliceTok).
		WithJSON(map[string]int64{"user_id": carol.ID}).
		Expect().Status(201).JSON().Object().
		Value("status").IsEqual("added")

	// list members — owner + bob + carol.
	e.GET("/api/channels/"+strconv.FormatInt(chID, 10)+"/members").
		WithHeader("Authorization", "Bearer "+aliceTok).
		Expect().Status(200).JSON().Array().Length().IsEqual(3)

	// bob is a plain member — cannot update the channel (admin/owner only).
	e.PUT("/api/channels/"+strconv.FormatInt(chID, 10)).
		WithHeader("Authorization", "Bearer "+bobTok).
		WithJSON(map[string]string{"name": "renamed"}).
		Expect().Status(403)

	// alice (owner) renames it.
	e.PUT("/api/channels/"+strconv.FormatInt(chID, 10)).
		WithHeader("Authorization", "Bearer "+aliceTok).
		WithJSON(map[string]string{"name": "renamed"}).
		Expect().Status(200).JSON().Object().
		Value("name").IsEqual("renamed")

	// alice (owner) cannot leave — must transfer first.
	e.POST("/api/channels/"+strconv.FormatInt(chID, 10)+"/leave").
		WithHeader("Authorization", "Bearer "+aliceTok).
		Expect().Status(403)

	// bob (member) leaves cleanly.
	e.POST("/api/channels/"+strconv.FormatInt(chID, 10)+"/leave").
		WithHeader("Authorization", "Bearer "+bobTok).
		Expect().Status(200).JSON().Object().
		Value("status").IsEqual("left")

	// bob is no longer a member → cannot list.
	e.GET("/api/channels/"+strconv.FormatInt(chID, 10)+"/members").
		WithHeader("Authorization", "Bearer "+bobTok).
		Expect().Status(403)

	// alice + carol still see the channel in their lists.
	e.GET("/api/channels").
		WithHeader("Authorization", "Bearer "+aliceTok).
		Expect().Status(200).JSON().Array().Length().IsEqual(1)
}


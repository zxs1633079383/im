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

// TestFavorite_FullFlow exercises the Phase 7.8 Gin favorite slice end-to-end
// against a real Postgres: register a user, create a channel + send a message
// directly via the repos, then add → list → remove → list-empty over the API.
//
// We seed the message via repos (rather than over the channel/message HTTP
// API) to keep this test focused on the favorite cut-over — channel +
// message slices already have their own integration tests.
func TestFavorite_FullFlow(t *testing.T) {
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
	favorites := repo.NewFavoriteRepo(db)

	ctx := context.Background()

	// Seed alice + a self-channel + a message she'll favorite.
	alice := &repo.User{
		Username: "alice", Email: "a@x.com", PasswordHash: "x",
		DisplayName: "Alice", Status: repo.UserStatusActive,
	}
	require.NoError(t, users.Create(ctx, alice))

	ch := &repo.Channel{
		Name:      "favs",
		Type:      repo.ChannelTypeGroup,
		CreatorID: &alice.ID,
	}
	require.NoError(t, channels.Create(ctx, ch))
	require.NoError(t, channels.AddMember(ctx, ch.ID, alice.ID, repo.MemberRoleOwner))

	msg := &repo.Message{
		ChannelID: ch.ID,
		SenderID:  alice.ID,
		Content:   "favorite me",
		MsgType:   1,
	}
	require.NoError(t, messages.Send(ctx, msg))
	require.NotZero(t, msg.ID)

	aliceTok, err := auth.GenerateToken(integrationSecret, alice.ID, alice.Username)
	require.NoError(t, err)

	gin.SetMode(gin.TestMode)
	r := gin.New()
	authedAPI := r.Group("/api")
	authedAPI.Use(middleware.JWTGin(integrationSecret))
	imhttp.RegisterFavoriteRoutes(authedAPI, service.NewFavoriteService(favorites))

	e := testutil.NewExpect(t, r)
	msgPath := strconv.FormatInt(msg.ID, 10)

	// Initially empty.
	e.GET("/api/favorites").
		WithHeader("Authorization", "Bearer "+aliceTok).
		Expect().Status(200).JSON().Object().
		Value("favorites").Array().IsEmpty()

	// Add → 201 with status:ok.
	e.POST("/api/favorites/"+msgPath).
		WithHeader("Authorization", "Bearer "+aliceTok).
		Expect().Status(201).JSON().Object().
		Value("status").IsEqual("ok")

	// Idempotent — second add still returns 201, list still has length 1.
	e.POST("/api/favorites/"+msgPath).
		WithHeader("Authorization", "Bearer "+aliceTok).
		Expect().Status(201)

	// List — one favorite, joined to the message body.
	resp := e.GET("/api/favorites").
		WithHeader("Authorization", "Bearer "+aliceTok).
		Expect().Status(200).JSON().Object()
	favs := resp.Value("favorites").Array()
	favs.Length().IsEqual(1)
	favs.Value(0).Object().Value("message_id").Number().IsEqual(msg.ID)
	favs.Value(0).Object().Value("message").Object().Value("content").IsEqual("favorite me")

	// Remove → 204.
	e.DELETE("/api/favorites/"+msgPath).
		WithHeader("Authorization", "Bearer "+aliceTok).
		Expect().Status(204)

	// Removing again — service surfaces ErrNotFound, handler returns 404.
	e.DELETE("/api/favorites/"+msgPath).
		WithHeader("Authorization", "Bearer "+aliceTok).
		Expect().Status(404)

	// List back to empty.
	e.GET("/api/favorites").
		WithHeader("Authorization", "Bearer "+aliceTok).
		Expect().Status(200).JSON().Object().
		Value("favorites").Array().IsEmpty()
}

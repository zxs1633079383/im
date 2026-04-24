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

// TestSearch_FullFlow exercises the Phase 7.6 Gin search slice end-to-end
// against a real Postgres: register users → create a group channel → send a
// message that mentions a known token → call /api/search?q=token and verify
// the response's three categories return what we expect.
//
// The legacy SearchHandler covered all three lookups in one request; the
// integration test mirrors that shape so a regression in any branch (FTS for
// messages, ILIKE for users/channels) fails loudly.
func TestSearch_FullFlow(t *testing.T) {
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
	search := repo.NewSearchRepo(db)

	// Three users: alice (caller), bob (a "matching" user — display name +
	// username share the search token), eve (control — should never appear).
	alice := &repo.User{
		Username: "alice", Email: "a@x.com", PasswordHash: "x",
		DisplayName: "Alice", Status: repo.UserStatusActive,
	}
	bob := &repo.User{
		Username: "bobwonder", Email: "b@x.com", PasswordHash: "x",
		DisplayName: "Bob Wonder", Status: repo.UserStatusActive,
	}
	eve := &repo.User{
		Username: "eve", Email: "e@x.com", PasswordHash: "x",
		DisplayName: "Eve", Status: repo.UserStatusActive,
	}
	require.NoError(t, users.Create(context.Background(), alice))
	require.NoError(t, users.Create(context.Background(), bob))
	require.NoError(t, users.Create(context.Background(), eve))

	aliceTok, err := auth.GenerateToken(integrationSecret, alice.ID, alice.Username)
	require.NoError(t, err)

	gin.SetMode(gin.TestMode)
	r := gin.New()
	authedAPI := r.Group("/api")
	authedAPI.Use(middleware.JWTGin(integrationSecret))
	imhttp.RegisterChannelRoutes(authedAPI, service.NewChannelService(channels, users, nil), nil)
	imhttp.RegisterMessageRoutes(authedAPI,
		service.NewMessageService(messages, channels, files),
		imhttp.MessageRouteOpts{},
	)
	imhttp.RegisterSearchRoutes(authedAPI, service.NewSearchService(search), nil)

	e := testutil.NewExpect(t, r)

	// alice creates a group channel named "wonder-team" so the channel
	// search has something to match. bob is a member; eve is not.
	chID := int64(e.POST("/api/channels").
		WithHeader("Authorization", "Bearer "+aliceTok).
		WithJSON(map[string]any{
			"name":       "wonder-team",
			"member_ids": []int64{bob.ID},
		}).
		Expect().Status(201).JSON().Object().
		Value("id").Number().Raw())
	require.NotZero(t, chID)
	chPath := strconv.FormatInt(chID, 10)

	// alice posts one matching and one non-matching message so the FTS
	// branch has something to find — and we can prove it filters. Use the
	// bare token "wonder" so the Postgres simple-FTS tokeniser indexes it
	// (FTS does not do prefix matching unless we switch configs).
	e.POST("/api/channels/"+chPath+"/messages").
		WithHeader("Authorization", "Bearer "+aliceTok).
		WithJSON(map[string]any{
			"content":       "wonder is in the air",
			"client_msg_id": "uuid-match",
		}).
		Expect().Status(201)
	e.POST("/api/channels/"+chPath+"/messages").
		WithHeader("Authorization", "Bearer "+aliceTok).
		WithJSON(map[string]any{
			"content":       "lunch plans",
			"client_msg_id": "uuid-nomatch",
		}).
		Expect().Status(201)

	// alice searches for "wonder" across all categories. The legacy
	// envelope ships keys for every requested category, so the response is
	// {messages: [...], users: [...], channels: [...]}.
	resp := e.GET("/api/search").
		WithHeader("Authorization", "Bearer "+aliceTok).
		WithQuery("q", "wonder").
		Expect().Status(200).JSON().Object()
	resp.ContainsKey("messages")
	resp.ContainsKey("users")
	resp.ContainsKey("channels")

	// Messages: only the message containing the "wonder" token matches; the
	// joined channel name comes from MessageSearchResult.ChannelName.
	msgs := resp.Value("messages").Array()
	msgs.Length().IsEqual(1)
	msgs.Value(0).Object().Value("content").IsEqual("wonder is in the air")
	msgs.Value(0).Object().Value("channel_name").IsEqual("wonder-team")

	// Users: the caller is excluded (legacy parity); bob's display_name
	// "Bob Wonder" matches; eve doesn't.
	users0 := resp.Value("users").Array()
	users0.Length().IsEqual(1)
	users0.Value(0).Object().Value("id").Number().IsEqual(bob.ID)

	// Channels: the group channel "wonder-team" matches.
	chans := resp.Value("channels").Array()
	chans.Length().IsEqual(1)
	chans.Value(0).Object().Value("id").Number().IsEqual(chID)

	// Type filter narrows to one category — sister keys must be absent.
	usersOnly := e.GET("/api/search").
		WithHeader("Authorization", "Bearer "+aliceTok).
		WithQuery("q", "wonder").
		WithQuery("type", "users").
		Expect().Status(200).JSON().Object()
	usersOnly.NotContainsKey("messages")
	usersOnly.NotContainsKey("channels")
	usersOnly.Value("users").Array().Length().IsEqual(1)

	// channel_id filter restricts message FTS to one channel — same query
	// for a different channel id returns zero hits.
	scoped := e.GET("/api/search").
		WithHeader("Authorization", "Bearer "+aliceTok).
		WithQuery("q", "wonder").
		WithQuery("type", "messages").
		WithQuery("channel_id", strconv.FormatInt(chID+999, 10)).
		Expect().Status(200).JSON().Object()
	scoped.Value("messages").Array().Length().IsEqual(0)
}

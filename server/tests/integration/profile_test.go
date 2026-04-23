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

func TestProfile_FullFlow(t *testing.T) {
	dsn := containers.StartPostgres(t)
	db, err := repo.Open(repo.Config{DSN: dsn})
	require.NoError(t, err)
	t.Cleanup(func() {
		if sqlDB, e := db.DB(); e == nil {
			_ = sqlDB.Close()
		}
	})

	users := repo.NewUserRepo(db)

	// Seed a user directly via the repo so we have a known ID + token.
	u := &repo.User{
		Username:     "carol",
		Email:        "c@x.com",
		PasswordHash: "x", // not exercised in this test
		DisplayName:  "Carol",
		Status:       repo.UserStatusActive,
	}
	require.NoError(t, users.Create(context.Background(), u))
	require.NotZero(t, u.ID)

	tok, err := auth.GenerateToken(integrationSecret, u.ID, u.Username)
	require.NoError(t, err)

	gin.SetMode(gin.TestMode)
	r := gin.New()
	authedAPI := r.Group("/api")
	authedAPI.Use(middleware.JWTGin(integrationSecret))
	imhttp.RegisterProfileRoutes(authedAPI, service.NewProfileService(users))

	e := testutil.NewExpect(t, r)

	// Update display name + avatar.
	e.PUT("/api/users/me").
		WithHeader("Authorization", "Bearer "+tok).
		WithJSON(map[string]string{
			"display_name": "Carol Updated",
			"avatar_url":   "https://example.com/c.png",
		}).
		Expect().Status(200).JSON().Object().
		Value("display_name").IsEqual("Carol Updated")

	// Verify persisted via the repo.
	got, err := users.GetByID(context.Background(), u.ID)
	require.NoError(t, err)
	require.Equal(t, "Carol Updated", got.DisplayName)
	require.Equal(t, "https://example.com/c.png", got.AvatarURL)

	// Bad URL → 422.
	e.PUT("/api/users/me").
		WithHeader("Authorization", "Bearer "+tok).
		WithJSON(map[string]string{"avatar_url": "javascript:alert(1)"}).
		Expect().Status(422)

	// No token → 401.
	e.PUT("/api/users/me").
		WithJSON(map[string]string{"display_name": "x"}).
		Expect().Status(401)
}

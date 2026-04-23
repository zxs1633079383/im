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

func TestSettings_FullFlow(t *testing.T) {
	dsn := containers.StartPostgres(t)
	db, err := repo.Open(repo.Config{DSN: dsn})
	require.NoError(t, err)
	t.Cleanup(func() {
		if sqlDB, e := db.DB(); e == nil {
			_ = sqlDB.Close()
		}
	})

	users := repo.NewUserRepo(db)
	settings := repo.NewUserSettingsRepo(db)

	// Seed a user — settings rows are created on demand by Upsert.
	u := &repo.User{
		Username:     "dave",
		Email:        "d@x.com",
		PasswordHash: "x",
		DisplayName:  "Dave",
		Status:       repo.UserStatusActive,
	}
	require.NoError(t, users.Create(context.Background(), u))

	tok, err := auth.GenerateToken(integrationSecret, u.ID, u.Username)
	require.NoError(t, err)

	gin.SetMode(gin.TestMode)
	r := gin.New()
	authedAPI := r.Group("/api")
	authedAPI.Use(middleware.JWTGin(integrationSecret))
	imhttp.RegisterSettingsRoutes(authedAPI, service.NewSettingsService(settings))

	e := testutil.NewExpect(t, r)

	// GET with no row yet → defaults (theme=system, language=zh).
	obj := e.GET("/api/settings").
		WithHeader("Authorization", "Bearer "+tok).
		Expect().Status(200).JSON().Object()
	obj.Value("theme").IsEqual("system")
	obj.Value("language").IsEqual("zh")
	obj.Value("notification_enabled").IsEqual(true)

	// PUT updates theme; everything else preserved.
	e.PUT("/api/settings").
		WithHeader("Authorization", "Bearer "+tok).
		WithJSON(map[string]string{"theme": "dark"}).
		Expect().Status(200).JSON().Object().
		Value("theme").IsEqual("dark")

	// Subsequent GET returns the persisted row.
	obj2 := e.GET("/api/settings").
		WithHeader("Authorization", "Bearer "+tok).
		Expect().Status(200).JSON().Object()
	obj2.Value("theme").IsEqual("dark")
	obj2.Value("language").IsEqual("zh")

	// PUT toggles notification_enabled to false.
	e.PUT("/api/settings").
		WithHeader("Authorization", "Bearer "+tok).
		WithJSON(map[string]any{"notification_enabled": false}).
		Expect().Status(200).JSON().Object().
		Value("notification_enabled").IsEqual(false)

	// Verify persisted.
	got, err := settings.Get(context.Background(), u.ID)
	require.NoError(t, err)
	require.Equal(t, "dark", got.Theme)
	require.False(t, got.NotificationEnabled)

	// No token → 401 on both endpoints.
	e.GET("/api/settings").Expect().Status(401)
	e.PUT("/api/settings").
		WithJSON(map[string]string{"theme": "light"}).
		Expect().Status(401)
}

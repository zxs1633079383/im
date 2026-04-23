//go:build integration

// Package integration drives the new Gin handlers end-to-end against real
// backing services (Postgres via testcontainers). This file covers the
// Phase 6 auth slice cut-over.
package integration

import (
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"

	imhttp "im-server/internal/http"
	"im-server/internal/repo"
	"im-server/internal/service"
	"im-server/internal/testutil"
	"im-server/internal/testutil/containers"
)

const integrationSecret = "integration-secret-32-bytes-minimum!"

func TestAuth_FullFlow(t *testing.T) {
	dsn := containers.StartPostgres(t)
	db, err := repo.Open(repo.Config{DSN: dsn})
	require.NoError(t, err)
	t.Cleanup(func() {
		if sqlDB, e := db.DB(); e == nil {
			_ = sqlDB.Close()
		}
	})

	users := repo.NewUserRepo(db)

	gin.SetMode(gin.TestMode)
	r := gin.New()
	imhttp.RegisterAuthRoutes(r, service.NewAuthService(users, integrationSecret), users, integrationSecret)

	e := testutil.NewExpect(t, r)

	// register bob
	e.POST("/api/auth/register").
		WithJSON(map[string]string{
			"username": "bob",
			"email":    "b@x.com",
			"password": "pwd12345",
		}).
		Expect().Status(201)

	// duplicate username -> 409
	e.POST("/api/auth/register").
		WithJSON(map[string]string{
			"username": "bob",
			"email":    "other@x.com",
			"password": "pwd12345",
		}).
		Expect().Status(409)

	// duplicate email -> 409
	e.POST("/api/auth/register").
		WithJSON(map[string]string{
			"username": "bob2",
			"email":    "b@x.com",
			"password": "pwd12345",
		}).
		Expect().Status(409)

	// login by username
	tok := e.POST("/api/auth/login").
		WithJSON(map[string]string{"login": "bob", "password": "pwd12345"}).
		Expect().Status(200).JSON().Object().
		Value("token").String().NotEmpty().Raw()

	// login by email also works
	e.POST("/api/auth/login").
		WithJSON(map[string]string{"login": "b@x.com", "password": "pwd12345"}).
		Expect().Status(200)

	// /me with the freshly issued token
	e.GET("/api/auth/me").
		WithHeader("Authorization", "Bearer "+tok).
		Expect().Status(200).JSON().Object().
		Value("username").IsEqual("bob")

	// /me without a token -> 401
	e.GET("/api/auth/me").Expect().Status(401)

	// login wrong password -> 401
	e.POST("/api/auth/login").
		WithJSON(map[string]string{"login": "bob", "password": "wrong"}).
		Expect().Status(401)
}

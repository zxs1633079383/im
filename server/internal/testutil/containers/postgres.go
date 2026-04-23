//go:build integration

// Package containers provides testcontainers-go helpers for spinning up
// real backing services (PostgreSQL, Redis, Pulsar) in Go integration tests.
// All helpers register t.Cleanup so callers don't leak containers.
package containers

import (
	"context"
	"path/filepath"
	"runtime"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"github.com/testcontainers/testcontainers-go"
	"github.com/testcontainers/testcontainers-go/modules/postgres"
	"github.com/testcontainers/testcontainers-go/wait"
)

// StartPostgres launches a Postgres 16 container with the project's initial
// migration applied (server/migrations/001_init.up.sql). Returns the DSN.
// Cleanup is registered automatically.
func StartPostgres(t *testing.T) string {
	t.Helper()
	ctx := context.Background()

	_, file, _, _ := runtime.Caller(0)
	migrationsDir := filepath.Join(filepath.Dir(file), "..", "..", "..", "migrations")

	pg, err := postgres.Run(ctx, "postgres:16-alpine",
		postgres.WithDatabase("im_test"),
		postgres.WithUsername("test"),
		postgres.WithPassword("test"),
		postgres.WithInitScripts(filepath.Join(migrationsDir, "001_init.up.sql")),
		testcontainers.WithWaitStrategy(
			wait.ForLog("database system is ready to accept connections").
				WithOccurrence(2).WithStartupTimeout(60*time.Second)),
	)
	require.NoError(t, err)

	t.Cleanup(func() { _ = pg.Terminate(ctx) })

	dsn, err := pg.ConnectionString(ctx, "sslmode=disable")
	require.NoError(t, err)
	return dsn
}

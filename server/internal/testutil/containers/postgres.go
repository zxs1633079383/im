//go:build integration

// Package containers provides testcontainers-go helpers for spinning up
// real backing services (PostgreSQL, Redis, Pulsar) in Go integration tests.
// All helpers register t.Cleanup so callers don't leak containers.
package containers

import (
	"context"
	"path/filepath"
	"runtime"
	"sort"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"github.com/testcontainers/testcontainers-go"
	"github.com/testcontainers/testcontainers-go/modules/postgres"
	"github.com/testcontainers/testcontainers-go/wait"
)

// StartPostgres launches a Postgres 16 container with every numbered
// `*.up.sql` migration in server/migrations/ applied in lexical order.
// Returns the DSN. Cleanup is registered automatically.
//
// V3/V5 integration tests rely on all tables/columns being present (including
// M1 soft-delete + indices from 002_m1_message_lifecycle.up.sql), so we load
// the full migration chain rather than just 001_init.
func StartPostgres(t *testing.T) string {
	t.Helper()
	ctx := context.Background()

	_, file, _, _ := runtime.Caller(0)
	migrationsDir := filepath.Join(filepath.Dir(file), "..", "..", "..", "migrations")

	scripts, err := filepath.Glob(filepath.Join(migrationsDir, "*.up.sql"))
	require.NoError(t, err)
	require.NotEmpty(t, scripts, "no *.up.sql files in %s", migrationsDir)
	sort.Strings(scripts)

	pg, err := postgres.Run(ctx, "postgres:16-alpine",
		postgres.WithDatabase("im_test"),
		postgres.WithUsername("test"),
		postgres.WithPassword("test"),
		postgres.WithInitScripts(scripts...),
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

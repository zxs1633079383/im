//go:build integration

package repo

import (
	"testing"

	"github.com/stretchr/testify/require"
	"im-server/internal/testutil/containers"
)

func TestOpen_Smoke(t *testing.T) {
	dsn := containers.StartPostgres(t)
	db, err := Open(Config{DSN: dsn})
	require.NoError(t, err)
	require.NotNil(t, db)
	sqlDB, err := db.DB()
	require.NoError(t, err)
	require.NoError(t, sqlDB.Ping())
}

//go:build integration

package containers

import (
	"database/sql"
	"testing"

	_ "github.com/jackc/pgx/v5/stdlib"
	"github.com/stretchr/testify/require"
)

func TestStartPostgres_Smoke(t *testing.T) {
	if testing.Short() {
		t.Skip("requires docker")
	}
	dsn := StartPostgres(t)
	db, err := sql.Open("pgx", dsn)
	require.NoError(t, err)
	defer db.Close()

	require.NoError(t, db.Ping())

	var n int
	require.NoError(t, db.QueryRow("SELECT count(*) FROM users").Scan(&n))
	require.Equal(t, 0, n, "fresh container should have no users")
}

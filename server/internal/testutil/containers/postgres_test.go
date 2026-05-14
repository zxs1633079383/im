//go:build integration

package containers

import (
	"database/sql"
	"testing"

	_ "github.com/lib/pq"
	"github.com/stretchr/testify/require"
)

func TestStartPostgres_Smoke(t *testing.T) {
	if testing.Short() {
		t.Skip("requires docker")
	}
	dsn := StartPostgres(t)
	db, err := sql.Open("postgres", dsn)
	require.NoError(t, err)
	defer db.Close()

	require.NoError(t, db.Ping())

	// v0.7.4: users 表已下线 (UserData 移到 mm 外部仓库)。改用 channels 表做容器
	// smoke (channels 是 im 自管的核心表，container init 应能跑通 migrations 创建)。
	var n int
	require.NoError(t, db.QueryRow("SELECT count(*) FROM channels").Scan(&n))
	require.Equal(t, 0, n, "fresh container should have no channels")
}

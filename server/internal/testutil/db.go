package testutil

import (
	"context"
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"im-server/internal/store"
)

// PGPool 返回一个连接到测试数据库的连接池。
// 如果 IM_TEST_PG_DSN 未设置则跳过测试。
// 每次调用会清空所有业务表数据。
func PGPool(t *testing.T) *pgxpool.Pool {
	t.Helper()

	dsn := os.Getenv("IM_TEST_PG_DSN")
	if dsn == "" {
		t.Skip("IM_TEST_PG_DSN not set")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	pool, err := store.NewPGPool(ctx, dsn, 5)
	if err != nil {
		t.Fatalf("connect to test PG: %v", err)
	}

	t.Cleanup(func() { pool.Close() })

	cleanTables(t, pool)
	return pool
}

func cleanTables(t *testing.T, pool *pgxpool.Pool) {
	t.Helper()
	tables := []string{
		"message_favorites",
		"message_attachments",
		"files",
		"messages",
		"channel_members",
		"channels",
		"friendships",
		"user_settings",
		"users",
	}
	for _, table := range tables {
		_, err := pool.Exec(context.Background(), fmt.Sprintf("TRUNCATE TABLE %s CASCADE", table))
		if err != nil {
			t.Fatalf("truncate %s: %v", table, err)
		}
	}
}

package store

import (
	"context"
	"os"
	"testing"
	"time"
)

func TestNewPGPool(t *testing.T) {
	dsn := os.Getenv("IM_TEST_PG_DSN")
	if dsn == "" {
		t.Skip("IM_TEST_PG_DSN not set, skipping PG integration test")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	pool, err := NewPGPool(ctx, dsn, 5)
	if err != nil {
		t.Fatalf("NewPGPool() error: %v", err)
	}
	defer pool.Close()

	var result int
	err = pool.QueryRow(ctx, "SELECT 1").Scan(&result)
	if err != nil {
		t.Fatalf("ping query error: %v", err)
	}
	if result != 1 {
		t.Errorf("SELECT 1 = %d, want 1", result)
	}
}

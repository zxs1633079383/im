package repo

import (
	"context"
	"fmt"

	"github.com/redis/go-redis/v9"
)

// OpenRedis returns a configured Redis client. Caller should Close on shutdown.
//
// Mirrors the legacy store.NewRedisClient signature: same options, same
// ping check on the supplied context. OTel instrumentation is intentionally
// omitted here — Phase 8 wires that in across the stack.
func OpenRedis(ctx context.Context, addr, password string, db int) (*redis.Client, error) {
	client := redis.NewClient(&redis.Options{
		Addr:     addr,
		Password: password,
		DB:       db,
	})

	if err := client.Ping(ctx).Err(); err != nil {
		_ = client.Close()
		return nil, fmt.Errorf("ping redis: %w", err)
	}

	return client, nil
}

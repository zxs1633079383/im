package repo

import (
	"context"
	"fmt"

	"github.com/redis/go-redis/v9"
)

// RedisOptions configures OpenRedis.
//
//   - Addrs: seed node list. Cluster mode auto-discovers the rest from one
//     seed; Single mode uses Addrs[0].
//   - DB: single-node only. Redis Cluster only supports DB 0, so pre/prod
//     isolate im state via key prefixes (see routing.go).
//   - Cluster: force Cluster mode even with a single headless-service DNS
//     entry. Required on pre/prod where headless DNS returns one A record.
type RedisOptions struct {
	Addrs    []string
	Password string
	DB       int
	Cluster  bool
}

// OpenRedis returns a redis.UniversalClient (Cluster or Single depending on
// opts.Cluster). Caller must Close on shutdown.
//
// The Ping uses the caller-supplied ctx so startup timeouts are explicit.
func OpenRedis(ctx context.Context, opts RedisOptions) (redis.UniversalClient, error) {
	if len(opts.Addrs) == 0 {
		return nil, fmt.Errorf("redis: at least one addr required")
	}
	var client redis.UniversalClient
	if opts.Cluster {
		client = redis.NewClusterClient(&redis.ClusterOptions{
			Addrs:    opts.Addrs,
			Password: opts.Password,
		})
	} else {
		client = redis.NewClient(&redis.Options{
			Addr:     opts.Addrs[0],
			Password: opts.Password,
			DB:       opts.DB,
		})
	}
	if err := client.Ping(ctx).Err(); err != nil {
		_ = client.Close()
		return nil, fmt.Errorf("ping redis: %w", err)
	}
	return client, nil
}

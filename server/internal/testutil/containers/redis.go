//go:build integration

package containers

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"
	tcredis "github.com/testcontainers/testcontainers-go/modules/redis"
)

// StartRedis launches a Redis 7 container and returns its connection URI
// (redis://host:port). Cleanup is registered automatically.
func StartRedis(t *testing.T) string {
	t.Helper()
	ctx := context.Background()
	r, err := tcredis.Run(ctx, "redis:7-alpine")
	require.NoError(t, err)
	t.Cleanup(func() { _ = r.Terminate(ctx) })
	uri, err := r.ConnectionString(ctx)
	require.NoError(t, err)
	return uri
}

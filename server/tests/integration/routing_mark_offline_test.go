//go:build integration

package integration

import (
	"context"
	"testing"

	"github.com/redis/go-redis/v9"
	"github.com/stretchr/testify/require"

	"im-server/internal/repo"
	"im-server/internal/testutil/containers"
)

// TestRouting_MarkOffline_EvictsOnlyMatchingGatewayDevices locks in the Lua
// script semantics: HDEL only those deviceID entries whose value equals the
// target gatewayID. Other devices on healthy pods stay.
func TestRouting_MarkOffline_EvictsOnlyMatchingGatewayDevices(t *testing.T) {
	uri := containers.StartRedis(t)
	opts, err := redis.ParseURL(uri)
	require.NoError(t, err)
	rdb := redis.NewClient(opts)
	t.Cleanup(func() { _ = rdb.Close() })

	ctx := context.Background()
	rA := repo.NewRouting(rdb, "gw-A")
	rB := repo.NewRouting(rdb, "gw-B")
	rC := repo.NewRouting(rdb, "gw-C")

	// user 1: three devices, two on gw-B (the one to evict), one on gw-A
	require.NoError(t, rA.Register(ctx, 1, "dev-a"))
	require.NoError(t, rB.Register(ctx, 1, "dev-b1"))
	require.NoError(t, rB.Register(ctx, 1, "dev-b2"))
	// user 2: single device on gw-C (should be untouched)
	require.NoError(t, rC.Register(ctx, 2, "dev-c"))

	// Evict gw-B for user 1.
	n, err := rA.MarkOffline(ctx, 1, "gw-B")
	require.NoError(t, err)
	require.Equal(t, 2, n, "both gw-B devices must be evicted")

	// user 1 should now have only gw-A
	gwA, err := rA.LookupBatch(ctx, []int64{1})
	require.NoError(t, err)
	require.Equal(t, []string{"gw-A"}, gwA[1])

	// user 2 unaffected.
	gwC, err := rC.LookupBatch(ctx, []int64{2})
	require.NoError(t, err)
	require.Equal(t, []string{"gw-C"}, gwC[2])

	// Evicting a gateway with no matching devices returns 0, not an error.
	n, err = rA.MarkOffline(ctx, 1, "gw-Z")
	require.NoError(t, err)
	require.Equal(t, 0, n)

	// Evicting against an empty user key is a no-op (0, nil), not a Lua error.
	n, err = rA.MarkOffline(ctx, 99999, "gw-A")
	require.NoError(t, err)
	require.Equal(t, 0, n)

	// Empty gatewayID must reject.
	_, err = rA.MarkOffline(ctx, 1, "")
	require.Error(t, err, "empty gatewayID should surface a guard error")
}

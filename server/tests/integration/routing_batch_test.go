//go:build integration

package integration

import (
	"context"
	"sort"
	"testing"

	"github.com/redis/go-redis/v9"
	"github.com/stretchr/testify/require"

	"im-server/internal/repo"
	"im-server/internal/testutil/containers"
)

// TestRouting_LookupBatch_AgainstRealRedis covers the pipelined HGETALL that
// backs CrossPodBroadcast. We spin up a real Redis container (same pattern as
// the Postgres-backed flows), populate routing rows for a mix of online /
// offline / multi-device users, and verify LookupBatch returns an entry for
// every input uid with the correct distinct-gatewayID set.
func TestRouting_LookupBatch_AgainstRealRedis(t *testing.T) {
	uri := containers.StartRedis(t)
	opts, err := redis.ParseURL(uri)
	require.NoError(t, err)
	rdb := redis.NewClient(opts)
	t.Cleanup(func() { _ = rdb.Close() })

	ctx := context.Background()
	r := repo.NewRouting(rdb, "gw-self")

	// user 1 on gw-A via one device
	require.NoError(t, r.Register(ctx, 1, "dev-1"))
	// user 2 on two devices: one on gw-self (should still be returned so the
	// caller can skip), one on gw-B
	rsvB := repo.NewRouting(rdb, "gw-B")
	require.NoError(t, rsvB.Register(ctx, 2, "dev-2b"))
	require.NoError(t, r.Register(ctx, 2, "dev-2a"))
	// user 3 offline — no entries at all
	// user 4 on gw-B and gw-C (two devices, two pods)
	rsvC := repo.NewRouting(rdb, "gw-C")
	require.NoError(t, rsvB.Register(ctx, 4, "dev-4b"))
	require.NoError(t, rsvC.Register(ctx, 4, "dev-4c"))

	got, err := r.LookupBatch(ctx, []int64{1, 2, 3, 4})
	require.NoError(t, err)

	require.Equal(t, []string{"gw-self"}, got[1])
	require.Equal(t, sortedGwSet(got[2]), []string{"gw-B", "gw-self"})
	require.Empty(t, got[3])
	require.Equal(t, sortedGwSet(got[4]), []string{"gw-B", "gw-C"})

	// Smoke: empty input returns empty map, no panic.
	empty, err := r.LookupBatch(ctx, nil)
	require.NoError(t, err)
	require.Empty(t, empty)
}

// sortedGwSet sorts a copy of the slice so assertions are order-independent
// (HGETALL returns an arbitrary order + LookupBatch uses a map).
func sortedGwSet(in []string) []string {
	out := append([]string(nil), in...)
	sort.Strings(out)
	return out
}

//go:build integration

package containers

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	tcredis "github.com/testcontainers/testcontainers-go/modules/redis"
)

// StartRedis launches a Redis 7 container and returns its connection URI
// (redis://host:port). Cleanup is registered automatically.
//
// Race-handling note: testcontainers-go v0.35.0 redis module occasionally
// races on the Docker engine reporting back the mapped port — the container
// is reported Ready (LogStrategy "Ready to accept connections" matches),
// then a follow-up Inspect that ConnectionString does internally returns
// `port "6379/tcp" not found` because the port mapping table hasn't fully
// settled. The race is rare (~0.5% of invocations under load) but
// non-deterministically fails individual subtests when present.
//
// Mitigation: retry ConnectionString up to 5 times with 200ms backoff. The
// retry window is short relative to test runtime but long enough to clear
// every observed race in CI / local. We narrowly retry on the literal
// "port \"6379/tcp\" not found" substring so unrelated errors still fail
// fast.
func StartRedis(t *testing.T) string {
	t.Helper()
	ctx := context.Background()
	r, err := tcredis.Run(ctx, "redis:7-alpine")
	require.NoError(t, err)
	t.Cleanup(func() { _ = r.Terminate(ctx) })

	const (
		maxAttempts = 5
		backoff     = 200 * time.Millisecond
	)
	var (
		uri     string
		lastErr error
	)
	for attempt := 1; attempt <= maxAttempts; attempt++ {
		uri, lastErr = r.ConnectionString(ctx)
		if lastErr == nil {
			return uri
		}
		// Retry only on the port-not-yet-mapped race; surface other errors
		// immediately so genuine failures (auth, image pull) still abort.
		if !strings.Contains(lastErr.Error(), `port "6379/tcp" not found`) {
			break
		}
		time.Sleep(backoff)
	}
	require.NoError(t, lastErr,
		"StartRedis: ConnectionString failed after %d attempts (last error attached)",
		maxAttempts)
	return uri
}

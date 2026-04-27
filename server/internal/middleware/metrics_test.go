package middleware

import (
	"testing"

	"github.com/stretchr/testify/require"
)

// TestMetrics_NoopReady — metrics() must return a non-nil bundle even when
// no OTel MeterProvider is wired (default global is noop). The instruments
// themselves are nil-safe — counters are guarded with nil-checks at call
// site, so tests can drive the cache path without spinning up an exporter.
func TestMetrics_NoopReady(t *testing.T) {
	m := metrics()
	require.NotNil(t, m, "metrics bundle must be initialised")
	// Singleton: a second call returns the same struct, exercises the
	// sync.Once branch.
	require.Same(t, m, metrics(), "metrics() must be singleton")
}

// TestMetrics_HitMissDoNotPanic — counters returned by noop meter are
// callable without panicking. Guards against future refactors that drop
// the nil-check at the call site.
func TestMetrics_HitMissDoNotPanic(t *testing.T) {
	m := metrics()
	if m.CookieCacheHit != nil {
		require.NotPanics(t, func() { m.CookieCacheHit.Add(t.Context(), 1) })
	}
	if m.CookieCacheMiss != nil {
		require.NotPanics(t, func() { m.CookieCacheMiss.Add(t.Context(), 1) })
	}
}

package middleware

import (
	"context"
	"sync"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/metric"
)

// mwMetrics bundles middleware-layer OTel instruments. Built lazily on first
// access; falls back to noop when the global MeterProvider is still noop, so
// unit tests don't have to wire OTel.
type mwMetrics struct {
	// CookieCacheHit increments when MattermostCookieResolve serves a request
	// from the in-process LRU instead of going to Redis.
	CookieCacheHit metric.Int64Counter
	// CookieCacheMiss increments on Redis fall-through (cold cookie or
	// recently evicted entry). Track the ratio against Hit to validate the
	// 30s TTL × 10k capacity sizing — target hit_rate ≥ 90% under steady VU.
	CookieCacheMiss metric.Int64Counter
}

var (
	mwMetricsInstance *mwMetrics
	mwMetricsOnce     sync.Once
)

// metrics returns the lazily-initialised middleware metric bundle. Safe to
// call from any goroutine.
func metrics() *mwMetrics {
	mwMetricsOnce.Do(func() {
		m := &mwMetrics{}
		meter := otel.Meter("im-middleware")
		m.CookieCacheHit, _ = meter.Int64Counter(
			"im.auth.cookie_cache.hit",
			metric.WithDescription("MattermostCookieResolve LRU cache hit"),
		)
		m.CookieCacheMiss, _ = meter.Int64Counter(
			"im.auth.cookie_cache.miss",
			metric.WithDescription("MattermostCookieResolve LRU cache miss (Redis fall-through)"),
		)
		// Observable gauge for cache size. The callback samples the
		// process-global LRU each scrape; nil-safe before initCookieCache
		// runs.
		_, _ = meter.Int64ObservableGauge(
			"im.auth.cookie_cache.size",
			metric.WithDescription("MattermostCookieResolve LRU current entry count"),
			metric.WithInt64Callback(func(_ context.Context, o metric.Int64Observer) error {
				if cookieCache != nil {
					o.Observe(int64(cookieCache.Len()))
				}
				return nil
			}),
		)
		mwMetricsInstance = m
	})
	return mwMetricsInstance
}

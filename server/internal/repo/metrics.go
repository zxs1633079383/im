package repo

import (
	"sync"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/metric"
)

// repoMetrics bundles repo-layer OTel instruments. Split from each Repo's
// own file so migration / interface evolution doesn't churn metric
// registration.
type repoMetrics struct {
	// RoutingOps: Redis routing ops tagged op=sadd|srem|exists|expire.
	RoutingOps metric.Int64Counter
	// AllocSeqDur: AllocSeqAndInsert duration — the message hot path's
	// single per-channel serialisation point.
	AllocSeqDur metric.Float64Histogram
}

var (
	repoMetricsInstance *repoMetrics
	repoMetricsOnce     sync.Once
)

// metrics returns the lazily initialised repo metrics bundle. Safe even if
// the global MeterProvider is still noop — instruments fall back to noop.
func metrics() *repoMetrics {
	repoMetricsOnce.Do(func() {
		m := &repoMetrics{}
		meter := otel.Meter("im-repo")
		m.RoutingOps, _ = meter.Int64Counter(
			"im.routing.redis.ops",
			metric.WithDescription("Redis routing ops tagged op=sadd|srem|exists|expire"),
		)
		m.AllocSeqDur, _ = meter.Float64Histogram(
			"im.message.alloc_seq.duration",
			metric.WithDescription("MessageRepo.AllocSeqAndInsert duration"),
			metric.WithUnit("ms"),
		)
		repoMetricsInstance = m
	})
	return repoMetricsInstance
}

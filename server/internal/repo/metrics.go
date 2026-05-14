package repo

import (
	"context"
	"database/sql"
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
	// ChannelMembersCount: histogram over `len(members)` returned by
	// ChannelRepo.ListMembers. Use the bucket distribution to spot huge
	// channels (P95 / P99 quantiles) — they dominate broadcast cost.
	ChannelMembersCount metric.Int64Histogram
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
		m.ChannelMembersCount, _ = meter.Int64Histogram(
			"im.channel.members.count",
			metric.WithDescription("Channel member count observed per ListMembers call"),
		)
		repoMetricsInstance = m
	})
	return repoMetricsInstance
}

var dbPoolMetricsOnce sync.Once

// registerDBPoolMetrics wires five ObservableGauges that sample sql.DB.Stats
// on every scrape. Idempotent — calling it more than once is a no-op so
// repo.Open can register without coordination from main().
func registerDBPoolMetrics(db *sql.DB) {
	if db == nil {
		return
	}
	dbPoolMetricsOnce.Do(func() {
		meter := otel.Meter("im-repo")

		openConns, _ := meter.Int64ObservableGauge(
			"im.db.pool.open_connections",
			metric.WithDescription("sql.DB.Stats.OpenConnections — total open (in-use + idle)"),
		)
		inUse, _ := meter.Int64ObservableGauge(
			"im.db.pool.in_use",
			metric.WithDescription("sql.DB.Stats.InUse — connections currently in use"),
		)
		idle, _ := meter.Int64ObservableGauge(
			"im.db.pool.idle",
			metric.WithDescription("sql.DB.Stats.Idle — connections currently idle"),
		)
		waitCount, _ := meter.Int64ObservableGauge(
			"im.db.pool.wait_count",
			metric.WithDescription("sql.DB.Stats.WaitCount — total times a goroutine waited for a free conn"),
		)
		waitDur, _ := meter.Float64ObservableGauge(
			"im.db.pool.wait_duration",
			metric.WithDescription("sql.DB.Stats.WaitDuration — cumulative time goroutines waited for a free conn"),
			metric.WithUnit("ms"),
		)

		_, _ = meter.RegisterCallback(
			func(_ context.Context, o metric.Observer) error {
				s := db.Stats()
				o.ObserveInt64(openConns, int64(s.OpenConnections))
				o.ObserveInt64(inUse, int64(s.InUse))
				o.ObserveInt64(idle, int64(s.Idle))
				o.ObserveInt64(waitCount, s.WaitCount)
				o.ObserveFloat64(waitDur, float64(s.WaitDuration.Milliseconds()))
				return nil
			},
			openConns, inUse, idle, waitCount, waitDur,
		)
	})
}

package service

import (
	"sync"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/metric"
)

// svcMetrics bundles service-layer OTel instruments. Sync is the Grafana
// dashboard's Sync row's sole feeder so we keep all three Sync metrics
// together for easy grep + one-shot init.
type svcMetrics struct {
	// SyncResp: /api/sync response count, tagged is_empty=0|1 + has_more=0|1.
	SyncResp metric.Int64Counter
	// SyncChannels: histogram over channels count returned per Sync call.
	SyncChannels metric.Int64Histogram
	// SyncMessages: histogram over messages count returned per Sync call.
	SyncMessages metric.Int64Histogram
}

var (
	svcMetricsInstance *svcMetrics
	svcMetricsOnce     sync.Once
)

func metrics() *svcMetrics {
	svcMetricsOnce.Do(func() {
		m := &svcMetrics{}
		meter := otel.Meter("im-service")
		m.SyncResp, _ = meter.Int64Counter(
			"im.sync.response",
			metric.WithDescription("/api/sync response count tagged is_empty + has_more"),
		)
		m.SyncChannels, _ = meter.Int64Histogram(
			"im.sync.channels.count",
			metric.WithDescription("Channels returned per /api/sync call"),
		)
		m.SyncMessages, _ = meter.Int64Histogram(
			"im.sync.messages.count",
			metric.WithDescription("Messages returned per /api/sync call"),
		)
		svcMetricsInstance = m
	})
	return svcMetricsInstance
}

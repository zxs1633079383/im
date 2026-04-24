package http

import (
	"sync"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/metric"
)

// httpMetrics exposes the HTTP-layer metric not already emitted by otelgin.
// otelgin gives us http_server_duration per route; fanoutE2E is the extra
// im-specific view: total time the SendMessage handler spends from the
// HTTP receive timestamp until the pushers have finished fanning out.
type httpMetrics struct {
	FanoutE2E metric.Float64Histogram
}

var (
	httpMetricsInstance *httpMetrics
	httpMetricsOnce     sync.Once
)

func metrics() *httpMetrics {
	httpMetricsOnce.Do(func() {
		m := &httpMetrics{}
		meter := otel.Meter("im-http")
		m.FanoutE2E, _ = meter.Float64Histogram(
			"im.fanout.e2e.duration",
			metric.WithDescription("SendMessage handler E2E duration (HTTP receive → push fan-out done)"),
			metric.WithUnit("ms"),
		)
		httpMetricsInstance = m
	})
	return httpMetricsInstance
}

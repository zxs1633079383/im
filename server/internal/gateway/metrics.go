package gateway

import (
	"sync"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/metric"
)

// gwMetrics bundles the gateway-layer OTel instruments surfaced on the
// Grafana "Gateway" row. Lazy-initialised behind metricsOnce so the global
// MeterProvider is already wired up by observability.Init when we grab a
// Meter handle. Init failures (unlikely outside of misconfiguration) leave
// fields nil and the otel noop fallback keeps Record/Add calls safe.
type gwMetrics struct {
	// PushSend: rate of cross-pod Pulsar push attempts, tagged status=ok/error.
	PushSend metric.Int64Counter
	// PulsarSendDur: producer.Send latency (ms) — surfaces slow Pulsar brokers.
	PulsarSendDur metric.Float64Histogram
	// ProdActive: live count of cached Pulsar producers; goes up/down on
	// cache fills + LRU evictions.
	ProdActive metric.Int64UpDownCounter
	// WSFramesIn: rate of inbound WebSocket frames (post JSON-decode).
	// Counts every successful ReadMessage call in WsHandler.readPump.
	WSFramesIn metric.Int64Counter
	// WSFramesOut: rate of outbound WebSocket frames. Counts every successful
	// WriteMessage call in Conn.writePump.
	WSFramesOut metric.Int64Counter
}

var (
	gwMetricsInstance *gwMetrics
	gwMetricsOnce     sync.Once
)

// metrics returns the lazily initialised gateway metrics bundle. First call
// acquires the Meter; subsequent calls reuse the same instance.
func metrics() *gwMetrics {
	gwMetricsOnce.Do(func() {
		m := &gwMetrics{}
		meter := otel.Meter("im-gateway")
		m.PushSend, _ = meter.Int64Counter(
			"im.push.pulsar.send",
			metric.WithDescription("Cross-pod Pulsar push attempts, tagged status=ok|error"),
		)
		m.PulsarSendDur, _ = meter.Float64Histogram(
			"im.pulsar.producer.send.duration",
			metric.WithDescription("Pulsar producer.Send duration"),
			metric.WithUnit("ms"),
		)
		m.ProdActive, _ = meter.Int64UpDownCounter(
			"im.pulsar.producer.active",
			metric.WithDescription("Active Pulsar producers in LRU cache"),
		)
		m.WSFramesIn, _ = meter.Int64Counter(
			"im.ws.frames.in",
			metric.WithDescription("Inbound WebSocket frames received from clients"),
		)
		m.WSFramesOut, _ = meter.Int64Counter(
			"im.ws.frames.out",
			metric.WithDescription("Outbound WebSocket frames written to clients"),
		)
		gwMetricsInstance = m
	})
	return gwMetricsInstance
}

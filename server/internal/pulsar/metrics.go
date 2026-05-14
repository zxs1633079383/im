package pulsar

import (
	"sync"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/metric"
)

// pulsarMetrics holds OTel instruments owned by the pulsar client layer. The
// Consume loop records into ConsumeLag on every successful receive so the
// "broker → consumer" delay is observable per topic without each downstream
// HandlerFunc having to wire up its own histogram.
type pulsarMetrics struct {
	// ConsumeLag is the wall-clock delta between the broker-side publish
	// timestamp and the moment Consume hands the payload to the handler.
	// Tagged with the topic name. Spikes here mean the consumer fell behind
	// — typically slow handler, backed-up Pulsar subscription, or noisy
	// broker.
	ConsumeLag metric.Float64Histogram
}

var (
	pulsarMetricsInstance *pulsarMetrics
	pulsarMetricsOnce     sync.Once
)

func metrics() *pulsarMetrics {
	pulsarMetricsOnce.Do(func() {
		m := &pulsarMetrics{}
		meter := otel.Meter("im-pulsar")
		m.ConsumeLag, _ = meter.Float64Histogram(
			"im.pulsar.consume.lag.duration",
			metric.WithDescription("Pulsar consumer lag: now - msg.PublishTime, per topic"),
			metric.WithUnit("ms"),
		)
		pulsarMetricsInstance = m
	})
	return pulsarMetricsInstance
}

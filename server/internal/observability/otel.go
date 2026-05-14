// Package observability wires the OpenTelemetry SDK (traces + metrics)
// for im-server services. Callers invoke Init at startup and defer the
// returned ShutdownFunc to flush pending exports on graceful shutdown.
package observability

import (
	"context"
	"fmt"
	"net/http"
	"time"

	"github.com/prometheus/client_golang/prometheus/promhttp"
	"go.opentelemetry.io/contrib/instrumentation/runtime"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/exporters/otlp/otlpmetric/otlpmetricgrpc"
	"go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracegrpc"
	otelprom "go.opentelemetry.io/otel/exporters/prometheus"
	"go.opentelemetry.io/otel/propagation"
	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
	"go.opentelemetry.io/otel/sdk/resource"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	semconv "go.opentelemetry.io/otel/semconv/v1.26.0"
)

// PrometheusHandler exposes the Prometheus pull endpoint Init wired up.
// Mount on /metrics via the gateway's HTTP engine. Nil if Init returned
// an error or was Disabled.
var PrometheusHandler http.Handler

// Config configures the OpenTelemetry SDK.
// SampleRatio defaults to 1.0 if zero. Disabled returns a noop shutdown.
//
// Endpoint is OTLP/gRPC host:port (no scheme). When empty the SDK falls back
// to OTEL_EXPORTER_OTLP_ENDPOINT (default localhost:4317), preserving the
// historical env-driven behaviour for local dev.
type Config struct {
	ServiceName    string
	ServiceVersion string
	SampleRatio    float64
	Disabled       bool
	Endpoint       string
}

// ShutdownFunc flushes pending exports and shuts down providers.
// Honors the passed context's deadline.
type ShutdownFunc func(context.Context) error

// Init wires global TracerProvider and MeterProvider with OTLP/gRPC exporters
// (endpoint via OTEL_EXPORTER_OTLP_ENDPOINT) and starts runtime metrics.
// Returns a ShutdownFunc that the caller MUST defer.
func Init(ctx context.Context, cfg Config) (ShutdownFunc, error) {
	if cfg.Disabled {
		return func(context.Context) error { return nil }, nil
	}
	if cfg.SampleRatio == 0 {
		cfg.SampleRatio = 1.0
	}

	res, err := resource.New(ctx,
		resource.WithAttributes(
			semconv.ServiceName(cfg.ServiceName),
			semconv.ServiceVersion(cfg.ServiceVersion),
		),
	)
	if err != nil {
		return nil, fmt.Errorf("resource: %w", err)
	}

	traceOpts := []otlptracegrpc.Option{otlptracegrpc.WithInsecure()}
	if cfg.Endpoint != "" {
		traceOpts = append(traceOpts, otlptracegrpc.WithEndpoint(cfg.Endpoint))
	}
	traceExp, err := otlptracegrpc.New(ctx, traceOpts...)
	if err != nil {
		return nil, fmt.Errorf("trace exporter: %w", err)
	}
	tp := sdktrace.NewTracerProvider(
		sdktrace.WithBatcher(traceExp, sdktrace.WithBatchTimeout(5*time.Second)),
		sdktrace.WithResource(res),
		sdktrace.WithSampler(sdktrace.ParentBased(sdktrace.TraceIDRatioBased(cfg.SampleRatio))),
	)
	otel.SetTracerProvider(tp)
	otel.SetTextMapPropagator(propagation.NewCompositeTextMapPropagator(
		propagation.TraceContext{}, propagation.Baggage{},
	))

	metricOpts := []otlpmetricgrpc.Option{otlpmetricgrpc.WithInsecure()}
	if cfg.Endpoint != "" {
		metricOpts = append(metricOpts, otlpmetricgrpc.WithEndpoint(cfg.Endpoint))
	}
	metricExp, err := otlpmetricgrpc.New(ctx, metricOpts...)
	if err != nil {
		return nil, fmt.Errorf("metric exporter: %w", err)
	}
	// Prometheus pull reader — exposes a /metrics endpoint Prometheus can
	// scrape directly. Required because the OTLP push to jaeger-cses
	// drops metric payloads (jaeger-v2 only accepts traces).
	promReader, err := otelprom.New()
	if err != nil {
		return nil, fmt.Errorf("prometheus exporter: %w", err)
	}
	mp := sdkmetric.NewMeterProvider(
		sdkmetric.WithResource(res),
		sdkmetric.WithReader(sdkmetric.NewPeriodicReader(metricExp,
			sdkmetric.WithInterval(15*time.Second))),
		sdkmetric.WithReader(promReader),
	)
	otel.SetMeterProvider(mp)
	PrometheusHandler = promhttp.Handler()

	if err := runtime.Start(runtime.WithMinimumReadMemStatsInterval(time.Second)); err != nil {
		return nil, fmt.Errorf("runtime metrics: %w", err)
	}

	return func(ctx context.Context) error {
		ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
		defer cancel()
		_ = tp.Shutdown(ctx)
		_ = mp.Shutdown(ctx)
		return nil
	}, nil
}

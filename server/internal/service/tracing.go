package service

import "go.opentelemetry.io/otel"

// tracer is the package-wide OTel tracer used by every service method that
// wraps its body in a span. Keeping it here (unexported, single instance)
// avoids the per-method "otel.Tracer" call and keeps the span name prefix
// consistent across the package ("im-server/service").
var tracer = otel.Tracer("im-server/service")

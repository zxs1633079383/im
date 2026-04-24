package repo

import "go.opentelemetry.io/otel"

// tracer is the package-wide OTel tracer used by hot repo methods that wrap
// their body in a span. Keeping it here (unexported, single instance) avoids
// the per-method "otel.Tracer" call and keeps the span name prefix consistent
// across the package ("im-server/repo").
var tracer = otel.Tracer("im-server/repo")

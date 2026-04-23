package gateway

import "go.opentelemetry.io/otel"

// tracer is the package-wide OTel tracer for gateway-layer spans that are not
// the WS frame dispatcher (which owns its own wsTracer under "im-gateway/ws").
// Keeps the "im-server/gateway" prefix consistent with service / repo tracers.
var tracer = otel.Tracer("im-server/gateway")

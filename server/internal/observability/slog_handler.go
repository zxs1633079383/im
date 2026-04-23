package observability

import (
	"context"
	"log/slog"

	"go.opentelemetry.io/otel/trace"
)

// TraceHandler wraps any slog.Handler and injects the active span's
// trace_id and span_id (when present) into every log record.
type TraceHandler struct {
	slog.Handler
}

// NewTraceHandler wraps h. All Handler interface methods delegate to h
// except Handle, which annotates the record with span context.
func NewTraceHandler(h slog.Handler) slog.Handler {
	return &TraceHandler{Handler: h}
}

// Handle adds trace_id/span_id attrs from ctx's active span (if any),
// then forwards to the wrapped handler.
func (h *TraceHandler) Handle(ctx context.Context, r slog.Record) error {
	if span := trace.SpanFromContext(ctx); span.SpanContext().IsValid() {
		sc := span.SpanContext()
		r.AddAttrs(
			slog.String("trace_id", sc.TraceID().String()),
			slog.String("span_id", sc.SpanID().String()),
		)
	}
	return h.Handler.Handle(ctx, r)
}

// WithAttrs returns a new TraceHandler wrapping the inner handler with attrs.
func (h *TraceHandler) WithAttrs(attrs []slog.Attr) slog.Handler {
	return &TraceHandler{Handler: h.Handler.WithAttrs(attrs)}
}

// WithGroup returns a new TraceHandler wrapping the inner handler grouped.
func (h *TraceHandler) WithGroup(name string) slog.Handler {
	return &TraceHandler{Handler: h.Handler.WithGroup(name)}
}

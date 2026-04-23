package observability

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"testing"

	"github.com/stretchr/testify/require"
	"go.opentelemetry.io/otel"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
)

func TestTraceHandler_InjectsTraceID(t *testing.T) {
	tp := sdktrace.NewTracerProvider()
	otel.SetTracerProvider(tp)
	t.Cleanup(func() { _ = tp.Shutdown(context.Background()) })

	var buf bytes.Buffer
	logger := slog.New(NewTraceHandler(slog.NewJSONHandler(&buf, nil)))

	ctx, span := tp.Tracer("t").Start(context.Background(), "op")
	defer span.End()

	logger.InfoContext(ctx, "hello")

	var entry map[string]any
	require.NoError(t, json.Unmarshal(buf.Bytes(), &entry))
	require.Equal(t, span.SpanContext().TraceID().String(), entry["trace_id"])
	require.Equal(t, span.SpanContext().SpanID().String(), entry["span_id"])
	require.Equal(t, "hello", entry["msg"])
}

func TestTraceHandler_NoSpan_NoIDs(t *testing.T) {
	var buf bytes.Buffer
	logger := slog.New(NewTraceHandler(slog.NewJSONHandler(&buf, nil)))
	logger.Info("plain")

	var entry map[string]any
	require.NoError(t, json.Unmarshal(buf.Bytes(), &entry))
	_, hasTrace := entry["trace_id"]
	_, hasSpan := entry["span_id"]
	require.False(t, hasTrace, "trace_id should not appear when no span")
	require.False(t, hasSpan, "span_id should not appear when no span")
}

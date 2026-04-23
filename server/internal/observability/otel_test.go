package observability

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestInit_DisabledNoop(t *testing.T) {
	shutdown, err := Init(context.Background(), Config{ServiceName: "t", Disabled: true})
	require.NoError(t, err)
	require.NotNil(t, shutdown)
	require.NoError(t, shutdown(context.Background()))
}

func TestInit_WithEndpoint(t *testing.T) {
	t.Setenv("OTEL_EXPORTER_OTLP_ENDPOINT", "localhost:4317")
	shutdown, err := Init(context.Background(), Config{ServiceName: "t"})
	require.NoError(t, err)
	require.NotNil(t, shutdown)
	require.NoError(t, shutdown(context.Background()))
}

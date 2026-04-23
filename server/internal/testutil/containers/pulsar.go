//go:build integration

package containers

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"github.com/testcontainers/testcontainers-go"
	"github.com/testcontainers/testcontainers-go/wait"
)

// StartPulsar launches a Pulsar 3.3 standalone container and returns the
// broker URL (pulsar://host:port). Cleanup is registered automatically.
func StartPulsar(t *testing.T) string {
	t.Helper()
	ctx := context.Background()
	req := testcontainers.ContainerRequest{
		Image:        "apachepulsar/pulsar:3.3.2",
		ExposedPorts: []string{"6650/tcp", "8080/tcp"},
		Cmd: []string{"bin/pulsar", "standalone",
			"--no-functions-worker", "--no-stream-storage"},
		WaitingFor: wait.ForLog("Created namespace public/default").
			WithStartupTimeout(120 * time.Second),
	}
	c, err := testcontainers.GenericContainer(ctx, testcontainers.GenericContainerRequest{
		ContainerRequest: req, Started: true,
	})
	require.NoError(t, err)
	t.Cleanup(func() { _ = c.Terminate(ctx) })

	host, err := c.Host(ctx)
	require.NoError(t, err)
	port, err := c.MappedPort(ctx, "6650")
	require.NoError(t, err)
	return fmt.Sprintf("pulsar://%s:%s", host, port.Port())
}

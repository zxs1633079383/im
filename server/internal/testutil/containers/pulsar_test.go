//go:build integration

package containers

import (
	"testing"

	"github.com/apache/pulsar-client-go/pulsar"
	"github.com/stretchr/testify/require"
)

func TestStartPulsar_Smoke(t *testing.T) {
	url := StartPulsar(t)
	cli, err := pulsar.NewClient(pulsar.ClientOptions{URL: url})
	require.NoError(t, err)
	defer cli.Close()
}

//go:build integration

package containers

import (
	"context"
	"testing"

	"github.com/redis/go-redis/v9"
	"github.com/stretchr/testify/require"
)

func TestStartRedis_Smoke(t *testing.T) {
	uri := StartRedis(t)
	opts, err := redis.ParseURL(uri)
	require.NoError(t, err)
	c := redis.NewClient(opts)
	defer c.Close()
	require.NoError(t, c.Ping(context.Background()).Err())
}

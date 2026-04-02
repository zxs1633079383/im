package store

import (
	"context"
	"fmt"
	"time"

	"github.com/redis/go-redis/v9"
)

const (
	connKeyTTL = 2 * time.Hour
)

// Routing manages the Redis user-connection routing table.
// It maps each user's deviceID to the gateway pod ID that hosts the connection.
type Routing struct {
	rdb       *redis.Client
	gatewayID string
}

// NewRouting creates a new Routing backed by rdb.
func NewRouting(rdb *redis.Client, gatewayID string) *Routing {
	return &Routing{rdb: rdb, gatewayID: gatewayID}
}

func connKey(userID int64) string {
	return fmt.Sprintf("user:connections:%d", userID)
}

// Register records that deviceID for userID is connected to this gateway.
// Sets a TTL on the hash key so stale entries expire automatically.
func (r *Routing) Register(ctx context.Context, userID int64, deviceID string) error {
	key := connKey(userID)
	pipe := r.rdb.TxPipeline()
	pipe.HSet(ctx, key, deviceID, r.gatewayID)
	pipe.Expire(ctx, key, connKeyTTL)
	_, err := pipe.Exec(ctx)
	return err
}

// Deregister removes the deviceID entry for userID.
func (r *Routing) Deregister(ctx context.Context, userID int64, deviceID string) error {
	return r.rdb.HDel(ctx, connKey(userID), deviceID).Err()
}

// RefreshTTL resets the expiry of the routing key (call on each heartbeat).
func (r *Routing) RefreshTTL(ctx context.Context, userID int64) error {
	return r.rdb.Expire(ctx, connKey(userID), connKeyTTL).Err()
}

// GatewayIDsForUser returns the set of distinct gateway IDs that userID is connected to.
// Returns an empty slice if the user has no active connections.
func (r *Routing) GatewayIDsForUser(ctx context.Context, userID int64) ([]string, error) {
	m, err := r.rdb.HGetAll(ctx, connKey(userID)).Result()
	if err != nil {
		return nil, err
	}
	seen := make(map[string]struct{})
	out := make([]string, 0, len(m))
	for _, gwID := range m {
		if _, dup := seen[gwID]; !dup {
			seen[gwID] = struct{}{}
			out = append(out, gwID)
		}
	}
	return out, nil
}

// DevicesForUser returns all device_id → gateway_id entries for userID.
func (r *Routing) DevicesForUser(ctx context.Context, userID int64) (map[string]string, error) {
	return r.rdb.HGetAll(ctx, connKey(userID)).Result()
}

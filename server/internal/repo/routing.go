package repo

import (
	"context"
	"fmt"
	"time"

	"github.com/redis/go-redis/v9"
)

const (
	connKeyTTL = 2 * time.Hour

	// RoutingTTL is the short TTL applied on each heartbeat so stale presence
	// entries disappear within one ping cycle after a pod goes away.
	// Aligned with BACKEND.md §3.2 — ping interval ~15s, presence TTL 45s.
	RoutingTTL = 45 * time.Second
)

// refreshScript atomically re-registers presence for (userID, deviceID, gatewayID)
// and resets the key TTL to RoutingTTL. Running HSET + EXPIRE in a single EVAL
// avoids a race where the key could expire between the two writes.
//
// KEYS[1] = user:connections:{userID}
// ARGV[1] = deviceID (a.k.a. connID)
// ARGV[2] = gatewayID
// ARGV[3] = ttl in seconds
var refreshScript = redis.NewScript(`
redis.call("HSET", KEYS[1], ARGV[1], ARGV[2])
redis.call("EXPIRE", KEYS[1], ARGV[3])
return 1
`)

// routingKeyPrefix namespaces every key this package writes in Redis.
//
// Rationale: pre/prod Redis is a shared Cluster (no multi-DB support), so we
// isolate im state by prefix instead of by DB index. The prefix is stable
// across environments; the deployment itself picks the target Redis Cluster.
const routingKeyPrefix = "im-new:routing"

// Routing manages the Redis user-connection routing table.
// It maps each user's deviceID to the gateway pod ID that hosts the connection.
type Routing struct {
	rdb       redis.UniversalClient
	gatewayID string
}

// NewRouting creates a new Routing backed by rdb. The client is a
// UniversalClient so both single-node and Cluster deployments work with the
// same code path.
func NewRouting(rdb redis.UniversalClient, gatewayID string) *Routing {
	return &Routing{rdb: rdb, gatewayID: gatewayID}
}

// connKey returns the Redis hash key holding userID's (deviceID → gatewayID)
// map. All Lua scripts below must target this single key so Cluster routing
// by hash slot stays correct.
func connKey(userID int64) string {
	return fmt.Sprintf("%s:user:%d", routingKeyPrefix, userID)
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

// Refresh atomically re-registers the (connID → gatewayID) entry for userID
// and resets the routing key TTL to RoutingTTL. Call on every ping so a
// user's presence disappears within one cycle of their last heartbeat.
//
// The gatewayID argument is authoritative — if the caller's bound gateway
// (r.gatewayID) differs from the gatewayID argument, the argument wins so
// Refresh stays usable in tests / future multi-gateway code paths.
func (r *Routing) Refresh(ctx context.Context, userID int64, gatewayID string, connID string) error {
	key := connKey(userID)
	_, err := refreshScript.Run(ctx, r.rdb,
		[]string{key},
		connID, gatewayID, int(RoutingTTL.Seconds()),
	).Result()
	if err != nil {
		return fmt.Errorf("routing refresh: %w", err)
	}
	return nil
}

// Lookup returns the distinct gateway IDs that userID is currently connected to.
// It is an alias for GatewayIDsForUser that matches the BACKEND.md naming.
func (r *Routing) Lookup(ctx context.Context, userID int64) ([]string, error) {
	return r.GatewayIDsForUser(ctx, userID)
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

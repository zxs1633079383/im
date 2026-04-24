package gateway

import "im-server/internal/repo"

// Routing is re-exported from repo.Routing for use within the gateway package.
// It manages the Redis user-connection routing table.
type Routing = repo.Routing

// NewRouting creates a new Routing. See repo.NewRouting for details.
var NewRouting = repo.NewRouting

// RoutingTTL mirrors repo.RoutingTTL so gateway callers can reference the
// presence TTL without importing repo directly.
const RoutingTTL = repo.RoutingTTL

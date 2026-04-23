package gateway

import "im-server/internal/repo"

// Routing is re-exported from repo.Routing for use within the gateway package.
// It manages the Redis user-connection routing table.
type Routing = repo.Routing

// NewRouting creates a new Routing. See repo.NewRouting for details.
var NewRouting = repo.NewRouting

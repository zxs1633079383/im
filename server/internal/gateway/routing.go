package gateway

import "im-server/internal/store"

// Routing is re-exported from store.Routing for use within the gateway package.
// It manages the Redis user-connection routing table.
type Routing = store.Routing

// NewRouting creates a new Routing. See store.NewRouting for details.
var NewRouting = store.NewRouting

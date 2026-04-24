package gateway

import (
	"context"
	"log/slog"
	"sync"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/metric"
)

// Hub is the in-process connection registry.
// It is safe for concurrent use from multiple goroutines.
type Hub struct {
	mu    sync.RWMutex
	conns map[int64][]*Conn // userID → list of active connections
	// failures is an optional tracker that evicts stale routing entries when
	// producer.Send repeatedly fails for the same destination pod. Nil means
	// failure-driven eviction is disabled (zero-value Hub in tests leaves it
	// nil; production wires it in cmd/gateway/main.go).
	failures *sendFailureTracker
}

// NewHub creates an empty Hub and registers an OTel ObservableGauge that
// reports the live WebSocket connection count.
func NewHub() *Hub {
	h := &Hub{conns: make(map[int64][]*Conn)}
	_ = h.registerMetrics()
	return h
}

// AttachFailureTracker wires a sendFailureTracker backed by the given routing
// so broadcast-side producer.Send failures can evict stale presence entries
// after markOfflineThreshold consecutive failures. Call once at startup; a
// subsequent call replaces the tracker (last-writer-wins, harmless in tests).
func (h *Hub) AttachFailureTracker(routing offlineMarker, log *slog.Logger) {
	h.failures = newSendFailureTracker(routing, log)
}

// connCount returns the number of currently registered connections across all users.
func (h *Hub) connCount() int {
	h.mu.RLock()
	defer h.mu.RUnlock()
	n := 0
	for _, list := range h.conns {
		n += len(list)
	}
	return n
}

// registerMetrics registers the im.ws.active_connections ObservableGauge.
// Errors are returned but the caller may safely ignore them — failure to
// register a metric must not prevent the gateway from serving traffic.
func (h *Hub) registerMetrics() error {
	meter := otel.Meter("im-gateway")
	_, err := meter.Int64ObservableGauge("im.ws.active_connections",
		metric.WithDescription("Active WebSocket connections on this gateway pod"),
		metric.WithInt64Callback(func(_ context.Context, o metric.Int64Observer) error {
			o.Observe(int64(h.connCount()))
			return nil
		}),
	)
	return err
}

// Register adds a connection to the hub.
func (h *Hub) Register(c *Conn) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.conns[c.UserID] = append(h.conns[c.UserID], c)
}

// Deregister removes a connection from the hub.
func (h *Hub) Deregister(c *Conn) {
	h.mu.Lock()
	defer h.mu.Unlock()
	list := h.conns[c.UserID]
	updated := list[:0]
	for _, existing := range list {
		if existing != c {
			updated = append(updated, existing)
		}
	}
	if len(updated) == 0 {
		delete(h.conns, c.UserID)
	} else {
		h.conns[c.UserID] = updated
	}
}

// ConnsForUser returns a snapshot of all connections for the given user.
// Callers must not modify the returned slice.
func (h *Hub) ConnsForUser(userID int64) []*Conn {
	h.mu.RLock()
	defer h.mu.RUnlock()
	list := h.conns[userID]
	if len(list) == 0 {
		return nil
	}
	out := make([]*Conn, len(list))
	copy(out, list)
	return out
}

// PushToUser pushes a payload to all connections of userID.
// Returns the number of connections reached.
func (h *Hub) PushToUser(userID int64, msgType WSMessageType, payload any) int {
	conns := h.ConnsForUser(userID)
	sent := 0
	for _, c := range conns {
		if c.Push(msgType, payload) {
			sent++
		}
	}
	return sent
}

// PushRawToUser is the zero-marshal counterpart of PushToUser. Used by the
// cross-pod broadcast path so the same JSON bytes are reused across every
// recipient on this pod (N PushRaw calls share one json.Marshal).
func (h *Hub) PushRawToUser(userID int64, msgType WSMessageType, rawPayload []byte) int {
	conns := h.ConnsForUser(userID)
	sent := 0
	for _, c := range conns {
		if c.PushRaw(msgType, rawPayload) {
			sent++
		}
	}
	return sent
}

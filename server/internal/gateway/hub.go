package gateway

import "sync"

// Hub is the in-process connection registry.
// It is safe for concurrent use from multiple goroutines.
type Hub struct {
	mu    sync.RWMutex
	conns map[int64][]*Conn // userID → list of active connections
}

// NewHub creates an empty Hub.
func NewHub() *Hub {
	return &Hub{conns: make(map[int64][]*Conn)}
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

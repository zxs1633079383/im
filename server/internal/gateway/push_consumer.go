package gateway

import "sync"

// ackRegistry is a lightweight in-process structure that lets the push consumer
// await client ACKs for a given push_id. The push consumer registers a channel,
// and the read pump resolves it when a push_ack frame arrives.
type ackRegistry struct {
	mu      sync.Mutex
	waiters map[string]chan struct{}
}

// resolve signals that the client has ACKed the given push_id.
// If no waiter is registered, this is a no-op.
func (a *ackRegistry) resolve(pushID string) {
	a.mu.Lock()
	ch, ok := a.waiters[pushID]
	if ok {
		delete(a.waiters, pushID)
	}
	a.mu.Unlock()
	if ok {
		close(ch)
	}
}

// await registers a waiter for pushID and returns a channel that is closed
// when the ACK arrives. The caller is responsible for removing the waiter
// if it times out before the ACK.
func (a *ackRegistry) await(pushID string) <-chan struct{} {
	ch := make(chan struct{})
	a.mu.Lock()
	a.waiters[pushID] = ch
	a.mu.Unlock()
	return ch
}

// cancel removes a pending waiter without signalling it (used on timeout cleanup).
func (a *ackRegistry) cancel(pushID string) {
	a.mu.Lock()
	delete(a.waiters, pushID)
	a.mu.Unlock()
}

// globalACKRegistry is the process-wide push ACK registry.
// The push consumer uses await/cancel; the read pump uses resolve.
var globalACKRegistry = &ackRegistry{
	waiters: make(map[string]chan struct{}),
}

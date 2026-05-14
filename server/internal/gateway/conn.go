package gateway

import (
	"encoding/json"
	"sync"
	"sync/atomic"
	"time"

	"github.com/gorilla/websocket"
)

const (
	writeWait   = 10 * time.Second
	sendBufSize = 256
)

// Conn represents one authenticated WebSocket connection. M4: UserID is the
// resolved Mattermost user id (24-char hex string) carried on every push.
type Conn struct {
	UserID   string
	DeviceID string // e.g. "web-<uuid>" or from JWT claim

	ws   *websocket.Conn
	send chan []byte // buffered outbound channel; closed on disconnect
	hub  *Hub

	// knownSeq tracks the last seq successfully pushed to this connection
	// per channel. Used to compute the pong diff.
	//
	// C012 P-D: channel_id key migrates to TEXT (string). Seq stays int64.
	mu       sync.RWMutex
	knownSeq map[string]int64 // channel_id → seq

	lastPong time.Time // updated on every pong received

	// closeOnce + closed protect Close() from running twice and let Push()
	// short-circuit cleanly after the send channel has been closed. Without
	// this, a heartbeat or fan-out Push racing with Close panics with
	// "send on closed channel" — observed in the 2026-04-24 pre benchmark.
	closeOnce sync.Once
	closed    atomic.Bool
}

// NewConn creates a new Conn and starts its writePump goroutine.
func NewConn(userID, deviceID string, ws *websocket.Conn, hub *Hub) *Conn {
	c := &Conn{
		UserID:   userID,
		DeviceID: deviceID,
		ws:       ws,
		send:     make(chan []byte, sendBufSize),
		hub:      hub,
		knownSeq: make(map[string]int64),
		lastPong: time.Now(),
	}
	go c.writePump()
	return c
}

// UpdateKnownSeq records that this connection has received up to seq for channelID.
//
// C012 P-D: channelID is now TEXT (string).
func (c *Conn) UpdateKnownSeq(channelID string, seq int64) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if current, ok := c.knownSeq[channelID]; !ok || seq > current {
		c.knownSeq[channelID] = seq
	}
}

// KnownSeqFor returns the last known seq for channelID (0 if unknown).
func (c *Conn) KnownSeqFor(channelID string) int64 {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.knownSeq[channelID]
}

// KnownSeqs returns a snapshot of the full knownSeq map.
func (c *Conn) KnownSeqs() map[string]int64 {
	c.mu.RLock()
	defer c.mu.RUnlock()
	m := make(map[string]int64, len(c.knownSeq))
	for k, v := range c.knownSeq {
		m[k] = v
	}
	return m
}

// Push enqueues a JSON-serialisable payload for asynchronous delivery.
// Returns false if the connection's send buffer is full (slow consumer)
// or the connection has already been closed.
//
// The recover() guards against a narrow race: runHeartbeat / xpod fan-out
// can call Push concurrently with Close(), and if Close's close(c.send)
// lands first the `case c.send <- b` panics with "send on closed channel"
// and takes down the whole pod. Treat that as a benign "conn is gone" and
// return false — 2026-04-24 pre benchmark crashed all 3 gateways this way.
func (c *Conn) Push(msgType WSMessageType, payload any) (ok bool) {
	defer func() {
		if r := recover(); r != nil {
			ok = false
		}
	}()

	data, err := json.Marshal(payload)
	if err != nil {
		return false
	}
	frame := struct {
		Type    WSMessageType   `json:"type"`
		Payload json.RawMessage `json:"payload"`
	}{Type: msgType, Payload: data}
	b, err := json.Marshal(frame)
	if err != nil {
		return false
	}
	if c.closed.Load() {
		return false
	}
	select {
	case c.send <- b:
		return true
	default:
		return false // slow consumer — caller should close the conn
	}
}

// PushRaw enqueues a pre-marshaled WS payload without re-marshaling. Used by
// the cross-pod push consumer: a single Pulsar envelope carries one already-
// JSON-encoded payload plus N target UIDs, and every recipient's conn should
// see the same bytes. Saves N-1 json.Marshal calls per broadcast.
//
// Share the recover() semantics with Push — a concurrent Close() that lands
// between closed.Load() and the send can race, but the deferred recover
// turns it into a clean false return rather than a pod-wide panic.
func (c *Conn) PushRaw(msgType WSMessageType, rawPayload json.RawMessage) (ok bool) {
	defer func() {
		if r := recover(); r != nil {
			ok = false
		}
	}()

	frame := struct {
		Type    WSMessageType   `json:"type"`
		Payload json.RawMessage `json:"payload"`
	}{Type: msgType, Payload: rawPayload}
	b, err := json.Marshal(frame)
	if err != nil {
		return false
	}
	if c.closed.Load() {
		return false
	}
	select {
	case c.send <- b:
		return true
	default:
		return false
	}
}

// Close closes the send channel, which causes writePump to exit. Safe to
// call multiple times; subsequent Pushes after Close return false.
func (c *Conn) Close() {
	c.closeOnce.Do(func() {
		c.closed.Store(true)
		close(c.send)
	})
}

// writePump drains c.send and writes to the WebSocket.
// It exits when c.send is closed.
func (c *Conn) writePump() {
	defer c.ws.Close()
	for b := range c.send {
		c.ws.SetWriteDeadline(time.Now().Add(writeWait)) //nolint:errcheck
		if err := c.ws.WriteMessage(websocket.TextMessage, b); err != nil {
			break
		}
	}
}

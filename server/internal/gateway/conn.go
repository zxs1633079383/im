package gateway

import (
	"encoding/json"
	"sync"
	"time"

	"github.com/gorilla/websocket"
)

const (
	writeWait   = 10 * time.Second
	sendBufSize = 256
)

// Conn represents one authenticated WebSocket connection.
type Conn struct {
	UserID   int64
	DeviceID string // e.g. "web-<uuid>" or from JWT claim

	ws   *websocket.Conn
	send chan []byte // buffered outbound channel; closed on disconnect
	hub  *Hub

	// knownSeq tracks the last seq successfully pushed to this connection
	// per channel. Used to compute the pong diff.
	mu       sync.RWMutex
	knownSeq map[int64]int64 // channel_id → seq

	lastPong time.Time // updated on every pong received
}

// NewConn creates a new Conn and starts its writePump goroutine.
func NewConn(userID int64, deviceID string, ws *websocket.Conn, hub *Hub) *Conn {
	c := &Conn{
		UserID:   userID,
		DeviceID: deviceID,
		ws:       ws,
		send:     make(chan []byte, sendBufSize),
		hub:      hub,
		knownSeq: make(map[int64]int64),
		lastPong: time.Now(),
	}
	go c.writePump()
	return c
}

// UpdateKnownSeq records that this connection has received up to seq for channelID.
func (c *Conn) UpdateKnownSeq(channelID, seq int64) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if current, ok := c.knownSeq[channelID]; !ok || seq > current {
		c.knownSeq[channelID] = seq
	}
}

// KnownSeqFor returns the last known seq for channelID (0 if unknown).
func (c *Conn) KnownSeqFor(channelID int64) int64 {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.knownSeq[channelID]
}

// KnownSeqs returns a snapshot of the full knownSeq map.
func (c *Conn) KnownSeqs() map[int64]int64 {
	c.mu.RLock()
	defer c.mu.RUnlock()
	m := make(map[int64]int64, len(c.knownSeq))
	for k, v := range c.knownSeq {
		m[k] = v
	}
	return m
}

// Push enqueues a JSON-serialisable payload for asynchronous delivery.
// Returns false if the connection's send buffer is full (slow consumer).
func (c *Conn) Push(msgType WSMessageType, payload any) bool {
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
	select {
	case c.send <- b:
		return true
	default:
		return false // slow consumer — caller should close the conn
	}
}

// Close closes the send channel, which causes writePump to exit.
func (c *Conn) Close() {
	close(c.send)
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

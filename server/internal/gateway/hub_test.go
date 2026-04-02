package gateway

import (
	"encoding/json"
	"sync"
	"testing"
)

// mockConn creates a Conn with no real websocket, suitable for unit testing Hub logic.
// The send channel is buffered so Push calls don't block.
func mockConn(userID int64, deviceID string) *Conn {
	return &Conn{
		UserID:   userID,
		DeviceID: deviceID,
		send:     make(chan []byte, sendBufSize),
		knownSeq: make(map[int64]int64),
	}
}

func TestHub_RegisterAndConnsForUser(t *testing.T) {
	h := NewHub()
	c1 := mockConn(42, "web-1")
	c2 := mockConn(42, "web-2")
	c3 := mockConn(99, "mobile-1")

	h.Register(c1)
	h.Register(c2)
	h.Register(c3)

	conns := h.ConnsForUser(42)
	if len(conns) != 2 {
		t.Fatalf("expected 2 conns for user 42, got %d", len(conns))
	}

	conns99 := h.ConnsForUser(99)
	if len(conns99) != 1 {
		t.Fatalf("expected 1 conn for user 99, got %d", len(conns99))
	}

	// Unknown user returns nil.
	if h.ConnsForUser(0) != nil {
		t.Fatal("expected nil for unknown user")
	}
}

func TestHub_Deregister(t *testing.T) {
	h := NewHub()
	c1 := mockConn(42, "web-1")
	c2 := mockConn(42, "web-2")

	h.Register(c1)
	h.Register(c2)
	h.Deregister(c1)

	conns := h.ConnsForUser(42)
	if len(conns) != 1 {
		t.Fatalf("expected 1 conn after deregister, got %d", len(conns))
	}
	if conns[0] != c2 {
		t.Fatal("remaining conn should be c2")
	}

	// Deregister last conn removes the user entry.
	h.Deregister(c2)
	if h.ConnsForUser(42) != nil {
		t.Fatal("expected nil after all conns deregistered")
	}
}

func TestHub_Deregister_NoOp(t *testing.T) {
	h := NewHub()
	c := mockConn(7, "web-1")
	// Deregistering a conn that was never registered should not panic.
	h.Deregister(c)
}

func TestHub_PushToUser(t *testing.T) {
	h := NewHub()
	c1 := mockConn(42, "web-1")
	c2 := mockConn(42, "web-2")
	h.Register(c1)
	h.Register(c2)

	payload := map[string]string{"hello": "world"}
	sent := h.PushToUser(42, TypePushMsg, payload)
	if sent != 2 {
		t.Fatalf("expected 2 sends, got %d", sent)
	}

	// Both conns should have received a message.
	for i, c := range []*Conn{c1, c2} {
		select {
		case raw := <-c.send:
			var frame struct {
				Type    WSMessageType   `json:"type"`
				Payload json.RawMessage `json:"payload"`
			}
			if err := json.Unmarshal(raw, &frame); err != nil {
				t.Fatalf("conn %d: invalid JSON: %v", i+1, err)
			}
			if frame.Type != TypePushMsg {
				t.Fatalf("conn %d: expected type push_msg, got %s", i+1, frame.Type)
			}
		default:
			t.Fatalf("conn %d: send channel empty", i+1)
		}
	}
}

func TestHub_PushToUser_UnknownUser(t *testing.T) {
	h := NewHub()
	sent := h.PushToUser(999, TypePushMsg, map[string]string{})
	if sent != 0 {
		t.Fatalf("expected 0 sends for unknown user, got %d", sent)
	}
}

func TestHub_PushToUser_SlowConsumer(t *testing.T) {
	h := NewHub()
	// Fill the send buffer entirely before pushing via Hub.
	c := mockConn(1, "web-slow")
	for i := 0; i < sendBufSize; i++ {
		c.send <- []byte("{}")
	}
	h.Register(c)

	// Buffer is full: Push should return false, PushToUser should return 0.
	sent := h.PushToUser(1, TypePushMsg, map[string]string{"k": "v"})
	if sent != 0 {
		t.Fatalf("expected 0 (slow consumer), got %d", sent)
	}
}

func TestHub_ConcurrentAccess(t *testing.T) {
	h := NewHub()
	var wg sync.WaitGroup

	// Concurrently register and deregister many conns.
	for i := 0; i < 50; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			c := mockConn(int64(i%5), "dev")
			h.Register(c)
			h.ConnsForUser(int64(i % 5))
			h.PushToUser(int64(i%5), TypePushMsg, map[string]string{})
			h.Deregister(c)
		}(i)
	}
	wg.Wait()
}

func TestConn_UpdateAndReadKnownSeq(t *testing.T) {
	c := mockConn(1, "web-1")

	c.UpdateKnownSeq(10, 5)
	if got := c.KnownSeqFor(10); got != 5 {
		t.Fatalf("expected 5, got %d", got)
	}

	// Update with a lower seq should be ignored.
	c.UpdateKnownSeq(10, 3)
	if got := c.KnownSeqFor(10); got != 5 {
		t.Fatalf("expected 5 after lower update, got %d", got)
	}

	// Update with a higher seq should be applied.
	c.UpdateKnownSeq(10, 10)
	if got := c.KnownSeqFor(10); got != 10 {
		t.Fatalf("expected 10, got %d", got)
	}

	// Unknown channel returns 0.
	if got := c.KnownSeqFor(999); got != 0 {
		t.Fatalf("expected 0 for unknown channel, got %d", got)
	}
}

func TestConn_KnownSeqs_Snapshot(t *testing.T) {
	c := mockConn(1, "web-1")
	c.UpdateKnownSeq(1, 10)
	c.UpdateKnownSeq(2, 20)

	snap := c.KnownSeqs()
	if len(snap) != 2 {
		t.Fatalf("expected 2 entries in snapshot, got %d", len(snap))
	}
	if snap[1] != 10 || snap[2] != 20 {
		t.Fatalf("snapshot values incorrect: %v", snap)
	}

	// Mutating snapshot must not affect the conn.
	snap[1] = 999
	if c.KnownSeqFor(1) != 10 {
		t.Fatal("snapshot mutation affected conn's knownSeq")
	}
}

func TestConn_Push_InvalidPayload(t *testing.T) {
	c := mockConn(1, "web-1")
	// json.Marshal of a channel type fails — Push should return false.
	ok := c.Push(TypePushMsg, make(chan int))
	if ok {
		t.Fatal("expected Push to return false for un-marshallable payload")
	}
}

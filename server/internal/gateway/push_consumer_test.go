package gateway

import (
	"context"
	"encoding/json"
	"testing"
	"time"
)

// TestPushConsumer_Handle_FansOutToEveryTargetUID verifies the new envelope
// path: a single Pulsar message carrying N TargetUIDs produces one WS frame
// per uid on this pod. This is the exact regression that the old code had —
// the previous Handle assumed a single target_uid and silently dropped N-1.
func TestPushConsumer_Handle_FansOutToEveryTargetUID(t *testing.T) {
	hub := NewHub()
	// Pre-register three conns with a 4-entry send buffer so each can
	// receive one frame without blocking.
	for _, uid := range []int64{1, 2, 3} {
		hub.Register(&Conn{
			UserID:   uid,
			send:     make(chan []byte, 4),
			hub:      hub,
			knownSeq: map[int64]int64{},
		})
	}
	pc := NewPushConsumer(hub, "gw-self", "local", testLogger())

	env := PulsarPushEnvelope{
		TargetUIDs: []int64{1, 2, 3},
		MsgType:    TypeReadSync,
		Payload:    json.RawMessage(`{"channel_id":42,"read_seq":7}`),
	}
	data, err := json.Marshal(env)
	if err != nil {
		t.Fatal(err)
	}
	if err := pc.Handle(context.Background(), data); err != nil {
		t.Fatalf("Handle returned error: %v", err)
	}

	for _, uid := range []int64{1, 2, 3} {
		conns := hub.ConnsForUser(uid)
		if len(conns) != 1 {
			t.Fatalf("uid %d: want 1 conn, got %d", uid, len(conns))
		}
		select {
		case frame := <-conns[0].send:
			var f struct {
				Type    WSMessageType   `json:"type"`
				Payload json.RawMessage `json:"payload"`
			}
			if err := json.Unmarshal(frame, &f); err != nil {
				t.Fatalf("uid %d: unmarshal frame: %v", uid, err)
			}
			if f.Type != TypeReadSync {
				t.Fatalf("uid %d: WS frame type = %q, want %q", uid, f.Type, TypeReadSync)
			}
			if string(f.Payload) != `{"channel_id":42,"read_seq":7}` {
				t.Fatalf("uid %d: payload bytes differ: %s", uid, f.Payload)
			}
		case <-time.After(50 * time.Millisecond):
			t.Fatalf("uid %d: no frame received", uid)
		}
	}
}

// TestPushConsumer_Handle_SkipsOfflineTargets confirms the handler tolerates
// envelopes whose TargetUIDs include users not connected to this pod. Such
// entries appear when stale routing hands the sender a false positive;
// the handler must no-op on them without error.
func TestPushConsumer_Handle_SkipsOfflineTargets(t *testing.T) {
	hub := NewHub()
	hub.Register(&Conn{UserID: 1, send: make(chan []byte, 1), hub: hub, knownSeq: map[int64]int64{}})

	pc := NewPushConsumer(hub, "gw-self", "local", testLogger())
	env := PulsarPushEnvelope{
		TargetUIDs: []int64{1, 999}, // 999 not on this pod
		MsgType:    TypeReadSync,
		Payload:    json.RawMessage(`null`),
	}
	data, _ := json.Marshal(env)
	if err := pc.Handle(context.Background(), data); err != nil {
		t.Fatalf("Handle returned error: %v", err)
	}
	// uid 1 got its frame; uid 999 silently dropped.
	if got := len(hub.ConnsForUser(1)[0].send); got != 1 {
		t.Fatalf("uid 1 send buffer has %d frames; want 1", got)
	}
}

// TestPushConsumer_Handle_MalformedEnvelopeReturnsError asserts that bad JSON
// bubbles up so Pulsar NACKs (redelivery on retriable producer hiccups).
func TestPushConsumer_Handle_MalformedEnvelopeReturnsError(t *testing.T) {
	pc := NewPushConsumer(NewHub(), "gw-self", "local", testLogger())
	err := pc.Handle(context.Background(), []byte("not-json"))
	if err == nil {
		t.Fatal("expected error on malformed envelope, got nil")
	}
}

// TestExtractPushID covers the push_id peek helper used to decide whether a
// payload participates in ACK tracking.
func TestExtractPushID(t *testing.T) {
	cases := []struct {
		in   string
		want string
	}{
		{`{"push_id":"http-42-7"}`, "http-42-7"},
		{`{"channel_id":1}`, ""}, // non-push_msg payloads
		{`null`, ""},
		{``, ""},           // empty input
		{`not-json`, ""},   // recovers gracefully
	}
	for _, c := range cases {
		got := extractPushID(json.RawMessage(c.in))
		if got != c.want {
			t.Errorf("extractPushID(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

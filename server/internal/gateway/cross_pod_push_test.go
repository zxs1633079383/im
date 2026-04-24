package gateway

import (
	"context"
	"errors"
	"log/slog"
	"os"
	"sync"
	"testing"
)

// stubRouting implements routingLookup for tests.
type stubRouting struct {
	gws []string
	err error
}

func (s *stubRouting) Lookup(_ context.Context, _ int64) ([]string, error) {
	return s.gws, s.err
}

// stubSender implements crossPodSender, recording every Send call.
type stubSender struct {
	mu    sync.Mutex
	sends []stubSendCall
	err   error
}

type stubSendCall struct {
	key     string
	payload any
}

func (s *stubSender) Send(_ context.Context, key string, payload any) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.sends = append(s.sends, stubSendCall{key: key, payload: payload})
	return s.err
}

func (s *stubSender) calls() []stubSendCall {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]stubSendCall, len(s.sends))
	copy(out, s.sends)
	return out
}

// stubCache implements producerGetter. Each (topic) returns the same sender
// so tests can assert on total Send counts across all topics.
type stubCache struct {
	mu        sync.Mutex
	createErr error
	// perTopic maps topic string -> *stubSender (lazy-created on first ask).
	perTopic map[string]*stubSender
	// createOrder records topics in the order they were requested.
	createOrder []string
}

func newStubCache() *stubCache {
	return &stubCache{perTopic: map[string]*stubSender{}}
}

func (s *stubCache) GetOrCreate(_ context.Context, topic string) (crossPodSender, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.createErr != nil {
		return nil, s.createErr
	}
	if sender, ok := s.perTopic[topic]; ok {
		return sender, nil
	}
	sender := &stubSender{}
	s.perTopic[topic] = sender
	s.createOrder = append(s.createOrder, topic)
	return sender, nil
}

func (s *stubCache) topics() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]string, len(s.createOrder))
	copy(out, s.createOrder)
	return out
}

func (s *stubCache) sender(topic string) *stubSender {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.perTopic[topic]
}

func testLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))
}

// TestCrossPodPush_LocalHit verifies the local short-circuit of CrossPodPush:
// when the hub already holds a connection for userID, the routing/cache
// pathway must be skipped entirely (no calls into cache.GetOrCreate).
//
// We don't need a real WebSocket here — Hub.PushToUser returns the count of
// delivered connections, and we can pre-seed one Conn with a big enough send
// buffer by calling Register directly.
func TestCrossPodPush_LocalHit(t *testing.T) {
	t.Setenv("USER", "tester")

	hub := NewHub()
	// Fake but functional Conn: send channel large enough to accept one push
	// without blocking; no writePump is needed because we never read it.
	c := &Conn{
		UserID:   42,
		DeviceID: "dev-1",
		send:     make(chan []byte, 1),
		hub:      hub,
		knownSeq: map[int64]int64{},
	}
	hub.Register(c)

	cache := newStubCache()
	// crossPodPushImpl must NOT be invoked when there's a local hit, so check
	// cache stays untouched by calling the public CrossPodPush.
	hub.CrossPodPush(
		context.Background(),
		42,
		TypeMsgUpdated,
		map[string]any{"hello": "world"},
		nil, // routing — must not be used
		nil, // cache — must not be used
		"gw-self",
		"local",
		testLogger(),
	)

	if len(cache.topics()) != 0 {
		t.Fatalf("expected cache untouched on local hit; topics=%v", cache.topics())
	}
	// The send buffer should have received exactly one frame.
	if len(c.send) != 1 {
		t.Fatalf("expected 1 buffered frame on local conn; got %d", len(c.send))
	}
}

// TestCrossPodPush_RemoteOnly verifies that when routing returns a remote
// gateway, the cache is asked for a producer exactly once and Send is called
// exactly once with the user ID as partition key.
func TestCrossPodPush_RemoteOnly(t *testing.T) {
	t.Setenv("USER", "tester")

	hub := NewHub()
	routing := &stubRouting{gws: []string{"gw-remote"}}
	cache := newStubCache()

	hub.crossPodPushImpl(
		context.Background(),
		101,
		TypeMsgUpdated,
		map[string]any{"id": 7},
		routing,
		cache,
		"gw-self",
		"local",
		testLogger(),
	)

	topics := cache.topics()
	if len(topics) != 1 {
		t.Fatalf("expected 1 topic opened; got %v", topics)
	}
	wantTopic := PushTopicFor("gw-remote", "local")
	if topics[0] != wantTopic {
		t.Fatalf("topic = %q; want %q", topics[0], wantTopic)
	}
	sender := cache.sender(wantTopic)
	if sender == nil {
		t.Fatalf("sender for %q was not created", wantTopic)
	}
	calls := sender.calls()
	if len(calls) != 1 {
		t.Fatalf("expected 1 Send call; got %d", len(calls))
	}
	if calls[0].key != "101" {
		t.Fatalf("partition key = %q; want %q", calls[0].key, "101")
	}
}

// TestCrossPodPush_AllOffline verifies the empty-routing branch logs and
// makes no producer calls.
func TestCrossPodPush_AllOffline(t *testing.T) {
	hub := NewHub()
	routing := &stubRouting{gws: nil}
	cache := newStubCache()

	hub.crossPodPushImpl(
		context.Background(),
		7,
		TypeMsgDeleted,
		map[string]any{"msg_id": 1},
		routing,
		cache,
		"gw-self",
		"local",
		testLogger(),
	)

	if len(cache.topics()) != 0 {
		t.Fatalf("expected no topics when offline; got %v", cache.topics())
	}
}

// TestCrossPodPush_RoutingError logs and exits without calling the cache.
func TestCrossPodPush_RoutingError(t *testing.T) {
	hub := NewHub()
	routing := &stubRouting{err: errors.New("redis down")}
	cache := newStubCache()

	hub.crossPodPushImpl(
		context.Background(),
		7,
		TypeMsgUpdated,
		nil,
		routing,
		cache,
		"gw-self",
		"local",
		testLogger(),
	)
	if len(cache.topics()) != 0 {
		t.Fatalf("expected no topics on routing error; got %v", cache.topics())
	}
}

// TestCrossPodPush_SkipsSelf ensures the self gateway ID is excluded even if
// routing returns it (stale presence entries).
func TestCrossPodPush_SkipsSelf(t *testing.T) {
	t.Setenv("USER", "tester")

	hub := NewHub()
	routing := &stubRouting{gws: []string{"gw-self", "gw-other"}}
	cache := newStubCache()

	hub.crossPodPushImpl(
		context.Background(),
		55,
		TypeMsgUpdated,
		"payload",
		routing,
		cache,
		"gw-self",
		"local",
		testLogger(),
	)

	topics := cache.topics()
	if len(topics) != 1 {
		t.Fatalf("expected only gw-other topic; got %v", topics)
	}
	if topics[0] != PushTopicFor("gw-other", "local") {
		t.Fatalf("wrong topic: %q", topics[0])
	}
}

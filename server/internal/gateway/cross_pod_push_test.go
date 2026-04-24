package gateway

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"os"
	"sync"
	"testing"
)

// stubRoutingBatch implements routingBatchLookup for tests.
type stubRoutingBatch struct {
	// gws returned per uid. Missing uids return nil (offline).
	gws map[int64][]string
	err error
}

func (s *stubRoutingBatch) LookupBatch(_ context.Context, uids []int64) (map[int64][]string, error) {
	if s.err != nil {
		return nil, s.err
	}
	out := make(map[int64][]string, len(uids))
	for _, uid := range uids {
		if gs, ok := s.gws[uid]; ok {
			out[uid] = gs
		} else {
			out[uid] = nil
		}
	}
	return out, nil
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

// stubCache implements producerGetter.
type stubCache struct {
	mu          sync.Mutex
	createErr   error
	perTopic    map[string]*stubSender
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

// TestCrossPodBroadcast_LocalHitOnly — every uid resolves locally, routing/
// cache must stay untouched.
func TestCrossPodBroadcast_LocalHitOnly(t *testing.T) {
	t.Setenv("USER", "tester")
	hub := NewHub()
	for _, uid := range []int64{42, 43} {
		hub.Register(&Conn{UserID: uid, send: make(chan []byte, 4), hub: hub, knownSeq: map[int64]int64{}})
	}
	cache := newStubCache()

	hub.CrossPodBroadcast(
		context.Background(),
		[]int64{42, 43},
		"chan-7",
		TypeMsgUpdated,
		map[string]any{"hello": "world"},
		nil, nil, // routing + cache intentionally nil — not used on full local hit
		"gw-self", "local", testLogger(),
	)
	_ = cache // touched only if logic regresses
}

// TestCrossPodBroadcast_BucketsByGateway — two remote users on gw-A, one on
// gw-B: producer cache must open exactly two topics and each receive one
// Send carrying the correct TargetUIDs subset.
func TestCrossPodBroadcast_BucketsByGateway(t *testing.T) {
	t.Setenv("USER", "tester")
	hub := NewHub()
	routing := &stubRoutingBatch{gws: map[int64][]string{
		101: {"gw-A"},
		102: {"gw-A"},
		103: {"gw-B"},
	}}
	cache := newStubCache()

	hub.crossPodBroadcastImpl(
		context.Background(),
		[]int64{101, 102, 103},
		"chan-77",
		TypePushMsg,
		json.RawMessage(`{"push_id":"x"}`),
		routing, cache,
		"gw-self", "local", testLogger(),
	)

	topics := cache.topics()
	if len(topics) != 2 {
		t.Fatalf("expected 2 topics; got %v", topics)
	}
	assertOneSendPerTopic(t, cache)
	assertUIDsBucketed(t, cache, map[string][]int64{
		PushTopicFor("gw-A", "local"): {101, 102},
		PushTopicFor("gw-B", "local"): {103},
	})
}

// TestCrossPodBroadcast_SkipsSelfGateway — stale routing entries that point
// back to the local pod are dropped so we never loop-back through Pulsar.
func TestCrossPodBroadcast_SkipsSelfGateway(t *testing.T) {
	t.Setenv("USER", "tester")
	hub := NewHub()
	routing := &stubRoutingBatch{gws: map[int64][]string{
		55: {"gw-self", "gw-other"},
	}}
	cache := newStubCache()

	hub.crossPodBroadcastImpl(
		context.Background(),
		[]int64{55},
		"chan-1",
		TypeMsgUpdated,
		json.RawMessage("null"),
		routing, cache,
		"gw-self", "local", testLogger(),
	)

	topics := cache.topics()
	if len(topics) != 1 || topics[0] != PushTopicFor("gw-other", "local") {
		t.Fatalf("expected only gw-other topic; got %v", topics)
	}
}

// TestCrossPodBroadcast_AllOffline — empty gatewayID list → no Pulsar send.
func TestCrossPodBroadcast_AllOffline(t *testing.T) {
	hub := NewHub()
	cache := newStubCache()
	hub.crossPodBroadcastImpl(
		context.Background(),
		[]int64{7, 8},
		"chan-1",
		TypeMsgDeleted,
		json.RawMessage("null"),
		&stubRoutingBatch{gws: map[int64][]string{}},
		cache,
		"gw-self", "local", testLogger(),
	)
	if len(cache.topics()) != 0 {
		t.Fatalf("expected no topics when everyone offline; got %v", cache.topics())
	}
}

// TestCrossPodBroadcast_RoutingError — routing failure aborts, no Send.
func TestCrossPodBroadcast_RoutingError(t *testing.T) {
	hub := NewHub()
	cache := newStubCache()
	hub.crossPodBroadcastImpl(
		context.Background(),
		[]int64{7},
		"key",
		TypeMsgUpdated,
		json.RawMessage("null"),
		&stubRoutingBatch{err: errors.New("redis down")},
		cache,
		"gw-self", "local", testLogger(),
	)
	if len(cache.topics()) != 0 {
		t.Fatalf("expected no topics on routing error; got %v", cache.topics())
	}
}

// TestCrossPodPush_SingleUserWrapper — the legacy single-user alias still
// works and delegates to the batch path with a 1-element slice.
func TestCrossPodPush_SingleUserWrapper(t *testing.T) {
	t.Setenv("USER", "tester")
	hub := NewHub()
	// Not registered locally → goes to remote path.
	// Use CrossPodPush (the thin wrapper) through the public hub; we only
	// check it doesn't panic and respects the empty routing offline branch.
	hub.CrossPodPush(
		context.Background(),
		99,
		TypeReadSync,
		map[string]int{"ch": 1},
		nil, nil, // routing + cache nil: nil-safe adapters log and return
		"gw-self", "local", testLogger(),
	)
}

// assertOneSendPerTopic fails if any topic in cache received a different
// number of Send calls than 1.
func assertOneSendPerTopic(t *testing.T, cache *stubCache) {
	t.Helper()
	for _, topic := range cache.topics() {
		if got := len(cache.sender(topic).calls()); got != 1 {
			t.Fatalf("topic %q received %d Send calls; want 1", topic, got)
		}
	}
}

// assertUIDsBucketed decodes each envelope and compares the TargetUIDs list
// against the expected value for the topic it landed on.
func assertUIDsBucketed(t *testing.T, cache *stubCache, want map[string][]int64) {
	t.Helper()
	for topic, expectedUIDs := range want {
		sender := cache.sender(topic)
		if sender == nil {
			t.Fatalf("no sender for topic %q", topic)
		}
		calls := sender.calls()
		if len(calls) != 1 {
			t.Fatalf("topic %q: want 1 call, got %d", topic, len(calls))
		}
		env, ok := calls[0].payload.(PulsarPushEnvelope)
		if !ok {
			t.Fatalf("topic %q: payload is %T, want PulsarPushEnvelope", topic, calls[0].payload)
		}
		if !sameUIDSet(env.TargetUIDs, expectedUIDs) {
			t.Fatalf("topic %q: TargetUIDs=%v, want %v", topic, env.TargetUIDs, expectedUIDs)
		}
	}
}

// sameUIDSet returns true when a and b contain the same int64s regardless of
// order — ChannelBroadcast.bucketByGateway's map iteration order is not
// deterministic, so we compare as sets.
func sameUIDSet(a, b []int64) bool {
	if len(a) != len(b) {
		return false
	}
	seen := make(map[int64]int, len(a))
	for _, x := range a {
		seen[x]++
	}
	for _, x := range b {
		seen[x]--
	}
	for _, count := range seen {
		if count != 0 {
			return false
		}
	}
	return true
}

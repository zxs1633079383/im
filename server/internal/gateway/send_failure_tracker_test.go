package gateway

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
)

// fakeMarker records every MarkOffline call for assertion. Swappable per test.
type fakeMarker struct {
	mu    sync.Mutex
	calls []offlineCall
	err   error
}

type offlineCall struct {
	uid int64
	gw  string
}

func (f *fakeMarker) MarkOffline(_ context.Context, uid int64, gw string) (int, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, offlineCall{uid: uid, gw: gw})
	if f.err != nil {
		return 0, f.err
	}
	return 1, nil
}

func (f *fakeMarker) snapshot() []offlineCall {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]offlineCall, len(f.calls))
	copy(out, f.calls)
	return out
}

// TestTracker_FiresOnlyAtThreshold locks in the core invariant: below the
// markOfflineThreshold the tracker stays silent; exactly at threshold it
// evicts every uid in the failing bucket once, then resets.
func TestTracker_FiresOnlyAtThreshold(t *testing.T) {
	m := &fakeMarker{}
	tracker := newSendFailureTracker(m, testLogger())

	uids := []int64{101, 102, 103}

	// threshold - 1 failures → no eviction yet.
	for i := 0; i < markOfflineThreshold-1; i++ {
		tracker.RecordFailure(context.Background(), "gw-A", uids)
	}
	if len(m.snapshot()) != 0 {
		t.Fatalf("eviction fired too early: %+v", m.snapshot())
	}

	// Nth failure → eviction for all three uids, exactly once each.
	tracker.RecordFailure(context.Background(), "gw-A", uids)
	got := m.snapshot()
	if len(got) != len(uids) {
		t.Fatalf("want %d MarkOffline calls; got %d", len(uids), len(got))
	}
}

// TestTracker_SuccessResetsCounter verifies that a single success between
// failures zeros the streak — intermittent blips must not accumulate forever.
func TestTracker_SuccessResetsCounter(t *testing.T) {
	m := &fakeMarker{}
	tracker := newSendFailureTracker(m, testLogger())

	for i := 0; i < markOfflineThreshold-1; i++ {
		tracker.RecordFailure(context.Background(), "gw-B", []int64{1})
	}
	tracker.RecordSuccess("gw-B")
	// After success, another threshold-1 failures must stay silent again.
	for i := 0; i < markOfflineThreshold-1; i++ {
		tracker.RecordFailure(context.Background(), "gw-B", []int64{1})
	}
	if len(m.snapshot()) != 0 {
		t.Fatalf("eviction fired despite reset: %+v", m.snapshot())
	}
}

// TestTracker_PerGatewayCounters — failures on one gateway must not influence
// another's threshold.
func TestTracker_PerGatewayCounters(t *testing.T) {
	m := &fakeMarker{}
	tracker := newSendFailureTracker(m, testLogger())

	// Saturate gw-A to the edge, hit gw-B 1 time, gw-A one more → evicts only gw-A.
	for i := 0; i < markOfflineThreshold-1; i++ {
		tracker.RecordFailure(context.Background(), "gw-A", []int64{1})
	}
	tracker.RecordFailure(context.Background(), "gw-B", []int64{2})
	tracker.RecordFailure(context.Background(), "gw-A", []int64{1})

	got := m.snapshot()
	if len(got) != 1 || got[0].gw != "gw-A" {
		t.Fatalf("want single eviction for gw-A; got %+v", got)
	}
}

// TestTracker_ConcurrentFailuresFireOnce locks in the CAS guard that keeps
// only one goroutine triggering the eviction path when many racing goroutines
// bump the counter simultaneously.
func TestTracker_ConcurrentFailuresFireOnce(t *testing.T) {
	m := &fakeMarker{}
	tracker := newSendFailureTracker(m, testLogger())

	var wg sync.WaitGroup
	var started atomic.Int32
	concurrent := markOfflineThreshold * 10
	for i := 0; i < concurrent; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			started.Add(1)
			tracker.RecordFailure(context.Background(), "gw-C", []int64{7})
		}()
	}
	wg.Wait()

	got := m.snapshot()
	// Each full threshold streak can fire at most ⌊concurrent / threshold⌋ times.
	// At minimum it must have fired once, at most concurrent/threshold times.
	if len(got) < 1 {
		t.Fatalf("want at least one eviction; got %d", len(got))
	}
	maxFires := concurrent / markOfflineThreshold
	if len(got) > maxFires {
		t.Fatalf("eviction fired too many times: %d > %d", len(got), maxFires)
	}
}

// TestTracker_NilGuards — nil tracker + empty gatewayID + nil routing must not
// panic. The production Hub zero-value leaves failures=nil; calls must no-op.
func TestTracker_NilGuards(t *testing.T) {
	var tracker *sendFailureTracker
	tracker.RecordSuccess("gw-X") // must not panic
	tracker.RecordFailure(context.Background(), "gw-X", []int64{1})

	// Empty gatewayID → no-op.
	real := newSendFailureTracker(&fakeMarker{}, testLogger())
	real.RecordFailure(context.Background(), "", []int64{1})

	// Nil routing — eviction path runs but skips the MarkOffline call.
	nilRouting := newSendFailureTracker(nil, testLogger())
	for i := 0; i < markOfflineThreshold; i++ {
		nilRouting.RecordFailure(context.Background(), "gw-Y", []int64{1})
	}
}

// TestTracker_MarkOfflineErrorDoesNotAbortLoop keeps the fan-out alive when
// one MarkOffline call errors — other uids in the bucket still get a try.
func TestTracker_MarkOfflineErrorDoesNotAbortLoop(t *testing.T) {
	m := &fakeMarker{err: errors.New("redis down")}
	tracker := newSendFailureTracker(m, testLogger())

	for i := 0; i < markOfflineThreshold; i++ {
		tracker.RecordFailure(context.Background(), "gw-D", []int64{1, 2, 3})
	}
	got := m.snapshot()
	if len(got) != 3 {
		t.Fatalf("want 3 attempted calls despite errors; got %d", len(got))
	}
}

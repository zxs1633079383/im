package gateway

import (
	"context"
	"log/slog"
	"sync"
	"sync/atomic"
)

// markOfflineThreshold is the number of consecutive producer.Send failures to
// a single destination gatewayID before the broadcast side evicts every user
// that was routed there. One-off Pulsar hiccups (broker re-election, transient
// network) should not evict — three in a row means the target pod is really
// gone and waiting for the 45s routing TTL to expire just wastes fan-out.
const markOfflineThreshold = 3

// offlineMarker is the narrow subset of repo.Routing the failure tracker needs.
// Lets unit tests stub Redis out.
type offlineMarker interface {
	MarkOffline(ctx context.Context, userID string, gatewayID string) (int, error)
}

// sendFailureTracker counts consecutive producer.Send failures per destination
// gatewayID across the broadcast fan-out. When a gateway tips over the
// threshold, every TargetUID in the failing bucket is evicted from routing so
// subsequent broadcasts skip the dead pod entirely.
//
// Counters are keyed by gatewayID (not user) because one crashed pod affects
// every user routed to it. Concurrent RecordFailure / RecordSuccess calls are
// safe: per-gwID counts are atomic.Int32, and the eviction Fire-once is
// serialised by a CAS from threshold → 0.
type sendFailureTracker struct {
	counters sync.Map // map[string]*atomic.Int32
	routing  offlineMarker
	log      *slog.Logger
}

// newSendFailureTracker wires a routing-backed tracker. log is used for
// non-fatal eviction reports; callers may pass slog.Default().
func newSendFailureTracker(routing offlineMarker, log *slog.Logger) *sendFailureTracker {
	if log == nil {
		log = slog.Default()
	}
	return &sendFailureTracker{routing: routing, log: log}
}

// RecordSuccess resets the failure count for gatewayID to zero. Called in the
// send-succeeded branch so an intermittent blip cannot accumulate forever.
func (t *sendFailureTracker) RecordSuccess(gatewayID string) {
	if t == nil {
		return
	}
	if c := t.counter(gatewayID); c != nil {
		c.Store(0)
	}
}

// RecordFailure bumps the failure count for gatewayID; if the threshold is
// reached it calls routing.MarkOffline once for every uid in the failing
// bucket and resets the counter so re-arming requires another streak.
//
// userIDs is the TargetUIDs slice from the envelope that just failed to send.
// Passing an empty slice is a no-op (nothing to evict).
//
// Eviction is best-effort — if MarkOffline returns an error we log and keep
// going so one flaky Redis Lua call cannot wedge the whole fan-out loop.
func (t *sendFailureTracker) RecordFailure(ctx context.Context, gatewayID string, userIDs []string) {
	if t == nil || gatewayID == "" {
		return
	}
	c := t.counter(gatewayID)
	if c == nil {
		return
	}
	n := c.Add(1)
	if n < markOfflineThreshold {
		return
	}
	// CAS-reset threshold → 0 so only one goroutine fires the eviction path.
	if !c.CompareAndSwap(n, 0) {
		return
	}
	t.evict(ctx, gatewayID, userIDs)
}

// evict is the per-bucket MarkOffline loop. Separated so RecordFailure stays
// under the 60-line function cap and so tests can exercise it directly.
func (t *sendFailureTracker) evict(ctx context.Context, gatewayID string, userIDs []string) {
	if t.routing == nil || len(userIDs) == 0 {
		return
	}
	evicted := 0
	for _, uid := range userIDs {
		n, err := t.routing.MarkOffline(ctx, uid, gatewayID)
		if err != nil {
			t.log.Warn("mark offline failed",
				"gw", gatewayID, "uid", uid, "error", err)
			continue
		}
		evicted += n
	}
	t.log.Info("routing eviction after send failures",
		"gw", gatewayID, "uid_count", len(userIDs), "entries_evicted", evicted,
		"threshold", markOfflineThreshold)
}

// counter lazily creates the atomic counter for gatewayID using LoadOrStore so
// concurrent first-time RecordFailure calls observe the same counter.
func (t *sendFailureTracker) counter(gatewayID string) *atomic.Int32 {
	if v, ok := t.counters.Load(gatewayID); ok {
		return v.(*atomic.Int32)
	}
	fresh := &atomic.Int32{}
	actual, _ := t.counters.LoadOrStore(gatewayID, fresh)
	return actual.(*atomic.Int32)
}

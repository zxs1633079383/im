package service

import (
	"context"
	"fmt"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/metric"

	"im-server/internal/repo"
)

// Sync tunables — channel_event.event_seq cursor algorithm (C019 §3.2).
//
// 历史背景：v1 messages.seq cursor 已于 2026-05-17 cutover 整族下线（原
// 函数 SyncV2 此次重命名为 Sync）。本项目唯一 client = cses-client，已
// 同步迁移到 event_seq，不存在 v1 client 需要兼容。
const (
	// MaxChannelsPerCall caps the number of cursors one /api/sync call may
	// carry. Clients holding more channels must batch across multiple calls.
	// Contract locked, 对齐 docs/BACKEND.md §3.3; 改动前先改文档并通知前端.
	MaxChannelsPerCall = 500

	// EventLimitPerChannel caps the per-channel events returned in one
	// Sync call. Mirrors C019 §3.2 — 200 is large enough to cover a
	// typical "back from holiday" sync in one round trip while bounding
	// the response size; bigger gaps recurse via Kind=slice + NextCursor.
	EventLimitPerChannel = 200

	// EventTooLongThreshold — if (serverEventSeq - clientEventSeq) > this,
	// the per-channel delta is short-circuited to Kind=too_long{reset_to=
	// serverEventSeq}. Client clears local rows for that channel and
	// re-fetches the first screen via /messagesAround (cf. TG
	// differenceTooLong; C019 §3.2). 10000 = EventLimitPerChannel × 50, the
	// scale at which incremental replay loses to a clean snapshot.
	EventTooLongThreshold = 10000
)

// EventKind enumerates the sync per-channel kind tag. Wire form is the
// lower-case string for compactness (匹配 C019 §3.1 wire 契约 + cses-client
// `types_v2::SyncEntryKind` 内部标签 enum)。
type EventKind string

const (
	// KindEmpty — client cursor already at or past serverEventSeq, no
	// events to deliver. Still emitted so the client can confirm the
	// channel is in sync (vs. silently dropped, which would be ambiguous
	// with "no membership").
	KindEmpty EventKind = "empty"
	// KindEvents — full incremental delta delivered, gap ≤ EventLimit and
	// ≤ TooLongThreshold. Client can advance its cursor to the highest
	// event_seq it observed.
	KindEvents EventKind = "events"
	// KindSlice — gap exceeded EventLimitPerChannel; only the first
	// EventLimit events are returned. Client must recurse with the
	// supplied NextCursor to fetch the rest.
	KindSlice EventKind = "slice"
	// KindTooLong — gap exceeded EventTooLongThreshold; client clears
	// local rows for this channel and rebases at ResetTo == serverEventSeq.
	// No Events / Messages payload — pure protocol signal.
	KindTooLong EventKind = "too_long"
)

// SyncChannelStore is the subset of repo.ChannelRepo SyncService needs.
// Defined consumer-side (Go's "accept small interfaces" idiom) so the service
// surface is documented at the call site.
type SyncChannelStore interface {
	GetMemberChannelSeqs(ctx context.Context, userID string) (map[string]int64, error)
	GetMember(ctx context.Context, channelID string, userID string) (*repo.ChannelMember, error)
}

// SyncMsgStore is the subset of repo.MessageRepo SyncService needs.
//
// GetByIDsForUser is the bulk-fetch path — Sync reads channel_event rows
// then hydrates the referenced message snapshots in one call so the wire
// payload is self-contained (C019 §3.2 step 3 — clients must not need a
// follow-up /messages call to interpret an event).
type SyncMsgStore interface {
	GetByIDsForUser(ctx context.Context, userID string, ids []string) ([]repo.Message, error)
}

// SyncEventStore is the subset of repo.ChannelEventRepo SyncService needs.
// Defined consumer-side so the SyncService surface lists every dependency
// at the call site.
//
// FetchAfter walks the per-channel append-only event log from a client
// cursor (event_seq strictly > afterEventSeq, ascending). limit ≤ 0 = no
// bound (Sync always passes EventLimitPerChannel).
//
// GetMemberChannelEventSeqs returns {channel_id: max(event_seq)} for every
// channel the user is a member of — drives the per-channel decision loop
// (which channels need an update? which are unknown to the client?).
type SyncEventStore interface {
	FetchAfter(ctx context.Context, channelID string, afterEventSeq int64, limit int) ([]repo.ChannelEvent, error)
	GetMemberChannelEventSeqs(ctx context.Context, userID string) (map[string]int64, error)
}

// SyncCursor is one channel cursor from the client, addressed by
// channel_event.event_seq.
//
// 命名规约：必须叫 `event_seq` 而不是 `seq` (C019 §2.1)，否则与历史的
// messages.seq 同名却语义不同，会让客户端误用而 silently re-introduce
// edit/delete-of-old-message 看不到的 bug（重设计本意就是修这个）。
type SyncCursor struct {
	ID       string
	EventSeq int64
}

// SyncParams is the input to SyncService.Sync.
type SyncParams struct {
	Cursors []SyncCursor
}

// SyncEntryKind is the per-channel kind tag. EventKind is a typed enum
// (cf. C019 §2.3 — "kind 用 string 字面量" is the C019 §2.3 anti-pattern).
// ResetTo only carries a value when Type=="too_long"; omitempty drops it
// for the other three kinds.
type SyncEntryKind struct {
	Type    EventKind `json:"type"`
	ResetTo int64     `json:"reset_to,omitempty"`
}

// SyncChannelDelta is the per-channel result for Sync.
//
// Events carries the channel_event rows for this channel (ordered by
// event_seq ASC). Messages carries the message snapshots referenced by
// those events, keyed by message id (the same id may appear in multiple
// events — e.g. NEW then EDIT — but the snapshot is deduplicated).
//
// ServerEventSeq is the current channel_event high-water mark on the
// server side; the client should advance its cursor to either max(Events
// event_seq) or — for Kind=empty / Kind=too_long — ServerEventSeq itself.
//
// NextCursor is non-nil only when Kind=slice (continuation cursor for
// the next call).
type SyncChannelDelta struct {
	ID             string
	ServerEventSeq int64
	Unread         int64
	Events         []repo.ChannelEvent
	Messages       map[string]repo.Message
	Kind           *SyncEntryKind
	NextCursor     *int64
}

// SyncResult bundles the per-channel deltas. The transport layer wraps
// this in {"channels": [...]}.
type SyncResult struct {
	Channels []SyncChannelDelta
}

// SyncService implements the batch incremental-sync algorithm on top of
// SyncChannelStore + SyncMsgStore + SyncEventStore using
// channel_event.event_seq as the cursor (C019 §3.2). See Sync below.
type SyncService struct {
	channels SyncChannelStore
	messages SyncMsgStore
	events   SyncEventStore
}

// NewSyncService wires the supplied stores. All three are required —
// passing a nil SyncEventStore is a programming error and will panic on
// first Sync call. Production wires this in cmd/gateway/main.go.
func NewSyncService(channels SyncChannelStore, messages SyncMsgStore, events SyncEventStore) *SyncService {
	if events == nil {
		panic("sync: SyncEventStore must not be nil (cutover-ban: v1 fallback removed)")
	}
	return &SyncService{channels: channels, messages: messages, events: events}
}

// Sync computes the per-channel deltas the caller needs to catch up,
// using channel_event.event_seq as the cursor (C019 §3.2).
//
// Algorithm:
//  1. Load all per-channel event high-water marks for the user.
//  2. Build a client-cursor map from the request body.
//  3. For each membership channel:
//     - client cursor >= serverEventSeq → Kind=empty (still emitted so
//       client can confirm sync state).
//     - gap > EventTooLongThreshold → Kind=too_long{reset_to=serverEventSeq};
//       no events / messages payload (client clears local rows + refetches
//       first screen via /messagesAround).
//     - unknown channel (no client cursor): treated as gap from zero —
//       small/known gap → events; larger → slice; but never too_long for
//       a new channel (the client has nothing to "clear" yet, so deliver
//       content instead of bouncing it to /messagesAround).
//     - Otherwise: FetchAfter the next EventLimitPerChannel events; if
//       saturated → Kind=slice + NextCursor=max(event_seq); else
//       Kind=events.
//  4. Hydrate referenced message snapshots in one bulk fetch per channel
//     via GetByIDsForUser (visibility filter applied at SQL level — events
//     for messages the user can't see are returned without an entry in
//     Messages; client must tolerate the missing key as "filtered").
//  5. Compute unread the same way the legacy v1 path did (membership state
//     derived).
//
// Membership-revoked channels silently drop out — they don't appear in
// GetMemberChannelEventSeqs. Per-channel fetch errors are non-fatal — the
// channel still appears with an empty Events slice (log-and-continue
// parity with legacy v1).
func (s *SyncService) Sync(ctx context.Context, callerID string, p SyncParams) (SyncResult, error) {
	ctx, span := tracer.Start(ctx, "SyncService.Sync")
	defer span.End()

	serverEventSeqs, err := s.events.GetMemberChannelEventSeqs(ctx, callerID)
	if err != nil {
		return SyncResult{}, fmt.Errorf("get member channel_event seqs: %w", err)
	}

	clientEventSeqs := make(map[string]int64, len(p.Cursors))
	for _, c := range p.Cursors {
		clientEventSeqs[c.ID] = c.EventSeq
	}

	results := make([]SyncChannelDelta, 0, len(serverEventSeqs))
	for chID, serverEventSeq := range serverEventSeqs {
		clientEventSeq, known := clientEventSeqs[chID]
		delta := SyncChannelDelta{ID: chID, ServerEventSeq: serverEventSeq}

		// Unread is independent of the event log — same membership-state
		// formula the legacy v1 path used so clients can keep their
		// badge-rendering logic. Member-fetch error → unread=0 (v1 parity).
		if member, err := s.channels.GetMember(ctx, chID, callerID); err == nil {
			if mSeqs, err := s.channels.GetMemberChannelSeqs(ctx, callerID); err == nil {
				if serverMsgSeq, ok := mSeqs[chID]; ok {
					unread := (serverMsgSeq - member.LastReadSeq) -
						(member.PhantomCount - member.PhantomAtRead)
					if unread < 0 {
						unread = 0
					}
					delta.Unread = unread
				}
			}
		}

		s.fillDeltaPayload(ctx, &delta, chID, callerID, clientEventSeq, known)
		results = append(results, delta)
	}

	recordSyncMetrics(ctx, results)
	return SyncResult{Channels: results}, nil
}

// fillDeltaPayload writes Events / Messages / Kind / NextCursor on delta
// according to the C019 §3.2 decision tree. Split out so Sync stays
// focused on the membership loop / cursor math.
//
// Decision tree:
//   - known && client >= server → empty.
//   - known && gap > TooLong  → too_long{reset_to=server}.
//   - else fetch up to EventLimit events; saturated → slice + NextCursor;
//     not saturated → events (empty Events slice tolerated when the user
//     was added to the channel just before this call but no message has
//     fired yet — len(events) == 0 still tagged events, not empty, so the
//     client can distinguish "I just joined" from "I'm fully synced").
func (s *SyncService) fillDeltaPayload(
	ctx context.Context,
	delta *SyncChannelDelta,
	chID, callerID string,
	clientEventSeq int64,
	known bool,
) {
	if known && clientEventSeq >= delta.ServerEventSeq {
		delta.Kind = &SyncEntryKind{Type: KindEmpty}
		return
	}

	gap := delta.ServerEventSeq - clientEventSeq
	if known && gap > EventTooLongThreshold {
		delta.Kind = &SyncEntryKind{Type: KindTooLong, ResetTo: delta.ServerEventSeq}
		return
	}

	// Walk the event log strictly after the client cursor. For unknown
	// channels clientEventSeq == 0 so we get the whole tail; for known
	// channels it picks up exactly where the client left off.
	events, err := s.events.FetchAfter(ctx, chID, clientEventSeq, EventLimitPerChannel)
	if err != nil {
		// Non-fatal — surface empty events so the channel appears in the
		// result (parity with v1's log-and-continue). Kind tagged events
		// rather than empty because we did *intend* to deliver — the
		// client retrying later will still see a non-empty server cursor.
		delta.Kind = &SyncEntryKind{Type: KindEvents}
		return
	}
	delta.Events = events

	// Bulk-hydrate referenced message snapshots. Same call for all
	// EventType branches — events without a MsgID (e.g. ReadMark / Member)
	// just contribute zero ids and the bulk call returns the others.
	msgIDs := uniqueMsgIDs(events)
	if len(msgIDs) > 0 {
		if msgs, err := s.messages.GetByIDsForUser(ctx, callerID, msgIDs); err == nil {
			delta.Messages = indexMessagesByID(msgs)
		}
		// Missing/visibility-filtered ids leave Messages without an
		// entry. Clients must tolerate this — the runbook §3.3 dispatch
		// table reads `messages.get(id)` not `messages[id]`.
	}

	if len(events) == EventLimitPerChannel {
		delta.Kind = &SyncEntryKind{Type: KindSlice}
		next := events[len(events)-1].EventSeq
		delta.NextCursor = &next
	} else {
		delta.Kind = &SyncEntryKind{Type: KindEvents}
	}
}

// recordSyncMetrics feeds the Grafana "Sync" row: response count (tagged
// is_empty / has_slice), plus histograms over channels + events returned.
// Split out so Sync itself stays focused on the cursor math.
func recordSyncMetrics(ctx context.Context, results []SyncChannelDelta) {
	m := metrics()
	totalEvents := 0
	anySlice := false
	for _, d := range results {
		totalEvents += len(d.Events)
		if d.Kind != nil && d.Kind.Type == KindSlice {
			anySlice = true
		}
	}
	isEmpty := "0"
	if len(results) == 0 {
		isEmpty = "1"
	}
	hasMore := "0"
	if anySlice {
		hasMore = "1"
	}
	if m.SyncResp != nil {
		m.SyncResp.Add(ctx, 1, metric.WithAttributes(
			attribute.String("is_empty", isEmpty),
			attribute.String("has_more", hasMore),
		))
	}
	if m.SyncChannels != nil {
		m.SyncChannels.Record(ctx, int64(len(results)))
	}
	if m.SyncMessages != nil {
		m.SyncMessages.Record(ctx, int64(totalEvents))
	}
}

// uniqueMsgIDs collects the distinct non-nil MsgID values from events,
// preserving first-occurrence order. Returned slice is empty (not nil)
// when no event carries a message id.
func uniqueMsgIDs(events []repo.ChannelEvent) []string {
	if len(events) == 0 {
		return nil
	}
	seen := make(map[string]struct{}, len(events))
	out := make([]string, 0, len(events))
	for _, e := range events {
		if e.MsgID == nil {
			continue
		}
		if _, dup := seen[*e.MsgID]; dup {
			continue
		}
		seen[*e.MsgID] = struct{}{}
		out = append(out, *e.MsgID)
	}
	return out
}

// indexMessagesByID re-indexes a flat []Message into a map[id]Message for
// the wire payload. GetByIDsForUser doesn't preserve caller-supplied id
// order so this is required (cf. C019 §3.2 — "messages map[id]snapshot").
func indexMessagesByID(msgs []repo.Message) map[string]repo.Message {
	if len(msgs) == 0 {
		return nil
	}
	out := make(map[string]repo.Message, len(msgs))
	for _, m := range msgs {
		out[m.ID] = m
	}
	return out
}

package service

import (
	"context"
	"fmt"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/metric"

	"im-server/internal/repo"
)

// Sync tunables — preserved verbatim from the legacy handler so the response
// shape (and per-channel message budget) is identical after the cut-over.
const (
	// SyncGapThreshold is the largest seq-gap the server will return as a
	// full incremental delta. Larger gaps return has_more=true plus the last
	// SyncMsgLimit messages.
	SyncGapThreshold = 100
	// SyncMsgLimit caps the per-channel messages returned for new channels
	// or large-gap fast-forward — bounds the response size.
	SyncMsgLimit = 50
	// MaxChannelsPerCall caps the number of cursors one /api/sync call may
	// carry. Clients holding more channels must batch across multiple calls.
	// Contract locked, 对齐 docs/BACKEND.md §3.3; 改动前先改文档并通知前端.
	MaxChannelsPerCall = 500
	// SyncTooLongSeqDiff — v0.7.3 P-7.5: gap > this value → return
	// `kind:{type:"too_long", reset_to: serverSeq}` so the client clears its
	// local `message` rows for that channel and re-fetches the first screen
	// via `messagesAround`. 设计参 TG `differenceTooLong` + TDLib
	// MAX_CHANNEL_DIFFERENCE=100；10000 = SyncMsgLimit×200 是 fast-forward
	// 也无意义的边界（user 离线 ≥ 一周或 在 万人群里时典型触发）。
	SyncTooLongSeqDiff = 10000

	// EventLimitPerChannel caps the per-channel events returned in one
	// SyncV2 call. Mirrors C019 §3.2 — 200 is large enough to cover a
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

// EventKind enumerates the v2 sync per-channel kind tag. Wire form is the
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
// GetByIDsForUser is the v2-only bulk-fetch path — sync v2 reads
// channel_event rows then hydrates the referenced message snapshots in one
// call so the wire payload is self-contained (C019 §3.2 step 3 — clients
// must not need a follow-up /messages call to interpret an event).
type SyncMsgStore interface {
	FetchForUser(ctx context.Context, channelID string, userID string, afterSeq int64, limit int) ([]repo.Message, error)
	GetByIDsForUser(ctx context.Context, userID string, ids []string) ([]repo.Message, error)
}

// SyncEventStore is the subset of repo.ChannelEventRepo SyncService.SyncV2
// needs. Defined consumer-side so the SyncService surface lists every
// dependency at the call site.
//
// FetchAfter walks the per-channel append-only event log from a client
// cursor (event_seq strictly > afterEventSeq, ascending). limit ≤ 0 = no
// bound (sync v2 always passes EventLimitPerChannel).
//
// GetMemberChannelEventSeqs returns {channel_id: max(event_seq)} for every
// channel the user is a member of — drives the per-channel decision loop
// (which channels need an update? which are unknown to the client?).
type SyncEventStore interface {
	FetchAfter(ctx context.Context, channelID string, afterEventSeq int64, limit int) ([]repo.ChannelEvent, error)
	GetMemberChannelEventSeqs(ctx context.Context, userID string) (map[string]int64, error)
}

// SyncCursor is one channel cursor from the client.
//
// contract locked, 对齐 docs/BACKEND.md §3.3; 改动前先改文档并通知前端.
type SyncCursor struct {
	ID  string
	Seq int64 // client's local max seq for this channel
}

// SyncParams is the input to SyncService.Sync — the caller's per-channel
// cursors. The transport layer constructs this from the JSON body.
//
// contract locked, 对齐 docs/BACKEND.md §3.3; 改动前先改文档并通知前端.
type SyncParams struct {
	Cursors []SyncCursor
}

// SyncEntryKind is the v0.7.3 4-branch tag (Empty / Full / Slice / TooLong)
// the server writes on each per-channel delta. Wire form matches Rust client
// `types_v2::SyncEntryKind` (internally tagged enum, `tag="type"`,
// rename_all=snake_case):
//
//	{"type":"empty"}
//	{"type":"full"}
//	{"type":"slice"}
//	{"type":"too_long","reset_to":N}
//
// `ResetTo` only carries a value when Type=="too_long"; omitempty drops it
// for the other three.
type SyncEntryKind struct {
	Type    string `json:"type"`
	ResetTo int64  `json:"reset_to,omitempty"`
}

// SyncChannelDelta is the per-channel sync result for one channel that has
// changes. v0.7.3 P-7.5 adds Kind + NextCursor (omitempty 渐进切换), preserving
// the legacy HasMore field for old clients still doing fallback inference.
//
// contract locked, 对齐 docs/BACKEND.md §3.3; 改动前先改文档并通知前端.
type SyncChannelDelta struct {
	ID         string
	ServerSeq  int64
	Unread     int64
	Messages   []repo.Message
	HasMore    bool
	Kind       *SyncEntryKind // v0.7.3 新字段：nil 时旧客户端 fallback default_for_legacy
	NextCursor *int64         // 仅 Kind.Type=="slice" 时非 nil
}

// SyncResult bundles the per-channel deltas. The transport layer wraps this
// in {"channels": [...]} to match the legacy SyncResponse shape.
//
// contract locked, 对齐 docs/BACKEND.md §3.3; 改动前先改文档并通知前端.
type SyncResult struct {
	Channels []SyncChannelDelta
}

// ─── v2 (event_seq cursor) wire shapes ──────────────────────────────────────
//
// All v2 types are append-only / additive — v1 is preserved verbatim so
// legacy clients (no event_seq field in request body) keep working through
// the old Sync() path. The dispatcher decision lives in the HTTP layer.
// See C019 §3 wire contract.

// SyncCursorV2 is one channel cursor from the client, addressed by
// channel_event.event_seq instead of messages.seq. The rename to
// `event_seq` is mandatory (C019 §2.1) — using `seq` would be ambiguous
// against messages.seq and silently re-introduce the bug the redesign
// exists to fix (edit/delete of old messages don't advance messages.seq
// → sync skips → client never sees the change).
type SyncCursorV2 struct {
	ID       string
	EventSeq int64
}

// SyncParamsV2 is the input to SyncService.SyncV2.
type SyncParamsV2 struct {
	Cursors []SyncCursorV2
}

// SyncEntryKindV2 is the v2 per-channel kind tag. EventKind is a typed
// enum (cf. C019 §2.3 — "kind 用 string 字面量" is the C019 §2.3 anti-pattern).
// ResetTo only carries a value when Type=="too_long"; omitempty drops it
// for the other three kinds.
type SyncEntryKindV2 struct {
	Type    EventKind `json:"type"`
	ResetTo int64     `json:"reset_to,omitempty"`
}

// SyncChannelDeltaV2 is the per-channel result for v2 sync.
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
type SyncChannelDeltaV2 struct {
	ID             string
	ServerEventSeq int64
	Unread         int64
	Events         []repo.ChannelEvent
	Messages       map[string]repo.Message
	Kind           *SyncEntryKindV2
	NextCursor     *int64
}

// SyncResultV2 bundles the per-channel v2 deltas. The transport layer
// wraps this in {"channels": [...]}.
type SyncResultV2 struct {
	Channels []SyncChannelDeltaV2
}

// SyncService implements the batch incremental-sync algorithm on top of
// SyncChannelStore + SyncMsgStore (+ SyncEventStore for v2). The v1 path
// (Sync, messages.seq cursor) is preserved verbatim from the legacy
// handler.SyncHandler — see Sync below for the four-case decision tree.
// The v2 path (SyncV2, channel_event.event_seq cursor) is the C019 redesign
// — see SyncV2 below.
//
// events may be nil — callers that only need v1 (legacy gateway start-up
// pre-Phase P5) can pass NewSyncService without it; SyncV2 then short-
// circuits with an explicit error. Production wires both via
// NewSyncServiceV2.
type SyncService struct {
	channels SyncChannelStore
	messages SyncMsgStore
	events   SyncEventStore
}

// NewSyncService wires the supplied stores for v1-only use. SyncV2 calls
// against a service built this way return an explicit error — callers that
// need v2 should use NewSyncServiceV2.
//
// Both repos satisfy the small interfaces above — production passes
// repo.ChannelRepo / repo.MessageRepo directly.
func NewSyncService(channels SyncChannelStore, messages SyncMsgStore) *SyncService {
	return &SyncService{channels: channels, messages: messages}
}

// NewSyncServiceV2 wires the supplied stores including the channel_event
// repo for v2 sync. Use this in production once the cut-over lands; the
// HTTP layer routes v1 vs v2 based on the request body shape (presence of
// `event_seq` field), so a single service instance handles both wire
// versions transparently.
func NewSyncServiceV2(channels SyncChannelStore, messages SyncMsgStore, events SyncEventStore) *SyncService {
	return &SyncService{channels: channels, messages: messages, events: events}
}

// Sync computes the per-channel deltas the caller needs to catch up.
//
// Algorithm (preserved from the legacy handler):
//  1. Load all channel seqs for the user from the DB (server source-of-truth).
//  2. Build a map of client-known seqs from the request.
//  3. For each server channel:
//     - client_seq >= server_seq → no change, skip.
//     - server_seq - client_seq <= SyncGapThreshold → return all missed messages.
//     - gap > threshold → return has_more=true + last SyncMsgLimit messages.
//     - channel unknown to client → new channel, return last SyncMsgLimit messages.
//  4. Compute unread for every returned channel from membership state.
//
// Channels the user is no longer a member of are silently dropped — they
// don't appear in GetMemberChannelSeqs. Per-channel fetch errors are
// non-fatal (the channel still appears with empty Messages, matching the
// legacy log-and-continue behaviour); the transport layer is responsible
// for logging.
func (s *SyncService) Sync(ctx context.Context, callerID string, p SyncParams) (SyncResult, error) {
	ctx, span := tracer.Start(ctx, "SyncService.Sync")
	defer span.End()

	serverSeqs, err := s.channels.GetMemberChannelSeqs(ctx, callerID)
	if err != nil {
		return SyncResult{}, fmt.Errorf("get member channel seqs: %w", err)
	}

	clientSeqs := make(map[string]int64, len(p.Cursors))
	for _, c := range p.Cursors {
		clientSeqs[c.ID] = c.Seq
	}

	results := make([]SyncChannelDelta, 0, len(serverSeqs))
	for chID, serverSeq := range serverSeqs {
		clientSeq, known := clientSeqs[chID]
		if known && clientSeq >= serverSeq {
			continue
		}

		delta := SyncChannelDelta{
			ID:        chID,
			ServerSeq: serverSeq,
		}

		// Compute unread from membership state. Match the legacy formula
		// exactly: (server_seq - last_read_seq) - (phantom_count - phantom_at_read),
		// floored at zero. Member-fetch errors leave unread=0 (legacy parity).
		if member, err := s.channels.GetMember(ctx, chID, callerID); err == nil {
			unread := (serverSeq - member.LastReadSeq) - (member.PhantomCount - member.PhantomAtRead)
			if unread < 0 {
				unread = 0
			}
			delta.Unread = unread
		}

		s.fillDeltaPayload(ctx, &delta, chID, callerID, clientSeq, known)
		results = append(results, delta)
	}

	recordSyncMetrics(ctx, results)
	return SyncResult{Channels: results}, nil
}

// fillDeltaPayload writes Messages / HasMore / Kind / NextCursor into delta
// according to the v0.7.3 four-branch decision tree. Split out of Sync so
// the main function stays under 60 lines per project Go style rules.
//
// Decision tree (参 TG `differenceDone` Empty/Slice/Full/TooLong + TDLib
// MAX_CHANNEL_DIFFERENCE=100):
//
//  1. gap > SyncTooLongSeqDiff → too_long{reset_to=serverSeq}, no messages.
//     Client clears local `message` rows for the channel + re-fetches first screen.
//  2. unknown channel (first sync for this peer) → full with latest SyncMsgLimit.
//     HasMore=true is set when the channel has older history; legacy clients
//     still infer from HasMore, new clients see kind="full".
//  3. small gap (<= SyncGapThreshold): return every missed message → empty if
//     none, full otherwise.
//  4. mid gap (SyncGapThreshold < gap <= SyncTooLongSeqDiff): slice — send
//     SyncMsgLimit oldest-of-gap messages + next_cursor = last_in_slice.seq.
func (s *SyncService) fillDeltaPayload(
	ctx context.Context,
	delta *SyncChannelDelta,
	chID, callerID string,
	clientSeq int64,
	known bool,
) {
	gap := delta.ServerSeq - clientSeq

	if known && gap > SyncTooLongSeqDiff {
		delta.Kind = &SyncEntryKind{Type: "too_long", ResetTo: delta.ServerSeq}
		return
	}

	switch {
	case !known:
		msgs, _ := s.fetchLatest(ctx, chID, callerID, delta.ServerSeq, SyncMsgLimit)
		delta.Messages = msgs
		delta.HasMore = delta.ServerSeq > int64(len(delta.Messages))
		delta.Kind = &SyncEntryKind{Type: "full"}
	case gap <= SyncGapThreshold:
		msgs, _ := s.messages.FetchForUser(ctx, chID, callerID, clientSeq, SyncGapThreshold)
		delta.Messages = msgs
		if len(msgs) == 0 {
			delta.Kind = &SyncEntryKind{Type: "empty"}
		} else {
			delta.Kind = &SyncEntryKind{Type: "full"}
		}
	default:
		// Slice: SyncGapThreshold < gap ≤ SyncTooLongSeqDiff.
		// FetchForUser already returns oldest-first (afterSeq+1 ascending), so
		// the last element is the highest seq in the slice → next_cursor.
		msgs, _ := s.messages.FetchForUser(ctx, chID, callerID, clientSeq, SyncMsgLimit)
		delta.Messages = msgs
		delta.HasMore = true
		delta.Kind = &SyncEntryKind{Type: "slice"}
		if n := len(msgs); n > 0 {
			next := msgs[n-1].Seq
			delta.NextCursor = &next
		}
	}
}

// recordSyncMetrics feeds the Grafana "Sync" row: response count (tagged
// is_empty / has_more), plus histograms over channels + messages returned.
// Split out so Sync itself stays focused on the cursor math.
func recordSyncMetrics(ctx context.Context, results []SyncChannelDelta) {
	m := metrics()
	totalMsgs := 0
	anyHasMore := false
	for _, d := range results {
		totalMsgs += len(d.Messages)
		if d.HasMore {
			anyHasMore = true
		}
	}
	isEmpty := "0"
	if len(results) == 0 {
		isEmpty = "1"
	}
	hasMore := "0"
	if anyHasMore {
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
		m.SyncMessages.Record(ctx, int64(totalMsgs))
	}
}

// fetchLatest returns up to limit messages with seq <= serverSeq for
// (chID, userID), ordered ascending. Implemented in terms of FetchForUser
// (which returns seq > afterSeq) by computing afterSeq = serverSeq - limit.
func (s *SyncService) fetchLatest(ctx context.Context, chID string, userID string, serverSeq int64, limit int) ([]repo.Message, error) {
	afterSeq := serverSeq - int64(limit)
	if afterSeq < 0 {
		afterSeq = 0
	}
	return s.messages.FetchForUser(ctx, chID, userID, afterSeq, limit)
}

// ErrSyncV2Unconfigured is returned by SyncV2 when the service was built
// via the legacy NewSyncService (no SyncEventStore wired). HTTP layer
// translates this to 501 so test rigs that only stand up v1 fail loudly
// rather than silently dropping all v2 traffic.
var ErrSyncV2Unconfigured = fmt.Errorf("sync v2: channel_event store not wired (use NewSyncServiceV2)")

// SyncV2 computes the per-channel deltas the caller needs to catch up,
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
//       a new channel (preserve v1 "first sync always gets some content"
//       invariant from fillDeltaPayload, see TestFillDeltaPayload_Unknown_
//       Channel_DoesNotTriggerTooLong).
//     - Otherwise: FetchAfter the next EventLimitPerChannel events; if
//       saturated → Kind=slice + NextCursor=max(event_seq); else
//       Kind=events.
//  4. Hydrate referenced message snapshots in one bulk fetch per channel
//     via GetByIDsForUser (visibility filter applied at SQL level — events
//     for messages the user can't see are returned without an entry in
//     Messages; client must tolerate the missing key as "filtered").
//  5. Compute unread the same way Sync v1 does (membership state derived).
//
// Membership-revoked channels silently drop out — they don't appear in
// GetMemberChannelEventSeqs. Per-channel fetch errors are non-fatal — the
// channel still appears with an empty Events slice (matching v1
// log-and-continue parity).
func (s *SyncService) SyncV2(ctx context.Context, callerID string, p SyncParamsV2) (SyncResultV2, error) {
	ctx, span := tracer.Start(ctx, "SyncService.SyncV2")
	defer span.End()

	if s.events == nil {
		return SyncResultV2{}, ErrSyncV2Unconfigured
	}

	serverEventSeqs, err := s.events.GetMemberChannelEventSeqs(ctx, callerID)
	if err != nil {
		return SyncResultV2{}, fmt.Errorf("get member channel_event seqs: %w", err)
	}

	clientEventSeqs := make(map[string]int64, len(p.Cursors))
	for _, c := range p.Cursors {
		clientEventSeqs[c.ID] = c.EventSeq
	}

	results := make([]SyncChannelDeltaV2, 0, len(serverEventSeqs))
	for chID, serverEventSeq := range serverEventSeqs {
		clientEventSeq, known := clientEventSeqs[chID]
		delta := SyncChannelDeltaV2{ID: chID, ServerEventSeq: serverEventSeq}

		// Unread is independent of the event log — same membership-state
		// formula as v1 so clients can keep their badge-rendering logic.
		// Member-fetch error → unread=0 (v1 parity).
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

		s.fillDeltaV2Payload(ctx, &delta, chID, callerID, clientEventSeq, known)
		results = append(results, delta)
	}

	return SyncResultV2{Channels: results}, nil
}

// fillDeltaV2Payload writes Events / Messages / Kind / NextCursor on delta
// according to the C019 §3.2 decision tree. Split out so SyncV2 stays
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
func (s *SyncService) fillDeltaV2Payload(
	ctx context.Context,
	delta *SyncChannelDeltaV2,
	chID, callerID string,
	clientEventSeq int64,
	known bool,
) {
	if known && clientEventSeq >= delta.ServerEventSeq {
		delta.Kind = &SyncEntryKindV2{Type: KindEmpty}
		return
	}

	gap := delta.ServerEventSeq - clientEventSeq
	if known && gap > EventTooLongThreshold {
		delta.Kind = &SyncEntryKindV2{Type: KindTooLong, ResetTo: delta.ServerEventSeq}
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
		delta.Kind = &SyncEntryKindV2{Type: KindEvents}
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
		delta.Kind = &SyncEntryKindV2{Type: KindSlice}
		next := events[len(events)-1].EventSeq
		delta.NextCursor = &next
	} else {
		delta.Kind = &SyncEntryKindV2{Type: KindEvents}
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

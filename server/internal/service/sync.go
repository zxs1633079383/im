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
)

// SyncChannelStore is the subset of repo.ChannelRepo SyncService needs.
// Defined consumer-side (Go's "accept small interfaces" idiom) so the service
// surface is documented at the call site.
type SyncChannelStore interface {
	GetMemberChannelSeqs(ctx context.Context, userID string) (map[string]int64, error)
	GetMember(ctx context.Context, channelID string, userID string) (*repo.ChannelMember, error)
}

// SyncMsgStore is the subset of repo.MessageRepo SyncService needs.
type SyncMsgStore interface {
	FetchForUser(ctx context.Context, channelID string, userID string, afterSeq int64, limit int) ([]repo.Message, error)
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

// SyncService implements the batch incremental-sync algorithm on top of
// SyncChannelStore + SyncMsgStore. The algorithm is preserved verbatim from
// the legacy handler.SyncHandler — see Sync below for the four-case decision.
type SyncService struct {
	channels SyncChannelStore
	messages SyncMsgStore
}

// NewSyncService wires the supplied stores. Both repos satisfy the small
// interfaces above — production passes repo.ChannelRepo / repo.MessageRepo
// directly.
func NewSyncService(channels SyncChannelStore, messages SyncMsgStore) *SyncService {
	return &SyncService{channels: channels, messages: messages}
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

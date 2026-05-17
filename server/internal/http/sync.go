package http

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"log/slog"

	"github.com/gin-gonic/gin"

	"im-server/internal/repo"
	"im-server/internal/service"
)

// syncRequest mirrors the legacy handler.SyncRequest body shape exactly so
// existing clients continue to work after the cut-over.
type syncRequest struct {
	Channels []syncChannelEntry `json:"channels"`
}

// syncChannelEntry is one channel cursor from the client.
//
// C012 P-D: channel ID migrates to TEXT (string); Seq remains int64
// (monotonic per-channel counter, not an entity ID).
//
// EventSeq is the v2 cursor field (C019 §3.1). It is decoded as a *int64
// so its presence-vs-absence is observable — sync v1 clients omit the
// field entirely, sync v2 clients always set it. This is the in-band
// version marker (see decideWireVersion).
type syncChannelEntry struct {
	ID       string `json:"id"`
	Seq      int64  `json:"seq"`
	EventSeq *int64 `json:"event_seq,omitempty"`
}

// syncEntryKind is the wire form of `service.SyncEntryKind` (v1 path) and
// `service.SyncEntryKindV2` (v2 path). Field tags match Rust client
// `types_v2::SyncEntryKind` (internally tagged enum):
//
//	v1: {"type":"empty"} / {"type":"full"} / {"type":"slice"}
//	v2: {"type":"empty"} / {"type":"events"} / {"type":"slice"}
//	both: {"type":"too_long","reset_to": <serverSeq>}
//
// Wire fields are identical; the union of v1 + v2 kinds is what differs.
type syncEntryKind struct {
	Type    string `json:"type"`
	ResetTo int64  `json:"reset_to,omitempty"`
}

// syncChannelResult — v0.7.3 P-7.5 adds Kind + NextCursor (omitempty so old
// clients see legacy shape). v1 wire only.
//
// Legacy clients infer from `messages` (Empty if empty, Full otherwise) and
// `has_more`; new Rust client reads `kind` directly and recurses on slice with
// `next_cursor`.
type syncChannelResult struct {
	ID         string         `json:"id"`
	ServerSeq  int64          `json:"server_seq"`
	Unread     int64          `json:"unread"`
	Messages   []repo.Message `json:"messages,omitempty"`
	HasMore    bool           `json:"has_more,omitempty"`
	Kind       *syncEntryKind `json:"kind,omitempty"`
	NextCursor *int64         `json:"next_cursor,omitempty"`
}

// syncResponse wraps the per-channel v1 deltas in {"channels": [...]} —
// same envelope as the legacy handler.SyncResponse.
type syncResponse struct {
	Channels []syncChannelResult `json:"channels"`
}

// syncChannelResultV2 is the v2 per-channel wire shape (C019 §3.1).
//
// ServerEventSeq replaces ServerSeq — names diverge intentionally so a
// careless client parser can't decode a v2 body with v1 types and
// silently drop both seq fields.
//
// Events carries the channel_event rows; Messages is the bulk-hydrated
// snapshot map keyed by message id (deduped — same id may be referenced
// by NEW + EDIT events).
type syncChannelResultV2 struct {
	ID             string                  `json:"id"`
	ServerEventSeq int64                   `json:"server_event_seq"`
	Unread         int64                   `json:"unread"`
	Events         []repo.ChannelEvent     `json:"events,omitempty"`
	Messages       map[string]repo.Message `json:"messages,omitempty"`
	Kind           *syncEntryKind          `json:"kind"`
	NextCursor     *int64                  `json:"next_cursor,omitempty"`
}

// syncResponseV2 wraps the per-channel v2 deltas. Same envelope shape as
// v1 — only the per-channel result type differs.
type syncResponseV2 struct {
	Channels []syncChannelResultV2 `json:"channels"`
}

// RegisterSyncRoutes wires POST /api/sync onto authed. authed must already
// have JWT middleware applied (see RegisterProfileRoutes for the contract).
//
// The handler dispatches v1 vs v2 by inspecting the request body for the
// `event_seq` field on any channel entry — if present, v2 (event_seq
// cursor algorithm); otherwise v1 (messages.seq cursor, legacy). Both
// paths share the same /api/sync endpoint per C019 §3.1 to keep client
// migration in-band (no separate /api/sync/v2 URL to rev).
//
// log is optional — pass nil to fall back to slog.Default(). Used for
// non-fatal 500 detail.
func RegisterSyncRoutes(authed *gin.RouterGroup, svc *service.SyncService, log *slog.Logger) {
	if log == nil {
		log = slog.Default()
	}

	authed.POST("/sync", func(c *gin.Context) {
		uid, ok := userIDFromCtx(c)
		if !ok {
			return
		}

		// Read the body once so we can both inspect it for version detection
		// and decode it normally. gin.ShouldBindJSON consumes the body, so we
		// can't bind first then peek — we have to peek first then unmarshal
		// from the saved bytes.
		body, err := io.ReadAll(c.Request.Body)
		if err != nil {
			c.JSON(400, gin.H{"error": "read body"})
			return
		}
		var in syncRequest
		if err := json.Unmarshal(body, &in); err != nil {
			c.JSON(400, gin.H{"error": "invalid JSON"})
			return
		}

		// Reject oversized batches; clients must split into multiple calls.
		// Contract locked, 对齐 docs/BACKEND.md §3.3.
		if len(in.Channels) > service.MaxChannelsPerCall {
			c.JSON(400, gin.H{"error": "too many channels"})
			return
		}

		if isV2 := decideWireVersion(in.Channels); isV2 {
			handleSyncV2(c, svc, uid, in, log)
			return
		}
		handleSyncV1(c, svc, uid, in, log)

		// body is unused after dispatch — silence unused linter without
		// having to drop the read (we keep it for future request-trailing
		// hooks like audit logging).
		_ = bytes.Equal(body, nil)
	})
}

// decideWireVersion returns true iff any channel entry carries the
// `event_seq` field — the in-band v2 marker (C019 §3.1). Empty Channels
// (e.g. an initial poll from a brand-new client) defaults to v1 to
// preserve legacy behaviour; v2 clients are expected to always send at
// least one cursor entry (even if EventSeq=0 for "I don't know this
// channel yet").
func decideWireVersion(entries []syncChannelEntry) bool {
	for _, e := range entries {
		if e.EventSeq != nil {
			return true
		}
	}
	return false
}

// handleSyncV1 keeps the legacy messages.seq cursor algorithm — preserved
// verbatim from the pre-P4 handler so legacy clients continue to work
// during the cut-over window.
func handleSyncV1(c *gin.Context, svc *service.SyncService, uid string, in syncRequest, log *slog.Logger) {
	cursors := make([]service.SyncCursor, 0, len(in.Channels))
	for _, ch := range in.Channels {
		cursors = append(cursors, service.SyncCursor{ID: ch.ID, Seq: ch.Seq})
	}

	result, err := svc.Sync(c.Request.Context(), uid, service.SyncParams{Cursors: cursors})
	if err != nil {
		log.Error("sync", "user_id", uid, "error", err)
		c.JSON(500, gin.H{"error": "internal error"})
		return
	}

	// Transcribe to the wire shape. Always emit a non-nil array so the
	// JSON envelope matches the legacy handler when there are no deltas.
	out := make([]syncChannelResult, 0, len(result.Channels))
	for _, d := range result.Channels {
		out = append(out, syncChannelResult{
			ID:         d.ID,
			ServerSeq:  d.ServerSeq,
			Unread:     d.Unread,
			Messages:   d.Messages,
			HasMore:    d.HasMore,
			Kind:       transcribeKind(d.Kind),
			NextCursor: d.NextCursor,
		})
	}
	c.JSON(200, syncResponse{Channels: out})
}

// handleSyncV2 routes to the v2 algorithm — channel_event.event_seq
// cursor + 4 kind branches (empty / events / slice / too_long).
//
// 501 is returned when the SyncService wasn't built with a ChannelEventRepo
// dep (NewSyncService instead of NewSyncServiceV2) — happens only in test
// rigs that don't stand up v2 infrastructure. Production wires v2 in
// gateway/main.go.
func handleSyncV2(c *gin.Context, svc *service.SyncService, uid string, in syncRequest, log *slog.Logger) {
	cursors := make([]service.SyncCursorV2, 0, len(in.Channels))
	for _, ch := range in.Channels {
		// EventSeq is guaranteed non-nil for v2 entries (decideWireVersion
		// returned true → at least one entry has it). Defensive deref so
		// mixed-version bodies (forbidden by contract but possible from a
		// buggy client) don't NPE — fall back to 0 = "no cursor".
		var es int64
		if ch.EventSeq != nil {
			es = *ch.EventSeq
		}
		cursors = append(cursors, service.SyncCursorV2{ID: ch.ID, EventSeq: es})
	}

	result, err := svc.SyncV2(c.Request.Context(), uid, service.SyncParamsV2{Cursors: cursors})
	if err != nil {
		if errors.Is(err, service.ErrSyncV2Unconfigured) {
			log.Warn("sync v2 unconfigured", "user_id", uid)
			c.JSON(501, gin.H{"error": "sync v2 not enabled on this server"})
			return
		}
		log.Error("sync v2", "user_id", uid, "error", err)
		c.JSON(500, gin.H{"error": "internal error"})
		return
	}

	out := make([]syncChannelResultV2, 0, len(result.Channels))
	for _, d := range result.Channels {
		out = append(out, syncChannelResultV2{
			ID:             d.ID,
			ServerEventSeq: d.ServerEventSeq,
			Unread:         d.Unread,
			Events:         d.Events,
			Messages:       d.Messages,
			Kind:           transcribeKindV2(d.Kind),
			NextCursor:     d.NextCursor,
		})
	}
	c.JSON(200, syncResponseV2{Channels: out})
}

// transcribeKind maps the v1 service-layer enum into the HTTP wire form.
// Returns nil (omitempty drops the field) when the service layer didn't
// tag the entry — preserves legacy wire shape so old clients keep working.
func transcribeKind(k *service.SyncEntryKind) *syncEntryKind {
	if k == nil {
		return nil
	}
	return &syncEntryKind{Type: k.Type, ResetTo: k.ResetTo}
}

// transcribeKindV2 maps the v2 service-layer enum into the HTTP wire form.
// Unlike v1 transcribeKind, v2 Kind is REQUIRED (every per-channel delta
// must carry one) — but we still tolerate nil to be defensive against
// service-layer bugs (caller's nil check at the JSON encoder will simply
// drop the field via omitempty, which a client would log as "malformed").
func transcribeKindV2(k *service.SyncEntryKindV2) *syncEntryKind {
	if k == nil {
		return nil
	}
	return &syncEntryKind{Type: string(k.Type), ResetTo: k.ResetTo}
}

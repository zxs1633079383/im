package http

import (
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
type syncChannelEntry struct {
	ID  string `json:"id"`
	Seq int64  `json:"seq"`
}

// syncEntryKind is the wire form of `service.SyncEntryKind`. Field tags match
// Rust client `types_v2::SyncEntryKind` (internally tagged enum):
//
//	{"type":"empty"} / {"type":"full"} / {"type":"slice"}
//	{"type":"too_long","reset_to": <serverSeq>}
type syncEntryKind struct {
	Type    string `json:"type"`
	ResetTo int64  `json:"reset_to,omitempty"`
}

// syncChannelResult — v0.7.3 P-7.5 adds Kind + NextCursor (omitempty so old
// clients see legacy shape).
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

// syncResponse wraps the per-channel deltas in {"channels": [...]} — same
// envelope as the legacy handler.SyncResponse.
type syncResponse struct {
	Channels []syncChannelResult `json:"channels"`
}

// RegisterSyncRoutes wires POST /api/sync onto authed. authed must already
// have JWT middleware applied (see RegisterProfileRoutes for the contract).
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

		var in syncRequest
		if err := c.ShouldBindJSON(&in); err != nil {
			c.JSON(400, gin.H{"error": "invalid JSON"})
			return
		}

		// Reject oversized batches; clients must split into multiple calls.
		// Contract locked, 对齐 docs/BACKEND.md §3.3.
		if len(in.Channels) > service.MaxChannelsPerCall {
			c.JSON(400, gin.H{"error": "too many channels"})
			return
		}

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
	})
}

// transcribeKind maps the service-layer enum into the HTTP wire form. Returns
// nil (omitempty drops the field) when the service layer didn't tag the entry —
// preserves legacy wire shape so old clients keep working.
func transcribeKind(k *service.SyncEntryKind) *syncEntryKind {
	if k == nil {
		return nil
	}
	return &syncEntryKind{Type: k.Type, ResetTo: k.ResetTo}
}

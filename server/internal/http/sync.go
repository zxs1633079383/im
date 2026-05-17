package http

import (
	"log/slog"

	"github.com/gin-gonic/gin"

	"im-server/internal/repo"
	"im-server/internal/service"
)

// syncRequest is the /api/sync request body shape (C019 §3.1).
//
// 历史背景：v1 messages.seq cursor + 双轨 dispatch 已于 2026-05-17 cutover
// 整族下线。本项目唯一 client = cses-client，已同步迁移到 event_seq。
type syncRequest struct {
	Channels []syncChannelEntry `json:"channels"`
}

// syncChannelEntry is one channel cursor from the client.
//
// C012 P-D: channel ID is TEXT (string); EventSeq is the
// channel_event.event_seq cursor (C019 §3.1) — strictly monotonic per
// channel, advanced by every server-emitted event.
type syncChannelEntry struct {
	ID       string `json:"id"`
	EventSeq int64  `json:"event_seq"`
}

// syncEntryKind mirrors `service.SyncEntryKind` on the wire:
//
//	{"type":"empty"} / {"type":"events"} / {"type":"slice"}
//	{"type":"too_long","reset_to": <serverEventSeq>}
type syncEntryKind struct {
	Type    string `json:"type"`
	ResetTo int64  `json:"reset_to,omitempty"`
}

// syncChannelResult is the per-channel wire shape (C019 §3.1).
//
// ServerEventSeq is the server-side channel_event high-water mark.
// Events carries the channel_event rows; Messages is the bulk-hydrated
// snapshot map keyed by message id (deduped — same id may be referenced
// by NEW + EDIT events). Kind is always non-nil (required by C019 §3.1);
// NextCursor is non-nil only when Kind.type=="slice".
type syncChannelResult struct {
	ID             string                  `json:"id"`
	ServerEventSeq int64                   `json:"server_event_seq"`
	Unread         int64                   `json:"unread"`
	Events         []repo.ChannelEvent     `json:"events,omitempty"`
	Messages       map[string]repo.Message `json:"messages,omitempty"`
	Kind           *syncEntryKind          `json:"kind"`
	NextCursor     *int64                  `json:"next_cursor,omitempty"`
}

// syncResponse wraps the per-channel deltas in {"channels": [...]}.
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
			cursors = append(cursors, service.SyncCursor{ID: ch.ID, EventSeq: ch.EventSeq})
		}

		result, err := svc.Sync(c.Request.Context(), uid, service.SyncParams{Cursors: cursors})
		if err != nil {
			log.Error("sync", "user_id", uid, "error", err)
			c.JSON(500, gin.H{"error": "internal error"})
			return
		}

		out := make([]syncChannelResult, 0, len(result.Channels))
		for _, d := range result.Channels {
			out = append(out, syncChannelResult{
				ID:             d.ID,
				ServerEventSeq: d.ServerEventSeq,
				Unread:         d.Unread,
				Events:         d.Events,
				Messages:       d.Messages,
				Kind:           transcribeKind(d.Kind),
				NextCursor:     d.NextCursor,
			})
		}
		c.JSON(200, syncResponse{Channels: out})
	})
}

// transcribeKind maps the service-layer enum into the HTTP wire form.
// Kind is required by C019 §3.1 but we still tolerate nil defensively
// against service-layer bugs — nil here drops the field via omitempty,
// which a client would log as "malformed".
func transcribeKind(k *service.SyncEntryKind) *syncEntryKind {
	if k == nil {
		return nil
	}
	return &syncEntryKind{Type: string(k.Type), ResetTo: k.ResetTo}
}

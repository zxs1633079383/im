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
type syncChannelEntry struct {
	ID  int64 `json:"id"`
	Seq int64 `json:"seq"`
}

// syncChannelResult mirrors the legacy handler.SyncChannelResult exactly. The
// `omitempty` tags on Messages + HasMore preserve the legacy wire format
// (no-change channels never appear; small/large/new gap shape unchanged).
type syncChannelResult struct {
	ID        int64          `json:"id"`
	ServerSeq int64          `json:"server_seq"`
	Unread    int64          `json:"unread"`
	Messages  []repo.Message `json:"messages,omitempty"`
	HasMore   bool           `json:"has_more,omitempty"`
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
				ID:        d.ID,
				ServerSeq: d.ServerSeq,
				Unread:    d.Unread,
				Messages:  d.Messages,
				HasMore:   d.HasMore,
			})
		}
		c.JSON(200, syncResponse{Channels: out})
	})
}

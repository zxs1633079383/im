package http

import (
	"errors"
	"log/slog"
	"strings"

	"github.com/gin-gonic/gin"

	"im-server/internal/repo"
	"im-server/internal/service"
)

// readStatsResp wraps the slice in a {"stats": [...]} envelope. Top-level
// arrays would also work but envelope-style keeps room for future metadata
// (e.g. server-side cache hints) without breaking clients.
type readStatsResp struct {
	Stats []repo.ReadStat `json:"stats"`
}

// registerReadStatsRoute wires GET /api/messages/read-stats?ids=1,2,3.
// See decision 1c in docs/CSES_CLIENT_CUTOVER.md: this batch endpoint
// replaces the cses-client per-message readBits bitmap by computing read /
// unread on demand from channel_members.last_read_seq. The single batched
// call is what makes the new UI tractable — message-status renders one
// component per visible message, and we don't want N round-trips.
func registerReadStatsRoute(
	authed *gin.RouterGroup,
	svc *service.MessageService,
	opts MessageRouteOpts,
) {
	logger := opts.Logger
	if logger == nil {
		logger = slog.Default()
	}
	authed.GET("/messages/read-stats", func(c *gin.Context) {
		uid, ok := userIDFromCtx(c)
		if !ok {
			return
		}
		idsParam := strings.TrimSpace(c.Query("ids"))
		if idsParam == "" {
			c.JSON(400, gin.H{"error": "ids is required"})
			return
		}
		msgIDs := parseStringCSV(idsParam)
		if len(msgIDs) == 0 {
			c.JSON(400, gin.H{"error": "ids is required"})
			return
		}

		stats, err := svc.GetReadStatsBatch(c.Request.Context(), uid, msgIDs)
		switch {
		case errors.Is(err, service.ErrTooManyReadStats):
			c.JSON(400, gin.H{"error": "too many ids; max 100 per request"})
			return
		case err != nil:
			logger.Error("get read stats batch",
				"error", err, "user_id", uid, "ids_len", len(msgIDs))
			c.JSON(500, gin.H{"error": "internal error"})
			return
		}
		if stats == nil {
			stats = []repo.ReadStat{}
		}
		c.JSON(200, readStatsResp{Stats: stats})
	})
}

// parseStringCSV parses "a,b,c" into a []string. Empty entries produced by a
// trailing comma are silently skipped; whitespace is trimmed.
//
// C012 P-D: replaces parseInt64CSV; entity IDs are now TEXT, so no numeric
// validation — the storage layer rejects unknown IDs with NotFound.
func parseStringCSV(s string) []string {
	parts := strings.Split(s, ",")
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		p = strings.TrimSpace(p)
		if p == "" {
			continue
		}
		out = append(out, p)
	}
	return out
}

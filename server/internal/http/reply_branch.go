package http

import (
	"errors"
	"log/slog"
	"strconv"

	"github.com/gin-gonic/gin"

	"im-server/internal/repo"
	"im-server/internal/service"
)

// RegisterReplyBranchRoute wires GET /api/messages/:id/replies/branch — the
// cses-client `reply-root-message.component.ts` second-level pagination
// surface (v0.7.3 gap #2). Replaces mattermost csesapi `/posts/getReplyBranch`.
//
// Query: ?offset=N&limit=M (defaults 0 / 50, capped 200).
// Returns a slice of replies ordered by seq ASC, the same shape as the
// existing GET /messages/:id/replies endpoint plus a `has_more` flag the
// component uses to decide whether to fire the next page.
func RegisterReplyBranchRoute(
	authed *gin.RouterGroup,
	svc *service.MessageService,
	log *slog.Logger,
) {
	if log == nil {
		log = slog.Default()
	}
	authed.GET("/messages/:id/replies/branch",
		func(c *gin.Context) { replyBranchEndpoint(c, svc, log) })
}

func replyBranchEndpoint(c *gin.Context, svc *service.MessageService, log *slog.Logger) {
	uid, ok := userIDFromCtx(c)
	if !ok {
		return
	}
	rootID, ok := pathInt64(c, "id")
	if !ok {
		return
	}
	limit := parseLimit(c.Query("limit"))
	offset := parseOffset(c.Query("offset"))
	msgs, hasMore, err := svc.GetRepliesBranch(c.Request.Context(), rootID, uid, offset, limit)
	switch {
	case errors.Is(err, repo.ErrNotFound):
		c.JSON(404, gin.H{"error": "message not found"})
	case errors.Is(err, service.ErrNotMember):
		c.JSON(403, gin.H{"error": "not a member of this channel"})
	case err != nil:
		log.Error("reply branch", "error", err, "root_id", rootID, "user_id", uid)
		c.JSON(500, gin.H{"error": "internal error"})
	default:
		if msgs == nil {
			msgs = []repo.Message{}
		}
		c.JSON(200, gin.H{
			"messages": msgs,
			"has_more": hasMore,
			"offset":   offset,
			"limit":    limit,
		})
	}
}

// parseOffset reads ?offset=N from the query string, clamped to [0, MaxInt32]
// with a default of 0. Mirrors parseLimit so reply-branch + future paginated
// endpoints share the same dialect.
func parseOffset(s string) int {
	if s == "" {
		return 0
	}
	v, err := strconv.Atoi(s)
	if err != nil || v < 0 {
		return 0
	}
	return v
}

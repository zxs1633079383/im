package http

import (
	"errors"
	"log/slog"

	"github.com/gin-gonic/gin"

	"im-server/internal/repo"
	"im-server/internal/service"
)

// registerTemplateReceivedRoute wires POST /api/messages/:id/received.
//
// Decision 6: instead of inventing a new WS event type (which would break the
// V1+M2 = 16 event lock from docs/GOAL.md §4 #1), we reuse msg_updated to
// broadcast the new receipt list. The cses-client side already has a handler
// chain for msg_updated (Tauri Rust → CoreEvent POST_UPDATE → message-v3
// stores) so the receipts UI updates with no client changes beyond stripping
// the optimistic local update.
//
// Idempotency: a re-click by the same user returns 200 with the unchanged
// message and skips the broadcast (svc.MarkTemplateReceived returns changed=
// false). This matches the cses-client UI which disables the button after
// the first click but still tolerates a stray request from a stale tab.
func registerTemplateReceivedRoute(
	authed *gin.RouterGroup,
	svc *service.MessageService,
	opts MessageRouteOpts,
) {
	logger := opts.Logger
	if logger == nil {
		logger = slog.Default()
	}
	authed.POST("/messages/:id/received", func(c *gin.Context) {
		uid, ok := userIDFromCtx(c)
		if !ok {
			return
		}
		msgID, ok := pathInt64(c, "id")
		if !ok {
			return
		}

		msg, changed, err := svc.MarkTemplateReceived(c.Request.Context(), msgID, uid)
		switch {
		case errors.Is(err, repo.ErrNotFound):
			c.JSON(404, gin.H{"error": "message not found"})
			return
		case errors.Is(err, repo.ErrGone):
			c.JSON(410, gin.H{"error": "message already deleted"})
			return
		case errors.Is(err, repo.ErrInvalidTemplate):
			c.JSON(422, gin.H{"error": "not a template message"})
			return
		case errors.Is(err, service.ErrNotMember):
			c.JSON(403, gin.H{"error": "not a member of this channel"})
			return
		case err != nil:
			logger.Error("mark template received",
				"error", err, "msg_id", msgID, "user_id", uid)
			c.JSON(500, gin.H{"error": "internal error"})
			return
		}

		if changed && opts.Broadcaster != nil {
			opts.Broadcaster.BroadcastToMembers(msg.ChannelID, EventMsgUpdated, msg)
		}
		c.JSON(200, msg)
	})
}

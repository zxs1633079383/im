package http

import (
	"errors"

	"github.com/gin-gonic/gin"

	"im-server/internal/repo"
	"im-server/internal/service"
)

// Urgent-related WS event type — plain string so this package stays decoupled
// from the gateway package.
const (
	EventUrgentPosted MessageEventType = "urgent_posted"
)

// sendUrgentReq is POST /api/messages/urgent body.
type sendUrgentReq struct {
	ChannelID   int64  `json:"channel_id"`
	Content     string `json:"content"`
	ClientMsgID string `json:"client_msg_id"`
}

// RegisterUrgentRoutes wires the 3 urgent endpoints.
// broadcaster may be nil; when non-nil, SendUrgent fans an urgent_posted
// event to channel members.
func RegisterUrgentRoutes(
	authed *gin.RouterGroup,
	svc *service.UrgentService,
	broadcaster MessageEventBroadcaster,
) {
	// POST /api/messages/urgent — send an urgent message.
	authed.POST("/messages/urgent", func(c *gin.Context) {
		uid, ok := userIDFromCtx(c)
		if !ok {
			return
		}
		var in sendUrgentReq
		if err := c.ShouldBindJSON(&in); err != nil {
			c.JSON(400, gin.H{"error": "invalid JSON"})
			return
		}
		if in.ChannelID == 0 {
			c.JSON(422, gin.H{"error": "channel_id is required"})
			return
		}
		msg, err := svc.SendUrgent(c.Request.Context(), in.ChannelID, uid, in.Content, in.ClientMsgID)
		switch {
		case errors.Is(err, service.ErrUrgentContentEmpty):
			c.JSON(422, gin.H{"error": "content is required"})
		case errors.Is(err, service.ErrNotMember):
			c.JSON(403, gin.H{"error": "not a member of this channel"})
		case err != nil:
			c.JSON(500, gin.H{"error": "internal error"})
		default:
			if broadcaster != nil {
				broadcaster.BroadcastToMembers(msg.ChannelID, EventUrgentPosted, msg)
			}
			c.JSON(201, msg)
		}
	})

	// POST /api/messages/:id/urgent/confirm — recipient confirms.
	authed.POST("/messages/:id/urgent/confirm", func(c *gin.Context) {
		uid, ok := userIDFromCtx(c)
		if !ok {
			return
		}
		msgID, ok := pathInt64(c, "id")
		if !ok {
			return
		}
		err := svc.ConfirmUrgent(c.Request.Context(), msgID, uid)
		switch {
		case errors.Is(err, repo.ErrNotFound):
			c.JSON(404, gin.H{"error": "message not found"})
		case errors.Is(err, service.ErrNotUrgentMsg):
			c.JSON(422, gin.H{"error": "message is not urgent"})
		case errors.Is(err, service.ErrNotMember):
			c.JSON(403, gin.H{"error": "not a member of this channel"})
		case err != nil:
			c.JSON(500, gin.H{"error": "internal error"})
		default:
			c.JSON(200, gin.H{"status": "confirmed"})
		}
	})

	// POST /api/messages/:id/urgent/cancel — sender or manager cancels urgent flag.
	authed.POST("/messages/:id/urgent/cancel", func(c *gin.Context) {
		uid, ok := userIDFromCtx(c)
		if !ok {
			return
		}
		msgID, ok := pathInt64(c, "id")
		if !ok {
			return
		}
		err := svc.CancelUrgent(c.Request.Context(), msgID, uid)
		switch {
		case errors.Is(err, repo.ErrNotFound):
			c.JSON(404, gin.H{"error": "message not found"})
		case errors.Is(err, service.ErrNotMember):
			c.JSON(403, gin.H{"error": "not a member of this channel"})
		case errors.Is(err, service.ErrNotSender):
			c.JSON(403, gin.H{"error": "only sender or manager can cancel"})
		case err != nil:
			c.JSON(500, gin.H{"error": "internal error"})
		default:
			c.JSON(200, gin.H{"status": "cancelled"})
		}
	})

	// GET /api/messages/:id/urgent/confirmations — list users who confirmed.
	// (Out-of-spec convenience but necessary for the multi-user confirm test;
	// keep it behind a member-level check.)
	authed.GET("/messages/:id/urgent/confirmations", func(c *gin.Context) {
		uid, ok := userIDFromCtx(c)
		if !ok {
			return
		}
		msgID, ok := pathInt64(c, "id")
		if !ok {
			return
		}
		ids, err := svc.ListConfirmations(c.Request.Context(), msgID, uid)
		switch {
		case errors.Is(err, repo.ErrNotFound):
			c.JSON(404, gin.H{"error": "message not found"})
		case errors.Is(err, service.ErrNotMember):
			c.JSON(403, gin.H{"error": "not a member of this channel"})
		case err != nil:
			c.JSON(500, gin.H{"error": "internal error"})
		default:
			if ids == nil {
				ids = []string{}
			}
			c.JSON(200, gin.H{"confirmations": ids})
		}
	})
}

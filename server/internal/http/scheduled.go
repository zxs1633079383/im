package http

import (
	"errors"
	"time"

	"github.com/gin-gonic/gin"

	"im-server/internal/repo"
	"im-server/internal/service"
)

// createScheduledReq is POST /api/messages/scheduled body. scheduled_at is
// carried as RFC3339 so clients don't need to pick a number format.
type createScheduledReq struct {
	ChannelID   int64     `json:"channel_id"`
	Content     string    `json:"content"`
	MsgType     int16     `json:"msg_type"`
	VisibleTo   []string  `json:"visible_to"`
	ReplyTo     *int64    `json:"reply_to"`
	FileIDs     []int64   `json:"file_ids"`
	ScheduledAt time.Time `json:"scheduled_at"`
}

// RegisterScheduledRoutes wires the 3 scheduled-message endpoints.
func RegisterScheduledRoutes(
	authed *gin.RouterGroup,
	svc *service.ScheduledService,
) {
	// POST /api/messages/scheduled — enqueue a deferred send.
	authed.POST("/messages/scheduled", func(c *gin.Context) {
		uid, ok := userIDFromCtx(c)
		if !ok {
			return
		}
		var in createScheduledReq
		if err := c.ShouldBindJSON(&in); err != nil {
			c.JSON(400, gin.H{"error": "invalid JSON"})
			return
		}
		if in.ChannelID == 0 {
			c.JSON(422, gin.H{"error": "channel_id is required"})
			return
		}
		sm, err := svc.Create(c.Request.Context(), service.ScheduledCreateParams{
			ChannelID:   in.ChannelID,
			SenderID:    uid,
			Content:     in.Content,
			MsgType:     in.MsgType,
			VisibleTo:   in.VisibleTo,
			ReplyTo:     in.ReplyTo,
			FileIDs:     in.FileIDs,
			ScheduledAt: in.ScheduledAt,
		})
		switch {
		case errors.Is(err, service.ErrScheduledContentEmpty):
			c.JSON(422, gin.H{"error": "content is required"})
		case errors.Is(err, service.ErrScheduledTimeInPast):
			c.JSON(422, gin.H{"error": "scheduled_at must be at least 60s in the future"})
		case errors.Is(err, service.ErrNotMember):
			c.JSON(403, gin.H{"error": "not a member of this channel"})
		case err != nil:
			c.JSON(500, gin.H{"error": "internal error"})
		default:
			c.JSON(201, sm)
		}
	})

	// DELETE /api/messages/scheduled/:id — cancel pending.
	authed.DELETE("/messages/scheduled/:id", func(c *gin.Context) {
		uid, ok := userIDFromCtx(c)
		if !ok {
			return
		}
		id, ok := pathInt64(c, "id")
		if !ok {
			return
		}
		err := svc.Cancel(c.Request.Context(), id, uid)
		switch {
		case errors.Is(err, repo.ErrNotFound):
			c.JSON(404, gin.H{"error": "scheduled message not found"})
		case errors.Is(err, service.ErrScheduledNotSender):
			c.JSON(403, gin.H{"error": "only the sender may cancel"})
		case errors.Is(err, service.ErrScheduledNotPending):
			c.JSON(409, gin.H{"error": "scheduled message is not pending"})
		case err != nil:
			c.JSON(500, gin.H{"error": "internal error"})
		default:
			c.JSON(200, gin.H{"status": "cancelled"})
		}
	})

	// GET /api/messages/scheduled?status=pending|all — list the caller's queue.
	authed.GET("/messages/scheduled", func(c *gin.Context) {
		uid, ok := userIDFromCtx(c)
		if !ok {
			return
		}
		statusFilter := int16(-1)
		switch c.Query("status") {
		case "", "all":
			statusFilter = -1
		case "pending":
			statusFilter = repo.ScheduledStatusPending
		case "delivered":
			statusFilter = repo.ScheduledStatusDelivered
		case "cancelled":
			statusFilter = repo.ScheduledStatusCancelled
		case "failed":
			statusFilter = repo.ScheduledStatusFailed
		}
		limit := queryIntDefault(c, "limit", 50)
		cursor := int64(queryIntDefault(c, "cursor", 0))
		ls, err := svc.List(c.Request.Context(), uid, statusFilter, limit, cursor)
		if err != nil {
			c.JSON(500, gin.H{"error": "internal error"})
			return
		}
		if ls == nil {
			ls = []repo.ScheduledMessage{}
		}
		c.JSON(200, gin.H{"scheduled": ls})
	})
}

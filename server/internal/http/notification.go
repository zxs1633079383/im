package http

import (
	"encoding/json"
	"errors"

	"github.com/gin-gonic/gin"

	"im-server/internal/repo"
	"im-server/internal/service"
)

// Notification-related WS event type. Plain string keeps this package
// decoupled from the gateway package.
const (
	EventNotificationReceived MessageEventType = "notification_received"
)

// sendNotificationReq is POST /api/notifications body.
type sendNotificationReq struct {
	ReceiverID string           `json:"receiver_id"`
	Title      string           `json:"title"`
	Body       string           `json:"body"`
	Type       int16            `json:"type"`
	Props      *json.RawMessage `json:"props,omitempty"`
}

// RegisterNotificationRoutes wires the 4 notification endpoints. pusher may be
// nil (tests / offline). When non-nil, Send fires a notification_received event
// to the receiver.
func RegisterNotificationRoutes(
	authed *gin.RouterGroup,
	svc *service.NotificationService,
	pusher UserEventPusher,
) {
	// POST /api/notifications — send a notification.
	authed.POST("/notifications", func(c *gin.Context) {
		uid, ok := userIDFromCtx(c)
		if !ok {
			return
		}
		var in sendNotificationReq
		if err := c.ShouldBindJSON(&in); err != nil {
			c.JSON(400, gin.H{"error": "invalid JSON"})
			return
		}
		props := ""
		if in.Props != nil {
			props = string(*in.Props)
		}
		n, err := svc.Send(c.Request.Context(), service.NotificationSendParams{
			SenderID:   uid,
			ReceiverID: in.ReceiverID,
			Title:      in.Title,
			Body:       in.Body,
			Type:       in.Type,
			Props:      props,
		})
		switch {
		case errors.Is(err, service.ErrNotificationTitleEmpty):
			c.JSON(422, gin.H{"error": "title is required"})
		case errors.Is(err, service.ErrNotificationBadReceiver):
			c.JSON(422, gin.H{"error": "receiver_id is required or not found"})
		case err != nil:
			c.JSON(500, gin.H{"error": "internal error"})
		default:
			if pusher != nil {
				pusher.PushToUser(n.ReceiverID, EventNotificationReceived, n)
			}
			c.JSON(201, n)
		}
	})

	// GET /api/notifications/received — my inbox.
	authed.GET("/notifications/received", func(c *gin.Context) {
		uid, ok := userIDFromCtx(c)
		if !ok {
			return
		}
		unreadOnly := c.Query("unread_only") == "true"
		limit := queryIntDefault(c, "limit", 50)
		// C012 P-D: cursor is now a string ID (empty = page 0).
		cursor := c.Query("cursor")
		ls, err := svc.ListReceived(c.Request.Context(), uid, unreadOnly, limit, cursor)
		if err != nil {
			c.JSON(500, gin.H{"error": "internal error"})
			return
		}
		if ls == nil {
			ls = []repo.Notification{}
		}
		c.JSON(200, gin.H{"notifications": ls})
	})

	// GET /api/notifications/sent — my outbox.
	authed.GET("/notifications/sent", func(c *gin.Context) {
		uid, ok := userIDFromCtx(c)
		if !ok {
			return
		}
		limit := queryIntDefault(c, "limit", 50)
		// C012 P-D: cursor is now a string ID (empty = page 0).
		cursor := c.Query("cursor")
		ls, err := svc.ListSent(c.Request.Context(), uid, limit, cursor)
		if err != nil {
			c.JSON(500, gin.H{"error": "internal error"})
			return
		}
		if ls == nil {
			ls = []repo.Notification{}
		}
		c.JSON(200, gin.H{"notifications": ls})
	})

	// POST /api/notifications/:id/read — mark as read.
	authed.POST("/notifications/:id/read", func(c *gin.Context) {
		uid, ok := userIDFromCtx(c)
		if !ok {
			return
		}
		id, ok := pathInt64(c, "id")
		if !ok {
			return
		}
		err := svc.MarkRead(c.Request.Context(), id, uid)
		switch {
		case errors.Is(err, repo.ErrNotFound):
			c.JSON(404, gin.H{"error": "notification not found"})
		case err != nil:
			c.JSON(500, gin.H{"error": "internal error"})
		default:
			c.JSON(200, gin.H{"status": "read"})
		}
	})
}

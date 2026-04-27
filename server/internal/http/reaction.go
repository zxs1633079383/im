package http

import (
	"errors"

	"github.com/gin-gonic/gin"

	"im-server/internal/repo"
	"im-server/internal/service"
)

// ReactionEventPusher fans reaction_added / reaction_removed events out
// over the channel's WS connections. Same shape as MessageEventBroadcaster
// but kept distinct so the gateway hub adapter can route these to the
// correct WS frame type without coupling the HTTP layer to gateway types.
type ReactionEventPusher interface {
	BroadcastReaction(channelID int64, eventType ReactionEventType, payload any)
}

// ReactionEventType is the wire-level event name. Defined here so the
// http layer stays independent of gateway/types.go.
type ReactionEventType string

const (
	EventReactionAdded   ReactionEventType = "reaction_added"
	EventReactionRemoved ReactionEventType = "reaction_removed"
)

type reactionAddReq struct {
	Emoji string `json:"emoji"`
}

// reactionPayload is the WS body for both add / remove events. Matches the
// repo.MessageReaction shape clients already know.
type reactionPayload struct {
	ChannelID int64  `json:"channel_id"`
	MessageID int64  `json:"message_id"`
	UserID    string `json:"user_id"`
	Emoji     string `json:"emoji"`
}

// RegisterReactionRoutes wires the three endpoints onto authed:
//
//   - POST   /api/messages/:id/reactions       — add (idempotent)
//   - DELETE /api/messages/:id/reactions/:emoji — remove (404 when missing)
//   - GET    /api/messages/:id/reactions       — list (oldest first)
//
// pusher may be nil — when nil the WS broadcast is skipped (tests / minimal
// deployments). The /api group must already have cookie + CookieRequired
// middleware on it.
func RegisterReactionRoutes(authed *gin.RouterGroup, svc *service.ReactionService, pusher ReactionEventPusher) {
	authed.POST("/messages/:id/reactions", func(c *gin.Context) {
		uid, ok := userIDFromCtx(c)
		if !ok {
			return
		}
		messageID, ok := pathInt64(c, "id")
		if !ok {
			return
		}
		var in reactionAddReq
		if err := c.ShouldBindJSON(&in); err != nil {
			c.JSON(400, gin.H{"error": "invalid JSON"})
			return
		}
		if in.Emoji == "" {
			c.JSON(422, gin.H{"error": "emoji is required"})
			return
		}
		channelID, err := svc.Add(c.Request.Context(), messageID, uid, in.Emoji)
		switch {
		case errors.Is(err, service.ErrSourceNotFound):
			c.JSON(404, gin.H{"error": "message not found"})
		case errors.Is(err, service.ErrSourceNotMember):
			c.JSON(403, gin.H{"error": "not a member of the message's channel"})
		case err != nil:
			c.JSON(422, gin.H{"error": err.Error()})
		default:
			if pusher != nil {
				pusher.BroadcastReaction(channelID, EventReactionAdded, reactionPayload{
					ChannelID: channelID, MessageID: messageID, UserID: uid, Emoji: in.Emoji,
				})
			}
			c.JSON(201, gin.H{"status": "ok"})
		}
	})

	authed.DELETE("/messages/:id/reactions/:emoji", func(c *gin.Context) {
		uid, ok := userIDFromCtx(c)
		if !ok {
			return
		}
		messageID, ok := pathInt64(c, "id")
		if !ok {
			return
		}
		emoji := c.Param("emoji")
		if emoji == "" {
			c.JSON(422, gin.H{"error": "emoji is required"})
			return
		}
		channelID, err := svc.Remove(c.Request.Context(), messageID, uid, emoji)
		switch {
		case errors.Is(err, service.ErrSourceNotFound):
			c.JSON(404, gin.H{"error": "message not found"})
		case errors.Is(err, service.ErrSourceNotMember):
			c.JSON(403, gin.H{"error": "not a member of the message's channel"})
		case errors.Is(err, repo.ErrNotFound):
			c.JSON(404, gin.H{"error": "reaction not found"})
		case err != nil:
			c.JSON(500, gin.H{"error": "internal error"})
		default:
			if pusher != nil {
				pusher.BroadcastReaction(channelID, EventReactionRemoved, reactionPayload{
					ChannelID: channelID, MessageID: messageID, UserID: uid, Emoji: emoji,
				})
			}
			c.JSON(200, gin.H{"status": "ok"})
		}
	})

	authed.GET("/messages/:id/reactions", func(c *gin.Context) {
		uid, ok := userIDFromCtx(c)
		if !ok {
			return
		}
		messageID, ok := pathInt64(c, "id")
		if !ok {
			return
		}
		list, err := svc.List(c.Request.Context(), messageID, uid)
		switch {
		case errors.Is(err, service.ErrSourceNotFound):
			c.JSON(404, gin.H{"error": "message not found"})
		case errors.Is(err, service.ErrSourceNotMember):
			c.JSON(403, gin.H{"error": "not a member of the message's channel"})
		case err != nil:
			c.JSON(500, gin.H{"error": "internal error"})
		default:
			if list == nil {
				list = []repo.MessageReaction{}
			}
			c.JSON(200, list)
		}
	})
}

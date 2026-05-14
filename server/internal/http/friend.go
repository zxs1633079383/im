package http

import (
	"errors"

	"github.com/gin-gonic/gin"

	"im-server/internal/repo"
	"im-server/internal/service"
)

// FriendEventPusher pushes friend events (request/accept/reject) to online
// users via the gateway hub. M4: ids are mm UserIDs (24-hex strings).
type FriendEventPusher interface {
	PushFriendEvent(targetUserID, eventType, fromUserID string)
}

type sendFriendRequestReq struct {
	AddresseeID string `json:"addressee_id"`
}

type friendshipIDReq struct {
	FriendshipID string `json:"friendship_id"`
}

type blockReq struct {
	UserID string `json:"user_id"`
}

// RegisterFriendRoutes wires the seven friend + user-search endpoints.
//
// M4: user search has been retired (no local users table); the endpoint now
// returns an empty list. Clients should query cses-side directories.
func RegisterFriendRoutes(authed *gin.RouterGroup, svc *service.FriendService, pusher FriendEventPusher) {
	authed.POST("/friends/request", func(c *gin.Context) {
		uid, ok := userIDFromCtx(c)
		if !ok {
			return
		}
		var in sendFriendRequestReq
		if err := c.ShouldBindJSON(&in); err != nil {
			c.JSON(400, gin.H{"error": "invalid JSON"})
			return
		}
		if in.AddresseeID == "" {
			c.JSON(422, gin.H{"error": "addressee_id is required"})
			return
		}
		err := svc.SendRequest(c.Request.Context(), uid, in.AddresseeID)
		switch {
		case errors.Is(err, service.ErrAlreadyExists):
			c.JSON(409, gin.H{"error": "friend request already exists"})
		case err != nil:
			c.JSON(500, gin.H{"error": "internal error"})
		default:
			if pusher != nil {
				pusher.PushFriendEvent(in.AddresseeID, "request", uid)
			}
			c.JSON(201, gin.H{"status": "pending"})
		}
	})

	authed.POST("/friends/accept", func(c *gin.Context) {
		uid, ok := userIDFromCtx(c)
		if !ok {
			return
		}
		var in friendshipIDReq
		if err := c.ShouldBindJSON(&in); err != nil {
			c.JSON(400, gin.H{"error": "invalid JSON"})
			return
		}
		if in.FriendshipID == "" {
			c.JSON(422, gin.H{"error": "friendship_id is required"})
			return
		}
		requesterID, err := svc.AcceptRequest(c.Request.Context(), in.FriendshipID, uid)
		switch {
		case errors.Is(err, repo.ErrNotFound):
			c.JSON(404, gin.H{"error": "pending request not found"})
		case err != nil:
			c.JSON(500, gin.H{"error": "internal error"})
		default:
			if pusher != nil {
				pusher.PushFriendEvent(requesterID, "accepted", uid)
			}
			c.JSON(200, gin.H{"status": "accepted"})
		}
	})

	authed.POST("/friends/reject", func(c *gin.Context) {
		uid, ok := userIDFromCtx(c)
		if !ok {
			return
		}
		var in friendshipIDReq
		if err := c.ShouldBindJSON(&in); err != nil {
			c.JSON(400, gin.H{"error": "invalid JSON"})
			return
		}
		if in.FriendshipID == "" {
			c.JSON(422, gin.H{"error": "friendship_id is required"})
			return
		}
		requesterID, err := svc.RejectRequest(c.Request.Context(), in.FriendshipID, uid)
		switch {
		case errors.Is(err, repo.ErrNotFound):
			c.JSON(404, gin.H{"error": "pending request not found"})
		case err != nil:
			c.JSON(500, gin.H{"error": "internal error"})
		default:
			if pusher != nil {
				pusher.PushFriendEvent(requesterID, "rejected", uid)
			}
			c.JSON(200, gin.H{"status": "rejected"})
		}
	})

	authed.GET("/friends", func(c *gin.Context) {
		uid, ok := userIDFromCtx(c)
		if !ok {
			return
		}
		friends, err := svc.ListFriends(c.Request.Context(), uid)
		if err != nil {
			c.JSON(500, gin.H{"error": "internal error"})
			return
		}
		if friends == nil {
			friends = []string{}
		}
		c.JSON(200, friends)
	})

	authed.GET("/friends/pending", func(c *gin.Context) {
		uid, ok := userIDFromCtx(c)
		if !ok {
			return
		}
		pending, err := svc.ListPending(c.Request.Context(), uid)
		if err != nil {
			c.JSON(500, gin.H{"error": "internal error"})
			return
		}
		if pending == nil {
			pending = []repo.PendingRequest{}
		}
		c.JSON(200, pending)
	})

	authed.POST("/friends/block", func(c *gin.Context) {
		uid, ok := userIDFromCtx(c)
		if !ok {
			return
		}
		var in blockReq
		if err := c.ShouldBindJSON(&in); err != nil {
			c.JSON(400, gin.H{"error": "invalid JSON"})
			return
		}
		if in.UserID == "" {
			c.JSON(422, gin.H{"error": "user_id is required"})
			return
		}
		if err := svc.BlockUser(c.Request.Context(), uid, in.UserID); err != nil {
			c.JSON(500, gin.H{"error": "internal error"})
			return
		}
		c.JSON(200, gin.H{"status": "blocked"})
	})

	// M4: User search retired — cses owns the user directory. Returns an
	// empty list with the same wire shape so v0.5.x clients keep working.
	authed.GET("/users/search", func(c *gin.Context) {
		_, ok := userIDFromCtx(c)
		if !ok {
			return
		}
		c.JSON(200, []string{})
	})
}

package http

import (
	"errors"

	"github.com/gin-gonic/gin"

	"im-server/internal/repo"
	"im-server/internal/service"
)

// FriendEventPusher pushes friend events (request/accept/reject) to online
// users via the gateway hub. Mirrors the legacy handler.FriendEventPusher
// contract: nil = no real-time notifications (the integration / unit tests
// don't need a live hub).
type FriendEventPusher interface {
	PushFriendEvent(targetUserID int64, eventType string, fromUserID int64)
}

// Request bodies. Field names + JSON tags match the legacy handler exactly so
// existing clients continue to work after the cut-over.
type sendFriendRequestReq struct {
	AddresseeID int64 `json:"addressee_id"`
}

type friendshipIDReq struct {
	FriendshipID int64 `json:"friendship_id"`
}

type blockReq struct {
	UserID int64 `json:"user_id"`
}

// RegisterFriendRoutes wires the seven friend + user-search endpoints onto
// authed. authed must already have JWT middleware applied (see
// RegisterProfileRoutes for the contract).
//
// pusher is optional; pass nil to disable the real-time WebSocket push that
// notifies the addressee on a new friend request. The legacy handler exposed
// the same WithEventPusher hook — preserving it keeps gateway/main.go's
// hubFriendEventPusher wiring unchanged.
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
		if in.AddresseeID == 0 {
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
		if in.FriendshipID == 0 {
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
			// Notify the original requester that their invite has been
			// accepted — target is the requester, from_user is the accepter.
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
		if in.FriendshipID == 0 {
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
			// Mirror accept: target is the requester, from_user is the
			// rejecter. Clients use this to drop the outgoing request from
			// the user's pending list in real time.
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
			friends = []repo.User{}
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
		if in.UserID == 0 {
			c.JSON(422, gin.H{"error": "user_id is required"})
			return
		}
		if err := svc.BlockUser(c.Request.Context(), uid, in.UserID); err != nil {
			c.JSON(500, gin.H{"error": "internal error"})
			return
		}
		c.JSON(200, gin.H{"status": "blocked"})
	})

	authed.GET("/users/search", func(c *gin.Context) {
		uid, ok := userIDFromCtx(c)
		if !ok {
			return
		}
		q := c.Query("q")
		users, err := svc.SearchUsers(c.Request.Context(), q, uid)
		if err != nil {
			c.JSON(500, gin.H{"error": "internal error"})
			return
		}
		if users == nil {
			users = []repo.User{}
		}
		c.JSON(200, users)
	})
}

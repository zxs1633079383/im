package http

import (
	"errors"
	"log/slog"

	"github.com/gin-gonic/gin"

	"im-server/internal/repo"
	"im-server/internal/service"
)

// RegisterFavoriteRoutes wires the favorite add/remove/list endpoints onto
// authed. authed must already have JWT middleware applied (see
// RegisterProfileRoutes for the contract).
//
//	POST   /api/favorites/:message_id   — add a favorite (idempotent)
//	DELETE /api/favorites/:message_id   — remove a favorite (404 if absent)
//	GET    /api/favorites               — list favorites, newest first
//
// log is optional — pass nil to fall back to slog.Default(). Used for
// non-fatal 500 detail.
//
// Note on DELETE semantics: the legacy handler returned 500 for any error
// (including "not favorited"). We tighten that to 404 here — DELETE on a
// missing resource is correctly a 404 in REST terms, and the service now
// surfaces repo.ErrNotFound explicitly so the transport can distinguish.
func RegisterFavoriteRoutes(authed *gin.RouterGroup, svc *service.FavoriteService) {
	log := slog.Default()

	authed.POST("/favorites/:message_id", func(c *gin.Context) {
		uid, ok := userIDFromCtx(c)
		if !ok {
			return
		}

		messageID, ok := parsePathID(c, "message_id")
		if !ok {
			c.JSON(400, gin.H{"error": "invalid message_id"})
			return
		}

		if err := svc.Add(c.Request.Context(), uid, messageID); err != nil {
			log.Error("add favorite", "user_id", uid, "message_id", messageID, "error", err)
			c.JSON(500, gin.H{"error": "internal error"})
			return
		}
		c.JSON(201, gin.H{"status": "ok"})
	})

	authed.DELETE("/favorites/:message_id", func(c *gin.Context) {
		uid, ok := userIDFromCtx(c)
		if !ok {
			return
		}

		messageID, ok := parsePathID(c, "message_id")
		if !ok {
			c.JSON(400, gin.H{"error": "invalid message_id"})
			return
		}

		err := svc.Remove(c.Request.Context(), uid, messageID)
		switch {
		case errors.Is(err, repo.ErrNotFound):
			c.JSON(404, gin.H{"error": "favorite not found"})
		case err != nil:
			log.Error("remove favorite", "user_id", uid, "message_id", messageID, "error", err)
			c.JSON(500, gin.H{"error": "internal error"})
		default:
			c.Status(204)
		}
	})

	authed.GET("/favorites", func(c *gin.Context) {
		uid, ok := userIDFromCtx(c)
		if !ok {
			return
		}

		favs, err := svc.List(c.Request.Context(), uid)
		if err != nil {
			log.Error("list favorites", "user_id", uid, "error", err)
			c.JSON(500, gin.H{"error": "internal error"})
			return
		}
		c.JSON(200, gin.H{"favorites": favs})
	})
}

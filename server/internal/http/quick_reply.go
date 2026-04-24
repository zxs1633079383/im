package http

import (
	"errors"

	"github.com/gin-gonic/gin"

	"im-server/internal/repo"
	"im-server/internal/service"
)

// createQuickReplyReq is POST /api/quick-replies body.
type createQuickReplyReq struct {
	Label     string `json:"label"`
	Content   string `json:"content"`
	SortOrder int    `json:"sort_order"`
}

// patchQuickReplyReq is PATCH /api/quick-replies/:id body. Pointer fields let
// the caller omit whatever they don't want to change.
type patchQuickReplyReq struct {
	Label     *string `json:"label,omitempty"`
	Content   *string `json:"content,omitempty"`
	SortOrder *int    `json:"sort_order,omitempty"`
}

// RegisterQuickReplyRoutes wires the 4 endpoints.
func RegisterQuickReplyRoutes(
	authed *gin.RouterGroup,
	svc *service.QuickReplyService,
) {
	// POST /api/quick-replies — create.
	authed.POST("/quick-replies", func(c *gin.Context) {
		uid, ok := userIDFromCtx(c)
		if !ok {
			return
		}
		var in createQuickReplyReq
		if err := c.ShouldBindJSON(&in); err != nil {
			c.JSON(400, gin.H{"error": "invalid JSON"})
			return
		}
		q, err := svc.Create(c.Request.Context(), service.CreateQuickReplyParams{
			UserID:    uid,
			Label:     in.Label,
			Content:   in.Content,
			SortOrder: in.SortOrder,
		})
		switch {
		case errors.Is(err, service.ErrQuickReplyLabelEmpty):
			c.JSON(422, gin.H{"error": "label is required"})
		case errors.Is(err, service.ErrQuickReplyContentEmpty):
			c.JSON(422, gin.H{"error": "content is required"})
		case err != nil:
			c.JSON(500, gin.H{"error": "internal error"})
		default:
			c.JSON(201, q)
		}
	})

	// GET /api/quick-replies — list the caller's presets.
	authed.GET("/quick-replies", func(c *gin.Context) {
		uid, ok := userIDFromCtx(c)
		if !ok {
			return
		}
		ls, err := svc.List(c.Request.Context(), uid)
		if err != nil {
			c.JSON(500, gin.H{"error": "internal error"})
			return
		}
		if ls == nil {
			ls = []repo.QuickReply{}
		}
		c.JSON(200, gin.H{"quick_replies": ls})
	})

	// PATCH /api/quick-replies/:id — update.
	authed.PATCH("/quick-replies/:id", func(c *gin.Context) {
		uid, ok := userIDFromCtx(c)
		if !ok {
			return
		}
		id, ok := pathInt64(c, "id")
		if !ok {
			return
		}
		var in patchQuickReplyReq
		if err := c.ShouldBindJSON(&in); err != nil {
			c.JSON(400, gin.H{"error": "invalid JSON"})
			return
		}
		q, err := svc.Update(c.Request.Context(), id, uid, repo.QuickReplyPatch{
			Label:     in.Label,
			Content:   in.Content,
			SortOrder: in.SortOrder,
		})
		switch {
		case errors.Is(err, repo.ErrNotFound):
			c.JSON(404, gin.H{"error": "quick reply not found"})
		case errors.Is(err, service.ErrQuickReplyNotOwner):
			c.JSON(403, gin.H{"error": "not your quick reply"})
		case err != nil:
			c.JSON(500, gin.H{"error": "internal error"})
		default:
			c.JSON(200, q)
		}
	})

	// DELETE /api/quick-replies/:id.
	authed.DELETE("/quick-replies/:id", func(c *gin.Context) {
		uid, ok := userIDFromCtx(c)
		if !ok {
			return
		}
		id, ok := pathInt64(c, "id")
		if !ok {
			return
		}
		err := svc.Delete(c.Request.Context(), id, uid)
		switch {
		case errors.Is(err, repo.ErrNotFound):
			c.JSON(404, gin.H{"error": "quick reply not found"})
		case errors.Is(err, service.ErrQuickReplyNotOwner):
			c.JSON(403, gin.H{"error": "not your quick reply"})
		case err != nil:
			c.JSON(500, gin.H{"error": "internal error"})
		default:
			c.JSON(200, gin.H{"status": "deleted"})
		}
	})
}

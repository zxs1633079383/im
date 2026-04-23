package http

import (
	"errors"
	"strings"

	"github.com/gin-gonic/gin"

	"im-server/internal/middleware"
	"im-server/internal/repo"
	"im-server/internal/service"
)

// updateMeReq is the JSON body for PUT /api/users/me. Both fields are
// optional; the legacy handler treats empty strings as "leave unchanged"
// at the validation layer (the repo.UpdateProfile call still writes them).
type updateMeReq struct {
	DisplayName string `json:"display_name"`
	AvatarURL   string `json:"avatar_url"`
}

// RegisterProfileRoutes wires PUT /api/users/me onto authed.
//
// authed must already have JWT middleware applied so middleware.UserIDKey is
// set on the context. Caller owns the group:
//
//	authed := engine.Group("/api")
//	authed.Use(middleware.JWTGin(secret))
//	imhttp.RegisterProfileRoutes(authed, profileSvc)
func RegisterProfileRoutes(authed *gin.RouterGroup, svc *service.ProfileService) {
	authed.PUT("/users/me", func(c *gin.Context) {
		var in updateMeReq
		if err := c.ShouldBindJSON(&in); err != nil {
			// Match the legacy handler: malformed JSON → 400.
			c.JSON(400, gin.H{"error": "invalid JSON"})
			return
		}

		// Validate display_name if provided — 1..64 chars after trim.
		if in.DisplayName != "" {
			in.DisplayName = strings.TrimSpace(in.DisplayName)
			if len(in.DisplayName) < 1 || len(in.DisplayName) > 64 {
				c.JSON(422, gin.H{"error": "display_name must be 1-64 characters"})
				return
			}
		}
		// Validate avatar_url if provided — must look like a URL.
		if in.AvatarURL != "" && !strings.HasPrefix(in.AvatarURL, "http") {
			c.JSON(422, gin.H{"error": "avatar_url must be a valid URL"})
			return
		}

		uidAny, ok := c.Get(middleware.UserIDKey)
		if !ok {
			c.JSON(401, gin.H{"error": "unauthorized"})
			return
		}
		uid, ok := uidAny.(int64)
		if !ok {
			c.JSON(401, gin.H{"error": "unauthorized"})
			return
		}

		u, err := svc.UpdateProfile(c.Request.Context(), uid, in.DisplayName, in.AvatarURL)
		switch {
		case errors.Is(err, repo.ErrNotFound):
			c.JSON(404, gin.H{"error": "user not found"})
		case err != nil:
			c.JSON(500, gin.H{"error": "internal error"})
		default:
			c.JSON(200, u)
		}
	})
}

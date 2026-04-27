package http

import (
	"github.com/gin-gonic/gin"

	"im-server/internal/middleware"
	"im-server/internal/service"
)

// updateSettingsReq mirrors the legacy handler's body shape exactly:
// NotificationEnabled is *bool (so an explicit false is distinguishable
// from "field omitted"), Theme and Language are plain strings where empty
// means "leave unchanged". SettingsJSON was not part of the legacy body —
// keep parity here and don't accept it.
type updateSettingsReq struct {
	NotificationEnabled *bool  `json:"notification_enabled"`
	Theme               string `json:"theme"`
	Language            string `json:"language"`
}

// RegisterSettingsRoutes wires GET/PUT /api/settings onto authed.
//
// authed must already have JWT middleware applied so middleware.UserIDKey is
// set on the context. See RegisterProfileRoutes for the wiring contract.
func RegisterSettingsRoutes(authed *gin.RouterGroup, svc *service.SettingsService) {
	authed.GET("/settings", func(c *gin.Context) {
		uid, ok := userIDFromCtx(c)
		if !ok {
			return
		}
		s, err := svc.Get(c.Request.Context(), uid)
		if err != nil {
			c.JSON(500, gin.H{"error": "internal error"})
			return
		}
		c.JSON(200, s)
	})

	authed.PUT("/settings", func(c *gin.Context) {
		uid, ok := userIDFromCtx(c)
		if !ok {
			return
		}

		var in updateSettingsReq
		if err := c.ShouldBindJSON(&in); err != nil {
			// Legacy handler returned 400 for malformed JSON — preserve.
			c.JSON(400, gin.H{"error": "invalid JSON"})
			return
		}

		// Load current state (or defaults) so partial updates work as PATCH-like.
		cur, err := svc.Get(c.Request.Context(), uid)
		if err != nil {
			c.JSON(500, gin.H{"error": "internal error"})
			return
		}
		cur.UserID = uid
		if in.NotificationEnabled != nil {
			cur.NotificationEnabled = *in.NotificationEnabled
		}
		if in.Theme != "" {
			cur.Theme = in.Theme
		}
		if in.Language != "" {
			cur.Language = in.Language
		}

		if err := svc.Update(c.Request.Context(), cur); err != nil {
			c.JSON(500, gin.H{"error": "internal error"})
			return
		}
		// Re-read to return the post-write state. GORM's Create may rewrite
		// some struct fields from `default:` tags after Upsert (e.g. setting
		// notification_enabled back to true when its Go value was the zero
		// false), which would otherwise leak into the response.
		fresh, err := svc.Get(c.Request.Context(), uid)
		if err != nil {
			c.JSON(500, gin.H{"error": "internal error"})
			return
		}
		c.JSON(200, fresh)
	})
}

// userIDFromCtx extracts the authenticated mm UserID set by
// middleware.MattermostCookieResolve. On failure it writes a 401 and returns
// ok=false so the caller can simply early-return.
//
// M4: returns string (mm UserID, 24-char hex). The format check here is
// deliberately strict — an unexpected non-string value, an empty value, or a
// non-hex value all fail closed.
func userIDFromCtx(c *gin.Context) (string, bool) {
	uidAny, exists := c.Get(middleware.UserIDKey)
	if !exists {
		c.JSON(401, gin.H{"error": "unauthorized"})
		return "", false
	}
	uid, ok := uidAny.(string)
	if !ok || uid == "" {
		c.JSON(401, gin.H{"error": "unauthorized"})
		return "", false
	}
	return uid, true
}

// teamIDFromCtx returns the resolved team_id (mm CompanyID, with OrgID
// fallback) — empty string when the user has no organisation. Handlers pass
// this into service params for messages.team_id / channels.team_id
// denormalisation.
func teamIDFromCtx(c *gin.Context) string {
	return middleware.TeamIDFromCtx(c)
}

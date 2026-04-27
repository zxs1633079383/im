package http

import (
	"github.com/gin-gonic/gin"

	"im-server/internal/middleware"
)

// gone is the canonical 410 body for retired auth endpoints. Keep the wire
// shape minimal so cses-client error handlers don't need new code paths.
var gone = gin.H{"error": "register/login retired in M4; cookie auth only"}

// RegisterAuthRoutes wires the M4 cookie-only auth surface:
//
//   - POST /api/auth/register → 410 Gone (im no longer mints accounts)
//   - POST /api/auth/login    → 410 Gone (cses owns identity)
//   - GET  /api/auth/me       → returns the resolved MattermostUser injected
//                                by MattermostCookieResolve; 401 when the
//                                cookieId is missing/invalid.
//
// authedExtra typically carries MattermostCookieResolve so the /me handler
// finds the user on the context. CookieRequired is appended after so the
// 401 path is shared.
func RegisterAuthRoutes(r *gin.Engine, authedExtra ...gin.HandlerFunc) {
	pub := r.Group("/api/auth")
	pub.POST("/register", func(c *gin.Context) { c.JSON(410, gone) })
	pub.POST("/login", func(c *gin.Context) { c.JSON(410, gone) })

	authed := r.Group("/api/auth")
	for _, mw := range authedExtra {
		authed.Use(mw)
	}
	authed.Use(middleware.CookieRequired())
	authed.GET("/me", func(c *gin.Context) {
		mm := middleware.MMUserFromCtx(c)
		if mm == nil {
			// CookieRequired upstream guarantees this is unreachable in
			// practice, but be defensive — the alternative is a typed nil
			// JSON body which clients would mis-handle.
			c.JSON(401, gin.H{"error": "unauthorized"})
			return
		}
		c.JSON(200, mm)
	})
}

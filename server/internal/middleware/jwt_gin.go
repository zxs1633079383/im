package middleware

import (
	"strings"

	"github.com/gin-gonic/gin"
	"im-server/internal/auth"
)

// Context keys set by JWTGin upon successful token validation.
const (
	UserIDKey   = "user_id"
	UsernameKey = "username"
)

// JWTGin parses and validates a Bearer JWT against secret. On success,
// sets UserIDKey and UsernameKey on the gin.Context. On failure, aborts
// with 401 and a JSON error body. Use as: r.Use(JWTGin(cfg.JWTSecret)).
func JWTGin(secret string) gin.HandlerFunc {
	return func(c *gin.Context) {
		header := c.GetHeader("Authorization")
		token := strings.TrimPrefix(header, "Bearer ")
		if token == "" || token == header {
			c.AbortWithStatusJSON(401, gin.H{"error": "missing or malformed Authorization header"})
			return
		}

		claims, err := auth.ValidateToken(secret, token)
		if err != nil {
			c.AbortWithStatusJSON(401, gin.H{"error": "invalid token"})
			return
		}

		c.Set(UserIDKey, claims.UserID)
		c.Set(UsernameKey, claims.Username)
		c.Next()
	}
}

// JWTOrCookie is the dual-stack auth gate. It accepts the request when
// EITHER:
//   - a previous middleware (typically MattermostCookieAuth) already set
//     UserIDKey on the context, OR
//   - the Authorization header carries a valid Bearer JWT.
//
// Use it INSTEAD of JWTGin when MattermostCookieAuth runs first:
//
//	authedAPI.Use(MattermostCookieAuth(rdb, users, log))
//	authedAPI.Use(JWTOrCookie(cfg.JWTSecret))
//
// Cookie-only callers pass cleanly (no Bearer JWT required); JWT-only
// callers behave exactly as before; both-present callers get JWT identity
// (cookie identity already on the context is overwritten by the JWT
// claims for explicitness — JWT is the higher-trust source).
func JWTOrCookie(secret string) gin.HandlerFunc {
	return func(c *gin.Context) {
		header := c.GetHeader("Authorization")
		token := strings.TrimPrefix(header, "Bearer ")
		hasBearer := token != "" && token != header

		if hasBearer {
			claims, err := auth.ValidateToken(secret, token)
			if err != nil {
				c.AbortWithStatusJSON(401, gin.H{"error": "invalid token"})
				return
			}
			c.Set(UserIDKey, claims.UserID)
			c.Set(UsernameKey, claims.Username)
			c.Next()
			return
		}
		if _, ok := c.Get(UserIDKey); ok {
			c.Next()
			return
		}
		c.AbortWithStatusJSON(401, gin.H{"error": "missing auth: provide Bearer JWT or cookieId header"})
	}
}

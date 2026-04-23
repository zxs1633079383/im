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

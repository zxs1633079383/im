package middleware

import "github.com/gin-gonic/gin"

// CookieRequired is the M4 auth gate. It accepts the request iff a previous
// MattermostCookieResolve has populated UserIDKey on the context. Otherwise
// it aborts with 401.
//
// Use it in place of JWTOrCookie:
//
//	authedAPI.Use(MattermostCookieResolve(rdb, log))
//	authedAPI.Use(CookieRequired())
func CookieRequired() gin.HandlerFunc {
	return func(c *gin.Context) {
		v, ok := c.Get(UserIDKey)
		if !ok {
			c.AbortWithStatusJSON(401, gin.H{"error": "missing auth: cookieId header required"})
			return
		}
		uid, ok := v.(string)
		if !ok || uid == "" {
			c.AbortWithStatusJSON(401, gin.H{"error": "missing auth: cookieId header required"})
			return
		}
		c.Next()
	}
}

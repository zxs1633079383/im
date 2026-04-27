package middleware

import (
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"

	"im-server/internal/auth"
)

const testJWTSecret = "unit-test-secret-32-chars-minimum-aaaaa"

// TestJWTOrCookie_AcceptsBearerOnly — classic JWT path; cookie middleware
// not in chain, header carries a valid Bearer token.
func TestJWTOrCookie_AcceptsBearerOnly(t *testing.T) {
	gin.SetMode(gin.TestMode)
	tok, err := auth.GenerateToken(testJWTSecret, 42, "alice")
	require.NoError(t, err)

	r := gin.New()
	r.Use(JWTOrCookie(testJWTSecret))
	var seenUID int64
	r.GET("/x", func(c *gin.Context) {
		seenUID = c.GetInt64(UserIDKey)
		c.Status(204)
	})

	req := httptest.NewRequest("GET", "/x", nil)
	req.Header.Set("Authorization", "Bearer "+tok)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	require.Equal(t, 204, w.Code)
	require.Equal(t, int64(42), seenUID)
}

// TestJWTOrCookie_AcceptsCookieOnly — cookie middleware ran first and put
// UserIDKey on the context; no Bearer header. The gate must let it through.
func TestJWTOrCookie_AcceptsCookieOnly(t *testing.T) {
	gin.SetMode(gin.TestMode)
	r := gin.New()
	// Simulate MattermostCookieAuth's outcome.
	r.Use(func(c *gin.Context) {
		c.Set(UserIDKey, int64(99))
		c.Set(UsernameKey, "from-cookie")
		c.Next()
	})
	r.Use(JWTOrCookie(testJWTSecret))

	var seenUID int64
	var seenName string
	r.GET("/x", func(c *gin.Context) {
		seenUID = c.GetInt64(UserIDKey)
		seenName = c.GetString(UsernameKey)
		c.Status(204)
	})

	req := httptest.NewRequest("GET", "/x", nil) // no Authorization
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	require.Equal(t, 204, w.Code)
	require.Equal(t, int64(99), seenUID)
	require.Equal(t, "from-cookie", seenName)
}

// TestJWTOrCookie_RejectsUnauthenticated — neither cookie identity nor JWT.
// Must 401, not pass.
func TestJWTOrCookie_RejectsUnauthenticated(t *testing.T) {
	gin.SetMode(gin.TestMode)
	r := gin.New()
	r.Use(JWTOrCookie(testJWTSecret))
	r.GET("/x", func(c *gin.Context) { c.Status(204) })

	req := httptest.NewRequest("GET", "/x", nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	require.Equal(t, 401, w.Code)
}

// TestJWTOrCookie_BothPresentJWTWins — JWT takes precedence (higher trust):
// the cookie-set values get overwritten by the JWT claims.
func TestJWTOrCookie_BothPresentJWTWins(t *testing.T) {
	gin.SetMode(gin.TestMode)
	tok, err := auth.GenerateToken(testJWTSecret, 7, "from-jwt")
	require.NoError(t, err)

	r := gin.New()
	r.Use(func(c *gin.Context) {
		c.Set(UserIDKey, int64(99)) // cookie identity
		c.Set(UsernameKey, "from-cookie")
		c.Next()
	})
	r.Use(JWTOrCookie(testJWTSecret))

	var seenUID int64
	var seenName string
	r.GET("/x", func(c *gin.Context) {
		seenUID = c.GetInt64(UserIDKey)
		seenName = c.GetString(UsernameKey)
		c.Status(204)
	})

	req := httptest.NewRequest("GET", "/x", nil)
	req.Header.Set("Authorization", "Bearer "+tok)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	require.Equal(t, 204, w.Code)
	require.Equal(t, int64(7), seenUID, "JWT identity must override cookie identity")
	require.Equal(t, "from-jwt", seenName)
}

// TestJWTOrCookie_BadBearerRejected — invalid JWT must 401 even when no
// cookie identity is on the context (don't fall through to "missing auth"
// branch silently — the explicit invalid-token case is more useful).
func TestJWTOrCookie_BadBearerRejected(t *testing.T) {
	gin.SetMode(gin.TestMode)
	r := gin.New()
	r.Use(JWTOrCookie(testJWTSecret))
	r.GET("/x", func(c *gin.Context) { c.Status(204) })

	req := httptest.NewRequest("GET", "/x", nil)
	req.Header.Set("Authorization", "Bearer not-a-valid-token")
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	require.Equal(t, 401, w.Code)
}

package middleware

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
	"im-server/internal/auth"
)

func TestJWTGin_NoToken_401(t *testing.T) {
	gin.SetMode(gin.TestMode)
	r := gin.New()
	r.Use(JWTGin("secret"))
	r.GET("/x", func(c *gin.Context) { c.Status(200) })

	req := httptest.NewRequest("GET", "/x", nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	require.Equal(t, http.StatusUnauthorized, w.Code)
}

func TestJWTGin_BadToken_401(t *testing.T) {
	gin.SetMode(gin.TestMode)
	r := gin.New()
	r.Use(JWTGin("secret"))
	r.GET("/x", func(c *gin.Context) { c.Status(200) })

	req := httptest.NewRequest("GET", "/x", nil)
	req.Header.Set("Authorization", "Bearer not-a-real-token")
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	require.Equal(t, http.StatusUnauthorized, w.Code)
}

func TestJWTGin_ValidToken_SetsUserID(t *testing.T) {
	gin.SetMode(gin.TestMode)
	tok, err := auth.GenerateToken("secret", 42, "alice")
	require.NoError(t, err)

	r := gin.New()
	r.Use(JWTGin("secret"))
	r.GET("/x", func(c *gin.Context) {
		uid, _ := c.Get(UserIDKey)
		uname, _ := c.Get(UsernameKey)
		c.JSON(200, gin.H{"uid": uid, "uname": uname})
	})

	req := httptest.NewRequest("GET", "/x", nil)
	req.Header.Set("Authorization", "Bearer "+tok)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	require.Equal(t, http.StatusOK, w.Code)
	require.Contains(t, w.Body.String(), `"uid":42`)
	require.Contains(t, w.Body.String(), `"uname":"alice"`)
}

func TestJWTGin_WrongSecret_401(t *testing.T) {
	gin.SetMode(gin.TestMode)
	tok, _ := auth.GenerateToken("good", 1, "u")
	r := gin.New()
	r.Use(JWTGin("wrong"))
	r.GET("/x", func(c *gin.Context) { c.Status(200) })

	req := httptest.NewRequest("GET", "/x", nil)
	req.Header.Set("Authorization", "Bearer "+tok)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	require.Equal(t, http.StatusUnauthorized, w.Code)
}

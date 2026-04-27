package http_test

import (
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"

	imhttp "im-server/internal/http"
	"im-server/internal/middleware"
)

// TestRegisterAuthRoutes_RegisterIs410 — POST /api/auth/register must always
// return 410 in M4. Cookie middleware is irrelevant for this path; bare 410
// is the wire contract retired clients see.
func TestRegisterAuthRoutes_RegisterIs410(t *testing.T) {
	gin.SetMode(gin.TestMode)
	engine := gin.New()
	imhttp.RegisterAuthRoutes(engine)

	req := httptest.NewRequest("POST", "/api/auth/register", nil)
	w := httptest.NewRecorder()
	engine.ServeHTTP(w, req)
	require.Equal(t, 410, w.Code)
}

// TestRegisterAuthRoutes_LoginIs410 — POST /api/auth/login mirrors register.
func TestRegisterAuthRoutes_LoginIs410(t *testing.T) {
	gin.SetMode(gin.TestMode)
	engine := gin.New()
	imhttp.RegisterAuthRoutes(engine)

	req := httptest.NewRequest("POST", "/api/auth/login", nil)
	w := httptest.NewRecorder()
	engine.ServeHTTP(w, req)
	require.Equal(t, 410, w.Code)
}

// TestRegisterAuthRoutes_MeRequiresCookie — GET /api/auth/me without a
// cookieId / resolved MattermostUser → 401 (CookieRequired upstream).
func TestRegisterAuthRoutes_MeRequiresCookie(t *testing.T) {
	gin.SetMode(gin.TestMode)
	engine := gin.New()
	imhttp.RegisterAuthRoutes(engine)

	req := httptest.NewRequest("GET", "/api/auth/me", nil)
	w := httptest.NewRecorder()
	engine.ServeHTTP(w, req)
	require.Equal(t, 401, w.Code)
}

// TestRegisterAuthRoutes_MeReturnsMMUser — when an upstream middleware sets
// the resolved Mattermost user on the context, /me echoes the JSON shape.
func TestRegisterAuthRoutes_MeReturnsMMUser(t *testing.T) {
	gin.SetMode(gin.TestMode)
	engine := gin.New()
	// Inject a synthetic resolver so CookieRequired sees a UserIDKey.
	imhttp.RegisterAuthRoutes(engine, func(c *gin.Context) {
		c.Set("im_mm_user", &middleware.MattermostUser{
			ID:        "676cc4ccfbbc501161d5cd65",
			UserID:    "676cc4ccfbbc501161d5cd65",
			Name:      "张立超",
			CompanyID: "6111fb0a202d425d221c53db",
		})
		c.Set(middleware.UserIDKey, "676cc4ccfbbc501161d5cd65")
		c.Next()
	})

	req := httptest.NewRequest("GET", "/api/auth/me", nil)
	w := httptest.NewRecorder()
	engine.ServeHTTP(w, req)
	require.Equal(t, 200, w.Code)
	require.Contains(t, w.Body.String(), "676cc4ccfbbc501161d5cd65")
	require.Contains(t, w.Body.String(), "张立超")
}

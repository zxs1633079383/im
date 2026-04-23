package http_test

import (
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"

	"im-server/internal/auth"
	imhttp "im-server/internal/http"
	"im-server/internal/middleware"
	"im-server/internal/repo"
	"im-server/internal/repo/mocks"
	"im-server/internal/service"
	"im-server/internal/testutil"
)

func setupProfileHandler(t *testing.T) (*gin.Engine, *mocks.UserRepoMock) {
	t.Helper()
	gin.SetMode(gin.TestMode)
	m := mocks.NewUserRepoMock(t)
	svc := service.NewProfileService(m)
	r := gin.New()
	authed := r.Group("/api")
	authed.Use(middleware.JWTGin(testSecret))
	imhttp.RegisterProfileRoutes(authed, svc)
	return r, m
}

func newProfileToken(t *testing.T, uid int64, username string) string {
	t.Helper()
	tok, err := auth.GenerateToken(testSecret, uid, username)
	require.NoError(t, err)
	return tok
}

func TestProfileHandler_UpdateMe_NoToken_401(t *testing.T) {
	r, _ := setupProfileHandler(t)
	testutil.NewExpect(t, r).PUT("/api/users/me").
		WithJSON(map[string]string{"display_name": "x"}).
		Expect().Status(401)
}

func TestProfileHandler_UpdateMe_OK(t *testing.T) {
	r, m := setupProfileHandler(t)
	tok := newProfileToken(t, 1, "alice")

	m.EXPECT().UpdateProfile(mock.Anything, int64(1), "Alice Updated", "").
		Return(&repo.User{ID: 1, Username: "alice", DisplayName: "Alice Updated"}, nil)

	testutil.NewExpect(t, r).PUT("/api/users/me").
		WithHeader("Authorization", "Bearer "+tok).
		WithJSON(map[string]string{"display_name": "Alice Updated"}).
		Expect().Status(200).JSON().Object().
		Value("display_name").IsEqual("Alice Updated")
}

func TestProfileHandler_UpdateMe_422_DisplayNameTooLong(t *testing.T) {
	r, _ := setupProfileHandler(t)
	tok := newProfileToken(t, 1, "alice")

	long := strings.Repeat("a", 65)
	testutil.NewExpect(t, r).PUT("/api/users/me").
		WithHeader("Authorization", "Bearer "+tok).
		WithJSON(map[string]string{"display_name": long}).
		Expect().Status(422)
}

func TestProfileHandler_UpdateMe_422_BadAvatarURL(t *testing.T) {
	r, _ := setupProfileHandler(t)
	tok := newProfileToken(t, 1, "alice")

	testutil.NewExpect(t, r).PUT("/api/users/me").
		WithHeader("Authorization", "Bearer "+tok).
		WithJSON(map[string]string{"avatar_url": "not-a-url"}).
		Expect().Status(422)
}

func TestProfileHandler_UpdateMe_404_UserGone(t *testing.T) {
	r, m := setupProfileHandler(t)
	tok := newProfileToken(t, 99, "ghost")

	m.EXPECT().UpdateProfile(mock.Anything, int64(99), "x", "").
		Return(nil, repo.ErrNotFound)

	testutil.NewExpect(t, r).PUT("/api/users/me").
		WithHeader("Authorization", "Bearer "+tok).
		WithJSON(map[string]string{"display_name": "x"}).
		Expect().Status(404)
}

func TestProfileHandler_UpdateMe_400_BadJSON(t *testing.T) {
	r, _ := setupProfileHandler(t)
	tok := newProfileToken(t, 1, "alice")

	testutil.NewExpect(t, r).PUT("/api/users/me").
		WithHeader("Authorization", "Bearer "+tok).
		WithHeader("Content-Type", "application/json").
		WithText("{not json").
		Expect().Status(400)
}

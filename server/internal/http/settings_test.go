package http_test

import (
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

func setupSettingsHandler(t *testing.T) (*gin.Engine, *mocks.UserSettingsRepoMock) {
	t.Helper()
	gin.SetMode(gin.TestMode)
	m := mocks.NewUserSettingsRepoMock(t)
	svc := service.NewSettingsService(m)
	r := gin.New()
	authed := r.Group("/api")
	authed.Use(middleware.JWTGin(testSecret))
	imhttp.RegisterSettingsRoutes(authed, svc)
	return r, m
}

func newSettingsToken(t *testing.T, uid int64, username string) string {
	t.Helper()
	tok, err := auth.GenerateToken(testSecret, uid, username)
	require.NoError(t, err)
	return tok
}

func TestSettingsHandler_Get_NoToken_401(t *testing.T) {
	r, _ := setupSettingsHandler(t)
	testutil.NewExpect(t, r).GET("/api/settings").Expect().Status(401)
}

func TestSettingsHandler_Get_ReturnsDefaults(t *testing.T) {
	r, m := setupSettingsHandler(t)
	tok := newSettingsToken(t, 1, "alice")

	m.EXPECT().Get(mock.Anything, int64(1)).Return(nil, repo.ErrNotFound)

	obj := testutil.NewExpect(t, r).GET("/api/settings").
		WithHeader("Authorization", "Bearer "+tok).
		Expect().Status(200).JSON().Object()
	obj.Value("theme").IsEqual("system")
	obj.Value("language").IsEqual("zh")
	obj.Value("notification_enabled").IsEqual(true)
}

func TestSettingsHandler_Get_ReturnsStoredRow(t *testing.T) {
	r, m := setupSettingsHandler(t)
	tok := newSettingsToken(t, 1, "alice")

	m.EXPECT().Get(mock.Anything, int64(1)).Return(&repo.UserSettings{
		UserID: 1, NotificationEnabled: false, Theme: "dark", Language: "en", SettingsJSON: "{}",
	}, nil)

	testutil.NewExpect(t, r).GET("/api/settings").
		WithHeader("Authorization", "Bearer "+tok).
		Expect().Status(200).JSON().Object().
		Value("theme").IsEqual("dark")
}

func TestSettingsHandler_Update_PartialTheme(t *testing.T) {
	r, m := setupSettingsHandler(t)
	tok := newSettingsToken(t, 1, "alice")

	// PUT triggers: Get (load current) → Upsert → Get (re-read for response).
	m.EXPECT().Get(mock.Anything, int64(1)).Return(&repo.UserSettings{
		UserID: 1, NotificationEnabled: true, Theme: "system", Language: "zh", SettingsJSON: "{}",
	}, nil).Once()
	m.EXPECT().Upsert(mock.Anything, mock.MatchedBy(func(s *repo.UserSettings) bool {
		return s.UserID == 1 && s.Theme == "dark" && s.Language == "zh" && s.NotificationEnabled
	})).Return(nil)
	m.EXPECT().Get(mock.Anything, int64(1)).Return(&repo.UserSettings{
		UserID: 1, NotificationEnabled: true, Theme: "dark", Language: "zh", SettingsJSON: "{}",
	}, nil).Once()

	obj := testutil.NewExpect(t, r).PUT("/api/settings").
		WithHeader("Authorization", "Bearer "+tok).
		WithJSON(map[string]string{"theme": "dark"}).
		Expect().Status(200).JSON().Object()
	obj.Value("theme").IsEqual("dark")
	obj.Value("language").IsEqual("zh")
}

func TestSettingsHandler_Update_NotificationToggle(t *testing.T) {
	r, m := setupSettingsHandler(t)
	tok := newSettingsToken(t, 1, "alice")

	m.EXPECT().Get(mock.Anything, int64(1)).Return(&repo.UserSettings{
		UserID: 1, NotificationEnabled: true, Theme: "system", Language: "zh", SettingsJSON: "{}",
	}, nil).Once()
	m.EXPECT().Upsert(mock.Anything, mock.MatchedBy(func(s *repo.UserSettings) bool {
		return s.UserID == 1 && !s.NotificationEnabled
	})).Return(nil)
	m.EXPECT().Get(mock.Anything, int64(1)).Return(&repo.UserSettings{
		UserID: 1, NotificationEnabled: false, Theme: "system", Language: "zh", SettingsJSON: "{}",
	}, nil).Once()

	testutil.NewExpect(t, r).PUT("/api/settings").
		WithHeader("Authorization", "Bearer "+tok).
		WithJSON(map[string]any{"notification_enabled": false}).
		Expect().Status(200).JSON().Object().
		Value("notification_enabled").IsEqual(false)
}

func TestSettingsHandler_Update_400_BadJSON(t *testing.T) {
	r, _ := setupSettingsHandler(t)
	tok := newSettingsToken(t, 1, "alice")

	testutil.NewExpect(t, r).PUT("/api/settings").
		WithHeader("Authorization", "Bearer "+tok).
		WithHeader("Content-Type", "application/json").
		WithText("{not json").
		Expect().Status(400)
}

func TestSettingsHandler_Update_NoToken_401(t *testing.T) {
	r, _ := setupSettingsHandler(t)
	testutil.NewExpect(t, r).PUT("/api/settings").
		WithJSON(map[string]string{"theme": "dark"}).
		Expect().Status(401)
}

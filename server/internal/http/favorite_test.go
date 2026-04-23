package http_test

import (
	"errors"
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

func setupFavoriteHandler(t *testing.T) (*gin.Engine, *mocks.FavoriteRepoMock) {
	t.Helper()
	gin.SetMode(gin.TestMode)
	store := mocks.NewFavoriteRepoMock(t)
	svc := service.NewFavoriteService(store)
	r := gin.New()
	authed := r.Group("/api")
	authed.Use(middleware.JWTGin(testSecret))
	imhttp.RegisterFavoriteRoutes(authed, svc)
	return r, store
}

func newFavoriteToken(t *testing.T, uid int64, username string) string {
	t.Helper()
	tok, err := auth.GenerateToken(testSecret, uid, username)
	require.NoError(t, err)
	return tok
}

func TestFavoriteHandler_Add_NoToken_401(t *testing.T) {
	r, _ := setupFavoriteHandler(t)
	testutil.NewExpect(t, r).POST("/api/favorites/42").
		Expect().Status(401)
}

func TestFavoriteHandler_Add_OK(t *testing.T) {
	r, store := setupFavoriteHandler(t)
	tok := newFavoriteToken(t, 7, "alice")

	store.EXPECT().Add(mock.Anything, int64(7), int64(42)).Return(nil)

	testutil.NewExpect(t, r).POST("/api/favorites/42").
		WithHeader("Authorization", "Bearer "+tok).
		Expect().Status(201).JSON().Object().
		Value("status").IsEqual("ok")
}

func TestFavoriteHandler_Add_BadID_400(t *testing.T) {
	r, _ := setupFavoriteHandler(t)
	tok := newFavoriteToken(t, 7, "alice")

	testutil.NewExpect(t, r).POST("/api/favorites/not-an-int").
		WithHeader("Authorization", "Bearer "+tok).
		Expect().Status(400)
}

func TestFavoriteHandler_Add_StoreError_500(t *testing.T) {
	r, store := setupFavoriteHandler(t)
	tok := newFavoriteToken(t, 7, "alice")
	store.EXPECT().Add(mock.Anything, int64(7), int64(42)).Return(errors.New("db down"))

	testutil.NewExpect(t, r).POST("/api/favorites/42").
		WithHeader("Authorization", "Bearer "+tok).
		Expect().Status(500)
}

func TestFavoriteHandler_Remove_OK(t *testing.T) {
	r, store := setupFavoriteHandler(t)
	tok := newFavoriteToken(t, 7, "alice")
	store.EXPECT().Remove(mock.Anything, int64(7), int64(42)).Return(nil)

	testutil.NewExpect(t, r).DELETE("/api/favorites/42").
		WithHeader("Authorization", "Bearer "+tok).
		Expect().Status(204)
}

func TestFavoriteHandler_Remove_NotFound_404(t *testing.T) {
	// Service surfaces repo.ErrNotFound; handler maps to 404 (tightened from
	// the legacy 500 — see RegisterFavoriteRoutes doc comment).
	r, store := setupFavoriteHandler(t)
	tok := newFavoriteToken(t, 7, "alice")
	store.EXPECT().Remove(mock.Anything, int64(7), int64(42)).Return(repo.ErrNotFound)

	testutil.NewExpect(t, r).DELETE("/api/favorites/42").
		WithHeader("Authorization", "Bearer "+tok).
		Expect().Status(404)
}

func TestFavoriteHandler_List_Empty(t *testing.T) {
	r, store := setupFavoriteHandler(t)
	tok := newFavoriteToken(t, 7, "alice")
	// Stores return literal nil for "no rows"; service normalises to an
	// empty (non-nil) slice so the JSON envelope always has "favorites": [].
	store.EXPECT().List(mock.Anything, int64(7)).Return(nil, nil)

	testutil.NewExpect(t, r).GET("/api/favorites").
		WithHeader("Authorization", "Bearer "+tok).
		Expect().Status(200).JSON().Object().
		Value("favorites").Array().IsEmpty()
}

func TestFavoriteHandler_List_Some(t *testing.T) {
	r, store := setupFavoriteHandler(t)
	tok := newFavoriteToken(t, 7, "alice")
	store.EXPECT().List(mock.Anything, int64(7)).Return([]repo.FavoriteWithMessage{
		{UserID: 7, MessageID: 1, Message: repo.Message{ID: 1, Content: "first"}},
		{UserID: 7, MessageID: 2, Message: repo.Message{ID: 2, Content: "second"}},
	}, nil)

	resp := testutil.NewExpect(t, r).GET("/api/favorites").
		WithHeader("Authorization", "Bearer "+tok).
		Expect().Status(200).JSON().Object()
	resp.Value("favorites").Array().Length().IsEqual(2)
	resp.Value("favorites").Array().Value(0).Object().
		Value("message").Object().Value("content").IsEqual("first")
}

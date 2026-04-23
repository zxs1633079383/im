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

func setupSearchHandler(t *testing.T) (*gin.Engine, *mocks.SearchRepoMock) {
	t.Helper()
	gin.SetMode(gin.TestMode)
	store := mocks.NewSearchRepoMock(t)
	svc := service.NewSearchService(store)
	r := gin.New()
	authed := r.Group("/api")
	authed.Use(middleware.JWTGin(testSecret))
	imhttp.RegisterSearchRoutes(authed, svc, nil)
	return r, store
}

func newSearchToken(t *testing.T, uid int64, username string) string {
	t.Helper()
	tok, err := auth.GenerateToken(testSecret, uid, username)
	require.NoError(t, err)
	return tok
}

func TestSearchHandler_NoToken_401(t *testing.T) {
	r, _ := setupSearchHandler(t)
	testutil.NewExpect(t, r).GET("/api/search").
		WithQuery("q", "hello").
		Expect().Status(401)
}

func TestSearchHandler_MissingQ_400(t *testing.T) {
	r, _ := setupSearchHandler(t)
	tok := newSearchToken(t, 1, "alice")
	testutil.NewExpect(t, r).GET("/api/search").
		WithHeader("Authorization", "Bearer "+tok).
		Expect().Status(400)
}

func TestSearchHandler_AllTypes_EmitsAllThreeKeys(t *testing.T) {
	r, store := setupSearchHandler(t)
	tok := newSearchToken(t, 1, "alice")

	store.EXPECT().SearchMessages(mock.Anything, "hello", int64(1), int64(0), service.SearchDefaultLimit).
		Return([]repo.MessageSearchResult{}, nil)
	store.EXPECT().SearchUsers(mock.Anything, "hello", int64(1), service.SearchDefaultLimit).
		Return([]repo.User{}, nil)
	store.EXPECT().SearchChannels(mock.Anything, "hello", int64(1), service.SearchDefaultLimit).
		Return([]repo.Channel{}, nil)

	resp := testutil.NewExpect(t, r).GET("/api/search").
		WithHeader("Authorization", "Bearer "+tok).
		WithQuery("q", "hello").
		Expect().Status(200).JSON().Object()
	resp.ContainsKey("messages")
	resp.ContainsKey("users")
	resp.ContainsKey("channels")
}

func TestSearchHandler_TypeMessages_OmitsOtherKeys(t *testing.T) {
	r, store := setupSearchHandler(t)
	tok := newSearchToken(t, 1, "alice")

	store.EXPECT().SearchMessages(mock.Anything, "hi", int64(1), int64(0), service.SearchDefaultLimit).
		Return([]repo.MessageSearchResult{
			{Message: repo.Message{ID: 10, ChannelID: 7, Seq: 1, Content: "hi"}, ChannelName: "general"},
		}, nil)

	resp := testutil.NewExpect(t, r).GET("/api/search").
		WithHeader("Authorization", "Bearer "+tok).
		WithQuery("q", "hi").
		WithQuery("type", "messages").
		Expect().Status(200).JSON().Object()
	resp.ContainsKey("messages")
	resp.NotContainsKey("users")
	resp.NotContainsKey("channels")
	resp.Value("messages").Array().Length().IsEqual(1)
	resp.Value("messages").Array().Value(0).Object().Value("channel_name").IsEqual("general")
}

func TestSearchHandler_TypeUsers_OmitsOtherKeys(t *testing.T) {
	r, store := setupSearchHandler(t)
	tok := newSearchToken(t, 1, "alice")

	store.EXPECT().SearchUsers(mock.Anything, "alice", int64(1), service.SearchDefaultLimit).
		Return([]repo.User{{ID: 7, Username: "alice"}}, nil)

	resp := testutil.NewExpect(t, r).GET("/api/search").
		WithHeader("Authorization", "Bearer "+tok).
		WithQuery("q", "alice").
		WithQuery("type", "users").
		Expect().Status(200).JSON().Object()
	resp.NotContainsKey("messages")
	resp.ContainsKey("users")
	resp.NotContainsKey("channels")
}

func TestSearchHandler_TypeChannels_OmitsOtherKeys(t *testing.T) {
	r, store := setupSearchHandler(t)
	tok := newSearchToken(t, 1, "alice")

	store.EXPECT().SearchChannels(mock.Anything, "general", int64(1), service.SearchDefaultLimit).
		Return([]repo.Channel{{ID: 5, Name: "general"}}, nil)

	resp := testutil.NewExpect(t, r).GET("/api/search").
		WithHeader("Authorization", "Bearer "+tok).
		WithQuery("q", "general").
		WithQuery("type", "channels").
		Expect().Status(200).JSON().Object()
	resp.NotContainsKey("messages")
	resp.NotContainsKey("users")
	resp.ContainsKey("channels")
}

func TestSearchHandler_ChannelIDForwardedToMessages(t *testing.T) {
	r, store := setupSearchHandler(t)
	tok := newSearchToken(t, 1, "alice")

	// Verify the integer query param is parsed and forwarded; the .EXPECT
	// arg-match is the assertion (mockery fails the test otherwise).
	store.EXPECT().SearchMessages(mock.Anything, "hi", int64(1), int64(99), service.SearchDefaultLimit).
		Return([]repo.MessageSearchResult{}, nil)

	testutil.NewExpect(t, r).GET("/api/search").
		WithHeader("Authorization", "Bearer "+tok).
		WithQuery("q", "hi").
		WithQuery("type", "messages").
		WithQuery("channel_id", "99").
		Expect().Status(200)
}

func TestSearchHandler_LimitClampedToMax(t *testing.T) {
	r, store := setupSearchHandler(t)
	tok := newSearchToken(t, 1, "alice")

	// Caller asks for 500 — handler must clamp to SearchMaxLimit before
	// hitting the store.
	store.EXPECT().SearchUsers(mock.Anything, "x", int64(1), service.SearchMaxLimit).
		Return([]repo.User{}, nil)

	testutil.NewExpect(t, r).GET("/api/search").
		WithHeader("Authorization", "Bearer "+tok).
		WithQuery("q", "x").
		WithQuery("type", "users").
		WithQuery("limit", "500").
		Expect().Status(200)
}

func TestSearchHandler_StoreError_500(t *testing.T) {
	r, store := setupSearchHandler(t)
	tok := newSearchToken(t, 1, "alice")
	store.EXPECT().SearchMessages(mock.Anything, "x", int64(1), int64(0), service.SearchDefaultLimit).
		Return(nil, errors.New("db down"))

	testutil.NewExpect(t, r).GET("/api/search").
		WithHeader("Authorization", "Bearer "+tok).
		WithQuery("q", "x").
		WithQuery("type", "messages").
		Expect().Status(500)
}

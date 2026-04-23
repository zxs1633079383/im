package http_test

import (
	"errors"
	"sync"
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

// recordingPusher captures PushFriendEvent calls so tests can assert that the
// real-time hook fired with the expected payload after a successful POST.
type recordingPusher struct {
	mu     sync.Mutex
	events []friendPushEvent
}

type friendPushEvent struct {
	target int64
	kind   string
	from   int64
}

func (p *recordingPusher) PushFriendEvent(target int64, kind string, from int64) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.events = append(p.events, friendPushEvent{target: target, kind: kind, from: from})
}

func (p *recordingPusher) snapshot() []friendPushEvent {
	p.mu.Lock()
	defer p.mu.Unlock()
	out := make([]friendPushEvent, len(p.events))
	copy(out, p.events)
	return out
}

func setupFriendHandler(t *testing.T, pusher imhttp.FriendEventPusher) (*gin.Engine, *mocks.FriendshipRepoMock, *mocks.UserRepoMock) {
	t.Helper()
	gin.SetMode(gin.TestMode)
	fs := mocks.NewFriendshipRepoMock(t)
	us := mocks.NewUserRepoMock(t)
	svc := service.NewFriendService(fs, us)
	r := gin.New()
	authed := r.Group("/api")
	authed.Use(middleware.JWTGin(testSecret))
	imhttp.RegisterFriendRoutes(authed, svc, pusher)
	return r, fs, us
}

func newFriendToken(t *testing.T, uid int64, username string) string {
	t.Helper()
	tok, err := auth.GenerateToken(testSecret, uid, username)
	require.NoError(t, err)
	return tok
}

func TestFriendHandler_NoToken_401(t *testing.T) {
	r, _, _ := setupFriendHandler(t, nil)
	testutil.NewExpect(t, r).GET("/api/friends").Expect().Status(401)
}

func TestFriendHandler_SendRequest_201_PushesEvent(t *testing.T) {
	pusher := &recordingPusher{}
	r, fs, _ := setupFriendHandler(t, pusher)
	tok := newFriendToken(t, 1, "alice")

	fs.EXPECT().SendRequest(mock.Anything, int64(1), int64(2)).Return(nil)

	testutil.NewExpect(t, r).POST("/api/friends/request").
		WithHeader("Authorization", "Bearer "+tok).
		WithJSON(map[string]int64{"addressee_id": 2}).
		Expect().Status(201).JSON().Object().
		Value("status").IsEqual("pending")

	got := pusher.snapshot()
	require.Len(t, got, 1, "pusher must fire exactly once on success")
	require.Equal(t, friendPushEvent{target: 2, kind: "request", from: 1}, got[0])
}

func TestFriendHandler_SendRequest_422_MissingAddressee(t *testing.T) {
	r, _, _ := setupFriendHandler(t, nil)
	tok := newFriendToken(t, 1, "alice")
	testutil.NewExpect(t, r).POST("/api/friends/request").
		WithHeader("Authorization", "Bearer "+tok).
		WithJSON(map[string]int64{"addressee_id": 0}).
		Expect().Status(422)
}

func TestFriendHandler_SendRequest_409_Duplicate(t *testing.T) {
	pusher := &recordingPusher{}
	r, fs, _ := setupFriendHandler(t, pusher)
	tok := newFriendToken(t, 1, "alice")

	fs.EXPECT().SendRequest(mock.Anything, int64(1), int64(2)).
		Return(errors.New("ERROR: duplicate key value violates unique constraint (SQLSTATE 23505)"))

	testutil.NewExpect(t, r).POST("/api/friends/request").
		WithHeader("Authorization", "Bearer "+tok).
		WithJSON(map[string]int64{"addressee_id": 2}).
		Expect().Status(409)

	require.Empty(t, pusher.snapshot(), "pusher must NOT fire on duplicate")
}

func TestFriendHandler_AcceptRequest_404_NotAddressee(t *testing.T) {
	r, fs, _ := setupFriendHandler(t, nil)
	tok := newFriendToken(t, 1, "alice")

	fs.EXPECT().AcceptRequest(mock.Anything, int64(7), int64(1)).Return(repo.ErrNotFound)

	testutil.NewExpect(t, r).POST("/api/friends/accept").
		WithHeader("Authorization", "Bearer "+tok).
		WithJSON(map[string]int64{"friendship_id": 7}).
		Expect().Status(404)
}

func TestFriendHandler_RejectRequest_OK(t *testing.T) {
	r, fs, _ := setupFriendHandler(t, nil)
	tok := newFriendToken(t, 2, "bob")

	fs.EXPECT().RejectRequest(mock.Anything, int64(7), int64(2)).Return(nil)

	testutil.NewExpect(t, r).POST("/api/friends/reject").
		WithHeader("Authorization", "Bearer "+tok).
		WithJSON(map[string]int64{"friendship_id": 7}).
		Expect().Status(200).JSON().Object().
		Value("status").IsEqual("rejected")
}

func TestFriendHandler_ListFriends_EmptyArray(t *testing.T) {
	r, fs, _ := setupFriendHandler(t, nil)
	tok := newFriendToken(t, 1, "alice")

	// nil slice from the repo must serialize as [] not null.
	fs.EXPECT().ListFriends(mock.Anything, int64(1)).Return(nil, nil)

	testutil.NewExpect(t, r).GET("/api/friends").
		WithHeader("Authorization", "Bearer "+tok).
		Expect().Status(200).JSON().Array().Length().IsEqual(0)
}

func TestFriendHandler_ListPending_OK(t *testing.T) {
	r, fs, _ := setupFriendHandler(t, nil)
	tok := newFriendToken(t, 1, "alice")

	fs.EXPECT().ListPendingRequests(mock.Anything, int64(1)).
		Return([]repo.PendingRequest{{
			Friendship: repo.Friendship{ID: 5, RequesterID: 2, AddresseeID: 1, Status: repo.FriendshipPending},
			Requester:  repo.User{ID: 2, Username: "bob"},
		}}, nil)

	testutil.NewExpect(t, r).GET("/api/friends/pending").
		WithHeader("Authorization", "Bearer "+tok).
		Expect().Status(200).JSON().Array().Length().IsEqual(1)
}

func TestFriendHandler_BlockUser_200(t *testing.T) {
	r, fs, _ := setupFriendHandler(t, nil)
	tok := newFriendToken(t, 1, "alice")

	fs.EXPECT().BlockUser(mock.Anything, int64(1), int64(2)).Return(nil)

	testutil.NewExpect(t, r).POST("/api/friends/block").
		WithHeader("Authorization", "Bearer "+tok).
		WithJSON(map[string]int64{"user_id": 2}).
		Expect().Status(200).JSON().Object().
		Value("status").IsEqual("blocked")
}

func TestFriendHandler_SearchUsers_OK(t *testing.T) {
	r, _, us := setupFriendHandler(t, nil)
	tok := newFriendToken(t, 1, "alice")

	us.EXPECT().Search(mock.Anything, "bob", int64(1)).
		Return([]repo.User{{ID: 2, Username: "bob"}}, nil)

	testutil.NewExpect(t, r).GET("/api/users/search").
		WithQuery("q", "bob").
		WithHeader("Authorization", "Bearer "+tok).
		Expect().Status(200).JSON().Array().Length().IsEqual(1)
}

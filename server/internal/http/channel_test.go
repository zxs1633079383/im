package http_test

import (
	"context"
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

// recordingChannelPusher captures PushChannelEvent calls so tests can assert
// the real-time hook fired with the right payload after a successful POST.
type recordingChannelPusher struct {
	mu     sync.Mutex
	events []channelPushEvent
}

type channelPushEvent struct {
	target int64
	kind   string
	chID   int64
	name   string
}

func (p *recordingChannelPusher) PushChannelEvent(target int64, kind string, chID int64, name string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.events = append(p.events, channelPushEvent{target: target, kind: kind, chID: chID, name: name})
}

func (p *recordingChannelPusher) snapshot() []channelPushEvent {
	p.mu.Lock()
	defer p.mu.Unlock()
	out := make([]channelPushEvent, len(p.events))
	copy(out, p.events)
	return out
}

func setupChannelHandler(t *testing.T, pusher imhttp.ChannelEventPusher) (*gin.Engine, *mocks.ChannelRepoMock, *mocks.UserRepoMock) {
	t.Helper()
	gin.SetMode(gin.TestMode)
	ch := mocks.NewChannelRepoMock(t)
	us := mocks.NewUserRepoMock(t)
	svc := service.NewChannelService(ch, us)
	r := gin.New()
	authed := r.Group("/api")
	authed.Use(middleware.JWTGin(testSecret))
	imhttp.RegisterChannelRoutes(authed, svc, pusher)
	return r, ch, us
}

func newChannelToken(t *testing.T, uid int64, username string) string {
	t.Helper()
	tok, err := auth.GenerateToken(testSecret, uid, username)
	require.NoError(t, err)
	return tok
}

func TestChannelHandler_NoToken_401(t *testing.T) {
	r, _, _ := setupChannelHandler(t, nil)
	testutil.NewExpect(t, r).GET("/api/channels").Expect().Status(401)
}

func TestChannelHandler_CreateGroup_201_PushesAddedEvents(t *testing.T) {
	pusher := &recordingChannelPusher{}
	r, ch, _ := setupChannelHandler(t, pusher)
	tok := newChannelToken(t, 1, "alice")

	ch.EXPECT().Create(mock.Anything, mock.MatchedBy(func(c *repo.Channel) bool {
		return c.Type == repo.ChannelTypeGroup && c.Name == "team"
	})).Run(func(_ context.Context, c *repo.Channel) {
		c.ID = 42
	}).Return(nil)
	ch.EXPECT().AddMember(mock.Anything, int64(42), int64(1), repo.MemberRoleOwner).Return(nil)
	ch.EXPECT().AddMember(mock.Anything, int64(42), int64(2), repo.MemberRoleMember).Return(nil)
	ch.EXPECT().AddMember(mock.Anything, int64(42), int64(3), repo.MemberRoleMember).Return(nil)

	testutil.NewExpect(t, r).POST("/api/channels").
		WithHeader("Authorization", "Bearer "+tok).
		WithJSON(map[string]any{
			"name":       "team",
			"member_ids": []int64{2, 3},
		}).
		Expect().Status(201).JSON().Object().
		Value("id").Number().IsEqual(42)

	got := pusher.snapshot()
	require.Len(t, got, 2)
	require.Equal(t, channelPushEvent{target: 2, kind: "added", chID: 42, name: "team"}, got[0])
	require.Equal(t, channelPushEvent{target: 3, kind: "added", chID: 42, name: "team"}, got[1])
}

func TestChannelHandler_CreateGroup_422_NoName(t *testing.T) {
	r, _, _ := setupChannelHandler(t, nil)
	tok := newChannelToken(t, 1, "alice")
	testutil.NewExpect(t, r).POST("/api/channels").
		WithHeader("Authorization", "Bearer "+tok).
		WithJSON(map[string]any{"name": ""}).
		Expect().Status(422)
}

func TestChannelHandler_CreateOrGetDM_201_New(t *testing.T) {
	r, ch, _ := setupChannelHandler(t, nil)
	tok := newChannelToken(t, 1, "alice")

	ch.EXPECT().FindDM(mock.Anything, int64(1), int64(2)).Return(nil, repo.ErrNotFound)
	ch.EXPECT().Create(mock.Anything, mock.MatchedBy(func(c *repo.Channel) bool {
		return c.Type == repo.ChannelTypeDM
	})).Run(func(_ context.Context, c *repo.Channel) {
		c.ID = 8
	}).Return(nil)
	ch.EXPECT().AddMember(mock.Anything, int64(8), int64(1), repo.MemberRoleMember).Return(nil)
	ch.EXPECT().AddMember(mock.Anything, int64(8), int64(2), repo.MemberRoleMember).Return(nil)

	testutil.NewExpect(t, r).POST("/api/channels/dm").
		WithHeader("Authorization", "Bearer "+tok).
		WithJSON(map[string]int64{"peer_id": 2}).
		Expect().Status(201).JSON().Object().
		Value("id").Number().IsEqual(8)
}

func TestChannelHandler_CreateOrGetDM_200_Existing(t *testing.T) {
	r, ch, _ := setupChannelHandler(t, nil)
	tok := newChannelToken(t, 1, "alice")

	ch.EXPECT().FindDM(mock.Anything, int64(1), int64(2)).
		Return(&repo.Channel{ID: 9, Type: repo.ChannelTypeDM}, nil)

	testutil.NewExpect(t, r).POST("/api/channels/dm").
		WithHeader("Authorization", "Bearer "+tok).
		WithJSON(map[string]int64{"peer_id": 2}).
		Expect().Status(200).JSON().Object().
		Value("id").Number().IsEqual(9)
}

func TestChannelHandler_CreateOrGetDM_422_Self(t *testing.T) {
	r, _, _ := setupChannelHandler(t, nil)
	tok := newChannelToken(t, 1, "alice")
	testutil.NewExpect(t, r).POST("/api/channels/dm").
		WithHeader("Authorization", "Bearer "+tok).
		WithJSON(map[string]int64{"peer_id": 1}).
		Expect().Status(422)
}

func TestChannelHandler_ListChannels_EmptyArray(t *testing.T) {
	r, ch, _ := setupChannelHandler(t, nil)
	tok := newChannelToken(t, 1, "alice")

	// nil slice from the repo must serialize as [] not null.
	ch.EXPECT().ListByUserWithPreview(mock.Anything, int64(1)).Return(nil, nil)

	testutil.NewExpect(t, r).GET("/api/channels").
		WithHeader("Authorization", "Bearer "+tok).
		Expect().Status(200).JSON().Array().Length().IsEqual(0)
}

func TestChannelHandler_GetChannel_403_NonMember(t *testing.T) {
	r, ch, _ := setupChannelHandler(t, nil)
	tok := newChannelToken(t, 2, "bob")

	ch.EXPECT().GetMember(mock.Anything, int64(7), int64(2)).Return(nil, repo.ErrNotFound)

	testutil.NewExpect(t, r).GET("/api/channels/7").
		WithHeader("Authorization", "Bearer "+tok).
		Expect().Status(403)
}

func TestChannelHandler_GetChannel_OK(t *testing.T) {
	r, ch, _ := setupChannelHandler(t, nil)
	tok := newChannelToken(t, 1, "alice")

	ch.EXPECT().GetMember(mock.Anything, int64(7), int64(1)).
		Return(&repo.ChannelMember{ChannelID: 7, UserID: 1, Role: repo.MemberRoleOwner}, nil)
	ch.EXPECT().GetByID(mock.Anything, int64(7)).
		Return(&repo.Channel{ID: 7, Name: "team"}, nil)

	testutil.NewExpect(t, r).GET("/api/channels/7").
		WithHeader("Authorization", "Bearer "+tok).
		Expect().Status(200).JSON().Object().
		Value("name").IsEqual("team")
}

func TestChannelHandler_UpdateChannel_403_PlainMember(t *testing.T) {
	r, ch, _ := setupChannelHandler(t, nil)
	tok := newChannelToken(t, 2, "bob")

	ch.EXPECT().GetMember(mock.Anything, int64(7), int64(2)).
		Return(&repo.ChannelMember{ChannelID: 7, UserID: 2, Role: repo.MemberRoleMember}, nil)

	testutil.NewExpect(t, r).PUT("/api/channels/7").
		WithHeader("Authorization", "Bearer "+tok).
		WithJSON(map[string]string{"name": "x"}).
		Expect().Status(403)
}

func TestChannelHandler_AddMember_201(t *testing.T) {
	pusher := &recordingChannelPusher{}
	r, ch, _ := setupChannelHandler(t, pusher)
	tok := newChannelToken(t, 1, "alice")

	ch.EXPECT().GetMember(mock.Anything, int64(7), int64(1)).
		Return(&repo.ChannelMember{ChannelID: 7, UserID: 1, Role: repo.MemberRoleOwner}, nil)
	ch.EXPECT().AddMember(mock.Anything, int64(7), int64(9), repo.MemberRoleMember).Return(nil)
	// Post-insert name lookup feeds the channel_event "added" payload.
	ch.EXPECT().GetByID(mock.Anything, int64(7)).
		Return(&repo.Channel{ID: 7, Name: "team"}, nil)

	testutil.NewExpect(t, r).POST("/api/channels/7/members").
		WithHeader("Authorization", "Bearer "+tok).
		WithJSON(map[string]int64{"user_id": 9}).
		Expect().Status(201).JSON().Object().
		Value("status").IsEqual("added")

	got := pusher.snapshot()
	require.Len(t, got, 1, "pusher must fire exactly once on add-member success")
	require.Equal(t, channelPushEvent{target: 9, kind: "added", chID: 7, name: "team"}, got[0])
}

func TestChannelHandler_AddMember_403_NonAdmin_NoPush(t *testing.T) {
	pusher := &recordingChannelPusher{}
	r, ch, _ := setupChannelHandler(t, pusher)
	tok := newChannelToken(t, 2, "bob")

	ch.EXPECT().GetMember(mock.Anything, int64(7), int64(2)).
		Return(&repo.ChannelMember{ChannelID: 7, UserID: 2, Role: repo.MemberRoleMember}, nil)

	testutil.NewExpect(t, r).POST("/api/channels/7/members").
		WithHeader("Authorization", "Bearer "+tok).
		WithJSON(map[string]int64{"user_id": 9}).
		Expect().Status(403)

	require.Empty(t, pusher.snapshot(), "pusher must NOT fire on 403")
}

func TestChannelHandler_RemoveMember_403_OwnerProtected(t *testing.T) {
	r, ch, _ := setupChannelHandler(t, nil)
	tok := newChannelToken(t, 2, "bob")

	ch.EXPECT().GetMember(mock.Anything, int64(7), int64(2)).
		Return(&repo.ChannelMember{ChannelID: 7, UserID: 2, Role: repo.MemberRoleAdmin}, nil)
	ch.EXPECT().GetMember(mock.Anything, int64(7), int64(1)).
		Return(&repo.ChannelMember{ChannelID: 7, UserID: 1, Role: repo.MemberRoleOwner}, nil)

	testutil.NewExpect(t, r).DELETE("/api/channels/7/members/1").
		WithHeader("Authorization", "Bearer "+tok).
		Expect().Status(403)
}

func TestChannelHandler_ListMembers_200(t *testing.T) {
	r, ch, us := setupChannelHandler(t, nil)
	tok := newChannelToken(t, 1, "alice")

	ch.EXPECT().GetMember(mock.Anything, int64(7), int64(1)).
		Return(&repo.ChannelMember{ChannelID: 7, UserID: 1, Role: repo.MemberRoleOwner}, nil)
	ch.EXPECT().ListMembers(mock.Anything, int64(7)).
		Return([]repo.ChannelMember{
			{ChannelID: 7, UserID: 1, Role: repo.MemberRoleOwner},
		}, nil)
	us.EXPECT().GetByID(mock.Anything, int64(1)).
		Return(&repo.User{ID: 1, Username: "alice", DisplayName: "Alice"}, nil)

	testutil.NewExpect(t, r).GET("/api/channels/7/members").
		WithHeader("Authorization", "Bearer "+tok).
		Expect().Status(200).JSON().Array().Length().IsEqual(1)
}

func TestChannelHandler_LeaveChannel_403_Owner(t *testing.T) {
	r, ch, _ := setupChannelHandler(t, nil)
	tok := newChannelToken(t, 1, "alice")

	ch.EXPECT().GetMember(mock.Anything, int64(7), int64(1)).
		Return(&repo.ChannelMember{ChannelID: 7, UserID: 1, Role: repo.MemberRoleOwner}, nil)

	testutil.NewExpect(t, r).POST("/api/channels/7/leave").
		WithHeader("Authorization", "Bearer "+tok).
		Expect().Status(403)
}

func TestChannelHandler_LeaveChannel_200(t *testing.T) {
	r, ch, _ := setupChannelHandler(t, nil)
	tok := newChannelToken(t, 2, "bob")

	ch.EXPECT().GetMember(mock.Anything, int64(7), int64(2)).
		Return(&repo.ChannelMember{ChannelID: 7, UserID: 2, Role: repo.MemberRoleMember}, nil)
	ch.EXPECT().RemoveMember(mock.Anything, int64(7), int64(2)).Return(nil)

	testutil.NewExpect(t, r).POST("/api/channels/7/leave").
		WithHeader("Authorization", "Bearer "+tok).
		Expect().Status(200).JSON().Object().
		Value("status").IsEqual("left")
}

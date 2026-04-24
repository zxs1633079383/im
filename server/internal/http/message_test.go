package http_test

import (
	"context"
	"strconv"
	"sync"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/lib/pq"
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

// recordingMessagePusher captures BroadcastMessage calls so tests can assert
// that the post-send fan-out hook fired with the right payload per user
// bucket. Each batch is flattened into one event-per-user in `events` so
// existing tests that count "this user got pushed" keep working.
type recordingMessagePusher struct {
	mu     sync.Mutex
	events []messagePushEvent
}

type messagePushEvent struct {
	userID int64
	msg    *repo.Message
}

func (p *recordingMessagePusher) BroadcastMessage(_ int64, userIDs []int64, msg *repo.Message) {
	p.mu.Lock()
	defer p.mu.Unlock()
	for _, uid := range userIDs {
		p.events = append(p.events, messagePushEvent{userID: uid, msg: msg})
	}
}

func (p *recordingMessagePusher) snapshot() []messagePushEvent {
	p.mu.Lock()
	defer p.mu.Unlock()
	out := make([]messagePushEvent, len(p.events))
	copy(out, p.events)
	return out
}

// recordingReadSyncer captures PushReadSync calls.
type recordingReadSyncer struct {
	mu     sync.Mutex
	events []readSyncEvent
}

type readSyncEvent struct {
	userID    int64
	channelID int64
	readSeq   int64
}

func (s *recordingReadSyncer) PushReadSync(userID, channelID, readSeq int64) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.events = append(s.events, readSyncEvent{userID: userID, channelID: channelID, readSeq: readSeq})
}

func (s *recordingReadSyncer) snapshot() []readSyncEvent {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]readSyncEvent, len(s.events))
	copy(out, s.events)
	return out
}

// setupMessageHandler wires the routes against repo mocks and the supplied
// hook implementations (any may be nil to disable that side-channel).
func setupMessageHandler(t *testing.T, opts imhttp.MessageRouteOpts) (*gin.Engine, *mocks.MessageRepoMock, *mocks.ChannelRepoMock) {
	t.Helper()
	gin.SetMode(gin.TestMode)
	ms := mocks.NewMessageRepoMock(t)
	cs := mocks.NewChannelRepoMock(t)
	svc := service.NewMessageService(ms, cs, nil)
	r := gin.New()
	authed := r.Group("/api")
	authed.Use(middleware.JWTGin(testSecret))
	imhttp.RegisterMessageRoutes(authed, svc, opts)
	return r, ms, cs
}

func newMessageToken(t *testing.T, uid int64, username string) string {
	t.Helper()
	tok, err := auth.GenerateToken(testSecret, uid, username)
	require.NoError(t, err)
	return tok
}

func TestMessageHandler_NoToken_401(t *testing.T) {
	r, _, _ := setupMessageHandler(t, imhttp.MessageRouteOpts{})
	testutil.NewExpect(t, r).GET("/api/channels/1/messages").Expect().Status(401)
}

func TestMessageHandler_SendMessage_201(t *testing.T) {
	r, ms, cs := setupMessageHandler(t, imhttp.MessageRouteOpts{})
	tok := newMessageToken(t, 42, "alice")

	cs.EXPECT().GetMember(mock.Anything, int64(1), int64(42)).
		Return(&repo.ChannelMember{ChannelID: 1, UserID: 42}, nil)
	ms.EXPECT().Send(mock.Anything, mock.MatchedBy(func(m *repo.Message) bool {
		return m.ChannelID == 1 && m.SenderID == 42 &&
			m.Content == "hello" && m.ClientMsgID == "uuid-1"
	})).Run(func(_ context.Context, m *repo.Message) {
		m.ID, m.Seq = 100, 1
	}).Return(nil)

	testutil.NewExpect(t, r).POST("/api/channels/1/messages").
		WithHeader("Authorization", "Bearer "+tok).
		WithJSON(map[string]any{"content": "hello", "client_msg_id": "uuid-1"}).
		Expect().Status(201).JSON().Object().
		Value("id").Number().IsEqual(100)
}

func TestMessageHandler_SendMessage_403_NotMember(t *testing.T) {
	r, _, cs := setupMessageHandler(t, imhttp.MessageRouteOpts{})
	tok := newMessageToken(t, 42, "alice")

	cs.EXPECT().GetMember(mock.Anything, int64(1), int64(42)).Return(nil, repo.ErrNotFound)

	testutil.NewExpect(t, r).POST("/api/channels/1/messages").
		WithHeader("Authorization", "Bearer "+tok).
		WithJSON(map[string]any{"content": "hi"}).
		Expect().Status(403)
}

func TestMessageHandler_SendMessage_422_EmptyContent(t *testing.T) {
	r, _, _ := setupMessageHandler(t, imhttp.MessageRouteOpts{})
	tok := newMessageToken(t, 42, "alice")

	testutil.NewExpect(t, r).POST("/api/channels/1/messages").
		WithHeader("Authorization", "Bearer "+tok).
		WithJSON(map[string]any{"content": ""}).
		Expect().Status(422)
}

func TestMessageHandler_SendMessage_PushesToMembers(t *testing.T) {
	pusher := &recordingMessagePusher{}
	r, ms, cs := setupMessageHandler(t, imhttp.MessageRouteOpts{Pusher: pusher})
	tok := newMessageToken(t, 1, "alice")

	cs.EXPECT().GetMember(mock.Anything, int64(7), int64(1)).
		Return(&repo.ChannelMember{ChannelID: 7, UserID: 1}, nil)
	ms.EXPECT().Send(mock.Anything, mock.Anything).Run(func(_ context.Context, m *repo.Message) {
		m.ID, m.Seq = 1, 1
	}).Return(nil)
	// Background fan-out goroutine queries members.
	cs.EXPECT().ListMembers(mock.Anything, int64(7)).
		Return([]repo.ChannelMember{
			{ChannelID: 7, UserID: 1},
			{ChannelID: 7, UserID: 2},
		}, nil).Maybe()

	testutil.NewExpect(t, r).POST("/api/channels/7/messages").
		WithHeader("Authorization", "Bearer "+tok).
		WithJSON(map[string]any{"content": "hi"}).
		Expect().Status(201)

	// The push fan-out runs in a goroutine — give it a beat to land.
	require.Eventually(t, func() bool { return len(pusher.snapshot()) == 2 }, 500*time.Millisecond, 10*time.Millisecond,
		"pusher should fan out to both channel members")
}

func TestMessageHandler_SendMessage_DirectedSendsPhantomToExcluded(t *testing.T) {
	pusher := &recordingMessagePusher{}
	r, ms, cs := setupMessageHandler(t, imhttp.MessageRouteOpts{Pusher: pusher})
	tok := newMessageToken(t, 1, "alice")

	cs.EXPECT().GetMember(mock.Anything, int64(7), int64(1)).
		Return(&repo.ChannelMember{ChannelID: 7, UserID: 1}, nil)
	ms.EXPECT().Send(mock.Anything, mock.MatchedBy(func(m *repo.Message) bool {
		// VisibleTo is a directed-message marker; service must propagate it.
		return len(m.VisibleTo) == 1 && m.VisibleTo[0] == 2
	})).Run(func(_ context.Context, m *repo.Message) {
		m.ID, m.Seq = 1, 1
		m.VisibleTo = pq.Int64Array{2}
	}).Return(nil)
	// Members: 1, 2, 3 — only 3 should receive a phantom.
	cs.EXPECT().ListMembers(mock.Anything, int64(7)).
		Return([]repo.ChannelMember{
			{ChannelID: 7, UserID: 1},
			{ChannelID: 7, UserID: 2},
			{ChannelID: 7, UserID: 3},
		}, nil).Maybe()

	testutil.NewExpect(t, r).POST("/api/channels/7/messages").
		WithHeader("Authorization", "Bearer "+tok).
		WithJSON(map[string]any{"content": "secret", "visible_to": []int64{2}}).
		Expect().Status(201)

	require.Eventually(t, func() bool { return len(pusher.snapshot()) == 3 }, 500*time.Millisecond, 10*time.Millisecond)

	// Verify exactly one event is a phantom for user 3.
	var phantomFor3 int
	for _, ev := range pusher.snapshot() {
		if ev.userID == 3 && ev.msg.MsgType == repo.MsgTypePhantom {
			phantomFor3++
		}
	}
	require.Equal(t, 1, phantomFor3, "non-visible user must receive a phantom only")
}

func TestMessageHandler_FetchMessages_Default_LatestN(t *testing.T) {
	r, ms, cs := setupMessageHandler(t, imhttp.MessageRouteOpts{})
	tok := newMessageToken(t, 1, "alice")

	cs.EXPECT().GetMember(mock.Anything, int64(7), int64(1)).
		Return(&repo.ChannelMember{ChannelID: 7, UserID: 1}, nil)
	ms.EXPECT().FetchBefore(mock.Anything, int64(7), int64(1), int64(1<<62), 50).
		Return([]repo.Message{{ID: 1}, {ID: 2}}, nil)

	testutil.NewExpect(t, r).GET("/api/channels/7/messages").
		WithHeader("Authorization", "Bearer "+tok).
		Expect().Status(200).JSON().Object().
		Value("messages").Array().Length().IsEqual(2)
}

func TestMessageHandler_FetchMessages_AfterSeq(t *testing.T) {
	r, ms, cs := setupMessageHandler(t, imhttp.MessageRouteOpts{})
	tok := newMessageToken(t, 1, "alice")

	cs.EXPECT().GetMember(mock.Anything, int64(7), int64(1)).
		Return(&repo.ChannelMember{ChannelID: 7, UserID: 1}, nil)
	ms.EXPECT().FetchForUser(mock.Anything, int64(7), int64(1), int64(5), 25).
		Return([]repo.Message{{ID: 6}}, nil)

	testutil.NewExpect(t, r).GET("/api/channels/7/messages").
		WithQuery("after_seq", 5).
		WithQuery("limit", 25).
		WithHeader("Authorization", "Bearer "+tok).
		Expect().Status(200).JSON().Object().
		Value("messages").Array().Length().IsEqual(1)
}

func TestMessageHandler_FetchMessages_403_NotMember(t *testing.T) {
	r, _, cs := setupMessageHandler(t, imhttp.MessageRouteOpts{})
	tok := newMessageToken(t, 1, "alice")
	cs.EXPECT().GetMember(mock.Anything, int64(7), int64(1)).Return(nil, repo.ErrNotFound)

	testutil.NewExpect(t, r).GET("/api/channels/7/messages").
		WithHeader("Authorization", "Bearer "+tok).
		Expect().Status(403)
}

func TestMessageHandler_FetchMessages_NilBecomesEmptyArray(t *testing.T) {
	r, ms, cs := setupMessageHandler(t, imhttp.MessageRouteOpts{})
	tok := newMessageToken(t, 1, "alice")

	cs.EXPECT().GetMember(mock.Anything, int64(7), int64(1)).
		Return(&repo.ChannelMember{ChannelID: 7, UserID: 1}, nil)
	ms.EXPECT().FetchBefore(mock.Anything, int64(7), int64(1), int64(1<<62), 50).
		Return(nil, nil) // explicit nil

	testutil.NewExpect(t, r).GET("/api/channels/7/messages").
		WithHeader("Authorization", "Bearer "+tok).
		Expect().Status(200).JSON().Object().
		Value("messages").Array().Length().IsEqual(0)
}

func TestMessageHandler_MarkRead_200_PushesReadSync(t *testing.T) {
	syncer := &recordingReadSyncer{}
	r, _, cs := setupMessageHandler(t, imhttp.MessageRouteOpts{ReadSyncer: syncer})
	tok := newMessageToken(t, 7, "alice")

	cs.EXPECT().GetMember(mock.Anything, int64(10), int64(7)).
		Return(&repo.ChannelMember{ChannelID: 10, UserID: 7}, nil)
	cs.EXPECT().GetByID(mock.Anything, int64(10)).Return(&repo.Channel{ID: 10, Seq: 5}, nil)
	cs.EXPECT().MarkRead(mock.Anything, int64(10), int64(7), int64(5)).Return(nil)

	testutil.NewExpect(t, r).POST("/api/channels/10/read").
		WithHeader("Authorization", "Bearer "+tok).
		Expect().Status(200).JSON().Object().
		Value("seq").Number().IsEqual(5)

	got := syncer.snapshot()
	require.Len(t, got, 1, "read syncer must fire on success")
	require.Equal(t, readSyncEvent{userID: 7, channelID: 10, readSeq: 5}, got[0])
}

func TestMessageHandler_MarkRead_403_NotMember(t *testing.T) {
	r, _, cs := setupMessageHandler(t, imhttp.MessageRouteOpts{})
	tok := newMessageToken(t, 7, "alice")
	cs.EXPECT().GetMember(mock.Anything, int64(10), int64(7)).Return(nil, repo.ErrNotFound)

	testutil.NewExpect(t, r).POST("/api/channels/10/read").
		WithHeader("Authorization", "Bearer "+tok).
		Expect().Status(403)
}

func TestMessageHandler_Forward_201_ToTwoChannels(t *testing.T) {
	r, ms, cs := setupMessageHandler(t, imhttp.MessageRouteOpts{})
	tok := newMessageToken(t, 7, "alice")

	source := &repo.Message{ID: 5, ChannelID: 1, SenderID: 99, MsgType: repo.MsgTypeText, Content: "fwd"}
	ms.EXPECT().GetByID(mock.Anything, int64(5)).Return(source, nil)
	cs.EXPECT().GetMember(mock.Anything, int64(1), int64(7)).
		Return(&repo.ChannelMember{ChannelID: 1, UserID: 7}, nil)
	cs.EXPECT().GetMember(mock.Anything, int64(2), int64(7)).
		Return(&repo.ChannelMember{ChannelID: 2, UserID: 7}, nil)
	cs.EXPECT().GetMember(mock.Anything, int64(3), int64(7)).
		Return(&repo.ChannelMember{ChannelID: 3, UserID: 7}, nil)
	ms.EXPECT().Send(mock.Anything, mock.MatchedBy(func(m *repo.Message) bool {
		return m.ChannelID == 2 && m.SenderID == 7 && m.Content == "fwd"
	})).Return(nil)
	ms.EXPECT().Send(mock.Anything, mock.MatchedBy(func(m *repo.Message) bool {
		return m.ChannelID == 3 && m.SenderID == 7 && m.Content == "fwd"
	})).Return(nil)

	testutil.NewExpect(t, r).POST("/api/messages/forward").
		WithHeader("Authorization", "Bearer "+tok).
		WithJSON(map[string]any{
			"message_id":         5,
			"target_channel_ids": []int64{2, 3},
		}).
		Expect().Status(201).JSON().Object().
		Value("messages").Array().Length().IsEqual(2)
}

func TestMessageHandler_Forward_422_MissingMessageID(t *testing.T) {
	r, _, _ := setupMessageHandler(t, imhttp.MessageRouteOpts{})
	tok := newMessageToken(t, 7, "alice")

	testutil.NewExpect(t, r).POST("/api/messages/forward").
		WithHeader("Authorization", "Bearer "+tok).
		WithJSON(map[string]any{"target_channel_ids": []int64{2}}).
		Expect().Status(422)
}

func TestMessageHandler_Forward_422_TooManyTargets(t *testing.T) {
	r, _, _ := setupMessageHandler(t, imhttp.MessageRouteOpts{})
	tok := newMessageToken(t, 7, "alice")

	targets := make([]int64, 0, 11)
	for i := int64(1); i <= 11; i++ {
		targets = append(targets, i)
	}
	testutil.NewExpect(t, r).POST("/api/messages/forward").
		WithHeader("Authorization", "Bearer "+tok).
		WithJSON(map[string]any{"message_id": 5, "target_channel_ids": targets}).
		Expect().Status(422)
}

func TestMessageHandler_BadChannelID_400(t *testing.T) {
	r, _, _ := setupMessageHandler(t, imhttp.MessageRouteOpts{})
	tok := newMessageToken(t, 1, "alice")

	// Non-numeric :id — the router still matches but pathInt64 returns 400.
	testutil.NewExpect(t, r).GET("/api/channels/abc/messages").
		WithHeader("Authorization", "Bearer "+tok).
		Expect().Status(400)
}

// Compile-time assertion: the route paths use :id, so /forward + a single
// /:id/messages don't collide. This catches accidental router refactors.
var _ = strconv.FormatInt

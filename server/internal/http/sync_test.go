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

func setupSyncHandler(t *testing.T) (*gin.Engine, *mocks.ChannelRepoMock, *mocks.MessageRepoMock) {
	t.Helper()
	gin.SetMode(gin.TestMode)
	ch := mocks.NewChannelRepoMock(t)
	ms := mocks.NewMessageRepoMock(t)
	svc := service.NewSyncService(ch, ms)
	r := gin.New()
	authed := r.Group("/api")
	authed.Use(middleware.JWTGin(testSecret))
	imhttp.RegisterSyncRoutes(authed, svc, nil)
	return r, ch, ms
}

func newSyncToken(t *testing.T, uid int64, username string) string {
	t.Helper()
	tok, err := auth.GenerateToken(testSecret, uid, username)
	require.NoError(t, err)
	return tok
}

func TestSyncHandler_NoToken_401(t *testing.T) {
	r, _, _ := setupSyncHandler(t)
	testutil.NewExpect(t, r).POST("/api/sync").
		WithJSON(map[string]any{"channels": []any{}}).
		Expect().Status(401)
}

func TestSyncHandler_NoChanges_EmptyChannelsArray(t *testing.T) {
	r, ch, _ := setupSyncHandler(t)
	tok := newSyncToken(t, 42, "alice")
	// Client up-to-date on every server channel → response.channels = [].
	ch.EXPECT().GetMemberChannelSeqs(mock.Anything, int64(42)).
		Return(map[int64]int64{1: 100}, nil)

	testutil.NewExpect(t, r).POST("/api/sync").
		WithHeader("Authorization", "Bearer "+tok).
		WithJSON(map[string]any{
			"channels": []any{map[string]any{"id": 1, "seq": 100}},
		}).
		Expect().Status(200).JSON().Object().
		Value("channels").Array().Length().IsEqual(0)
}

func TestSyncHandler_SmallGap_ReturnsMessages(t *testing.T) {
	r, ch, ms := setupSyncHandler(t)
	tok := newSyncToken(t, 42, "alice")

	ch.EXPECT().GetMemberChannelSeqs(mock.Anything, int64(42)).
		Return(map[int64]int64{7: 105}, nil)
	ch.EXPECT().GetMember(mock.Anything, int64(7), int64(42)).
		Return(&repo.ChannelMember{LastReadSeq: 100}, nil)
	missed := []repo.Message{
		{ID: 101, ChannelID: 7, Seq: 101, Content: "m1"},
		{ID: 102, ChannelID: 7, Seq: 102, Content: "m2"},
		{ID: 103, ChannelID: 7, Seq: 103, Content: "m3"},
		{ID: 104, ChannelID: 7, Seq: 104, Content: "m4"},
		{ID: 105, ChannelID: 7, Seq: 105, Content: "m5"},
	}
	ms.EXPECT().FetchForUser(mock.Anything, int64(7), int64(42), int64(100), service.SyncGapThreshold).
		Return(missed, nil)

	resp := testutil.NewExpect(t, r).POST("/api/sync").
		WithHeader("Authorization", "Bearer "+tok).
		WithJSON(map[string]any{
			"channels": []any{map[string]any{"id": 7, "seq": 100}},
		}).
		Expect().Status(200).JSON().Object()
	chans := resp.Value("channels").Array()
	chans.Length().IsEqual(1)
	first := chans.Value(0).Object()
	first.Value("id").Number().IsEqual(7)
	first.Value("server_seq").Number().IsEqual(105)
	first.Value("unread").Number().IsEqual(5)
	first.Value("messages").Array().Length().IsEqual(5)
	first.NotContainsKey("has_more")
}

func TestSyncHandler_LargeGap_SetsHasMore(t *testing.T) {
	r, ch, ms := setupSyncHandler(t)
	tok := newSyncToken(t, 42, "alice")

	ch.EXPECT().GetMemberChannelSeqs(mock.Anything, int64(42)).
		Return(map[int64]int64{7: 500}, nil)
	ch.EXPECT().GetMember(mock.Anything, int64(7), int64(42)).
		Return(&repo.ChannelMember{LastReadSeq: 0}, nil)

	tail := make([]repo.Message, service.SyncMsgLimit)
	for i := range tail {
		tail[i] = repo.Message{ChannelID: 7, Seq: int64(451 + i)}
	}
	ms.EXPECT().FetchForUser(mock.Anything, int64(7), int64(42),
		int64(500-service.SyncMsgLimit), service.SyncMsgLimit).
		Return(tail, nil)

	resp := testutil.NewExpect(t, r).POST("/api/sync").
		WithHeader("Authorization", "Bearer "+tok).
		WithJSON(map[string]any{
			"channels": []any{map[string]any{"id": 7, "seq": 0}},
		}).
		Expect().Status(200).JSON().Object()
	first := resp.Value("channels").Array().Value(0).Object()
	first.Value("has_more").Boolean().IsTrue()
	first.Value("messages").Array().Length().IsEqual(service.SyncMsgLimit)
}

func TestSyncHandler_BadJSON_400(t *testing.T) {
	r, _, _ := setupSyncHandler(t)
	tok := newSyncToken(t, 42, "alice")
	testutil.NewExpect(t, r).POST("/api/sync").
		WithHeader("Authorization", "Bearer "+tok).
		WithBytes([]byte("{not-json")).
		WithHeader("Content-Type", "application/json").
		Expect().Status(400)
}

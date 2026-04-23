package service_test

import (
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"

	"im-server/internal/repo"
	"im-server/internal/repo/mocks"
	"im-server/internal/service"
)

// newMessageSvc returns a service backed by fresh repo mocks. files is nil by
// default — pass a non-nil mock when a test cares about attachment linkage.
func newMessageSvc(t *testing.T) (*service.MessageService, *mocks.MessageRepoMock, *mocks.ChannelRepoMock) {
	t.Helper()
	ms := mocks.NewMessageRepoMock(t)
	cs := mocks.NewChannelRepoMock(t)
	return service.NewMessageService(ms, cs, nil), ms, cs
}

func TestMessage_SendMessage_HappyPath_AssignsSeq(t *testing.T) {
	svc, ms, cs := newMessageSvc(t)
	cs.EXPECT().GetMember(mock.Anything, int64(7), int64(42)).
		Return(&repo.ChannelMember{ChannelID: 7, UserID: 42}, nil)
	// Repo.Send stamps Seq + ID on the in-place pointer.
	ms.EXPECT().Send(mock.Anything, mock.MatchedBy(func(m *repo.Message) bool {
		return m.ChannelID == 7 && m.SenderID == 42 && m.Content == "hi" &&
			m.MsgType == repo.MsgTypeText && m.ClientMsgID == "uuid-1"
	})).Run(func(_ context.Context, m *repo.Message) {
		m.ID, m.Seq = 100, 5
	}).Return(nil)

	got, err := svc.SendMessage(context.Background(), service.SendParams{
		ChannelID:   7,
		SenderID:    42,
		Content:     "hi",
		ClientMsgID: "uuid-1",
	})
	require.NoError(t, err)
	require.Equal(t, int64(100), got.ID)
	require.Equal(t, int64(5), got.Seq)
}

func TestMessage_SendMessage_NotMember(t *testing.T) {
	svc, _, cs := newMessageSvc(t)
	cs.EXPECT().GetMember(mock.Anything, int64(1), int64(42)).Return(nil, repo.ErrNotFound)

	_, err := svc.SendMessage(context.Background(), service.SendParams{
		ChannelID: 1,
		SenderID:  42,
		Content:   "blocked",
	})
	require.ErrorIs(t, err, service.ErrNotMember)
}

func TestMessage_SendMessage_DefaultsMsgTypeToText(t *testing.T) {
	svc, ms, cs := newMessageSvc(t)
	cs.EXPECT().GetMember(mock.Anything, int64(1), int64(2)).
		Return(&repo.ChannelMember{ChannelID: 1, UserID: 2}, nil)
	ms.EXPECT().Send(mock.Anything, mock.MatchedBy(func(m *repo.Message) bool {
		return m.MsgType == repo.MsgTypeText
	})).Return(nil)

	_, err := svc.SendMessage(context.Background(), service.SendParams{
		ChannelID: 1, SenderID: 2, Content: "x",
	})
	require.NoError(t, err)
}

func TestMessage_SendMessage_AttachesFiles(t *testing.T) {
	ms := mocks.NewMessageRepoMock(t)
	cs := mocks.NewChannelRepoMock(t)
	files := mocks.NewFileRepoMock(t)
	svc := service.NewMessageService(ms, cs, files)

	cs.EXPECT().GetMember(mock.Anything, int64(1), int64(2)).
		Return(&repo.ChannelMember{ChannelID: 1, UserID: 2}, nil)
	ms.EXPECT().Send(mock.Anything, mock.Anything).Run(func(_ context.Context, m *repo.Message) {
		m.ID = 999
	}).Return(nil)
	files.EXPECT().AttachToMessage(mock.Anything, int64(999), int64(11)).Return(nil)
	files.EXPECT().AttachToMessage(mock.Anything, int64(999), int64(12)).Return(nil)

	_, err := svc.SendMessage(context.Background(), service.SendParams{
		ChannelID: 1, SenderID: 2, Content: "x", FileIDs: []int64{11, 12},
	})
	require.NoError(t, err)
}

func TestMessage_FetchMessages_BeforeSeq(t *testing.T) {
	svc, ms, cs := newMessageSvc(t)
	cs.EXPECT().GetMember(mock.Anything, int64(1), int64(2)).
		Return(&repo.ChannelMember{ChannelID: 1, UserID: 2}, nil)
	want := []repo.Message{{ID: 1}, {ID: 2}}
	ms.EXPECT().FetchBefore(mock.Anything, int64(1), int64(2), int64(100), 50).Return(want, nil)

	got, err := svc.FetchMessages(context.Background(), 1, 2, 100, 50)
	require.NoError(t, err)
	require.Equal(t, want, got)
}

func TestMessage_FetchMessages_NotMember(t *testing.T) {
	svc, _, cs := newMessageSvc(t)
	cs.EXPECT().GetMember(mock.Anything, int64(1), int64(2)).Return(nil, repo.ErrNotFound)

	_, err := svc.FetchMessages(context.Background(), 1, 2, 100, 50)
	require.ErrorIs(t, err, service.ErrNotMember)
}

func TestMessage_FetchAfter_NotMember(t *testing.T) {
	svc, _, cs := newMessageSvc(t)
	cs.EXPECT().GetMember(mock.Anything, int64(1), int64(2)).Return(nil, repo.ErrNotFound)

	_, err := svc.FetchAfter(context.Background(), 1, 2, 0, 50)
	require.ErrorIs(t, err, service.ErrNotMember)
}

func TestMessage_FetchAround_NotMember(t *testing.T) {
	svc, _, cs := newMessageSvc(t)
	cs.EXPECT().GetMember(mock.Anything, int64(1), int64(2)).Return(nil, repo.ErrNotFound)

	_, err := svc.FetchAround(context.Background(), 1, 2, 50, 50)
	require.ErrorIs(t, err, service.ErrNotMember)
}

func TestMessage_MarkRead_WritesChannelSeq(t *testing.T) {
	svc, _, cs := newMessageSvc(t)
	cs.EXPECT().GetMember(mock.Anything, int64(10), int64(7)).
		Return(&repo.ChannelMember{ChannelID: 10, UserID: 7}, nil)
	cs.EXPECT().GetByID(mock.Anything, int64(10)).Return(&repo.Channel{ID: 10, Seq: 42}, nil)
	cs.EXPECT().MarkRead(mock.Anything, int64(10), int64(7), int64(42)).Return(nil)

	seq, err := svc.MarkRead(context.Background(), 10, 7)
	require.NoError(t, err)
	require.Equal(t, int64(42), seq)
}

func TestMessage_MarkRead_NotMember(t *testing.T) {
	svc, _, cs := newMessageSvc(t)
	cs.EXPECT().GetMember(mock.Anything, int64(10), int64(7)).Return(nil, repo.ErrNotFound)

	_, err := svc.MarkRead(context.Background(), 10, 7)
	require.ErrorIs(t, err, service.ErrNotMember)
}

func TestMessage_ForwardMessages_ToMultipleChannels(t *testing.T) {
	svc, ms, cs := newMessageSvc(t)
	source := &repo.Message{ID: 5, ChannelID: 1, SenderID: 99, MsgType: repo.MsgTypeText, Content: "fwd-me"}
	ms.EXPECT().GetByID(mock.Anything, int64(5)).Return(source, nil)
	// caller is a member of source channel
	cs.EXPECT().GetMember(mock.Anything, int64(1), int64(7)).
		Return(&repo.ChannelMember{ChannelID: 1, UserID: 7}, nil)
	// + each target channel
	cs.EXPECT().GetMember(mock.Anything, int64(2), int64(7)).
		Return(&repo.ChannelMember{ChannelID: 2, UserID: 7}, nil)
	cs.EXPECT().GetMember(mock.Anything, int64(3), int64(7)).
		Return(&repo.ChannelMember{ChannelID: 3, UserID: 7}, nil)
	// One Send call per target.
	ms.EXPECT().Send(mock.Anything, mock.MatchedBy(func(m *repo.Message) bool {
		return m.ChannelID == 2 && m.ForwardedFrom != nil && *m.ForwardedFrom == 5
	})).Return(nil)
	ms.EXPECT().Send(mock.Anything, mock.MatchedBy(func(m *repo.Message) bool {
		return m.ChannelID == 3 && m.ForwardedFrom != nil && *m.ForwardedFrom == 5
	})).Return(nil)

	got, err := svc.ForwardMessages(context.Background(), 7, service.ForwardParams{
		MessageID:        5,
		TargetChannelIDs: []int64{2, 3},
	})
	require.NoError(t, err)
	require.Len(t, got, 2)
}

func TestMessage_ForwardMessages_SkipsTargetWhenNotMember(t *testing.T) {
	svc, ms, cs := newMessageSvc(t)
	source := &repo.Message{ID: 5, ChannelID: 1, SenderID: 99, MsgType: repo.MsgTypeText, Content: "x"}
	ms.EXPECT().GetByID(mock.Anything, int64(5)).Return(source, nil)
	cs.EXPECT().GetMember(mock.Anything, int64(1), int64(7)).
		Return(&repo.ChannelMember{ChannelID: 1, UserID: 7}, nil)
	cs.EXPECT().GetMember(mock.Anything, int64(2), int64(7)).Return(nil, repo.ErrNotFound)
	cs.EXPECT().GetMember(mock.Anything, int64(3), int64(7)).
		Return(&repo.ChannelMember{ChannelID: 3, UserID: 7}, nil)
	ms.EXPECT().Send(mock.Anything, mock.MatchedBy(func(m *repo.Message) bool {
		return m.ChannelID == 3
	})).Return(nil)

	got, err := svc.ForwardMessages(context.Background(), 7, service.ForwardParams{
		MessageID:        5,
		TargetChannelIDs: []int64{2, 3},
	})
	require.NoError(t, err)
	require.Len(t, got, 1, "channel 2 should be silently skipped")
	require.Equal(t, int64(3), got[0].ChannelID)
}

func TestMessage_ForwardMessages_SourceNotFound(t *testing.T) {
	svc, ms, _ := newMessageSvc(t)
	ms.EXPECT().GetByID(mock.Anything, int64(404)).Return(nil, repo.ErrNotFound)

	_, err := svc.ForwardMessages(context.Background(), 7, service.ForwardParams{
		MessageID:        404,
		TargetChannelIDs: []int64{2},
	})
	require.ErrorIs(t, err, service.ErrSourceNotFound)
}

func TestMessage_ForwardMessages_SourceChannelNotMember(t *testing.T) {
	svc, ms, cs := newMessageSvc(t)
	source := &repo.Message{ID: 5, ChannelID: 1, SenderID: 99}
	ms.EXPECT().GetByID(mock.Anything, int64(5)).Return(source, nil)
	cs.EXPECT().GetMember(mock.Anything, int64(1), int64(7)).Return(nil, repo.ErrNotFound)

	_, err := svc.ForwardMessages(context.Background(), 7, service.ForwardParams{
		MessageID:        5,
		TargetChannelIDs: []int64{2},
	})
	require.ErrorIs(t, err, service.ErrSourceNotMember)
}

func TestMessage_SendMessage_RepoError(t *testing.T) {
	svc, ms, cs := newMessageSvc(t)
	cs.EXPECT().GetMember(mock.Anything, int64(1), int64(2)).
		Return(&repo.ChannelMember{ChannelID: 1, UserID: 2}, nil)
	boom := errors.New("db down")
	ms.EXPECT().Send(mock.Anything, mock.Anything).Return(boom)

	_, err := svc.SendMessage(context.Background(), service.SendParams{
		ChannelID: 1, SenderID: 2, Content: "x",
	})
	require.Error(t, err)
}

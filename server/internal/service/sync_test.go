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

// newSyncSvc returns a service backed by fresh repo mocks. Returning the
// mocks lets each test pin only the calls it cares about — extra calls fail
// loudly so we don't hide regressions.
func newSyncSvc(t *testing.T) (*service.SyncService, *mocks.ChannelRepoMock, *mocks.MessageRepoMock) {
	t.Helper()
	ch := mocks.NewChannelRepoMock(t)
	ms := mocks.NewMessageRepoMock(t)
	return service.NewSyncService(ch, ms), ch, ms
}

func TestSync_NoChanges_EmptyResult(t *testing.T) {
	svc, ch, _ := newSyncSvc(t)
	// Server reports two channels, client is up-to-date on both → no per-channel
	// fetch required, no per-channel result.
	ch.EXPECT().GetMemberChannelSeqs(mock.Anything, int64(42)).
		Return(map[int64]int64{1: 100, 2: 200}, nil)

	got, err := svc.Sync(context.Background(), 42, service.SyncParams{
		Cursors: []service.SyncCursor{{ID: 1, Seq: 100}, {ID: 2, Seq: 200}},
	})
	require.NoError(t, err)
	require.Empty(t, got.Channels)
}

func TestSync_SmallGap_ReturnsIncrementalMessages(t *testing.T) {
	svc, ch, ms := newSyncSvc(t)
	ch.EXPECT().GetMemberChannelSeqs(mock.Anything, int64(42)).
		Return(map[int64]int64{7: 105}, nil)
	ch.EXPECT().GetMember(mock.Anything, int64(7), int64(42)).
		Return(&repo.ChannelMember{LastReadSeq: 100}, nil)

	missed := []repo.Message{
		{ChannelID: 7, Seq: 101},
		{ChannelID: 7, Seq: 102},
		{ChannelID: 7, Seq: 103},
		{ChannelID: 7, Seq: 104},
		{ChannelID: 7, Seq: 105},
	}
	// Small gap: fetch all messages after client's cursor up to the threshold.
	ms.EXPECT().FetchForUser(mock.Anything, int64(7), int64(42), int64(100), service.SyncGapThreshold).
		Return(missed, nil)

	got, err := svc.Sync(context.Background(), 42, service.SyncParams{
		Cursors: []service.SyncCursor{{ID: 7, Seq: 100}},
	})
	require.NoError(t, err)
	require.Len(t, got.Channels, 1)
	d := got.Channels[0]
	require.Equal(t, int64(7), d.ID)
	require.Equal(t, int64(105), d.ServerSeq)
	require.Equal(t, int64(5), d.Unread)
	require.False(t, d.HasMore, "small gap must not set has_more")
	require.Len(t, d.Messages, 5)
}

func TestSync_LargeGap_SetsHasMoreAndFastForwards(t *testing.T) {
	svc, ch, ms := newSyncSvc(t)
	ch.EXPECT().GetMemberChannelSeqs(mock.Anything, int64(42)).
		Return(map[int64]int64{7: 500}, nil)
	ch.EXPECT().GetMember(mock.Anything, int64(7), int64(42)).
		Return(&repo.ChannelMember{LastReadSeq: 0}, nil)

	// Large gap (500 > threshold). Service must request the *last* SyncMsgLimit
	// messages by passing afterSeq = serverSeq - SyncMsgLimit.
	wantAfter := int64(500 - service.SyncMsgLimit)
	tail := make([]repo.Message, service.SyncMsgLimit)
	for i := range tail {
		tail[i] = repo.Message{ChannelID: 7, Seq: int64(451 + i)}
	}
	ms.EXPECT().FetchForUser(mock.Anything, int64(7), int64(42), wantAfter, service.SyncMsgLimit).
		Return(tail, nil)

	got, err := svc.Sync(context.Background(), 42, service.SyncParams{
		Cursors: []service.SyncCursor{{ID: 7, Seq: 0}},
	})
	require.NoError(t, err)
	require.Len(t, got.Channels, 1)
	d := got.Channels[0]
	require.True(t, d.HasMore, "large gap must set has_more=true")
	require.Len(t, d.Messages, service.SyncMsgLimit)
}

func TestSync_NewChannel_ReturnsLatestMessages(t *testing.T) {
	svc, ch, ms := newSyncSvc(t)
	// Server knows about channel 99 the client doesn't.
	ch.EXPECT().GetMemberChannelSeqs(mock.Anything, int64(42)).
		Return(map[int64]int64{99: 10}, nil)
	ch.EXPECT().GetMember(mock.Anything, int64(99), int64(42)).
		Return(&repo.ChannelMember{LastReadSeq: 0}, nil)

	// New-channel path: afterSeq floors at 0 when serverSeq < SyncMsgLimit.
	msgs := make([]repo.Message, 10)
	for i := range msgs {
		msgs[i] = repo.Message{ChannelID: 99, Seq: int64(i + 1)}
	}
	ms.EXPECT().FetchForUser(mock.Anything, int64(99), int64(42), int64(0), service.SyncMsgLimit).
		Return(msgs, nil)

	got, err := svc.Sync(context.Background(), 42, service.SyncParams{
		Cursors: []service.SyncCursor{},
	})
	require.NoError(t, err)
	require.Len(t, got.Channels, 1)
	d := got.Channels[0]
	require.Equal(t, int64(99), d.ID)
	require.Len(t, d.Messages, 10)
	// All channel history fits in the limit → no has_more.
	require.False(t, d.HasMore)
}

func TestSync_EmptyServerState_ReturnsNoDeltas(t *testing.T) {
	svc, ch, _ := newSyncSvc(t)
	// User belongs to no channels — empty result regardless of client cursors.
	ch.EXPECT().GetMemberChannelSeqs(mock.Anything, int64(42)).
		Return(map[int64]int64{}, nil)

	got, err := svc.Sync(context.Background(), 42, service.SyncParams{
		Cursors: []service.SyncCursor{{ID: 1, Seq: 50}},
	})
	require.NoError(t, err)
	require.Empty(t, got.Channels)
}

func TestSync_GetSeqsError_PropagatesWrapped(t *testing.T) {
	svc, ch, _ := newSyncSvc(t)
	boom := errors.New("db down")
	ch.EXPECT().GetMemberChannelSeqs(mock.Anything, int64(42)).Return(nil, boom)

	_, err := svc.Sync(context.Background(), 42, service.SyncParams{})
	require.ErrorIs(t, err, boom)
}

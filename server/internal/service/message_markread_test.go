package service

import (
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"

	"im-server/internal/repo"
	"im-server/internal/repo/mocks"
)

// TestMessageService_MarkRead_LegacyPath asserts that when no ChannelEventRepo
// is attached, MarkRead falls back to the legacy MarkRead method on
// ChannelRepo and does NOT touch WithinTx / MarkReadTx / event repo. This
// preserves the behaviour pre-C017 for tests that don't exercise sync.
func TestMessageService_MarkRead_LegacyPath(t *testing.T) {
	const channelID = "01J0MARKREAD0CH00000000000"
	const callerID = "01J0MARKREAD0USR0000000000"

	chRepo := mocks.NewChannelRepoMock(t)
	msgRepo := mocks.NewMessageRepoMock(t)
	fileRepo := mocks.NewFileRepoMock(t)

	chRepo.On("GetMember", mock.Anything, channelID, callerID).
		Return(&repo.ChannelMember{UserID: callerID, Role: repo.MemberRoleMember}, nil).Once()
	chRepo.On("GetByID", mock.Anything, channelID).
		Return(&repo.Channel{ID: channelID, Seq: 42}, nil).Once()
	chRepo.On("MarkRead", mock.Anything, channelID, callerID, int64(42)).
		Return(nil).Once()

	svc := NewMessageService(msgRepo, chRepo, fileRepo)
	// AttachChannelEventRepo intentionally NOT called — legacy path.

	seq, err := svc.MarkRead(context.Background(), channelID, callerID)
	require.NoError(t, err)
	require.Equal(t, int64(42), seq)
}

// TestMessageService_MarkRead_ModernPath_AppendsEvent asserts that when a
// ChannelEventRepo is attached, MarkRead opens a tx via WithinTx, calls
// MarkReadTx, then allocates an event_seq and appends a single
// EventTypeReadMark row — all in the same transaction.
func TestMessageService_MarkRead_ModernPath_AppendsEvent(t *testing.T) {
	const channelID = "01J0MARKREAD0CH00000000000"
	const callerID = "01J0MARKREAD0USR0000000000"

	chRepo := mocks.NewChannelRepoMock(t)
	msgRepo := mocks.NewMessageRepoMock(t)
	fileRepo := mocks.NewFileRepoMock(t)
	evtRepo := mocks.NewChannelEventRepoMock(t)

	chRepo.On("GetMember", mock.Anything, channelID, callerID).
		Return(&repo.ChannelMember{UserID: callerID, Role: repo.MemberRoleMember}, nil).Once()
	chRepo.On("GetByID", mock.Anything, channelID).
		Return(&repo.Channel{ID: channelID, Seq: 7}, nil).Once()

	// WithinTx invokes its fn with a (nil) tx handle for unit-test purposes;
	// the inner MarkReadTx + NextEventSeq + AppendEvent are all mocked so we
	// don't actually touch a DB.
	chRepo.On("WithinTx", mock.Anything, mock.AnythingOfType("func(*gorm.DB) error")).
		Run(func(args mock.Arguments) {
			fn := args.Get(1).(func(*gorm.DB) error)
			require.NoError(t, fn(nil))
		}).Return(nil).Once()
	chRepo.On("MarkReadTx", mock.Anything, (*gorm.DB)(nil), channelID, callerID, int64(7)).
		Return(int64(1), nil).Once()
	evtRepo.On("NextEventSeq", mock.Anything, (*gorm.DB)(nil), channelID).
		Return(int64(100), nil).Once()
	evtRepo.On("AppendEvent", mock.Anything, (*gorm.DB)(nil), mock.MatchedBy(func(e *repo.ChannelEvent) bool {
		return e != nil &&
			e.ChannelID == channelID &&
			e.EventSeq == 100 &&
			e.EventType == repo.EventTypeReadMark &&
			e.ActorID == callerID &&
			e.MsgID == nil
	})).Return(nil).Once()

	svc := NewMessageService(msgRepo, chRepo, fileRepo)
	svc.AttachChannelEventRepo(evtRepo)

	seq, err := svc.MarkRead(context.Background(), channelID, callerID)
	require.NoError(t, err)
	require.Equal(t, int64(7), seq)
}

// TestMessageService_MarkRead_ModernPath_NoOpSkipsEvent asserts that when
// MarkReadTx reports 0 rows affected (caller is not a member or seq already
// past), MarkRead does NOT append a channel_event row. A phantom event
// would mislead sync into reporting a non-existent cursor advance.
func TestMessageService_MarkRead_ModernPath_NoOpSkipsEvent(t *testing.T) {
	const channelID = "01J0MARKREAD0CH00000000000"
	const callerID = "01J0MARKREAD0USR0000000000"

	chRepo := mocks.NewChannelRepoMock(t)
	msgRepo := mocks.NewMessageRepoMock(t)
	fileRepo := mocks.NewFileRepoMock(t)
	evtRepo := mocks.NewChannelEventRepoMock(t)

	chRepo.On("GetMember", mock.Anything, channelID, callerID).
		Return(&repo.ChannelMember{UserID: callerID, Role: repo.MemberRoleMember}, nil).Once()
	chRepo.On("GetByID", mock.Anything, channelID).
		Return(&repo.Channel{ID: channelID, Seq: 5}, nil).Once()
	chRepo.On("WithinTx", mock.Anything, mock.AnythingOfType("func(*gorm.DB) error")).
		Run(func(args mock.Arguments) {
			fn := args.Get(1).(func(*gorm.DB) error)
			require.NoError(t, fn(nil))
		}).Return(nil).Once()
	chRepo.On("MarkReadTx", mock.Anything, (*gorm.DB)(nil), channelID, callerID, int64(5)).
		Return(int64(0), nil).Once()
	// NextEventSeq / AppendEvent must NOT be called — mockery would fail the
	// test on unexpected calls.

	svc := NewMessageService(msgRepo, chRepo, fileRepo)
	svc.AttachChannelEventRepo(evtRepo)

	seq, err := svc.MarkRead(context.Background(), channelID, callerID)
	require.NoError(t, err)
	require.Equal(t, int64(5), seq)
}

// TestMessageService_MarkRead_ModernPath_AppendError asserts that an error
// from AppendEvent propagates as the MarkRead error so callers know the
// read cursor was rolled back (atomic guarantee — the channel_members
// UPDATE must NOT survive a failed event append).
func TestMessageService_MarkRead_ModernPath_AppendError(t *testing.T) {
	const channelID = "01J0MARKREAD0CH00000000000"
	const callerID = "01J0MARKREAD0USR0000000000"

	chRepo := mocks.NewChannelRepoMock(t)
	msgRepo := mocks.NewMessageRepoMock(t)
	fileRepo := mocks.NewFileRepoMock(t)
	evtRepo := mocks.NewChannelEventRepoMock(t)

	appendErr := errors.New("boom")

	chRepo.On("GetMember", mock.Anything, channelID, callerID).
		Return(&repo.ChannelMember{UserID: callerID, Role: repo.MemberRoleMember}, nil).Once()
	chRepo.On("GetByID", mock.Anything, channelID).
		Return(&repo.Channel{ID: channelID, Seq: 9}, nil).Once()
	chRepo.On("WithinTx", mock.Anything, mock.AnythingOfType("func(*gorm.DB) error")).
		Run(func(args mock.Arguments) {
			fn := args.Get(1).(func(*gorm.DB) error)
			// The fn must return the wrapped error so callers see rollback.
			require.Error(t, fn(nil))
		}).Return(appendErr).Once()
	chRepo.On("MarkReadTx", mock.Anything, (*gorm.DB)(nil), channelID, callerID, int64(9)).
		Return(int64(1), nil).Once()
	evtRepo.On("NextEventSeq", mock.Anything, (*gorm.DB)(nil), channelID).
		Return(int64(50), nil).Once()
	evtRepo.On("AppendEvent", mock.Anything, (*gorm.DB)(nil), mock.AnythingOfType("*repo.ChannelEvent")).
		Return(appendErr).Once()

	svc := NewMessageService(msgRepo, chRepo, fileRepo)
	svc.AttachChannelEventRepo(evtRepo)

	_, err := svc.MarkRead(context.Background(), channelID, callerID)
	require.ErrorIs(t, err, appendErr,
		"AppendEvent failure must propagate as the MarkRead error so callers see rollback")
}

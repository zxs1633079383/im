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

// P2-followup unit tests — ChannelService.CreateGroup / CreateOrGetDM must
// drive ChannelEventRepo.CreateChannelSequences inside the channel-creation
// tx when AttachChannelEventRepo is wired (production path). Without this
// the very first push_msg against the new channel fails with
// `relation "channel_msg_seq_<uuid>" does not exist` (C018 §3.2 / P3
// NEED_FOLLOWUP).
//
// The legacy non-tx path (channelEvent == nil) is exercised by the existing
// TestChannelService_CreateOrGetDM cases in channel_member_test.go — those
// still pass after this refactor because the new code branches on nil.

const (
	tCreatorID = "01J0CREATOR000000000000000"
	tPeerID    = "01J0PEER000000000000000000"
	tMemberID1 = "01J0MEMBER1000000000000000"
)

// withinTxRunAndReturn drives the closure exactly once and returns its
// error to the caller — mirrors gormChannelRepo.WithinTx in production.
// Use this with mock.RunAndReturn (NOT Run + Return) to avoid double
// closure invocation (Run executes the closure but Return discards its
// result; combining the two would call inner mocks twice).
func withinTxRunAndReturn(_ context.Context, fn func(*gorm.DB) error) error {
	return fn(nil)
}

func TestChannelService_CreateGroup_WithChannelEvent_HappyPath(t *testing.T) {
	chanMock := mocks.NewChannelRepoMock(t)
	evtMock := mocks.NewChannelEventRepoMock(t)

	chanMock.On("WithinTx", mock.Anything, mock.AnythingOfType("func(*gorm.DB) error")).
		Return(withinTxRunAndReturn).Once()
	chanMock.On("CreateTx", mock.Anything, mock.Anything, mock.AnythingOfType("*repo.Channel")).
		Run(func(args mock.Arguments) {
			ch := args.Get(2).(*repo.Channel)
			ch.ID = "01J0GROUP000000000000000000"
		}).Return(nil).Once()
	// CreateChannelSequences must be invoked inside the same tx, with the
	// post-INSERT channel id — this is the gap P3 NEED_FOLLOWUP flagged.
	evtMock.On("CreateChannelSequences", mock.Anything, mock.Anything, "01J0GROUP000000000000000000").
		Return(nil).Once()
	// Owner is added first (role=Owner) then each non-creator member as
	// MemberRoleMember inside the same tx via AddMemberTx.
	chanMock.On("AddMemberTx", mock.Anything, mock.Anything, "01J0GROUP000000000000000000", tCreatorID, repo.MemberRoleOwner).
		Return(nil).Once()
	chanMock.On("AddMemberTx", mock.Anything, mock.Anything, "01J0GROUP000000000000000000", tMemberID1, repo.MemberRoleMember).
		Return(nil).Once()

	svc := NewChannelService(chanMock, nil) // messages == nil → postSys short-circuits
	svc.AttachChannelEventRepo(evtMock)

	ch, added, err := svc.CreateGroup(context.Background(), tCreatorID, "team-x", "g1", []string{tMemberID1})
	require.NoError(t, err)
	require.NotNil(t, ch)
	require.Equal(t, "01J0GROUP000000000000000000", ch.ID)
	require.Len(t, added, 1)
	require.Equal(t, tMemberID1, added[0].UserID)
}

func TestChannelService_CreateGroup_WithChannelEvent_SequenceFailRollsBack(t *testing.T) {
	chanMock := mocks.NewChannelRepoMock(t)
	evtMock := mocks.NewChannelEventRepoMock(t)

	// WithinTx must surface whatever the closure returns to the caller so a
	// real PG tx would roll back the channel row when CreateChannelSequences
	// fails. Drive the closure exactly once via RunAndReturn — using Run + a
	// stub Return would invoke the closure twice (Run executes it AND
	// Return discards its result), which double-counts the inner mocks.
	chanMock.On("WithinTx", mock.Anything, mock.AnythingOfType("func(*gorm.DB) error")).
		Return(withinTxRunAndReturn).Once()
	chanMock.On("CreateTx", mock.Anything, mock.Anything, mock.AnythingOfType("*repo.Channel")).
		Run(func(args mock.Arguments) {
			ch := args.Get(2).(*repo.Channel)
			ch.ID = "01J0GROUP000000000000000001"
		}).Return(nil).Once()
	evtMock.On("CreateChannelSequences", mock.Anything, mock.Anything, "01J0GROUP000000000000000001").
		Return(errors.New("pg sequence blew up")).Once()
	// AddMemberTx must NOT be invoked because the closure aborts at
	// CreateChannelSequences — mockery .Once() enforces zero calls implicitly.

	svc := NewChannelService(chanMock, nil)
	svc.AttachChannelEventRepo(evtMock)

	_, _, err := svc.CreateGroup(context.Background(), tCreatorID, "team-x", "g1", []string{tMemberID1})
	require.Error(t, err)
	require.Contains(t, err.Error(), "create group sequences")
	require.Contains(t, err.Error(), "pg sequence blew up")
}

func TestChannelService_CreateOrGetDM_WithChannelEvent_HappyPath(t *testing.T) {
	chanMock := mocks.NewChannelRepoMock(t)
	evtMock := mocks.NewChannelEventRepoMock(t)

	// 1. FindDM returns NotFound → goes down the create path.
	chanMock.On("FindDM", mock.Anything, tCreatorID, tPeerID).
		Return((*repo.Channel)(nil), repo.ErrNotFound).Once()
	// 2. WithinTx wraps create + sequences + both AddMemberTx.
	chanMock.On("WithinTx", mock.Anything, mock.AnythingOfType("func(*gorm.DB) error")).
		Return(withinTxRunAndReturn).Once()
	chanMock.On("CreateTx", mock.Anything, mock.Anything, mock.AnythingOfType("*repo.Channel")).
		Run(func(args mock.Arguments) {
			ch := args.Get(2).(*repo.Channel)
			ch.ID = "01J0DM00000000000000000000"
		}).Return(nil).Once()
	evtMock.On("CreateChannelSequences", mock.Anything, mock.Anything, "01J0DM00000000000000000000").
		Return(nil).Once()
	chanMock.On("AddMemberTx", mock.Anything, mock.Anything, "01J0DM00000000000000000000", tCreatorID, repo.MemberRoleMember).
		Return(nil).Once()
	chanMock.On("AddMemberTx", mock.Anything, mock.Anything, "01J0DM00000000000000000000", tPeerID, repo.MemberRoleMember).
		Return(nil).Once()

	svc := NewChannelService(chanMock, nil)
	svc.AttachChannelEventRepo(evtMock)

	ch, isNew, err := svc.CreateOrGetDM(context.Background(), tCreatorID, tPeerID, "team-x")
	require.NoError(t, err)
	require.True(t, isNew)
	require.NotNil(t, ch)
	require.Equal(t, "01J0DM00000000000000000000", ch.ID)
}

func TestChannelService_CreateOrGetDM_WithChannelEvent_ExistingDM(t *testing.T) {
	chanMock := mocks.NewChannelRepoMock(t)
	evtMock := mocks.NewChannelEventRepoMock(t)

	// FindDM hits → CreateChannelSequences MUST NOT fire (existing channel
	// already has its sequences from the original creation tx). Mockery
	// .NewChannelEventRepoMock(t) auto-fails on unexpected calls.
	chanMock.On("FindDM", mock.Anything, tCreatorID, tPeerID).
		Return(&repo.Channel{ID: "01J0DM00EXISTING0000000000", Type: repo.ChannelTypeDM}, nil).Once()

	svc := NewChannelService(chanMock, nil)
	svc.AttachChannelEventRepo(evtMock)

	ch, isNew, err := svc.CreateOrGetDM(context.Background(), tCreatorID, tPeerID, "team-x")
	require.NoError(t, err)
	require.False(t, isNew)
	require.Equal(t, "01J0DM00EXISTING0000000000", ch.ID)
}

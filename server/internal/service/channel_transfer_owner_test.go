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

// TestChannelService_TransferOwner is the C014 §3.3 unit-test sweep for the
// C013 endpoint. The 8 cases below exercise every branch BEFORE the gorm
// transaction in TransferOwner / runTransferOwnerTx, plus one happy-path case
// that mocks WithinTx + the four tx-aware repo writes. messages is left nil
// so postSys short-circuits (no MessageRepo mock needed).
func TestChannelService_TransferOwner(t *testing.T) {
	const (
		channelID  = "01J0CHANNEL000000000000000"
		callerID   = "01J0CALLER0000000000000000"
		newOwnerID = "01J0NEWOWNER00000000000000"
	)

	groupCh := func() *repo.Channel {
		return &repo.Channel{ID: channelID, Type: repo.ChannelTypeGroup}
	}
	dmCh := func() *repo.Channel {
		return &repo.Channel{ID: channelID, Type: repo.ChannelTypeDM}
	}
	closedCh := func() *repo.Channel {
		now := groupCh().CreatedAt
		ch := groupCh()
		ch.DeletedAt = &now
		return ch
	}

	cases := []struct {
		name      string
		params    TransferOwnerParams
		setupRepo func(*mocks.ChannelRepoMock)
		wantErr   error
	}{
		{
			name:   "transfer_to_self",
			params: TransferOwnerParams{ChannelID: channelID, CallerID: callerID, NewOwnerID: callerID},
			setupRepo: func(_ *mocks.ChannelRepoMock) {
				// short-circuit before any repo call
			},
			wantErr: ErrTransferToSelf,
		},
		{
			name:   "caller_not_member",
			params: TransferOwnerParams{ChannelID: channelID, CallerID: callerID, NewOwnerID: newOwnerID},
			setupRepo: func(m *mocks.ChannelRepoMock) {
				m.On("GetMember", mock.Anything, channelID, callerID).
					Return((*repo.ChannelMember)(nil), repo.ErrNotFound).Once()
			},
			wantErr: ErrNotMember,
		},
		{
			name:   "caller_not_owner",
			params: TransferOwnerParams{ChannelID: channelID, CallerID: callerID, NewOwnerID: newOwnerID},
			setupRepo: func(m *mocks.ChannelRepoMock) {
				m.On("GetMember", mock.Anything, channelID, callerID).
					Return(&repo.ChannelMember{UserID: callerID, Role: repo.MemberRoleMember}, nil).Once()
			},
			wantErr: ErrOwnerOnly,
		},
		{
			name:   "channel_not_found",
			params: TransferOwnerParams{ChannelID: channelID, CallerID: callerID, NewOwnerID: newOwnerID},
			setupRepo: func(m *mocks.ChannelRepoMock) {
				m.On("GetMember", mock.Anything, channelID, callerID).
					Return(&repo.ChannelMember{UserID: callerID, Role: repo.MemberRoleOwner}, nil).Once()
				m.On("GetByID", mock.Anything, channelID).
					Return((*repo.Channel)(nil), repo.ErrNotFound).Once()
			},
			wantErr: repo.ErrNotFound,
		},
		{
			name:   "is_dm",
			params: TransferOwnerParams{ChannelID: channelID, CallerID: callerID, NewOwnerID: newOwnerID},
			setupRepo: func(m *mocks.ChannelRepoMock) {
				m.On("GetMember", mock.Anything, channelID, callerID).
					Return(&repo.ChannelMember{UserID: callerID, Role: repo.MemberRoleOwner}, nil).Once()
				m.On("GetByID", mock.Anything, channelID).
					Return(dmCh(), nil).Once()
			},
			wantErr: ErrDMNoOwner,
		},
		{
			name:   "channel_gone",
			params: TransferOwnerParams{ChannelID: channelID, CallerID: callerID, NewOwnerID: newOwnerID},
			setupRepo: func(m *mocks.ChannelRepoMock) {
				m.On("GetMember", mock.Anything, channelID, callerID).
					Return(&repo.ChannelMember{UserID: callerID, Role: repo.MemberRoleOwner}, nil).Once()
				m.On("GetByID", mock.Anything, channelID).
					Return(closedCh(), nil).Once()
			},
			wantErr: repo.ErrGone,
		},
		{
			name:   "new_owner_not_member",
			params: TransferOwnerParams{ChannelID: channelID, CallerID: callerID, NewOwnerID: newOwnerID},
			setupRepo: func(m *mocks.ChannelRepoMock) {
				m.On("GetMember", mock.Anything, channelID, callerID).
					Return(&repo.ChannelMember{UserID: callerID, Role: repo.MemberRoleOwner}, nil).Once()
				m.On("GetByID", mock.Anything, channelID).
					Return(groupCh(), nil).Once()
				m.On("GetMember", mock.Anything, channelID, newOwnerID).
					Return((*repo.ChannelMember)(nil), repo.ErrNotFound).Once()
			},
			wantErr: ErrTargetNotMember,
		},
		{
			name:   "happy_no_leave",
			params: TransferOwnerParams{ChannelID: channelID, CallerID: callerID, NewOwnerID: newOwnerID, AlsoLeave: false},
			setupRepo: func(m *mocks.ChannelRepoMock) {
				m.On("GetMember", mock.Anything, channelID, callerID).
					Return(&repo.ChannelMember{UserID: callerID, Role: repo.MemberRoleOwner}, nil).Once()
				m.On("GetByID", mock.Anything, channelID).
					Return(groupCh(), nil).Once()
				m.On("GetMember", mock.Anything, channelID, newOwnerID).
					Return(&repo.ChannelMember{UserID: newOwnerID, Role: repo.MemberRoleMember}, nil).Once()
				// WithinTx: run fn(nil) directly so the inner tx-aware mock
				// methods get invoked.
				m.On("WithinTx", mock.Anything, mock.Anything).Run(func(args mock.Arguments) {
					fn := args.Get(1).(func(*gorm.DB) error)
					_ = fn(nil)
				}).Return(nil).Once()
				m.On("SetMemberRoleTx", mock.Anything, (*gorm.DB)(nil), channelID, callerID, repo.MemberRoleMember).
					Return(nil).Once()
				m.On("SetMemberRoleTx", mock.Anything, (*gorm.DB)(nil), channelID, newOwnerID, repo.MemberRoleOwner).
					Return(nil).Once()
				m.On("SetCreatorTx", mock.Anything, (*gorm.DB)(nil), channelID, newOwnerID).
					Return(nil).Once()
				// Post-commit reads (refresh + ListMembers).
				m.On("GetByID", mock.Anything, channelID).
					Return(groupCh(), nil).Once()
				m.On("ListMembers", mock.Anything, channelID).
					Return([]repo.ChannelMember{
						{UserID: callerID, Role: repo.MemberRoleMember},
						{UserID: newOwnerID, Role: repo.MemberRoleOwner},
					}, nil).Once()
			},
			wantErr: nil,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			repoMock := mocks.NewChannelRepoMock(t)
			tc.setupRepo(repoMock)

			svc := NewChannelService(repoMock, nil)
			res, err := svc.TransferOwner(context.Background(), tc.params)

			if tc.wantErr != nil {
				require.Error(t, err)
				require.True(t, errors.Is(err, tc.wantErr),
					"want errors.Is == %v, got %v", tc.wantErr, err)
				require.Nil(t, res)
			} else {
				require.NoError(t, err)
				require.NotNil(t, res)
				require.Equal(t, callerID, res.OldOwnerID)
				require.Equal(t, newOwnerID, res.NewOwnerID)
				require.Equal(t, channelID, res.Channel.ID)
				require.Len(t, res.Members, 2)
			}
		})
	}
}

package service

import (
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"

	"im-server/internal/repo"
	"im-server/internal/repo/mocks"
)

// C014 §3.3 sweep for ChannelService membership methods. messages is left nil
// so postSys short-circuits; broadcaster is left nil so fanMemberUpdate no-ops.

const (
	tChannelID = "01J0CHAN0MEMBER0000000000000"
	tCallerID  = "01J0CALLER0MEMBER0000000000"
	tTargetID  = "01J0TARGET0MEMBER0000000000"
)

func TestChannelService_CreateOrGetDM(t *testing.T) {
	cases := []struct {
		name      string
		caller    string
		other     string
		teamID    string
		setupRepo func(*mocks.ChannelRepoMock)
		wantErr   error
		wantNew   bool
	}{
		{
			name:      "self_dm_rejected",
			caller:    tCallerID,
			other:     tCallerID,
			setupRepo: func(_ *mocks.ChannelRepoMock) {},
			wantErr:   ErrSelfDM,
		},
		{
			name:   "existing_dm_returned",
			caller: tCallerID,
			other:  tTargetID,
			setupRepo: func(m *mocks.ChannelRepoMock) {
				m.On("FindDM", mock.Anything, tCallerID, tTargetID).
					Return(&repo.Channel{ID: "01J0DM0EXISTING000000000000", Type: repo.ChannelTypeDM}, nil).Once()
			},
			wantNew: false,
		},
		{
			name:   "find_dm_unexpected_error",
			caller: tCallerID,
			other:  tTargetID,
			setupRepo: func(m *mocks.ChannelRepoMock) {
				m.On("FindDM", mock.Anything, tCallerID, tTargetID).
					Return((*repo.Channel)(nil), errors.New("db boom")).Once()
			},
			wantErr: errors.New("find dm: db boom"),
		},
		{
			name:   "create_dm_success",
			caller: tCallerID,
			other:  tTargetID,
			teamID: "team-x",
			setupRepo: func(m *mocks.ChannelRepoMock) {
				m.On("FindDM", mock.Anything, tCallerID, tTargetID).
					Return((*repo.Channel)(nil), repo.ErrNotFound).Once()
				m.On("Create", mock.Anything, mock.AnythingOfType("*repo.Channel")).
					Run(func(args mock.Arguments) {
						ch := args.Get(1).(*repo.Channel)
						ch.ID = "01J0DM0NEW00000000000000000"
					}).
					Return(nil).Once()
				m.On("AddMember", mock.Anything, "01J0DM0NEW00000000000000000", tCallerID, repo.MemberRoleMember).
					Return(nil).Once()
				m.On("AddMember", mock.Anything, "01J0DM0NEW00000000000000000", tTargetID, repo.MemberRoleMember).
					Return(nil).Once()
			},
			wantNew: true,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			repoMock := mocks.NewChannelRepoMock(t)
			tc.setupRepo(repoMock)
			svc := NewChannelService(repoMock, nil)

			ch, isNew, err := svc.CreateOrGetDM(context.Background(), tc.caller, tc.other, tc.teamID)
			if tc.wantErr != nil {
				require.Error(t, err)
				// Some errors are wrapped; match by string when sentinel is
				// not exported.
				if errors.Is(tc.wantErr, ErrSelfDM) {
					require.ErrorIs(t, err, ErrSelfDM)
				} else {
					require.Contains(t, err.Error(), tc.wantErr.Error())
				}
				require.Nil(t, ch)
				return
			}
			require.NoError(t, err)
			require.NotNil(t, ch)
			require.Equal(t, tc.wantNew, isNew)
		})
	}
}

func TestChannelService_AddMember(t *testing.T) {
	cases := []struct {
		name      string
		setupRepo func(*mocks.ChannelRepoMock)
		wantErr   error
	}{
		{
			name: "caller_not_member",
			setupRepo: func(m *mocks.ChannelRepoMock) {
				m.On("GetMember", mock.Anything, tChannelID, tCallerID).
					Return((*repo.ChannelMember)(nil), repo.ErrNotFound).Once()
			},
			wantErr: ErrNotMember,
		},
		{
			name: "caller_plain_member_forbidden",
			setupRepo: func(m *mocks.ChannelRepoMock) {
				m.On("GetMember", mock.Anything, tChannelID, tCallerID).
					Return(&repo.ChannelMember{UserID: tCallerID, Role: repo.MemberRoleMember}, nil).Once()
			},
			wantErr: ErrForbidden,
		},
		{
			name: "happy_admin_caller",
			setupRepo: func(m *mocks.ChannelRepoMock) {
				m.On("GetMember", mock.Anything, tChannelID, tCallerID).
					Return(&repo.ChannelMember{UserID: tCallerID, Role: repo.MemberRoleAdmin}, nil).Once()
				m.On("AddMember", mock.Anything, tChannelID, tTargetID, repo.MemberRoleMember).
					Return(nil).Once()
				m.On("GetByID", mock.Anything, tChannelID).
					Return(&repo.Channel{ID: tChannelID, Name: "team-x", Type: repo.ChannelTypeGroup}, nil).Once()
			},
			wantErr: nil,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			repoMock := mocks.NewChannelRepoMock(t)
			tc.setupRepo(repoMock)
			svc := NewChannelService(repoMock, nil)

			name, err := svc.AddMember(context.Background(), tChannelID, tCallerID, tTargetID)
			if tc.wantErr != nil {
				require.ErrorIs(t, err, tc.wantErr)
				require.Empty(t, name)
				return
			}
			require.NoError(t, err)
			require.Equal(t, "team-x", name)
		})
	}
}

func TestChannelService_RemoveMember(t *testing.T) {
	cases := []struct {
		name      string
		setupRepo func(*mocks.ChannelRepoMock)
		wantErr   error
	}{
		{
			name: "caller_not_admin",
			setupRepo: func(m *mocks.ChannelRepoMock) {
				m.On("GetMember", mock.Anything, tChannelID, tCallerID).
					Return(&repo.ChannelMember{UserID: tCallerID, Role: repo.MemberRoleMember}, nil).Once()
			},
			wantErr: ErrForbidden,
		},
		{
			name: "target_not_found",
			setupRepo: func(m *mocks.ChannelRepoMock) {
				m.On("GetMember", mock.Anything, tChannelID, tCallerID).
					Return(&repo.ChannelMember{UserID: tCallerID, Role: repo.MemberRoleOwner}, nil).Once()
				m.On("GetMember", mock.Anything, tChannelID, tTargetID).
					Return((*repo.ChannelMember)(nil), repo.ErrNotFound).Once()
			},
			wantErr: repo.ErrNotFound,
		},
		{
			name: "cannot_remove_owner",
			setupRepo: func(m *mocks.ChannelRepoMock) {
				m.On("GetMember", mock.Anything, tChannelID, tCallerID).
					Return(&repo.ChannelMember{UserID: tCallerID, Role: repo.MemberRoleOwner}, nil).Once()
				m.On("GetMember", mock.Anything, tChannelID, tTargetID).
					Return(&repo.ChannelMember{UserID: tTargetID, Role: repo.MemberRoleOwner}, nil).Once()
			},
			wantErr: ErrCannotRemoveOwner,
		},
		{
			name: "happy_owner_removes_member",
			setupRepo: func(m *mocks.ChannelRepoMock) {
				m.On("GetMember", mock.Anything, tChannelID, tCallerID).
					Return(&repo.ChannelMember{UserID: tCallerID, Role: repo.MemberRoleOwner}, nil).Once()
				m.On("GetMember", mock.Anything, tChannelID, tTargetID).
					Return(&repo.ChannelMember{UserID: tTargetID, Role: repo.MemberRoleMember}, nil).Once()
				m.On("RemoveMember", mock.Anything, tChannelID, tTargetID).
					Return(nil).Once()
			},
			wantErr: nil,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			repoMock := mocks.NewChannelRepoMock(t)
			tc.setupRepo(repoMock)
			svc := NewChannelService(repoMock, nil)

			err := svc.RemoveMember(context.Background(), tChannelID, tCallerID, tTargetID)
			if tc.wantErr != nil {
				require.ErrorIs(t, err, tc.wantErr)
				return
			}
			require.NoError(t, err)
		})
	}
}

func TestChannelService_LeaveChannel(t *testing.T) {
	cases := []struct {
		name      string
		setupRepo func(*mocks.ChannelRepoMock)
		wantErr   error
	}{
		{
			name: "caller_not_member",
			setupRepo: func(m *mocks.ChannelRepoMock) {
				m.On("GetMember", mock.Anything, tChannelID, tCallerID).
					Return((*repo.ChannelMember)(nil), repo.ErrNotFound).Once()
			},
			wantErr: ErrNotMember,
		},
		{
			name: "owner_cannot_leave",
			setupRepo: func(m *mocks.ChannelRepoMock) {
				m.On("GetMember", mock.Anything, tChannelID, tCallerID).
					Return(&repo.ChannelMember{UserID: tCallerID, Role: repo.MemberRoleOwner}, nil).Once()
			},
			wantErr: ErrOwnerCannotLeave,
		},
		{
			name: "happy_member_leaves",
			setupRepo: func(m *mocks.ChannelRepoMock) {
				m.On("GetMember", mock.Anything, tChannelID, tCallerID).
					Return(&repo.ChannelMember{UserID: tCallerID, Role: repo.MemberRoleMember}, nil).Once()
				m.On("RemoveMember", mock.Anything, tChannelID, tCallerID).
					Return(nil).Once()
			},
			wantErr: nil,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			repoMock := mocks.NewChannelRepoMock(t)
			tc.setupRepo(repoMock)
			svc := NewChannelService(repoMock, nil)

			err := svc.LeaveChannel(context.Background(), tChannelID, tCallerID)
			if tc.wantErr != nil {
				require.ErrorIs(t, err, tc.wantErr)
				return
			}
			require.NoError(t, err)
		})
	}
}

func TestChannelService_ListMembers(t *testing.T) {
	t.Run("caller_not_member", func(t *testing.T) {
		repoMock := mocks.NewChannelRepoMock(t)
		repoMock.On("GetMember", mock.Anything, tChannelID, tCallerID).
			Return((*repo.ChannelMember)(nil), repo.ErrNotFound).Once()

		svc := NewChannelService(repoMock, nil)
		got, err := svc.ListMembers(context.Background(), tChannelID, tCallerID)
		require.ErrorIs(t, err, ErrNotMember)
		require.Nil(t, got)
	})

	t.Run("happy", func(t *testing.T) {
		repoMock := mocks.NewChannelRepoMock(t)
		repoMock.On("GetMember", mock.Anything, tChannelID, tCallerID).
			Return(&repo.ChannelMember{UserID: tCallerID, Role: repo.MemberRoleMember}, nil).Once()
		repoMock.On("ListMembers", mock.Anything, tChannelID).
			Return([]repo.ChannelMember{
				{UserID: tCallerID, Role: repo.MemberRoleMember},
				{UserID: tTargetID, Role: repo.MemberRoleAdmin},
			}, nil).Once()

		svc := NewChannelService(repoMock, nil)
		got, err := svc.ListMembers(context.Background(), tChannelID, tCallerID)
		require.NoError(t, err)
		require.Len(t, got, 2)
		require.Equal(t, tCallerID, got[0].UserID)
		require.Equal(t, tTargetID, got[1].UserID)
	})
}

func TestChannelService_GetByID(t *testing.T) {
	t.Run("not_member", func(t *testing.T) {
		repoMock := mocks.NewChannelRepoMock(t)
		repoMock.On("GetMember", mock.Anything, tChannelID, tCallerID).
			Return((*repo.ChannelMember)(nil), repo.ErrNotFound).Once()
		svc := NewChannelService(repoMock, nil)
		_, err := svc.GetByID(context.Background(), tChannelID, tCallerID)
		require.ErrorIs(t, err, ErrNotMember)
	})
	t.Run("happy", func(t *testing.T) {
		repoMock := mocks.NewChannelRepoMock(t)
		repoMock.On("GetMember", mock.Anything, tChannelID, tCallerID).
			Return(&repo.ChannelMember{UserID: tCallerID, Role: repo.MemberRoleMember}, nil).Once()
		repoMock.On("GetByID", mock.Anything, tChannelID).
			Return(&repo.Channel{ID: tChannelID, Name: "foo"}, nil).Once()
		svc := NewChannelService(repoMock, nil)
		ch, err := svc.GetByID(context.Background(), tChannelID, tCallerID)
		require.NoError(t, err)
		require.Equal(t, "foo", ch.Name)
	})
}

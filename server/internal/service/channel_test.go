package service_test

import (
	"context"
	"testing"

	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"

	"im-server/internal/repo"
	"im-server/internal/repo/mocks"
	"im-server/internal/service"
)

// newChannelSvc returns a service backed by fresh repo mocks. Returning the
// mocks lets each test pin only the calls it cares about — extra calls fail
// loudly so we don't hide regressions.
func newChannelSvc(t *testing.T) (*service.ChannelService, *mocks.ChannelRepoMock, *mocks.UserRepoMock) {
	t.Helper()
	ch := mocks.NewChannelRepoMock(t)
	us := mocks.NewUserRepoMock(t)
	return service.NewChannelService(ch, us, nil), ch, us
}

func TestChannel_CreateGroup_AddsCallerAsOwner(t *testing.T) {
	svc, ch, _ := newChannelSvc(t)

	// Create stamps the channel ID — emulate the GORM behaviour.
	ch.EXPECT().Create(mock.Anything, mock.MatchedBy(func(c *repo.Channel) bool {
		return c.Type == repo.ChannelTypeGroup && c.Name == "team" && c.CreatorID != nil && *c.CreatorID == 1
	})).Run(func(_ context.Context, c *repo.Channel) {
		c.ID = 99
	}).Return(nil)
	ch.EXPECT().AddMember(mock.Anything, int64(99), int64(1), repo.MemberRoleOwner).Return(nil)
	ch.EXPECT().AddMember(mock.Anything, int64(99), int64(2), repo.MemberRoleMember).Return(nil)
	ch.EXPECT().AddMember(mock.Anything, int64(99), int64(3), repo.MemberRoleMember).Return(nil)

	got, added, err := svc.CreateGroup(context.Background(), 1, "team", []int64{1, 2, 3})
	require.NoError(t, err)
	require.Equal(t, int64(99), got.ID)
	// The creator must be filtered out of the returned added list — only the
	// post-fan-out push targets are returned.
	require.Equal(t, []service.AddedMember{{UserID: 2}, {UserID: 3}}, added)
}

func TestChannel_CreateOrGetDM_ReturnsExistingWhenFound(t *testing.T) {
	svc, ch, _ := newChannelSvc(t)
	ch.EXPECT().FindDM(mock.Anything, int64(1), int64(2)).
		Return(&repo.Channel{ID: 7, Type: repo.ChannelTypeDM}, nil)

	got, created, err := svc.CreateOrGetDM(context.Background(), 1, 2)
	require.NoError(t, err)
	require.False(t, created, "existing DM must not be re-created")
	require.Equal(t, int64(7), got.ID)
}

func TestChannel_CreateOrGetDM_CreatesWhenMissing(t *testing.T) {
	svc, ch, _ := newChannelSvc(t)
	ch.EXPECT().FindDM(mock.Anything, int64(1), int64(2)).Return(nil, repo.ErrNotFound)
	ch.EXPECT().Create(mock.Anything, mock.MatchedBy(func(c *repo.Channel) bool {
		return c.Type == repo.ChannelTypeDM
	})).Run(func(_ context.Context, c *repo.Channel) {
		c.ID = 11
	}).Return(nil)
	ch.EXPECT().AddMember(mock.Anything, int64(11), int64(1), repo.MemberRoleMember).Return(nil)
	ch.EXPECT().AddMember(mock.Anything, int64(11), int64(2), repo.MemberRoleMember).Return(nil)

	got, created, err := svc.CreateOrGetDM(context.Background(), 1, 2)
	require.NoError(t, err)
	require.True(t, created)
	require.Equal(t, int64(11), got.ID)
}

func TestChannel_CreateOrGetDM_SelfRejected(t *testing.T) {
	svc, _, _ := newChannelSvc(t)
	_, _, err := svc.CreateOrGetDM(context.Background(), 1, 1)
	require.ErrorIs(t, err, service.ErrSelfDM)
}

func TestChannel_GetByID_NonMemberForbidden(t *testing.T) {
	svc, ch, _ := newChannelSvc(t)
	ch.EXPECT().GetMember(mock.Anything, int64(5), int64(2)).Return(nil, repo.ErrNotFound)

	_, err := svc.GetByID(context.Background(), 5, 2)
	require.ErrorIs(t, err, service.ErrNotMember)
}

func TestChannel_Update_RequiresAdminOrOwner(t *testing.T) {
	svc, ch, _ := newChannelSvc(t)
	ch.EXPECT().GetMember(mock.Anything, int64(5), int64(2)).
		Return(&repo.ChannelMember{ChannelID: 5, UserID: 2, Role: repo.MemberRoleMember}, nil)

	_, err := svc.Update(context.Background(), 5, 2, "x", "")
	require.ErrorIs(t, err, service.ErrForbidden)
}

func TestChannel_Update_AdminOK(t *testing.T) {
	svc, ch, _ := newChannelSvc(t)
	ch.EXPECT().GetMember(mock.Anything, int64(5), int64(2)).
		Return(&repo.ChannelMember{ChannelID: 5, UserID: 2, Role: repo.MemberRoleAdmin}, nil)
	ch.EXPECT().Update(mock.Anything, int64(5), "new-name", "").Return(nil)
	ch.EXPECT().GetByID(mock.Anything, int64(5)).
		Return(&repo.Channel{ID: 5, Name: "new-name"}, nil)

	got, err := svc.Update(context.Background(), 5, 2, "new-name", "")
	require.NoError(t, err)
	require.Equal(t, "new-name", got.Name)
}

func TestChannel_RemoveMember_OwnerProtected(t *testing.T) {
	svc, ch, _ := newChannelSvc(t)
	// Caller is admin.
	ch.EXPECT().GetMember(mock.Anything, int64(5), int64(2)).
		Return(&repo.ChannelMember{ChannelID: 5, UserID: 2, Role: repo.MemberRoleAdmin}, nil)
	// Target is the owner — must be rejected.
	ch.EXPECT().GetMember(mock.Anything, int64(5), int64(1)).
		Return(&repo.ChannelMember{ChannelID: 5, UserID: 1, Role: repo.MemberRoleOwner}, nil)

	err := svc.RemoveMember(context.Background(), 5, 2, 1)
	require.ErrorIs(t, err, service.ErrCannotRemoveOwner)
}

func TestChannel_LeaveChannel_OwnerBlocked(t *testing.T) {
	svc, ch, _ := newChannelSvc(t)
	ch.EXPECT().GetMember(mock.Anything, int64(5), int64(1)).
		Return(&repo.ChannelMember{ChannelID: 5, UserID: 1, Role: repo.MemberRoleOwner}, nil)

	err := svc.LeaveChannel(context.Background(), 5, 1)
	require.ErrorIs(t, err, service.ErrOwnerCannotLeave)
}

func TestChannel_LeaveChannel_MemberOK(t *testing.T) {
	svc, ch, _ := newChannelSvc(t)
	ch.EXPECT().GetMember(mock.Anything, int64(5), int64(2)).
		Return(&repo.ChannelMember{ChannelID: 5, UserID: 2, Role: repo.MemberRoleMember}, nil)
	ch.EXPECT().RemoveMember(mock.Anything, int64(5), int64(2)).Return(nil)

	require.NoError(t, svc.LeaveChannel(context.Background(), 5, 2))
}

func TestChannel_ListMembers_EnrichesUsers(t *testing.T) {
	svc, ch, us := newChannelSvc(t)
	ch.EXPECT().GetMember(mock.Anything, int64(5), int64(1)).
		Return(&repo.ChannelMember{ChannelID: 5, UserID: 1, Role: repo.MemberRoleOwner}, nil)
	ch.EXPECT().ListMembers(mock.Anything, int64(5)).
		Return([]repo.ChannelMember{
			{ChannelID: 5, UserID: 1, Role: repo.MemberRoleOwner},
			{ChannelID: 5, UserID: 2, Role: repo.MemberRoleMember},
		}, nil)
	us.EXPECT().GetByID(mock.Anything, int64(1)).
		Return(&repo.User{ID: 1, Username: "alice", DisplayName: "Alice"}, nil)
	us.EXPECT().GetByID(mock.Anything, int64(2)).
		Return(&repo.User{ID: 2, Username: "bob", DisplayName: "Bob"}, nil)

	got, err := svc.ListMembers(context.Background(), 5, 1)
	require.NoError(t, err)
	require.Len(t, got, 2)
	require.Equal(t, "alice", got[0].Username)
	require.Equal(t, "Bob", got[1].DisplayName)
}

func TestChannel_AddMember_RequiresAdmin(t *testing.T) {
	svc, ch, _ := newChannelSvc(t)
	ch.EXPECT().GetMember(mock.Anything, int64(5), int64(2)).
		Return(&repo.ChannelMember{ChannelID: 5, UserID: 2, Role: repo.MemberRoleMember}, nil)

	name, err := svc.AddMember(context.Background(), 5, 2, 9)
	require.ErrorIs(t, err, service.ErrForbidden)
	require.Empty(t, name, "non-admin must not leak channel name")
}

func TestChannel_AddMember_ReturnsChannelName(t *testing.T) {
	svc, ch, _ := newChannelSvc(t)
	ch.EXPECT().GetMember(mock.Anything, int64(5), int64(1)).
		Return(&repo.ChannelMember{ChannelID: 5, UserID: 1, Role: repo.MemberRoleOwner}, nil)
	ch.EXPECT().AddMember(mock.Anything, int64(5), int64(9), repo.MemberRoleMember).
		Return(nil)
	// Post-insert lookup powers the channel_event "added" payload.
	ch.EXPECT().GetByID(mock.Anything, int64(5)).
		Return(&repo.Channel{ID: 5, Name: "team"}, nil)

	name, err := svc.AddMember(context.Background(), 5, 1, 9)
	require.NoError(t, err)
	require.Equal(t, "team", name)
}

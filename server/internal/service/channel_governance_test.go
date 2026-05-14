package service

import (
	"context"
	"testing"

	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"

	"im-server/internal/repo"
	"im-server/internal/repo/mocks"
)

// C014 §3.3 — ChannelGovernanceService method sweep. Per user指示，
// 新写测试只覆盖 1 happy path 即可（断言主语义，不再扩 N error case）。

const govChannelID = "01J0GOV0CHANNEL00000000000"
const govCallerID = "01J0GOV0CALLER00000000000"
const govTargetID = "01J0GOV0TARGET00000000000"

func newGovService(t *testing.T) (
	*ChannelGovernanceService,
	*mocks.ChannelRepoMock,
	*mocks.ChannelGovernanceRepoMock,
) {
	t.Helper()
	chRepo := mocks.NewChannelRepoMock(t)
	govRepo := mocks.NewChannelGovernanceRepoMock(t)
	return NewChannelGovernanceService(chRepo, govRepo), chRepo, govRepo
}

// ownerMember returns the channels.GetMember stub for a happy-path owner caller.
func stubOwnerCaller(chRepo *mocks.ChannelRepoMock) {
	chRepo.On("GetMember", mock.Anything, govChannelID, govCallerID).
		Return(&repo.ChannelMember{UserID: govCallerID, Role: repo.MemberRoleOwner}, nil).Once()
}

func TestChannelGovernanceService_PatchChannel_Happy(t *testing.T) {
	svc, chRepo, govRepo := newGovService(t)
	stubOwnerCaller(chRepo)
	newName := "newName"
	govRepo.On("PatchChannel", mock.Anything, govChannelID, mock.AnythingOfType("repo.PatchChannelFields")).
		Return(nil).Once()
	chRepo.On("GetByID", mock.Anything, govChannelID).
		Return(&repo.Channel{ID: govChannelID, Name: newName}, nil).Once()

	got, err := svc.PatchChannel(context.Background(), govChannelID, govCallerID,
		PatchChannelFields{Name: &newName})
	require.NoError(t, err)
	require.Equal(t, newName, got.Name)
}

func TestChannelGovernanceService_AddManager_Happy(t *testing.T) {
	svc, chRepo, govRepo := newGovService(t)
	stubOwnerCaller(chRepo)
	chRepo.On("GetMember", mock.Anything, govChannelID, govTargetID).
		Return(&repo.ChannelMember{UserID: govTargetID, Role: repo.MemberRoleMember}, nil).Once()
	govRepo.On("AddManager", mock.Anything, govChannelID, govTargetID, govCallerID).
		Return(nil).Once()

	err := svc.AddManager(context.Background(), govChannelID, govCallerID, govTargetID)
	require.NoError(t, err)
}

func TestChannelGovernanceService_RemoveManager_Happy(t *testing.T) {
	svc, chRepo, govRepo := newGovService(t)
	stubOwnerCaller(chRepo)
	govRepo.On("RemoveManager", mock.Anything, govChannelID, govTargetID).Return(nil).Once()

	err := svc.RemoveManager(context.Background(), govChannelID, govCallerID, govTargetID)
	require.NoError(t, err)
}

func TestChannelGovernanceService_ListManagers_Happy(t *testing.T) {
	svc, chRepo, govRepo := newGovService(t)
	chRepo.On("GetMember", mock.Anything, govChannelID, govCallerID).
		Return(&repo.ChannelMember{UserID: govCallerID, Role: repo.MemberRoleMember}, nil).Once()
	govRepo.On("ListManagers", mock.Anything, govChannelID).
		Return([]string{govTargetID}, nil).Once()

	got, err := svc.ListManagers(context.Background(), govChannelID, govCallerID)
	require.NoError(t, err)
	require.Equal(t, []string{govTargetID}, got)
}

func TestChannelGovernanceService_PinMessage_Happy(t *testing.T) {
	svc, chRepo, govRepo := newGovService(t)
	stubOwnerCaller(chRepo)
	govRepo.On("PinMessage", mock.Anything, govChannelID, "msg-1", govCallerID).Return(nil).Once()

	err := svc.PinMessage(context.Background(), govChannelID, govCallerID, "msg-1")
	require.NoError(t, err)
}

func TestChannelGovernanceService_UnpinMessage_Happy(t *testing.T) {
	svc, chRepo, govRepo := newGovService(t)
	stubOwnerCaller(chRepo)
	govRepo.On("UnpinMessage", mock.Anything, govChannelID, "msg-1").Return(nil).Once()

	err := svc.UnpinMessage(context.Background(), govChannelID, govCallerID, "msg-1")
	require.NoError(t, err)
}

func TestChannelGovernanceService_ListPins_Happy(t *testing.T) {
	svc, chRepo, govRepo := newGovService(t)
	chRepo.On("GetMember", mock.Anything, govChannelID, govCallerID).
		Return(&repo.ChannelMember{UserID: govCallerID, Role: repo.MemberRoleMember}, nil).Once()
	govRepo.On("ListPins", mock.Anything, govChannelID).
		Return([]string{"msg-1", "msg-2"}, nil).Once()

	got, err := svc.ListPins(context.Background(), govChannelID, govCallerID)
	require.NoError(t, err)
	require.Equal(t, []string{"msg-1", "msg-2"}, got)
}

func TestChannelGovernanceService_UpdateMemberRole_Happy(t *testing.T) {
	svc, chRepo, govRepo := newGovService(t)
	stubOwnerCaller(chRepo)
	govRepo.On("UpdateMemberRole", mock.Anything, govChannelID, govTargetID, repo.MemberRoleAdmin).
		Return(nil).Once()

	err := svc.UpdateMemberRole(context.Background(), govChannelID, govCallerID, govTargetID, repo.MemberRoleAdmin)
	require.NoError(t, err)
}

func TestChannelGovernanceService_UpdateMemberNotifyPref_Happy(t *testing.T) {
	svc, chRepo, govRepo := newGovService(t)
	chRepo.On("GetMember", mock.Anything, govChannelID, govCallerID).
		Return(&repo.ChannelMember{UserID: govCallerID, Role: repo.MemberRoleMember}, nil).Once()
	govRepo.On("UpdateMemberNotifyPref", mock.Anything, govChannelID, govCallerID, repo.NotifyPrefMentions).
		Return(nil).Once()

	err := svc.UpdateMemberNotifyPref(context.Background(), govChannelID, govCallerID, repo.NotifyPrefMentions)
	require.NoError(t, err)
}

func TestChannelGovernanceService_UpdateMemberIsTop_Happy(t *testing.T) {
	svc, chRepo, govRepo := newGovService(t)
	chRepo.On("GetMember", mock.Anything, govChannelID, govCallerID).
		Return(&repo.ChannelMember{UserID: govCallerID, Role: repo.MemberRoleMember}, nil).Once()
	govRepo.On("UpdateMemberIsTop", mock.Anything, govChannelID, govCallerID, true).Return(nil).Once()

	err := svc.UpdateMemberIsTop(context.Background(), govChannelID, govCallerID, true)
	require.NoError(t, err)
}

func TestChannelGovernanceService_IsManagerOrOwner_Happy(t *testing.T) {
	svc, chRepo, _ := newGovService(t)
	chRepo.On("GetMember", mock.Anything, govChannelID, govCallerID).
		Return(&repo.ChannelMember{UserID: govCallerID, Role: repo.MemberRoleOwner}, nil).Once()

	ok, err := svc.IsManagerOrOwner(context.Background(), govChannelID, govCallerID)
	require.NoError(t, err)
	require.True(t, ok)
}

package service_test

import (
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"

	"im-server/internal/repo"
	"im-server/internal/service"
)

func TestCreateTopic_RequiresCallerMembership(t *testing.T) {
	svc, ch, _ := newChannelSvc(t)
	ch.EXPECT().GetMember(mock.Anything, int64(100), int64(9)).
		Return(nil, repo.ErrNotFound)

	_, err := svc.CreateTopic(context.Background(), service.CreateTopicRequest{
		CallerID: 9, ParentID: 100, RootMessageID: 555, Name: "t",
	})
	require.ErrorIs(t, err, service.ErrNotMember)
}

func TestCreateTopic_RejectsNonParentMembers(t *testing.T) {
	svc, ch, _ := newChannelSvc(t)
	ch.EXPECT().GetMember(mock.Anything, int64(100), int64(1)).
		Return(&repo.ChannelMember{UserID: 1, ChannelID: 100}, nil)
	// Parent has only 1 and 2; caller asks for 3 which is not a member.
	ch.EXPECT().ListMembers(mock.Anything, int64(100)).
		Return([]repo.ChannelMember{
			{UserID: 1, ChannelID: 100},
			{UserID: 2, ChannelID: 100},
		}, nil)

	_, err := svc.CreateTopic(context.Background(), service.CreateTopicRequest{
		CallerID:      1,
		ParentID:      100,
		RootMessageID: 555,
		Name:          "t",
		MemberIDs:     []int64{2, 3},
	})
	require.Error(t, err)
	require.Contains(t, err.Error(), "not a member of parent channel")
}

func TestCreateTopic_DelegatesToRepoWhenAuthorized(t *testing.T) {
	svc, ch, _ := newChannelSvc(t)
	ch.EXPECT().GetMember(mock.Anything, int64(100), int64(1)).
		Return(&repo.ChannelMember{UserID: 1, ChannelID: 100}, nil)
	ch.EXPECT().ListMembers(mock.Anything, int64(100)).
		Return([]repo.ChannelMember{
			{UserID: 1, ChannelID: 100},
			{UserID: 2, ChannelID: 100},
			{UserID: 3, ChannelID: 100},
		}, nil)
	ch.EXPECT().CreateTopic(mock.Anything,
		mock.MatchedBy(func(p repo.CreateTopicParams) bool {
			return p.ParentID == 100 && p.RootMessageID == 555 &&
				p.Name == "design" && p.CreatorID == 1 &&
				len(p.MemberIDs) == 2
		}),
	).Return(&repo.Channel{ID: 777, Name: "design"}, nil)

	got, err := svc.CreateTopic(context.Background(), service.CreateTopicRequest{
		CallerID:      1,
		ParentID:      100,
		RootMessageID: 555,
		Name:          "design",
		MemberIDs:     []int64{2, 3},
	})
	require.NoError(t, err)
	require.Equal(t, int64(777), got.ID)
}

func TestListTopics_RequiresCallerMembership(t *testing.T) {
	svc, ch, _ := newChannelSvc(t)
	ch.EXPECT().GetMember(mock.Anything, int64(100), int64(9)).
		Return(nil, repo.ErrNotFound)

	_, err := svc.ListTopics(context.Background(), 9, 100)
	require.ErrorIs(t, err, service.ErrNotMember)
}

func TestListTopics_ReturnsRepoOutput(t *testing.T) {
	svc, ch, _ := newChannelSvc(t)
	ch.EXPECT().GetMember(mock.Anything, int64(100), int64(1)).
		Return(&repo.ChannelMember{UserID: 1, ChannelID: 100}, nil)
	parent := int64(100)
	ch.EXPECT().ListTopics(mock.Anything, int64(100)).
		Return([]repo.Channel{{ID: 1, RootID: &parent}, {ID: 2, RootID: &parent}}, nil)

	got, err := svc.ListTopics(context.Background(), 1, 100)
	require.NoError(t, err)
	require.Len(t, got, 2)
}

func TestListTopics_PropagatesRepoError(t *testing.T) {
	svc, ch, _ := newChannelSvc(t)
	ch.EXPECT().GetMember(mock.Anything, int64(100), int64(1)).
		Return(&repo.ChannelMember{UserID: 1, ChannelID: 100}, nil)
	boom := errors.New("db down")
	ch.EXPECT().ListTopics(mock.Anything, int64(100)).Return(nil, boom)

	_, err := svc.ListTopics(context.Background(), 1, 100)
	require.ErrorIs(t, err, boom)
}

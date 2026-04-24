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

// fakeRouting is a tiny stand-in for *repo.Routing. Tests control which
// userIDs appear online by pre-populating the devices map.
type fakeRouting struct {
	devices map[int64]map[string]string
	err     error
}

func (f *fakeRouting) DevicesForUser(_ context.Context, userID int64) (map[string]string, error) {
	if f.err != nil {
		return nil, f.err
	}
	return f.devices[userID], nil
}

func newPresenceSvc(t *testing.T, r *fakeRouting) (*service.PresenceService, *mocks.ChannelRepoMock) {
	t.Helper()
	ch := mocks.NewChannelRepoMock(t)
	return service.NewPresenceService(ch, r), ch
}

func TestPresence_RequiresMembership(t *testing.T) {
	svc, ch := newPresenceSvc(t, &fakeRouting{})
	ch.EXPECT().GetMember(mock.Anything, int64(10), int64(7)).
		Return(nil, repo.ErrNotFound)

	_, err := svc.OnlineUsersInChannel(context.Background(), 10, 7)
	require.ErrorIs(t, err, service.ErrNotMember)
}

func TestPresence_ReturnsSubsetMarkedOnline(t *testing.T) {
	r := &fakeRouting{devices: map[int64]map[string]string{
		1: {"dev1": "gw-a"},
		3: {"dev7": "gw-b", "dev9": "gw-c"},
		// 2 has no entry → offline
	}}
	svc, ch := newPresenceSvc(t, r)
	ch.EXPECT().GetMember(mock.Anything, int64(10), int64(1)).
		Return(&repo.ChannelMember{UserID: 1, ChannelID: 10}, nil)
	ch.EXPECT().ListMembers(mock.Anything, int64(10)).Return([]repo.ChannelMember{
		{UserID: 1}, {UserID: 2}, {UserID: 3},
	}, nil)

	got, err := svc.OnlineUsersInChannel(context.Background(), 10, 1)
	require.NoError(t, err)
	require.ElementsMatch(t, []int64{1, 3}, got)
}

func TestPresence_EmptyMembersEmptyOnline(t *testing.T) {
	svc, ch := newPresenceSvc(t, &fakeRouting{})
	ch.EXPECT().GetMember(mock.Anything, int64(10), int64(1)).
		Return(&repo.ChannelMember{UserID: 1, ChannelID: 10}, nil)
	ch.EXPECT().ListMembers(mock.Anything, int64(10)).Return([]repo.ChannelMember{}, nil)

	got, err := svc.OnlineUsersInChannel(context.Background(), 10, 1)
	require.NoError(t, err)
	require.Empty(t, got)
}

func TestPresence_RoutingErrorTreatedAsOffline(t *testing.T) {
	// DevicesForUser returns error for every userID → no one counts as online.
	r := &fakeRouting{err: errors.New("redis down")}
	svc, ch := newPresenceSvc(t, r)
	ch.EXPECT().GetMember(mock.Anything, int64(10), int64(1)).
		Return(&repo.ChannelMember{UserID: 1, ChannelID: 10}, nil)
	ch.EXPECT().ListMembers(mock.Anything, int64(10)).Return([]repo.ChannelMember{
		{UserID: 1}, {UserID: 2},
	}, nil)

	got, err := svc.OnlineUsersInChannel(context.Background(), 10, 1)
	require.NoError(t, err, "presence should not fail the whole request on transient Redis errors")
	require.Empty(t, got)
}

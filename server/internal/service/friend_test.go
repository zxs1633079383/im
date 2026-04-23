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

func TestFriend_SendRequest_Success(t *testing.T) {
	fs := mocks.NewFriendshipRepoMock(t)
	us := mocks.NewUserRepoMock(t)
	fs.EXPECT().SendRequest(mock.Anything, int64(1), int64(2)).Return(nil)

	svc := service.NewFriendService(fs, us)
	require.NoError(t, svc.SendRequest(context.Background(), 1, 2))
}

func TestFriend_SendRequest_AlreadyExists(t *testing.T) {
	fs := mocks.NewFriendshipRepoMock(t)
	us := mocks.NewUserRepoMock(t)
	// The repo surfaces the driver error string verbatim — service must
	// translate the "duplicate" keyword into ErrAlreadyExists.
	fs.EXPECT().SendRequest(mock.Anything, int64(1), int64(2)).
		Return(errors.New("ERROR: duplicate key value violates unique constraint (SQLSTATE 23505)"))

	svc := service.NewFriendService(fs, us)
	err := svc.SendRequest(context.Background(), 1, 2)
	require.ErrorIs(t, err, service.ErrAlreadyExists)
}

func TestFriend_SendRequest_RepoError(t *testing.T) {
	fs := mocks.NewFriendshipRepoMock(t)
	us := mocks.NewUserRepoMock(t)
	boom := errors.New("db down")
	fs.EXPECT().SendRequest(mock.Anything, int64(1), int64(2)).Return(boom)

	svc := service.NewFriendService(fs, us)
	err := svc.SendRequest(context.Background(), 1, 2)
	require.ErrorIs(t, err, boom)
	require.NotErrorIs(t, err, service.ErrAlreadyExists)
}

func TestFriend_AcceptRequest_NotFound(t *testing.T) {
	fs := mocks.NewFriendshipRepoMock(t)
	us := mocks.NewUserRepoMock(t)
	fs.EXPECT().AcceptRequest(mock.Anything, int64(7), int64(99)).Return(int64(0), repo.ErrNotFound)

	svc := service.NewFriendService(fs, us)
	_, err := svc.AcceptRequest(context.Background(), 7, 99)
	require.ErrorIs(t, err, repo.ErrNotFound)
}

func TestFriend_AcceptRequest_ReturnsRequesterID(t *testing.T) {
	fs := mocks.NewFriendshipRepoMock(t)
	us := mocks.NewUserRepoMock(t)
	// The repo returns the original requester's id so the transport layer
	// can fan the real-time push back to them.
	fs.EXPECT().AcceptRequest(mock.Anything, int64(7), int64(2)).Return(int64(99), nil)

	svc := service.NewFriendService(fs, us)
	requesterID, err := svc.AcceptRequest(context.Background(), 7, 2)
	require.NoError(t, err)
	require.Equal(t, int64(99), requesterID)
}

func TestFriend_RejectRequest_OK(t *testing.T) {
	fs := mocks.NewFriendshipRepoMock(t)
	us := mocks.NewUserRepoMock(t)
	fs.EXPECT().RejectRequest(mock.Anything, int64(7), int64(2)).Return(int64(99), nil)

	svc := service.NewFriendService(fs, us)
	requesterID, err := svc.RejectRequest(context.Background(), 7, 2)
	require.NoError(t, err)
	require.Equal(t, int64(99), requesterID)
}

func TestFriend_ListFriends_OK(t *testing.T) {
	fs := mocks.NewFriendshipRepoMock(t)
	us := mocks.NewUserRepoMock(t)
	want := []repo.User{{ID: 2, Username: "bob"}, {ID: 3, Username: "carol"}}
	fs.EXPECT().ListFriends(mock.Anything, int64(1)).Return(want, nil)

	svc := service.NewFriendService(fs, us)
	got, err := svc.ListFriends(context.Background(), 1)
	require.NoError(t, err)
	require.Equal(t, want, got)
}

func TestFriend_ListPending_OK(t *testing.T) {
	fs := mocks.NewFriendshipRepoMock(t)
	us := mocks.NewUserRepoMock(t)
	want := []repo.PendingRequest{{
		Friendship: repo.Friendship{ID: 5, RequesterID: 2, AddresseeID: 1, Status: repo.FriendshipPending},
		Requester:  repo.User{ID: 2, Username: "bob"},
	}}
	fs.EXPECT().ListPendingRequests(mock.Anything, int64(1)).Return(want, nil)

	svc := service.NewFriendService(fs, us)
	got, err := svc.ListPending(context.Background(), 1)
	require.NoError(t, err)
	require.Equal(t, want, got)
}

func TestFriend_BlockUser_OK(t *testing.T) {
	fs := mocks.NewFriendshipRepoMock(t)
	us := mocks.NewUserRepoMock(t)
	fs.EXPECT().BlockUser(mock.Anything, int64(1), int64(2)).Return(nil)

	svc := service.NewFriendService(fs, us)
	require.NoError(t, svc.BlockUser(context.Background(), 1, 2))
}

func TestFriend_SearchUsers_OK(t *testing.T) {
	fs := mocks.NewFriendshipRepoMock(t)
	us := mocks.NewUserRepoMock(t)
	want := []repo.User{{ID: 2, Username: "bob"}}
	us.EXPECT().Search(mock.Anything, "bob", int64(1)).Return(want, nil)

	svc := service.NewFriendService(fs, us)
	got, err := svc.SearchUsers(context.Background(), "bob", 1)
	require.NoError(t, err)
	require.Equal(t, want, got)
}

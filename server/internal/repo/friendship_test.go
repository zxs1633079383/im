//go:build integration

package repo

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"
	"im-server/internal/testutil/containers"
)

func newFriendshipRepo(t *testing.T) (FriendshipRepo, UserRepo, context.Context) {
	dsn := containers.StartPostgres(t)
	db, err := Open(Config{DSN: dsn})
	require.NoError(t, err)
	return NewFriendshipRepo(db), NewUserRepo(db), context.Background()
}

func mkUser(t *testing.T, users UserRepo, ctx context.Context, name string) *User {
	u := &User{Username: name, Email: name + "@x.com", PasswordHash: "h", Status: 1}
	require.NoError(t, users.Create(ctx, u))
	return u
}

func TestFriendship_RequestAcceptListFriends(t *testing.T) {
	fr, ur, ctx := newFriendshipRepo(t)
	a, b := mkUser(t, ur, ctx, "alice"), mkUser(t, ur, ctx, "bob")

	require.NoError(t, fr.SendRequest(ctx, a.ID, b.ID))
	got, err := fr.GetFriendship(ctx, a.ID, b.ID)
	require.NoError(t, err)
	require.Equal(t, FriendshipPending, got.Status)

	requesterID, err := fr.AcceptRequest(ctx, got.ID, b.ID)
	require.NoError(t, err)
	require.Equal(t, a.ID, requesterID)

	friendsOfA, err := fr.ListFriends(ctx, a.ID)
	require.NoError(t, err)
	require.Len(t, friendsOfA, 1)
	require.Equal(t, b.ID, friendsOfA[0].ID)

	friendsOfB, err := fr.ListFriends(ctx, b.ID)
	require.NoError(t, err)
	require.Len(t, friendsOfB, 1)
	require.Equal(t, a.ID, friendsOfB[0].ID)
}

func TestFriendship_AcceptByNonAddressee_NotFound(t *testing.T) {
	fr, ur, ctx := newFriendshipRepo(t)
	a, b := mkUser(t, ur, ctx, "alice"), mkUser(t, ur, ctx, "bob")
	require.NoError(t, fr.SendRequest(ctx, a.ID, b.ID))
	got, _ := fr.GetFriendship(ctx, a.ID, b.ID)

	// requester (a) tries to accept their own request — should fail.
	_, err := fr.AcceptRequest(ctx, got.ID, a.ID)
	require.ErrorIs(t, err, ErrNotFound)
}

func TestFriendship_Reject(t *testing.T) {
	fr, ur, ctx := newFriendshipRepo(t)
	a, b := mkUser(t, ur, ctx, "alice"), mkUser(t, ur, ctx, "bob")
	require.NoError(t, fr.SendRequest(ctx, a.ID, b.ID))
	got, _ := fr.GetFriendship(ctx, a.ID, b.ID)
	requesterID, err := fr.RejectRequest(ctx, got.ID, b.ID)
	require.NoError(t, err)
	require.Equal(t, a.ID, requesterID)
	updated, _ := fr.GetFriendship(ctx, a.ID, b.ID)
	require.Equal(t, FriendshipRejected, updated.Status)
}

func TestFriendship_ListPending(t *testing.T) {
	fr, ur, ctx := newFriendshipRepo(t)
	a := mkUser(t, ur, ctx, "alice")
	b := mkUser(t, ur, ctx, "bob")
	c := mkUser(t, ur, ctx, "carol")

	require.NoError(t, fr.SendRequest(ctx, a.ID, c.ID))
	require.NoError(t, fr.SendRequest(ctx, b.ID, c.ID))

	pending, err := fr.ListPendingRequests(ctx, c.ID)
	require.NoError(t, err)
	require.Len(t, pending, 2)
	for _, p := range pending {
		require.Equal(t, FriendshipPending, p.Status)
		require.NotEmpty(t, p.Requester.Username)
	}
}

func TestFriendship_Block(t *testing.T) {
	fr, ur, ctx := newFriendshipRepo(t)
	a, b := mkUser(t, ur, ctx, "alice"), mkUser(t, ur, ctx, "bob")
	require.NoError(t, fr.BlockUser(ctx, a.ID, b.ID))
	got, err := fr.GetFriendship(ctx, a.ID, b.ID)
	require.NoError(t, err)
	require.Equal(t, FriendshipBlocked, got.Status)

	// Block again — should update existing row, not error.
	require.NoError(t, fr.BlockUser(ctx, a.ID, b.ID))
}

func TestFriendship_SelfRequest_Error(t *testing.T) {
	fr, ur, ctx := newFriendshipRepo(t)
	a := mkUser(t, ur, ctx, "alice")
	require.Error(t, fr.SendRequest(ctx, a.ID, a.ID))
	require.Error(t, fr.BlockUser(ctx, a.ID, a.ID))
}

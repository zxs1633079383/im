//go:build integration

package repo

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"
	"im-server/internal/testutil/containers"
)

func newUserRepo(t *testing.T) (UserRepo, context.Context) {
	dsn := containers.StartPostgres(t)
	db, err := Open(Config{DSN: dsn})
	require.NoError(t, err)
	return NewUserRepo(db), context.Background()
}

func TestUserRepo_CreateAndGetByUsername(t *testing.T) {
	r, ctx := newUserRepo(t)
	u := &User{Username: "alice", Email: "a@x.com", PasswordHash: "h", Status: 1}
	require.NoError(t, r.Create(ctx, u))
	require.NotZero(t, u.ID)
	got, err := r.GetByUsername(ctx, "alice")
	require.NoError(t, err)
	require.Equal(t, u.ID, got.ID)
}

func TestUserRepo_GetByEmail(t *testing.T) {
	r, ctx := newUserRepo(t)
	u := &User{Username: "bob", Email: "bob@x.com", PasswordHash: "h", Status: 1}
	require.NoError(t, r.Create(ctx, u))
	got, err := r.GetByEmail(ctx, "bob@x.com")
	require.NoError(t, err)
	require.Equal(t, u.ID, got.ID)
}

func TestUserRepo_NotFound(t *testing.T) {
	r, ctx := newUserRepo(t)
	_, err := r.GetByUsername(ctx, "ghost")
	require.ErrorIs(t, err, ErrNotFound)
	_, err = r.GetByEmail(ctx, "ghost@x.com")
	require.ErrorIs(t, err, ErrNotFound)
	_, err = r.GetByID(ctx, 9999)
	require.ErrorIs(t, err, ErrNotFound)
}

func TestUserRepo_DuplicateUsername(t *testing.T) {
	r, ctx := newUserRepo(t)
	require.NoError(t, r.Create(ctx, &User{Username: "alice", Email: "a@x.com", PasswordHash: "h", Status: 1}))
	err := r.Create(ctx, &User{Username: "alice", Email: "b@x.com", PasswordHash: "h", Status: 1})
	require.Error(t, err)
}

func TestUserRepo_UpdateProfile(t *testing.T) {
	r, ctx := newUserRepo(t)
	u := &User{Username: "u", Email: "u@x.com", PasswordHash: "h", Status: 1}
	require.NoError(t, r.Create(ctx, u))
	got, err := r.UpdateProfile(ctx, u.ID, "New Name", "https://x.com/a.png")
	require.NoError(t, err)
	require.Equal(t, "New Name", got.DisplayName)
	require.Equal(t, "https://x.com/a.png", got.AvatarURL)
}

func TestUserRepo_UpdateProfile_NotFound(t *testing.T) {
	r, ctx := newUserRepo(t)
	_, err := r.UpdateProfile(ctx, 9999, "x", "y")
	require.ErrorIs(t, err, ErrNotFound)
}

func TestUserRepo_Search(t *testing.T) {
	r, ctx := newUserRepo(t)
	require.NoError(t, r.Create(ctx, &User{Username: "alice", Email: "a@x.com", PasswordHash: "h", Status: 1}))
	require.NoError(t, r.Create(ctx, &User{Username: "alex", Email: "alex@x.com", PasswordHash: "h", Status: 1}))
	caller := &User{Username: "caller", Email: "c@x.com", PasswordHash: "h", Status: 1}
	require.NoError(t, r.Create(ctx, caller))

	got, err := r.Search(ctx, "al", caller.ID)
	require.NoError(t, err)
	require.Len(t, got, 2)
	for _, u := range got {
		require.NotEqual(t, caller.ID, u.ID, "caller should be excluded")
	}
}

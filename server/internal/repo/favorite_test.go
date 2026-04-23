//go:build integration

package repo

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"
	"im-server/internal/testutil/containers"
)

func newFavoriteRepo(t *testing.T) (FavoriteRepo, MessageRepo, ChannelRepo, UserRepo, context.Context) {
	dsn := containers.StartPostgres(t)
	db, err := Open(Config{DSN: dsn})
	require.NoError(t, err)
	cr := NewChannelRepo(db)
	return NewFavoriteRepo(db), NewMessageRepo(db, cr), cr, NewUserRepo(db), context.Background()
}

func TestFavoriteRepo_AddAndList(t *testing.T) {
	fav, mr, cr, ur, ctx := newFavoriteRepo(t)
	u := mkUser(t, ur, ctx, "alice")
	ch := mkMsgChannel(t, cr, ctx, "favs", u.ID)

	m1 := &Message{ChannelID: ch.ID, SenderID: u.ID, Content: "first", MsgType: 1}
	m2 := &Message{ChannelID: ch.ID, SenderID: u.ID, Content: "second", MsgType: 1}
	require.NoError(t, mr.Send(ctx, m1))
	require.NoError(t, mr.Send(ctx, m2))

	require.NoError(t, fav.Add(ctx, u.ID, m1.ID))
	require.NoError(t, fav.Add(ctx, u.ID, m2.ID))

	got, err := fav.List(ctx, u.ID)
	require.NoError(t, err)
	require.Len(t, got, 2)
	// Newest first — m2 was favorited last.
	require.Equal(t, m2.ID, got[0].MessageID)
	require.Equal(t, m1.ID, got[1].MessageID)
	require.Equal(t, "second", got[0].Message.Content)
	require.Equal(t, "first", got[1].Message.Content)
	require.Equal(t, ch.ID, got[0].Message.ChannelID)
	require.Equal(t, u.ID, got[0].UserID)
	require.False(t, got[0].CreatedAt.IsZero(), "favorite CreatedAt should be set by DB default")
}

func TestFavoriteRepo_Add_Idempotent(t *testing.T) {
	fav, mr, cr, ur, ctx := newFavoriteRepo(t)
	u := mkUser(t, ur, ctx, "alice")
	ch := mkMsgChannel(t, cr, ctx, "idem", u.ID)
	m := &Message{ChannelID: ch.ID, SenderID: u.ID, Content: "x", MsgType: 1}
	require.NoError(t, mr.Send(ctx, m))

	require.NoError(t, fav.Add(ctx, u.ID, m.ID))
	require.NoError(t, fav.Add(ctx, u.ID, m.ID)) // duplicate — must not error

	got, err := fav.List(ctx, u.ID)
	require.NoError(t, err)
	require.Len(t, got, 1)
}

func TestFavoriteRepo_Remove(t *testing.T) {
	fav, mr, cr, ur, ctx := newFavoriteRepo(t)
	u := mkUser(t, ur, ctx, "alice")
	ch := mkMsgChannel(t, cr, ctx, "rm", u.ID)
	m := &Message{ChannelID: ch.ID, SenderID: u.ID, Content: "x", MsgType: 1}
	require.NoError(t, mr.Send(ctx, m))

	require.NoError(t, fav.Add(ctx, u.ID, m.ID))
	require.NoError(t, fav.Remove(ctx, u.ID, m.ID))

	got, err := fav.List(ctx, u.ID)
	require.NoError(t, err)
	require.Empty(t, got)
}

func TestFavoriteRepo_Remove_NotFound(t *testing.T) {
	fav, _, _, ur, ctx := newFavoriteRepo(t)
	u := mkUser(t, ur, ctx, "alice")

	err := fav.Remove(ctx, u.ID, 99999)
	require.ErrorIs(t, err, ErrNotFound)
}

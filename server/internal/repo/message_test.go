//go:build integration

package repo

import (
	"context"
	"fmt"
	"sync"
	"testing"

	"github.com/lib/pq"
	"github.com/stretchr/testify/require"
	"im-server/internal/testutil/containers"
)

// newMessageRepo builds a fresh DB with channel + user + message repos
// wired up. Returns the trio plus a context.
func newMessageRepo(t *testing.T) (MessageRepo, ChannelRepo, UserRepo, context.Context) {
	dsn := containers.StartPostgres(t)
	db, err := Open(Config{DSN: dsn})
	require.NoError(t, err)
	cr := NewChannelRepo(db)
	return NewMessageRepo(db, cr), cr, NewUserRepo(db), context.Background()
}

// mkMsgChannel creates a channel + adds members (each with role member).
func mkMsgChannel(t *testing.T, cr ChannelRepo, ctx context.Context, name string, members ...int64) *Channel {
	ch := &Channel{Type: ChannelTypeGroup, Name: name}
	require.NoError(t, cr.Create(ctx, ch))
	for _, uid := range members {
		require.NoError(t, cr.AddMember(ctx, ch.ID, uid, MemberRoleMember))
	}
	return ch
}

func TestMessageRepo_SendHappyPath(t *testing.T) {
	mr, cr, ur, ctx := newMessageRepo(t)
	u := mkUser(t, ur, ctx, "alice")
	ch := mkMsgChannel(t, cr, ctx, "happy", u.ID)

	msg := &Message{
		ChannelID:   ch.ID,
		SenderID:    u.ID,
		ClientMsgID: "uuid-001",
		MsgType:     1,
		Content:     "hello",
	}
	require.NoError(t, mr.Send(ctx, msg))
	require.Equal(t, int64(1), msg.Seq)
	require.NotZero(t, msg.ID)
	require.False(t, msg.CreatedAt.IsZero(), "CreatedAt should be set by DB default")

	// Channel seq should also be 1.
	got, err := cr.GetByID(ctx, ch.ID)
	require.NoError(t, err)
	require.Equal(t, int64(1), got.Seq)
}

func TestMessageRepo_SendIdempotent(t *testing.T) {
	mr, cr, ur, ctx := newMessageRepo(t)
	u := mkUser(t, ur, ctx, "alice")
	ch := mkMsgChannel(t, cr, ctx, "idem", u.ID)

	first := &Message{ChannelID: ch.ID, SenderID: u.ID, ClientMsgID: "dup", Content: "first", MsgType: 1}
	require.NoError(t, mr.Send(ctx, first))

	second := &Message{ChannelID: ch.ID, SenderID: u.ID, ClientMsgID: "dup", Content: "duplicate", MsgType: 1}
	require.NoError(t, mr.Send(ctx, second))

	require.Equal(t, first.ID, second.ID, "duplicate ClientMsgID should resolve to same ID")
	require.Equal(t, first.Seq, second.Seq, "duplicate ClientMsgID should resolve to same Seq")

	// Only one row inserted.
	all, err := mr.FetchAfter(ctx, ch.ID, 0, 50)
	require.NoError(t, err)
	require.Len(t, all, 1)
	require.Equal(t, "first", all[0].Content)
}

func TestMessageRepo_SendEmptyClientMsgIDInsertsDistinctRows(t *testing.T) {
	mr, cr, ur, ctx := newMessageRepo(t)
	u := mkUser(t, ur, ctx, "alice")
	ch := mkMsgChannel(t, cr, ctx, "empty-cid", u.ID)

	a := &Message{ChannelID: ch.ID, SenderID: u.ID, Content: "a", MsgType: 1}
	b := &Message{ChannelID: ch.ID, SenderID: u.ID, Content: "b", MsgType: 1}
	require.NoError(t, mr.Send(ctx, a))
	require.NoError(t, mr.Send(ctx, b))

	require.NotEqual(t, a.ID, b.ID)
	require.Equal(t, int64(1), a.Seq)
	require.Equal(t, int64(2), b.Seq)

	all, err := mr.FetchAfter(ctx, ch.ID, 0, 50)
	require.NoError(t, err)
	require.Len(t, all, 2)
}

func TestMessageRepo_SendConcurrent(t *testing.T) {
	mr, cr, ur, ctx := newMessageRepo(t)
	u := mkUser(t, ur, ctx, "alice")
	ch := mkMsgChannel(t, cr, ctx, "concurrent", u.ID)

	const N = 10
	var wg sync.WaitGroup
	wg.Add(N)
	seqs := make([]int64, N)
	errs := make([]error, N)
	for i := 0; i < N; i++ {
		go func(i int) {
			defer wg.Done()
			msg := &Message{
				ChannelID:   ch.ID,
				SenderID:    u.ID,
				ClientMsgID: fmt.Sprintf("c-%d", i),
				Content:     fmt.Sprintf("msg-%d", i),
				MsgType:     1,
			}
			errs[i] = mr.Send(ctx, msg)
			seqs[i] = msg.Seq
		}(i)
	}
	wg.Wait()

	seen := make(map[int64]bool, N)
	for i, s := range seqs {
		require.NoError(t, errs[i])
		require.False(t, seen[s], "duplicate seq %d", s)
		seen[s] = true
	}
	for want := int64(1); want <= N; want++ {
		require.True(t, seen[want], "missing seq %d", want)
	}

	got, err := cr.GetByID(ctx, ch.ID)
	require.NoError(t, err)
	require.Equal(t, int64(N), got.Seq)
}

func TestMessageRepo_SendVisibleToBumpsPhantom(t *testing.T) {
	mr, cr, ur, ctx := newMessageRepo(t)
	a := mkUser(t, ur, ctx, "alice")
	b := mkUser(t, ur, ctx, "bob")
	c := mkUser(t, ur, ctx, "carol")
	ch := mkMsgChannel(t, cr, ctx, "visible", a.ID, b.ID, c.ID)

	// Alice sends a message visible only to Bob (carol is excluded).
	msg := &Message{
		ChannelID: ch.ID,
		SenderID:  a.ID,
		Content:   "directed",
		MsgType:   1,
		VisibleTo: pq.Int64Array{b.ID},
	}
	require.NoError(t, mr.Send(ctx, msg))

	ma, _ := cr.GetMember(ctx, ch.ID, a.ID)
	mb, _ := cr.GetMember(ctx, ch.ID, b.ID)
	mc, _ := cr.GetMember(ctx, ch.ID, c.ID)
	require.Equal(t, int64(0), ma.PhantomCount, "sender should not see phantom")
	require.Equal(t, int64(0), mb.PhantomCount, "included recipient should not see phantom")
	require.Equal(t, int64(1), mc.PhantomCount, "excluded member should see phantom")
}

func TestMessageRepo_GetByID(t *testing.T) {
	mr, cr, ur, ctx := newMessageRepo(t)
	u := mkUser(t, ur, ctx, "alice")
	ch := mkMsgChannel(t, cr, ctx, "get", u.ID)

	msg := &Message{ChannelID: ch.ID, SenderID: u.ID, Content: "x", MsgType: 1}
	require.NoError(t, mr.Send(ctx, msg))

	got, err := mr.GetByID(ctx, msg.ID)
	require.NoError(t, err)
	require.Equal(t, msg.ID, got.ID)
	require.Equal(t, "x", got.Content)

	_, err = mr.GetByID(ctx, 99999)
	require.ErrorIs(t, err, ErrNotFound)
}

func TestMessageRepo_FetchAfterOrdering(t *testing.T) {
	mr, cr, ur, ctx := newMessageRepo(t)
	u := mkUser(t, ur, ctx, "alice")
	ch := mkMsgChannel(t, cr, ctx, "after", u.ID)

	for i := 0; i < 5; i++ {
		require.NoError(t, mr.Send(ctx, &Message{
			ChannelID: ch.ID, SenderID: u.ID, Content: fmt.Sprintf("m%d", i), MsgType: 1,
		}))
	}

	out, err := mr.FetchAfter(ctx, ch.ID, 2, 50)
	require.NoError(t, err)
	require.Len(t, out, 3)
	for i, m := range out {
		require.Equal(t, int64(3+i), m.Seq)
	}
}

func TestMessageRepo_FetchForUserHonorsVisibleTo(t *testing.T) {
	mr, cr, ur, ctx := newMessageRepo(t)
	a := mkUser(t, ur, ctx, "alice")
	b := mkUser(t, ur, ctx, "bob")
	c := mkUser(t, ur, ctx, "carol")
	ch := mkMsgChannel(t, cr, ctx, "vis", a.ID, b.ID, c.ID)

	// 1) public; 2) visible to alice only; 3) public.
	require.NoError(t, mr.Send(ctx, &Message{ChannelID: ch.ID, SenderID: a.ID, Content: "pub1", MsgType: 1}))
	require.NoError(t, mr.Send(ctx, &Message{ChannelID: ch.ID, SenderID: a.ID, Content: "secret", MsgType: 1, VisibleTo: pq.Int64Array{a.ID}}))
	require.NoError(t, mr.Send(ctx, &Message{ChannelID: ch.ID, SenderID: a.ID, Content: "pub2", MsgType: 1}))

	// Alice (sender of the secret) sees all 3.
	aliceView, err := mr.FetchForUser(ctx, ch.ID, a.ID, 0, 50)
	require.NoError(t, err)
	require.Len(t, aliceView, 3)

	// Bob (excluded) sees only the 2 publics.
	bobView, err := mr.FetchForUser(ctx, ch.ID, b.ID, 0, 50)
	require.NoError(t, err)
	require.Len(t, bobView, 2)
	require.Equal(t, "pub1", bobView[0].Content)
	require.Equal(t, "pub2", bobView[1].Content)

	// Carol (excluded) also sees only the 2 publics.
	carolView, err := mr.FetchForUser(ctx, ch.ID, c.ID, 0, 50)
	require.NoError(t, err)
	require.Len(t, carolView, 2)
}

func TestMessageRepo_FetchForUserIncludedRecipientSees(t *testing.T) {
	mr, cr, ur, ctx := newMessageRepo(t)
	a := mkUser(t, ur, ctx, "alice")
	b := mkUser(t, ur, ctx, "bob")
	c := mkUser(t, ur, ctx, "carol")
	ch := mkMsgChannel(t, cr, ctx, "inc", a.ID, b.ID, c.ID)

	// Alice sends a message visible to Bob — carol is excluded.
	require.NoError(t, mr.Send(ctx, &Message{
		ChannelID: ch.ID, SenderID: a.ID, Content: "for-bob", MsgType: 1,
		VisibleTo: pq.Int64Array{b.ID},
	}))

	bobView, err := mr.FetchForUser(ctx, ch.ID, b.ID, 0, 50)
	require.NoError(t, err)
	require.Len(t, bobView, 1)
	require.Equal(t, "for-bob", bobView[0].Content)

	carolView, err := mr.FetchForUser(ctx, ch.ID, c.ID, 0, 50)
	require.NoError(t, err)
	require.Len(t, carolView, 0)
}

func TestMessageRepo_FetchBefore(t *testing.T) {
	mr, cr, ur, ctx := newMessageRepo(t)
	u := mkUser(t, ur, ctx, "alice")
	ch := mkMsgChannel(t, cr, ctx, "before", u.ID)

	for i := 0; i < 5; i++ {
		require.NoError(t, mr.Send(ctx, &Message{
			ChannelID: ch.ID, SenderID: u.ID, Content: fmt.Sprintf("m%d", i), MsgType: 1,
		}))
	}

	// Asking for messages before seq=4, limit 2 → seqs {2, 3} (oldest first).
	out, err := mr.FetchBefore(ctx, ch.ID, u.ID, 4, 2)
	require.NoError(t, err)
	require.Len(t, out, 2)
	require.Equal(t, int64(2), out[0].Seq)
	require.Equal(t, int64(3), out[1].Seq)
}

func TestMessageRepo_FetchAround(t *testing.T) {
	mr, cr, ur, ctx := newMessageRepo(t)
	u := mkUser(t, ur, ctx, "alice")
	ch := mkMsgChannel(t, cr, ctx, "around", u.ID)

	for i := 0; i < 10; i++ {
		require.NoError(t, mr.Send(ctx, &Message{
			ChannelID: ch.ID, SenderID: u.ID, Content: fmt.Sprintf("m%d", i), MsgType: 1,
		}))
	}

	// Around seq 5 with limit 6 → 3 before/at + 3 after = seqs {3,4,5,6,7,8}.
	out, err := mr.FetchAround(ctx, ch.ID, u.ID, 5, 6)
	require.NoError(t, err)
	require.Len(t, out, 6)
	for i, m := range out {
		require.Equal(t, int64(3+i), m.Seq)
	}
}

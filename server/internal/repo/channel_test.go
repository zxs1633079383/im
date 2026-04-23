//go:build integration

package repo

import (
	"context"
	"sync"
	"testing"

	"github.com/stretchr/testify/require"
	"im-server/internal/testutil/containers"
)

func newChannelRepo(t *testing.T) (ChannelRepo, UserRepo, context.Context) {
	dsn := containers.StartPostgres(t)
	db, err := Open(Config{DSN: dsn})
	require.NoError(t, err)
	return NewChannelRepo(db), NewUserRepo(db), context.Background()
}

func mkChannel(t *testing.T, cr ChannelRepo, ctx context.Context, name string, creator *int64) *Channel {
	ch := &Channel{Type: ChannelTypeGroup, Name: name, CreatorID: creator}
	require.NoError(t, cr.Create(ctx, ch))
	require.NotZero(t, ch.ID)
	return ch
}

func TestChannelRepo_CreateAndGetByID(t *testing.T) {
	cr, ur, ctx := newChannelRepo(t)
	u := mkUser(t, ur, ctx, "alice")
	ch := mkChannel(t, cr, ctx, "g1", &u.ID)
	require.Equal(t, int64(0), ch.Seq)

	got, err := cr.GetByID(ctx, ch.ID)
	require.NoError(t, err)
	require.Equal(t, ch.ID, got.ID)
	require.Equal(t, "g1", got.Name)
	require.NotNil(t, got.CreatorID)
	require.Equal(t, u.ID, *got.CreatorID)

	// Nonexistent ID -> ErrNotFound.
	_, err = cr.GetByID(ctx, 9999)
	require.ErrorIs(t, err, ErrNotFound)
}

func TestChannelRepo_AddMemberGetMemberListMembers(t *testing.T) {
	cr, ur, ctx := newChannelRepo(t)
	a := mkUser(t, ur, ctx, "alice")
	b := mkUser(t, ur, ctx, "bob")
	ch := mkChannel(t, cr, ctx, "g", &a.ID)

	require.NoError(t, cr.AddMember(ctx, ch.ID, a.ID, MemberRoleOwner))
	require.NoError(t, cr.AddMember(ctx, ch.ID, b.ID, MemberRoleMember))

	// Idempotent — second AddMember returns nil (ON CONFLICT DO NOTHING).
	require.NoError(t, cr.AddMember(ctx, ch.ID, a.ID, MemberRoleOwner))

	got, err := cr.GetMember(ctx, ch.ID, a.ID)
	require.NoError(t, err)
	require.Equal(t, MemberRoleOwner, got.Role)
	require.Equal(t, a.ID, got.UserID)
	require.Equal(t, ch.ID, got.ChannelID)

	members, err := cr.ListMembers(ctx, ch.ID)
	require.NoError(t, err)
	require.Len(t, members, 2)

	// Nonexistent member -> ErrNotFound.
	_, err = cr.GetMember(ctx, ch.ID, 9999)
	require.ErrorIs(t, err, ErrNotFound)
}

func TestChannelRepo_RemoveMemberIdempotent(t *testing.T) {
	cr, ur, ctx := newChannelRepo(t)
	a := mkUser(t, ur, ctx, "alice")
	ch := mkChannel(t, cr, ctx, "g", &a.ID)

	require.NoError(t, cr.AddMember(ctx, ch.ID, a.ID, MemberRoleOwner))
	require.NoError(t, cr.RemoveMember(ctx, ch.ID, a.ID))
	// Idempotent: second remove succeeds.
	require.NoError(t, cr.RemoveMember(ctx, ch.ID, a.ID))

	_, err := cr.GetMember(ctx, ch.ID, a.ID)
	require.ErrorIs(t, err, ErrNotFound)
}

func TestChannelRepo_IncrementSeqSequential(t *testing.T) {
	cr, _, ctx := newChannelRepo(t)
	ch := mkChannel(t, cr, ctx, "seq", nil)

	for want := int64(1); want <= 5; want++ {
		got, err := cr.IncrementSeq(ctx, nil, ch.ID)
		require.NoError(t, err)
		require.Equal(t, want, got)
	}
}

func TestChannelRepo_IncrementSeqConcurrent(t *testing.T) {
	cr, _, ctx := newChannelRepo(t)
	ch := mkChannel(t, cr, ctx, "seq-conc", nil)

	const N = 10
	results := make([]int64, N)
	var wg sync.WaitGroup
	wg.Add(N)
	for i := 0; i < N; i++ {
		go func(i int) {
			defer wg.Done()
			seq, err := cr.IncrementSeq(ctx, nil, ch.ID)
			require.NoError(t, err)
			results[i] = seq
		}(i)
	}
	wg.Wait()

	seen := make(map[int64]bool, N)
	for _, s := range results {
		require.False(t, seen[s], "duplicate seq %d", s)
		seen[s] = true
	}
	for want := int64(1); want <= N; want++ {
		require.True(t, seen[want], "missing seq %d", want)
	}
}

func TestChannelRepo_MarkRead(t *testing.T) {
	cr, ur, ctx := newChannelRepo(t)
	a := mkUser(t, ur, ctx, "alice")
	ch := mkChannel(t, cr, ctx, "g", &a.ID)
	require.NoError(t, cr.AddMember(ctx, ch.ID, a.ID, MemberRoleOwner))

	require.NoError(t, cr.MarkRead(ctx, ch.ID, a.ID, 5))
	m, err := cr.GetMember(ctx, ch.ID, a.ID)
	require.NoError(t, err)
	require.Equal(t, int64(5), m.LastReadSeq)
	require.Equal(t, m.PhantomCount, m.PhantomAtRead)

	// Bumping further still works.
	require.NoError(t, cr.MarkRead(ctx, ch.ID, a.ID, 10))
	m, err = cr.GetMember(ctx, ch.ID, a.ID)
	require.NoError(t, err)
	require.Equal(t, int64(10), m.LastReadSeq)
}

func TestChannelRepo_IncrementPhantomCountExcludesUsers(t *testing.T) {
	cr, ur, ctx := newChannelRepo(t)
	a := mkUser(t, ur, ctx, "alice")
	b := mkUser(t, ur, ctx, "bob")
	c := mkUser(t, ur, ctx, "carol")
	ch := mkChannel(t, cr, ctx, "g", &a.ID)
	require.NoError(t, cr.AddMember(ctx, ch.ID, a.ID, MemberRoleOwner))
	require.NoError(t, cr.AddMember(ctx, ch.ID, b.ID, MemberRoleMember))
	require.NoError(t, cr.AddMember(ctx, ch.ID, c.ID, MemberRoleMember))

	// Exclude alice — bob and carol should bump.
	require.NoError(t, cr.IncrementPhantomCount(ctx, nil, ch.ID, []int64{a.ID}))

	ma, _ := cr.GetMember(ctx, ch.ID, a.ID)
	mb, _ := cr.GetMember(ctx, ch.ID, b.ID)
	mc, _ := cr.GetMember(ctx, ch.ID, c.ID)
	require.Equal(t, int64(0), ma.PhantomCount)
	require.Equal(t, int64(1), mb.PhantomCount)
	require.Equal(t, int64(1), mc.PhantomCount)

	// Empty exclude list — all bump.
	require.NoError(t, cr.IncrementPhantomCount(ctx, nil, ch.ID, nil))
	ma, _ = cr.GetMember(ctx, ch.ID, a.ID)
	mb, _ = cr.GetMember(ctx, ch.ID, b.ID)
	require.Equal(t, int64(1), ma.PhantomCount)
	require.Equal(t, int64(2), mb.PhantomCount)
}

func TestChannelRepo_FindDM(t *testing.T) {
	cr, ur, ctx := newChannelRepo(t)
	a := mkUser(t, ur, ctx, "alice")
	b := mkUser(t, ur, ctx, "bob")

	// Not found before creation.
	_, err := cr.FindDM(ctx, a.ID, b.ID)
	require.ErrorIs(t, err, ErrNotFound)

	dm := &Channel{Type: ChannelTypeDM}
	require.NoError(t, cr.Create(ctx, dm))
	require.NoError(t, cr.AddMember(ctx, dm.ID, a.ID, MemberRoleMember))
	require.NoError(t, cr.AddMember(ctx, dm.ID, b.ID, MemberRoleMember))

	got, err := cr.FindDM(ctx, a.ID, b.ID)
	require.NoError(t, err)
	require.Equal(t, dm.ID, got.ID)

	// Reverse order returns the same DM.
	got2, err := cr.FindDM(ctx, b.ID, a.ID)
	require.NoError(t, err)
	require.Equal(t, dm.ID, got2.ID)
}

func TestChannelRepo_ListByUserWithPreviewNoMessages(t *testing.T) {
	cr, ur, ctx := newChannelRepo(t)
	a := mkUser(t, ur, ctx, "alice")
	ch := mkChannel(t, cr, ctx, "preview", &a.ID)
	require.NoError(t, cr.AddMember(ctx, ch.ID, a.ID, MemberRoleOwner))

	previews, err := cr.ListByUserWithPreview(ctx, a.ID)
	require.NoError(t, err)
	require.Len(t, previews, 1)
	require.Equal(t, ch.ID, previews[0].ID)
	require.Equal(t, "", previews[0].LastMsgContent)
	require.Equal(t, int64(0), previews[0].UnreadCount)
}

func TestChannelRepo_GetMemberChannelSeqs(t *testing.T) {
	cr, ur, ctx := newChannelRepo(t)
	a := mkUser(t, ur, ctx, "alice")
	b := mkUser(t, ur, ctx, "bob")
	ch1 := mkChannel(t, cr, ctx, "c1", &a.ID)
	ch2 := mkChannel(t, cr, ctx, "c2", &a.ID)
	chOther := mkChannel(t, cr, ctx, "c3", &b.ID)
	require.NoError(t, cr.AddMember(ctx, ch1.ID, a.ID, MemberRoleOwner))
	require.NoError(t, cr.AddMember(ctx, ch2.ID, a.ID, MemberRoleOwner))
	require.NoError(t, cr.AddMember(ctx, chOther.ID, b.ID, MemberRoleOwner))

	// Bump seq on ch1 a few times so it's nonzero.
	_, _ = cr.IncrementSeq(ctx, nil, ch1.ID)
	_, _ = cr.IncrementSeq(ctx, nil, ch1.ID)

	seqs, err := cr.GetMemberChannelSeqs(ctx, a.ID)
	require.NoError(t, err)
	require.Len(t, seqs, 2)
	require.Equal(t, int64(2), seqs[ch1.ID])
	require.Equal(t, int64(0), seqs[ch2.ID])
	_, hasOther := seqs[chOther.ID]
	require.False(t, hasOther, "should not include channels caller doesn't belong to")
}

func TestChannelRepo_UpdateEmptyStringSkipsField(t *testing.T) {
	cr, ur, ctx := newChannelRepo(t)
	a := mkUser(t, ur, ctx, "alice")
	ch := &Channel{Type: ChannelTypeGroup, Name: "old-name", AvatarURL: "old-url", CreatorID: &a.ID}
	require.NoError(t, cr.Create(ctx, ch))

	// Update name only — avatar should stay.
	require.NoError(t, cr.Update(ctx, ch.ID, "new-name", ""))
	got, err := cr.GetByID(ctx, ch.ID)
	require.NoError(t, err)
	require.Equal(t, "new-name", got.Name)
	require.Equal(t, "old-url", got.AvatarURL)

	// Update avatar only — name should stay.
	require.NoError(t, cr.Update(ctx, ch.ID, "", "new-url"))
	got, err = cr.GetByID(ctx, ch.ID)
	require.NoError(t, err)
	require.Equal(t, "new-name", got.Name)
	require.Equal(t, "new-url", got.AvatarURL)

	// Both empty — neither changes.
	require.NoError(t, cr.Update(ctx, ch.ID, "", ""))
	got, err = cr.GetByID(ctx, ch.ID)
	require.NoError(t, err)
	require.Equal(t, "new-name", got.Name)
	require.Equal(t, "new-url", got.AvatarURL)
}

func TestChannelRepo_ListByUser(t *testing.T) {
	cr, ur, ctx := newChannelRepo(t)
	a := mkUser(t, ur, ctx, "alice")
	b := mkUser(t, ur, ctx, "bob")
	ch1 := mkChannel(t, cr, ctx, "shared", &a.ID)
	ch2 := mkChannel(t, cr, ctx, "alice-only", &a.ID)
	chB := mkChannel(t, cr, ctx, "bob-only", &b.ID)
	require.NoError(t, cr.AddMember(ctx, ch1.ID, a.ID, MemberRoleOwner))
	require.NoError(t, cr.AddMember(ctx, ch1.ID, b.ID, MemberRoleMember))
	require.NoError(t, cr.AddMember(ctx, ch2.ID, a.ID, MemberRoleOwner))
	require.NoError(t, cr.AddMember(ctx, chB.ID, b.ID, MemberRoleOwner))

	got, err := cr.ListByUser(ctx, a.ID)
	require.NoError(t, err)
	require.Len(t, got, 2)
	ids := map[int64]bool{got[0].ID: true, got[1].ID: true}
	require.True(t, ids[ch1.ID])
	require.True(t, ids[ch2.ID])
}

//go:build integration

package repo

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"
	"im-server/internal/testutil/containers"
)

// newSearchRepo wires SearchRepo with the user/channel/message repos a test
// needs to seed data.
func newSearchRepo(t *testing.T) (SearchRepo, MessageRepo, ChannelRepo, UserRepo, context.Context) {
	dsn := containers.StartPostgres(t)
	db, err := Open(Config{DSN: dsn})
	require.NoError(t, err)
	cr := NewChannelRepo(db)
	return NewSearchRepo(db), NewMessageRepo(db, cr), cr, NewUserRepo(db), context.Background()
}

// sendMsg is a tiny helper that inserts a message via MessageRepo.Send so the
// channel.seq + idempotency machinery stays consistent.
func sendMsg(t *testing.T, mr MessageRepo, ctx context.Context, channelID, senderID int64, clientMsgID, content string) {
	t.Helper()
	require.NoError(t, mr.Send(ctx, &Message{
		ChannelID:   channelID,
		SenderID:    senderID,
		ClientMsgID: clientMsgID,
		MsgType:     1,
		Content:     content,
	}))
}

func TestSearchRepo_Messages_Match(t *testing.T) {
	sr, mr, cr, ur, ctx := newSearchRepo(t)
	u := mkUser(t, ur, ctx, "alice")
	ch := mkMsgChannel(t, cr, ctx, "general", u.ID)

	sendMsg(t, mr, ctx, ch.ID, u.ID, "m1", "hello world")
	sendMsg(t, mr, ctx, ch.ID, u.ID, "m2", "goodbye world")
	sendMsg(t, mr, ctx, ch.ID, u.ID, "m3", "totally unrelated text")

	results, err := sr.SearchMessages(ctx, "world", u.ID, 0, 20)
	require.NoError(t, err)
	require.Len(t, results, 2)
	for _, r := range results {
		require.Equal(t, "general", r.ChannelName)
		require.Equal(t, ch.ID, r.ChannelID)
	}
}

func TestSearchRepo_Messages_NotMember_Excluded(t *testing.T) {
	sr, mr, cr, ur, ctx := newSearchRepo(t)
	owner := mkUser(t, ur, ctx, "owner")
	outsider := mkUser(t, ur, ctx, "outsider")
	ch := mkMsgChannel(t, cr, ctx, "private", owner.ID)

	sendMsg(t, mr, ctx, ch.ID, owner.ID, "m1", "secret world stuff")

	results, err := sr.SearchMessages(ctx, "world", outsider.ID, 0, 20)
	require.NoError(t, err)
	require.Empty(t, results)
}

func TestSearchRepo_Messages_ChannelFilter(t *testing.T) {
	sr, mr, cr, ur, ctx := newSearchRepo(t)
	u := mkUser(t, ur, ctx, "alice")
	chA := mkMsgChannel(t, cr, ctx, "alpha", u.ID)
	chB := mkMsgChannel(t, cr, ctx, "beta", u.ID)

	sendMsg(t, mr, ctx, chA.ID, u.ID, "a1", "shared keyword here")
	sendMsg(t, mr, ctx, chB.ID, u.ID, "b1", "shared keyword here")

	all, err := sr.SearchMessages(ctx, "shared", u.ID, 0, 20)
	require.NoError(t, err)
	require.Len(t, all, 2)

	onlyA, err := sr.SearchMessages(ctx, "shared", u.ID, chA.ID, 20)
	require.NoError(t, err)
	require.Len(t, onlyA, 1)
	require.Equal(t, chA.ID, onlyA[0].ChannelID)
	require.Equal(t, "alpha", onlyA[0].ChannelName)
}

func TestSearchRepo_Users_ExcludesCaller(t *testing.T) {
	sr, _, _, ur, ctx := newSearchRepo(t)
	caller := mkUser(t, ur, ctx, "alice")
	mkUser(t, ur, ctx, "alicia")
	mkUser(t, ur, ctx, "alfred")

	results, err := sr.SearchUsers(ctx, "al", caller.ID, 20)
	require.NoError(t, err)
	require.Len(t, results, 2)
	for _, u := range results {
		require.NotEqual(t, caller.ID, u.ID)
	}
}

func TestSearchRepo_Users_Empty_NoResults(t *testing.T) {
	sr, _, _, ur, ctx := newSearchRepo(t)
	caller := mkUser(t, ur, ctx, "alice")
	mkUser(t, ur, ctx, "bob")

	results, err := sr.SearchUsers(ctx, "zzz-no-match", caller.ID, 20)
	require.NoError(t, err)
	require.Empty(t, results)
}

func TestSearchRepo_Channels_OnlyMemberChannels(t *testing.T) {
	sr, _, cr, ur, ctx := newSearchRepo(t)
	caller := mkUser(t, ur, ctx, "alice")
	other := mkUser(t, ur, ctx, "bob")

	chMine := mkMsgChannel(t, cr, ctx, "team-rocket", caller.ID)
	mkMsgChannel(t, cr, ctx, "team-magma", other.ID)

	results, err := sr.SearchChannels(ctx, "team", caller.ID, 20)
	require.NoError(t, err)
	require.Len(t, results, 1)
	require.Equal(t, chMine.ID, results[0].ID)
	require.Equal(t, "team-rocket", results[0].Name)
}

func TestSearchRepo_Channels_ExcludesDM(t *testing.T) {
	sr, _, cr, ur, ctx := newSearchRepo(t)
	a := mkUser(t, ur, ctx, "alice")
	b := mkUser(t, ur, ctx, "bob")

	// Group channel that should match.
	group := mkMsgChannel(t, cr, ctx, "lounge", a.ID)
	// DM channel — name "lounge" but type=DM — must be excluded.
	dm := &Channel{Type: ChannelTypeDM, Name: "lounge"}
	require.NoError(t, cr.Create(ctx, dm))
	require.NoError(t, cr.AddMember(ctx, dm.ID, a.ID, MemberRoleMember))
	require.NoError(t, cr.AddMember(ctx, dm.ID, b.ID, MemberRoleMember))

	results, err := sr.SearchChannels(ctx, "lounge", a.ID, 20)
	require.NoError(t, err)
	require.Len(t, results, 1)
	require.Equal(t, group.ID, results[0].ID)
}

func TestSearchRepo_EmptyQuery_ReturnsNil(t *testing.T) {
	sr, _, _, ur, ctx := newSearchRepo(t)
	caller := mkUser(t, ur, ctx, "alice")

	msgs, err := sr.SearchMessages(ctx, "", caller.ID, 0, 20)
	require.NoError(t, err)
	require.Nil(t, msgs)

	users, err := sr.SearchUsers(ctx, "   ", caller.ID, 20)
	require.NoError(t, err)
	require.Nil(t, users)

	channels, err := sr.SearchChannels(ctx, "", caller.ID, 20)
	require.NoError(t, err)
	require.Nil(t, channels)
}

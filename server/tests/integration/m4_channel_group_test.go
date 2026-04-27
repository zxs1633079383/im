//go:build integration

package integration

import (
	"context"
	"sort"
	"testing"

	"github.com/stretchr/testify/require"

	"im-server/internal/middleware"
)

// TestM4ChannelCreateGroup — POST /api/channels with two member ids creates
// a Group, persists creator as owner + members in channel_members
// (user_id TEXT path), and channels.team_id ends up = caller.companyId.
func TestM4ChannelCreateGroup(t *testing.T) {
	env := newM4Env(t)
	cookieOwner, ownerID := env.seedUser(20)
	_, member1 := env.seedUser(21)
	_, member2 := env.seedUser(22)

	created := env.expect.POST("/api/channels").
		WithHeader(middleware.MMCookieHeader, cookieOwner).
		WithJSON(map[string]any{
			"name":       "m4-group-happy",
			"member_ids": []string{member1, member2},
		}).
		Expect().Status(201).
		JSON().Object()

	created.Value("name").IsEqual("m4-group-happy")
	created.Value("creator_id").IsEqual(ownerID)
	channelID := int64(created.Value("id").Number().Raw())
	require.NotZero(t, channelID)

	// Persisted member set must contain owner + the two members. Ordering
	// is by joined_at; sort to make the assertion stable.
	members, err := env.channels.ListMembers(context.Background(), channelID)
	require.NoError(t, err)
	require.Len(t, members, 3, "owner + 2 members")
	got := []string{members[0].UserID, members[1].UserID, members[2].UserID}
	sort.Strings(got)
	want := []string{ownerID, member1, member2}
	sort.Strings(want)
	require.Equal(t, want, got)
}

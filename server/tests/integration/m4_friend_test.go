//go:build integration

package integration

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"

	"im-server/internal/middleware"
	"im-server/internal/repo"
)

// TestM4FriendRequestAccept — full request → list pending → accept loop.
// Verifies friendships.{requester_id, addressee_id} round-trip as TEXT and
// the persisted row flips to status=accepted on the second call.
func TestM4FriendRequestAccept(t *testing.T) {
	env := newM4Env(t)
	cookieReq, requester := env.seedUser(40)
	cookieAddr, addressee := env.seedUser(41)

	// Requester sends → 201.
	env.expect.POST("/api/friends/request").
		WithHeader(middleware.MMCookieHeader, cookieReq).
		WithJSON(map[string]any{"addressee_id": addressee}).
		Expect().Status(201)

	// Addressee lists pending → 1 entry, ids round-trip as TEXT.
	pending := successBodyArray(env.expect.GET("/api/friends/pending").
		WithHeader(middleware.MMCookieHeader, cookieAddr).
		Expect().Status(200))
	pending.Length().IsEqual(1)
	row := pending.Value(0).Object()
	row.Value("requester_id").IsEqual(requester)
	row.Value("addressee_id").IsEqual(addressee)
	friendshipID := int64(row.Value("id").Number().Raw())
	require.NotZero(t, friendshipID)

	// Addressee accepts → 200, status flips on the row.
	successBody(env.expect.POST("/api/friends/accept").
		WithHeader(middleware.MMCookieHeader, cookieAddr).
		WithJSON(map[string]any{"friendship_id": friendshipID}).
		Expect().Status(200)).
		Value("status").IsEqual("accepted")

	got, err := env.friends.GetFriendship(context.Background(), requester, addressee)
	require.NoError(t, err)
	require.Equal(t, repo.FriendshipAccepted, got.Status,
		"row must flip to accepted after POST /api/friends/accept")
}

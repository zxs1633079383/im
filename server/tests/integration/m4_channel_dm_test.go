//go:build integration

package integration

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"

	"im-server/internal/middleware"
	"im-server/internal/testutil"
)

// TestM4ChannelCreateDM — happy path: caller hits POST /api/channels/dm with
// a peer userId, gets back a 201 + the new channel, and the persisted row
// has team_id frozen to caller.companyId. A second call with the same peer
// must return 200 (idempotent) on the same channel id.
func TestM4ChannelCreateDM(t *testing.T) {
	env := newM4Env(t)
	cookieA, _ := env.seedUser(10)
	_, userB := env.seedUser(11)

	// First call → 201 + persisted DM with team_id == caller.companyId.
	//
	// v0.7.4 contract change: companyId is sourced from the `companyId`
	// request header (TeamIDFromCtx), no longer from the Redis MattermostUser
	// payload. Tests must stamp it explicitly — see middleware/mattermost_cookie.go
	// MattermostUser comment and internal/testutil/cookie_fixture.go example.
	created := successBody(env.expect.POST("/api/channels/dm").
		WithHeader(middleware.MMCookieHeader, cookieA).
		WithHeader(middleware.MMTeamHeader, testutil.RealCompanyID).
		WithJSON(map[string]any{"peer_id": userB}).
		Expect().Status(201))

	created.Value("type").Number().IsEqual(1) // ChannelTypeDM
	createdID := created.Value("id").String().Raw()
	require.NotZero(t, createdID, "DM channel id must be non-zero")

	ch, err := env.channels.GetByID(context.Background(), createdID)
	require.NoError(t, err)
	require.NotNil(t, ch.TeamID, "team_id must be denormalised onto the DM row")
	require.Equal(t, testutil.RealCompanyID, *ch.TeamID,
		"team_id should equal caller.companyId frozen at create time")

	// Second call with same peer → 200 + same channel id (idempotent).
	repeat := successBody(env.expect.POST("/api/channels/dm").
		WithHeader(middleware.MMCookieHeader, cookieA).
		WithHeader(middleware.MMTeamHeader, testutil.RealCompanyID).
		WithJSON(map[string]any{"peer_id": userB}).
		Expect().Status(200))
	repeat.Value("id").String().IsEqual(createdID)
}

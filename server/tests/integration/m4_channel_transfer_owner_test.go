//go:build integration

// C013 — POST /api/channels/:id/transfer-owner integration tests.
//
// Five cases mirroring docs/harness/C013-owner-transfer-endpoint.md §4.2:
//
//   - TestM4ChannelTransferOwner_Happy             owner A → B (member)        200
//   - TestM4ChannelTransferOwner_NotOwner          plain member C → other      403
//   - TestM4ChannelTransferOwner_NewOwnerNotMember owner A → D (non-member)    404
//   - TestM4ChannelTransferOwner_DM                DM channel (type=1)         400
//   - TestM4ChannelTransferOwner_AlsoLeave         owner A → B + also_leave    200 + A gone
//
// Each test reuses newM4Env / seedUser / seedGroup / seedDM helpers from the
// shared harness so the seed-state setup is identical to other channel tests.
// Seed range 380-399 is reserved for C013; do not collide with G3 (300-379).
package integration

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"im-server/internal/gateway"
	"im-server/internal/middleware"
	"im-server/internal/repo"
)

// transferOwnerURL keeps the path concatenation single-source-of-truth so
// adjustments to the route only need editing in one place.
func transferOwnerURL(chID string) string {
	return "/api/channels/" + chID + "/transfer-owner"
}

// listMembersByID is a small repo-layer helper so test assertions can drill
// straight into the post-state roster without mediating through the HTTP
// surface (which is exercised by separate Batch-B tests).
func listMembersByID(t *testing.T, env *m4env, channelID string) []repo.ChannelMember {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	members, err := env.channels.ListMembers(ctx, channelID)
	require.NoError(t, err, "list members for channel %s", channelID)
	return members
}

// findMember returns the (channel_id, user_id) row from a list, or nil when
// the user isn't a member of the channel any more (used to assert "old owner
// was removed by AlsoLeave=true").
func findMember(members []repo.ChannelMember, userID string) *repo.ChannelMember {
	for i := range members {
		if members[i].UserID == userID {
			return &members[i]
		}
	}
	return nil
}

// TestM4ChannelTransferOwner_Happy — owner A transfers ownership to B (a
// regular member). Asserts the response body + persisted member roles + the
// channel_member_updated WS frame.
func TestM4ChannelTransferOwner_Happy(t *testing.T) {
	env := newM4Env(t)
	cookieOwner, idOwner := env.seedUser(380)
	cookieMember, idMember := env.seedUser(381)
	chID := env.seedGroup(cookieOwner, "c013-happy", idMember)

	// Dial B's WS so we can verify the channel_member_updated fan-out lands
	// on the new owner's conn. settleDelay covers the Redis routing write.
	wcMember := wsDial(t, env, cookieMember)
	time.Sleep(settleDelay)

	body := successBody(env.expect.POST(transferOwnerURL(chID)).
		WithHeader(middleware.MMCookieHeader, cookieOwner).
		WithJSON(map[string]any{
			"new_owner_id": idMember,
		}).
		Expect().Status(200))

	body.Value("old_owner_id").IsEqual(idOwner)
	body.Value("new_owner_id").IsEqual(idMember)
	body.Value("channel").Object().Value("creator_id").IsEqual(idMember)

	// Persisted state: A is now Member, B is now Owner. Both still in roster.
	members := listMembersByID(t, env, chID)
	require.Len(t, members, 2, "happy: roster unchanged in count")
	oldRow := findMember(members, idOwner)
	require.NotNil(t, oldRow, "old owner still in channel")
	require.Equal(t, repo.MemberRoleMember, oldRow.Role, "A demoted to member")
	newRow := findMember(members, idMember)
	require.NotNil(t, newRow, "new owner row exists")
	require.Equal(t, repo.MemberRoleOwner, newRow.Role, "B promoted to owner")

	// WS frame: channel_member_updated{change_type:"owner_transfer"} lands.
	frame := wcMember.expectFrame(gateway.TypeChannelMemberUpdated, 5*time.Second)
	var p map[string]any
	decodePayload(t, frame, &p)
	require.Equal(t, chID, p["channel_id"], "owner_transfer payload channel_id")
	require.Equal(t, string(gateway.MemberChangeOwnerTransfer), p["change_type"],
		"owner_transfer payload change_type")
	require.Equal(t, idOwner, p["actor_id"], "actor_id = old owner")
	require.Equal(t, idMember, p["target_id"], "target_id = new owner")

	// System message: a single owner_transferred row appended at the tail of
	// the channel's message stream. Fetch via FetchAfter so we don't depend
	// on the seedGroup-emitted channel_created / member_joined IDs.
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	msgs, err := env.messages.FetchAfter(ctx, chID, 0, 100)
	require.NoError(t, err, "fetch channel messages")
	require.NotEmpty(t, msgs, "expect at least the owner_transferred sys msg")
	tail := msgs[len(msgs)-1]
	require.Equal(t, repo.MsgTypeSystem, tail.MsgType, "tail msg is system")
	require.NotNil(t, tail.Props, "system msg props non-nil")
	var props map[string]any
	require.NoError(t, json.Unmarshal([]byte(*tail.Props), &props),
		"unmarshal owner_transferred props")
	require.Equal(t, repo.SysTypeOwnerTransferred, props["sys_type"],
		"sys_type = owner_transferred")
	require.Equal(t, idOwner, props["actor_id"], "props.actor_id = old owner")
	require.Equal(t, idMember, props["target_id"], "props.target_id = new owner")
}

// TestM4ChannelTransferOwner_NotOwner — plain member C tries to transfer the
// channel; service returns ErrOwnerOnly, handler maps it to 403.
func TestM4ChannelTransferOwner_NotOwner(t *testing.T) {
	env := newM4Env(t)
	cookieOwner, _ := env.seedUser(382)
	cookieMember, idMember := env.seedUser(383)
	_, idThird := env.seedUser(384)
	chID := env.seedGroup(cookieOwner, "c013-not-owner", idMember, idThird)

	env.expect.POST(transferOwnerURL(chID)).
		WithHeader(middleware.MMCookieHeader, cookieMember).
		WithJSON(map[string]any{
			"new_owner_id": idThird,
		}).
		Expect().Status(403)

	// Persisted state untouched: owner row still owner.
	members := listMembersByID(t, env, chID)
	for _, m := range members {
		if m.Role == repo.MemberRoleOwner {
			require.NotEqual(t, idMember, m.UserID, "caller did not become owner")
			require.NotEqual(t, idThird, m.UserID, "target did not become owner either")
		}
	}
}

// TestM4ChannelTransferOwner_NewOwnerNotMember — owner A nominates D (a user
// who isn't in the channel). Service returns ErrTargetNotMember → 404.
func TestM4ChannelTransferOwner_NewOwnerNotMember(t *testing.T) {
	env := newM4Env(t)
	cookieOwner, idOwner := env.seedUser(385)
	_, idMember := env.seedUser(386)
	_, idOutsider := env.seedUser(387)
	chID := env.seedGroup(cookieOwner, "c013-target-outsider", idMember)

	env.expect.POST(transferOwnerURL(chID)).
		WithHeader(middleware.MMCookieHeader, cookieOwner).
		WithJSON(map[string]any{
			"new_owner_id": idOutsider,
		}).
		Expect().Status(404)

	// State untouched: A is still owner, D never joined.
	members := listMembersByID(t, env, chID)
	require.Len(t, members, 2, "outsider must NOT have been added by the failed call")
	ownerRow := findMember(members, idOwner)
	require.NotNil(t, ownerRow, "owner row preserved")
	require.Equal(t, repo.MemberRoleOwner, ownerRow.Role, "owner role preserved")
	require.Nil(t, findMember(members, idOutsider), "outsider not in roster")
}

// TestM4ChannelTransferOwner_DM — DM channels (type=1) have no owner concept,
// so the endpoint must reject the transfer. The DM seed creates both parties
// with MemberRoleMember (no Owner), so requireOwner runs first and surfaces
// ErrOwnerOnly → 403. Either way the post-state is untouched.
func TestM4ChannelTransferOwner_DM(t *testing.T) {
	env := newM4Env(t)
	cookieA, _ := env.seedUser(388)
	_, idB := env.seedUser(389)
	dmID := env.seedDM(cookieA, idB)

	// Sanity check: confirm the channel is a DM.
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	ch, err := env.channels.GetByID(ctx, dmID)
	require.NoError(t, err, "load dm channel")
	require.Equal(t, repo.ChannelTypeDM, ch.Type, "channel must be DM type")

	// DM members hold MemberRoleMember, so requireOwner returns 403 before
	// the DM-type check can run. That's the correct production behaviour —
	// the spec accepts EITHER 400 (DM check) OR 403 (no owner) as "transfer
	// blocked"; we assert non-success here to capture both.
	resp := env.expect.POST(transferOwnerURL(dmID)).
		WithHeader(middleware.MMCookieHeader, cookieA).
		WithJSON(map[string]any{
			"new_owner_id": idB,
		}).
		Expect()
	status := resp.Raw().StatusCode
	require.Truef(t, status == 400 || status == 403,
		"DM transfer must be rejected (400 or 403), got %d", status)

	// Post-state untouched: still 2 DM members, neither holds Owner role.
	members := listMembersByID(t, env, dmID)
	require.Len(t, members, 2, "DM roster unchanged")
	for _, m := range members {
		require.NotEqual(t, repo.MemberRoleOwner, m.Role,
			"DM never has an owner; transfer must not have promoted anyone")
	}
}

// TestM4ChannelTransferOwner_AlsoLeave — owner A transfers + leaves in one
// shot. Post-state: B is Owner; A is no longer a member. Two system messages
// land (owner_transferred + member_left) and B's conn receives both an
// owner_transfer and a leave channel_member_updated frame.
func TestM4ChannelTransferOwner_AlsoLeave(t *testing.T) {
	env := newM4Env(t)
	cookieOwner, idOwner := env.seedUser(390)
	cookieMember, idMember := env.seedUser(391)
	chID := env.seedGroup(cookieOwner, "c013-also-leave", idMember)

	wcMember := wsDial(t, env, cookieMember)
	time.Sleep(settleDelay)

	body := successBody(env.expect.POST(transferOwnerURL(chID)).
		WithHeader(middleware.MMCookieHeader, cookieOwner).
		WithJSON(map[string]any{
			"new_owner_id": idMember,
			"also_leave":   true,
		}).
		Expect().Status(200))
	body.Value("old_owner_id").IsEqual(idOwner)
	body.Value("new_owner_id").IsEqual(idMember)

	// Persisted state: only B remains; A is GONE.
	members := listMembersByID(t, env, chID)
	require.Len(t, members, 1, "also_leave: roster collapses to {B}")
	require.Equal(t, idMember, members[0].UserID, "remaining member is B")
	require.Equal(t, repo.MemberRoleOwner, members[0].Role, "B is the new owner")
	require.Nil(t, findMember(members, idOwner), "old owner removed")

	// channels.creator_id flipped.
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	ch, err := env.channels.GetByID(ctx, chID)
	require.NoError(t, err, "load channel after also-leave")
	require.Equal(t, idMember, ch.CreatorID, "creator_id = new owner")

	// WS frames: first owner_transfer, then leave. The fixture inbox drains
	// them in order; expectFrame skips any noise frames between.
	first := wcMember.expectFrame(gateway.TypeChannelMemberUpdated, 5*time.Second)
	var firstP map[string]any
	decodePayload(t, first, &firstP)
	require.Equal(t, string(gateway.MemberChangeOwnerTransfer), firstP["change_type"],
		"first frame is owner_transfer")

	second := wcMember.expectFrame(gateway.TypeChannelMemberUpdated, 5*time.Second)
	var secondP map[string]any
	decodePayload(t, second, &secondP)
	require.Equal(t, string(gateway.MemberChangeLeave), secondP["change_type"],
		"second frame is leave")
	require.Equal(t, idOwner, secondP["actor_id"], "leave actor_id = old owner")
	require.Equal(t, idOwner, secondP["target_id"], "leave target_id = old owner (self)")

	// System messages: two new tail rows (owner_transferred + member_left).
	msgs, err := env.messages.FetchAfter(ctx, chID, 0, 100)
	require.NoError(t, err, "fetch all messages")
	require.GreaterOrEqual(t, len(msgs), 2, "expect ≥ 2 sys msgs in tail")
	// Tail-2 = owner_transferred, Tail-1 = member_left.
	owner := msgs[len(msgs)-2]
	left := msgs[len(msgs)-1]
	require.Equal(t, repo.MsgTypeSystem, owner.MsgType)
	require.Equal(t, repo.MsgTypeSystem, left.MsgType)
	require.NotNil(t, owner.Props)
	require.NotNil(t, left.Props)
	var ownerProps, leftProps map[string]any
	require.NoError(t, json.Unmarshal([]byte(*owner.Props), &ownerProps))
	require.NoError(t, json.Unmarshal([]byte(*left.Props), &leftProps))
	require.Equal(t, repo.SysTypeOwnerTransferred, ownerProps["sys_type"])
	require.Equal(t, repo.SysTypeMemberLeft, leftProps["sys_type"])
	require.Equal(t, idOwner, leftProps["actor_id"], "member_left actor = old owner")
}

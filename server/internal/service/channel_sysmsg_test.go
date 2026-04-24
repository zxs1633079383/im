package service_test

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"

	"im-server/internal/repo"
	"im-server/internal/repo/mocks"
	"im-server/internal/service"
)

// newChannelSvcWithMessages wires a ChannelService with a real message mock so
// system-message emission can be asserted. Separate from newChannelSvc which
// passes nil and disables the side channel.
func newChannelSvcWithMessages(t *testing.T) (
	*service.ChannelService,
	*mocks.ChannelRepoMock,
	*mocks.UserRepoMock,
	*mocks.MessageRepoMock,
) {
	t.Helper()
	ch := mocks.NewChannelRepoMock(t)
	us := mocks.NewUserRepoMock(t)
	ms := mocks.NewMessageRepoMock(t)
	return service.NewChannelService(ch, us, ms), ch, us, ms
}

// matchSysType returns a MatchedBy predicate asserting the props map carries
// the expected sys_type plus every (key, value) pair in mustHave.
func matchSysType(expected string, mustHave map[string]any) any {
	return mock.MatchedBy(func(props map[string]any) bool {
		if got, _ := props[repo.SysTypeKey].(string); got != expected {
			return false
		}
		for k, v := range mustHave {
			if props[k] != v {
				return false
			}
		}
		return true
	})
}

func TestChannel_Update_EmitsChannelUpdatedSystemMessage(t *testing.T) {
	svc, ch, _, ms := newChannelSvcWithMessages(t)

	ch.EXPECT().GetMember(mock.Anything, int64(77), int64(1)).
		Return(&repo.ChannelMember{UserID: 1, ChannelID: 77, Role: repo.MemberRoleOwner}, nil)
	ch.EXPECT().Update(mock.Anything, int64(77), "new-name", "avatar.png").Return(nil)
	ch.EXPECT().GetByID(mock.Anything, int64(77)).
		Return(&repo.Channel{ID: 77, Name: "new-name", AvatarURL: "avatar.png"}, nil)

	ms.EXPECT().PostSystemMessage(
		mock.Anything, (*gorm.DB)(nil), int64(77), int64(1),
		matchSysType(repo.SysTypeChannelUpdated, map[string]any{
			"actor_id":   int64(1),
			"name":       "new-name",
			"avatar_url": "avatar.png",
		}),
	).Return(&repo.Message{ID: 1, Seq: 5, MsgType: repo.MsgTypeSystem}, nil)

	got, err := svc.Update(context.Background(), 77, 1, "new-name", "avatar.png")
	require.NoError(t, err)
	require.Equal(t, "new-name", got.Name)
}

func TestChannel_Update_BestEffortSkipsOnPostFailure(t *testing.T) {
	// If the underlying Update succeeded, a failing PostSystemMessage must
	// not flip the overall result to an error — rename stays, marker lost.
	svc, ch, _, ms := newChannelSvcWithMessages(t)

	ch.EXPECT().GetMember(mock.Anything, int64(77), int64(1)).
		Return(&repo.ChannelMember{Role: repo.MemberRoleOwner}, nil)
	ch.EXPECT().Update(mock.Anything, int64(77), "n", "").Return(nil)
	ch.EXPECT().GetByID(mock.Anything, int64(77)).Return(&repo.Channel{ID: 77, Name: "n"}, nil)

	ms.EXPECT().PostSystemMessage(
		mock.Anything, (*gorm.DB)(nil), int64(77), int64(1), mock.Anything,
	).Return(nil, errors.New("seq alloc failed"))

	_, err := svc.Update(context.Background(), 77, 1, "n", "")
	require.NoError(t, err)
}

func TestChannel_AddMember_EmitsMemberJoinedSystemMessage(t *testing.T) {
	svc, ch, _, ms := newChannelSvcWithMessages(t)

	ch.EXPECT().GetMember(mock.Anything, int64(77), int64(1)).
		Return(&repo.ChannelMember{Role: repo.MemberRoleOwner}, nil)
	ch.EXPECT().AddMember(mock.Anything, int64(77), int64(9), repo.MemberRoleMember).Return(nil)
	ch.EXPECT().GetByID(mock.Anything, int64(77)).Return(&repo.Channel{ID: 77, Name: "dev"}, nil)

	ms.EXPECT().PostSystemMessage(
		mock.Anything, (*gorm.DB)(nil), int64(77), int64(1),
		matchSysType(repo.SysTypeMemberJoined, map[string]any{
			"actor_id":  int64(1),
			"target_id": int64(9),
		}),
	).Return(&repo.Message{MsgType: repo.MsgTypeSystem}, nil)

	name, err := svc.AddMember(context.Background(), 77, 1, 9)
	require.NoError(t, err)
	require.Equal(t, "dev", name)
}

// TestChannel_RemoveMember_RunsSystemMessageAndDeleteInOneTx locks in the
// invariant: the system message is inserted BEFORE the DELETE inside a single
// transaction. We drive this by capturing the order of WithinTx's inner calls.
func TestChannel_RemoveMember_RunsSystemMessageAndDeleteInOneTx(t *testing.T) {
	svc, ch, _, ms := newChannelSvcWithMessages(t)

	ch.EXPECT().GetMember(mock.Anything, int64(77), int64(1)).
		Return(&repo.ChannelMember{Role: repo.MemberRoleOwner}, nil)
	ch.EXPECT().GetMember(mock.Anything, int64(77), int64(9)).
		Return(&repo.ChannelMember{Role: repo.MemberRoleMember}, nil)

	// Stand-in tx: since it's only passed through, nil is fine — the real
	// code only checks non-nil when deciding which gorm.DB to chain from.
	var order []string
	ch.EXPECT().WithinTx(mock.Anything, mock.Anything).
		RunAndReturn(func(_ context.Context, fn func(*gorm.DB) error) error {
			return fn(&gorm.DB{})
		})

	ms.EXPECT().PostSystemMessage(
		mock.Anything, mock.Anything, int64(77), int64(1),
		matchSysType(repo.SysTypeMemberRemoved, map[string]any{
			"actor_id":  int64(1),
			"target_id": int64(9),
		}),
	).RunAndReturn(func(context.Context, *gorm.DB, int64, int64, map[string]any) (*repo.Message, error) {
		order = append(order, "post_sys")
		return &repo.Message{MsgType: repo.MsgTypeSystem}, nil
	})
	ch.EXPECT().RemoveMemberTx(mock.Anything, mock.Anything, int64(77), int64(9)).
		RunAndReturn(func(context.Context, *gorm.DB, int64, int64) error {
			order = append(order, "delete")
			return nil
		})

	err := svc.RemoveMember(context.Background(), 77, 1, 9)
	require.NoError(t, err)
	require.Equal(t, []string{"post_sys", "delete"}, order,
		"system message must be inserted BEFORE the DELETE so the target still receives the push fan-out")
}

func TestChannel_LeaveChannel_EmitsMemberLeftSystemMessage(t *testing.T) {
	svc, ch, _, ms := newChannelSvcWithMessages(t)

	ch.EXPECT().GetMember(mock.Anything, int64(77), int64(5)).
		Return(&repo.ChannelMember{Role: repo.MemberRoleMember}, nil)
	ch.EXPECT().WithinTx(mock.Anything, mock.Anything).
		RunAndReturn(func(_ context.Context, fn func(*gorm.DB) error) error {
			return fn(&gorm.DB{})
		})
	ms.EXPECT().PostSystemMessage(
		mock.Anything, mock.Anything, int64(77), int64(5),
		matchSysType(repo.SysTypeMemberLeft, map[string]any{"actor_id": int64(5)}),
	).Return(&repo.Message{MsgType: repo.MsgTypeSystem}, nil)
	ch.EXPECT().RemoveMemberTx(mock.Anything, mock.Anything, int64(77), int64(5)).Return(nil)

	require.NoError(t, svc.LeaveChannel(context.Background(), 77, 5))
}

func TestChannel_CreateGroup_EmitsChannelCreatedAndPerMemberJoined(t *testing.T) {
	svc, ch, _, ms := newChannelSvcWithMessages(t)

	ch.EXPECT().Create(mock.Anything, mock.Anything).
		Run(func(_ context.Context, c *repo.Channel) { c.ID = 99; c.Name = "team" }).Return(nil)
	ch.EXPECT().AddMember(mock.Anything, int64(99), int64(1), repo.MemberRoleOwner).Return(nil)
	ch.EXPECT().AddMember(mock.Anything, int64(99), int64(2), repo.MemberRoleMember).Return(nil)
	ch.EXPECT().AddMember(mock.Anything, int64(99), int64(3), repo.MemberRoleMember).Return(nil)

	// One channel_created + one member_joined per non-creator, in order.
	ms.EXPECT().PostSystemMessage(
		mock.Anything, (*gorm.DB)(nil), int64(99), int64(1),
		matchSysType(repo.SysTypeChannelCreated, map[string]any{"actor_id": int64(1), "name": "team"}),
	).Return(&repo.Message{MsgType: repo.MsgTypeSystem}, nil)
	ms.EXPECT().PostSystemMessage(
		mock.Anything, (*gorm.DB)(nil), int64(99), int64(1),
		matchSysType(repo.SysTypeMemberJoined, map[string]any{"actor_id": int64(1), "target_id": int64(2)}),
	).Return(&repo.Message{MsgType: repo.MsgTypeSystem}, nil)
	ms.EXPECT().PostSystemMessage(
		mock.Anything, (*gorm.DB)(nil), int64(99), int64(1),
		matchSysType(repo.SysTypeMemberJoined, map[string]any{"actor_id": int64(1), "target_id": int64(3)}),
	).Return(&repo.Message{MsgType: repo.MsgTypeSystem}, nil)

	_, _, err := svc.CreateGroup(context.Background(), 1, "team", []int64{1, 2, 3})
	require.NoError(t, err)
}

// Sanity: the raw props builder is stable — guards against accidental rename
// of the sys_type string constant which would silently break clients.
func TestSysTypeConstantsAreStable(t *testing.T) {
	cases := []struct {
		got, want string
	}{
		{repo.SysTypeChannelCreated, "channel_created"},
		{repo.SysTypeChannelUpdated, "channel_updated"},
		{repo.SysTypeMemberJoined, "member_joined"},
		{repo.SysTypeMemberRemoved, "member_removed"},
		{repo.SysTypeMemberLeft, "member_left"},
		{repo.SysTypeKey, "sys_type"},
	}
	for _, c := range cases {
		require.Equal(t, c.want, c.got)
	}
	// Round-trip a representative payload — the wire shape must be a plain
	// JSON object with string-keyed fields, not a typed struct.
	payload, err := json.Marshal(map[string]any{
		repo.SysTypeKey: repo.SysTypeMemberJoined,
		"actor_id":      int64(1),
		"target_id":     int64(9),
	})
	require.NoError(t, err)
	require.Contains(t, string(payload), `"sys_type":"member_joined"`)
}

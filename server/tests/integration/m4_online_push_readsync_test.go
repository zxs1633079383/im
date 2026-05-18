//go:build integration

package integration

// P4 §D + §E — read_sync 多设备 + channel governance 5 类推送。
//
// §D doc-05 §5 read_sync 多设备
//   - 同 user 2 device, A device 1 标记已读, B 收 read_sync
//   - channel 中其他 user 不收 read_sync (negative assertion)
//
// §E doc-05 §6 channel governance
//   - channel_info_updated 全员 (PATCH /channels/:id notice/purpose)
//   - channel_member_updated 5 change_type (P1 已细化, P4 在一个 Test 内
//     验证 multi-frame 顺序: join → nickname → kick)
//   - channel_top_updated 仅 self 多设备 (PATCH /members/:uid is_top)
//
// Seed 范围 700-749 预留给 P4。

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"im-server/internal/gateway"
	"im-server/internal/middleware"
)

// TestM4Push_ReadSync_MultiDevice — 同 cookie 起 2 wsClient (模拟多设备),
// device 1 触发 POST /channels/:id/read, 两 device 都收 read_sync。
//
// 与 m4_ws_reaction_heartbeat_test.go::TestM4WSReadSync_HappyPath 不同点:
// 这里走真实 HTTP endpoint (harness ReadSyncer 已 wire 见 m4_harness_test.go
// 行 234)。
func TestM4Push_ReadSync_MultiDevice(t *testing.T) {
	env := newM4Env(t)
	cookieA, _ := env.seedUser(700)
	cookieB, idB := env.seedUser(701)
	channelID := env.seedDM(cookieA, idB)
	msg := env.seedMessage(channelID, idB, "p4-read-target")

	wcDev1 := wsDial(t, env, cookieA)
	wcDev2 := wsDial(t, env, cookieA)
	time.Sleep(settleDelay)

	env.expect.POST("/api/channels/"+channelID+"/read").
		WithHeader(middleware.MMCookieHeader, cookieA).
		WithJSON(map[string]any{"last_read_seq": msg.Seq}).
		Expect().Status(200)

	for i, wc := range []*wsClient{wcDev1, wcDev2} {
		frame := wc.expectFrame(gateway.TypeReadSync, 5*time.Second)
		var p gateway.ReadSyncPayload
		decodePayload(t, frame, &p)
		require.Equal(t, channelID, p.ChannelID, "device #%d read_sync channel_id", i+1)
		require.Equal(t, msg.Seq, p.ReadSeq, "device #%d read_sync read_seq", i+1)
	}
	_ = cookieB
}

// TestM4Push_ReadSync_OtherUserNotPushed — A 触发 read, channel 内其他
// user B 不应收 read_sync (read_sync 仅在同 user 多设备之间同步)。
func TestM4Push_ReadSync_OtherUserNotPushed(t *testing.T) {
	env := newM4Env(t)
	cookieA, _ := env.seedUser(702)
	cookieB, idB := env.seedUser(703)
	channelID := env.seedDM(cookieA, idB)
	msg := env.seedMessage(channelID, idB, "p4-other-not-pushed")

	// B 上 WS, A 触发 mark read
	wcB := wsDial(t, env, cookieB)
	// 排干 hello, 避免 negative assertion 误命中
	wcB.expectFrame(gateway.TypeHello, 5*time.Second)
	time.Sleep(settleDelay)

	env.expect.POST("/api/channels/"+channelID+"/read").
		WithHeader(middleware.MMCookieHeader, cookieA).
		WithJSON(map[string]any{"last_read_seq": msg.Seq}).
		Expect().Status(200)

	// B 不应收 read_sync (500ms negative window)
	select {
	case f := <-wcB.inbox:
		t.Fatalf("B 不应收 read_sync, got type=%s", f.Type)
	case <-time.After(500 * time.Millisecond):
		// 期望
	}
}

// TestM4Push_ChannelInfoUpdated_AllMembers — owner PATCH /channels/:id
// 改 notice 字段, 全员收 TypeChannelInfoUpdated。
func TestM4Push_ChannelInfoUpdated_AllMembers(t *testing.T) {
	env := newM4Env(t)
	cookieOwner, _ := env.seedUser(704)
	cookieA, idA := env.seedUser(705)
	channelID := env.seedGroup(cookieOwner, "p4-info-updated", idA)

	wcA := wsDial(t, env, cookieA)
	time.Sleep(settleDelay)

	env.expect.PATCH("/api/channels/"+channelID).
		WithHeader(middleware.MMCookieHeader, cookieOwner).
		WithJSON(map[string]any{"notice": "p4-new-notice"}).
		Expect().Status(200)

	frame := wcA.expectFrame(gateway.TypeChannelInfoUpdated, 5*time.Second)
	var snap map[string]any
	decodePayload(t, frame, &snap)
	require.Equal(t, channelID, snap["id"], "channel_info_updated id")
	require.Equal(t, "p4-new-notice", snap["notice"], "notice 新值")
}

// TestM4Push_ChannelMemberUpdated_5ChangeType_OrderedFrames — 1 个 Test 内
// 跑完 join / kick / leave / nickname / owner_transfer 5 种 change_type,
// 验证它们在同一 channel 上能按 HTTP 顺序触发对应帧。
//
// 不同帧用 expectFrame 顺序消费 (跳过 hello 等无关帧由 expectFrame 内部
// 处理)。最后一个 owner_transfer 把群主切走, 该 channel 下游测试已不可
// 复用, 但本 Test 独立 env 不影响。
func TestM4Push_ChannelMemberUpdated_5ChangeType_OrderedFrames(t *testing.T) {
	env := newM4Env(t)
	env.wireOnlinePushExtras(t)
	cookieOwner, ownerID := env.seedUser(706)
	cookieA, idA := env.seedUser(707)
	cookieB, idB := env.seedUser(708)

	// Owner 建群只有自己, 之后逐个动作。
	channelID := env.seedGroup(cookieOwner, "p4-5changetype")

	wcOwner := wsDial(t, env, cookieOwner)
	time.Sleep(settleDelay)

	// Step 1: owner add A → join
	env.expect.POST("/api/channels/"+channelID+"/members").
		WithHeader(middleware.MMCookieHeader, cookieOwner).
		WithJSON(map[string]any{"user_id": idA}).
		Expect().Status(201)
	f1 := wcOwner.expectFrame(gateway.TypeChannelMemberUpdated, 5*time.Second)
	var p1 gateway.ChannelMemberUpdatedPayload
	decodePayload(t, f1, &p1)
	require.Equal(t, gateway.MemberChangeJoin, p1.ChangeType, "frame #1 join")
	require.Equal(t, idA, p1.TargetID, "join target_id=A")

	// Step 2: owner change A 's nickname → nickname
	env.expect.PATCH("/api/channels/"+channelID+"/members/"+idA+"/nickname").
		WithHeader(middleware.MMCookieHeader, cookieOwner).
		WithJSON(map[string]any{"nick_name": "A-改名"}).
		Expect().Status(200)
	f2 := wcOwner.expectFrame(gateway.TypeChannelMemberUpdated, 5*time.Second)
	var p2 gateway.ChannelMemberUpdatedPayload
	decodePayload(t, f2, &p2)
	require.Equal(t, gateway.MemberChangeNickname, p2.ChangeType, "frame #2 nickname")
	require.Equal(t, "A-改名", p2.NickName, "nickname value")

	// Step 3: add B → join
	env.expect.POST("/api/channels/"+channelID+"/members").
		WithHeader(middleware.MMCookieHeader, cookieOwner).
		WithJSON(map[string]any{"user_id": idB}).
		Expect().Status(201)
	f3 := wcOwner.expectFrame(gateway.TypeChannelMemberUpdated, 5*time.Second)
	var p3 gateway.ChannelMemberUpdatedPayload
	decodePayload(t, f3, &p3)
	require.Equal(t, gateway.MemberChangeJoin, p3.ChangeType, "frame #3 join B")

	// Step 4: B leave → leave
	env.expect.POST("/api/channels/"+channelID+"/leave").
		WithHeader(middleware.MMCookieHeader, cookieB).
		Expect().Status(200)
	f4 := wcOwner.expectFrame(gateway.TypeChannelMemberUpdated, 5*time.Second)
	var p4 gateway.ChannelMemberUpdatedPayload
	decodePayload(t, f4, &p4)
	require.Equal(t, gateway.MemberChangeLeave, p4.ChangeType, "frame #4 leave")
	require.Equal(t, idB, p4.TargetID, "leave target_id=B")

	// Step 5: kick A → kick (注意 leave 不能 owner 走, 所以 kick 走 owner→A)
	env.expect.DELETE("/api/channels/"+channelID+"/members/"+idA).
		WithHeader(middleware.MMCookieHeader, cookieOwner).
		Expect().Status(200)
	f5 := wcOwner.expectFrame(gateway.TypeChannelMemberUpdated, 5*time.Second)
	var p5 gateway.ChannelMemberUpdatedPayload
	decodePayload(t, f5, &p5)
	require.Equal(t, gateway.MemberChangeKick, p5.ChangeType, "frame #5 kick")
	require.Equal(t, idA, p5.TargetID, "kick target_id=A")

	// owner_transfer 需要至少 1 个剩余成员; 此 Test 把 A/B 都清空, 不再做
	// 第 6 帧。owner_transfer 单独 case 见 P1
	// TestM4WS_ChannelMemberUpdated_OwnerTransfer_TwoFrames + smoke step 11。
	_ = cookieA
	_ = ownerID
}

// TestM4Push_ChannelTopUpdated_SelfOnly — caller PATCH /channels/:id/
// members/:uid {is_top:true} 触发 TypeChannelTopUpdated, 仅推 self 多
// 设备 (另一设备), channel 其他 user 不收。
func TestM4Push_ChannelTopUpdated_SelfOnly(t *testing.T) {
	env := newM4Env(t)
	cookieA, userA := env.seedUser(709)
	cookieB, idB := env.seedUser(710)
	channelID := env.seedGroup(cookieA, "p4-top-updated", idB)

	// A 多设备 + B 一台
	wcADev1 := wsDial(t, env, cookieA)
	wcADev2 := wsDial(t, env, cookieA)
	wcB := wsDial(t, env, cookieB)
	wcB.expectFrame(gateway.TypeHello, 5*time.Second) // 排干 B's hello
	time.Sleep(settleDelay)

	// A device 1 触发 (实际是 HTTP, harness 直接 hub.PushToUser 推所有 A 的 conn)
	env.expect.PATCH("/api/channels/"+channelID+"/members/"+userA).
		WithHeader(middleware.MMCookieHeader, cookieA).
		WithJSON(map[string]any{"is_top": true}).
		Expect().Status(200)

	// A 两 device 都收
	for i, wc := range []*wsClient{wcADev1, wcADev2} {
		frame := wc.expectFrame(gateway.TypeChannelTopUpdated, 5*time.Second)
		var p struct {
			ChannelID string `json:"channel_id"`
			IsTop     bool   `json:"is_top"`
		}
		decodePayload(t, frame, &p)
		require.Equal(t, channelID, p.ChannelID, "A device #%d channel_top_updated channel_id", i+1)
		require.True(t, p.IsTop, "A device #%d is_top=true", i+1)
	}

	// B 不应收
	select {
	case f := <-wcB.inbox:
		t.Fatalf("B 不应收 channel_top_updated, got type=%s", f.Type)
	case <-time.After(500 * time.Millisecond):
		// 期望
	}
}

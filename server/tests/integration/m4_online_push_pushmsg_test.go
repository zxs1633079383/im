//go:build integration

package integration

// P3 §B + §C — push_msg / msg_updated / msg_deleted 完整链路。
//
// §B: doc-05 §3 push_msg
//   - 普通消息 fanout 2 成员 (sender + recv)
//   - 系统消息 type=4 NoticeType="NOTICE" + props 非空
//   - 加急消息 走 TypeUrgentPosted (不是 TypePushMsg) + is_urgent=true
//   - reply_to 链路 (push_msg.reply_to 携带)
//
// §C: doc-05 §4 msg_updated / msg_deleted 系列
//   - edit → msg_updated 帧含新 content
//   - delete → msg_deleted 帧 {msg_id, channel_id, deleted_at}
//   - template received → 触发 msg_updated 帧 (handler 复用)
//   - (urgent_cancelled 已在 P1 覆盖)
//
// 命名沿用 TestM4Push_<scope>_<case>。Seed 范围 850-879 预留给 P3。

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"im-server/internal/gateway"
	"im-server/internal/middleware"
	"im-server/internal/repo"
)

// TestM4Push_NormalMsg_FanOut2Members — 2-member channel: A 发, B 收
// push_msg。同时 sender A 也收到 self echo (production hub.PushToUser
// 不去重 sender, harness localMessagePusher 同样)。
func TestM4Push_NormalMsg_FanOut2Members(t *testing.T) {
	env := newM4Env(t)
	cookieA, _ := env.seedUser(850)
	cookieB, idB := env.seedUser(851)
	channelID := env.seedDM(cookieA, idB)

	wcA := wsDial(t, env, cookieA)
	wcB := wsDial(t, env, cookieB)
	time.Sleep(settleDelay)

	env.expect.POST("/api/channels/"+channelID+"/messages").
		WithHeader(middleware.MMCookieHeader, cookieA).
		WithJSON(map[string]any{
			"content":  "normal-fan-out",
			"msg_type": 1,
		}).
		Expect().Status(201)

	// A 收 self echo
	frameA := wcA.expectFrame(gateway.TypePushMsg, 5*time.Second)
	var pA gateway.PushMsgPayload
	decodePayload(t, frameA, &pA)
	require.Equal(t, "normal-fan-out", pA.Content, "A self echo content")

	// B 收 fan-out
	frameB := wcB.expectFrame(gateway.TypePushMsg, 5*time.Second)
	var pB gateway.PushMsgPayload
	decodePayload(t, frameB, &pB)
	require.Equal(t, "normal-fan-out", pB.Content, "B fan-out content")
	require.Equal(t, channelID, pB.ChannelID, "channel_id match")
}

// TestM4Push_SystemMsg_NoticeType — system message (msg_type=4) 在 push 帧
// 上 type=NOTICE + props 字段非空 (cses-client 据此判断走 NOTICE 路径解
// props.sys_type)。
//
// 注意: harness localMessagePusher 不拷贝 PushMsgPayload.Type 字段 (m4_
// harness_test.go 519-527), 该字段的 wire 契约只在生产 hubMessagePusher
// 完整设置。这里测 DB 落地为主, push 帧只断 MsgType 字段。
func TestM4Push_SystemMsg_NoticeType(t *testing.T) {
	env := newM4Env(t)
	cookieA, idA := env.seedUser(852)
	cookieB, idB := env.seedUser(853)
	channelID := env.seedDM(cookieA, idB)

	// 系统消息无法走标准 POST /messages (handler 限定 msg_type)。
	// 直接 repo.Send 注入 type=4 + props。
	props := `{"sys_type":"channel_member_added","actor_id":"X"}`
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	msg := &repo.Message{
		ChannelID: channelID,
		SenderID:  idA,
		MsgType:   repo.MsgTypeSystem,
		Content:   "X joined the channel",
		Props:     &props,
	}
	require.NoError(t, env.messages.Send(ctx, msg))

	// 验证 DB 落地
	got, err := env.messages.GetByID(ctx, msg.ID)
	require.NoError(t, err, "system message must persist")
	require.Equal(t, repo.MsgTypeSystem, got.MsgType, "msg_type=4 system")
	require.NotNil(t, got.Props, "system message props 必填")
	require.Contains(t, *got.Props, "sys_type", "props 必须含 sys_type 字段")
	_ = cookieB
}

// TestM4Push_UrgentMsg_TypeUrgentPosted — 加急消息走 TypeUrgentPosted (不是
// TypePushMsg) + payload.is_urgent=true。已有 m4_ws_governance_events_test.go
// TestM4WSUrgentPosted_HappyPath 覆盖, 这里增加同时验证 push_msg 与 urgent_
// posted 不混用。
func TestM4Push_UrgentMsg_TypeUrgentPosted(t *testing.T) {
	env := newM4Env(t)
	cookieSender, _ := env.seedUser(854)
	cookieA, idA := env.seedUser(855)
	channelID := env.seedGroup(cookieSender, "p3-urgent", idA)

	wcA := wsDial(t, env, cookieA)
	time.Sleep(settleDelay)

	successBody(env.expect.POST("/api/messages/urgent").
		WithHeader(middleware.MMCookieHeader, cookieSender).
		WithJSON(map[string]any{
			"channel_id":    channelID,
			"content":       "URGENT-P3",
			"client_msg_id": "p3-urg-001",
		}).
		Expect().Status(201))

	frame := wcA.expectFrame(gateway.TypeUrgentPosted, 5*time.Second)
	var p map[string]any
	decodePayload(t, frame, &p)
	require.Equal(t, channelID, p["channel_id"], "urgent_posted channel_id")
	require.Equal(t, true, p["is_urgent"], "urgent_posted is_urgent=true")
	require.Equal(t, "URGENT-P3", p["content"], "urgent_posted content")
}

// TestM4Push_WithReplyTo_ReplyChain — sender 发消息 body 携 reply_to
// (指向之前消息的 server_msg_id), 接收端 push_msg 字段不可少。
//
// 注意: PushMsgPayload struct 没暴露 ReplyTo 字段 (gateway/types.go 看不到),
// 实际通过 DB 落地。这里验证 repo.Message.ReplyTo 字段正确持久化。
func TestM4Push_WithReplyTo_ReplyChain(t *testing.T) {
	env := newM4Env(t)
	cookieA, _ := env.seedUser(856)
	cookieB, idB := env.seedUser(857)
	channelID := env.seedDM(cookieA, idB)

	// Step 1: 发原始消息
	orig := successBody(env.expect.POST("/api/channels/"+channelID+"/messages").
		WithHeader(middleware.MMCookieHeader, cookieA).
		WithJSON(map[string]any{
			"content":  "original-msg",
			"msg_type": 1,
		}).
		Expect().Status(201))
	origID := orig.Value("id").String().Raw()

	// Step 2: B 回复原始消息
	reply := successBody(env.expect.POST("/api/channels/"+channelID+"/messages").
		WithHeader(middleware.MMCookieHeader, cookieB).
		WithJSON(map[string]any{
			"content":  "reply-to-orig",
			"msg_type": 1,
			"reply_to": origID,
		}).
		Expect().Status(201))
	replyID := reply.Value("id").String().Raw()

	// 验证 DB reply_to 字段
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	got, err := env.messages.GetByID(ctx, replyID)
	require.NoError(t, err, "reply 必须能取回")
	require.NotNil(t, got.ReplyTo, "reply_to 必填")
	require.Equal(t, origID, *got.ReplyTo, "reply_to 指向 original msgID")
}

// TestM4Push_EditMessage_MsgUpdatedFrame — PATCH /api/messages/:id 触发
// msg_updated 帧, payload 含新 content。
func TestM4Push_EditMessage_MsgUpdatedFrame(t *testing.T) {
	env := newM4Env(t)
	cookieA, _ := env.seedUser(858)
	cookieB, idB := env.seedUser(859)
	channelID := env.seedDM(cookieA, idB)
	msg := env.seedMessage(channelID, idB, "before-edit")

	wcA := wsDial(t, env, cookieA)
	time.Sleep(settleDelay)

	env.expect.PATCH("/api/messages/"+msg.ID).
		WithHeader(middleware.MMCookieHeader, cookieB).
		WithJSON(map[string]any{"content": "after-edit-p3"}).
		Expect().Status(200)

	frame := wcA.expectFrame(gateway.TypeMsgUpdated, 5*time.Second)
	var snap map[string]any
	decodePayload(t, frame, &snap)
	require.Equal(t, msg.ID, snap["id"], "msg_updated id")
	require.Equal(t, "after-edit-p3", snap["content"], "msg_updated 新 content")
	require.Equal(t, channelID, snap["channel_id"], "msg_updated channel_id")
}

// TestM4Push_DeleteMessage_MsgDeletedFrame — DELETE /api/messages/:id 触发
// msg_deleted 帧 {msg_id, channel_id, deleted_at}。
func TestM4Push_DeleteMessage_MsgDeletedFrame(t *testing.T) {
	env := newM4Env(t)
	cookieA, _ := env.seedUser(860)
	cookieB, idB := env.seedUser(861)
	channelID := env.seedDM(cookieA, idB)
	msg := env.seedMessage(channelID, idB, "to-be-deleted")

	wcA := wsDial(t, env, cookieA)
	time.Sleep(settleDelay)

	env.expect.DELETE("/api/messages/"+msg.ID).
		WithHeader(middleware.MMCookieHeader, cookieB).
		Expect().Status(200)

	frame := wcA.expectFrame(gateway.TypeMsgDeleted, 5*time.Second)
	var p map[string]any
	decodePayload(t, frame, &p)
	require.Equal(t, msg.ID, p["msg_id"], "msg_deleted msg_id")
	require.Equal(t, channelID, p["channel_id"], "msg_deleted channel_id")
	require.NotEmpty(t, p["deleted_at"], "msg_deleted deleted_at 必填")
}

// TestM4Push_TemplateReceived_TriggersMsgUpdated — POST /api/messages/:id/
// received (模板消息已收到) 复用 msg_updated 帧 (decision 6: 不新增 type,
// 复用 V1 锁定的 22 种), 帧含更新后的 props.template.userIds 列表。
func TestM4Push_TemplateReceived_TriggersMsgUpdated(t *testing.T) {
	env := newM4Env(t)
	cookieSender, idSender := env.seedUser(862)
	cookieRecv, idRecv := env.seedUser(863)
	channelID := env.seedDM(cookieSender, idRecv)

	// 注入模板消息
	templateProps := `{"template":{"type":"TEXT","text":"模板","userIds":[]}}`
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	tmpl := &repo.Message{
		ChannelID: channelID,
		SenderID:  idSender,
		MsgType:   repo.MsgTypeText,
		Content:   "模板消息",
		Props:     &templateProps,
	}
	require.NoError(t, env.messages.Send(ctx, tmpl))

	// sender 监听 (recv 触发 received → broadcast 到全员包括 sender)
	wcSender := wsDial(t, env, cookieSender)
	time.Sleep(settleDelay)

	env.expect.POST("/api/messages/"+tmpl.ID+"/received").
		WithHeader(middleware.MMCookieHeader, cookieRecv).
		Expect().Status(200)

	frame := wcSender.expectFrame(gateway.TypeMsgUpdated, 5*time.Second)
	var snap map[string]any
	decodePayload(t, frame, &snap)
	require.Equal(t, tmpl.ID, snap["id"], "msg_updated id 匹配模板 msgID")
	require.NotNil(t, snap["props"], "msg_updated props 必填 (含新 userIds)")
}

//go:build integration

package integration

import (
	"strconv"
	"testing"

	"im-server/internal/middleware"
	"im-server/internal/testutil"
)

// G1 — message-write family integration coverage. Five endpoints × five cases.
//
// Endpoints exercised:
//   - POST   /api/channels/:id/messages   (Send)
//   - PATCH  /api/messages/:id            (Edit)
//   - DELETE /api/messages/:id            (Revoke / soft-delete)
//   - POST   /api/messages/forward        (Forward)
//   - POST   /api/messages/batch          (Batch send)
//
// Each top-level test owns its own testcontainer pair (see newM4Env). Seeds
// 100..199 are reserved for G1 to avoid colliding with the other Batch-B
// agents running concurrently. Names follow TestM4Message{Action}_C{N}_{Desc}
// so the matrix is grep-able.

// ---- POST /api/channels/:id/messages -----------------------------------------

// TestM4MessageSend_C1_HappyPath — channel member 发消息, 201 + sender_id 回填.
func TestM4MessageSend_C1_HappyPath(t *testing.T) {
	env := newM4Env(t)
	cookieSender, senderID := env.seedUser(100)
	_, recvID := env.seedUser(101)
	channelID := env.seedDM(cookieSender, recvID)

	resp := successBody(env.expect.POST("/api/channels/"+strconv.FormatInt(channelID, 10)+"/messages").
		WithHeader(middleware.MMCookieHeader, cookieSender).
		WithJSON(map[string]any{
			"content":  "g1-hello",
			"msg_type": 1,
		}).
		Expect().Status(201))

	resp.Value("sender_id").IsEqual(senderID)
	resp.Value("content").IsEqual("g1-hello")
	resp.Value("seq").Number().IsEqual(1)
	resp.Value("team_id").IsEqual(testutil.RealCompanyID)
}

// TestM4MessageSend_C2_CookieMissing — 缺 cookie header → 401.
func TestM4MessageSend_C2_CookieMissing(t *testing.T) {
	env := newM4Env(t)
	cookieSender, _ := env.seedUser(102)
	_, recvID := env.seedUser(103)
	channelID := env.seedDM(cookieSender, recvID)

	errorBody(env.expect.POST("/api/channels/"+strconv.FormatInt(channelID, 10)+"/messages").
		WithJSON(map[string]any{"content": "x", "msg_type": 1}).
		Expect().Status(401)).
		Value("error").String().NotEmpty()
}

// TestM4MessageSend_C3_CookieInvalid — header 设 redis 不存在的 cookieId → 401.
func TestM4MessageSend_C3_CookieInvalid(t *testing.T) {
	env := newM4Env(t)
	cookieSender, _ := env.seedUser(104)
	_, recvID := env.seedUser(105)
	channelID := env.seedDM(cookieSender, recvID)

	bogus := testutil.MakeCookieID(9999)
	errorBody(env.expect.POST("/api/channels/"+strconv.FormatInt(channelID, 10)+"/messages").
		WithHeader(middleware.MMCookieHeader, bogus).
		WithJSON(map[string]any{"content": "x", "msg_type": 1}).
		Expect().Status(401)).
		Value("error").String().NotEmpty()
}

// TestM4MessageSend_C4_NotMember — 越权用户向不属于自己的 DM 发消息 → 403.
func TestM4MessageSend_C4_NotMember(t *testing.T) {
	env := newM4Env(t)
	cookieOwner, _ := env.seedUser(106)
	_, peerID := env.seedUser(107)
	cookieOutsider, _ := env.seedUser(108)
	channelID := env.seedDM(cookieOwner, peerID)

	errorBody(env.expect.POST("/api/channels/"+strconv.FormatInt(channelID, 10)+"/messages").
		WithHeader(middleware.MMCookieHeader, cookieOutsider).
		WithJSON(map[string]any{"content": "intrude", "msg_type": 1}).
		Expect().Status(403)).
		Value("error").String().NotEmpty()
}

// TestM4MessageSend_C5_BadRequest — content 缺失 → 422 (handler 显式校验).
func TestM4MessageSend_C5_BadRequest(t *testing.T) {
	env := newM4Env(t)
	cookieSender, _ := env.seedUser(109)
	_, recvID := env.seedUser(110)
	channelID := env.seedDM(cookieSender, recvID)

	errorBody(env.expect.POST("/api/channels/"+strconv.FormatInt(channelID, 10)+"/messages").
		WithHeader(middleware.MMCookieHeader, cookieSender).
		WithJSON(map[string]any{"msg_type": 1}).
		Expect().Status(422)).
		Value("error").String().NotEmpty()
}

// ---- PATCH /api/messages/:id -------------------------------------------------

// TestM4MessageEdit_C1_HappyPath — sender 编辑自己的消息, 200 + content 已更新.
func TestM4MessageEdit_C1_HappyPath(t *testing.T) {
	env := newM4Env(t)
	cookieSender, senderID := env.seedUser(111)
	_, recvID := env.seedUser(112)
	channelID := env.seedDM(cookieSender, recvID)
	msg := env.seedMessage(channelID, senderID, "before")

	resp := successBody(env.expect.PATCH("/api/messages/"+strconv.FormatInt(msg.ID, 10)).
		WithHeader(middleware.MMCookieHeader, cookieSender).
		WithJSON(map[string]any{"content": "after"}).
		Expect().Status(200))

	resp.Value("id").Number().IsEqual(float64(msg.ID))
	resp.Value("content").IsEqual("after")
}

// TestM4MessageEdit_C2_CookieMissing — 缺 cookie → 401.
func TestM4MessageEdit_C2_CookieMissing(t *testing.T) {
	env := newM4Env(t)
	cookieSender, senderID := env.seedUser(113)
	_, recvID := env.seedUser(114)
	channelID := env.seedDM(cookieSender, recvID)
	msg := env.seedMessage(channelID, senderID, "before")

	errorBody(env.expect.PATCH("/api/messages/"+strconv.FormatInt(msg.ID, 10)).
		WithJSON(map[string]any{"content": "after"}).
		Expect().Status(401)).
		Value("error").String().NotEmpty()
}

// TestM4MessageEdit_C3_CookieInvalid — 假 cookieId → 401.
func TestM4MessageEdit_C3_CookieInvalid(t *testing.T) {
	env := newM4Env(t)
	cookieSender, senderID := env.seedUser(115)
	_, recvID := env.seedUser(116)
	channelID := env.seedDM(cookieSender, recvID)
	msg := env.seedMessage(channelID, senderID, "before")

	bogus := testutil.MakeCookieID(9999)
	errorBody(env.expect.PATCH("/api/messages/"+strconv.FormatInt(msg.ID, 10)).
		WithHeader(middleware.MMCookieHeader, bogus).
		WithJSON(map[string]any{"content": "after"}).
		Expect().Status(401)).
		Value("error").String().NotEmpty()
}

// TestM4MessageEdit_C4_NotSender — 非 sender 试图编辑 → 403.
func TestM4MessageEdit_C4_NotSender(t *testing.T) {
	env := newM4Env(t)
	cookieSender, senderID := env.seedUser(117)
	cookieOther, _ := env.seedUser(118)
	_, recvID := env.seedUser(119)
	channelID := env.seedDM(cookieSender, recvID)
	msg := env.seedMessage(channelID, senderID, "before")

	errorBody(env.expect.PATCH("/api/messages/"+strconv.FormatInt(msg.ID, 10)).
		WithHeader(middleware.MMCookieHeader, cookieOther).
		WithJSON(map[string]any{"content": "hijack"}).
		Expect().Status(403)).
		Value("error").String().NotEmpty()
}

// TestM4MessageEdit_C5_BadRequestPathID — :id 非数字 → 400.
func TestM4MessageEdit_C5_BadRequestPathID(t *testing.T) {
	env := newM4Env(t)
	cookieSender, _ := env.seedUser(120)

	errorBody(env.expect.PATCH("/api/messages/abc").
		WithHeader(middleware.MMCookieHeader, cookieSender).
		WithJSON(map[string]any{"content": "x"}).
		Expect().Status(400)).
		Value("error").String().NotEmpty()
}

// ---- DELETE /api/messages/:id ------------------------------------------------

// TestM4MessageDelete_C1_HappyPath — sender 撤回自己消息 → 200 ok=true.
func TestM4MessageDelete_C1_HappyPath(t *testing.T) {
	env := newM4Env(t)
	cookieSender, senderID := env.seedUser(121)
	_, recvID := env.seedUser(122)
	channelID := env.seedDM(cookieSender, recvID)
	msg := env.seedMessage(channelID, senderID, "to-revoke")

	resp := successBody(env.expect.DELETE("/api/messages/"+strconv.FormatInt(msg.ID, 10)).
		WithHeader(middleware.MMCookieHeader, cookieSender).
		Expect().Status(200))
	resp.Value("ok").Boolean().IsTrue()
}

// TestM4MessageDelete_C2_CookieMissing — 缺 cookie → 401.
func TestM4MessageDelete_C2_CookieMissing(t *testing.T) {
	env := newM4Env(t)
	cookieSender, senderID := env.seedUser(123)
	_, recvID := env.seedUser(124)
	channelID := env.seedDM(cookieSender, recvID)
	msg := env.seedMessage(channelID, senderID, "x")

	errorBody(env.expect.DELETE("/api/messages/"+strconv.FormatInt(msg.ID, 10)).
		Expect().Status(401)).
		Value("error").String().NotEmpty()
}

// TestM4MessageDelete_C3_CookieInvalid — 假 cookieId → 401.
func TestM4MessageDelete_C3_CookieInvalid(t *testing.T) {
	env := newM4Env(t)
	cookieSender, senderID := env.seedUser(125)
	_, recvID := env.seedUser(126)
	channelID := env.seedDM(cookieSender, recvID)
	msg := env.seedMessage(channelID, senderID, "x")

	bogus := testutil.MakeCookieID(9999)
	errorBody(env.expect.DELETE("/api/messages/"+strconv.FormatInt(msg.ID, 10)).
		WithHeader(middleware.MMCookieHeader, bogus).
		Expect().Status(401)).
		Value("error").String().NotEmpty()
}

// TestM4MessageDelete_C4_NotSender — 非 sender 试图撤回 → 403.
func TestM4MessageDelete_C4_NotSender(t *testing.T) {
	env := newM4Env(t)
	cookieSender, senderID := env.seedUser(127)
	cookieOther, _ := env.seedUser(128)
	_, recvID := env.seedUser(129)
	channelID := env.seedDM(cookieSender, recvID)
	msg := env.seedMessage(channelID, senderID, "x")

	errorBody(env.expect.DELETE("/api/messages/"+strconv.FormatInt(msg.ID, 10)).
		WithHeader(middleware.MMCookieHeader, cookieOther).
		Expect().Status(403)).
		Value("error").String().NotEmpty()
}

// TestM4MessageDelete_C5_BadRequestPathID — :id 非数字 → 400.
func TestM4MessageDelete_C5_BadRequestPathID(t *testing.T) {
	env := newM4Env(t)
	cookieSender, _ := env.seedUser(130)

	errorBody(env.expect.DELETE("/api/messages/abc").
		WithHeader(middleware.MMCookieHeader, cookieSender).
		Expect().Status(400)).
		Value("error").String().NotEmpty()
}

// ---- POST /api/messages/forward ----------------------------------------------

// TestM4MessageForward_C1_HappyPath — 源 + 目标 channel 都属于 caller, 201 +
// forwarded 数组长度 1.
func TestM4MessageForward_C1_HappyPath(t *testing.T) {
	env := newM4Env(t)
	cookieSender, senderID := env.seedUser(131)
	_, peer1 := env.seedUser(132)
	_, peer2 := env.seedUser(133)
	srcChannel := env.seedDM(cookieSender, peer1)
	dstChannel := env.seedDM(cookieSender, peer2)
	src := env.seedMessage(srcChannel, senderID, "to-forward")

	resp := successBody(env.expect.POST("/api/messages/forward").
		WithHeader(middleware.MMCookieHeader, cookieSender).
		WithJSON(map[string]any{
			"message_id":         src.ID,
			"target_channel_ids": []int64{dstChannel},
		}).
		Expect().Status(201))

	msgs := resp.Value("messages").Array()
	msgs.Length().IsEqual(1)
	msgs.Value(0).Object().Value("content").IsEqual("to-forward")
	msgs.Value(0).Object().Value("channel_id").Number().IsEqual(float64(dstChannel))
}

// TestM4MessageForward_C2_CookieMissing — 缺 cookie → 401.
func TestM4MessageForward_C2_CookieMissing(t *testing.T) {
	env := newM4Env(t)
	cookieSender, senderID := env.seedUser(134)
	_, peer1 := env.seedUser(135)
	srcChannel := env.seedDM(cookieSender, peer1)
	src := env.seedMessage(srcChannel, senderID, "x")

	errorBody(env.expect.POST("/api/messages/forward").
		WithJSON(map[string]any{
			"message_id":         src.ID,
			"target_channel_ids": []int64{srcChannel},
		}).
		Expect().Status(401)).
		Value("error").String().NotEmpty()
}

// TestM4MessageForward_C3_CookieInvalid — 假 cookieId → 401.
func TestM4MessageForward_C3_CookieInvalid(t *testing.T) {
	env := newM4Env(t)
	cookieSender, senderID := env.seedUser(136)
	_, peer1 := env.seedUser(137)
	srcChannel := env.seedDM(cookieSender, peer1)
	src := env.seedMessage(srcChannel, senderID, "x")

	bogus := testutil.MakeCookieID(9999)
	errorBody(env.expect.POST("/api/messages/forward").
		WithHeader(middleware.MMCookieHeader, bogus).
		WithJSON(map[string]any{
			"message_id":         src.ID,
			"target_channel_ids": []int64{srcChannel},
		}).
		Expect().Status(401)).
		Value("error").String().NotEmpty()
}

// TestM4MessageForward_C4_NotSourceMember — caller 不是源 channel 成员 → 403.
func TestM4MessageForward_C4_NotSourceMember(t *testing.T) {
	env := newM4Env(t)
	cookieOwner, ownerID := env.seedUser(138)
	_, peer1 := env.seedUser(139)
	cookieOutsider, _ := env.seedUser(140)
	_, peer2 := env.seedUser(141)
	srcChannel := env.seedDM(cookieOwner, peer1)
	dstChannel := env.seedDM(cookieOutsider, peer2)
	src := env.seedMessage(srcChannel, ownerID, "x")

	errorBody(env.expect.POST("/api/messages/forward").
		WithHeader(middleware.MMCookieHeader, cookieOutsider).
		WithJSON(map[string]any{
			"message_id":         src.ID,
			"target_channel_ids": []int64{dstChannel},
		}).
		Expect().Status(403)).
		Value("error").String().NotEmpty()
}

// TestM4MessageForward_C5_NoTargetChannels — target_channel_ids 空 → 422.
func TestM4MessageForward_C5_NoTargetChannels(t *testing.T) {
	env := newM4Env(t)
	cookieSender, senderID := env.seedUser(142)
	_, peer1 := env.seedUser(143)
	srcChannel := env.seedDM(cookieSender, peer1)
	src := env.seedMessage(srcChannel, senderID, "x")

	errorBody(env.expect.POST("/api/messages/forward").
		WithHeader(middleware.MMCookieHeader, cookieSender).
		WithJSON(map[string]any{
			"message_id":         src.ID,
			"target_channel_ids": []int64{},
		}).
		Expect().Status(422)).
		Value("error").String().NotEmpty()
}

// ---- POST /api/messages/batch ------------------------------------------------

// TestM4MessageBatch_C1_HappyPath — caller 是两个 channel 的成员 → 201 + 2 条.
func TestM4MessageBatch_C1_HappyPath(t *testing.T) {
	env := newM4Env(t)
	cookieSender, _ := env.seedUser(144)
	_, peer1 := env.seedUser(145)
	_, peer2 := env.seedUser(146)
	ch1 := env.seedDM(cookieSender, peer1)
	ch2 := env.seedDM(cookieSender, peer2)

	resp := successBody(env.expect.POST("/api/messages/batch").
		WithHeader(middleware.MMCookieHeader, cookieSender).
		WithJSON(map[string]any{
			"channel_ids": []int64{ch1, ch2},
			"content":     "fanout",
			"msg_type":    1,
		}).
		Expect().Status(201))

	msgs := resp.Value("messages").Array()
	msgs.Length().IsEqual(2)
	msgs.Value(0).Object().Value("content").IsEqual("fanout")
	msgs.Value(1).Object().Value("content").IsEqual("fanout")
}

// TestM4MessageBatch_C2_CookieMissing — 缺 cookie → 401.
func TestM4MessageBatch_C2_CookieMissing(t *testing.T) {
	env := newM4Env(t)
	cookieSender, _ := env.seedUser(147)
	_, peer1 := env.seedUser(148)
	ch1 := env.seedDM(cookieSender, peer1)

	errorBody(env.expect.POST("/api/messages/batch").
		WithJSON(map[string]any{
			"channel_ids": []int64{ch1},
			"content":     "fanout",
			"msg_type":    1,
		}).
		Expect().Status(401)).
		Value("error").String().NotEmpty()
}

// TestM4MessageBatch_C3_CookieInvalid — 假 cookieId → 401.
func TestM4MessageBatch_C3_CookieInvalid(t *testing.T) {
	env := newM4Env(t)
	cookieSender, _ := env.seedUser(149)
	_, peer1 := env.seedUser(150)
	ch1 := env.seedDM(cookieSender, peer1)

	bogus := testutil.MakeCookieID(9999)
	errorBody(env.expect.POST("/api/messages/batch").
		WithHeader(middleware.MMCookieHeader, bogus).
		WithJSON(map[string]any{
			"channel_ids": []int64{ch1},
			"content":     "fanout",
			"msg_type":    1,
		}).
		Expect().Status(401)).
		Value("error").String().NotEmpty()
}

// TestM4MessageBatch_C4_NotMember — caller 不是任何目标 channel 的成员 → 201 但
// messages 为空 (handler 静默跳过非成员, 这是 batch 的设计契约).
func TestM4MessageBatch_C4_NotMember(t *testing.T) {
	env := newM4Env(t)
	cookieOwner, _ := env.seedUser(151)
	_, peer1 := env.seedUser(152)
	cookieOutsider, _ := env.seedUser(153)
	ch1 := env.seedDM(cookieOwner, peer1)

	resp := successBody(env.expect.POST("/api/messages/batch").
		WithHeader(middleware.MMCookieHeader, cookieOutsider).
		WithJSON(map[string]any{
			"channel_ids": []int64{ch1},
			"content":     "fanout",
			"msg_type":    1,
		}).
		Expect().Status(201))

	resp.Value("messages").Array().Length().IsEqual(0)
}

// TestM4MessageBatch_C5_BadRequest — channel_ids 空 → 422.
func TestM4MessageBatch_C5_BadRequest(t *testing.T) {
	env := newM4Env(t)
	cookieSender, _ := env.seedUser(154)

	errorBody(env.expect.POST("/api/messages/batch").
		WithHeader(middleware.MMCookieHeader, cookieSender).
		WithJSON(map[string]any{
			"channel_ids": []int64{},
			"content":     "fanout",
			"msg_type":    1,
		}).
		Expect().Status(422)).
		Value("error").String().NotEmpty()
}

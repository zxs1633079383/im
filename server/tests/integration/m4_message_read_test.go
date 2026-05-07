//go:build integration

package integration

import (
	"strconv"
	"testing"
	"time"

	"im-server/internal/middleware"
	"im-server/internal/testutil"
)

// G2 Batch-B coverage for the message-read + channel-read endpoint family.
//
// Six endpoints × five canonical cases:
//   GET    /api/channels/:id/messages
//   GET    /api/channels/:id/messages/around
//   GET    /api/messages/:id/readers
//   GET    /api/messages/:id/replies
//   GET    /api/messages/:id/after
//   POST   /api/channels/:id/read
//
// Cases: C1 HappyPath / C2 CookieMissing / C3 CookieInvalid / C4 Forbidden /
// C5 BadRequest. seed range 200-299 to avoid colliding with G1/G3/G4.
//
// Each test allocates its own m4 env (postgres + redis testcontainers) and
// uses the harness seed helpers exclusively — no struct field touches against
// internal repos. Query strings are passed via .WithQuery to avoid the
// httpexpect v2 ?-encoding gotcha (see C006 in CLAUDE.md §1.8).

// ---------- GET /api/channels/:id/messages -----------------------------------

// TestM4MessageList_C1_HappyPath: 频道成员拉取消息列表，返回 messages 数组并含 sender。
func TestM4MessageList_C1_HappyPath(t *testing.T) {
	env := newM4Env(t)
	cookieA, aID := env.seedUser(200)
	_, bID := env.seedUser(201)
	channelID := env.seedDM(cookieA, bID)
	env.seedMessage(channelID, aID, "hello list")

	resp := env.expect.GET("/api/channels/" + strconv.FormatInt(channelID, 10) + "/messages").
		WithHeader(middleware.MMCookieHeader, cookieA).
		WithQuery("limit", 50).
		Expect().Status(200).JSON().Object()

	resp.Value("messages").Array().Length().IsEqual(1)
	resp.Value("messages").Array().Value(0).Object().Value("sender_id").IsEqual(aID)
}

// TestM4MessageList_C2_CookieMissing: 不带 cookieId 头 → 401。
func TestM4MessageList_C2_CookieMissing(t *testing.T) {
	env := newM4Env(t)
	cookieA, aID := env.seedUser(202)
	_, bID := env.seedUser(203)
	channelID := env.seedDM(cookieA, bID)
	env.seedMessage(channelID, aID, "hidden")

	body := env.expect.GET("/api/channels/" + strconv.FormatInt(channelID, 10) + "/messages").
		Expect().Status(401).JSON().Object()
	body.Value("error").String().NotEmpty()
}

// TestM4MessageList_C3_CookieInvalid: cookieId 在 Redis 不存在 → 401。
func TestM4MessageList_C3_CookieInvalid(t *testing.T) {
	env := newM4Env(t)
	cookieA, aID := env.seedUser(204)
	_, bID := env.seedUser(205)
	channelID := env.seedDM(cookieA, bID)
	env.seedMessage(channelID, aID, "hidden")

	bogus := testutil.MakeCookieID(9201)
	env.expect.GET("/api/channels/"+strconv.FormatInt(channelID, 10)+"/messages").
		WithHeader(middleware.MMCookieHeader, bogus).
		Expect().Status(401)
}

// TestM4MessageList_C4_NotChannelMember: 非 channel 成员 → 403。
func TestM4MessageList_C4_NotChannelMember(t *testing.T) {
	env := newM4Env(t)
	cookieA, aID := env.seedUser(206)
	_, bID := env.seedUser(207)
	cookieC, _ := env.seedUser(208)
	channelID := env.seedDM(cookieA, bID)
	env.seedMessage(channelID, aID, "secret")

	env.expect.GET("/api/channels/"+strconv.FormatInt(channelID, 10)+"/messages").
		WithHeader(middleware.MMCookieHeader, cookieC).
		Expect().Status(403)
}

// TestM4MessageList_C5_InvalidPath: 路径 ID 非法（非数字）→ 400。
func TestM4MessageList_C5_InvalidPath(t *testing.T) {
	env := newM4Env(t)
	cookieA, _ := env.seedUser(209)

	env.expect.GET("/api/channels/abc/messages").
		WithHeader(middleware.MMCookieHeader, cookieA).
		Expect().Status(400)
}

// ---------- GET /api/channels/:id/messages/around ----------------------------

// TestM4MessageAround_C1_HappyPath: 围绕指定时间戳取窗口，返回 has_older/has_newer 字段。
func TestM4MessageAround_C1_HappyPath(t *testing.T) {
	env := newM4Env(t)
	cookieA, aID := env.seedUser(210)
	_, bID := env.seedUser(211)
	channelID := env.seedDM(cookieA, bID)
	env.seedMessage(channelID, aID, "around-anchor")

	tsMs := time.Now().Add(time.Hour).UnixMilli()
	resp := env.expect.GET("/api/channels/"+strconv.FormatInt(channelID, 10)+"/messages/around").
		WithHeader(middleware.MMCookieHeader, cookieA).
		WithQuery("timestamp", tsMs).
		WithQuery("limit", 20).
		Expect().Status(200).JSON().Object()

	resp.Value("messages").Array().NotEmpty()
	resp.Value("has_older").Boolean()
	resp.Value("has_newer").Boolean()
}

// TestM4MessageAround_C2_CookieMissing: 不带 cookieId → 401。
func TestM4MessageAround_C2_CookieMissing(t *testing.T) {
	env := newM4Env(t)
	cookieA, _ := env.seedUser(212)
	_, bID := env.seedUser(213)
	channelID := env.seedDM(cookieA, bID)

	env.expect.GET("/api/channels/"+strconv.FormatInt(channelID, 10)+"/messages/around").
		WithQuery("timestamp", time.Now().UnixMilli()).
		Expect().Status(401)
}

// TestM4MessageAround_C3_CookieInvalid: 无效 cookieId → 401。
func TestM4MessageAround_C3_CookieInvalid(t *testing.T) {
	env := newM4Env(t)
	cookieA, _ := env.seedUser(214)
	_, bID := env.seedUser(215)
	channelID := env.seedDM(cookieA, bID)

	env.expect.GET("/api/channels/"+strconv.FormatInt(channelID, 10)+"/messages/around").
		WithHeader(middleware.MMCookieHeader, testutil.MakeCookieID(9202)).
		WithQuery("timestamp", time.Now().UnixMilli()).
		Expect().Status(401)
}

// TestM4MessageAround_C4_NotChannelMember: 非成员 → 403。
func TestM4MessageAround_C4_NotChannelMember(t *testing.T) {
	env := newM4Env(t)
	cookieA, _ := env.seedUser(216)
	_, bID := env.seedUser(217)
	cookieC, _ := env.seedUser(218)
	channelID := env.seedDM(cookieA, bID)

	env.expect.GET("/api/channels/"+strconv.FormatInt(channelID, 10)+"/messages/around").
		WithHeader(middleware.MMCookieHeader, cookieC).
		WithQuery("timestamp", time.Now().UnixMilli()).
		Expect().Status(403)
}

// TestM4MessageAround_C5_MissingTimestamp: 不传 timestamp → 400。
func TestM4MessageAround_C5_MissingTimestamp(t *testing.T) {
	env := newM4Env(t)
	cookieA, _ := env.seedUser(219)
	_, bID := env.seedUser(220)
	channelID := env.seedDM(cookieA, bID)

	body := env.expect.GET("/api/channels/" + strconv.FormatInt(channelID, 10) + "/messages/around").
		WithHeader(middleware.MMCookieHeader, cookieA).
		Expect().Status(400).JSON().Object()
	body.Value("error").String().NotEmpty()
}

// ---------- GET /api/messages/:id/readers ------------------------------------

// TestM4MessageReaders_C1_HappyPath: 成员拉取已读人列表，返回 readers 数组 + next_cursor。
func TestM4MessageReaders_C1_HappyPath(t *testing.T) {
	env := newM4Env(t)
	cookieA, aID := env.seedUser(221)
	_, bID := env.seedUser(222)
	channelID := env.seedDM(cookieA, bID)
	msg := env.seedMessage(channelID, aID, "to-be-read")

	resp := env.expect.GET("/api/messages/" + strconv.FormatInt(msg.ID, 10) + "/readers").
		WithHeader(middleware.MMCookieHeader, cookieA).
		WithQuery("limit", 50).
		Expect().Status(200).JSON().Object()

	resp.Value("readers").Array()
	resp.ContainsKey("next_cursor")
}

// TestM4MessageReaders_C2_CookieMissing: 不带 cookieId → 401。
func TestM4MessageReaders_C2_CookieMissing(t *testing.T) {
	env := newM4Env(t)
	cookieA, aID := env.seedUser(223)
	_, bID := env.seedUser(224)
	channelID := env.seedDM(cookieA, bID)
	msg := env.seedMessage(channelID, aID, "guarded")

	env.expect.GET("/api/messages/" + strconv.FormatInt(msg.ID, 10) + "/readers").
		Expect().Status(401)
}

// TestM4MessageReaders_C3_CookieInvalid: Redis 不存在的 cookieId → 401。
func TestM4MessageReaders_C3_CookieInvalid(t *testing.T) {
	env := newM4Env(t)
	cookieA, aID := env.seedUser(225)
	_, bID := env.seedUser(226)
	channelID := env.seedDM(cookieA, bID)
	msg := env.seedMessage(channelID, aID, "guarded")

	env.expect.GET("/api/messages/"+strconv.FormatInt(msg.ID, 10)+"/readers").
		WithHeader(middleware.MMCookieHeader, testutil.MakeCookieID(9203)).
		Expect().Status(401)
}

// TestM4MessageReaders_C4_NotChannelMember: 非 channel 成员 → 403。
func TestM4MessageReaders_C4_NotChannelMember(t *testing.T) {
	env := newM4Env(t)
	cookieA, aID := env.seedUser(227)
	_, bID := env.seedUser(228)
	cookieC, _ := env.seedUser(229)
	channelID := env.seedDM(cookieA, bID)
	msg := env.seedMessage(channelID, aID, "guarded")

	env.expect.GET("/api/messages/"+strconv.FormatInt(msg.ID, 10)+"/readers").
		WithHeader(middleware.MMCookieHeader, cookieC).
		Expect().Status(403)
}

// TestM4MessageReaders_C5_InvalidPath: 路径 ID 非法 → 400。
func TestM4MessageReaders_C5_InvalidPath(t *testing.T) {
	env := newM4Env(t)
	cookieA, _ := env.seedUser(230)

	env.expect.GET("/api/messages/not-a-number/readers").
		WithHeader(middleware.MMCookieHeader, cookieA).
		Expect().Status(400)
}

// ---------- GET /api/messages/:id/replies ------------------------------------

// TestM4MessageReplies_C1_HappyPath: 拉根消息所有 reply，含 reply_to 字段。
func TestM4MessageReplies_C1_HappyPath(t *testing.T) {
	env := newM4Env(t)
	cookieA, aID := env.seedUser(231)
	_, bID := env.seedUser(232)
	channelID := env.seedDM(cookieA, bID)
	root := env.seedMessage(channelID, aID, "root-of-thread")

	// Post a reply via the HTTP send endpoint so reply_to is honoured.
	env.expect.POST("/api/channels/"+strconv.FormatInt(channelID, 10)+"/messages").
		WithHeader(middleware.MMCookieHeader, cookieA).
		WithJSON(map[string]any{
			"content":  "first reply",
			"msg_type": 1,
			"reply_to": root.ID,
		}).
		Expect().Status(201)

	resp := env.expect.GET("/api/messages/" + strconv.FormatInt(root.ID, 10) + "/replies").
		WithHeader(middleware.MMCookieHeader, cookieA).
		Expect().Status(200).JSON().Object()
	resp.Value("messages").Array().Length().IsEqual(1)
	resp.Value("messages").Array().Value(0).Object().Value("content").IsEqual("first reply")
}

// TestM4MessageReplies_C2_CookieMissing: 不带 cookieId → 401。
func TestM4MessageReplies_C2_CookieMissing(t *testing.T) {
	env := newM4Env(t)
	cookieA, aID := env.seedUser(233)
	_, bID := env.seedUser(234)
	channelID := env.seedDM(cookieA, bID)
	root := env.seedMessage(channelID, aID, "root")

	env.expect.GET("/api/messages/" + strconv.FormatInt(root.ID, 10) + "/replies").
		Expect().Status(401)
}

// TestM4MessageReplies_C3_CookieInvalid: Redis 不存在的 cookieId → 401。
func TestM4MessageReplies_C3_CookieInvalid(t *testing.T) {
	env := newM4Env(t)
	cookieA, aID := env.seedUser(235)
	_, bID := env.seedUser(236)
	channelID := env.seedDM(cookieA, bID)
	root := env.seedMessage(channelID, aID, "root")

	env.expect.GET("/api/messages/"+strconv.FormatInt(root.ID, 10)+"/replies").
		WithHeader(middleware.MMCookieHeader, testutil.MakeCookieID(9204)).
		Expect().Status(401)
}

// TestM4MessageReplies_C4_NotChannelMember: 非 channel 成员 → 403。
func TestM4MessageReplies_C4_NotChannelMember(t *testing.T) {
	env := newM4Env(t)
	cookieA, aID := env.seedUser(237)
	_, bID := env.seedUser(238)
	cookieC, _ := env.seedUser(239)
	channelID := env.seedDM(cookieA, bID)
	root := env.seedMessage(channelID, aID, "root")

	env.expect.GET("/api/messages/"+strconv.FormatInt(root.ID, 10)+"/replies").
		WithHeader(middleware.MMCookieHeader, cookieC).
		Expect().Status(403)
}

// TestM4MessageReplies_C5_InvalidPath: 路径 ID 非法 → 400。
func TestM4MessageReplies_C5_InvalidPath(t *testing.T) {
	env := newM4Env(t)
	cookieA, _ := env.seedUser(240)

	env.expect.GET("/api/messages/oops/replies").
		WithHeader(middleware.MMCookieHeader, cookieA).
		Expect().Status(400)
}

// ---------- GET /api/messages/:id/after --------------------------------------

// TestM4MessageAfter_C1_HappyPath: 从某 msg 之后增量拉取，返回 messages 数组。
func TestM4MessageAfter_C1_HappyPath(t *testing.T) {
	env := newM4Env(t)
	cookieA, aID := env.seedUser(241)
	_, bID := env.seedUser(242)
	channelID := env.seedDM(cookieA, bID)
	first := env.seedMessage(channelID, aID, "anchor")
	env.seedMessage(channelID, aID, "after-1")
	env.seedMessage(channelID, aID, "after-2")

	resp := env.expect.GET("/api/messages/" + strconv.FormatInt(first.ID, 10) + "/after").
		WithHeader(middleware.MMCookieHeader, cookieA).
		WithQuery("limit", 50).
		Expect().Status(200).JSON().Object()
	resp.Value("messages").Array().Length().IsEqual(2)
}

// TestM4MessageAfter_C2_CookieMissing: 不带 cookieId → 401。
func TestM4MessageAfter_C2_CookieMissing(t *testing.T) {
	env := newM4Env(t)
	cookieA, aID := env.seedUser(243)
	_, bID := env.seedUser(244)
	channelID := env.seedDM(cookieA, bID)
	first := env.seedMessage(channelID, aID, "anchor")

	env.expect.GET("/api/messages/" + strconv.FormatInt(first.ID, 10) + "/after").
		Expect().Status(401)
}

// TestM4MessageAfter_C3_CookieInvalid: 无效 cookieId → 401。
func TestM4MessageAfter_C3_CookieInvalid(t *testing.T) {
	env := newM4Env(t)
	cookieA, aID := env.seedUser(245)
	_, bID := env.seedUser(246)
	channelID := env.seedDM(cookieA, bID)
	first := env.seedMessage(channelID, aID, "anchor")

	env.expect.GET("/api/messages/"+strconv.FormatInt(first.ID, 10)+"/after").
		WithHeader(middleware.MMCookieHeader, testutil.MakeCookieID(9205)).
		Expect().Status(401)
}

// TestM4MessageAfter_C4_NotChannelMember: 非 channel 成员 → 403。
func TestM4MessageAfter_C4_NotChannelMember(t *testing.T) {
	env := newM4Env(t)
	cookieA, aID := env.seedUser(247)
	_, bID := env.seedUser(248)
	cookieC, _ := env.seedUser(249)
	channelID := env.seedDM(cookieA, bID)
	first := env.seedMessage(channelID, aID, "anchor")

	env.expect.GET("/api/messages/"+strconv.FormatInt(first.ID, 10)+"/after").
		WithHeader(middleware.MMCookieHeader, cookieC).
		Expect().Status(403)
}

// TestM4MessageAfter_C5_InvalidPath: 路径 ID 非法 → 400。
func TestM4MessageAfter_C5_InvalidPath(t *testing.T) {
	env := newM4Env(t)
	cookieA, _ := env.seedUser(250)

	env.expect.GET("/api/messages/xyz/after").
		WithHeader(middleware.MMCookieHeader, cookieA).
		WithQuery("limit", 50).
		Expect().Status(400)
}

// ---------- POST /api/channels/:id/read --------------------------------------

// TestM4ChannelRead_C1_HappyPath: 频道成员推进 last_read_seq → 200 + 返回最新 seq。
func TestM4ChannelRead_C1_HappyPath(t *testing.T) {
	env := newM4Env(t)
	cookieA, aID := env.seedUser(251)
	_, bID := env.seedUser(252)
	channelID := env.seedDM(cookieA, bID)
	env.seedMessage(channelID, aID, "msg1")

	resp := env.expect.POST("/api/channels/" + strconv.FormatInt(channelID, 10) + "/read").
		WithHeader(middleware.MMCookieHeader, cookieA).
		Expect().Status(200).JSON().Object()
	resp.Value("seq").Number().Ge(1)
}

// TestM4ChannelRead_C2_CookieMissing: 不带 cookieId → 401。
func TestM4ChannelRead_C2_CookieMissing(t *testing.T) {
	env := newM4Env(t)
	cookieA, _ := env.seedUser(253)
	_, bID := env.seedUser(254)
	channelID := env.seedDM(cookieA, bID)

	env.expect.POST("/api/channels/" + strconv.FormatInt(channelID, 10) + "/read").
		Expect().Status(401)
}

// TestM4ChannelRead_C3_CookieInvalid: Redis 不存在的 cookieId → 401。
func TestM4ChannelRead_C3_CookieInvalid(t *testing.T) {
	env := newM4Env(t)
	cookieA, _ := env.seedUser(255)
	_, bID := env.seedUser(256)
	channelID := env.seedDM(cookieA, bID)

	env.expect.POST("/api/channels/"+strconv.FormatInt(channelID, 10)+"/read").
		WithHeader(middleware.MMCookieHeader, testutil.MakeCookieID(9206)).
		Expect().Status(401)
}

// TestM4ChannelRead_C4_NotChannelMember: 非 channel 成员 → 403。
func TestM4ChannelRead_C4_NotChannelMember(t *testing.T) {
	env := newM4Env(t)
	cookieA, _ := env.seedUser(257)
	_, bID := env.seedUser(258)
	cookieC, _ := env.seedUser(259)
	channelID := env.seedDM(cookieA, bID)

	env.expect.POST("/api/channels/"+strconv.FormatInt(channelID, 10)+"/read").
		WithHeader(middleware.MMCookieHeader, cookieC).
		Expect().Status(403)
}

// TestM4ChannelRead_C5_InvalidPath: 路径 ID 非法 → 400。
func TestM4ChannelRead_C5_InvalidPath(t *testing.T) {
	env := newM4Env(t)
	cookieA, _ := env.seedUser(260)

	env.expect.POST("/api/channels/abc/read").
		WithHeader(middleware.MMCookieHeader, cookieA).
		Expect().Status(400)
}

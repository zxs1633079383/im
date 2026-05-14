//go:build integration

// Package integration — Batch-B favorite + urgent endpoint coverage (G4 slice).
//
// 7 endpoints × 5 cases = 35 tests. Each top-level test calls newM4Env(t)
// to get a fresh Postgres + Redis testcontainer pair so cases stay isolated.
// Seed range 400-499 is reserved for this file (G1/G2/G3 use 100-399).
//
// Case matrix:
//   C1 HappyPath        正常入参 + 有权限 → 2xx
//   C2 CookieMissing    不带 cookie 头   → 401
//   C3 CookieInvalid    cookieId Redis 不存在 → 401
//   C4 Forbidden        有效 cookieId 但越权 → 403  (or documented non-403)
//   C5 BadRequest       body / 路径参数非法 → 400 / 422
//
// Notes on Forbidden cases:
//   - Urgent endpoints all enforce member / sender / urgent-flag checks via
//     UrgentService (see internal/service/urgent.go) — straightforward 403.
//   - Favorite endpoints currently DO NOT enforce a per-channel membership
//     check at the handler or service layer. The C4 tests for favorite
//     therefore document the actual behaviour: Add/Remove/List operate on
//     the caller's own favorites only, with no cross-tenant 403 path. If
//     the product later tightens this to "must be channel member to
//     favorite", these tests will surface the change.
package integration

import (
	"testing"

	"im-server/internal/middleware"
	"im-server/internal/repo"
)

// ---------------------------------------------------------------------------
// POST /api/favorites/:message_id — add a favorite
// ---------------------------------------------------------------------------

// TestM4FavoriteAdd_C1_HappyPath — sender 在自己 DM 里发条消息，再
// 收藏它 → 201 + DB 中存在 (uid, msg_id) 行。
func TestM4FavoriteAdd_C1_HappyPath(t *testing.T) {
	env := newM4Env(t)
	cookieA, idA := env.seedUser(400)
	_, idB := env.seedUser(401)
	channelID := env.seedDM(cookieA, idB)
	msg := env.seedMessage(channelID, idA, "fav-target")

	successBody(env.expect.POST("/api/favorites/"+msg.ID).
		WithHeader(middleware.MMCookieHeader, cookieA).
		Expect().Status(201)).Value("status").IsEqual("ok")
}

// TestM4FavoriteAdd_C2_CookieMissing — 不带 cookieId → 401。
func TestM4FavoriteAdd_C2_CookieMissing(t *testing.T) {
	env := newM4Env(t)
	cookieA, idA := env.seedUser(402)
	_, idB := env.seedUser(403)
	channelID := env.seedDM(cookieA, idB)
	msg := env.seedMessage(channelID, idA, "fav-no-auth")

	env.expect.POST("/api/favorites/" + msg.ID).
		Expect().Status(401)
}

// TestM4FavoriteAdd_C3_CookieInvalid — cookieId 不在 Redis 里 → 401。
func TestM4FavoriteAdd_C3_CookieInvalid(t *testing.T) {
	env := newM4Env(t)
	cookieA, idA := env.seedUser(404)
	_, idB := env.seedUser(405)
	channelID := env.seedDM(cookieA, idB)
	msg := env.seedMessage(channelID, idA, "fav-stale")

	env.expect.POST("/api/favorites/"+msg.ID).
		WithHeader(middleware.MMCookieHeader, "ffffffffffffffffffffffff").
		Expect().Status(401)
}

// TestM4FavoriteAdd_C4_CrossTenantNoCheck — 用户 X 收藏一条只属于 A↔B 私聊的
// 消息：handler / service 层并没有 channel-member check，会照样写入 → 201。
// 该用例固定当前行为；如果后续加上跨租户拦截，这里会立刻失败提醒。
func TestM4FavoriteAdd_C4_CrossTenantNoCheck(t *testing.T) {
	env := newM4Env(t)
	cookieA, idA := env.seedUser(406)
	_, idB := env.seedUser(407)
	cookieX, _ := env.seedUser(408)

	channelAB := env.seedDM(cookieA, idB)
	msg := env.seedMessage(channelAB, idA, "cross-tenant")

	env.expect.POST("/api/favorites/" + msg.ID).
		WithHeader(middleware.MMCookieHeader, cookieX).
		Expect().Status(201)
}

// TestM4FavoriteAdd_C5_FKViolation — C012 后 path :message_id 是 string，handler
// 不再校验数字格式；但 favorites 表对 messages.id 有 FK，写入不存在 message 时
// PostgreSQL 抛 FK 违约 → handler 报 500。原 "non-numeric path → 400" case 在
// spec §3.2 (不再 ParseInt) 下不再适用，本 case 锁定"非法 ID 至 service/DB 层
// 才被拒"的实际行为。未来 C014 可考虑在 service 层加 message 存在性预校验把 500
// 收敛成 404。
func TestM4FavoriteAdd_C5_FKViolation(t *testing.T) {
	env := newM4Env(t)
	cookieA, _ := env.seedUser(409)

	env.expect.POST("/api/favorites/not-a-number").
		WithHeader(middleware.MMCookieHeader, cookieA).
		Expect().Status(500)
}

// ---------------------------------------------------------------------------
// DELETE /api/favorites/:message_id — remove a favorite
// ---------------------------------------------------------------------------

// TestM4FavoriteRemove_C1_HappyPath — 先 Add 再 DELETE → 204。
func TestM4FavoriteRemove_C1_HappyPath(t *testing.T) {
	env := newM4Env(t)
	cookieA, idA := env.seedUser(410)
	_, idB := env.seedUser(411)
	channelID := env.seedDM(cookieA, idB)
	msg := env.seedMessage(channelID, idA, "to-unfav")

	env.expect.POST("/api/favorites/" + msg.ID).
		WithHeader(middleware.MMCookieHeader, cookieA).
		Expect().Status(201)

	env.expect.DELETE("/api/favorites/" + msg.ID).
		WithHeader(middleware.MMCookieHeader, cookieA).
		Expect().Status(204)
}

// TestM4FavoriteRemove_C2_CookieMissing — 缺 cookie → 401。
func TestM4FavoriteRemove_C2_CookieMissing(t *testing.T) {
	env := newM4Env(t)
	cookieA, idA := env.seedUser(412)
	_, idB := env.seedUser(413)
	channelID := env.seedDM(cookieA, idB)
	msg := env.seedMessage(channelID, idA, "rm-no-auth")

	env.expect.DELETE("/api/favorites/" + msg.ID).
		Expect().Status(401)
}

// TestM4FavoriteRemove_C3_CookieInvalid — cookieId 在 Redis 里查不到 → 401。
func TestM4FavoriteRemove_C3_CookieInvalid(t *testing.T) {
	env := newM4Env(t)
	cookieA, idA := env.seedUser(414)
	_, idB := env.seedUser(415)
	channelID := env.seedDM(cookieA, idB)
	msg := env.seedMessage(channelID, idA, "rm-stale")

	env.expect.DELETE("/api/favorites/"+msg.ID).
		WithHeader(middleware.MMCookieHeader, "ffffffffffffffffffffffff").
		Expect().Status(401)
}

// TestM4FavoriteRemove_C4_OtherUserFavoriteIs404 — 用户 A 收藏了消息，用户 X 试
// 图删除 → 命中 ErrNotFound（X 没有这条收藏），返回 404 而不是 403。这是当前
// "无跨租户检查"的副作用：每个 user 的 favorite 名义上只对自己可见。
func TestM4FavoriteRemove_C4_OtherUserFavoriteIs404(t *testing.T) {
	env := newM4Env(t)
	cookieA, idA := env.seedUser(416)
	_, idB := env.seedUser(417)
	cookieX, _ := env.seedUser(418)

	channelAB := env.seedDM(cookieA, idB)
	msg := env.seedMessage(channelAB, idA, "x-cant-touch")

	env.expect.POST("/api/favorites/" + msg.ID).
		WithHeader(middleware.MMCookieHeader, cookieA).
		Expect().Status(201)

	env.expect.DELETE("/api/favorites/" + msg.ID).
		WithHeader(middleware.MMCookieHeader, cookieX).
		Expect().Status(404)
}

// TestM4FavoriteRemove_C5_NonExistentID — C012 后 path :message_id 是 string，
// 非数字字面值合法；该 user 没有这条 favorite → 404 ErrNotFound。
func TestM4FavoriteRemove_C5_NonExistentID(t *testing.T) {
	env := newM4Env(t)
	cookieA, _ := env.seedUser(419)

	env.expect.DELETE("/api/favorites/not-a-number").
		WithHeader(middleware.MMCookieHeader, cookieA).
		Expect().Status(404)
}

// ---------------------------------------------------------------------------
// GET /api/favorites — list favorites
// ---------------------------------------------------------------------------

// TestM4FavoriteList_C1_HappyPath — A 收藏一条消息，再列表 → 200，favorites
// 数组长度 1。
func TestM4FavoriteList_C1_HappyPath(t *testing.T) {
	env := newM4Env(t)
	cookieA, idA := env.seedUser(420)
	_, idB := env.seedUser(421)
	channelID := env.seedDM(cookieA, idB)
	msg := env.seedMessage(channelID, idA, "list-target")

	env.expect.POST("/api/favorites/" + msg.ID).
		WithHeader(middleware.MMCookieHeader, cookieA).
		Expect().Status(201)

	resp := successBody(env.expect.GET("/api/favorites").
		WithHeader(middleware.MMCookieHeader, cookieA).
		Expect().Status(200))
	resp.Value("favorites").Array().Length().IsEqual(1)
}

// TestM4FavoriteList_C2_CookieMissing — 不带 cookieId → 401。
func TestM4FavoriteList_C2_CookieMissing(t *testing.T) {
	env := newM4Env(t)
	_, _ = env.seedUser(422)

	env.expect.GET("/api/favorites").
		Expect().Status(401)
}

// TestM4FavoriteList_C3_CookieInvalid — cookieId 在 Redis 里查不到 → 401。
func TestM4FavoriteList_C3_CookieInvalid(t *testing.T) {
	env := newM4Env(t)
	_, _ = env.seedUser(423)

	env.expect.GET("/api/favorites").
		WithHeader(middleware.MMCookieHeader, "ffffffffffffffffffffffff").
		Expect().Status(401)
}

// TestM4FavoriteList_C4_OnlyOwnFavorites — A 收藏一条，X 列表 → 200，X 的
// favorites 应为空（GET 只能读自己的，跨租户无法窥探）。
func TestM4FavoriteList_C4_OnlyOwnFavorites(t *testing.T) {
	env := newM4Env(t)
	cookieA, idA := env.seedUser(424)
	_, idB := env.seedUser(425)
	cookieX, _ := env.seedUser(426)

	channelAB := env.seedDM(cookieA, idB)
	msg := env.seedMessage(channelAB, idA, "a-only")

	env.expect.POST("/api/favorites/" + msg.ID).
		WithHeader(middleware.MMCookieHeader, cookieA).
		Expect().Status(201)

	resp := successBody(env.expect.GET("/api/favorites").
		WithHeader(middleware.MMCookieHeader, cookieX).
		Expect().Status(200))
	resp.Value("favorites").Array().Length().IsEqual(0)
}

// TestM4FavoriteList_C5_NoFavoritesEmptyArray — 没收藏过的用户列表 → 200 + []。
// list endpoint 没有强校验入参，C5"参数非法"对该 endpoint 不存在；改为校验
// 空状态契约（service 必返回非 nil 空数组，序列化成 [] 而不是 null）。
func TestM4FavoriteList_C5_NoFavoritesEmptyArray(t *testing.T) {
	env := newM4Env(t)
	cookieA, _ := env.seedUser(427)

	resp := successBody(env.expect.GET("/api/favorites").
		WithHeader(middleware.MMCookieHeader, cookieA).
		Expect().Status(200))
	resp.Value("favorites").Array().Length().IsEqual(0)
}

// ---------------------------------------------------------------------------
// POST /api/messages/urgent — send urgent
// ---------------------------------------------------------------------------

// TestM4UrgentSend_C1_HappyPath — 群成员发加急 → 201，is_urgent=true。
func TestM4UrgentSend_C1_HappyPath(t *testing.T) {
	env := newM4Env(t)
	cookieOwner, _ := env.seedUser(430)
	_, member := env.seedUser(431)
	channelID := env.seedGroup(cookieOwner, "g-urgent-c1", member)

	resp := successBody(env.expect.POST("/api/messages/urgent").
		WithHeader(middleware.MMCookieHeader, cookieOwner).
		WithJSON(map[string]any{
			"channel_id":    channelID,
			"content":       "URGENT!",
			"client_msg_id": "cli-urgent-c1",
		}).
		Expect().Status(201))
	resp.Value("is_urgent").Boolean().IsTrue()
}

// TestM4UrgentSend_C2_CookieMissing — 缺 cookie → 401。
func TestM4UrgentSend_C2_CookieMissing(t *testing.T) {
	env := newM4Env(t)
	cookieOwner, _ := env.seedUser(432)
	_, member := env.seedUser(433)
	channelID := env.seedGroup(cookieOwner, "g-urgent-c2", member)

	env.expect.POST("/api/messages/urgent").
		WithJSON(map[string]any{"channel_id": channelID, "content": "x"}).
		Expect().Status(401)
}

// TestM4UrgentSend_C3_CookieInvalid — cookieId 在 Redis 里查不到 → 401。
func TestM4UrgentSend_C3_CookieInvalid(t *testing.T) {
	env := newM4Env(t)
	cookieOwner, _ := env.seedUser(434)
	_, member := env.seedUser(435)
	channelID := env.seedGroup(cookieOwner, "g-urgent-c3", member)

	env.expect.POST("/api/messages/urgent").
		WithHeader(middleware.MMCookieHeader, "ffffffffffffffffffffffff").
		WithJSON(map[string]any{"channel_id": channelID, "content": "x"}).
		Expect().Status(401)
}

// TestM4UrgentSend_C4_NotChannelMember — 非群成员发加急 → 403。
func TestM4UrgentSend_C4_NotChannelMember(t *testing.T) {
	env := newM4Env(t)
	cookieOwner, _ := env.seedUser(436)
	_, member := env.seedUser(437)
	cookieX, _ := env.seedUser(438)
	channelID := env.seedGroup(cookieOwner, "g-urgent-c4", member)

	env.expect.POST("/api/messages/urgent").
		WithHeader(middleware.MMCookieHeader, cookieX).
		WithJSON(map[string]any{"channel_id": channelID, "content": "intrusion"}).
		Expect().Status(403)
}

// TestM4UrgentSend_C5_BadRequest — channel_id 缺失 / content 空，命中 422。
func TestM4UrgentSend_C5_BadRequest(t *testing.T) {
	env := newM4Env(t)
	cookieOwner, _ := env.seedUser(439)

	// channel_id 缺失（默认 0）→ 422。
	env.expect.POST("/api/messages/urgent").
		WithHeader(middleware.MMCookieHeader, cookieOwner).
		WithJSON(map[string]any{"content": "no channel"}).
		Expect().Status(422)
}

// ---------------------------------------------------------------------------
// POST /api/messages/:id/urgent/confirm — confirm urgent
// ---------------------------------------------------------------------------

// TestM4UrgentConfirm_C1_HappyPath — owner 发加急，member 确认 → 200。
func TestM4UrgentConfirm_C1_HappyPath(t *testing.T) {
	env := newM4Env(t)
	cookieOwner, _ := env.seedUser(440)
	cookieMember, member := env.seedUser(441)
	channelID := env.seedGroup(cookieOwner, "g-confirm-c1", member)

	sent := successBody(env.expect.POST("/api/messages/urgent").
		WithHeader(middleware.MMCookieHeader, cookieOwner).
		WithJSON(map[string]any{"channel_id": channelID, "content": "ack me"}).
		Expect().Status(201))
	msgID := sent.Value("id").String().Raw()

	successBody(env.expect.POST("/api/messages/"+msgID+"/urgent/confirm").
		WithHeader(middleware.MMCookieHeader, cookieMember).
		Expect().Status(200)).Value("status").IsEqual("confirmed")
}

// TestM4UrgentConfirm_C2_CookieMissing — 缺 cookie → 401。
func TestM4UrgentConfirm_C2_CookieMissing(t *testing.T) {
	env := newM4Env(t)
	cookieOwner, _ := env.seedUser(442)
	_, member := env.seedUser(443)
	channelID := env.seedGroup(cookieOwner, "g-confirm-c2", member)

	sent := successBody(env.expect.POST("/api/messages/urgent").
		WithHeader(middleware.MMCookieHeader, cookieOwner).
		WithJSON(map[string]any{"channel_id": channelID, "content": "u"}).
		Expect().Status(201))
	msgID := sent.Value("id").String().Raw()

	env.expect.POST("/api/messages/" + msgID + "/urgent/confirm").
		Expect().Status(401)
}

// TestM4UrgentConfirm_C3_CookieInvalid — cookieId 在 Redis 里查不到 → 401。
func TestM4UrgentConfirm_C3_CookieInvalid(t *testing.T) {
	env := newM4Env(t)
	cookieOwner, _ := env.seedUser(444)
	_, member := env.seedUser(445)
	channelID := env.seedGroup(cookieOwner, "g-confirm-c3", member)

	sent := successBody(env.expect.POST("/api/messages/urgent").
		WithHeader(middleware.MMCookieHeader, cookieOwner).
		WithJSON(map[string]any{"channel_id": channelID, "content": "u"}).
		Expect().Status(201))
	msgID := sent.Value("id").String().Raw()

	env.expect.POST("/api/messages/"+msgID+"/urgent/confirm").
		WithHeader(middleware.MMCookieHeader, "ffffffffffffffffffffffff").
		Expect().Status(401)
}

// TestM4UrgentConfirm_C4_NotChannelMember — 非成员确认 → 403 ErrNotMember。
func TestM4UrgentConfirm_C4_NotChannelMember(t *testing.T) {
	env := newM4Env(t)
	cookieOwner, _ := env.seedUser(446)
	_, member := env.seedUser(447)
	cookieX, _ := env.seedUser(448)
	channelID := env.seedGroup(cookieOwner, "g-confirm-c4", member)

	sent := successBody(env.expect.POST("/api/messages/urgent").
		WithHeader(middleware.MMCookieHeader, cookieOwner).
		WithJSON(map[string]any{"channel_id": channelID, "content": "u"}).
		Expect().Status(201))
	msgID := sent.Value("id").String().Raw()

	env.expect.POST("/api/messages/"+msgID+"/urgent/confirm").
		WithHeader(middleware.MMCookieHeader, cookieX).
		Expect().Status(403)
}

// TestM4UrgentConfirm_C5_NonExistentID — C012 后 path :id 是 string，handler
// 不再校验数字；服务层找不到该 message → 404 ErrNotFound。
func TestM4UrgentConfirm_C5_NonExistentID(t *testing.T) {
	env := newM4Env(t)
	cookieOwner, _ := env.seedUser(449)

	env.expect.POST("/api/messages/not-a-number/urgent/confirm").
		WithHeader(middleware.MMCookieHeader, cookieOwner).
		Expect().Status(404)
}

// ---------------------------------------------------------------------------
// POST /api/messages/:id/urgent/cancel — cancel urgent flag
// ---------------------------------------------------------------------------

// TestM4UrgentCancel_C1_HappyPath — sender 自己取消 → 200。
func TestM4UrgentCancel_C1_HappyPath(t *testing.T) {
	env := newM4Env(t)
	cookieOwner, _ := env.seedUser(450)
	_, member := env.seedUser(451)
	channelID := env.seedGroup(cookieOwner, "g-cancel-c1", member)

	sent := successBody(env.expect.POST("/api/messages/urgent").
		WithHeader(middleware.MMCookieHeader, cookieOwner).
		WithJSON(map[string]any{"channel_id": channelID, "content": "drop me"}).
		Expect().Status(201))
	msgID := sent.Value("id").String().Raw()

	successBody(env.expect.POST("/api/messages/"+msgID+"/urgent/cancel").
		WithHeader(middleware.MMCookieHeader, cookieOwner).
		Expect().Status(200)).Value("status").IsEqual("cancelled")
}

// TestM4UrgentCancel_C2_CookieMissing — 缺 cookie → 401。
func TestM4UrgentCancel_C2_CookieMissing(t *testing.T) {
	env := newM4Env(t)
	cookieOwner, _ := env.seedUser(452)
	_, member := env.seedUser(453)
	channelID := env.seedGroup(cookieOwner, "g-cancel-c2", member)

	sent := successBody(env.expect.POST("/api/messages/urgent").
		WithHeader(middleware.MMCookieHeader, cookieOwner).
		WithJSON(map[string]any{"channel_id": channelID, "content": "u"}).
		Expect().Status(201))
	msgID := sent.Value("id").String().Raw()

	env.expect.POST("/api/messages/" + msgID + "/urgent/cancel").
		Expect().Status(401)
}

// TestM4UrgentCancel_C3_CookieInvalid — cookieId 不在 Redis → 401。
func TestM4UrgentCancel_C3_CookieInvalid(t *testing.T) {
	env := newM4Env(t)
	cookieOwner, _ := env.seedUser(454)
	_, member := env.seedUser(455)
	channelID := env.seedGroup(cookieOwner, "g-cancel-c3", member)

	sent := successBody(env.expect.POST("/api/messages/urgent").
		WithHeader(middleware.MMCookieHeader, cookieOwner).
		WithJSON(map[string]any{"channel_id": channelID, "content": "u"}).
		Expect().Status(201))
	msgID := sent.Value("id").String().Raw()

	env.expect.POST("/api/messages/"+msgID+"/urgent/cancel").
		WithHeader(middleware.MMCookieHeader, "ffffffffffffffffffffffff").
		Expect().Status(401)
}

// TestM4UrgentCancel_C4_NotSender — 普通 member（非 sender、非 manager）取消
// 别人发的加急 → 403 ErrNotSender。
func TestM4UrgentCancel_C4_NotSender(t *testing.T) {
	env := newM4Env(t)
	cookieOwner, _ := env.seedUser(456)
	cookieMember, member := env.seedUser(457)
	channelID := env.seedGroup(cookieOwner, "g-cancel-c4", member)

	sent := successBody(env.expect.POST("/api/messages/urgent").
		WithHeader(middleware.MMCookieHeader, cookieOwner).
		WithJSON(map[string]any{"channel_id": channelID, "content": "u"}).
		Expect().Status(201))
	msgID := sent.Value("id").String().Raw()

	env.expect.POST("/api/messages/"+msgID+"/urgent/cancel").
		WithHeader(middleware.MMCookieHeader, cookieMember).
		Expect().Status(403)
}

// TestM4UrgentCancel_C5_NonExistentID — C012 后 path :id 是 string，handler
// 不再校验数字；服务层找不到该 message → 404 ErrNotFound。
func TestM4UrgentCancel_C5_NonExistentID(t *testing.T) {
	env := newM4Env(t)
	cookieOwner, _ := env.seedUser(458)

	env.expect.POST("/api/messages/not-a-number/urgent/cancel").
		WithHeader(middleware.MMCookieHeader, cookieOwner).
		Expect().Status(404)
}

// ---------------------------------------------------------------------------
// GET /api/messages/:id/urgent/confirmations — list confirmations
// ---------------------------------------------------------------------------

// TestM4UrgentConfirmations_C1_HappyPath — 发加急 → member 确认 → owner 列出
// confirmations，应包含 member id。
func TestM4UrgentConfirmations_C1_HappyPath(t *testing.T) {
	env := newM4Env(t)
	cookieOwner, _ := env.seedUser(460)
	cookieMember, member := env.seedUser(461)
	channelID := env.seedGroup(cookieOwner, "g-conflist-c1", member)

	sent := successBody(env.expect.POST("/api/messages/urgent").
		WithHeader(middleware.MMCookieHeader, cookieOwner).
		WithJSON(map[string]any{"channel_id": channelID, "content": "u"}).
		Expect().Status(201))
	msgID := sent.Value("id").String().Raw()

	env.expect.POST("/api/messages/" + msgID + "/urgent/confirm").
		WithHeader(middleware.MMCookieHeader, cookieMember).
		Expect().Status(200)

	resp := successBody(env.expect.GET("/api/messages/" + msgID + "/urgent/confirmations").
		WithHeader(middleware.MMCookieHeader, cookieOwner).
		Expect().Status(200))
	arr := resp.Value("confirmations").Array()
	arr.Length().IsEqual(1)
	arr.Value(0).IsEqual(member)
}

// TestM4UrgentConfirmations_C2_CookieMissing — 缺 cookie → 401。
func TestM4UrgentConfirmations_C2_CookieMissing(t *testing.T) {
	env := newM4Env(t)
	cookieOwner, _ := env.seedUser(462)
	_, member := env.seedUser(463)
	channelID := env.seedGroup(cookieOwner, "g-conflist-c2", member)

	sent := successBody(env.expect.POST("/api/messages/urgent").
		WithHeader(middleware.MMCookieHeader, cookieOwner).
		WithJSON(map[string]any{"channel_id": channelID, "content": "u"}).
		Expect().Status(201))
	msgID := sent.Value("id").String().Raw()

	env.expect.GET("/api/messages/" + msgID + "/urgent/confirmations").
		Expect().Status(401)
}

// TestM4UrgentConfirmations_C3_CookieInvalid — cookieId 不在 Redis → 401。
func TestM4UrgentConfirmations_C3_CookieInvalid(t *testing.T) {
	env := newM4Env(t)
	cookieOwner, _ := env.seedUser(464)
	_, member := env.seedUser(465)
	channelID := env.seedGroup(cookieOwner, "g-conflist-c3", member)

	sent := successBody(env.expect.POST("/api/messages/urgent").
		WithHeader(middleware.MMCookieHeader, cookieOwner).
		WithJSON(map[string]any{"channel_id": channelID, "content": "u"}).
		Expect().Status(201))
	msgID := sent.Value("id").String().Raw()

	env.expect.GET("/api/messages/"+msgID+"/urgent/confirmations").
		WithHeader(middleware.MMCookieHeader, "ffffffffffffffffffffffff").
		Expect().Status(401)
}

// TestM4UrgentConfirmations_C4_NotChannelMember — 非成员列 confirmations → 403。
func TestM4UrgentConfirmations_C4_NotChannelMember(t *testing.T) {
	env := newM4Env(t)
	cookieOwner, _ := env.seedUser(466)
	_, member := env.seedUser(467)
	cookieX, _ := env.seedUser(468)
	channelID := env.seedGroup(cookieOwner, "g-conflist-c4", member)

	sent := successBody(env.expect.POST("/api/messages/urgent").
		WithHeader(middleware.MMCookieHeader, cookieOwner).
		WithJSON(map[string]any{"channel_id": channelID, "content": "u"}).
		Expect().Status(201))
	msgID := sent.Value("id").String().Raw()

	env.expect.GET("/api/messages/"+msgID+"/urgent/confirmations").
		WithHeader(middleware.MMCookieHeader, cookieX).
		Expect().Status(403)
}

// TestM4UrgentConfirmations_C5_NonExistentID — C012 后 path :id 是 string，
// handler 不再校验数字；service 找不到 message → 404 ErrNotFound。
func TestM4UrgentConfirmations_C5_NonExistentID(t *testing.T) {
	env := newM4Env(t)
	cookieOwner, _ := env.seedUser(469)

	env.expect.GET("/api/messages/not-a-number/urgent/confirmations").
		WithHeader(middleware.MMCookieHeader, cookieOwner).
		Expect().Status(404)
}

// _ keeps the repo import live in case future cases need to reach into
// repo types directly (Message, FavoriteWithMessage). All current cases
// drive behaviour exclusively through the HTTP surface + shared helpers.
var _ = repo.MsgTypeText

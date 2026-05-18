//go:build integration

package integration

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"

	"im-server/internal/middleware"
	"im-server/internal/repo"
)

// Seed 范围说明（Phase P1/P2 补 case 矩阵）：
//   - 40-49: TestM4FriendRequestAccept（既有）
//   - 2200-2299: Phase P2 新增的 reject / block / list / users-search C2-C5 矩阵

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
	friendshipID := row.Value("id").String().Raw()
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

// ----------------------------------------------------------------------------
// Phase P2 — reject / block / list / users-search 5-case 矩阵
// ----------------------------------------------------------------------------

// seedPendingFriendRequest 在 (requesterCookie, addresseeCookie) 之间创建一条
// pending 请求，并返回 friendship_id。复用 POST + GET pending 两步，避免直接
// reach into repo（保持端到端契约视角）。
func seedPendingFriendRequest(env *m4env, requesterCookie, addresseeCookie, addresseeID string) string {
	env.t.Helper()
	env.expect.POST("/api/friends/request").
		WithHeader(middleware.MMCookieHeader, requesterCookie).
		WithJSON(map[string]any{"addressee_id": addresseeID}).
		Expect().Status(201)

	pending := successBodyArray(env.expect.GET("/api/friends/pending").
		WithHeader(middleware.MMCookieHeader, addresseeCookie).
		Expect().Status(200))
	pending.Length().Gt(0)
	return pending.Value(0).Object().Value("id").String().Raw()
}

// ---- POST /api/friends/reject -----------------------------------------------

// TestM4FriendReject_C1_HappyPath — addressee 拒绝 pending 请求 → 200 +
// envelope.data.status == "rejected"；repo 内 status 也翻为 Rejected。
func TestM4FriendReject_C1_HappyPath(t *testing.T) {
	env := newM4Env(t)
	cookieReq, requester := env.seedUser(2200)
	cookieAddr, addressee := env.seedUser(2201)

	friendshipID := seedPendingFriendRequest(env, cookieReq, cookieAddr, addressee)

	successBody(env.expect.POST("/api/friends/reject").
		WithHeader(middleware.MMCookieHeader, cookieAddr).
		WithJSON(map[string]any{"friendship_id": friendshipID}).
		Expect().Status(200)).
		Value("status").IsEqual("rejected")

	got, err := env.friends.GetFriendship(context.Background(), requester, addressee)
	require.NoError(t, err)
	require.Equal(t, repo.FriendshipRejected, got.Status, "row must flip to rejected")
}

// TestM4FriendReject_C2_CookieMissing — 401.
func TestM4FriendReject_C2_CookieMissing(t *testing.T) {
	env := newM4Env(t)
	errorBody(env.expect.POST("/api/friends/reject").
		WithJSON(map[string]any{"friendship_id": "anything"}).
		Expect().Status(401))
}

// TestM4FriendReject_C3_CookieInvalid — 401.
func TestM4FriendReject_C3_CookieInvalid(t *testing.T) {
	env := newM4Env(t)
	errorBody(env.expect.POST("/api/friends/reject").
		WithHeader(middleware.MMCookieHeader, "deadbeefdeadbeefdeadbeef").
		WithJSON(map[string]any{"friendship_id": "anything"}).
		Expect().Status(401))
}

// TestM4FriendReject_C4_NotFound — friendship_id 指向不存在的 pending → 404
// "pending request not found"（repo.ErrNotFound 分支）。
func TestM4FriendReject_C4_NotFound(t *testing.T) {
	env := newM4Env(t)
	cookie, _ := env.seedUser(2210)

	errorBody(env.expect.POST("/api/friends/reject").
		WithHeader(middleware.MMCookieHeader, cookie).
		WithJSON(map[string]any{"friendship_id": "no-such-friendship"}).
		Expect().Status(404)).
		Value("error").String().Contains("not found")
}

// TestM4FriendReject_C5_MissingID — friendship_id 缺失 → 422.
func TestM4FriendReject_C5_MissingID(t *testing.T) {
	env := newM4Env(t)
	cookie, _ := env.seedUser(2211)

	errorBody(env.expect.POST("/api/friends/reject").
		WithHeader(middleware.MMCookieHeader, cookie).
		WithJSON(map[string]any{}).
		Expect().Status(422)).
		Value("error").String().Contains("friendship_id")
}

// ---- POST /api/friends/block ------------------------------------------------

// TestM4FriendBlock_C1_HappyPath — 200 + status "blocked"。block 调用前两人
// 没有 friendship 行，handler 也允许（service.BlockUser 创建 blocked 状态）。
func TestM4FriendBlock_C1_HappyPath(t *testing.T) {
	env := newM4Env(t)
	cookieA, _ := env.seedUser(2220)
	_, idB := env.seedUser(2221)

	successBody(env.expect.POST("/api/friends/block").
		WithHeader(middleware.MMCookieHeader, cookieA).
		WithJSON(map[string]any{"user_id": idB}).
		Expect().Status(200)).
		Value("status").IsEqual("blocked")
}

// TestM4FriendBlock_C2_CookieMissing — 401.
func TestM4FriendBlock_C2_CookieMissing(t *testing.T) {
	env := newM4Env(t)
	errorBody(env.expect.POST("/api/friends/block").
		WithJSON(map[string]any{"user_id": "any"}).
		Expect().Status(401))
}

// TestM4FriendBlock_C3_CookieInvalid — 401.
func TestM4FriendBlock_C3_CookieInvalid(t *testing.T) {
	env := newM4Env(t)
	errorBody(env.expect.POST("/api/friends/block").
		WithHeader(middleware.MMCookieHeader, "deadbeefdeadbeefdeadbeef").
		WithJSON(map[string]any{"user_id": "any"}).
		Expect().Status(401))
}

// TestM4FriendBlock_C5_MissingUserID — user_id 缺失 → 422.
func TestM4FriendBlock_C5_MissingUserID(t *testing.T) {
	env := newM4Env(t)
	cookie, _ := env.seedUser(2230)

	errorBody(env.expect.POST("/api/friends/block").
		WithHeader(middleware.MMCookieHeader, cookie).
		WithJSON(map[string]any{}).
		Expect().Status(422)).
		Value("error").String().Contains("user_id")
}

// TestM4FriendBlock_C5b_BadJSON — body 非合法 JSON → 400.
func TestM4FriendBlock_C5b_BadJSON(t *testing.T) {
	env := newM4Env(t)
	cookie, _ := env.seedUser(2231)

	errorBody(env.expect.POST("/api/friends/block").
		WithHeader(middleware.MMCookieHeader, cookie).
		WithHeader("Content-Type", "application/json").
		WithBytes([]byte("not-json")).
		Expect().Status(400)).
		Value("error").String().Contains("JSON")
}

// ---- GET /api/friends -------------------------------------------------------

// TestM4FriendList_C1_HappyPath — 接受朋友请求后 GET /api/friends → 200 +
// envelope.data 数组里含 peer 的 user_id。
func TestM4FriendList_C1_HappyPath(t *testing.T) {
	env := newM4Env(t)
	cookieReq, requester := env.seedUser(2240)
	cookieAddr, addressee := env.seedUser(2241)

	friendshipID := seedPendingFriendRequest(env, cookieReq, cookieAddr, addressee)
	env.expect.POST("/api/friends/accept").
		WithHeader(middleware.MMCookieHeader, cookieAddr).
		WithJSON(map[string]any{"friendship_id": friendshipID}).
		Expect().Status(200)

	arr := successBodyArray(env.expect.GET("/api/friends").
		WithHeader(middleware.MMCookieHeader, cookieReq).
		Expect().Status(200))
	arr.Length().Gt(0)
	arr.Value(0).String().IsEqual(addressee)
	_ = requester
}

// TestM4FriendList_C1b_EmptyList — 全新 user 调 GET /api/friends → 200 + []。
// 验证 handler 对 nil 做 `friends = []string{}` 兜底，envelope 不会 emit null。
func TestM4FriendList_C1b_EmptyList(t *testing.T) {
	env := newM4Env(t)
	cookie, _ := env.seedUser(2245)

	arr := successBodyArray(env.expect.GET("/api/friends").
		WithHeader(middleware.MMCookieHeader, cookie).
		Expect().Status(200))
	arr.Length().IsEqual(0)
}

// TestM4FriendList_C2_CookieMissing — 401.
func TestM4FriendList_C2_CookieMissing(t *testing.T) {
	env := newM4Env(t)
	errorBody(env.expect.GET("/api/friends").Expect().Status(401))
}

// TestM4FriendList_C3_CookieInvalid — 401.
func TestM4FriendList_C3_CookieInvalid(t *testing.T) {
	env := newM4Env(t)
	errorBody(env.expect.GET("/api/friends").
		WithHeader(middleware.MMCookieHeader, "deadbeefdeadbeefdeadbeef").
		Expect().Status(401))
}

// ---- GET /api/users/search --------------------------------------------------

// TestM4UsersSearch_C1_HappyPath — M4 后 cses 拥有用户目录，im 端永远返回 []
// 数组（保持 wire shape 兼容旧 client）。
func TestM4UsersSearch_C1_HappyPath(t *testing.T) {
	env := newM4Env(t)
	cookie, _ := env.seedUser(2250)

	arr := successBodyArray(env.expect.GET("/api/users/search").
		WithHeader(middleware.MMCookieHeader, cookie).
		WithQuery("q", "anything").
		Expect().Status(200))
	arr.Length().IsEqual(0)
}

// TestM4UsersSearch_C2_CookieMissing — 401.
func TestM4UsersSearch_C2_CookieMissing(t *testing.T) {
	env := newM4Env(t)
	errorBody(env.expect.GET("/api/users/search").
		WithQuery("q", "anything").
		Expect().Status(401))
}

// TestM4UsersSearch_C3_CookieInvalid — 401.
func TestM4UsersSearch_C3_CookieInvalid(t *testing.T) {
	env := newM4Env(t)
	errorBody(env.expect.GET("/api/users/search").
		WithHeader(middleware.MMCookieHeader, "deadbeefdeadbeefdeadbeef").
		WithQuery("q", "anything").
		Expect().Status(401))
}

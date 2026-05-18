//go:build integration

// Phase P2 — friend.request/accept/pending + quick_reply ×4 + notification ×4
// C2-C5 错误矩阵。Happy path 已在 m4_friend_test.go / m4_notification_quickreply_test.go。
// 路由由 buildEngine 装配。Seed 范围 2400-2599。
package integration

import (
	"testing"

	"im-server/internal/middleware"
	"im-server/internal/repo"
)

// ---------------------------------------------------------------------------
// friend.request — C2/C3/C4/C5
// ---------------------------------------------------------------------------

// TestM4FriendRequest_C2_CookieMissing — 401.
func TestM4FriendRequest_C2_CookieMissing(t *testing.T) {
	env := newM4Env(t)
	errorBody(env.expect.POST("/api/friends/request").
		WithJSON(map[string]any{"addressee_id": "any"}).
		Expect().Status(401))
}

// TestM4FriendRequest_C3_CookieInvalid — 401.
func TestM4FriendRequest_C3_CookieInvalid(t *testing.T) {
	env := newM4Env(t)
	errorBody(env.expect.POST("/api/friends/request").
		WithHeader(middleware.MMCookieHeader, "deadbeefdeadbeefdeadbeef").
		WithJSON(map[string]any{"addressee_id": "any"}).
		Expect().Status(401))
}

// TestM4FriendRequest_C4_DuplicateConflict — 重复 request → 409.
func TestM4FriendRequest_C4_DuplicateConflict(t *testing.T) {
	env := newM4Env(t)
	cookieA, _ := env.seedUser(2400)
	_, idB := env.seedUser(2401)

	env.expect.POST("/api/friends/request").
		WithHeader(middleware.MMCookieHeader, cookieA).
		WithJSON(map[string]any{"addressee_id": idB}).
		Expect().Status(201)

	errorBody(env.expect.POST("/api/friends/request").
		WithHeader(middleware.MMCookieHeader, cookieA).
		WithJSON(map[string]any{"addressee_id": idB}).
		Expect().Status(409)).
		Value("error").String().Contains("already exists")
}

// TestM4FriendRequest_C5_MissingAddressee — addressee_id 缺失 → 422.
func TestM4FriendRequest_C5_MissingAddressee(t *testing.T) {
	env := newM4Env(t)
	cookie, _ := env.seedUser(2410)
	errorBody(env.expect.POST("/api/friends/request").
		WithHeader(middleware.MMCookieHeader, cookie).
		WithJSON(map[string]any{}).
		Expect().Status(422)).
		Value("error").String().Contains("addressee_id")
}

// TestM4FriendRequest_C5b_BadJSON — body 非 JSON → 400.
func TestM4FriendRequest_C5b_BadJSON(t *testing.T) {
	env := newM4Env(t)
	cookie, _ := env.seedUser(2411)
	errorBody(env.expect.POST("/api/friends/request").
		WithHeader(middleware.MMCookieHeader, cookie).
		WithHeader("Content-Type", "application/json").
		WithBytes([]byte("not-json")).
		Expect().Status(400))
}

// ---------------------------------------------------------------------------
// friend.accept — C2/C3/C4/C5
// ---------------------------------------------------------------------------

// TestM4FriendAccept_C2_CookieMissing — 401.
func TestM4FriendAccept_C2_CookieMissing(t *testing.T) {
	env := newM4Env(t)
	errorBody(env.expect.POST("/api/friends/accept").
		WithJSON(map[string]any{"friendship_id": "any"}).
		Expect().Status(401))
}

// TestM4FriendAccept_C3_CookieInvalid — 401.
func TestM4FriendAccept_C3_CookieInvalid(t *testing.T) {
	env := newM4Env(t)
	errorBody(env.expect.POST("/api/friends/accept").
		WithHeader(middleware.MMCookieHeader, "deadbeefdeadbeefdeadbeef").
		WithJSON(map[string]any{"friendship_id": "any"}).
		Expect().Status(401))
}

// TestM4FriendAccept_C4_NotFound — friendship_id 不存在 → 404.
func TestM4FriendAccept_C4_NotFound(t *testing.T) {
	env := newM4Env(t)
	cookie, _ := env.seedUser(2420)
	errorBody(env.expect.POST("/api/friends/accept").
		WithHeader(middleware.MMCookieHeader, cookie).
		WithJSON(map[string]any{"friendship_id": "ghost"}).
		Expect().Status(404)).
		Value("error").String().Contains("not found")
}

// TestM4FriendAccept_C5_MissingID — friendship_id 缺失 → 422.
func TestM4FriendAccept_C5_MissingID(t *testing.T) {
	env := newM4Env(t)
	cookie, _ := env.seedUser(2421)
	errorBody(env.expect.POST("/api/friends/accept").
		WithHeader(middleware.MMCookieHeader, cookie).
		WithJSON(map[string]any{}).
		Expect().Status(422)).
		Value("error").String().Contains("friendship_id")
}

// ---------------------------------------------------------------------------
// friend.pending — C2/C3
// ---------------------------------------------------------------------------

// TestM4FriendPending_C2_CookieMissing — 401.
func TestM4FriendPending_C2_CookieMissing(t *testing.T) {
	env := newM4Env(t)
	errorBody(env.expect.GET("/api/friends/pending").Expect().Status(401))
}

// TestM4FriendPending_C3_CookieInvalid — 401.
func TestM4FriendPending_C3_CookieInvalid(t *testing.T) {
	env := newM4Env(t)
	errorBody(env.expect.GET("/api/friends/pending").
		WithHeader(middleware.MMCookieHeader, "deadbeefdeadbeefdeadbeef").
		Expect().Status(401))
}

// ---------------------------------------------------------------------------
// quick_reply.create — C2/C3/C5
// ---------------------------------------------------------------------------

// TestM4QuickReplyCreate_C2_CookieMissing — 401.
func TestM4QuickReplyCreate_C2_CookieMissing(t *testing.T) {
	env := newM4Env(t)
	errorBody(env.expect.POST("/api/quick-replies").
		WithJSON(map[string]any{"label": "l", "content": "c"}).
		Expect().Status(401))
}

// TestM4QuickReplyCreate_C3_CookieInvalid — 401.
func TestM4QuickReplyCreate_C3_CookieInvalid(t *testing.T) {
	env := newM4Env(t)
	errorBody(env.expect.POST("/api/quick-replies").
		WithHeader(middleware.MMCookieHeader, "deadbeefdeadbeefdeadbeef").
		WithJSON(map[string]any{"label": "l", "content": "c"}).
		Expect().Status(401))
}

// TestM4QuickReplyCreate_C5_MissingLabel — label 空 → 422.
func TestM4QuickReplyCreate_C5_MissingLabel(t *testing.T) {
	env := newM4Env(t)
	cookie, _ := env.seedUser(2500)
	errorBody(env.expect.POST("/api/quick-replies").
		WithHeader(middleware.MMCookieHeader, cookie).
		WithJSON(map[string]any{"content": "c"}).
		Expect().Status(422)).
		Value("error").String().Contains("label")
}

// TestM4QuickReplyCreate_C5b_MissingContent — content 空 → 422.
func TestM4QuickReplyCreate_C5b_MissingContent(t *testing.T) {
	env := newM4Env(t)
	cookie, _ := env.seedUser(2501)
	errorBody(env.expect.POST("/api/quick-replies").
		WithHeader(middleware.MMCookieHeader, cookie).
		WithJSON(map[string]any{"label": "l"}).
		Expect().Status(422)).
		Value("error").String().Contains("content")
}

// ---------------------------------------------------------------------------
// quick_reply.list — C2/C3
// ---------------------------------------------------------------------------

// TestM4QuickReplyList_C2_CookieMissing — 401.
func TestM4QuickReplyList_C2_CookieMissing(t *testing.T) {
	env := newM4Env(t)
	errorBody(env.expect.GET("/api/quick-replies").Expect().Status(401))
}

// TestM4QuickReplyList_C3_CookieInvalid — 401.
func TestM4QuickReplyList_C3_CookieInvalid(t *testing.T) {
	env := newM4Env(t)
	errorBody(env.expect.GET("/api/quick-replies").
		WithHeader(middleware.MMCookieHeader, "deadbeefdeadbeefdeadbeef").
		Expect().Status(401))
}

// ---------------------------------------------------------------------------
// quick_reply.update — C2/C3/C4/C5
// ---------------------------------------------------------------------------

// TestM4QuickReplyUpdate_C2_CookieMissing — 401.
func TestM4QuickReplyUpdate_C2_CookieMissing(t *testing.T) {
	env := newM4Env(t)
	errorBody(env.expect.PATCH("/api/quick-replies/anything").
		WithJSON(map[string]any{}).
		Expect().Status(401))
}

// TestM4QuickReplyUpdate_C3_CookieInvalid — 401.
func TestM4QuickReplyUpdate_C3_CookieInvalid(t *testing.T) {
	env := newM4Env(t)
	errorBody(env.expect.PATCH("/api/quick-replies/anything").
		WithHeader(middleware.MMCookieHeader, "deadbeefdeadbeefdeadbeef").
		WithJSON(map[string]any{}).
		Expect().Status(401))
}

// TestM4QuickReplyUpdate_C4_NotOwner — B 改 A 的 → 403.
func TestM4QuickReplyUpdate_C4_NotOwner(t *testing.T) {
	env := newM4Env(t)
	cookieA, _ := env.seedUser(2510)
	cookieB, _ := env.seedUser(2511)

	created := successBody(env.expect.POST("/api/quick-replies").
		WithHeader(middleware.MMCookieHeader, cookieA).
		WithJSON(map[string]any{"label": "a", "content": "a"}).
		Expect().Status(201))
	id := created.Value("id").String().Raw()

	errorBody(env.expect.PATCH("/api/quick-replies/"+id).
		WithHeader(middleware.MMCookieHeader, cookieB).
		WithJSON(map[string]any{"label": "hack"}).
		Expect().Status(403)).
		Value("error").String().Contains("not your")
}

// TestM4QuickReplyUpdate_C5_NotFound — id 指向不存在 → 404.
func TestM4QuickReplyUpdate_C5_NotFound(t *testing.T) {
	env := newM4Env(t)
	cookie, _ := env.seedUser(2512)

	errorBody(env.expect.PATCH("/api/quick-replies/ghost-id").
		WithHeader(middleware.MMCookieHeader, cookie).
		WithJSON(map[string]any{"label": "x"}).
		Expect().Status(404)).
		Value("error").String().Contains("not found")
}

// ---------------------------------------------------------------------------
// quick_reply.delete — C2/C3/C4/C5
// ---------------------------------------------------------------------------

// TestM4QuickReplyDelete_C2_CookieMissing — 401.
func TestM4QuickReplyDelete_C2_CookieMissing(t *testing.T) {
	env := newM4Env(t)
	errorBody(env.expect.DELETE("/api/quick-replies/anything").Expect().Status(401))
}

// TestM4QuickReplyDelete_C3_CookieInvalid — 401.
func TestM4QuickReplyDelete_C3_CookieInvalid(t *testing.T) {
	env := newM4Env(t)
	errorBody(env.expect.DELETE("/api/quick-replies/anything").
		WithHeader(middleware.MMCookieHeader, "deadbeefdeadbeefdeadbeef").
		Expect().Status(401))
}

// TestM4QuickReplyDelete_C4_NotOwner — B 删 A 的 → 403.
func TestM4QuickReplyDelete_C4_NotOwner(t *testing.T) {
	env := newM4Env(t)
	cookieA, _ := env.seedUser(2520)
	cookieB, _ := env.seedUser(2521)

	created := successBody(env.expect.POST("/api/quick-replies").
		WithHeader(middleware.MMCookieHeader, cookieA).
		WithJSON(map[string]any{"label": "del-target", "content": "c"}).
		Expect().Status(201))
	id := created.Value("id").String().Raw()

	errorBody(env.expect.DELETE("/api/quick-replies/"+id).
		WithHeader(middleware.MMCookieHeader, cookieB).
		Expect().Status(403)).
		Value("error").String().Contains("not your")
}

// TestM4QuickReplyDelete_C5_NotFound — 404.
func TestM4QuickReplyDelete_C5_NotFound(t *testing.T) {
	env := newM4Env(t)
	cookie, _ := env.seedUser(2522)
	errorBody(env.expect.DELETE("/api/quick-replies/ghost-id").
		WithHeader(middleware.MMCookieHeader, cookie).
		Expect().Status(404)).
		Value("error").String().Contains("not found")
}

// ---------------------------------------------------------------------------
// notification.post — C2/C3/C5
// ---------------------------------------------------------------------------

// TestM4NotificationPost_C2_CookieMissing — 401.
func TestM4NotificationPost_C2_CookieMissing(t *testing.T) {
	env := newM4Env(t)
	errorBody(env.expect.POST("/api/notifications").
		WithJSON(map[string]any{"receiver_id": "x", "title": "t", "type": repo.NotificationTypeGeneric}).
		Expect().Status(401))
}

// TestM4NotificationPost_C3_CookieInvalid — 401.
func TestM4NotificationPost_C3_CookieInvalid(t *testing.T) {
	env := newM4Env(t)
	errorBody(env.expect.POST("/api/notifications").
		WithHeader(middleware.MMCookieHeader, "deadbeefdeadbeefdeadbeef").
		WithJSON(map[string]any{"receiver_id": "x", "title": "t", "type": repo.NotificationTypeGeneric}).
		Expect().Status(401))
}

// TestM4NotificationPost_C5_MissingTitle — title 空 → 422.
func TestM4NotificationPost_C5_MissingTitle(t *testing.T) {
	env := newM4Env(t)
	cookie, _ := env.seedUser(2530)
	_, recv := env.seedUser(2531)

	errorBody(env.expect.POST("/api/notifications").
		WithHeader(middleware.MMCookieHeader, cookie).
		WithJSON(map[string]any{"receiver_id": recv, "type": repo.NotificationTypeGeneric}).
		Expect().Status(422)).
		Value("error").String().Contains("title")
}

// TestM4NotificationPost_C5b_MissingReceiver — receiver_id 空 → 422.
func TestM4NotificationPost_C5b_MissingReceiver(t *testing.T) {
	env := newM4Env(t)
	cookie, _ := env.seedUser(2532)

	errorBody(env.expect.POST("/api/notifications").
		WithHeader(middleware.MMCookieHeader, cookie).
		WithJSON(map[string]any{"title": "hi", "type": repo.NotificationTypeGeneric}).
		Expect().Status(422)).
		Value("error").String().Contains("receiver_id")
}

// ---------------------------------------------------------------------------
// notification.read — C2/C3/C4
// ---------------------------------------------------------------------------

// TestM4NotificationRead_C2_CookieMissing — 401.
func TestM4NotificationRead_C2_CookieMissing(t *testing.T) {
	env := newM4Env(t)
	errorBody(env.expect.POST("/api/notifications/x/read").Expect().Status(401))
}

// TestM4NotificationRead_C3_CookieInvalid — 401.
func TestM4NotificationRead_C3_CookieInvalid(t *testing.T) {
	env := newM4Env(t)
	errorBody(env.expect.POST("/api/notifications/x/read").
		WithHeader(middleware.MMCookieHeader, "deadbeefdeadbeefdeadbeef").
		Expect().Status(401))
}

// TestM4NotificationRead_C4_NotFound — id 不存在 → 404.
func TestM4NotificationRead_C4_NotFound(t *testing.T) {
	env := newM4Env(t)
	cookie, _ := env.seedUser(2540)
	errorBody(env.expect.POST("/api/notifications/ghost-id/read").
		WithHeader(middleware.MMCookieHeader, cookie).
		Expect().Status(404)).
		Value("error").String().Contains("not found")
}

// ---------------------------------------------------------------------------
// notification.listSent / listReceived — C2/C3
// ---------------------------------------------------------------------------

// TestM4NotificationListSent_C2_CookieMissing — 401.
func TestM4NotificationListSent_C2_CookieMissing(t *testing.T) {
	env := newM4Env(t)
	errorBody(env.expect.GET("/api/notifications/sent").Expect().Status(401))
}

// TestM4NotificationListSent_C3_CookieInvalid — 401.
func TestM4NotificationListSent_C3_CookieInvalid(t *testing.T) {
	env := newM4Env(t)
	errorBody(env.expect.GET("/api/notifications/sent").
		WithHeader(middleware.MMCookieHeader, "deadbeefdeadbeefdeadbeef").
		Expect().Status(401))
}

// TestM4NotificationListReceived_C2_CookieMissing — 401.
func TestM4NotificationListReceived_C2_CookieMissing(t *testing.T) {
	env := newM4Env(t)
	errorBody(env.expect.GET("/api/notifications/received").Expect().Status(401))
}

// TestM4NotificationListReceived_C3_CookieInvalid — 401.
func TestM4NotificationListReceived_C3_CookieInvalid(t *testing.T) {
	env := newM4Env(t)
	errorBody(env.expect.GET("/api/notifications/received").
		WithHeader(middleware.MMCookieHeader, "deadbeefdeadbeefdeadbeef").
		Expect().Status(401))
}

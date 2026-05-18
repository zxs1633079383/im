//go:build integration

// Phase P6 — auth.me / channel.dm / channel.group / channel.update 缺失矩阵补完。
// Happy path 已在 m4_auth_smoke_test.go / m4_channel_dm_test.go / m4_channel_group_test.go。
// Seed 范围 3300-3499。
package integration

import (
	"testing"

	"im-server/internal/middleware"
)

// ---- auth.me — C3 cookie invalid（C1 + C2 已在 m4_auth_smoke_test.go）-------

// TestM4AuthMe_C3_CookieInvalid — cookieId 指向不存在的 UserData → 401.
func TestM4AuthMe_C3_CookieInvalid(t *testing.T) {
	env := newM4Env(t)
	errorBody(env.expect.GET("/api/auth/me").
		WithHeader(middleware.MMCookieHeader, "deadbeefdeadbeefdeadbeef").
		Expect().Status(401))
}

// ---- channel.dm — C2/C3/C5 ------------------------------------------------

// TestM4ChannelCreateDM_C2_CookieMissing — 401.
func TestM4ChannelCreateDM_C2_CookieMissing(t *testing.T) {
	env := newM4Env(t)
	errorBody(env.expect.POST("/api/channels/dm").
		WithJSON(map[string]any{"peer_id": "any"}).
		Expect().Status(401))
}

// TestM4ChannelCreateDM_C3_CookieInvalid — 401.
func TestM4ChannelCreateDM_C3_CookieInvalid(t *testing.T) {
	env := newM4Env(t)
	errorBody(env.expect.POST("/api/channels/dm").
		WithHeader(middleware.MMCookieHeader, "deadbeefdeadbeefdeadbeef").
		WithJSON(map[string]any{"peer_id": "any"}).
		Expect().Status(401))
}

// TestM4ChannelCreateDM_C5_MissingPeer — peer_id 缺 → 422.
func TestM4ChannelCreateDM_C5_MissingPeer(t *testing.T) {
	env := newM4Env(t)
	cookie, _ := env.seedUser(3300)
	errorBody(env.expect.POST("/api/channels/dm").
		WithHeader(middleware.MMCookieHeader, cookie).
		WithJSON(map[string]any{}).
		Expect().Status(422)).
		Value("error").String().Contains("peer_id")
}

// TestM4ChannelCreateDM_C5b_BadJSON — body 非 JSON → 400.
func TestM4ChannelCreateDM_C5b_BadJSON(t *testing.T) {
	env := newM4Env(t)
	cookie, _ := env.seedUser(3301)
	errorBody(env.expect.POST("/api/channels/dm").
		WithHeader(middleware.MMCookieHeader, cookie).
		WithHeader("Content-Type", "application/json").
		WithBytes([]byte("not-json")).
		Expect().Status(400)).
		Value("error").String().Contains("JSON")
}

// ---- channel.group (POST /api/channels) — C2/C3/C5 ------------------------

// TestM4ChannelCreateGroup_C2_CookieMissing — 401.
func TestM4ChannelCreateGroup_C2_CookieMissing(t *testing.T) {
	env := newM4Env(t)
	errorBody(env.expect.POST("/api/channels").
		WithJSON(map[string]any{"name": "x", "member_ids": []string{}}).
		Expect().Status(401))
}

// TestM4ChannelCreateGroup_C3_CookieInvalid — 401.
func TestM4ChannelCreateGroup_C3_CookieInvalid(t *testing.T) {
	env := newM4Env(t)
	errorBody(env.expect.POST("/api/channels").
		WithHeader(middleware.MMCookieHeader, "deadbeefdeadbeefdeadbeef").
		WithJSON(map[string]any{"name": "x", "member_ids": []string{}}).
		Expect().Status(401))
}

// TestM4ChannelCreateGroup_C5_BadJSON — body 非 JSON → 400.
func TestM4ChannelCreateGroup_C5_BadJSON(t *testing.T) {
	env := newM4Env(t)
	cookie, _ := env.seedUser(3310)
	errorBody(env.expect.POST("/api/channels").
		WithHeader(middleware.MMCookieHeader, cookie).
		WithHeader("Content-Type", "application/json").
		WithBytes([]byte("not-json")).
		Expect().Status(400)).
		Value("error").String().Contains("JSON")
}

// ---- PUT /api/channels/:id — 完整 C1-C5 矩阵（之前 0 测试）----------------

// TestM4ChannelUpdate_C1_HappyPath — owner 改 name + avatar_url → 200，
// envelope.data 回显新值。
func TestM4ChannelUpdate_C1_HappyPath(t *testing.T) {
	env := newM4Env(t)
	cookieOwner, _ := env.seedUser(3320)
	_, peerID := env.seedUser(3321)
	channelID := env.seedGroup(cookieOwner, "p6-update-original", peerID)

	body := successBody(env.expect.PUT("/api/channels/"+channelID).
		WithHeader(middleware.MMCookieHeader, cookieOwner).
		WithJSON(map[string]any{
			"name":       "p6-update-renamed",
			"avatar_url": "https://example.com/new.png",
		}).
		Expect().Status(200))
	body.Value("name").IsEqual("p6-update-renamed")
}

// TestM4ChannelUpdate_C2_CookieMissing — 401.
func TestM4ChannelUpdate_C2_CookieMissing(t *testing.T) {
	env := newM4Env(t)
	errorBody(env.expect.PUT("/api/channels/x").
		WithJSON(map[string]any{}).
		Expect().Status(401))
}

// TestM4ChannelUpdate_C3_CookieInvalid — 401.
func TestM4ChannelUpdate_C3_CookieInvalid(t *testing.T) {
	env := newM4Env(t)
	errorBody(env.expect.PUT("/api/channels/x").
		WithHeader(middleware.MMCookieHeader, "deadbeefdeadbeefdeadbeef").
		WithJSON(map[string]any{}).
		Expect().Status(401))
}

// TestM4ChannelUpdate_C4_NotMember — outsider 改 → 403.
func TestM4ChannelUpdate_C4_NotMember(t *testing.T) {
	env := newM4Env(t)
	cookieOwner, _ := env.seedUser(3330)
	_, peerID := env.seedUser(3331)
	cookieOuter, _ := env.seedUser(3332)
	channelID := env.seedGroup(cookieOwner, "p6-update-outsider", peerID)

	errorBody(env.expect.PUT("/api/channels/"+channelID).
		WithHeader(middleware.MMCookieHeader, cookieOuter).
		WithJSON(map[string]any{"name": "hack"}).
		Expect().Status(403)).
		Value("error").String().Contains("member")
}

// TestM4ChannelUpdate_C4b_NotAdminOrOwner — plain member 改 → 403.
// 当前 group 只有 owner + member（无 admin），member 无权改 → 触发 ErrForbidden。
func TestM4ChannelUpdate_C4b_NotAdminOrOwner(t *testing.T) {
	env := newM4Env(t)
	cookieOwner, _ := env.seedUser(3340)
	cookieMember, memberID := env.seedUser(3341)
	channelID := env.seedGroup(cookieOwner, "p6-update-member-attempt", memberID)

	errorBody(env.expect.PUT("/api/channels/"+channelID).
		WithHeader(middleware.MMCookieHeader, cookieMember).
		WithJSON(map[string]any{"name": "member-attempt"}).
		Expect().Status(403)).
		Value("error").String().Contains("admin")
}

// TestM4ChannelUpdate_C5_BadJSON — body 非 JSON → 400.
func TestM4ChannelUpdate_C5_BadJSON(t *testing.T) {
	env := newM4Env(t)
	cookieOwner, _ := env.seedUser(3350)
	_, peerID := env.seedUser(3351)
	channelID := env.seedGroup(cookieOwner, "p6-update-badjson", peerID)

	errorBody(env.expect.PUT("/api/channels/"+channelID).
		WithHeader(middleware.MMCookieHeader, cookieOwner).
		WithHeader("Content-Type", "application/json").
		WithBytes([]byte("not-json")).
		Expect().Status(400)).
		Value("error").String().Contains("JSON")
}

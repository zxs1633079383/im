//go:build integration

// Phase P5 — announcement 6 endpoint × C2-C5 错误矩阵。Happy path 在
// m4_announcement_reaction_test.go。Seed 范围 3100-3299。
package integration

import (
	"testing"

	"im-server/internal/middleware"
)

// ---- POST /api/announcements -----------------------------------------------

// TestM4AnnouncementCreate_C2_CookieMissing — 401.
func TestM4AnnouncementCreate_C2_CookieMissing(t *testing.T) {
	env := newM4Env(t)
	errorBody(env.expect.POST("/api/announcements").
		WithJSON(map[string]any{}).
		Expect().Status(401))
}

// TestM4AnnouncementCreate_C3_CookieInvalid — 401.
func TestM4AnnouncementCreate_C3_CookieInvalid(t *testing.T) {
	env := newM4Env(t)
	errorBody(env.expect.POST("/api/announcements").
		WithHeader(middleware.MMCookieHeader, "deadbeefdeadbeefdeadbeef").
		WithJSON(map[string]any{}).
		Expect().Status(401))
}

// TestM4AnnouncementCreate_C4_NotMember — outsider 创建 → 403.
func TestM4AnnouncementCreate_C4_NotMember(t *testing.T) {
	env := newM4Env(t)
	cookieOwner, _ := env.seedUser(3100)
	_, peerID := env.seedUser(3101)
	cookieOuter, _ := env.seedUser(3102)
	channelID := env.seedGroup(cookieOwner, "p5-ann-outsider", peerID)

	errorBody(env.expect.POST("/api/announcements").
		WithHeader(middleware.MMCookieHeader, cookieOuter).
		WithJSON(map[string]any{
			"channel_id": channelID,
			"title":      "outsider",
			"content":    "ctn",
		}).
		Expect().Status(403)).
		Value("error").String().Contains("member")
}

// TestM4AnnouncementCreate_C5_MissingChannelID — channel_id 缺 → 422.
func TestM4AnnouncementCreate_C5_MissingChannelID(t *testing.T) {
	env := newM4Env(t)
	cookie, _ := env.seedUser(3103)
	errorBody(env.expect.POST("/api/announcements").
		WithHeader(middleware.MMCookieHeader, cookie).
		WithJSON(map[string]any{"title": "t", "content": "c"}).
		Expect().Status(422)).
		Value("error").String().Contains("channel_id")
}

// TestM4AnnouncementCreate_C5b_MissingTitle — title 空 → 422.
func TestM4AnnouncementCreate_C5b_MissingTitle(t *testing.T) {
	env := newM4Env(t)
	cookieOwner, _ := env.seedUser(3104)
	_, peerID := env.seedUser(3105)
	channelID := env.seedGroup(cookieOwner, "p5-ann-notitle", peerID)

	errorBody(env.expect.POST("/api/announcements").
		WithHeader(middleware.MMCookieHeader, cookieOwner).
		WithJSON(map[string]any{
			"channel_id": channelID,
			"content":    "c",
		}).
		Expect().Status(422)).
		Value("error").String().Contains("title")
}

// ---- POST /api/announcements/:id/read --------------------------------------

// TestM4AnnouncementRead_C2_CookieMissing — 401.
func TestM4AnnouncementRead_C2_CookieMissing(t *testing.T) {
	env := newM4Env(t)
	errorBody(env.expect.POST("/api/announcements/x/read").Expect().Status(401))
}

// TestM4AnnouncementRead_C3_CookieInvalid — 401.
func TestM4AnnouncementRead_C3_CookieInvalid(t *testing.T) {
	env := newM4Env(t)
	errorBody(env.expect.POST("/api/announcements/x/read").
		WithHeader(middleware.MMCookieHeader, "deadbeefdeadbeefdeadbeef").
		Expect().Status(401))
}

// TestM4AnnouncementRead_C4_NotMember — outsider 确认 → 403.
func TestM4AnnouncementRead_C4_NotMember(t *testing.T) {
	env := newM4Env(t)
	cookieOwner, _ := env.seedUser(3110)
	_, peerID := env.seedUser(3111)
	cookieOuter, _ := env.seedUser(3112)
	_, annID := seedAnnouncement(env, cookieOwner, "p5-ann-read-outsider")
	_ = peerID

	errorBody(env.expect.POST("/api/announcements/"+annID+"/read").
		WithHeader(middleware.MMCookieHeader, cookieOuter).
		Expect().Status(403)).
		Value("error").String().Contains("member")
}

// TestM4AnnouncementRead_C5_NotFound — 404.
func TestM4AnnouncementRead_C5_NotFound(t *testing.T) {
	env := newM4Env(t)
	cookie, _ := env.seedUser(3113)
	errorBody(env.expect.POST("/api/announcements/ghost/read").
		WithHeader(middleware.MMCookieHeader, cookie).
		Expect().Status(404)).
		Value("error").String().Contains("not found")
}

// ---- GET /api/announcements/:id/acks ---------------------------------------

// TestM4AnnouncementAcks_C2_CookieMissing — 401.
func TestM4AnnouncementAcks_C2_CookieMissing(t *testing.T) {
	env := newM4Env(t)
	errorBody(env.expect.GET("/api/announcements/x/acks").Expect().Status(401))
}

// TestM4AnnouncementAcks_C3_CookieInvalid — 401.
func TestM4AnnouncementAcks_C3_CookieInvalid(t *testing.T) {
	env := newM4Env(t)
	errorBody(env.expect.GET("/api/announcements/x/acks").
		WithHeader(middleware.MMCookieHeader, "deadbeefdeadbeefdeadbeef").
		Expect().Status(401))
}

// TestM4AnnouncementAcks_C4_NotMember — outsider → 403.
func TestM4AnnouncementAcks_C4_NotMember(t *testing.T) {
	env := newM4Env(t)
	cookieOwner, _ := env.seedUser(3120)
	cookieOuter, _ := env.seedUser(3121)
	_, annID := seedAnnouncement(env, cookieOwner, "p5-acks-outsider")

	errorBody(env.expect.GET("/api/announcements/"+annID+"/acks").
		WithHeader(middleware.MMCookieHeader, cookieOuter).
		Expect().Status(403)).
		Value("error").String().Contains("member")
}

// TestM4AnnouncementAcks_C5_NotFound — 404.
func TestM4AnnouncementAcks_C5_NotFound(t *testing.T) {
	env := newM4Env(t)
	cookie, _ := env.seedUser(3122)
	errorBody(env.expect.GET("/api/announcements/ghost/acks").
		WithHeader(middleware.MMCookieHeader, cookie).
		Expect().Status(404)).
		Value("error").String().Contains("not found")
}

// ---- DELETE /api/announcements/:id -----------------------------------------

// TestM4AnnouncementDelete_C2_CookieMissing — 401.
func TestM4AnnouncementDelete_C2_CookieMissing(t *testing.T) {
	env := newM4Env(t)
	errorBody(env.expect.DELETE("/api/announcements/x").Expect().Status(401))
}

// TestM4AnnouncementDelete_C3_CookieInvalid — 401.
func TestM4AnnouncementDelete_C3_CookieInvalid(t *testing.T) {
	env := newM4Env(t)
	errorBody(env.expect.DELETE("/api/announcements/x").
		WithHeader(middleware.MMCookieHeader, "deadbeefdeadbeefdeadbeef").
		Expect().Status(401))
}

// TestM4AnnouncementDelete_C4_NotMember — outsider → 403.
func TestM4AnnouncementDelete_C4_NotMember(t *testing.T) {
	env := newM4Env(t)
	cookieOwner, _ := env.seedUser(3130)
	cookieOuter, _ := env.seedUser(3131)
	_, annID := seedAnnouncement(env, cookieOwner, "p5-del-outsider")

	errorBody(env.expect.DELETE("/api/announcements/"+annID).
		WithHeader(middleware.MMCookieHeader, cookieOuter).
		Expect().Status(403)).
		Value("error").String().Contains("member")
}

// TestM4AnnouncementDelete_C5_NotFound — 404.
func TestM4AnnouncementDelete_C5_NotFound(t *testing.T) {
	env := newM4Env(t)
	cookie, _ := env.seedUser(3132)
	errorBody(env.expect.DELETE("/api/announcements/ghost").
		WithHeader(middleware.MMCookieHeader, cookie).
		Expect().Status(404)).
		Value("error").String().Contains("not found")
}

// ---- GET /api/channels/:id/announcements -----------------------------------

// TestM4AnnouncementListByChannel_C2_CookieMissing — 401.
func TestM4AnnouncementListByChannel_C2_CookieMissing(t *testing.T) {
	env := newM4Env(t)
	errorBody(env.expect.GET("/api/channels/x/announcements").Expect().Status(401))
}

// TestM4AnnouncementListByChannel_C3_CookieInvalid — 401.
func TestM4AnnouncementListByChannel_C3_CookieInvalid(t *testing.T) {
	env := newM4Env(t)
	errorBody(env.expect.GET("/api/channels/x/announcements").
		WithHeader(middleware.MMCookieHeader, "deadbeefdeadbeefdeadbeef").
		Expect().Status(401))
}

// TestM4AnnouncementListByChannel_C4_NotMember — outsider → 403.
func TestM4AnnouncementListByChannel_C4_NotMember(t *testing.T) {
	env := newM4Env(t)
	cookieOwner, _ := env.seedUser(3140)
	_, peerID := env.seedUser(3141)
	cookieOuter, _ := env.seedUser(3142)
	channelID := env.seedGroup(cookieOwner, "p5-list-outsider", peerID)

	errorBody(env.expect.GET("/api/channels/"+channelID+"/announcements").
		WithHeader(middleware.MMCookieHeader, cookieOuter).
		Expect().Status(403)).
		Value("error").String().Contains("member")
}

// ---- GET /api/announcements/:id --------------------------------------------

// TestM4AnnouncementGet_C2_CookieMissing — 401.
func TestM4AnnouncementGet_C2_CookieMissing(t *testing.T) {
	env := newM4Env(t)
	errorBody(env.expect.GET("/api/announcements/x").Expect().Status(401))
}

// TestM4AnnouncementGet_C3_CookieInvalid — 401.
func TestM4AnnouncementGet_C3_CookieInvalid(t *testing.T) {
	env := newM4Env(t)
	errorBody(env.expect.GET("/api/announcements/x").
		WithHeader(middleware.MMCookieHeader, "deadbeefdeadbeefdeadbeef").
		Expect().Status(401))
}

// TestM4AnnouncementGet_C4_NotMember — outsider → 403.
func TestM4AnnouncementGet_C4_NotMember(t *testing.T) {
	env := newM4Env(t)
	cookieOwner, _ := env.seedUser(3150)
	cookieOuter, _ := env.seedUser(3151)
	_, annID := seedAnnouncement(env, cookieOwner, "p5-get-outsider")

	errorBody(env.expect.GET("/api/announcements/"+annID).
		WithHeader(middleware.MMCookieHeader, cookieOuter).
		Expect().Status(403)).
		Value("error").String().Contains("member")
}

// TestM4AnnouncementGet_C5_NotFound — 404.
func TestM4AnnouncementGet_C5_NotFound(t *testing.T) {
	env := newM4Env(t)
	cookie, _ := env.seedUser(3152)
	errorBody(env.expect.GET("/api/announcements/ghost").
		WithHeader(middleware.MMCookieHeader, cookie).
		Expect().Status(404)).
		Value("error").String().Contains("not found")
}

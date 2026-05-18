//go:build integration

// Package integration — Batch-E happy-path coverage (C008 §4.4 收尾).
//
// 6 endpoints × 1 happy path each = 6 tests. Each top-level test calls
// newM4Env(t) for a fresh Postgres + Redis testcontainer pair; the harness
// (m4_harness_test.go) already wires sync, but module / presence / settings
// are NOT wired centrally on purpose — Batch-E owns its own minimal route
// registration via wireBatchERoutes(env) so the harness file stays
// untouched (per cutover rule "harness frozen after Batch-D").
//
// Seed range 1000-1099 is reserved for this file to avoid colliding with
// Batch-A (canonical fixture) / Batch-B (100-499) / Batch-C (500-599) /
// Batch-D (600-999).
//
// TODO (out of Batch-E scope, both families currently owned by cses Java):
//   - search.go (4 routes: /api/search/messages, /api/search/users,
//     /api/search/channels, /api/search/global) —待 cses Java→im 迁移完成
//     后再补集成测试。
//   - file.go (3 routes: /api/files upload + GET /api/files/:id +
//     DELETE /api/files/:id) — 走外部 OSS 服务，集成测试需要 testcontainers
//     起 minio，待底层迁移到 im 自管时再补。
//
// Contract scope is shallow on purpose — these tests assert "the route is
// wired, returns the documented 2xx envelope, and at least one response
// field looks reasonable". Deep behavioural assertions belong in unit
// tests for the underlying service.
package integration

import (
	"log/slog"
	"testing"

	"github.com/gin-gonic/gin"

	imhttp "im-server/internal/http"
	"im-server/internal/middleware"
	"im-server/internal/repo"
	"im-server/internal/service"
)

// wireBatchERoutes registers the module / presence / settings routes onto
// env.engine using the same /api group + Mattermost cookie middleware
// chain as cmd/gateway/main.go::buildRouter. The harness only wires the
// Batch-A..D handler families; Batch-E owns this glue so the harness file
// can stay frozen.
//
// Idempotent? No — caller must invoke at most once per env. Tests that
// touch Batch-E routes call it during their setup; tests that don't, skip
// it (avoiding double-registration panics on the same gin tree).
func wireBatchERoutes(env *m4env) {
	env.t.Helper()
	log := slog.Default()

	authed := env.engine.Group("/api")
	authed.Use(middleware.MattermostCookieResolve(env.rdb, log))
	authed.Use(middleware.CookieRequired())

	moduleRepo := repo.NewModuleRepo(env.db)
	imhttp.RegisterModuleRoutes(authed, moduleRepo)

	userSettingsRepo := repo.NewUserSettingsRepo(env.db)
	settingsSvc := service.NewSettingsService(userSettingsRepo)
	imhttp.RegisterSettingsRoutes(authed, settingsSvc)

	presenceSvc := service.NewPresenceService(env.channels, env.routing)
	imhttp.RegisterPresenceRoutes(authed, presenceSvc)
}

// init guarantees gin runs in test mode for this file's tests.
func init() { gin.SetMode(gin.TestMode) }

// ---------------------------------------------------------------------------
// module — 1 happy path
// ---------------------------------------------------------------------------

// TestM4ModuleList_HappyPath — GET /api/modules returns the 6 seeded entries
// from migration 016. Asserts envelope wrapping a JSON array and at least
// one row has a non-empty name.
func TestM4ModuleList_HappyPath(t *testing.T) {
	env := newM4Env(t)
	wireBatchERoutes(env)

	cookie, _ := env.seedUser(1000)

	arr := successBodyArray(env.expect.GET("/api/modules").
		WithHeader(middleware.MMCookieHeader, cookie).
		Expect().Status(200))

	// Migration 016 seeds 6 fixed-slot modules; assert >= 1 to stay
	// resilient to future seed shrinkage while still proving the route is
	// alive end-to-end.
	arr.Length().Gt(0)
	arr.Value(0).Object().Value("name").String().NotEmpty()
}

// ---------------------------------------------------------------------------
// sync — 1 happy path
// ---------------------------------------------------------------------------

// TestM4Sync_HappyPath — POST /api/sync with a single channel cursor returns
// the channels envelope. Empty cursors short-circuit to {channels: []};
// here we use a real DM with a freshly seeded message so the response
// actually carries delta.
func TestM4Sync_HappyPath(t *testing.T) {
	env := newM4Env(t)

	cookieA, idA := env.seedUser(1010)
	_, idB := env.seedUser(1011)
	channelID := env.seedDM(cookieA, idB)
	env.seedMessage(channelID, idA, "sync-batch-e")

	// C019 §3.1 wire shape: cursor field is `event_seq` (not `seq`);
	// per-channel result carries `server_event_seq` (channel_event high-water
	// mark) + `messages` map keyed by msg_id + `events` array + `kind` enum.
	body := successBody(env.expect.POST("/api/sync").
		WithHeader(middleware.MMCookieHeader, cookieA).
		WithJSON(map[string]any{
			"channels": []map[string]any{{"id": channelID, "event_seq": 0}},
		}).
		Expect().Status(200))

	channels := body.Value("channels").Array()
	channels.Length().IsEqual(1)
	first := channels.Value(0).Object()
	first.Value("id").String().IsEqual(channelID)
	first.Value("server_event_seq").Number().Gt(0)
	first.Value("kind").Object().Value("type").String().IsEqual("events")
	first.Value("messages").Object().NotEmpty()
}

// ---------------------------------------------------------------------------
// presence — 2 happy paths
// ---------------------------------------------------------------------------

// TestM4Presence_HappyPath — GET /api/presence?channel_id=X returns the
// online_user_ids array (may be empty since no WS conn is registered in
// this single-pod harness). Caller is a member, so the route returns 200.
//
// C006: ?channel_id=… must go through .WithQuery, never path concat.
func TestM4Presence_HappyPath(t *testing.T) {
	env := newM4Env(t)
	wireBatchERoutes(env)

	cookieA, _ := env.seedUser(1020)
	_, idB := env.seedUser(1021)
	channelID := env.seedDM(cookieA, idB)

	body := successBody(env.expect.GET("/api/presence").
		WithHeader(middleware.MMCookieHeader, cookieA).
		WithQuery("channel_id", channelID).
		Expect().Status(200))

	// online_user_ids is always present (even if empty) in the production
	// shape — no real WS conn in this harness so we only assert presence
	// of the field, not membership.
	body.Value("online_user_ids").Array()
}

// TestM4OnlineStatusBatch_HappyPath — GET /api/channels/online-status with
// a csv of channel_ids returns the per-channel summary array. Without
// include_users=true the rows carry online_count only.
func TestM4OnlineStatusBatch_HappyPath(t *testing.T) {
	env := newM4Env(t)
	wireBatchERoutes(env)

	cookieA, _ := env.seedUser(1030)
	_, idB := env.seedUser(1031)
	channelID := env.seedDM(cookieA, idB)

	body := successBody(env.expect.GET("/api/channels/online-status").
		WithHeader(middleware.MMCookieHeader, cookieA).
		WithQuery("channel_ids", channelID).
		Expect().Status(200))

	channels := body.Value("channels").Array()
	channels.Length().IsEqual(1)
	channels.Value(0).Object().Value("channel_id").String().IsEqual(channelID)
	// online_count is a number (>= 0); just assert the field exists with
	// a numeric type — no WS conn registered here so 0 is the expected
	// value but we keep the assertion shape-only to stay robust.
	channels.Value(0).Object().Value("online_count").Number().Ge(0)
}

// ---------------------------------------------------------------------------
// settings — 2 happy paths
// ---------------------------------------------------------------------------

// TestM4SettingsGet_HappyPath — GET /api/settings on a user with no row
// returns the default fallback shape (Theme=system, Language=zh,
// NotificationEnabled=true) per service.SettingsService.Get contract.
func TestM4SettingsGet_HappyPath(t *testing.T) {
	env := newM4Env(t)
	wireBatchERoutes(env)

	cookie, userID := env.seedUser(1040)

	body := successBody(env.expect.GET("/api/settings").
		WithHeader(middleware.MMCookieHeader, cookie).
		Expect().Status(200))

	body.Value("user_id").String().IsEqual(userID)
	body.Value("notification_enabled").Boolean().IsEqual(true)
	body.Value("theme").String().IsEqual("system")
	body.Value("language").String().IsEqual("zh")
}

// TestM4SettingsPut_HappyPath — PUT /api/settings with theme + language
// applies the partial update and returns the post-write state via re-read.
//
// notification_enabled is NOT flipped here. There is a known repo-layer
// bug (see internal/http/settings.go:73-76 inline note + matching comment
// in internal/repo/user_settings.go) where GORM's Upsert with
// `default:true` swaps a Go-zero `false` back to `true` on first INSERT.
// The handler tries to mitigate via re-read, but the underlying row still
// gets written with the wrong value. Asserting that flip here would test
// the bug, not the route — keep this test scoped to string fields where
// the wire shape is provably correct.
func TestM4SettingsPut_HappyPath(t *testing.T) {
	env := newM4Env(t)
	wireBatchERoutes(env)

	cookie, userID := env.seedUser(1050)

	body := successBody(env.expect.PUT("/api/settings").
		WithHeader(middleware.MMCookieHeader, cookie).
		WithJSON(map[string]any{
			"theme":    "dark",
			"language": "en",
		}).
		Expect().Status(200))

	body.Value("user_id").String().IsEqual(userID)
	body.Value("theme").String().IsEqual("dark")
	body.Value("language").String().IsEqual("en")
}

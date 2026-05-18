//go:build integration

// Package integration — Phase P1（autonomous test-coverage-100）补全 GET /api/search
// 端点的 5-case 矩阵。
//
// 该端点之前 0 测试。route registration 通过 wireSearchRoutes 本地装配（与
// m4_batch_e_test.go 的 wireBatchERoutes 同模式），不动 m4_harness_test.go
// 基座文件（harness frozen 约束）。
//
// Seed 范围 2000-2049 — Worktree A test-coverage-100 专属，避免与 batch_e
// (1000-1099) / batch-A (10-99) 撞库。
package integration

import (
	"log/slog"
	"testing"

	imhttp "im-server/internal/http"
	"im-server/internal/middleware"
	"im-server/internal/repo"
	"im-server/internal/service"
)

// wireSearchRoutes 注册 GET /api/search 到 env.engine，沿用 cmd/gateway/main.go
// 的 auth chain（MattermostCookieResolve + CookieRequired）。Idempotent? 不是 ——
// 调用方保证 per-env 只挂一次。
func wireSearchRoutes(env *m4env) {
	env.t.Helper()
	log := slog.Default()

	authed := env.engine.Group("/api")
	authed.Use(middleware.MattermostCookieResolve(env.rdb, log))
	authed.Use(middleware.CookieRequired())

	searchRepo := repo.NewSearchRepo(env.db)
	searchSvc := service.NewSearchService(searchRepo)
	imhttp.RegisterSearchRoutes(authed, searchSvc, log)
}

// TestM4Search_C1_HappyPath — caller 在自己加入的 DM 里搜消息，q 匹配 content。
// 返回 200 envelope.data.messages.length >= 1。
func TestM4Search_C1_HappyPath(t *testing.T) {
	env := newM4Env(t)
	wireSearchRoutes(env)

	cookieA, idA := env.seedUser(2000)
	_, idB := env.seedUser(2001)
	channelID := env.seedDM(cookieA, idB)
	env.seedMessage(channelID, idA, "needle-haystack")

	body := successBody(env.expect.GET("/api/search").
		WithHeader(middleware.MMCookieHeader, cookieA).
		WithQuery("q", "needle").
		WithQuery("type", "messages").
		Expect().Status(200))

	msgs := body.Value("messages").Array()
	msgs.Length().Gt(0)
}

// TestM4Search_C2_CookieMissing — no cookieId header → 401 from CookieRequired.
func TestM4Search_C2_CookieMissing(t *testing.T) {
	env := newM4Env(t)
	wireSearchRoutes(env)

	errorBody(env.expect.GET("/api/search").
		WithQuery("q", "needle").
		Expect().Status(401))
}

// TestM4Search_C3_CookieInvalid — cookieId 指向不存在的 UserData → 401
// （MattermostCookieResolve 不会塞 UserIDKey → CookieRequired abort 401）.
func TestM4Search_C3_CookieInvalid(t *testing.T) {
	env := newM4Env(t)
	wireSearchRoutes(env)

	errorBody(env.expect.GET("/api/search").
		WithHeader(middleware.MMCookieHeader, "deadbeefdeadbeefdeadbeef").
		WithQuery("q", "needle").
		Expect().Status(401))
}

// TestM4Search_C5_MissingQ — q 参数缺失 → 400 "q is required"。
// （没有 C4 forbidden case：搜索不做 channel 级 ACL，只在 SearchMessages
// 内部按 callerID JOIN channel_members 过滤；非成员消息天然不返回，无需 403。）
func TestM4Search_C5_MissingQ(t *testing.T) {
	env := newM4Env(t)
	wireSearchRoutes(env)

	cookieA, _ := env.seedUser(2010)

	errorBody(env.expect.GET("/api/search").
		WithHeader(middleware.MMCookieHeader, cookieA).
		Expect().Status(400)).
		Value("error").String().Contains("q is required")
}

// TestM4Search_C1b_ChannelsType — q 命中频道名时 type=channels 返回 channels
// 数组。覆盖 search.go switch 分支的 channels 路径（不仅是 messages）。
func TestM4Search_C1b_ChannelsType(t *testing.T) {
	env := newM4Env(t)
	wireSearchRoutes(env)

	cookieOwner, _ := env.seedUser(2020)
	_, member := env.seedUser(2021)
	env.seedGroup(cookieOwner, "search-channel-needle", member)

	body := successBody(env.expect.GET("/api/search").
		WithHeader(middleware.MMCookieHeader, cookieOwner).
		WithQuery("q", "search-channel-needle").
		WithQuery("type", "channels").
		Expect().Status(200))

	body.Value("channels").Array().Length().Gt(0)
}

// TestM4Search_C1c_UsersTypeEmpty — type=users 在 M4 后已退役 —— 服务端
// 永远返回空数组（cses 拥有用户目录）。该 case 锁定 wire shape 不变。
func TestM4Search_C1c_UsersTypeEmpty(t *testing.T) {
	env := newM4Env(t)
	wireSearchRoutes(env)

	cookieA, _ := env.seedUser(2030)

	body := successBody(env.expect.GET("/api/search").
		WithHeader(middleware.MMCookieHeader, cookieA).
		WithQuery("q", "anything").
		WithQuery("type", "users").
		Expect().Status(200))

	body.Value("users").Array().Length().IsEqual(0)
}

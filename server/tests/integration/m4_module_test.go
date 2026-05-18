//go:build integration

// Package integration — Phase P1（autonomous test-coverage-100）补全
// GET /api/modules 的 C2/C3 case。
//
// C1 happy path 已在 m4_batch_e_test.go::TestM4ModuleList_HappyPath 内覆盖；
// 本文件仅补未覆盖的 cookie-missing / cookie-invalid 两路 401。
// 路由用 wireBatchERoutes（同一处装配）。
//
// Seed 范围 2300-2399。
package integration

import (
	"testing"

	"im-server/internal/middleware"
)

// TestM4Module_C2_CookieMissing — 没有 cookieId header → 401（CookieRequired
// abort）。
func TestM4Module_C2_CookieMissing(t *testing.T) {
	env := newM4Env(t)
	wireBatchERoutes(env)

	errorBody(env.expect.GET("/api/modules").
		Expect().Status(401))
}

// TestM4Module_C3_CookieInvalid — cookieId 指向不存在的 UserData → 401。
// 触发 MattermostCookieResolve 拿不到用户 → CookieRequired abort。
func TestM4Module_C3_CookieInvalid(t *testing.T) {
	env := newM4Env(t)
	wireBatchERoutes(env)

	errorBody(env.expect.GET("/api/modules").
		WithHeader(middleware.MMCookieHeader, "deadbeefdeadbeefdeadbeef").
		Expect().Status(401))
}

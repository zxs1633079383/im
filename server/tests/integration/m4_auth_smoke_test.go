//go:build integration

package integration

import (
	"testing"

	"im-server/internal/middleware"
	"im-server/internal/testutil"
)

// TestM4AuthSmoke — the canonical 张立超 cookie clears
// MattermostCookieResolve + CookieRequired and /me echoes the JSON shape
// the cses-client expects. This is the same fixture the manual e2e script
// (server/scripts/seed-mm-cookies.sh) seeds, so a green run here implies
// the local handler stack matches what pre-7 will see in production.
func TestM4AuthSmoke(t *testing.T) {
	env := newM4Env(t)
	cookie := env.seedRealUser()

	resp := env.expect.GET("/api/auth/me").
		WithHeader(middleware.MMCookieHeader, cookie).
		Expect().Status(200).
		JSON().Object()

	resp.Value("userId").IsEqual(testutil.RealUserID)
	resp.Value("companyId").IsEqual(testutil.RealCompanyID)
	resp.Value("name").IsEqual(testutil.RealUserName)
}

// TestM4AuthSmoke_Missing — no cookieId header → 401 from CookieRequired.
// Pinned because the pre-7 deploy gate flips on this exact contract: any
// API call without a valid cookie must be 401, not 200, and not 500.
func TestM4AuthSmoke_Missing(t *testing.T) {
	env := newM4Env(t)
	env.expect.GET("/api/auth/me").
		Expect().Status(401)
}

package gateway

import (
	"context"
	"encoding/json"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/redis/go-redis/v9"
	"github.com/stretchr/testify/require"

	"im-server/internal/auth"
	"im-server/internal/middleware"
)

// fakeWSRedis stubs Get for the v0.7.4 cookie path (UserData:<userId>
// STRING). JWT path doesn't touch Redis, so the same fake serves both
// branches with different inputs.
type fakeWSRedis struct {
	redis.UniversalClient
	get func(ctx context.Context, key string) (string, error)
}

func (f *fakeWSRedis) Get(ctx context.Context, key string) *redis.StringCmd {
	cmd := redis.NewStringCmd(ctx)
	v, err := f.get(ctx, key)
	if err != nil {
		cmd.SetErr(err)
	} else {
		cmd.SetVal(v)
	}
	return cmd
}

const wsTestSecret = "ws-auth-test-secret"

func newAuthHandler(t *testing.T, rdb redis.UniversalClient) *WsHandler {
	t.Helper()
	h := NewWsHandler(nil, nil, wsTestSecret, "gw-test", nil, nil)
	if rdb != nil {
		h.WithCookieAuth(rdb)
	}
	// authenticate uses h.log via ResolveCookieID; nil is fine — middleware
	// falls back to slog.Default.
	return h
}

// mustMMUserJSON builds the v0.7.4 nested wire shape so the lookup parses it
// the same way production does. Top-level id carries the canonical userId;
// userId field is intentionally empty per the new upstream writer.
func mustMMUserJSON(t *testing.T, userID string) string {
	t.Helper()
	payload := map[string]any{
		"id":       userID,
		"mobile":   "",
		"name":     "ws-test-" + userID[:6],
		"userName": "ws-test-" + userID[:6],
		"userId":   "",
	}
	b, err := json.Marshal(payload)
	require.NoError(t, err)
	return string(b)
}

// TestWsAuth_CookieHeader_Resolves — v0.7.4 canonical wire: CookieId Header
// equals the userId, resolved through Redis GET UserData:<userId>.
func TestWsAuth_CookieHeader_Resolves(t *testing.T) {
	t.Cleanup(middleware.ResetCookieCacheForTest)
	middleware.ResetCookieCacheForTest()
	// v0.7.4: cookieId header value == userId, no separate session token.
	const user = "ad6847ade6614b70055ea2a5"

	rdb := &fakeWSRedis{get: func(_ context.Context, k string) (string, error) {
		require.True(t, strings.HasPrefix(k, middleware.UserDataKeyPrefix),
			"must hit UserData:<userId> STRING key, got %q", k)
		require.Equal(t, middleware.UserDataKeyPrefix+user, k)
		return mustMMUserJSON(t, user), nil
	}}
	h := newAuthHandler(t, rdb)

	req := httptest.NewRequest("GET", "/ws", nil)
	req.Header.Set("CookieId", user)
	uid, err := h.authenticate(req)
	require.NoError(t, err)
	require.Equal(t, user, uid)
}

// TestWsAuth_LowercaseCookieHeader — browsers normalise header case;
// 'cookieid' must work the same as 'CookieId'.
func TestWsAuth_LowercaseCookieHeader(t *testing.T) {
	t.Cleanup(middleware.ResetCookieCacheForTest)
	middleware.ResetCookieCacheForTest()
	const user = "ad6847ade6614b70055ea2b7"
	rdb := &fakeWSRedis{get: func(_ context.Context, k string) (string, error) {
		return mustMMUserJSON(t, user), nil
	}}
	h := newAuthHandler(t, rdb)

	req := httptest.NewRequest("GET", "/ws", nil)
	req.Header.Set("cookieid", user)
	uid, err := h.authenticate(req)
	require.NoError(t, err)
	require.Equal(t, user, uid)
}

// TestWsAuth_CookieQuery_Resolves — browsers cannot set custom upgrade
// headers, so a query param is the documented fallback. Both
// ?cookieId= (camelCase, message-v3 wire shape) and ?cookie_id=
// (snake_case alternative) must work. v0.7.4: value is the userId.
func TestWsAuth_CookieQuery_Resolves(t *testing.T) {
	t.Cleanup(middleware.ResetCookieCacheForTest)
	const user = "ad6847ade6614b70055ea2c9"
	rdb := &fakeWSRedis{get: func(_ context.Context, k string) (string, error) {
		return mustMMUserJSON(t, user), nil
	}}
	h := newAuthHandler(t, rdb)

	for _, q := range []string{"cookieId", "cookie_id"} {
		middleware.ResetCookieCacheForTest()
		req := httptest.NewRequest("GET", "/ws?"+q+"="+user, nil)
		uid, err := h.authenticate(req)
		require.NoError(t, err, "param %q", q)
		require.Equal(t, user, uid, "param %q", q)
	}
}

// TestWsAuth_StaleCookie_Refused — when a cookieId is supplied but Redis
// has no entry, the handler must refuse rather than fall through to JWT.
// Otherwise a session-timeout would be silently masked by a JWT replay.
func TestWsAuth_StaleCookie_Refused(t *testing.T) {
	t.Cleanup(middleware.ResetCookieCacheForTest)
	middleware.ResetCookieCacheForTest()
	rdb := &fakeWSRedis{get: func(context.Context, string) (string, error) {
		return "", redis.Nil
	}}
	h := newAuthHandler(t, rdb)

	req := httptest.NewRequest("GET", "/ws", nil)
	req.Header.Set("CookieId", "deadbeefdeadbeefdeadbeef")
	_, err := h.authenticate(req)
	require.Error(t, err)
	require.Contains(t, err.Error(), "invalid cookieId")
}

// TestWsAuth_JWT_FallthroughKept — no cookie + valid token = legacy path.
func TestWsAuth_JWT_FallthroughKept(t *testing.T) {
	const user = "jwt6847ade6614b70055ea2d"
	tok, err := auth.GenerateToken(wsTestSecret, user, "jwt-user")
	require.NoError(t, err)
	h := newAuthHandler(t, nil)
	req := httptest.NewRequest("GET", "/ws?token="+tok, nil)
	uid, err := h.authenticate(req)
	require.NoError(t, err)
	require.Equal(t, user, uid)
}

// TestWsAuth_NoAuth_401 — no cookie + no token → 401-style error.
func TestWsAuth_NoAuth_401(t *testing.T) {
	h := newAuthHandler(t, nil)
	req := httptest.NewRequest("GET", "/ws", nil)
	_, err := h.authenticate(req)
	require.Error(t, err)
	require.Contains(t, err.Error(), "missing auth")
}

// TestWsAuth_RdbNil_FallsThroughToJWT — when WithCookieAuth was never
// called, even a CookieId header takes the JWT path (legacy behaviour).
func TestWsAuth_RdbNil_FallsThroughToJWT(t *testing.T) {
	const user = "jwt-fallback-id-fakeeeeee"
	tok, err := auth.GenerateToken(wsTestSecret, user, "jwt-fb")
	require.NoError(t, err)
	h := newAuthHandler(t, nil)
	req := httptest.NewRequest("GET", "/ws?token="+tok, nil)
	req.Header.Set("CookieId", "ignored-when-rdb-nil-aaaa")
	uid, err := h.authenticate(req)
	require.NoError(t, err)
	require.Equal(t, user, uid)
}

package middleware

import (
	"context"
	"encoding/json"
	"errors"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/redis/go-redis/v9"
	"github.com/stretchr/testify/require"
)

// fakeRedis lets us drive the middleware without spinning miniredis. It
// implements just enough of redis.UniversalClient for Get because that is
// the only call the v0.7.4 middleware makes.
type fakeRedis struct {
	redis.UniversalClient
	getFn func(ctx context.Context, key string) (string, error)
}

func (f *fakeRedis) Get(ctx context.Context, key string) *redis.StringCmd {
	cmd := redis.NewStringCmd(ctx)
	val, err := f.getFn(ctx, key)
	if err != nil {
		cmd.SetErr(err)
	} else {
		cmd.SetVal(val)
	}
	return cmd
}

// sampleUserID is a 24-char hex id matching what cses Java writes as both
// the cookieId header value AND the field at the root of the JSON payload.
const sampleUserID = "6847ade6614b70055ea2a4b6"

// sampleCompanyID is what cses-client sends as the `companyId` header on
// every request post-v0.7.4. The middleware stamps it onto the context for
// TeamIDFromCtx without reading it from Redis.
const sampleCompanyID = "6111fb0a202d425d221c53db"

// sampleUserJSON builds the v0.7.4 nested wire shape — id at top, organizes[]
// carries org metadata that im itself ignores but the wire shape preserves
// for parity with the cses Java writer.
func sampleUserJSON(t *testing.T) string {
	t.Helper()
	payload := map[string]any{
		"id":       sampleUserID,
		"mobile":   "13800000000",
		"name":     "Alice",
		"userName": "alice@example.com",
		"userId":   "",
		"organizes": []map[string]any{
			{
				"companyId":   sampleCompanyID,
				"companyName": "Acme",
				"orgId":       "org-1",
				"orgName":     "eng",
				"userId":      sampleUserID,
				"userName":    "alice@example.com",
			},
		},
	}
	b, err := json.Marshal(payload)
	require.NoError(t, err)
	return string(b)
}

// TestLookupMattermostUser_Hit covers the v0.7.4 happy path: Get hits
// UserData:<userId>, lookup parses the nested shape, drops organizes[],
// and stamps CookieID back onto the struct.
func TestLookupMattermostUser_Hit(t *testing.T) {
	want := sampleUserJSON(t)
	rdb := &fakeRedis{getFn: func(_ context.Context, key string) (string, error) {
		require.Equal(t, UserDataKeyPrefix+sampleUserID, key,
			"must hit UserData:<userId> STRING key (v0.7.4)")
		return want, nil
	}}
	got, err := lookupMattermostUser(context.Background(), rdb, sampleUserID)
	require.NoError(t, err)
	require.Equal(t, sampleUserID, got.CookieID)
	require.Equal(t, sampleUserID, got.ID)
	require.Equal(t, "alice@example.com", got.UserName)
	require.Equal(t, "Alice", got.Name)
	require.Equal(t, "13800000000", got.Mobile)
}

// TestLookupMattermostUser_RedisMiss returns errMMUserNotFound on a clean
// redis.Nil so the middleware can swallow it silently (no warn log spam).
func TestLookupMattermostUser_RedisMiss(t *testing.T) {
	rdb := &fakeRedis{getFn: func(context.Context, string) (string, error) {
		return "", redis.Nil
	}}
	_, err := lookupMattermostUser(context.Background(), rdb, sampleUserID)
	require.ErrorIs(t, err, errMMUserNotFound)
}

// TestLookupMattermostUser_TransportError surfaces non-redis.Nil errors
// (timeout, broken pipe) so the middleware logs them.
func TestLookupMattermostUser_TransportError(t *testing.T) {
	boom := errors.New("dial tcp: connection refused")
	rdb := &fakeRedis{getFn: func(context.Context, string) (string, error) {
		return "", boom
	}}
	_, err := lookupMattermostUser(context.Background(), rdb, sampleUserID)
	require.ErrorIs(t, err, boom)
}

// TestLookupMattermostUser_BadJSON treats malformed payloads as parse
// errors — the middleware logs but does not crash the request.
func TestLookupMattermostUser_BadJSON(t *testing.T) {
	rdb := &fakeRedis{getFn: func(context.Context, string) (string, error) {
		return `{not-json`, nil
	}}
	_, err := lookupMattermostUser(context.Background(), rdb, sampleUserID)
	require.Error(t, err)
	require.NotErrorIs(t, err, errMMUserNotFound)
}

// TestLookupMattermostUser_EmptyValue treats a present-but-empty entry as
// a clean miss, not a parse error. Matches the upstream behaviour where
// cses Java DELs the key on logout but a concurrent reader may race on a
// zero-byte tombstone.
func TestLookupMattermostUser_EmptyValue(t *testing.T) {
	rdb := &fakeRedis{getFn: func(context.Context, string) (string, error) {
		return "", nil
	}}
	_, err := lookupMattermostUser(context.Background(), rdb, sampleUserID)
	require.ErrorIs(t, err, errMMUserNotFound)
}

// TestResolvedUserID_PrefersExplicitField asserts the helper falls back to
// id when userId is empty (the new wire shape always writes id and leaves
// userId blank). When the upstream payload does set userId, that takes
// precedence — matches the source-compat contract preserved in v0.7.4.
func TestResolvedUserID_PrefersExplicitField(t *testing.T) {
	t.Run("nil_user_returns_empty", func(t *testing.T) {
		var u *MattermostUser
		require.Equal(t, "", u.ResolvedUserID())
	})
	t.Run("empty_userId_falls_back_to_id", func(t *testing.T) {
		u := &MattermostUser{ID: sampleUserID}
		require.Equal(t, sampleUserID, u.ResolvedUserID())
	})
	t.Run("explicit_userId_wins", func(t *testing.T) {
		u := &MattermostUser{ID: "different", UserID: sampleUserID}
		require.Equal(t, sampleUserID, u.ResolvedUserID())
	})
}

// TestMattermostCookieAuth_HitInjectsUser exercises the full Gin pipeline:
// header set + Redis hit + downstream handler reads the user via
// MMUserFromCtx.
func TestMattermostCookieAuth_HitInjectsUser(t *testing.T) {
	gin.SetMode(gin.TestMode)
	t.Cleanup(resetCookieCacheForTest)
	rdb := &fakeRedis{getFn: func(context.Context, string) (string, error) {
		return sampleUserJSON(t), nil
	}}

	r := gin.New()
	r.Use(MattermostCookieAuth(rdb, nil, nil))
	var seen *MattermostUser
	r.GET("/test", func(c *gin.Context) {
		seen = MMUserFromCtx(c)
		c.Status(204)
	})

	req := httptest.NewRequest("GET", "/test", nil)
	req.Header.Set(MMCookieHeader, sampleUserID)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	require.Equal(t, 204, w.Code)
	require.NotNil(t, seen, "MMUserFromCtx must return the parsed user")
	require.Equal(t, "alice@example.com", seen.UserName)
	require.Equal(t, sampleUserID, seen.CookieID)
}

// TestMattermostCookieAuth_NoHeaderIsNoOp ensures requests without the
// cookieId header pass through cleanly with MMUserFromCtx returning nil
// (this is the dominant path during the cutover when JWT-only callers
// arrive).
func TestMattermostCookieAuth_NoHeaderIsNoOp(t *testing.T) {
	gin.SetMode(gin.TestMode)
	rdb := &fakeRedis{getFn: func(context.Context, string) (string, error) {
		t.Fatalf("Get must not be called when header is absent")
		return "", nil
	}}

	r := gin.New()
	r.Use(MattermostCookieAuth(rdb, nil, nil))
	var seen *MattermostUser
	called := false
	r.GET("/test", func(c *gin.Context) {
		called = true
		seen = MMUserFromCtx(c)
		c.Status(204)
	})

	req := httptest.NewRequest("GET", "/test", nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	require.Equal(t, 204, w.Code)
	require.True(t, called, "next handler must run")
	require.Nil(t, seen, "no header → no user injected")
}

// TestMattermostCookieAuth_RedisMissDoesNotAbort confirms a Redis miss
// still lets the handler chain run with a nil MMUser (the JWT middleware
// downstream will reject the request if it has no JWT either).
func TestMattermostCookieAuth_RedisMissDoesNotAbort(t *testing.T) {
	gin.SetMode(gin.TestMode)
	t.Cleanup(resetCookieCacheForTest)
	resetCookieCacheForTest()
	rdb := &fakeRedis{getFn: func(context.Context, string) (string, error) {
		return "", redis.Nil
	}}

	r := gin.New()
	r.Use(MattermostCookieAuth(rdb, nil, nil))
	called := false
	r.GET("/test", func(c *gin.Context) {
		called = true
		require.Nil(t, MMUserFromCtx(c))
		c.Status(204)
	})

	req := httptest.NewRequest("GET", "/test", nil)
	req.Header.Set(MMCookieHeader, sampleUserID)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	require.Equal(t, 204, w.Code)
	require.True(t, called)
}

// TestMattermostCookieAuth_CompanyHeader stamps the v0.7.4 companyId header
// onto the context so TeamIDFromCtx returns it without re-reading the
// request. Combined with a Redis hit this is the dominant happy path.
func TestMattermostCookieAuth_CompanyHeader(t *testing.T) {
	gin.SetMode(gin.TestMode)
	t.Cleanup(resetCookieCacheForTest)
	rdb := &fakeRedis{getFn: func(context.Context, string) (string, error) {
		return sampleUserJSON(t), nil
	}}

	r := gin.New()
	r.Use(MattermostCookieAuth(rdb, nil, nil))
	var team string
	r.GET("/test", func(c *gin.Context) {
		team = TeamIDFromCtx(c)
		c.Status(204)
	})

	req := httptest.NewRequest("GET", "/test", nil)
	req.Header.Set(MMCookieHeader, sampleUserID)
	req.Header.Set(MMTeamHeader, sampleCompanyID)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	require.Equal(t, 204, w.Code)
	require.Equal(t, sampleCompanyID, team,
		"v0.7.4 TeamIDFromCtx must mirror the companyId header, not parse Redis")
}

// TestMattermostCookieAuth_CompanyHeaderEmpty covers the optional case:
// a request without the companyId header still passes, TeamIDFromCtx
// returns "" which handlers treat as "no team scope" (NULL on write).
func TestMattermostCookieAuth_CompanyHeaderEmpty(t *testing.T) {
	gin.SetMode(gin.TestMode)
	t.Cleanup(resetCookieCacheForTest)
	rdb := &fakeRedis{getFn: func(context.Context, string) (string, error) {
		return sampleUserJSON(t), nil
	}}

	r := gin.New()
	r.Use(MattermostCookieAuth(rdb, nil, nil))
	var team string
	r.GET("/test", func(c *gin.Context) {
		team = TeamIDFromCtx(c)
		c.Status(204)
	})

	req := httptest.NewRequest("GET", "/test", nil)
	req.Header.Set(MMCookieHeader, sampleUserID)
	// intentionally do NOT set companyId
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	require.Equal(t, 204, w.Code)
	require.Equal(t, "", team, "missing companyId header → empty team")
}

// TestMattermostCookieAuth_CompanyHeaderWithoutCookieStillStamps covers
// the edge case where a request carries companyId but no cookieId.
// The middleware short-circuits the Redis lookup but still copies the
// header onto the context — consistency over surprise.
func TestMattermostCookieAuth_CompanyHeaderWithoutCookieStillStamps(t *testing.T) {
	gin.SetMode(gin.TestMode)
	rdb := &fakeRedis{getFn: func(context.Context, string) (string, error) {
		t.Fatalf("Get must not be called when cookieId is absent")
		return "", nil
	}}

	r := gin.New()
	r.Use(MattermostCookieAuth(rdb, nil, nil))
	var team string
	r.GET("/test", func(c *gin.Context) {
		team = TeamIDFromCtx(c)
		c.Status(204)
	})

	req := httptest.NewRequest("GET", "/test", nil)
	req.Header.Set(MMTeamHeader, sampleCompanyID)
	// intentionally NO cookieId
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	require.Equal(t, 204, w.Code)
	require.Equal(t, sampleCompanyID, team,
		"companyId must propagate even without cookieId (CookieRequired guards downstream)")
}

// TestMattermostCookieAuth_LRUCacheHit confirms a second request with the
// same cookieId never reaches Redis — the LRU should serve it.
func TestMattermostCookieAuth_LRUCacheHit(t *testing.T) {
	gin.SetMode(gin.TestMode)
	t.Cleanup(resetCookieCacheForTest)
	resetCookieCacheForTest()
	calls := 0
	rdb := &fakeRedis{getFn: func(context.Context, string) (string, error) {
		calls++
		return sampleUserJSON(t), nil
	}}

	r := gin.New()
	r.Use(MattermostCookieAuth(rdb, nil, nil))
	r.GET("/test", func(c *gin.Context) {
		require.NotNil(t, MMUserFromCtx(c))
		c.Status(204)
	})

	for i := 0; i < 5; i++ {
		req := httptest.NewRequest("GET", "/test", nil)
		req.Header.Set(MMCookieHeader, sampleUserID)
		w := httptest.NewRecorder()
		r.ServeHTTP(w, req)
		require.Equal(t, 204, w.Code)
	}
	require.Equal(t, 1, calls, "5 requests should hit Redis exactly once thanks to LRU")
}

// TestResolveCookieID_ExternalEntry exercises the ws_handler.authenticate
// reentry path — same cache + lookup as the gin middleware.
func TestResolveCookieID_ExternalEntry(t *testing.T) {
	t.Cleanup(resetCookieCacheForTest)
	resetCookieCacheForTest()
	rdb := &fakeRedis{getFn: func(_ context.Context, key string) (string, error) {
		require.Equal(t, UserDataKeyPrefix+sampleUserID, key)
		return sampleUserJSON(t), nil
	}}
	got, err := ResolveCookieID(context.Background(), rdb, sampleUserID, nil)
	require.NoError(t, err)
	require.Equal(t, sampleUserID, got.ResolvedUserID())
}

// TestMMUserFromCtx_NilSafe — calling the helper on an unrelated context
// (no middleware ever ran) returns nil rather than panicking.
func TestMMUserFromCtx_NilSafe(t *testing.T) {
	gin.SetMode(gin.TestMode)
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	require.Nil(t, MMUserFromCtx(c))
}

// TestTeamIDFromCtx_NilSafe — empty context returns empty team (handlers
// treat as NULL).
func TestTeamIDFromCtx_NilSafe(t *testing.T) {
	gin.SetMode(gin.TestMode)
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	require.Equal(t, "", TeamIDFromCtx(c))
}

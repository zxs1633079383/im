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
// implements just enough of redis.UniversalClient for HGet because that is
// the only call the middleware makes.
type fakeRedis struct {
	redis.UniversalClient
	getFn func(ctx context.Context, key, field string) (string, error)
}

func (f *fakeRedis) HGet(ctx context.Context, key, field string) *redis.StringCmd {
	cmd := redis.NewStringCmd(ctx)
	val, err := f.getFn(ctx, key, field)
	if err != nil {
		cmd.SetErr(err)
	} else {
		cmd.SetVal(val)
	}
	return cmd
}

const sampleCookie = "6847ade6614b70055ea2a4b6"

func sampleUserJSON(t *testing.T) string {
	t.Helper()
	b, err := json.Marshal(MattermostUser{
		ID:        sampleCookie,
		UserID:    sampleCookie,
		UserName:  "alice@example.com",
		Name:      "Alice",
		Email:     "alice@example.com",
		Mobile:    "13800000000",
		CompanyID: "co-7",
		DeptID:    "dept-3",
		OrgID:     "org-1",
		Roles:     []string{"member", "admin"},
		IsTeacher: true,
	})
	require.NoError(t, err)
	return string(b)
}

// TestLookupMattermostUser_Hit covers the happy path: HGet returns the
// JSON, lookup parses it and stamps the CookieID back onto the struct.
func TestLookupMattermostUser_Hit(t *testing.T) {
	want := sampleUserJSON(t)
	rdb := &fakeRedis{getFn: func(_ context.Context, key, field string) (string, error) {
		require.Equal(t, MMUserHashKey, key, "must hit the User hash")
		require.Equal(t, `"`+sampleCookie+`"`, field, "field must be JSON-quoted cookieId")
		return want, nil
	}}
	got, err := lookupMattermostUser(context.Background(), rdb, sampleCookie)
	require.NoError(t, err)
	require.Equal(t, sampleCookie, got.CookieID)
	require.Equal(t, "alice@example.com", got.UserName)
	require.Equal(t, []string{"member", "admin"}, got.Roles)
	require.True(t, got.IsTeacher)
}

// TestLookupMattermostUser_RedisMiss returns errMMUserNotFound on a clean
// redis.Nil so the middleware can swallow it silently (no warn log spam).
func TestLookupMattermostUser_RedisMiss(t *testing.T) {
	rdb := &fakeRedis{getFn: func(context.Context, string, string) (string, error) {
		return "", redis.Nil
	}}
	_, err := lookupMattermostUser(context.Background(), rdb, sampleCookie)
	require.ErrorIs(t, err, errMMUserNotFound)
}

// TestLookupMattermostUser_TransportError surfaces non-redis.Nil errors
// (timeout, broken pipe) so the middleware logs them.
func TestLookupMattermostUser_TransportError(t *testing.T) {
	boom := errors.New("dial tcp: connection refused")
	rdb := &fakeRedis{getFn: func(context.Context, string, string) (string, error) {
		return "", boom
	}}
	_, err := lookupMattermostUser(context.Background(), rdb, sampleCookie)
	require.ErrorIs(t, err, boom)
}

// TestLookupMattermostUser_BadJSON treats malformed payloads as parse
// errors — the middleware logs but does not crash the request.
func TestLookupMattermostUser_BadJSON(t *testing.T) {
	rdb := &fakeRedis{getFn: func(context.Context, string, string) (string, error) {
		return `{not-json`, nil
	}}
	_, err := lookupMattermostUser(context.Background(), rdb, sampleCookie)
	require.Error(t, err)
	require.NotErrorIs(t, err, errMMUserNotFound)
}

// TestMattermostCookieAuth_HitInjectsUser exercises the full Gin pipeline:
// header set + Redis hit + downstream handler reads the user via
// MMUserFromCtx.
func TestMattermostCookieAuth_HitInjectsUser(t *testing.T) {
	gin.SetMode(gin.TestMode)
	rdb := &fakeRedis{getFn: func(context.Context, string, string) (string, error) {
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
	req.Header.Set(MMCookieHeader, sampleCookie)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	require.Equal(t, 204, w.Code)
	require.NotNil(t, seen, "MMUserFromCtx must return the parsed user")
	require.Equal(t, "alice@example.com", seen.UserName)
}

// TestMattermostCookieAuth_NoHeaderIsNoOp ensures requests without the
// cookieId header pass through cleanly with MMUserFromCtx returning nil
// (this is the dominant path during the cutover when JWT-only callers
// arrive).
func TestMattermostCookieAuth_NoHeaderIsNoOp(t *testing.T) {
	gin.SetMode(gin.TestMode)
	rdb := &fakeRedis{getFn: func(context.Context, string, string) (string, error) {
		t.Fatalf("HGet must not be called when header is absent")
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
	rdb := &fakeRedis{getFn: func(context.Context, string, string) (string, error) {
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
	req.Header.Set(MMCookieHeader, sampleCookie)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	require.Equal(t, 204, w.Code)
	require.True(t, called)
}

// TestMMUserFromCtx_NilSafe — calling the helper on an unrelated context
// (no middleware ever ran) returns nil rather than panicking.
func TestMMUserFromCtx_NilSafe(t *testing.T) {
	gin.SetMode(gin.TestMode)
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	require.Nil(t, MMUserFromCtx(c))
}

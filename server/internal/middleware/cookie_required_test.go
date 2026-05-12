package middleware

import (
	"context"
	"encoding/json"
	"net/http/httptest"
	"sync/atomic"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/redis/go-redis/v9"
	"github.com/stretchr/testify/require"
)

// realFixture is the verified login response payload from cses-pre 张立超.
// v0.7.4: cookieId header value equals userId now — they collapsed into one.
const (
	realUserID    = "676cc4ccfbbc501161d5cd65"
	realCookieID  = realUserID // v0.7.4: cookieId == userId
	realCompanyID = "6111fb0a202d425d221c53db"
	realOrgID     = "6311a17c50c75d009ed3864f"
)

// realUserJSON builds the v0.7.4 nested wire shape stored at UserData:<id>.
// Top-level holds identity fields only; organizes[] carries org metadata
// that im ignores at lookup time but the wire shape preserves for parity
// with the upstream cses-Java writer.
func realUserJSON(t *testing.T) string {
	t.Helper()
	payload := map[string]any{
		"id":       realUserID,
		"mobile":   "17692704771",
		"name":     "张立超",
		"userName": "张立超",
		"userId":   "",
		"organizes": []map[string]any{
			{
				"companyId":   realCompanyID,
				"companyName": "中企云链（北京）信息科技有限公司",
				"deptId":      "616cee6ef7a6ae6354cddd9b",
				"deptName":    "技术部",
				"orgId":       realOrgID,
				"orgName":     "后端开发",
				"orgType":     "Member",
				"userId":      realUserID,
				"userName":    "张立超",
			},
		},
	}
	b, err := json.Marshal(payload)
	require.NoError(t, err)
	return string(b)
}

// TestCookieRequired_PassesWhenResolverInjected covers the v0.7.4 happy path:
// MattermostCookieResolve writes UserIDKey, the companyId header is stamped
// onto TeamIDFromCtx separately, and CookieRequired passes the request.
func TestCookieRequired_PassesWhenResolverInjected(t *testing.T) {
	gin.SetMode(gin.TestMode)
	t.Cleanup(resetCookieCacheForTest)
	resetCookieCacheForTest()
	rdb := &fakeRedis{getFn: func(_ context.Context, key string) (string, error) {
		require.Equal(t, UserDataKeyPrefix+realUserID, key)
		return realUserJSON(t), nil
	}}

	r := gin.New()
	r.Use(MattermostCookieResolve(rdb, nil))
	r.Use(CookieRequired())
	var seenUID, seenTeam string
	var seenMM *MattermostUser
	r.GET("/protected", func(c *gin.Context) {
		if v, ok := c.Get(UserIDKey); ok {
			seenUID, _ = v.(string)
		}
		seenTeam = TeamIDFromCtx(c)
		seenMM = MMUserFromCtx(c)
		c.Status(204)
	})

	req := httptest.NewRequest("GET", "/protected", nil)
	req.Header.Set(MMCookieHeader, realCookieID)
	req.Header.Set(MMTeamHeader, realCompanyID)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	require.Equal(t, 204, w.Code)
	require.Equal(t, realUserID, seenUID)
	require.Equal(t, realCompanyID, seenTeam,
		"v0.7.4: teamID comes from the companyId header, not the Redis payload")
	require.NotNil(t, seenMM)
	require.Equal(t, "张立超", seenMM.UserName)
}

// TestCookieRequired_RejectsMissingCookie returns 401 when the resolver had
// nothing to inject. This is the hard auth gate.
func TestCookieRequired_RejectsMissingCookie(t *testing.T) {
	gin.SetMode(gin.TestMode)
	t.Cleanup(resetCookieCacheForTest)
	resetCookieCacheForTest()
	rdb := &fakeRedis{getFn: func(context.Context, string) (string, error) {
		t.Fatalf("Get must not run when no cookie header is present")
		return "", nil
	}}

	r := gin.New()
	r.Use(MattermostCookieResolve(rdb, nil))
	r.Use(CookieRequired())
	r.GET("/protected", func(c *gin.Context) { c.Status(204) })

	req := httptest.NewRequest("GET", "/protected", nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	require.Equal(t, 401, w.Code)
}

// TestCookieRequired_RejectsInvalidCookie covers the case where the header
// is present but the userId does not exist at UserData:<userId>.
func TestCookieRequired_RejectsInvalidCookie(t *testing.T) {
	gin.SetMode(gin.TestMode)
	t.Cleanup(resetCookieCacheForTest)
	resetCookieCacheForTest()
	rdb := &fakeRedis{getFn: func(context.Context, string) (string, error) {
		return "", redis.Nil
	}}

	r := gin.New()
	r.Use(MattermostCookieResolve(rdb, nil))
	r.Use(CookieRequired())
	r.GET("/protected", func(c *gin.Context) { c.Status(204) })

	req := httptest.NewRequest("GET", "/protected", nil)
	req.Header.Set(MMCookieHeader, realCookieID)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	require.Equal(t, 401, w.Code)
}

// TestMattermostCookieResolve_LRUHit confirms the second request with the
// same cookieId reuses the cache and skips Redis. Without this, every
// authenticated request would pay the 200ms GET cap and add unnecessary
// load to the shared cses Redis cluster.
func TestMattermostCookieResolve_LRUHit(t *testing.T) {
	gin.SetMode(gin.TestMode)
	t.Cleanup(resetCookieCacheForTest)
	resetCookieCacheForTest()
	var hits int32
	rdb := &fakeRedis{getFn: func(context.Context, string) (string, error) {
		atomic.AddInt32(&hits, 1)
		return realUserJSON(t), nil
	}}

	r := gin.New()
	r.Use(MattermostCookieResolve(rdb, nil))
	r.GET("/x", func(c *gin.Context) { c.Status(204) })

	for i := 0; i < 3; i++ {
		req := httptest.NewRequest("GET", "/x", nil)
		req.Header.Set(MMCookieHeader, realCookieID)
		w := httptest.NewRecorder()
		r.ServeHTTP(w, req)
		require.Equal(t, 204, w.Code)
	}
	require.EqualValues(t, 1, atomic.LoadInt32(&hits),
		"second/third request must be served from LRU")
}

// TestMattermostUser_ResolvedUserID — UserID first, ID fallback, nil safe.
// v0.7.4: upstream now writes the id to "id" and leaves "userId" empty,
// so the ID-fallback branch is the dominant path; explicit-UserID test
// preserves source-compat with the rare wire variant.
func TestMattermostUser_ResolvedUserID(t *testing.T) {
	require.Equal(t, realUserID, (&MattermostUser{UserID: realUserID, ID: "other"}).ResolvedUserID())
	require.Equal(t, realUserID, (&MattermostUser{ID: realUserID}).ResolvedUserID())
	require.Empty(t, (&MattermostUser{}).ResolvedUserID())
	require.Empty(t, (*MattermostUser)(nil).ResolvedUserID())
}

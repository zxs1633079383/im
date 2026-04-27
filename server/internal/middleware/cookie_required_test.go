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
// We treat it as the ground-truth shape for all M4 cookie auth tests.
const (
	realCookieID  = "69eec6dbe6876865ff98945a"
	realUserID    = "676cc4ccfbbc501161d5cd65"
	realCompanyID = "6111fb0a202d425d221c53db"
	realOrgID     = "6311a17c50c75d009ed3864f"
)

func realUserJSON(t *testing.T) string {
	t.Helper()
	b, err := json.Marshal(MattermostUser{
		ID:        realUserID,
		UserID:    realUserID,
		UserName:  "张立超",
		Name:      "张立超",
		CompanyID: realCompanyID,
		OrgID:     realOrgID,
		OrgName:   "后端开发",
		OrgRole:   "Member",
		DeptID:    "616cee6ef7a6ae6354cddd9b",
		DeptName:  "技术部",
		Mobile:    "17692704771",
		Roles:     []string{"Member"},
	})
	require.NoError(t, err)
	return string(b)
}

// TestCookieRequired_PassesWhenResolverInjected covers the happy path:
// MattermostCookieResolve writes UserIDKey and TeamIDFromCtx, CookieRequired
// passes the request through.
func TestCookieRequired_PassesWhenResolverInjected(t *testing.T) {
	gin.SetMode(gin.TestMode)
	t.Cleanup(resetCookieCacheForTest)
	resetCookieCacheForTest()
	rdb := &fakeRedis{getFn: func(_ context.Context, key, field string) (string, error) {
		require.Equal(t, MMUserHashKey, key)
		require.Equal(t, `"`+realCookieID+`"`, field)
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
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	require.Equal(t, 204, w.Code)
	require.Equal(t, realUserID, seenUID)
	require.Equal(t, realCompanyID, seenTeam)
	require.NotNil(t, seenMM)
	require.Equal(t, "张立超", seenMM.UserName)
}

// TestCookieRequired_RejectsMissingCookie returns 401 when the resolver had
// nothing to inject. This is the M4 hard gate that replaces JWTOrCookie.
func TestCookieRequired_RejectsMissingCookie(t *testing.T) {
	gin.SetMode(gin.TestMode)
	t.Cleanup(resetCookieCacheForTest)
	resetCookieCacheForTest()
	rdb := &fakeRedis{getFn: func(context.Context, string, string) (string, error) {
		t.Fatalf("HGet must not run when no cookie header is present")
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
// is present but the cookieId does not exist in the upstream Redis HASH.
func TestCookieRequired_RejectsInvalidCookie(t *testing.T) {
	gin.SetMode(gin.TestMode)
	t.Cleanup(resetCookieCacheForTest)
	resetCookieCacheForTest()
	rdb := &fakeRedis{getFn: func(context.Context, string, string) (string, error) {
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
// authenticated request would pay the 200ms HGET cap and add unnecessary
// load to the shared cses Redis cluster.
func TestMattermostCookieResolve_LRUHit(t *testing.T) {
	gin.SetMode(gin.TestMode)
	t.Cleanup(resetCookieCacheForTest)
	resetCookieCacheForTest()
	var hits int32
	rdb := &fakeRedis{getFn: func(context.Context, string, string) (string, error) {
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
	require.EqualValues(t, 1, atomic.LoadInt32(&hits), "second/third request must be served from LRU")
}

// TestMattermostUser_ResolvedTeamID exercises the CompanyID-first / OrgID-fallback
// rule. NULL (no org) returns "".
func TestMattermostUser_ResolvedTeamID(t *testing.T) {
	cases := []struct {
		name      string
		companyID string
		orgID     string
		want      string
	}{
		{"company present", realCompanyID, realOrgID, realCompanyID},
		{"company empty falls back to org", "", realOrgID, realOrgID},
		{"both empty", "", "", ""},
	}
	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			u := &MattermostUser{CompanyID: tc.companyID, OrgID: tc.orgID}
			require.Equal(t, tc.want, u.ResolvedTeamID())
		})
	}
	require.Empty(t, (*MattermostUser)(nil).ResolvedTeamID())
}

// TestMattermostUser_ResolvedUserID — UserID first, ID fallback, nil safe.
func TestMattermostUser_ResolvedUserID(t *testing.T) {
	require.Equal(t, realUserID, (&MattermostUser{UserID: realUserID, ID: "other"}).ResolvedUserID())
	require.Equal(t, realUserID, (&MattermostUser{ID: realUserID}).ResolvedUserID())
	require.Empty(t, (&MattermostUser{}).ResolvedUserID())
	require.Empty(t, (*MattermostUser)(nil).ResolvedUserID())
}

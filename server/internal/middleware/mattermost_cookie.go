package middleware

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"sync"
	"time"

	lru "github.com/hashicorp/golang-lru/v2/expirable"
	"github.com/gin-gonic/gin"
	"github.com/redis/go-redis/v9"
)

// MMCookieHeader is the legacy Mattermost auth header that carries the user's
// session / cookie ID. Same name as upstream Mattermost (cses-server reads
// `r.Header.Get("cookieId")`), so cses-client builds keep working unmodified.
const MMCookieHeader = "cookieId"

// MMUserHashKey is the Redis HASH that stores Mattermost user profiles keyed
// by quoted cookieId. Matches the upstream Mattermost session_store wire shape
// (HGet User "\"<cookieId>\"") — cses Java backend writes the entries.
const MMUserHashKey = "User"

// mmLookupTimeout caps the per-request Redis HGET so a slow Mattermost-side
// cluster cannot block im handlers.
const mmLookupTimeout = 200 * time.Millisecond

// mmUserCtxKey is the gin.Context key the parsed *MattermostUser lands at.
const mmUserCtxKey = "im_mm_user"

// teamIDCtxKey carries the resolved team_id (mm CompanyID, falling back to
// OrgID, NULL for non-org users). Handlers should reach for it via TeamIDFromCtx.
const teamIDCtxKey = "im_team_id"

// cookieCacheCapacity caps the per-process LRU at 10k entries — covers a
// reasonable working set without blowing up memory under cookie-fuzz attacks.
const cookieCacheCapacity = 10_000

// cookieCacheTTL is short enough that an evicted upstream session expires
// before a follow-up request reuses a stale cache hit. 30s = same order as
// cses-server session expiry signals.
const cookieCacheTTL = 30 * time.Second

// cookieCache is the process-wide LRU keyed by raw cookieId. Constructed
// once via cookieCacheOnce so every middleware factory call shares state.
var (
	cookieCacheOnce sync.Once
	cookieCache     *lru.LRU[string, *MattermostUser]
)

// initCookieCache lazily constructs the singleton cache. Exposed for tests
// via resetCookieCacheForTest (test-only).
func initCookieCache() {
	cookieCacheOnce.Do(func() {
		cookieCache = lru.NewLRU[string, *MattermostUser](
			cookieCacheCapacity, nil, cookieCacheTTL)
	})
}

// MattermostUser mirrors the JSON shape stored at HASH "User" in the shared
// cses Redis cluster. The Java backend produces it, so field tags are
// camelCase. Add fields here as new handlers need them — encoding/json drops
// unknown keys silently. Reference fixture: cookieId 69eec6dbe6876865ff98945a
// resolves to userId=676cc4ccfbbc501161d5cd65 (张立超).
type MattermostUser struct {
	ID          string   `json:"id"`
	UserID      string   `json:"userId"`
	UserName    string   `json:"userName"`
	Name        string   `json:"name"`
	Email       string   `json:"email"`
	Mobile      string   `json:"mobile"`
	CompanyID   string   `json:"companyId"`
	DeptID      string   `json:"deptId"`
	DeptName    string   `json:"deptName"`
	OrgID       string   `json:"orgId"`
	OrgName     string   `json:"orgName"`
	OrgRole     string   `json:"orgRole"`
	OpenID      string   `json:"openId"`
	UnionID     string   `json:"unionId"`
	Roles       []string `json:"roles,omitempty"`
	Permissions []string `json:"permissions,omitempty"`
	Sex         int      `json:"sex"`
	State       int      `json:"state"`
	OrgState    int      `json:"orgState"`
	UpdateTime  int64    `json:"updateTime"`
	IsTeacher   bool     `json:"isTeacher"`
	InJinQi     bool     `json:"inJinQi"`
	Trace       bool     `json:"trace"`

	// CookieID is the raw header value the lookup ran against. Stamped
	// after a successful HGET so audit / tracing layers can correlate.
	CookieID string `json:"-"`
}

// ResolvedUserID returns the canonical id string for an mm user — UserID
// preferred, falls back to ID. Empty string means "no usable id"; handlers
// should treat that as 401.
func (u *MattermostUser) ResolvedUserID() string {
	if u == nil {
		return ""
	}
	if u.UserID != "" {
		return u.UserID
	}
	return u.ID
}

// ResolvedTeamID returns the canonical team id — CompanyID preferred,
// falls back to OrgID. Empty string is a valid value (non-org user) and
// is stored as NULL in PG.
func (u *MattermostUser) ResolvedTeamID() string {
	if u == nil {
		return ""
	}
	if u.CompanyID != "" {
		return u.CompanyID
	}
	return u.OrgID
}

// MattermostCookieResolve returns Gin middleware that resolves a Mattermost
// cookieId header into a *MattermostUser and injects it (plus user_id +
// team_id) onto the request context. It does NOT write to PG — M4 removed
// the lazy-upsert path; im no longer maintains a local users row.
//
// Behaviour:
//   - missing header → no-op (downstream CookieRequired returns 401)
//   - cache hit → reuse cached *MattermostUser, skip Redis
//   - Redis miss / parse error → no-op (downstream CookieRequired returns 401)
//   - hit → c.Set(mmUserCtxKey, *MattermostUser); UserIDKey = mm UserID;
//     teamIDCtxKey = CompanyID (or OrgID fallback)
//
// rdb may be nil (tests) — middleware then short-circuits to no-op.
func MattermostCookieResolve(rdb redis.UniversalClient, log *slog.Logger) gin.HandlerFunc {
	if log == nil {
		log = slog.Default()
	}
	initCookieCache()
	return func(c *gin.Context) {
		cookieID := c.GetHeader(MMCookieHeader)
		if cookieID == "" || rdb == nil {
			c.Next()
			return
		}
		mmUser, err := resolveMMUser(c.Request.Context(), rdb, cookieID, log)
		switch {
		case err == nil:
			c.Set(mmUserCtxKey, mmUser)
			if uid := mmUser.ResolvedUserID(); uid != "" {
				c.Set(UserIDKey, uid)
				c.Set(UsernameKey, mmUser.UserName)
			}
			c.Set(teamIDCtxKey, mmUser.ResolvedTeamID())
		case errors.Is(err, redis.Nil), errors.Is(err, errMMUserNotFound):
			// Cold miss / invalid cookieId — downstream auth gate decides.
		default:
			log.Warn("mm cookie resolve: lookup failed",
				"error", err, "cookie_len", len(cookieID))
		}
		c.Next()
	}
}

// MattermostCookieAuth is preserved as a thin alias for the M3 wiring that
// still passes a UserRepo. The userRepo argument is ignored in M4. Once the
// caller (cmd/gateway/main.go) is migrated to MattermostCookieResolve this
// shim can be deleted.
//
// Deprecated: use MattermostCookieResolve.
func MattermostCookieAuth(rdb redis.UniversalClient, _ any, log *slog.Logger) gin.HandlerFunc {
	return MattermostCookieResolve(rdb, log)
}

// errMMUserNotFound flags a clean Redis miss as distinct from a transport /
// parse error.
var errMMUserNotFound = errors.New("mm user not in redis hash")

// resolveMMUser is the cache-aware lookup. Cache layer is process-local;
// Redis fall-through has the upstream timeout cap.
func resolveMMUser(ctx context.Context, rdb redis.UniversalClient, cookieID string, log *slog.Logger) (*MattermostUser, error) {
	if cached, ok := cookieCache.Get(cookieID); ok {
		return cached, nil
	}
	user, err := lookupMattermostUser(ctx, rdb, cookieID)
	if err != nil {
		return nil, err
	}
	cookieCache.Add(cookieID, user)
	return user, nil
}

// lookupMattermostUser is the pure Redis-side helper, isolated from gin so
// unit tests can drive it without an HTTP harness.
func lookupMattermostUser(ctx context.Context, rdb redis.UniversalClient, cookieID string) (*MattermostUser, error) {
	lookupCtx, cancel := context.WithTimeout(ctx, mmLookupTimeout)
	defer cancel()

	// Mattermost stores the field key as the JSON-quoted cookieId
	// (`"<id>"`), matching how the Java writer serialises it.
	field := fmt.Sprintf("%q", cookieID)
	raw, err := rdb.HGet(lookupCtx, MMUserHashKey, field).Bytes()
	if errors.Is(err, redis.Nil) {
		return nil, errMMUserNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("mm cookie hget: %w", err)
	}
	if len(raw) == 0 {
		return nil, errMMUserNotFound
	}
	var user MattermostUser
	if err := json.Unmarshal(raw, &user); err != nil {
		return nil, fmt.Errorf("mm cookie unmarshal: %w", err)
	}
	user.CookieID = cookieID
	return &user, nil
}

// MMUserFromCtx returns the parsed Mattermost user the cookie middleware
// attached, or nil when the lookup did not run / failed.
func MMUserFromCtx(c *gin.Context) *MattermostUser {
	v, ok := c.Get(mmUserCtxKey)
	if !ok {
		return nil
	}
	u, _ := v.(*MattermostUser)
	return u
}

// TeamIDFromCtx returns the resolved team_id (mm CompanyID with OrgID
// fallback) the cookie middleware put on the context. Empty string is a
// valid value meaning "no org" — handlers persist it as NULL.
func TeamIDFromCtx(c *gin.Context) string {
	v, ok := c.Get(teamIDCtxKey)
	if !ok {
		return ""
	}
	s, _ := v.(string)
	return s
}

// resetCookieCacheForTest empties the singleton between unit tests.
// Test-only — production has no caller.
func resetCookieCacheForTest() {
	initCookieCache()
	cookieCache.Purge()
}

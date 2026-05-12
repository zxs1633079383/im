package middleware

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"github.com/gin-gonic/gin"
	lru "github.com/hashicorp/golang-lru/v2/expirable"
	"github.com/redis/go-redis/v9"
)

// MMCookieHeader is the auth header carrying the user's id. v0.7.4 changed
// the semantics: the value is now the mm UserID itself (24-char lowercase
// hex), not a separate session token. Header name is preserved as `cookieId`
// so cses-client wire stays unchanged through the rename.
const MMCookieHeader = "cookieId"

// MMTeamHeader is the v0.7.4 header carrying the active company / team id.
// cses-client sends the current company on every request; im no longer
// derives team_id from the Redis user payload (the new UserData shape stores
// it nested under organizes[] which we explicitly ignore — see the v0.7.4
// design note in docs/CSES_CLIENT_内部对接契约.md §2).
const MMTeamHeader = "companyId"

// UserDataKeyPrefix is the Redis STRING key prefix that stores user profiles
// keyed by mm UserID. The full key is `UserData:<userId>` (e.g.
// `UserData:676cc4ccfbbc501161d5cd65`). cses Java backend writes the entries
// with SET; im uses GET. Replaces the old HASH "User" model in v0.7.4.
const UserDataKeyPrefix = "UserData:"

// mmLookupTimeout caps the per-request Redis GET so a slow upstream cluster
// cannot block im handlers.
const mmLookupTimeout = 200 * time.Millisecond

// mmUserCtxKey is the gin.Context key the parsed *MattermostUser lands at.
const mmUserCtxKey = "im_mm_user"

// teamIDCtxKey carries the resolved team_id read from the companyId header.
// Handlers reach for it via TeamIDFromCtx. v0.7.4: source switched from the
// Redis payload to the request header.
const teamIDCtxKey = "im_team_id"

// cookieCacheCapacity caps the per-process LRU at 10k entries — covers a
// reasonable working set without blowing up memory under userId-fuzz attacks.
const cookieCacheCapacity = 10_000

// cookieCacheTTL is short enough that an evicted upstream session expires
// before a follow-up request reuses a stale cache hit. 30s = same order as
// cses-server session expiry signals; cses Java's `DEL UserData:<id>` on
// logout takes effect within one TTL window.
const cookieCacheTTL = 30 * time.Second

// cookieCache is the process-wide LRU keyed by userId. Constructed once via
// cookieCacheOnce so every middleware factory call shares state.
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

// MattermostUser mirrors the JSON shape stored at STRING `UserData:<userId>`
// in the shared cses Redis cluster. v0.7.4: drastically slimmed down — we
// only keep 4 identity fields. company / org / dept all moved out:
//   - team_id (companyId) → read from the `companyId` request header via
//     TeamIDFromCtx; the Redis payload's `organizes[]` array is ignored.
//   - org / dept names    → cses-client renders them itself from its own
//     org-tree query; im never needed them.
//
// Reference fixture: userId 676cc4ccfbbc501161d5cd65 (张立超) — the cookieId
// header value equals this id, no separate session token any more.
type MattermostUser struct {
	ID       string `json:"id"`       // mm UserID; equals the cookieId header value
	Mobile   string `json:"mobile"`   // 手机号，给客户端审计 / 通知模板用
	Name     string `json:"name"`     // 中文姓名
	UserName string `json:"userName"` // login name (可能与 Name 相同)

	// UserID is kept for wire-shape parity with the new upstream payload
	// (it ships as "userId":"" — the upstream now writes the canonical id
	// to `id`). Resolved* helpers fall back to ID when this is empty.
	UserID string `json:"userId,omitempty"`

	// CookieID is the raw header value the lookup ran against. v0.7.4: equals
	// ID. Stamped after a successful GET so audit / tracing layers can correlate.
	CookieID string `json:"-"`
}

// ResolvedUserID returns the canonical id string for an mm user. v0.7.4:
// UserID may be empty in the new payload — we fall back to ID, which is the
// authoritative field now. Empty string means "no usable id"; handlers
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

// MattermostCookieResolve returns Gin middleware that resolves a cookieId
// header (= mm UserID) into a *MattermostUser via Redis GET UserData:<id>
// and injects it onto the request context. Also stamps the companyId header
// (if present) as teamIDCtxKey so handlers can call TeamIDFromCtx without
// re-reading headers.
//
// Behaviour:
//   - missing cookieId header → no-op (downstream CookieRequired returns 401)
//   - cache hit → reuse cached *MattermostUser, skip Redis
//   - Redis miss / parse error → no-op (downstream CookieRequired returns 401)
//   - hit → c.Set(mmUserCtxKey, *MattermostUser); UserIDKey = mm UserID;
//     teamIDCtxKey = companyId header value (empty allowed)
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
			stampTeamHeader(c)
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
		case errors.Is(err, redis.Nil), errors.Is(err, errMMUserNotFound):
			// Cold miss / invalid cookieId — downstream auth gate decides.
		default:
			log.Warn("mm cookie resolve: lookup failed",
				"error", err, "cookie_len", len(cookieID))
		}
		stampTeamHeader(c)
		c.Next()
	}
}

// stampTeamHeader copies the companyId request header onto the context.
// Empty value is allowed and stored as ""; handlers treat empty as "no team
// scope" (persists as SQL NULL on write paths). v0.7.4: this replaces the
// old "derive team_id from Redis payload" path.
func stampTeamHeader(c *gin.Context) {
	c.Set(teamIDCtxKey, c.GetHeader(MMTeamHeader))
}

// MattermostCookieAuth is preserved as a thin alias for the M3 wiring that
// still passes a UserRepo. The userRepo argument is ignored in M4+. Once the
// caller (cmd/gateway/main.go) is migrated to MattermostCookieResolve this
// shim can be deleted.
//
// Deprecated: use MattermostCookieResolve.
func MattermostCookieAuth(rdb redis.UniversalClient, _ any, log *slog.Logger) gin.HandlerFunc {
	return MattermostCookieResolve(rdb, log)
}

// errMMUserNotFound flags a clean Redis miss as distinct from a transport /
// parse error.
var errMMUserNotFound = errors.New("mm user not in redis UserData")

// ResolveCookieID is the public re-entry point for cookie-id resolution
// outside the gin middleware chain. Use it from non-HTTP code paths
// (WebSocket upgrade handlers, gRPC interceptors, scripts) that need the
// same cache + Redis fall-through + metric counters as the gin
// MattermostCookieResolve middleware. rdb may be nil; a nil rdb forces a
// miss path that still records metrics consistently.
func ResolveCookieID(ctx context.Context, rdb redis.UniversalClient, cookieID string, log *slog.Logger) (*MattermostUser, error) {
	if log == nil {
		log = slog.Default()
	}
	initCookieCache()
	return resolveMMUser(ctx, rdb, cookieID, log)
}

// resolveMMUser is the cache-aware lookup. Cache layer is process-local;
// Redis fall-through has the upstream timeout cap. Hit/miss counters feed
// the im.auth.cookie_cache.{hit,miss} OTel instruments so ops can verify
// the 30s × 10k LRU sizing under load (target hit_rate ≥ 90%).
func resolveMMUser(ctx context.Context, rdb redis.UniversalClient, cookieID string, log *slog.Logger) (*MattermostUser, error) {
	m := metrics()
	if cached, ok := cookieCache.Get(cookieID); ok {
		if m.CookieCacheHit != nil {
			m.CookieCacheHit.Add(ctx, 1)
		}
		return cached, nil
	}
	if m.CookieCacheMiss != nil {
		m.CookieCacheMiss.Add(ctx, 1)
	}
	user, err := lookupMattermostUser(ctx, rdb, cookieID)
	if err != nil {
		return nil, err
	}
	cookieCache.Add(cookieID, user)
	return user, nil
}

// lookupMattermostUser is the pure Redis-side helper, isolated from gin so
// unit tests can drive it without an HTTP harness. v0.7.4: STRING GET against
// `UserData:<userId>` (where userId == cookieID per the new wire model).
func lookupMattermostUser(ctx context.Context, rdb redis.UniversalClient, cookieID string) (*MattermostUser, error) {
	lookupCtx, cancel := context.WithTimeout(ctx, mmLookupTimeout)
	defer cancel()

	key := UserDataKeyPrefix + cookieID
	raw, err := rdb.Get(lookupCtx, key).Bytes()
	if errors.Is(err, redis.Nil) {
		return nil, errMMUserNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("mm cookie get: %w", err)
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

// TeamIDFromCtx returns the team_id (mm CompanyID) the cookie middleware
// stamped from the `companyId` request header. v0.7.4: source switched from
// Redis payload (ResolvedTeamID) to a request header. Empty string is a
// valid value meaning "no team scope" — handlers persist it as NULL.
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

// ResetCookieCacheForTest is the cross-package alias of resetCookieCacheForTest.
// Exported so sibling test packages (e.g. internal/gateway) can flush the
// LRU between cases without duplicating the singleton plumbing. Production
// code MUST NOT call it — there is no use case outside tests.
func ResetCookieCacheForTest() { resetCookieCacheForTest() }

package middleware

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/redis/go-redis/v9"
)

// MMCookieHeader is the legacy Mattermost auth header that carries the user's
// session / cookie ID. The same header name is consumed by the upstream
// Mattermost server (`r.Header.Get("cookieId")` in csesapi/channel.go) so
// existing cses-client builds keep working unmodified during the cutover.
const MMCookieHeader = "cookieId"

// MMUserHashKey is the Redis HASH that stores Mattermost user profiles keyed
// by quoted cookieId. Schema confirmed against the upstream Mattermost
// session_store (HGet User "\"<cookieId>\""), and the Java backend that
// actually writes the entries uses the same wire shape.
const MMUserHashKey = "User"

// mmLookupTimeout caps the per-request Redis HGET so a temporarily slow
// Mattermost-side cluster cannot block im handlers.
const mmLookupTimeout = 200 * time.Millisecond

// mmUserCtxKey is the gin.Context key the parsed *MattermostUser lands at.
// Callers MUST use MMUserFromCtx instead of reading the raw key — the key
// is unexported on purpose so handlers cannot accidentally store something
// else there.
const mmUserCtxKey = "im_mm_user"

// MattermostUser mirrors the JSON shape stored at HASH "User" in the
// shared cses Redis cluster. The producer is the Java backend so the field
// tags are camelCase. Add fields here as new handlers need them — leaving
// unknown JSON keys ignored is fine, encoding/json drops them.
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
	// after a successful HGET so audit / tracing layers can correlate
	// without pulling the header out of the request again.
	CookieID string `json:"-"`
}

// MattermostCookieAuth returns Gin middleware that resolves a Mattermost
// cookieId header into a *MattermostUser and stashes it on the request
// context. Designed to coexist with the JWT middleware:
//
//   - missing header → no-op, handler chain continues
//   - Redis lookup miss / parse error → no-op, error logged at WARN
//   - hit → c.Set(mmUserCtxKey, *MattermostUser); handler reads via
//     MMUserFromCtx
//
// Never aborts the request — the Mattermost cookie is supplemental data
// (audit / dual-stack identity), and handlers that depend on im-native JWT
// auth still get rejected by the JWT middleware downstream.
func MattermostCookieAuth(rdb redis.UniversalClient, log *slog.Logger) gin.HandlerFunc {
	if log == nil {
		log = slog.Default()
	}
	return func(c *gin.Context) {
		cookieID := c.GetHeader(MMCookieHeader)
		if cookieID == "" || rdb == nil {
			c.Next()
			return
		}
		user, err := lookupMattermostUser(c.Request.Context(), rdb, cookieID)
		switch {
		case err == nil:
			c.Set(mmUserCtxKey, user)
		case errors.Is(err, redis.Nil), errors.Is(err, errMMUserNotFound):
			// Cold miss — no log line, this is expected for unknown cookieIds.
		default:
			log.Warn("mm cookie auth: lookup failed",
				"error", err, "cookie_len", len(cookieID))
		}
		c.Next()
	}
}

// errMMUserNotFound flags a clean Redis miss as distinct from a transport /
// parse error — keeps the middleware's switch readable.
var errMMUserNotFound = errors.New("mm user not in redis hash")

// lookupMattermostUser is the pure Redis-side helper, isolated from gin so
// the unit tests can drive it without spinning a full HTTP harness.
func lookupMattermostUser(ctx context.Context, rdb redis.UniversalClient, cookieID string) (*MattermostUser, error) {
	lookupCtx, cancel := context.WithTimeout(ctx, mmLookupTimeout)
	defer cancel()

	// Mattermost stores the field key as the JSON-quoted cookieId
	// (`"<id>"`), matching how the Java writer serialises it. Use %q so any
	// inner quote is escaped consistently with the upstream code.
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
// attached to this request, or nil when no cookieId was present (or the
// lookup failed). Handlers should always check the nil case rather than
// dereferencing blindly.
func MMUserFromCtx(c *gin.Context) *MattermostUser {
	v, ok := c.Get(mmUserCtxKey)
	if !ok {
		return nil
	}
	u, _ := v.(*MattermostUser)
	return u
}

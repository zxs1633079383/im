package testutil

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/redis/go-redis/v9"

	"im-server/internal/middleware"
)

// RealCookieID / RealUserID are the verified cses-pre identity for the
// production test fixture (张立超). v0.7.4 wire change: cookieId == userId
// now (no separate session token), so both constants reference the same
// value. Reuse them in any test that does not need to invent a new identity
// — that way the same id works in unit tests, integration tests, and the
// seed-mm-cookies.sh manual e2e flow.
const (
	RealUserID    = "676cc4ccfbbc501161d5cd65"
	RealCookieID  = RealUserID
	RealCompanyID = "6111fb0a202d425d221c53db"
	RealOrgID     = "6311a17c50c75d009ed3864f"
	RealUserName  = "张立超"
)

// CookieFixture writes a Mattermost-shaped user payload into the upstream
// Redis STRING `UserData:<userId>` so a request bearing cookieId clears
// MattermostCookieResolve + CookieRequired. Returns the cookieId header
// value to send back on each request. The fixture is automatically removed
// on t.Cleanup.
//
// v0.7.4 contract change: companyId no longer comes from the Redis payload —
// callers must additionally set the `companyId` request header for
// TeamIDFromCtx to return non-empty. Use CookieFixtureWithTeam when you need
// both pieces back in one call.
//
// Example:
//
//	cookie := testutil.CookieFixture(t, rdb, testutil.RealCookieID,
//	    testutil.RealUserID, testutil.RealCompanyID)
//	expect.GET("/api/channels").
//	    WithHeader(middleware.MMCookieHeader, cookie).
//	    WithHeader(middleware.MMTeamHeader, testutil.RealCompanyID).
//	    Expect().Status(200)
//
// All three positional arguments must already be 24-char hex (see
// auth.ValidateUserID); the helper panics otherwise so a typo in a fixture
// fails loudly instead of silently producing an unauthorised request.
//
// v0.7.4: cookieID and userID MUST be equal — the new wire shape collapsed
// the two into one (cookieId header value is the userId). The signature
// keeps both args for source-compat with existing call sites; mismatched
// values trigger t.Fatal so accidental drift is loud.
func CookieFixture(t *testing.T, rdb redis.UniversalClient, cookieID, userID, companyID string) string {
	t.Helper()
	requireHex24(t, "cookieID", cookieID)
	requireHex24(t, "userID", userID)
	if companyID != "" {
		requireHex24(t, "companyID", companyID)
	}
	if cookieID != userID {
		t.Fatalf("v0.7.4 wire requires cookieID == userID; got cookieID=%q userID=%q", cookieID, userID)
	}

	user := buildUserPayload(userID, companyID)
	raw, err := json.Marshal(user)
	if err != nil {
		t.Fatalf("CookieFixture marshal: %v", err)
	}

	key := middleware.UserDataKeyPrefix + userID
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if err := rdb.Set(ctx, key, string(raw), 0).Err(); err != nil {
		t.Fatalf("CookieFixture SET: %v", err)
	}
	t.Cleanup(func() {
		clean, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		_ = rdb.Del(clean, key).Err()
	})
	return cookieID
}

// userPayload mirrors the v0.7.4 cses-Java UserData JSON envelope. organizes[]
// is filled so the wire shape stays faithful to production — im itself ignores
// the field at lookup time but cses-client / debug tooling reads it.
type userPayload struct {
	ID        string             `json:"id"`
	Mobile    string             `json:"mobile"`
	Name      string             `json:"name"`
	UserName  string             `json:"userName"`
	UserID    string             `json:"userId"` // intentionally empty in new wire shape
	Organizes []organizePayload  `json:"organizes,omitempty"`
}

type organizePayload struct {
	CompanyID   string `json:"companyId"`
	CompanyName string `json:"companyName,omitempty"`
	DeptID      string `json:"deptId,omitempty"`
	DeptName    string `json:"deptName,omitempty"`
	OrgID       string `json:"orgId,omitempty"`
	OrgName     string `json:"orgName,omitempty"`
	OrgType     string `json:"orgType,omitempty"`
	UserID      string `json:"userId"`
	UserName    string `json:"userName"`
}

// buildUserPayload returns the v0.7.4 nested JSON shape. Helper is split out
// so tests can hand-craft alternative shapes (e.g. organizes-missing) without
// repeating the struct literal.
func buildUserPayload(userID, companyID string) userPayload {
	displayName := fmt.Sprintf("test-%s", userID[:8])
	mobile := ""
	orgID := companyID
	orgName := ""
	deptID := ""
	deptName := ""
	if userID == RealUserID {
		displayName = RealUserName
		mobile = "17692704771"
		orgID = RealOrgID
		orgName = "后端开发"
		deptID = "616cee6ef7a6ae6354cddd9b"
		deptName = "技术部"
	}
	out := userPayload{
		ID:       userID,
		Mobile:   mobile,
		Name:     displayName,
		UserName: displayName,
		UserID:   "", // v0.7.4: upstream now writes the id to "id" only
	}
	if companyID != "" {
		out.Organizes = []organizePayload{{
			CompanyID:   companyID,
			CompanyName: "fixture-co",
			DeptID:      deptID,
			DeptName:    deptName,
			OrgID:       orgID,
			OrgName:     orgName,
			OrgType:     "Member",
			UserID:      userID,
			UserName:    displayName,
		}}
	}
	return out
}

// MakeCookieID returns a deterministic 24-char hex id derived from seed.
// v0.7.4: same id can be used for both cookieId header and userId argument.
// Useful when a test needs multiple distinct identities. seed >= 0.
func MakeCookieID(seed int) string {
	return HexUserID(seed)
}

// MakeUserID is preserved for source compatibility — returns the same
// deterministic 24-hex id as MakeCookieID since the two collapsed in v0.7.4.
func MakeUserID(seed int) string {
	return HexUserID(seed)
}

// HexUserID returns a 24-char lowercase-hex id derived from seed,
// guaranteed to satisfy auth.ValidateUserID.
func HexUserID(seed int) string {
	if seed < 0 {
		seed = -seed
	}
	return fmt.Sprintf("%024x", uint64(seed)+1)
}

func requireHex24(t *testing.T, name, s string) {
	t.Helper()
	if len(s) != 24 {
		t.Fatalf("%s must be 24 chars, got %d (%q)", name, len(s), s)
	}
	for i := 0; i < len(s); i++ {
		c := s[i]
		ok := (c >= '0' && c <= '9') || (c >= 'a' && c <= 'f')
		if !ok {
			t.Fatalf("%s contains non-hex byte %q at offset %d (%q)", name, c, i, s)
		}
	}
	// Defensive: catch the legacy 'c'-prefix cookieId convention from
	// pre-v0.7.4 fixtures. Those are no longer valid hex userIds.
	if strings.HasPrefix(s, "c") && name == "cookieID" {
		t.Fatalf("v0.7.4: cookieID must equal userID and must be pure hex (got legacy 'c'-prefixed %q)", s)
	}
}

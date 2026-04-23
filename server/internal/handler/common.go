package handler

import (
	"encoding/json"
	"errors"
	"net/http"
	"strconv"

	"im-server/internal/auth"
)

// ContextKey is the type for keys stored in request context.
type ContextKey string

// ClaimsKey is the context key under which the JWT middleware stores
// the parsed *auth.Claims for downstream handlers to consume.
const ClaimsKey ContextKey = "claims"

// ErrNotFound is the local sentinel exposed for tests that build stubs
// and need a stable not-found error. Aliased to repo.ErrNotFound semantics.
var ErrNotFound = errors.New("not found")

// writeJSON writes v as JSON with the given status code.
func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

// writeError writes a JSON error envelope {"error": msg} with the given status.
func writeError(w http.ResponseWriter, status int, msg string) {
	writeJSON(w, status, map[string]string{"error": msg})
}

// claimsFromCtx extracts the JWT claims set by JWTAuth middleware. Returns
// (nil, false) when no claims are present so callers can write a 401.
func claimsFromCtx(r *http.Request) (*auth.Claims, bool) {
	c, ok := r.Context().Value(ClaimsKey).(*auth.Claims)
	return c, ok && c != nil
}

// pathID extracts a named path segment as int64. For Go 1.22 pattern routes
// like /api/channels/{id}, use r.PathValue("id"). Promoted from the legacy
// channel.go before the Phase 7.3 cut-over removed that file — favorite,
// file, and message handlers still reference it.
func pathID(r *http.Request, key string) (int64, bool) {
	s := r.PathValue(key)
	if s == "" {
		return 0, false
	}
	id, err := strconv.ParseInt(s, 10, 64)
	return id, err == nil
}


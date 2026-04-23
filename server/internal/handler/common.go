package handler

import (
	"encoding/json"
	"errors"
	"net/http"
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

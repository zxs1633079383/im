package handler_test

import (
	"context"
	"net/http"

	"im-server/internal/auth"
	"im-server/internal/handler"
)

// withClaims injects JWT claims into the request context. Shared across the
// remaining legacy-handler tests (favorite + file) — previously lived in
// search_test.go, which was removed in the Phase 7.6 cut-over.
func withClaims(r *http.Request, userID int64, username string) *http.Request {
	c := &auth.Claims{}
	c.UserID = userID
	c.Username = username
	return r.WithContext(context.WithValue(r.Context(), handler.ClaimsKey, c))
}

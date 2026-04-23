package handler_test

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"

	"im-server/internal/auth"
	"im-server/internal/handler"
)

// requestWithClaims builds an *http.Request with the JWT claims pre-populated
// on the context — the same context shape JWTAuth installs in production. Used
// by every legacy handler test that needs an authenticated caller.
//
// Lived in friend_test.go before the Phase 7.2 cut-over removed that file.
func requestWithClaims(method, path string, userID int64, body any) *http.Request {
	var req *http.Request
	if body != nil {
		b, _ := json.Marshal(body)
		req = httptest.NewRequest(method, path, bytes.NewReader(b))
		req.Header.Set("Content-Type", "application/json")
	} else {
		req = httptest.NewRequest(method, path, nil)
	}
	claims := &auth.Claims{}
	claims.UserID = userID
	req = req.WithContext(context.WithValue(req.Context(), handler.ClaimsKey, claims))
	return req
}

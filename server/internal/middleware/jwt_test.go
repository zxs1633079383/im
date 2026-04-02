package middleware_test

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"im-server/internal/auth"
	"im-server/internal/handler"
	"im-server/internal/middleware"
)

const testSecret = "test-secret-32-bytes-long-enough!"

func okHandler(w http.ResponseWriter, r *http.Request) {
	claims := r.Context().Value(handler.ClaimsKey).(*auth.Claims)
	w.WriteHeader(http.StatusOK)
	w.Write([]byte(claims.Username))
}

func applyMiddleware(secret string) http.Handler {
	return middleware.JWTAuth(secret)(http.HandlerFunc(okHandler))
}

func TestJWTMiddleware_ValidToken(t *testing.T) {
	token, _ := auth.GenerateToken(testSecret, 7, "frank")
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.Header.Set("Authorization", "Bearer "+token)
	rec := httptest.NewRecorder()

	applyMiddleware(testSecret).ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Code)
	}
	if rec.Body.String() != "frank" {
		t.Errorf("expected body 'frank', got %q", rec.Body.String())
	}
}

func TestJWTMiddleware_MissingHeader(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	rec := httptest.NewRecorder()
	applyMiddleware(testSecret).ServeHTTP(rec, req)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401, got %d", rec.Code)
	}
}

func TestJWTMiddleware_InvalidToken(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.Header.Set("Authorization", "Bearer garbage.token.here")
	rec := httptest.NewRecorder()
	applyMiddleware(testSecret).ServeHTTP(rec, req)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401, got %d", rec.Code)
	}
}

func TestJWTMiddleware_WrongSecret(t *testing.T) {
	token, _ := auth.GenerateToken("other-secret-here-padding-longer", 1, "grace")
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.Header.Set("Authorization", "Bearer "+token)
	rec := httptest.NewRecorder()
	applyMiddleware(testSecret).ServeHTTP(rec, req)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401, got %d", rec.Code)
	}
}

func TestJWTMiddleware_MalformedBearerFormat(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.Header.Set("Authorization", "Token sometoken")
	rec := httptest.NewRecorder()
	applyMiddleware(testSecret).ServeHTTP(rec, req)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401, got %d", rec.Code)
	}
}

package handler_test

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"

	"im-server/internal/auth"
	"im-server/internal/handler"
	"im-server/internal/model"
)

// ---------- stubs ----------

type stubSearchMsgStore struct {
	results []model.MessageSearchResult
	err     error
}

func (s *stubSearchMsgStore) SearchMessages(_ context.Context, _ string, _ int64, _ int64, _ int) ([]model.MessageSearchResult, error) {
	return s.results, s.err
}

type stubSearchUserStore struct {
	results []model.User
	err     error
}

func (s *stubSearchUserStore) SearchUsers(_ context.Context, _ string, _ int64, _ int) ([]model.User, error) {
	return s.results, s.err
}

type stubSearchChannelStore struct {
	results []model.Channel
	err     error
}

func (s *stubSearchChannelStore) SearchChannels(_ context.Context, _ string, _ int64, _ int) ([]model.Channel, error) {
	return s.results, s.err
}

// ---------- helpers ----------

func searchTestLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(os.Stderr, nil))
}

// withClaims injects JWT claims into the request context.
func withClaims(r *http.Request, userID int64, username string) *http.Request {
	c := &auth.Claims{}
	c.UserID = userID
	c.Username = username
	return r.WithContext(context.WithValue(r.Context(), handler.ClaimsKey, c))
}

func newSearchHandler() *handler.SearchHandler {
	return handler.NewSearchHandler(
		&stubSearchMsgStore{results: []model.MessageSearchResult{}},
		&stubSearchUserStore{results: []model.User{}},
		&stubSearchChannelStore{results: []model.Channel{}},
		searchTestLogger(),
	)
}

// ---------- tests ----------

func TestSearch_RequiresAuth(t *testing.T) {
	h := newSearchHandler()
	req := httptest.NewRequest(http.MethodGet, "/api/search?q=hello", nil)
	w := httptest.NewRecorder()
	h.Search(w, req)
	if w.Code != http.StatusUnauthorized {
		t.Errorf("want 401, got %d", w.Code)
	}
}

func TestSearch_RequiresQ(t *testing.T) {
	h := newSearchHandler()
	req := httptest.NewRequest(http.MethodGet, "/api/search", nil)
	req = withClaims(req, 1, "alice")
	w := httptest.NewRecorder()
	h.Search(w, req)
	if w.Code != http.StatusBadRequest {
		t.Errorf("want 400, got %d", w.Code)
	}
}

func TestSearch_ReturnsAllTypes(t *testing.T) {
	h := newSearchHandler()
	req := httptest.NewRequest(http.MethodGet, "/api/search?q=hello", nil)
	req = withClaims(req, 1, "alice")
	w := httptest.NewRecorder()
	h.Search(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("want 200, got %d", w.Code)
	}

	var resp map[string]any
	if err := json.NewDecoder(w.Body).Decode(&resp); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	for _, key := range []string{"messages", "users", "channels"} {
		if _, ok := resp[key]; !ok {
			t.Errorf("missing key %q in response", key)
		}
	}
}

func TestSearch_TypeFilterMessages(t *testing.T) {
	h := newSearchHandler()
	req := httptest.NewRequest(http.MethodGet, "/api/search?q=hello&type=messages", nil)
	req = withClaims(req, 1, "alice")
	w := httptest.NewRecorder()
	h.Search(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("want 200, got %d", w.Code)
	}
	var resp map[string]any
	_ = json.NewDecoder(w.Body).Decode(&resp)
	if _, ok := resp["users"]; ok {
		t.Error("users should be absent when type=messages")
	}
	if _, ok := resp["channels"]; ok {
		t.Error("channels should be absent when type=messages")
	}
}

func TestSearch_TypeFilterUsers(t *testing.T) {
	h := newSearchHandler()
	req := httptest.NewRequest(http.MethodGet, "/api/search?q=alice&type=users", nil)
	req = withClaims(req, 1, "alice")
	w := httptest.NewRecorder()
	h.Search(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("want 200, got %d", w.Code)
	}
	var resp map[string]any
	_ = json.NewDecoder(w.Body).Decode(&resp)
	if _, ok := resp["messages"]; ok {
		t.Error("messages should be absent when type=users")
	}
	if _, ok := resp["channels"]; ok {
		t.Error("channels should be absent when type=users")
	}
}

func TestSearch_TypeFilterChannels(t *testing.T) {
	h := newSearchHandler()
	req := httptest.NewRequest(http.MethodGet, "/api/search?q=general&type=channels", nil)
	req = withClaims(req, 1, "alice")
	w := httptest.NewRecorder()
	h.Search(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("want 200, got %d", w.Code)
	}
	var resp map[string]any
	_ = json.NewDecoder(w.Body).Decode(&resp)
	if _, ok := resp["messages"]; ok {
		t.Error("messages should be absent when type=channels")
	}
	if _, ok := resp["users"]; ok {
		t.Error("users should be absent when type=channels")
	}
}

package handler_test

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"

	"im-server/internal/handler"
	"im-server/internal/repo"
)

// ---------- stubs ----------

type stubFavStore struct {
	items []repo.FavoriteWithMessage
}

func (s *stubFavStore) Add(_ context.Context, _, _ int64) error    { return nil }
func (s *stubFavStore) Remove(_ context.Context, _, _ int64) error { return nil }
func (s *stubFavStore) List(_ context.Context, _ int64) ([]repo.FavoriteWithMessage, error) {
	return s.items, nil
}

func favTestLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(os.Stderr, nil))
}

// ---------- tests ----------

func TestFavoriteAdd_RequiresAuth(t *testing.T) {
	h := handler.NewFavoriteHandler(&stubFavStore{}, favTestLogger())
	req := httptest.NewRequest(http.MethodPost, "/api/favorites/1", nil)
	req.SetPathValue("message_id", "1")
	w := httptest.NewRecorder()
	h.AddFavorite(w, req)
	if w.Code != http.StatusUnauthorized {
		t.Errorf("want 401, got %d", w.Code)
	}
}

func TestFavoriteAdd_Success(t *testing.T) {
	h := handler.NewFavoriteHandler(&stubFavStore{}, favTestLogger())
	req := httptest.NewRequest(http.MethodPost, "/api/favorites/42", nil)
	req.SetPathValue("message_id", "42")
	req = withClaims(req, 1, "alice")
	w := httptest.NewRecorder()
	h.AddFavorite(w, req)
	if w.Code != http.StatusCreated {
		t.Errorf("want 201, got %d", w.Code)
	}
}

func TestFavoriteRemove_Success(t *testing.T) {
	h := handler.NewFavoriteHandler(&stubFavStore{}, favTestLogger())
	req := httptest.NewRequest(http.MethodDelete, "/api/favorites/42", nil)
	req.SetPathValue("message_id", "42")
	req = withClaims(req, 1, "alice")
	w := httptest.NewRecorder()
	h.RemoveFavorite(w, req)
	if w.Code != http.StatusNoContent {
		t.Errorf("want 204, got %d", w.Code)
	}
}

func TestFavoriteList_ReturnsEmpty(t *testing.T) {
	h := handler.NewFavoriteHandler(&stubFavStore{}, favTestLogger())
	req := httptest.NewRequest(http.MethodGet, "/api/favorites", nil)
	req = withClaims(req, 1, "alice")
	w := httptest.NewRecorder()
	h.ListFavorites(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("want 200, got %d", w.Code)
	}
	var resp map[string]any
	_ = json.NewDecoder(w.Body).Decode(&resp)
	if _, ok := resp["favorites"]; !ok {
		t.Error("response missing 'favorites' key")
	}
}

package handler_test

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"im-server/internal/handler"
	"im-server/internal/model"
)

// ---------- stub ----------

type stubProfileStore struct {
	user *model.User
}

func (s *stubProfileStore) GetByID(_ context.Context, _ int64) (*model.User, error) {
	return s.user, nil
}

func (s *stubProfileStore) UpdateProfile(_ context.Context, _ int64, displayName, avatarURL string) (*model.User, error) {
	cp := *s.user
	if displayName != "" {
		cp.DisplayName = displayName
	}
	if avatarURL != "" {
		cp.AvatarURL = avatarURL
	}
	cp.UpdatedAt = time.Now()
	return &cp, nil
}

// ---------- tests ----------

func TestUpdateMe_RequiresAuth(t *testing.T) {
	h := handler.NewProfileHandler(&stubProfileStore{}, testLogger())
	req := httptest.NewRequest(http.MethodPut, "/api/users/me", bytes.NewBufferString(`{}`))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	h.UpdateMe(w, req)
	if w.Code != http.StatusUnauthorized {
		t.Errorf("want 401, got %d", w.Code)
	}
}

func TestUpdateMe_InvalidDisplayName(t *testing.T) {
	user := &model.User{ID: 1, Username: "alice", DisplayName: "Alice", Status: model.UserStatusActive}
	h := handler.NewProfileHandler(&stubProfileStore{user: user}, testLogger())

	// 65-character display name
	longName := ""
	for i := 0; i < 65; i++ {
		longName += "a"
	}
	body, _ := json.Marshal(map[string]string{"display_name": longName})
	req := httptest.NewRequest(http.MethodPut, "/api/users/me", bytes.NewBuffer(body))
	req.Header.Set("Content-Type", "application/json")
	req = withClaims(req, 1, "alice")
	w := httptest.NewRecorder()
	h.UpdateMe(w, req)
	if w.Code != http.StatusUnprocessableEntity {
		t.Errorf("want 422, got %d", w.Code)
	}
}

func TestUpdateMe_Success(t *testing.T) {
	user := &model.User{ID: 1, Username: "alice", DisplayName: "Alice", Status: model.UserStatusActive}
	h := handler.NewProfileHandler(&stubProfileStore{user: user}, testLogger())

	body, _ := json.Marshal(map[string]string{"display_name": "Alice Updated"})
	req := httptest.NewRequest(http.MethodPut, "/api/users/me", bytes.NewBuffer(body))
	req.Header.Set("Content-Type", "application/json")
	req = withClaims(req, 1, "alice")
	w := httptest.NewRecorder()
	h.UpdateMe(w, req)
	if w.Code != http.StatusOK {
		t.Errorf("want 200, got %d: %s", w.Code, w.Body.String())
	}

	var resp model.User
	if err := json.NewDecoder(w.Body).Decode(&resp); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if resp.DisplayName != "Alice Updated" {
		t.Errorf("want DisplayName 'Alice Updated', got %q", resp.DisplayName)
	}
}

func TestUpdateMe_InvalidAvatarURL(t *testing.T) {
	user := &model.User{ID: 1, Username: "alice", Status: model.UserStatusActive}
	h := handler.NewProfileHandler(&stubProfileStore{user: user}, testLogger())
	body, _ := json.Marshal(map[string]string{"avatar_url": "not-a-url"})
	req := httptest.NewRequest(http.MethodPut, "/api/users/me", bytes.NewBuffer(body))
	req.Header.Set("Content-Type", "application/json")
	req = withClaims(req, 1, "alice")
	w := httptest.NewRecorder()
	h.UpdateMe(w, req)
	if w.Code != http.StatusUnprocessableEntity {
		t.Errorf("want 422, got %d", w.Code)
	}
}

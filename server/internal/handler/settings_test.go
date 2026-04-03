package handler_test

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"im-server/internal/handler"
	"im-server/internal/model"
)

// ---------- stub ----------

type stubSettingsStore struct {
	settings *model.UserSettings
}

func newStubSettingsStore() *stubSettingsStore {
	return &stubSettingsStore{
		settings: &model.UserSettings{
			UserID:              1,
			NotificationEnabled: true,
			Theme:               "system",
			Language:            "en",
		},
	}
}

func (s *stubSettingsStore) GetSettings(_ context.Context, _ int64) (*model.UserSettings, error) {
	return s.settings, nil
}

func (s *stubSettingsStore) UpsertSettings(_ context.Context, settings *model.UserSettings) error {
	*s.settings = *settings
	return nil
}

// ---------- tests ----------

func TestGetSettings_RequiresAuth(t *testing.T) {
	h := handler.NewSettingsHandler(newStubSettingsStore(), testLogger())
	req := httptest.NewRequest(http.MethodGet, "/api/settings", nil)
	w := httptest.NewRecorder()
	h.GetSettings(w, req)
	if w.Code != http.StatusUnauthorized {
		t.Errorf("want 401, got %d", w.Code)
	}
}

func TestGetSettings_ReturnsDefaults(t *testing.T) {
	h := handler.NewSettingsHandler(newStubSettingsStore(), testLogger())
	req := httptest.NewRequest(http.MethodGet, "/api/settings", nil)
	req = withClaims(req, 1, "alice")
	w := httptest.NewRecorder()
	h.GetSettings(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("want 200, got %d", w.Code)
	}
	var s model.UserSettings
	if err := json.NewDecoder(w.Body).Decode(&s); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if s.Theme != "system" {
		t.Errorf("want Theme 'system', got %q", s.Theme)
	}
}

func TestUpdateSettings_PartialUpdate(t *testing.T) {
	store := newStubSettingsStore()
	h := handler.NewSettingsHandler(store, testLogger())

	body, _ := json.Marshal(map[string]string{"theme": "dark"})
	req := httptest.NewRequest(http.MethodPut, "/api/settings", bytes.NewBuffer(body))
	req.Header.Set("Content-Type", "application/json")
	req = withClaims(req, 1, "alice")
	w := httptest.NewRecorder()
	h.UpdateSettings(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("want 200, got %d: %s", w.Code, w.Body.String())
	}

	var s model.UserSettings
	_ = json.NewDecoder(w.Body).Decode(&s)
	if s.Theme != "dark" {
		t.Errorf("want Theme 'dark', got %q", s.Theme)
	}
	// Language should still be the default
	if s.Language != "en" {
		t.Errorf("want Language 'en', got %q", s.Language)
	}
}

func TestUpdateSettings_NotificationToggle(t *testing.T) {
	store := newStubSettingsStore()
	h := handler.NewSettingsHandler(store, testLogger())

	notifFalse := false
	body, _ := json.Marshal(map[string]any{"notification_enabled": notifFalse})
	req := httptest.NewRequest(http.MethodPut, "/api/settings", bytes.NewBuffer(body))
	req.Header.Set("Content-Type", "application/json")
	req = withClaims(req, 1, "alice")
	w := httptest.NewRecorder()
	h.UpdateSettings(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("want 200, got %d", w.Code)
	}
	var s model.UserSettings
	_ = json.NewDecoder(w.Body).Decode(&s)
	if s.NotificationEnabled {
		t.Error("expected notification_enabled to be false")
	}
}

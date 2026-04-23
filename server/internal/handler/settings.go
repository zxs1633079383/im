package handler

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"

	"im-server/internal/repo"
)

// ---------- store interface ----------

// SettingsStore is the subset of repo.UserSettingsRepo used by SettingsHandler.
type SettingsStore interface {
	Get(ctx context.Context, userID int64) (*repo.UserSettings, error)
	Upsert(ctx context.Context, settings *repo.UserSettings) error
}

// ---------- handler ----------

// SettingsHandler serves the user settings endpoints.
type SettingsHandler struct {
	store SettingsStore
	log   *slog.Logger
}

func NewSettingsHandler(store SettingsStore, log *slog.Logger) *SettingsHandler {
	return &SettingsHandler{store: store, log: log}
}

// ---------- request type ----------

type updateSettingsBody struct {
	NotificationEnabled *bool  `json:"notification_enabled"`
	Theme               string `json:"theme"`
	Language            string `json:"language"`
}

// defaultSettings returns the default user_settings shape used when no row
// exists yet for a user. Mirrors the legacy store.UserStore.GetSettings
// fallback behavior so that GET /api/settings always returns 200 with
// usable defaults rather than 404.
func defaultSettings(userID int64) *repo.UserSettings {
	return &repo.UserSettings{
		UserID:              userID,
		NotificationEnabled: true,
		Theme:               "system",
		Language:            "zh",
		SettingsJSON:        "{}",
	}
}

// ---------- GET /api/settings ----------

// GetSettings handles GET /api/settings.
func (h *SettingsHandler) GetSettings(w http.ResponseWriter, r *http.Request) {
	claims, ok := claimsFromCtx(r)
	if !ok {
		writeError(w, http.StatusUnauthorized, "unauthorized")
		return
	}

	settings, err := h.store.Get(r.Context(), claims.UserID)
	if err != nil {
		if errors.Is(err, repo.ErrNotFound) {
			settings = defaultSettings(claims.UserID)
		} else {
			h.log.Error("get settings", "error", err)
			writeError(w, http.StatusInternalServerError, "internal error")
			return
		}
	}

	writeJSON(w, http.StatusOK, settings)
}

// ---------- PUT /api/settings ----------

// UpdateSettings handles PUT /api/settings.
// Partial updates are supported: only provided fields override the current values.
func (h *SettingsHandler) UpdateSettings(w http.ResponseWriter, r *http.Request) {
	claims, ok := claimsFromCtx(r)
	if !ok {
		writeError(w, http.StatusUnauthorized, "unauthorized")
		return
	}

	var body updateSettingsBody
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON")
		return
	}

	// Load existing settings first to support partial updates
	existing, err := h.store.Get(r.Context(), claims.UserID)
	if err != nil {
		if errors.Is(err, repo.ErrNotFound) {
			existing = defaultSettings(claims.UserID)
		} else {
			h.log.Error("get settings for update", "error", err)
			writeError(w, http.StatusInternalServerError, "internal error")
			return
		}
	}

	// Apply partial update
	if body.NotificationEnabled != nil {
		existing.NotificationEnabled = *body.NotificationEnabled
	}
	if body.Theme != "" {
		existing.Theme = body.Theme
	}
	if body.Language != "" {
		existing.Language = body.Language
	}

	if err := h.store.Upsert(r.Context(), existing); err != nil {
		h.log.Error("upsert settings", "error", err)
		writeError(w, http.StatusInternalServerError, "internal error")
		return
	}

	writeJSON(w, http.StatusOK, existing)
}

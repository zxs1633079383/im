package handler

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"

	"im-server/internal/model"
)

// ---------- store interface ----------

// SettingsStore is the subset of store.UserStore used by SettingsHandler.
type SettingsStore interface {
	GetSettings(ctx context.Context, userID int64) (*model.UserSettings, error)
	UpsertSettings(ctx context.Context, settings *model.UserSettings) error
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

// ---------- GET /api/settings ----------

// GetSettings handles GET /api/settings.
func (h *SettingsHandler) GetSettings(w http.ResponseWriter, r *http.Request) {
	claims, ok := claimsFromCtx(r)
	if !ok {
		writeError(w, http.StatusUnauthorized, "unauthorized")
		return
	}

	settings, err := h.store.GetSettings(r.Context(), claims.UserID)
	if err != nil {
		h.log.Error("get settings", "error", err)
		writeError(w, http.StatusInternalServerError, "internal error")
		return
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
	existing, err := h.store.GetSettings(r.Context(), claims.UserID)
	if err != nil {
		h.log.Error("get settings for update", "error", err)
		writeError(w, http.StatusInternalServerError, "internal error")
		return
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

	if err := h.store.UpsertSettings(r.Context(), existing); err != nil {
		h.log.Error("upsert settings", "error", err)
		writeError(w, http.StatusInternalServerError, "internal error")
		return
	}

	writeJSON(w, http.StatusOK, existing)
}

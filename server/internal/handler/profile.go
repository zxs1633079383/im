package handler

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"strings"

	"im-server/internal/model"
)

// ---------- store interface ----------

// ProfileStore is the subset of store.UserStore used by ProfileHandler.
type ProfileStore interface {
	GetByID(ctx context.Context, id int64) (*model.User, error)
	UpdateProfile(ctx context.Context, userID int64, displayName, avatarURL string) (*model.User, error)
}

// ---------- handler ----------

// ProfileHandler serves the profile update endpoint.
type ProfileHandler struct {
	users ProfileStore
	log   *slog.Logger
}

func NewProfileHandler(users ProfileStore, log *slog.Logger) *ProfileHandler {
	return &ProfileHandler{users: users, log: log}
}

// ---------- request type ----------

type updateProfileBody struct {
	DisplayName string `json:"display_name"`
	AvatarURL   string `json:"avatar_url"`
}

// ---------- PUT /api/users/me ----------

// UpdateMe handles PUT /api/users/me.
// Body (all fields optional):
//
//	{ "display_name": "...", "avatar_url": "..." }
func (h *ProfileHandler) UpdateMe(w http.ResponseWriter, r *http.Request) {
	claims, ok := claimsFromCtx(r)
	if !ok {
		writeError(w, http.StatusUnauthorized, "unauthorized")
		return
	}

	var body updateProfileBody
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON")
		return
	}

	// Validate display_name if provided
	if body.DisplayName != "" {
		body.DisplayName = strings.TrimSpace(body.DisplayName)
		if len(body.DisplayName) < 1 || len(body.DisplayName) > 64 {
			writeError(w, http.StatusUnprocessableEntity, "display_name must be 1-64 characters")
			return
		}
	}

	// Validate avatar_url if provided (basic check)
	if body.AvatarURL != "" && !strings.HasPrefix(body.AvatarURL, "http") {
		writeError(w, http.StatusUnprocessableEntity, "avatar_url must be a valid URL")
		return
	}

	updated, err := h.users.UpdateProfile(r.Context(), claims.UserID, body.DisplayName, body.AvatarURL)
	if err != nil {
		h.log.Error("update profile", "error", err)
		writeError(w, http.StatusInternalServerError, "internal error")
		return
	}

	writeJSON(w, http.StatusOK, updated)
}

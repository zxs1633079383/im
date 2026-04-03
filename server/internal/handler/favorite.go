package handler

import (
	"context"
	"log/slog"
	"net/http"

	"im-server/internal/store"
)

// ---------- store interface ----------

// FavStore is the subset of store.FavoriteStore used by FavoriteHandler.
type FavStore interface {
	Add(ctx context.Context, userID, messageID int64) error
	Remove(ctx context.Context, userID, messageID int64) error
	List(ctx context.Context, userID int64) ([]store.FavoriteWithMessage, error)
}

// ---------- handler ----------

// FavoriteHandler serves favorite add/remove/list endpoints.
type FavoriteHandler struct {
	favs FavStore
	log  *slog.Logger
}

func NewFavoriteHandler(favs FavStore, log *slog.Logger) *FavoriteHandler {
	return &FavoriteHandler{favs: favs, log: log}
}

// ---------- POST /api/favorites/{message_id} ----------

// AddFavorite handles POST /api/favorites/{message_id}.
func (h *FavoriteHandler) AddFavorite(w http.ResponseWriter, r *http.Request) {
	claims, ok := claimsFromCtx(r)
	if !ok {
		writeError(w, http.StatusUnauthorized, "unauthorized")
		return
	}

	messageID, ok := pathID(r, "message_id")
	if !ok {
		writeError(w, http.StatusBadRequest, "invalid message_id")
		return
	}

	if err := h.favs.Add(r.Context(), claims.UserID, messageID); err != nil {
		h.log.Error("add favorite", "error", err)
		writeError(w, http.StatusInternalServerError, "internal error")
		return
	}

	writeJSON(w, http.StatusCreated, map[string]string{"status": "ok"})
}

// ---------- DELETE /api/favorites/{message_id} ----------

// RemoveFavorite handles DELETE /api/favorites/{message_id}.
func (h *FavoriteHandler) RemoveFavorite(w http.ResponseWriter, r *http.Request) {
	claims, ok := claimsFromCtx(r)
	if !ok {
		writeError(w, http.StatusUnauthorized, "unauthorized")
		return
	}

	messageID, ok := pathID(r, "message_id")
	if !ok {
		writeError(w, http.StatusBadRequest, "invalid message_id")
		return
	}

	if err := h.favs.Remove(r.Context(), claims.UserID, messageID); err != nil {
		h.log.Error("remove favorite", "error", err)
		writeError(w, http.StatusInternalServerError, "internal error")
		return
	}

	w.WriteHeader(http.StatusNoContent)
}

// ---------- GET /api/favorites ----------

// ListFavorites handles GET /api/favorites.
func (h *FavoriteHandler) ListFavorites(w http.ResponseWriter, r *http.Request) {
	claims, ok := claimsFromCtx(r)
	if !ok {
		writeError(w, http.StatusUnauthorized, "unauthorized")
		return
	}

	favs, err := h.favs.List(r.Context(), claims.UserID)
	if err != nil {
		h.log.Error("list favorites", "error", err)
		writeError(w, http.StatusInternalServerError, "internal error")
		return
	}
	if favs == nil {
		favs = []store.FavoriteWithMessage{}
	}

	writeJSON(w, http.StatusOK, map[string]any{"favorites": favs})
}

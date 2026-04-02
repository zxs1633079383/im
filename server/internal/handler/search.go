package handler

import (
	"context"
	"log/slog"
	"net/http"

	"im-server/internal/model"
)

// ---------- store interfaces ----------

// SearchMsgStore is the subset of store.SearchStore used by SearchHandler.
type SearchMsgStore interface {
	SearchMessages(ctx context.Context, q string, userID int64, channelID int64, limit int) ([]model.MessageSearchResult, error)
}

// SearchUserStoreIface is a minimal interface for user search.
type SearchUserStoreIface interface {
	SearchUsers(ctx context.Context, q string, callerID int64, limit int) ([]model.User, error)
}

// SearchChannelStoreIface is the minimal interface for channel search.
type SearchChannelStoreIface interface {
	SearchChannels(ctx context.Context, q string, callerID int64, limit int) ([]model.Channel, error)
}

// ---------- handler ----------

// SearchHandler serves GET /api/search.
type SearchHandler struct {
	messages SearchMsgStore
	users    SearchUserStoreIface
	channels SearchChannelStoreIface
	log      *slog.Logger
}

func NewSearchHandler(
	messages SearchMsgStore,
	users SearchUserStoreIface,
	channels SearchChannelStoreIface,
	log *slog.Logger,
) *SearchHandler {
	return &SearchHandler{messages: messages, users: users, channels: channels, log: log}
}

// ---------- response types ----------

type searchResponse struct {
	Messages *[]model.MessageSearchResult `json:"messages,omitempty"`
	Users    *[]model.User                `json:"users,omitempty"`
	Channels *[]model.Channel             `json:"channels,omitempty"`
}

// ---------- GET /api/search ----------

// Search handles GET /api/search?q=xxx&type=messages|users|channels&channel_id=xxx
//
// Query params:
//   - q          (required) – search query, min 1 char
//   - type       (optional) – "messages", "users", or "channels"; omit to search all
//   - channel_id (optional) – restrict message search to a specific channel
//   - limit      (optional) – per-type result limit, default 20, max 50
func (h *SearchHandler) Search(w http.ResponseWriter, r *http.Request) {
	claims, ok := claimsFromCtx(r)
	if !ok {
		writeError(w, http.StatusUnauthorized, "unauthorized")
		return
	}

	q := r.URL.Query().Get("q")
	if len(q) == 0 {
		writeError(w, http.StatusBadRequest, "q is required")
		return
	}

	searchType := r.URL.Query().Get("type") // "", "messages", "users", "channels"
	channelID := parseIntParam(r.URL.Query().Get("channel_id"), 0)
	limit := int(parseIntParam(r.URL.Query().Get("limit"), 20))
	if limit > 50 {
		limit = 50
	}

	resp := searchResponse{}

	if searchType == "" || searchType == "messages" {
		msgs, err := h.messages.SearchMessages(r.Context(), q, claims.UserID, channelID, limit)
		if err != nil {
			h.log.Error("search messages", "error", err)
			writeError(w, http.StatusInternalServerError, "internal error")
			return
		}
		if msgs == nil {
			msgs = []model.MessageSearchResult{}
		}
		resp.Messages = &msgs
	}

	if searchType == "" || searchType == "users" {
		users, err := h.users.SearchUsers(r.Context(), q, claims.UserID, limit)
		if err != nil {
			h.log.Error("search users", "error", err)
			writeError(w, http.StatusInternalServerError, "internal error")
			return
		}
		if users == nil {
			users = []model.User{}
		}
		resp.Users = &users
	}

	if searchType == "" || searchType == "channels" {
		channels, err := h.channels.SearchChannels(r.Context(), q, claims.UserID, limit)
		if err != nil {
			h.log.Error("search channels", "error", err)
			writeError(w, http.StatusInternalServerError, "internal error")
			return
		}
		if channels == nil {
			channels = []model.Channel{}
		}
		resp.Channels = &channels
	}

	writeJSON(w, http.StatusOK, resp)
}

package handler

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"

	"im-server/internal/auth"
	"im-server/internal/model"
	"im-server/internal/store"
)

// ---------- interfaces ----------

// FriendStore is the subset of store.FriendshipStore used by FriendHandler.
type FriendStore interface {
	SendRequest(ctx context.Context, requesterID, addresseeID int64) error
	AcceptRequest(ctx context.Context, friendshipID, userID int64) error
	RejectRequest(ctx context.Context, friendshipID, userID int64) error
	ListFriends(ctx context.Context, userID int64) ([]model.User, error)
	ListPendingRequests(ctx context.Context, userID int64) ([]store.PendingRequest, error)
	BlockUser(ctx context.Context, blockerID, blockedID int64) error
}

// FriendUserStore is the subset of store.UserStore used by FriendHandler.
type FriendUserStore interface {
	GetByID(ctx context.Context, id int64) (*model.User, error)
	Search(ctx context.Context, q string, callerID int64) ([]model.User, error)
}

// ---------- handler ----------

// FriendHandler serves all friend-related HTTP endpoints.
type FriendHandler struct {
	friends FriendStore
	users   FriendUserStore
	log     *slog.Logger
}

func NewFriendHandler(friends FriendStore, users FriendUserStore, log *slog.Logger) *FriendHandler {
	return &FriendHandler{friends: friends, users: users, log: log}
}

// claimsFromCtx extracts the JWT claims set by JWTAuth middleware.
func claimsFromCtx(r *http.Request) (*auth.Claims, bool) {
	c, ok := r.Context().Value(ClaimsKey).(*auth.Claims)
	return c, ok && c != nil
}

// ---------- request types ----------

type sendRequestBody struct {
	AddresseeID int64 `json:"addressee_id"`
}

type friendshipIDBody struct {
	FriendshipID int64 `json:"friendship_id"`
}

type blockBody struct {
	UserID int64 `json:"user_id"`
}

// ---------- POST /api/friends/request ----------

// SendRequest handles POST /api/friends/request
func (h *FriendHandler) SendRequest(w http.ResponseWriter, r *http.Request) {
	claims, ok := claimsFromCtx(r)
	if !ok {
		writeError(w, http.StatusUnauthorized, "unauthorized")
		return
	}

	var body sendRequestBody
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON")
		return
	}
	if body.AddresseeID == 0 {
		writeError(w, http.StatusUnprocessableEntity, "addressee_id is required")
		return
	}

	err := h.friends.SendRequest(r.Context(), claims.UserID, body.AddresseeID)
	if err != nil {
		switch err {
		case store.ErrAlreadyExists:
			writeError(w, http.StatusConflict, "friend request already exists")
		default:
			h.log.Error("send request", "error", err)
			writeError(w, http.StatusInternalServerError, "internal error")
		}
		return
	}

	writeJSON(w, http.StatusCreated, map[string]string{"status": "pending"})
}

// ---------- POST /api/friends/accept ----------

// AcceptRequest handles POST /api/friends/accept
func (h *FriendHandler) AcceptRequest(w http.ResponseWriter, r *http.Request) {
	claims, ok := claimsFromCtx(r)
	if !ok {
		writeError(w, http.StatusUnauthorized, "unauthorized")
		return
	}

	var body friendshipIDBody
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON")
		return
	}
	if body.FriendshipID == 0 {
		writeError(w, http.StatusUnprocessableEntity, "friendship_id is required")
		return
	}

	err := h.friends.AcceptRequest(r.Context(), body.FriendshipID, claims.UserID)
	if err != nil {
		if err == store.ErrNotFound {
			writeError(w, http.StatusNotFound, "pending request not found")
			return
		}
		h.log.Error("accept request", "error", err)
		writeError(w, http.StatusInternalServerError, "internal error")
		return
	}

	writeJSON(w, http.StatusOK, map[string]string{"status": "accepted"})
}

// ---------- POST /api/friends/reject ----------

// RejectRequest handles POST /api/friends/reject
func (h *FriendHandler) RejectRequest(w http.ResponseWriter, r *http.Request) {
	claims, ok := claimsFromCtx(r)
	if !ok {
		writeError(w, http.StatusUnauthorized, "unauthorized")
		return
	}

	var body friendshipIDBody
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON")
		return
	}
	if body.FriendshipID == 0 {
		writeError(w, http.StatusUnprocessableEntity, "friendship_id is required")
		return
	}

	err := h.friends.RejectRequest(r.Context(), body.FriendshipID, claims.UserID)
	if err != nil {
		if err == store.ErrNotFound {
			writeError(w, http.StatusNotFound, "pending request not found")
			return
		}
		h.log.Error("reject request", "error", err)
		writeError(w, http.StatusInternalServerError, "internal error")
		return
	}

	writeJSON(w, http.StatusOK, map[string]string{"status": "rejected"})
}

// ---------- GET /api/friends ----------

// ListFriends handles GET /api/friends
func (h *FriendHandler) ListFriends(w http.ResponseWriter, r *http.Request) {
	claims, ok := claimsFromCtx(r)
	if !ok {
		writeError(w, http.StatusUnauthorized, "unauthorized")
		return
	}

	friends, err := h.friends.ListFriends(r.Context(), claims.UserID)
	if err != nil {
		h.log.Error("list friends", "error", err)
		writeError(w, http.StatusInternalServerError, "internal error")
		return
	}
	if friends == nil {
		friends = []model.User{}
	}
	writeJSON(w, http.StatusOK, friends)
}

// ---------- GET /api/friends/pending ----------

// ListPending handles GET /api/friends/pending
func (h *FriendHandler) ListPending(w http.ResponseWriter, r *http.Request) {
	claims, ok := claimsFromCtx(r)
	if !ok {
		writeError(w, http.StatusUnauthorized, "unauthorized")
		return
	}

	pending, err := h.friends.ListPendingRequests(r.Context(), claims.UserID)
	if err != nil {
		h.log.Error("list pending", "error", err)
		writeError(w, http.StatusInternalServerError, "internal error")
		return
	}
	if pending == nil {
		pending = []store.PendingRequest{}
	}
	writeJSON(w, http.StatusOK, pending)
}

// ---------- POST /api/friends/block ----------

// Block handles POST /api/friends/block
func (h *FriendHandler) Block(w http.ResponseWriter, r *http.Request) {
	claims, ok := claimsFromCtx(r)
	if !ok {
		writeError(w, http.StatusUnauthorized, "unauthorized")
		return
	}

	var body blockBody
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON")
		return
	}
	if body.UserID == 0 {
		writeError(w, http.StatusUnprocessableEntity, "user_id is required")
		return
	}

	err := h.friends.BlockUser(r.Context(), claims.UserID, body.UserID)
	if err != nil {
		h.log.Error("block user", "error", err)
		writeError(w, http.StatusInternalServerError, "internal error")
		return
	}

	writeJSON(w, http.StatusOK, map[string]string{"status": "blocked"})
}

// ---------- GET /api/users/search ----------

// SearchUsers handles GET /api/users/search?q=xxx
func (h *FriendHandler) SearchUsers(w http.ResponseWriter, r *http.Request) {
	claims, ok := claimsFromCtx(r)
	if !ok {
		writeError(w, http.StatusUnauthorized, "unauthorized")
		return
	}

	q := r.URL.Query().Get("q")

	users, err := h.users.Search(r.Context(), q, claims.UserID)
	if err != nil {
		h.log.Error("search users", "error", err)
		writeError(w, http.StatusInternalServerError, "internal error")
		return
	}
	if users == nil {
		users = []model.User{}
	}
	writeJSON(w, http.StatusOK, users)
}

package handler

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"strings"
	"unicode"

	"im-server/internal/auth"
	"im-server/internal/repo"
)

// UserStore is the subset of repo.UserRepo used by auth handlers.
type UserStore interface {
	Create(ctx context.Context, u *repo.User) error
	GetByUsername(ctx context.Context, username string) (*repo.User, error)
	GetByEmail(ctx context.Context, email string) (*repo.User, error)
	GetByID(ctx context.Context, id int64) (*repo.User, error)
}

// AuthHandler handles registration, login, and current-user requests.
type AuthHandler struct {
	store     UserStore
	jwtSecret string
	log       *slog.Logger
}

// NewAuthHandler creates a new AuthHandler.
func NewAuthHandler(store UserStore, jwtSecret string, log *slog.Logger) *AuthHandler {
	return &AuthHandler{store: store, jwtSecret: jwtSecret, log: log}
}

// ContextKey is the type for keys stored in request context.
type ContextKey string

const ClaimsKey ContextKey = "claims"

// ---------- request / response types ----------

type registerRequest struct {
	Username    string `json:"username"`
	Email       string `json:"email"`
	Password    string `json:"password"`
	DisplayName string `json:"display_name"`
}

type loginRequest struct {
	Login    string `json:"login"` // username or email
	Password string `json:"password"`
}

type authResponse struct {
	Token string     `json:"token"`
	User  *repo.User `json:"user"`
}

// ---------- helpers ----------

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func writeError(w http.ResponseWriter, status int, msg string) {
	writeJSON(w, status, map[string]string{"error": msg})
}

func validateRegisterInput(r *registerRequest) string {
	if len(r.Username) < 3 || len(r.Username) > 32 {
		return "username must be 3-32 characters"
	}
	for _, c := range r.Username {
		if !unicode.IsLetter(c) && !unicode.IsDigit(c) && c != '_' {
			return "username may only contain letters, digits, and underscores"
		}
	}
	if !strings.Contains(r.Email, "@") {
		return "invalid email"
	}
	if len(r.Password) < 8 {
		return "password must be at least 8 characters"
	}
	if len(r.DisplayName) == 0 {
		r.DisplayName = r.Username
	}
	return ""
}

// ---------- handlers ----------

// Register handles POST /api/auth/register
func (h *AuthHandler) Register(w http.ResponseWriter, r *http.Request) {
	var req registerRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON")
		return
	}

	if msg := validateRegisterInput(&req); msg != "" {
		writeError(w, http.StatusUnprocessableEntity, msg)
		return
	}

	hash, err := auth.HashPassword(req.Password)
	if err != nil {
		h.log.Error("hash password", "error", err)
		writeError(w, http.StatusInternalServerError, "internal error")
		return
	}

	user := &repo.User{
		Username:     req.Username,
		Email:        req.Email,
		PasswordHash: hash,
		DisplayName:  req.DisplayName,
	}

	if err := h.store.Create(r.Context(), user); err != nil {
		errMsg := err.Error()
		if strings.Contains(errMsg, "duplicate") || strings.Contains(errMsg, "unique") {
			writeError(w, http.StatusConflict, "username or email already taken")
			return
		}
		h.log.Error("create user", "error", err)
		writeError(w, http.StatusInternalServerError, "internal error")
		return
	}

	token, err := auth.GenerateToken(h.jwtSecret, user.ID, user.Username)
	if err != nil {
		h.log.Error("generate token", "error", err)
		writeError(w, http.StatusInternalServerError, "internal error")
		return
	}

	writeJSON(w, http.StatusCreated, authResponse{Token: token, User: user})
}

// Login handles POST /api/auth/login
func (h *AuthHandler) Login(w http.ResponseWriter, r *http.Request) {
	var req loginRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON")
		return
	}
	if req.Login == "" || req.Password == "" {
		writeError(w, http.StatusUnprocessableEntity, "login and password are required")
		return
	}

	var (
		user *repo.User
		err  error
	)
	if strings.Contains(req.Login, "@") {
		user, err = h.store.GetByEmail(r.Context(), req.Login)
	} else {
		user, err = h.store.GetByUsername(r.Context(), req.Login)
	}
	if err != nil {
		// Treat not-found and any DB error as invalid credentials to prevent enumeration
		writeError(w, http.StatusUnauthorized, "invalid credentials")
		return
	}

	if err := auth.CheckPassword(user.PasswordHash, req.Password); err != nil {
		writeError(w, http.StatusUnauthorized, "invalid credentials")
		return
	}

	if user.Status == repo.UserStatusDisabled {
		writeError(w, http.StatusForbidden, "account disabled")
		return
	}

	token, err := auth.GenerateToken(h.jwtSecret, user.ID, user.Username)
	if err != nil {
		h.log.Error("generate token", "error", err)
		writeError(w, http.StatusInternalServerError, "internal error")
		return
	}

	writeJSON(w, http.StatusOK, authResponse{Token: token, User: user})
}

// Me handles GET /api/auth/me — requires JWT middleware to have set claims in context.
func (h *AuthHandler) Me(w http.ResponseWriter, r *http.Request) {
	claims, ok := r.Context().Value(ClaimsKey).(*auth.Claims)
	if !ok || claims == nil {
		writeError(w, http.StatusUnauthorized, "unauthorized")
		return
	}

	user, err := h.store.GetByID(r.Context(), claims.UserID)
	if err != nil {
		writeError(w, http.StatusNotFound, "user not found")
		return
	}

	writeJSON(w, http.StatusOK, user)
}

// ---------- error sentinel ----------

// ErrNotFound is the local sentinel exposed for tests that build stubs
// and need a stable not-found error. Aliased to repo.ErrNotFound.
var ErrNotFound = errors.New("not found")

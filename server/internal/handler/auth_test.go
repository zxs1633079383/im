package handler_test

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"

	"im-server/internal/auth"
	"im-server/internal/handler"
	"im-server/internal/model"
)

// ---------- in-memory stub store ----------

type stubUserStore struct {
	users   map[int64]*model.User
	byName  map[string]*model.User
	byEmail map[string]*model.User
	nextID  int64
}

func newStubStore() *stubUserStore {
	return &stubUserStore{
		users:   make(map[int64]*model.User),
		byName:  make(map[string]*model.User),
		byEmail: make(map[string]*model.User),
		nextID:  1,
	}
}

func (s *stubUserStore) Create(_ context.Context, u *model.User) error {
	if _, exists := s.byName[u.Username]; exists {
		return fmt.Errorf("duplicate key: unique constraint")
	}
	u.ID = s.nextID
	s.nextID++
	u.Status = model.UserStatusActive
	s.users[u.ID] = u
	s.byName[u.Username] = u
	s.byEmail[u.Email] = u
	return nil
}

func (s *stubUserStore) GetByUsername(_ context.Context, username string) (*model.User, error) {
	u, ok := s.byName[username]
	if !ok {
		return nil, handler.ErrNotFound
	}
	return u, nil
}

func (s *stubUserStore) GetByEmail(_ context.Context, email string) (*model.User, error) {
	u, ok := s.byEmail[email]
	if !ok {
		return nil, handler.ErrNotFound
	}
	return u, nil
}

func (s *stubUserStore) GetByID(_ context.Context, id int64) (*model.User, error) {
	u, ok := s.users[id]
	if !ok {
		return nil, handler.ErrNotFound
	}
	return u, nil
}

// ---------- test helpers ----------

const testSecret = "test-secret-32-bytes-long-enough!"

func newHandler(t *testing.T) *handler.AuthHandler {
	t.Helper()
	log := slog.New(slog.NewTextHandler(os.Stderr, nil))
	return handler.NewAuthHandler(newStubStore(), testSecret, log)
}

func postJSON(t *testing.T, h http.HandlerFunc, path string, body any) *httptest.ResponseRecorder {
	t.Helper()
	b, _ := json.Marshal(body)
	req := httptest.NewRequest(http.MethodPost, path, bytes.NewReader(b))
	req.Header.Set("Content-Type", "application/json")
	rr := httptest.NewRecorder()
	h(rr, req)
	return rr
}

// ---------- tests ----------

func TestRegister_Success(t *testing.T) {
	h := newHandler(t)
	rr := postJSON(t, h.Register, "/api/auth/register", map[string]string{
		"username":     "alice",
		"email":        "alice@example.com",
		"password":     "password123",
		"display_name": "Alice",
	})
	if rr.Code != http.StatusCreated {
		t.Fatalf("expected 201, got %d: %s", rr.Code, rr.Body.String())
	}
	var resp map[string]any
	json.NewDecoder(rr.Body).Decode(&resp)
	if resp["token"] == "" {
		t.Fatal("expected token in response")
	}
}

func TestRegister_ShortUsername(t *testing.T) {
	h := newHandler(t)
	rr := postJSON(t, h.Register, "/api/auth/register", map[string]string{
		"username": "ab", "email": "x@x.com", "password": "password123",
	})
	if rr.Code != http.StatusUnprocessableEntity {
		t.Fatalf("expected 422, got %d", rr.Code)
	}
}

func TestRegister_ShortPassword(t *testing.T) {
	h := newHandler(t)
	rr := postJSON(t, h.Register, "/api/auth/register", map[string]string{
		"username": "alice", "email": "x@x.com", "password": "short",
	})
	if rr.Code != http.StatusUnprocessableEntity {
		t.Fatalf("expected 422, got %d", rr.Code)
	}
}

func TestLogin_Success(t *testing.T) {
	h := newHandler(t)
	// register first
	postJSON(t, h.Register, "/api/auth/register", map[string]string{
		"username": "bob", "email": "bob@example.com", "password": "password123",
	})

	rr := postJSON(t, h.Login, "/api/auth/login", map[string]string{
		"login": "bob", "password": "password123",
	})
	if rr.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rr.Code, rr.Body.String())
	}
}

func TestLogin_ByEmail(t *testing.T) {
	h := newHandler(t)
	postJSON(t, h.Register, "/api/auth/register", map[string]string{
		"username": "carol", "email": "carol@example.com", "password": "password123",
	})

	rr := postJSON(t, h.Login, "/api/auth/login", map[string]string{
		"login": "carol@example.com", "password": "password123",
	})
	if rr.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rr.Code, rr.Body.String())
	}
}

func TestLogin_WrongPassword(t *testing.T) {
	h := newHandler(t)
	postJSON(t, h.Register, "/api/auth/register", map[string]string{
		"username": "dave", "email": "dave@example.com", "password": "password123",
	})

	rr := postJSON(t, h.Login, "/api/auth/login", map[string]string{
		"login": "dave", "password": "wrongpass",
	})
	if rr.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401, got %d", rr.Code)
	}
}

func TestMe_WithValidToken(t *testing.T) {
	store := newStubStore()
	log := slog.New(slog.NewTextHandler(os.Stderr, nil))
	h := handler.NewAuthHandler(store, testSecret, log)

	// register via handler to get a real user in store
	rr := postJSON(t, h.Register, "/api/auth/register", map[string]string{
		"username": "eve", "email": "eve@example.com", "password": "password123",
	})
	var regResp map[string]any
	json.NewDecoder(rr.Body).Decode(&regResp)
	tokenStr := regResp["token"].(string)

	// inject claims into context
	claims, _ := auth.ValidateToken(testSecret, tokenStr)
	req := httptest.NewRequest(http.MethodGet, "/api/auth/me", nil)
	req = req.WithContext(context.WithValue(req.Context(), handler.ClaimsKey, claims))
	rec := httptest.NewRecorder()
	h.Me(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
}

func TestMe_WithoutToken(t *testing.T) {
	h := newHandler(t)
	req := httptest.NewRequest(http.MethodGet, "/api/auth/me", nil)
	rec := httptest.NewRecorder()
	h.Me(rec, req)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401, got %d", rec.Code)
	}
}

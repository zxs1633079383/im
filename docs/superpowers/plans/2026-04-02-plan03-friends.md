# Plan 3: 联系人与好友系统 Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** 实现好友申请/接受/拒绝、好友列表、用户搜索，以及客户端联系人页面

**Architecture:** FriendshipStore 操作 PG friendships 表，HTTP handlers 提供 RESTful API，客户端 FriendService + Contacts 页面提供完整交互。

**Tech Stack:** Go net/http, pgx, Angular HttpClient, Angular Signals

---

## 目录结构（Plan 3 新增文件）

```
server/
└── internal/
    ├── store/
    │   └── friendship.go          # FriendshipStore: DB operations
    │   └── friendship_test.go     # integration tests (package store_test)
    │   └── user.go                # add Search method
    │   └── user_test.go           # add Search test
    ├── handler/
    │   └── friend.go              # FriendHandler + UserSearchHandler
    │   └── friend_test.go         # unit tests with stub store
    └── cmd/gateway/main.go        # wire friend + search routes

client/src/app/
├── core/
│   └── friends/
│       └── friend.service.ts      # FriendService wrapping API
└── features/
    └── contacts/
        ├── contacts.component.ts
        ├── contacts.component.html
        └── contacts.component.scss
```

---

## Task 1: FriendshipStore (`internal/store/friendship.go`) + tests

**Files:**
- Create: `server/internal/store/friendship.go`
- Create: `server/internal/store/friendship_test.go`

### Overview

`FriendshipStore` wraps a `pgxpool.Pool` and exposes all friendship DB operations.
It returns `handler.ErrNotFound` (via `pgx.ErrNoRows`) for missing rows, and
forwards all other errors wrapped with `fmt.Errorf`.

### 1.1 Create `server/internal/store/friendship.go`

```go
package store

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"im-server/internal/model"
)

// ErrNotFound is returned when a record does not exist.
var ErrNotFound = errors.New("not found")

// ErrAlreadyExists is returned when a friendship row already exists.
var ErrAlreadyExists = errors.New("already exists")

// FriendshipStore handles all DB operations on the friendships table.
type FriendshipStore struct {
	pool *pgxpool.Pool
}

func NewFriendshipStore(pool *pgxpool.Pool) *FriendshipStore {
	return &FriendshipStore{pool: pool}
}

// SendRequest inserts a new friendship row with status=pending.
// Returns ErrAlreadyExists if a row between these two users already exists
// (in either direction).
func (s *FriendshipStore) SendRequest(ctx context.Context, requesterID, addresseeID int64) error {
	if requesterID == addresseeID {
		return fmt.Errorf("cannot send friend request to yourself")
	}
	_, err := s.pool.Exec(ctx,
		`INSERT INTO friendships (requester_id, addressee_id, status)
		 VALUES ($1, $2, $3)`,
		requesterID, addresseeID, model.FriendshipPending,
	)
	if err != nil {
		if isUniqueViolation(err) {
			return ErrAlreadyExists
		}
		return fmt.Errorf("send request: %w", err)
	}
	return nil
}

// AcceptRequest sets status=accepted for a pending friendship.
// Only the addressee (userID) may accept.
func (s *FriendshipStore) AcceptRequest(ctx context.Context, friendshipID, userID int64) error {
	tag, err := s.pool.Exec(ctx,
		`UPDATE friendships SET status = $1
		 WHERE id = $2 AND addressee_id = $3 AND status = $4`,
		model.FriendshipAccepted, friendshipID, userID, model.FriendshipPending,
	)
	if err != nil {
		return fmt.Errorf("accept request: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

// RejectRequest sets status=rejected for a pending friendship.
// Only the addressee (userID) may reject.
func (s *FriendshipStore) RejectRequest(ctx context.Context, friendshipID, userID int64) error {
	tag, err := s.pool.Exec(ctx,
		`UPDATE friendships SET status = $1
		 WHERE id = $2 AND addressee_id = $3 AND status = $4`,
		model.FriendshipRejected, friendshipID, userID, model.FriendshipPending,
	)
	if err != nil {
		return fmt.Errorf("reject request: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

// ListFriends returns all users who are accepted friends of userID.
// Both directions of the friendship row are considered.
func (s *FriendshipStore) ListFriends(ctx context.Context, userID int64) ([]model.User, error) {
	rows, err := s.pool.Query(ctx,
		`SELECT u.id, u.username, u.email, u.display_name, u.avatar_url, u.status, u.created_at, u.updated_at
		 FROM friendships f
		 JOIN users u ON u.id = CASE
		     WHEN f.requester_id = $1 THEN f.addressee_id
		     ELSE f.requester_id
		 END
		 WHERE (f.requester_id = $1 OR f.addressee_id = $1)
		   AND f.status = $2`,
		userID, model.FriendshipAccepted,
	)
	if err != nil {
		return nil, fmt.Errorf("list friends: %w", err)
	}
	defer rows.Close()

	var friends []model.User
	for rows.Next() {
		var u model.User
		if err := rows.Scan(&u.ID, &u.Username, &u.Email, &u.DisplayName, &u.AvatarURL, &u.Status, &u.CreatedAt, &u.UpdatedAt); err != nil {
			return nil, fmt.Errorf("scan friend: %w", err)
		}
		friends = append(friends, u)
	}
	return friends, rows.Err()
}

// PendingRequest is a friendship row augmented with the requester's user info.
type PendingRequest struct {
	model.Friendship
	Requester model.User `json:"requester"`
}

// ListPendingRequests returns incoming pending friend requests for userID,
// each enriched with the requester's user info.
func (s *FriendshipStore) ListPendingRequests(ctx context.Context, userID int64) ([]PendingRequest, error) {
	rows, err := s.pool.Query(ctx,
		`SELECT f.id, f.requester_id, f.addressee_id, f.status, f.created_at, f.updated_at,
		        u.id, u.username, u.email, u.display_name, u.avatar_url, u.status, u.created_at, u.updated_at
		 FROM friendships f
		 JOIN users u ON u.id = f.requester_id
		 WHERE f.addressee_id = $1 AND f.status = $2
		 ORDER BY f.created_at DESC`,
		userID, model.FriendshipPending,
	)
	if err != nil {
		return nil, fmt.Errorf("list pending: %w", err)
	}
	defer rows.Close()

	var result []PendingRequest
	for rows.Next() {
		var pr PendingRequest
		if err := rows.Scan(
			&pr.Friendship.ID, &pr.Friendship.RequesterID, &pr.Friendship.AddresseeID,
			&pr.Friendship.Status, &pr.Friendship.CreatedAt, &pr.Friendship.UpdatedAt,
			&pr.Requester.ID, &pr.Requester.Username, &pr.Requester.Email,
			&pr.Requester.DisplayName, &pr.Requester.AvatarURL, &pr.Requester.Status,
			&pr.Requester.CreatedAt, &pr.Requester.UpdatedAt,
		); err != nil {
			return nil, fmt.Errorf("scan pending: %w", err)
		}
		result = append(result, pr)
	}
	return result, rows.Err()
}

// GetFriendship returns the friendship row between userA and userB (any direction).
// Returns ErrNotFound if no row exists.
func (s *FriendshipStore) GetFriendship(ctx context.Context, userA, userB int64) (*model.Friendship, error) {
	f := &model.Friendship{}
	err := s.pool.QueryRow(ctx,
		`SELECT id, requester_id, addressee_id, status, created_at, updated_at
		 FROM friendships
		 WHERE (requester_id = $1 AND addressee_id = $2)
		    OR (requester_id = $2 AND addressee_id = $1)`,
		userA, userB,
	).Scan(&f.ID, &f.RequesterID, &f.AddresseeID, &f.Status, &f.CreatedAt, &f.UpdatedAt)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, ErrNotFound
		}
		return nil, fmt.Errorf("get friendship: %w", err)
	}
	return f, nil
}

// BlockUser upserts a friendship row with status=blocked where blockerID is requester.
// If a row already exists in either direction it is updated to blocked.
func (s *FriendshipStore) BlockUser(ctx context.Context, blockerID, blockedID int64) error {
	if blockerID == blockedID {
		return fmt.Errorf("cannot block yourself")
	}
	// Try update first (row exists in either direction)
	tag, err := s.pool.Exec(ctx,
		`UPDATE friendships SET status = $1, requester_id = $2, addressee_id = $3
		 WHERE (requester_id = $2 AND addressee_id = $3)
		    OR (requester_id = $3 AND addressee_id = $2)`,
		model.FriendshipBlocked, blockerID, blockedID,
	)
	if err != nil {
		return fmt.Errorf("block user update: %w", err)
	}
	if tag.RowsAffected() > 0 {
		return nil
	}
	// No row yet — insert new blocked row
	_, err = s.pool.Exec(ctx,
		`INSERT INTO friendships (requester_id, addressee_id, status) VALUES ($1, $2, $3)`,
		blockerID, blockedID, model.FriendshipBlocked,
	)
	if err != nil {
		return fmt.Errorf("block user insert: %w", err)
	}
	return nil
}

// isUniqueViolation detects PG unique constraint errors from pgx.
func isUniqueViolation(err error) bool {
	return err != nil && (contains(err.Error(), "unique") || contains(err.Error(), "duplicate"))
}

func contains(s, sub string) bool {
	return len(s) >= len(sub) && (s == sub || len(s) > 0 && containsRune(s, sub))
}

func containsRune(s, sub string) bool {
	for i := range s {
		if i+len(sub) <= len(s) && s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}
```

> **Note on `isUniqueViolation`:** pgx wraps PG errors in `*pgconn.PgError`.
> The idiomatic check is:
> ```go
> import "github.com/jackc/pgx/v5/pgconn"
> var pgErr *pgconn.PgError
> if errors.As(err, &pgErr) && pgErr.Code == "23505" { ... }
> ```
> Use that approach instead of the string-based helpers shown above. Remove the `contains`/`containsRune`/`isUniqueViolation` helpers and replace with the `pgconn` check.

### 1.2 Create `server/internal/store/friendship_test.go`

```go
package store_test

import (
	"context"
	"testing"

	"im-server/internal/model"
	"im-server/internal/store"
	"im-server/internal/testutil"
)

// helper: create a minimal user and return its ID
func createUser(t *testing.T, us *store.UserStore, username string) int64 {
	t.Helper()
	u := &model.User{
		Username:     username,
		Email:        username + "@example.com",
		PasswordHash: "hash",
		DisplayName:  username,
	}
	if err := us.Create(context.Background(), u); err != nil {
		t.Fatalf("createUser %s: %v", username, err)
	}
	return u.ID
}

func TestFriendshipStore_SendAndAccept(t *testing.T) {
	pool := testutil.PGPool(t)
	us := store.NewUserStore(pool)
	fs := store.NewFriendshipStore(pool)
	ctx := context.Background()

	alice := createUser(t, us, "alice")
	bob := createUser(t, us, "bob")

	// send request
	if err := fs.SendRequest(ctx, alice, bob); err != nil {
		t.Fatalf("SendRequest: %v", err)
	}

	// duplicate should fail
	if err := fs.SendRequest(ctx, alice, bob); err != store.ErrAlreadyExists {
		t.Fatalf("expected ErrAlreadyExists, got %v", err)
	}

	// bob sees a pending request
	pending, err := fs.ListPendingRequests(ctx, bob)
	if err != nil {
		t.Fatalf("ListPendingRequests: %v", err)
	}
	if len(pending) != 1 {
		t.Fatalf("expected 1 pending, got %d", len(pending))
	}
	if pending[0].Requester.ID != alice {
		t.Errorf("requester ID = %d, want %d", pending[0].Requester.ID, alice)
	}

	// bob accepts
	if err := fs.AcceptRequest(ctx, pending[0].Friendship.ID, bob); err != nil {
		t.Fatalf("AcceptRequest: %v", err)
	}

	// now both see each other as friends
	aliceFriends, _ := fs.ListFriends(ctx, alice)
	if len(aliceFriends) != 1 || aliceFriends[0].ID != bob {
		t.Errorf("alice friends: got %v, want [bob]", aliceFriends)
	}
	bobFriends, _ := fs.ListFriends(ctx, bob)
	if len(bobFriends) != 1 || bobFriends[0].ID != alice {
		t.Errorf("bob friends: got %v, want [alice]", bobFriends)
	}
}

func TestFriendshipStore_RejectRequest(t *testing.T) {
	pool := testutil.PGPool(t)
	us := store.NewUserStore(pool)
	fs := store.NewFriendshipStore(pool)
	ctx := context.Background()

	carol := createUser(t, us, "carol")
	dave := createUser(t, us, "dave")

	if err := fs.SendRequest(ctx, carol, dave); err != nil {
		t.Fatal(err)
	}
	pending, _ := fs.ListPendingRequests(ctx, dave)
	if len(pending) != 1 {
		t.Fatal("expected 1 pending request")
	}
	if err := fs.RejectRequest(ctx, pending[0].Friendship.ID, dave); err != nil {
		t.Fatalf("RejectRequest: %v", err)
	}
	friends, _ := fs.ListFriends(ctx, dave)
	if len(friends) != 0 {
		t.Errorf("expected 0 friends after rejection, got %d", len(friends))
	}
}

func TestFriendshipStore_GetFriendship(t *testing.T) {
	pool := testutil.PGPool(t)
	us := store.NewUserStore(pool)
	fs := store.NewFriendshipStore(pool)
	ctx := context.Background()

	eve := createUser(t, us, "eve")
	frank := createUser(t, us, "frank")

	// not found before request
	_, err := fs.GetFriendship(ctx, eve, frank)
	if err != store.ErrNotFound {
		t.Fatalf("expected ErrNotFound, got %v", err)
	}

	if err := fs.SendRequest(ctx, eve, frank); err != nil {
		t.Fatal(err)
	}

	f, err := fs.GetFriendship(ctx, eve, frank)
	if err != nil {
		t.Fatalf("GetFriendship: %v", err)
	}
	if f.Status != model.FriendshipPending {
		t.Errorf("status = %d, want pending", f.Status)
	}

	// reverse lookup also works
	f2, err := fs.GetFriendship(ctx, frank, eve)
	if err != nil {
		t.Fatalf("GetFriendship reverse: %v", err)
	}
	if f2.ID != f.ID {
		t.Errorf("reverse lookup ID mismatch")
	}
}

func TestFriendshipStore_BlockUser(t *testing.T) {
	pool := testutil.PGPool(t)
	us := store.NewUserStore(pool)
	fs := store.NewFriendshipStore(pool)
	ctx := context.Background()

	grace := createUser(t, us, "grace")
	henry := createUser(t, us, "henry")

	// block without prior friendship
	if err := fs.BlockUser(ctx, grace, henry); err != nil {
		t.Fatalf("BlockUser (new): %v", err)
	}
	f, err := fs.GetFriendship(ctx, grace, henry)
	if err != nil {
		t.Fatal(err)
	}
	if f.Status != model.FriendshipBlocked {
		t.Errorf("status = %d, want blocked", f.Status)
	}
}

func TestFriendshipStore_SelfRequest(t *testing.T) {
	pool := testutil.PGPool(t)
	us := store.NewUserStore(pool)
	fs := store.NewFriendshipStore(pool)
	ctx := context.Background()

	ian := createUser(t, us, "ian")
	if err := fs.SendRequest(ctx, ian, ian); err == nil {
		t.Fatal("expected error for self-request, got nil")
	}
}

func TestFriendshipStore_AcceptWrongUser(t *testing.T) {
	pool := testutil.PGPool(t)
	us := store.NewUserStore(pool)
	fs := store.NewFriendshipStore(pool)
	ctx := context.Background()

	jane := createUser(t, us, "jane")
	kate := createUser(t, us, "kate")
	leo := createUser(t, us, "leo")

	if err := fs.SendRequest(ctx, jane, kate); err != nil {
		t.Fatal(err)
	}
	pending, _ := fs.ListPendingRequests(ctx, kate)
	// leo tries to accept jane's request to kate — should get ErrNotFound
	err := fs.AcceptRequest(ctx, pending[0].Friendship.ID, leo)
	if err != store.ErrNotFound {
		t.Fatalf("expected ErrNotFound, got %v", err)
	}
}
```

### 1.3 Run tests

```bash
cd /Users/mac17/workspace/ai/im/server
IM_TEST_PG_DSN="postgres://..." go test ./internal/store/... -v -run TestFriendshipStore
```

### Commit

```bash
cd /Users/mac17/workspace/ai/im
git add server/internal/store/friendship.go server/internal/store/friendship_test.go
git commit -m "feat(store): add FriendshipStore with send/accept/reject/list/block"
```

---

## Task 2: User search — add `Search` to UserStore + tests

**Files:**
- Modify: `server/internal/store/user.go`
- Modify: `server/internal/store/user_test.go`

### 2.1 Add `Search` method to `server/internal/store/user.go`

Append to the existing file:

```go
// Search returns up to 20 users whose username or display_name match the query
// (case-insensitive prefix/substring). The calling user (callerID) is excluded.
func (s *UserStore) Search(ctx context.Context, q string, callerID int64) ([]model.User, error) {
	pattern := "%" + q + "%"
	rows, err := s.pool.Query(ctx,
		`SELECT id, username, email, display_name, avatar_url, status, created_at, updated_at
		 FROM users
		 WHERE id != $1
		   AND (username ILIKE $2 OR display_name ILIKE $2)
		 ORDER BY username
		 LIMIT 20`,
		callerID, pattern,
	)
	if err != nil {
		return nil, fmt.Errorf("search users: %w", err)
	}
	defer rows.Close()

	var users []model.User
	for rows.Next() {
		var u model.User
		if err := rows.Scan(&u.ID, &u.Username, &u.Email, &u.DisplayName, &u.AvatarURL, &u.Status, &u.CreatedAt, &u.UpdatedAt); err != nil {
			return nil, fmt.Errorf("scan user: %w", err)
		}
		users = append(users, u)
	}
	return users, rows.Err()
}
```

### 2.2 Add `TestUserStore_Search` to `server/internal/store/user_test.go`

Append to the existing test file:

```go
func TestUserStore_Search(t *testing.T) {
	pool := testutil.PGPool(t)
	us := store.NewUserStore(pool)
	ctx := context.Background()

	users := []model.User{
		{Username: "alpha", Email: "alpha@x.com", PasswordHash: "h", DisplayName: "Alpha User"},
		{Username: "beta",  Email: "beta@x.com",  PasswordHash: "h", DisplayName: "Beta Tester"},
		{Username: "gamma", Email: "gamma@x.com", PasswordHash: "h", DisplayName: "Gamma"},
	}
	for i := range users {
		if err := us.Create(ctx, &users[i]); err != nil {
			t.Fatal(err)
		}
	}

	// search by username prefix
	got, err := us.Search(ctx, "alp", 0)
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if len(got) != 1 || got[0].Username != "alpha" {
		t.Errorf("expected [alpha], got %v", got)
	}

	// search by display_name substring
	got, err = us.Search(ctx, "Tester", 0)
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if len(got) != 1 || got[0].Username != "beta" {
		t.Errorf("expected [beta], got %v", got)
	}

	// caller excluded from results
	got, err = us.Search(ctx, "alpha", users[0].ID)
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	for _, u := range got {
		if u.ID == users[0].ID {
			t.Error("caller should be excluded from search results")
		}
	}

	// empty query returns up to 20 others (not the caller)
	got, err = us.Search(ctx, "", users[0].ID)
	if err != nil {
		t.Fatalf("Search empty: %v", err)
	}
	if len(got) != 2 { // beta and gamma
		t.Errorf("expected 2, got %d", len(got))
	}
}
```

### 2.3 Run tests

```bash
cd /Users/mac17/workspace/ai/im/server
IM_TEST_PG_DSN="postgres://..." go test ./internal/store/... -v -run TestUserStore_Search
```

### Commit

```bash
cd /Users/mac17/workspace/ai/im
git add server/internal/store/user.go server/internal/store/user_test.go
git commit -m "feat(store): add UserStore.Search for user lookup"
```

---

## Task 3: Friend HTTP handlers (`internal/handler/friend.go`) + tests

**Files:**
- Create: `server/internal/handler/friend.go`
- Create: `server/internal/handler/friend_test.go`

### Overview

`FriendHandler` depends on two interfaces (defined in the same file):
- `FriendStore` — exposes the friendship operations
- `FriendUserStore` — exposes `GetByID` and `Search`

Handler test uses in-memory stubs; no DB needed.

### 3.1 Create `server/internal/handler/friend.go`

```go
package handler

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"strconv"

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
	_ = strconv.Itoa(0) // ensure strconv imported; remove if unused

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
```

> **Cleanup note:** Remove the `_ = strconv.Itoa(0)` line and the `"strconv"` import — it was a placeholder to keep the import. The `strconv` package is not needed in this handler.

### 3.2 Create `server/internal/handler/friend_test.go`

```go
package handler_test

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"

	"im-server/internal/auth"
	"im-server/internal/handler"
	"im-server/internal/model"
	"im-server/internal/store"
)

// ---------- in-memory stub FriendStore ----------

type stubFriendStore struct {
	friendships []model.Friendship
	nextID      int64
}

func newStubFriendStore() *stubFriendStore {
	return &stubFriendStore{nextID: 1}
}

func (s *stubFriendStore) SendRequest(_ context.Context, requesterID, addresseeID int64) error {
	if requesterID == addresseeID {
		return store.ErrAlreadyExists
	}
	for _, f := range s.friendships {
		if (f.RequesterID == requesterID && f.AddresseeID == addresseeID) ||
			(f.RequesterID == addresseeID && f.AddresseeID == requesterID) {
			return store.ErrAlreadyExists
		}
	}
	s.friendships = append(s.friendships, model.Friendship{
		ID:          s.nextID,
		RequesterID: requesterID,
		AddresseeID: addresseeID,
		Status:      model.FriendshipPending,
	})
	s.nextID++
	return nil
}

func (s *stubFriendStore) AcceptRequest(_ context.Context, friendshipID, userID int64) error {
	for i := range s.friendships {
		f := &s.friendships[i]
		if f.ID == friendshipID && f.AddresseeID == userID && f.Status == model.FriendshipPending {
			f.Status = model.FriendshipAccepted
			return nil
		}
	}
	return store.ErrNotFound
}

func (s *stubFriendStore) RejectRequest(_ context.Context, friendshipID, userID int64) error {
	for i := range s.friendships {
		f := &s.friendships[i]
		if f.ID == friendshipID && f.AddresseeID == userID && f.Status == model.FriendshipPending {
			f.Status = model.FriendshipRejected
			return nil
		}
	}
	return store.ErrNotFound
}

func (s *stubFriendStore) ListFriends(_ context.Context, userID int64) ([]model.User, error) {
	var friends []model.User
	for _, f := range s.friendships {
		if f.Status != model.FriendshipAccepted {
			continue
		}
		if f.RequesterID == userID {
			friends = append(friends, model.User{ID: f.AddresseeID})
		} else if f.AddresseeID == userID {
			friends = append(friends, model.User{ID: f.RequesterID})
		}
	}
	return friends, nil
}

func (s *stubFriendStore) ListPendingRequests(_ context.Context, userID int64) ([]store.PendingRequest, error) {
	var result []store.PendingRequest
	for _, f := range s.friendships {
		if f.AddresseeID == userID && f.Status == model.FriendshipPending {
			result = append(result, store.PendingRequest{
				Friendship: f,
				Requester:  model.User{ID: f.RequesterID, Username: "requester"},
			})
		}
	}
	return result, nil
}

func (s *stubFriendStore) BlockUser(_ context.Context, blockerID, blockedID int64) error {
	for i := range s.friendships {
		f := &s.friendships[i]
		if (f.RequesterID == blockerID && f.AddresseeID == blockedID) ||
			(f.RequesterID == blockedID && f.AddresseeID == blockerID) {
			f.Status = model.FriendshipBlocked
			f.RequesterID = blockerID
			f.AddresseeID = blockedID
			return nil
		}
	}
	s.friendships = append(s.friendships, model.Friendship{
		ID:          s.nextID,
		RequesterID: blockerID,
		AddresseeID: blockedID,
		Status:      model.FriendshipBlocked,
	})
	s.nextID++
	return nil
}

// ---------- in-memory stub FriendUserStore ----------

type stubFriendUserStore struct {
	users map[int64]*model.User
}

func newStubFriendUserStore() *stubFriendUserStore {
	return &stubFriendUserStore{users: map[int64]*model.User{
		1: {ID: 1, Username: "alice", DisplayName: "Alice"},
		2: {ID: 2, Username: "bob", DisplayName: "Bob"},
	}}
}

func (s *stubFriendUserStore) GetByID(_ context.Context, id int64) (*model.User, error) {
	u, ok := s.users[id]
	if !ok {
		return nil, handler.ErrNotFound
	}
	return u, nil
}

func (s *stubFriendUserStore) Search(_ context.Context, q string, callerID int64) ([]model.User, error) {
	var result []model.User
	for _, u := range s.users {
		if u.ID != callerID && (q == "" || contains(u.Username, q)) {
			result = append(result, *u)
		}
	}
	return result, nil
}

func contains(s, sub string) bool {
	return len(sub) == 0 || (len(s) >= len(sub) && func() bool {
		for i := range s {
			if i+len(sub) <= len(s) && s[i:i+len(sub)] == sub {
				return true
			}
		}
		return false
	}())
}

// ---------- test helpers ----------

const friendTestSecret = "test-secret-32-bytes-long-enough!"

func newFriendHandler(t *testing.T) *handler.FriendHandler {
	t.Helper()
	log := slog.New(slog.NewTextHandler(os.Stderr, nil))
	return handler.NewFriendHandler(newStubFriendStore(), newStubFriendUserStore(), log)
}

func requestWithClaims(method, path string, userID int64, body any) *http.Request {
	var req *http.Request
	if body != nil {
		b, _ := json.Marshal(body)
		req = httptest.NewRequest(method, path, bytes.NewReader(b))
		req.Header.Set("Content-Type", "application/json")
	} else {
		req = httptest.NewRequest(method, path, nil)
	}
	claims := &auth.Claims{}
	claims.UserID = userID
	ctx := req.Context()
	ctx = contextWithClaims(ctx, claims)
	return req.WithContext(ctx)
}

// contextWithClaims injects claims using the exported handler.ClaimsKey.
func contextWithClaims(ctx interface{ Value(any) any }, claims *auth.Claims) interface {
	Deadline() (deadline interface{ Equal(interface{}) bool }, ok bool)
	Done() <-chan struct{}
	Err() error
	Value(key any) any
} {
	// use standard context package
	panic("use context.WithValue(req.Context(), handler.ClaimsKey, claims) directly")
}
```

> **Note on `requestWithClaims`:** The helper above has a placeholder panic. Use the real implementation:
> ```go
> import "context"
> func requestWithClaims(method, path string, userID int64, body any) *http.Request {
>     var req *http.Request
>     if body != nil {
>         b, _ := json.Marshal(body)
>         req = httptest.NewRequest(method, path, bytes.NewReader(b))
>         req.Header.Set("Content-Type", "application/json")
>     } else {
>         req = httptest.NewRequest(method, path, nil)
>     }
>     claims := &auth.Claims{}
>     claims.UserID = userID
>     req = req.WithContext(context.WithValue(req.Context(), handler.ClaimsKey, claims))
>     return req
> }
> ```
> Use this corrected version; remove the `contextWithClaims` function entirely.

```go
// ---------- tests ----------

func TestFriendHandler_SendRequest_Success(t *testing.T) {
	h := newFriendHandler(t)
	req := requestWithClaims("POST", "/api/friends/request", 1, map[string]int64{"addressee_id": 2})
	rr := httptest.NewRecorder()
	h.SendRequest(rr, req)
	if rr.Code != http.StatusCreated {
		t.Fatalf("expected 201, got %d: %s", rr.Code, rr.Body.String())
	}
}

func TestFriendHandler_SendRequest_MissingAddressee(t *testing.T) {
	h := newFriendHandler(t)
	req := requestWithClaims("POST", "/api/friends/request", 1, map[string]int64{"addressee_id": 0})
	rr := httptest.NewRecorder()
	h.SendRequest(rr, req)
	if rr.Code != http.StatusUnprocessableEntity {
		t.Fatalf("expected 422, got %d", rr.Code)
	}
}

func TestFriendHandler_SendRequest_Duplicate(t *testing.T) {
	h := newFriendHandler(t)
	body := map[string]int64{"addressee_id": 2}
	req1 := requestWithClaims("POST", "/api/friends/request", 1, body)
	httptest.NewRecorder() // discard first
	rr1 := httptest.NewRecorder()
	h.SendRequest(rr1, req1)

	req2 := requestWithClaims("POST", "/api/friends/request", 1, body)
	rr2 := httptest.NewRecorder()
	h.SendRequest(rr2, req2)
	if rr2.Code != http.StatusConflict {
		t.Fatalf("expected 409, got %d: %s", rr2.Code, rr2.Body.String())
	}
}

func TestFriendHandler_AcceptRequest_Success(t *testing.T) {
	fs := newStubFriendStore()
	log := slog.New(slog.NewTextHandler(os.Stderr, nil))
	h := handler.NewFriendHandler(fs, newStubFriendUserStore(), log)

	// send from user 1 to user 2
	_ = fs.SendRequest(context.Background(), 1, 2)

	// user 2 accepts friendship ID=1
	req := requestWithClaims("POST", "/api/friends/accept", 2, map[string]int64{"friendship_id": 1})
	rr := httptest.NewRecorder()
	h.AcceptRequest(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rr.Code, rr.Body.String())
	}
}

func TestFriendHandler_AcceptRequest_WrongUser(t *testing.T) {
	fs := newStubFriendStore()
	log := slog.New(slog.NewTextHandler(os.Stderr, nil))
	h := handler.NewFriendHandler(fs, newStubFriendUserStore(), log)

	_ = fs.SendRequest(context.Background(), 1, 2)

	// user 1 tries to accept their own outgoing request — should 404
	req := requestWithClaims("POST", "/api/friends/accept", 1, map[string]int64{"friendship_id": 1})
	rr := httptest.NewRecorder()
	h.AcceptRequest(rr, req)
	if rr.Code != http.StatusNotFound {
		t.Fatalf("expected 404, got %d", rr.Code)
	}
}

func TestFriendHandler_ListFriends_Empty(t *testing.T) {
	h := newFriendHandler(t)
	req := requestWithClaims("GET", "/api/friends", 1, nil)
	rr := httptest.NewRecorder()
	h.ListFriends(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rr.Code)
	}
	var result []any
	json.NewDecoder(rr.Body).Decode(&result)
	if len(result) != 0 {
		t.Errorf("expected empty list, got %v", result)
	}
}

func TestFriendHandler_SearchUsers(t *testing.T) {
	h := newFriendHandler(t)
	req := requestWithClaims("GET", "/api/users/search?q=bob", 1, nil)
	rr := httptest.NewRecorder()
	h.SearchUsers(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rr.Code, rr.Body.String())
	}
	var users []map[string]any
	json.NewDecoder(rr.Body).Decode(&users)
	if len(users) == 0 {
		t.Error("expected at least one result for 'bob'")
	}
}

func TestFriendHandler_NoAuth(t *testing.T) {
	log := slog.New(slog.NewTextHandler(os.Stderr, nil))
	h := handler.NewFriendHandler(newStubFriendStore(), newStubFriendUserStore(), log)

	// request without claims in context
	req := httptest.NewRequest("GET", "/api/friends", nil)
	rr := httptest.NewRecorder()
	h.ListFriends(rr, req)
	if rr.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401, got %d", rr.Code)
	}
}
```

### 3.3 Compile check

```bash
cd /Users/mac17/workspace/ai/im/server
go build ./...
go test ./internal/handler/... -v
```

### Commit

```bash
cd /Users/mac17/workspace/ai/im
git add server/internal/handler/friend.go server/internal/handler/friend_test.go
git commit -m "feat(handler): add FriendHandler and user search endpoint"
```

---

## Task 4: Wire routes in `cmd/gateway/main.go`

**Files:**
- Modify: `server/cmd/gateway/main.go`

### 4.1 Updated `server/cmd/gateway/main.go`

Replace the `run()` function body from `// Wire stores` onward with:

```go
	userStore       := store.NewUserStore(pool)
	friendshipStore := store.NewFriendshipStore(pool)

	authHandler   := handler.NewAuthHandler(userStore, cfg.Gateway.JWTSecret, log)
	friendHandler := handler.NewFriendHandler(friendshipStore, userStore, log)
	jwtMiddleware  := middleware.JWTAuth(cfg.Gateway.JWTSecret)

	mux := http.NewServeMux()

	// Public auth routes
	mux.HandleFunc("POST /api/auth/register", authHandler.Register)
	mux.HandleFunc("POST /api/auth/login",    authHandler.Login)

	// Protected auth route
	mux.Handle("GET /api/auth/me", jwtMiddleware(http.HandlerFunc(authHandler.Me)))

	// Protected friend routes
	mux.Handle("POST /api/friends/request", jwtMiddleware(http.HandlerFunc(friendHandler.SendRequest)))
	mux.Handle("POST /api/friends/accept",  jwtMiddleware(http.HandlerFunc(friendHandler.AcceptRequest)))
	mux.Handle("POST /api/friends/reject",  jwtMiddleware(http.HandlerFunc(friendHandler.RejectRequest)))
	mux.Handle("GET /api/friends",          jwtMiddleware(http.HandlerFunc(friendHandler.ListFriends)))
	mux.Handle("GET /api/friends/pending",  jwtMiddleware(http.HandlerFunc(friendHandler.ListPending)))
	mux.Handle("POST /api/friends/block",   jwtMiddleware(http.HandlerFunc(friendHandler.Block)))

	// Protected user search route
	mux.Handle("GET /api/users/search", jwtMiddleware(http.HandlerFunc(friendHandler.SearchUsers)))
```

Full updated `server/cmd/gateway/main.go`:

```go
package main

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"im-server/internal/config"
	"im-server/internal/handler"
	"im-server/internal/middleware"
	"im-server/internal/store"
)

func main() {
	fmt.Println("gateway starting...")
	os.Exit(run())
}

func run() int {
	log := slog.New(slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelInfo}))

	cfgPath := os.Getenv("IM_CONFIG")
	if cfgPath == "" {
		cfgPath = "config.yaml"
	}

	cfg, err := config.Load(cfgPath)
	if err != nil {
		log.Error("load config", "error", err)
		return 1
	}

	if cfg.Gateway.JWTSecret == "" {
		log.Error("gateway.jwt_secret must not be empty")
		return 1
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	pool, err := store.NewPGPool(ctx, cfg.PG.DSN, cfg.PG.MaxConns)
	if err != nil {
		log.Error("connect to postgres", "error", err)
		return 1
	}
	defer pool.Close()

	userStore       := store.NewUserStore(pool)
	friendshipStore := store.NewFriendshipStore(pool)

	authHandler   := handler.NewAuthHandler(userStore, cfg.Gateway.JWTSecret, log)
	friendHandler := handler.NewFriendHandler(friendshipStore, userStore, log)
	jwtMiddleware  := middleware.JWTAuth(cfg.Gateway.JWTSecret)

	mux := http.NewServeMux()

	// Public auth routes
	mux.HandleFunc("POST /api/auth/register", authHandler.Register)
	mux.HandleFunc("POST /api/auth/login",    authHandler.Login)

	// Protected auth route
	mux.Handle("GET /api/auth/me", jwtMiddleware(http.HandlerFunc(authHandler.Me)))

	// Protected friend routes
	mux.Handle("POST /api/friends/request", jwtMiddleware(http.HandlerFunc(friendHandler.SendRequest)))
	mux.Handle("POST /api/friends/accept",  jwtMiddleware(http.HandlerFunc(friendHandler.AcceptRequest)))
	mux.Handle("POST /api/friends/reject",  jwtMiddleware(http.HandlerFunc(friendHandler.RejectRequest)))
	mux.Handle("GET /api/friends",          jwtMiddleware(http.HandlerFunc(friendHandler.ListFriends)))
	mux.Handle("GET /api/friends/pending",  jwtMiddleware(http.HandlerFunc(friendHandler.ListPending)))
	mux.Handle("POST /api/friends/block",   jwtMiddleware(http.HandlerFunc(friendHandler.Block)))

	// Protected user search route
	mux.Handle("GET /api/users/search", jwtMiddleware(http.HandlerFunc(friendHandler.SearchUsers)))

	// CORS middleware for development
	corsHandler := corsMiddleware(mux)

	srv := &http.Server{
		Addr:         cfg.Gateway.HTTPAddr,
		Handler:      corsHandler,
		ReadTimeout:  15 * time.Second,
		WriteTimeout: 15 * time.Second,
		IdleTimeout:  60 * time.Second,
	}

	quit := make(chan os.Signal, 1)
	signal.Notify(quit, syscall.SIGINT, syscall.SIGTERM)

	go func() {
		log.Info("HTTP server listening", "addr", cfg.Gateway.HTTPAddr)
		if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			log.Error("server error", "error", err)
			os.Exit(1)
		}
	}()

	<-quit
	log.Info("shutting down...")

	shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer shutdownCancel()
	if err := srv.Shutdown(shutdownCtx); err != nil {
		log.Error("shutdown error", "error", err)
		return 1
	}

	return 0
}

// corsMiddleware adds permissive CORS headers for local Tauri development.
func corsMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Access-Control-Allow-Origin", "*")
		w.Header().Set("Access-Control-Allow-Methods", "GET, POST, PUT, DELETE, OPTIONS")
		w.Header().Set("Access-Control-Allow-Headers", "Content-Type, Authorization")
		if r.Method == http.MethodOptions {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		next.ServeHTTP(w, r)
	})
}
```

### 4.2 Compile check

```bash
cd /Users/mac17/workspace/ai/im/server
go build ./cmd/gateway/...
```

### Commit

```bash
cd /Users/mac17/workspace/ai/im
git add server/cmd/gateway/main.go
git commit -m "feat(gateway): wire friend and user-search routes"
```

---

## Task 5: Client FriendService (`core/friends/friend.service.ts`)

**Files:**
- Create: `client/src/app/core/friends/friend.service.ts`

### 5.1 Create `client/src/app/core/friends/friend.service.ts`

```typescript
import { Injectable, signal } from '@angular/core';
import { HttpClient } from '@angular/common/http';
import { firstValueFrom } from 'rxjs';

// ---------- types ----------

export interface Friend {
  id: number;
  username: string;
  display_name: string;
  avatar_url: string;
  status: number;
}

export interface PendingRequest {
  id: number;
  requester_id: number;
  addressee_id: number;
  status: number;
  created_at: string;
  updated_at: string;
  requester: Friend;
}

export interface UserSearchResult {
  id: number;
  username: string;
  display_name: string;
  avatar_url: string;
  status: number;
}

const API_BASE = 'http://localhost:8080/api';

@Injectable({ providedIn: 'root' })
export class FriendService {
  /** Reactive signal: accepted friend list */
  readonly friends = signal<Friend[]>([]);

  /** Reactive signal: incoming pending requests */
  readonly pendingRequests = signal<PendingRequest[]>([]);

  constructor(private http: HttpClient) {}

  // ---------- friend operations ----------

  async sendRequest(addresseeId: number): Promise<void> {
    await firstValueFrom(
      this.http.post(`${API_BASE}/friends/request`, { addressee_id: addresseeId })
    );
  }

  async acceptRequest(friendshipId: number): Promise<void> {
    await firstValueFrom(
      this.http.post(`${API_BASE}/friends/accept`, { friendship_id: friendshipId })
    );
    await this.loadPendingRequests();
    await this.loadFriends();
  }

  async rejectRequest(friendshipId: number): Promise<void> {
    await firstValueFrom(
      this.http.post(`${API_BASE}/friends/reject`, { friendship_id: friendshipId })
    );
    await this.loadPendingRequests();
  }

  async blockUser(userId: number): Promise<void> {
    await firstValueFrom(
      this.http.post(`${API_BASE}/friends/block`, { user_id: userId })
    );
    await this.loadFriends();
  }

  // ---------- data loading ----------

  async loadFriends(): Promise<void> {
    const data = await firstValueFrom(
      this.http.get<Friend[]>(`${API_BASE}/friends`)
    );
    this.friends.set(data);
  }

  async loadPendingRequests(): Promise<void> {
    const data = await firstValueFrom(
      this.http.get<PendingRequest[]>(`${API_BASE}/friends/pending`)
    );
    this.pendingRequests.set(data);
  }

  async searchUsers(q: string): Promise<UserSearchResult[]> {
    return firstValueFrom(
      this.http.get<UserSearchResult[]>(`${API_BASE}/users/search`, {
        params: { q },
      })
    );
  }
}
```

### Commit

```bash
cd /Users/mac17/workspace/ai/im
git add client/src/app/core/friends/friend.service.ts
git commit -m "feat(client): add FriendService with signals and API calls"
```

---

## Task 6: Contacts page (`features/contacts/`)

**Files:**
- Create: `client/src/app/features/contacts/contacts.component.ts`
- Create: `client/src/app/features/contacts/contacts.component.html`
- Create: `client/src/app/features/contacts/contacts.component.scss`
- Modify: `client/src/app/app.routes.ts`

### 6.1 Create `client/src/app/features/contacts/contacts.component.ts`

```typescript
import { Component, inject, signal, OnInit } from '@angular/core';
import { CommonModule } from '@angular/common';
import { FormsModule } from '@angular/forms';
import { FriendService, UserSearchResult } from '../../core/friends/friend.service';

@Component({
  selector: 'app-contacts',
  standalone: true,
  imports: [CommonModule, FormsModule],
  templateUrl: './contacts.component.html',
  styleUrl: './contacts.component.scss',
})
export class ContactsComponent implements OnInit {
  friendService = inject(FriendService);

  /** Controls which tab is active: 'friends' | 'pending' | 'search' */
  activeTab = signal<'friends' | 'pending' | 'search'>('friends');

  searchQuery = signal('');
  searchResults = signal<UserSearchResult[]>([]);
  searching = signal(false);
  searchError = signal('');

  actionError = signal('');
  actionSuccess = signal('');

  async ngOnInit(): Promise<void> {
    await Promise.all([
      this.friendService.loadFriends(),
      this.friendService.loadPendingRequests(),
    ]);
  }

  setTab(tab: 'friends' | 'pending' | 'search'): void {
    this.activeTab.set(tab);
    this.clearMessages();
  }

  async onSearch(): Promise<void> {
    const q = this.searchQuery().trim();
    this.searching.set(true);
    this.searchError.set('');
    try {
      const results = await this.friendService.searchUsers(q);
      this.searchResults.set(results);
    } catch {
      this.searchError.set('Search failed. Please try again.');
    } finally {
      this.searching.set(false);
    }
  }

  async addFriend(userId: number): Promise<void> {
    this.clearMessages();
    try {
      await this.friendService.sendRequest(userId);
      this.actionSuccess.set('Friend request sent!');
    } catch (err: any) {
      const msg = err?.error?.error ?? 'Failed to send request.';
      this.actionError.set(msg);
    }
  }

  async accept(friendshipId: number): Promise<void> {
    this.clearMessages();
    try {
      await this.friendService.acceptRequest(friendshipId);
      this.actionSuccess.set('Friend request accepted!');
    } catch {
      this.actionError.set('Failed to accept request.');
    }
  }

  async reject(friendshipId: number): Promise<void> {
    this.clearMessages();
    try {
      await this.friendService.rejectRequest(friendshipId);
    } catch {
      this.actionError.set('Failed to reject request.');
    }
  }

  private clearMessages(): void {
    this.actionError.set('');
    this.actionSuccess.set('');
  }
}
```

### 6.2 Create `client/src/app/features/contacts/contacts.component.html`

```html
<div class="contacts-page">
  <h2>Contacts</h2>

  <!-- Tab bar -->
  <div class="tabs">
    <button [class.active]="activeTab() === 'friends'" (click)="setTab('friends')">
      Friends ({{ friendService.friends().length }})
    </button>
    <button [class.active]="activeTab() === 'pending'" (click)="setTab('pending')">
      Requests ({{ friendService.pendingRequests().length }})
    </button>
    <button [class.active]="activeTab() === 'search'" (click)="setTab('search')">
      Add Friend
    </button>
  </div>

  <!-- Status messages -->
  @if (actionSuccess()) {
    <p class="msg-success">{{ actionSuccess() }}</p>
  }
  @if (actionError()) {
    <p class="msg-error">{{ actionError() }}</p>
  }

  <!-- Friends tab -->
  @if (activeTab() === 'friends') {
    <ul class="user-list">
      @for (friend of friendService.friends(); track friend.id) {
        <li class="user-row">
          <div class="avatar">{{ friend.display_name[0] | uppercase }}</div>
          <div class="info">
            <span class="display-name">{{ friend.display_name }}</span>
            <span class="username">&#64;{{ friend.username }}</span>
          </div>
        </li>
      } @empty {
        <li class="empty">No friends yet. Use "Add Friend" to get started.</li>
      }
    </ul>
  }

  <!-- Pending requests tab -->
  @if (activeTab() === 'pending') {
    <ul class="user-list">
      @for (req of friendService.pendingRequests(); track req.id) {
        <li class="user-row">
          <div class="avatar">{{ req.requester.display_name[0] | uppercase }}</div>
          <div class="info">
            <span class="display-name">{{ req.requester.display_name }}</span>
            <span class="username">&#64;{{ req.requester.username }}</span>
          </div>
          <div class="actions">
            <button class="btn-accept" (click)="accept(req.id)">Accept</button>
            <button class="btn-reject" (click)="reject(req.id)">Reject</button>
          </div>
        </li>
      } @empty {
        <li class="empty">No pending requests.</li>
      }
    </ul>
  }

  <!-- Search tab -->
  @if (activeTab() === 'search') {
    <div class="search-bar">
      <input
        type="text"
        placeholder="Search by username or display name..."
        [ngModel]="searchQuery()"
        (ngModelChange)="searchQuery.set($event)"
        (keyup.enter)="onSearch()"
      />
      <button (click)="onSearch()" [disabled]="searching()">
        {{ searching() ? 'Searching...' : 'Search' }}
      </button>
    </div>
    @if (searchError()) {
      <p class="msg-error">{{ searchError() }}</p>
    }
    <ul class="user-list">
      @for (user of searchResults(); track user.id) {
        <li class="user-row">
          <div class="avatar">{{ user.display_name[0] | uppercase }}</div>
          <div class="info">
            <span class="display-name">{{ user.display_name }}</span>
            <span class="username">&#64;{{ user.username }}</span>
          </div>
          <div class="actions">
            <button class="btn-add" (click)="addFriend(user.id)">Add</button>
          </div>
        </li>
      } @empty {
        @if (!searching() && searchResults().length === 0) {
          <li class="empty">Enter a name and press Search.</li>
        }
      }
    </ul>
  }
</div>
```

### 6.3 Create `client/src/app/features/contacts/contacts.component.scss`

```scss
.contacts-page {
  padding: 1.5rem;
  max-width: 600px;

  h2 {
    margin-bottom: 1rem;
  }
}

.tabs {
  display: flex;
  gap: 0.5rem;
  margin-bottom: 1rem;
  border-bottom: 1px solid #ddd;
  padding-bottom: 0.5rem;

  button {
    background: none;
    border: none;
    padding: 0.4rem 1rem;
    cursor: pointer;
    border-radius: 4px;
    font-size: 0.9rem;
    color: #555;

    &.active {
      background: #3b82f6;
      color: #fff;
    }

    &:hover:not(.active) {
      background: #f0f0f0;
    }
  }
}

.msg-success {
  color: #16a34a;
  margin-bottom: 0.75rem;
}

.msg-error {
  color: #dc2626;
  margin-bottom: 0.75rem;
}

.user-list {
  list-style: none;
  padding: 0;
  margin: 0;
}

.user-row {
  display: flex;
  align-items: center;
  gap: 0.75rem;
  padding: 0.6rem 0;
  border-bottom: 1px solid #f0f0f0;

  &:last-child {
    border-bottom: none;
  }
}

.avatar {
  width: 36px;
  height: 36px;
  background: #3b82f6;
  color: #fff;
  border-radius: 50%;
  display: flex;
  align-items: center;
  justify-content: center;
  font-weight: bold;
  flex-shrink: 0;
}

.info {
  flex: 1;
  display: flex;
  flex-direction: column;

  .display-name {
    font-weight: 600;
    font-size: 0.95rem;
  }

  .username {
    font-size: 0.8rem;
    color: #888;
  }
}

.actions {
  display: flex;
  gap: 0.5rem;
}

button {
  cursor: pointer;
  border-radius: 4px;
  padding: 0.3rem 0.75rem;
  font-size: 0.85rem;
  border: none;

  &[disabled] {
    opacity: 0.6;
    cursor: not-allowed;
  }
}

.btn-accept {
  background: #16a34a;
  color: #fff;
}

.btn-reject {
  background: #dc2626;
  color: #fff;
}

.btn-add {
  background: #3b82f6;
  color: #fff;
}

.search-bar {
  display: flex;
  gap: 0.5rem;
  margin-bottom: 1rem;

  input {
    flex: 1;
    padding: 0.4rem 0.75rem;
    border: 1px solid #ddd;
    border-radius: 4px;
    font-size: 0.9rem;
    outline: none;

    &:focus {
      border-color: #3b82f6;
    }
  }

  button {
    background: #3b82f6;
    color: #fff;
    padding: 0.4rem 1rem;
  }
}

.empty {
  color: #aaa;
  font-size: 0.9rem;
  padding: 0.5rem 0;
}
```

### 6.4 Add contacts route to `client/src/app/app.routes.ts`

Replace the entire file:

```typescript
import { Routes } from '@angular/router';
import { authGuard } from './shared/guards/auth.guard';

export const routes: Routes = [
  {
    path: 'login',
    loadComponent: () =>
      import('./features/login/login.component').then((m) => m.LoginComponent),
  },
  {
    path: 'register',
    loadComponent: () =>
      import('./features/register/register.component').then((m) => m.RegisterComponent),
  },
  {
    path: '',
    canActivate: [authGuard],
    loadComponent: () =>
      import('./features/home/home.component').then((m) => m.HomeComponent),
  },
  {
    path: 'contacts',
    canActivate: [authGuard],
    loadComponent: () =>
      import('./features/contacts/contacts.component').then((m) => m.ContactsComponent),
  },
  {
    path: '**',
    redirectTo: '',
  },
];
```

### 6.5 Add contacts link to home page

Update `client/src/app/features/home/home.component.ts` to add a contacts nav link:

```typescript
import { Component, inject } from '@angular/core';
import { Router, RouterLink } from '@angular/router';
import { AuthService } from '../../core/auth/auth.service';

@Component({
  selector: 'app-home',
  standalone: true,
  imports: [RouterLink],
  template: `
    <div style="padding:2rem">
      <h1>Welcome, {{ auth.currentUser()?.display_name }}!</h1>
      <nav style="margin:1rem 0; display:flex; gap:1rem;">
        <a routerLink="/contacts">Contacts</a>
      </nav>
      <button (click)="logout()">Sign out</button>
    </div>
  `,
})
export class HomeComponent {
  auth = inject(AuthService);
  private router = inject(Router);

  logout(): void {
    this.auth.logout();
    this.router.navigate(['/login']);
  }
}
```

### 6.6 Build check

```bash
cd /Users/mac17/workspace/ai/im/client
npm run build -- --configuration development 2>&1 | tail -20
```

### Commit

```bash
cd /Users/mac17/workspace/ai/im
git add client/src/app/core/friends/ \
        client/src/app/features/contacts/ \
        client/src/app/app.routes.ts \
        client/src/app/features/home/home.component.ts
git commit -m "feat(client): add Contacts page with friend list, pending requests, and user search"
```

---

## Task 7: Integration verification

### 7.1 Run all server tests

```bash
cd /Users/mac17/workspace/ai/im/server

# Unit tests (no DB needed)
go test ./internal/auth/... ./internal/config/... ./internal/middleware/... ./internal/handler/... -v

# Integration tests (DB required)
IM_TEST_PG_DSN="postgres://im:im@localhost:5432/im_test?sslmode=disable" \
  go test ./internal/store/... -v
```

Expected: all tests pass with no compilation errors.

### 7.2 Manual end-to-end smoke test

With the gateway running (`IM_CONFIG=config.yaml ./gateway`), execute:

```bash
# 1. Register two users
curl -s -X POST http://localhost:8080/api/auth/register \
  -H 'Content-Type: application/json' \
  -d '{"username":"alice","email":"alice@x.com","password":"password123","display_name":"Alice"}' \
  | jq .

curl -s -X POST http://localhost:8080/api/auth/register \
  -H 'Content-Type: application/json' \
  -d '{"username":"bob","email":"bob@x.com","password":"password123","display_name":"Bob"}' \
  | jq .

# 2. Login as alice, capture token
ALICE_TOKEN=$(curl -s -X POST http://localhost:8080/api/auth/login \
  -H 'Content-Type: application/json' \
  -d '{"login":"alice","password":"password123"}' | jq -r .token)

# 3. Search for bob
curl -s "http://localhost:8080/api/users/search?q=bob" \
  -H "Authorization: Bearer $ALICE_TOKEN" | jq .

# 4. Get bob's ID and send request (replace BOB_ID)
BOB_ID=2
curl -s -X POST http://localhost:8080/api/friends/request \
  -H "Authorization: Bearer $ALICE_TOKEN" \
  -H 'Content-Type: application/json' \
  -d "{\"addressee_id\":$BOB_ID}" | jq .

# 5. Login as bob, accept request
BOB_TOKEN=$(curl -s -X POST http://localhost:8080/api/auth/login \
  -H 'Content-Type: application/json' \
  -d '{"login":"bob","password":"password123"}' | jq -r .token)

FRIENDSHIP_ID=$(curl -s "http://localhost:8080/api/friends/pending" \
  -H "Authorization: Bearer $BOB_TOKEN" | jq '.[0].id')

curl -s -X POST http://localhost:8080/api/friends/accept \
  -H "Authorization: Bearer $BOB_TOKEN" \
  -H 'Content-Type: application/json' \
  -d "{\"friendship_id\":$FRIENDSHIP_ID}" | jq .

# 6. Both see each other in friends list
curl -s "http://localhost:8080/api/friends" \
  -H "Authorization: Bearer $ALICE_TOKEN" | jq .
curl -s "http://localhost:8080/api/friends" \
  -H "Authorization: Bearer $BOB_TOKEN" | jq .
```

Expected responses:
- Step 3: array containing bob's user object
- Step 4: `{"status":"pending"}`
- Step 5 (accept): `{"status":"accepted"}`
- Step 6: each sees one friend

### 7.3 Client smoke test

```bash
cd /Users/mac17/workspace/ai/im/client
npm start
```

1. Register or log in
2. Navigate to `/contacts`
3. Verify "Friends" tab shows empty list with placeholder text
4. Switch to "Requests" tab — shows empty state
5. Switch to "Add Friend" — search for a registered user, click Add
6. Log in as that user in a separate browser, go to `/contacts` > "Requests", accept
7. Both users now see each other in the "Friends" tab

### Final commit

```bash
cd /Users/mac17/workspace/ai/im
git add -p  # stage any remaining changes
git commit -m "chore: plan 3 implementation complete - friends system"
```

---

## Summary of deliverables

| File | Type | Description |
|---|---|---|
| `server/internal/store/friendship.go` | New | FriendshipStore: SendRequest, AcceptRequest, RejectRequest, ListFriends, ListPendingRequests, GetFriendship, BlockUser |
| `server/internal/store/friendship_test.go` | New | Integration tests for all FriendshipStore methods |
| `server/internal/store/user.go` | Modified | Add Search(ctx, q, callerID) method |
| `server/internal/store/user_test.go` | Modified | Add TestUserStore_Search |
| `server/internal/handler/friend.go` | New | FriendHandler (6 endpoints) + FriendStore/FriendUserStore interfaces |
| `server/internal/handler/friend_test.go` | New | Unit tests with in-memory stubs |
| `server/cmd/gateway/main.go` | Modified | Wire 7 new routes |
| `client/src/app/core/friends/friend.service.ts` | New | FriendService with signals |
| `client/src/app/features/contacts/contacts.component.ts` | New | Contacts page component |
| `client/src/app/features/contacts/contacts.component.html` | New | Contacts template (tabs: friends / pending / search) |
| `client/src/app/features/contacts/contacts.component.scss` | New | Contacts styles |
| `client/src/app/app.routes.ts` | Modified | Add `/contacts` route |
| `client/src/app/features/home/home.component.ts` | Modified | Add contacts nav link |

## Key design decisions

1. **`ErrNotFound` and `ErrAlreadyExists` live in `store` package** — handlers import them directly; no sentinel duplication.
2. **`PendingRequest` struct in `store` package** — embeds `model.Friendship` plus a `Requester model.User` field for a single-query JOIN response.
3. **Handler interfaces** (`FriendStore`, `FriendUserStore`) defined in `handler/friend.go` — keeps handler package independently testable with stubs.
4. **`UserStore` satisfies `FriendUserStore`** — `store.UserStore` already has `GetByID`; we add `Search` in Task 2. No new type needed.
5. **`BlockUser` upserts** — if a friendship row exists, it is updated (requester/addressee reoriented to the blocker); otherwise a new row is inserted.
6. **ListFriends includes both directions** — the `CASE WHEN` in the JOIN handles rows where the calling user is either requester or addressee.
7. **Angular Signals throughout** — `FriendService.friends` and `FriendService.pendingRequests` are writable signals so the Contacts component template reacts automatically.

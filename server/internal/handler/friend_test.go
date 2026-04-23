package handler_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	"im-server/internal/auth"
	"im-server/internal/handler"
	"im-server/internal/repo"
)

// errAlreadyExists matches the keyword the handler uses to detect a unique
// violation. The legacy store had a typed sentinel; the repo path surfaces a
// driver-style error string instead.
var errAlreadyExists = errors.New("duplicate key value violates unique constraint")

// ---------- in-memory stub FriendStore ----------

type stubFriendStore struct {
	friendships []repo.Friendship
	nextID      int64
}

func newStubFriendStore() *stubFriendStore {
	return &stubFriendStore{nextID: 1}
}

func (s *stubFriendStore) SendRequest(_ context.Context, requesterID, addresseeID int64) error {
	if requesterID == addresseeID {
		return errAlreadyExists
	}
	for _, f := range s.friendships {
		if (f.RequesterID == requesterID && f.AddresseeID == addresseeID) ||
			(f.RequesterID == addresseeID && f.AddresseeID == requesterID) {
			return errAlreadyExists
		}
	}
	s.friendships = append(s.friendships, repo.Friendship{
		ID:          s.nextID,
		RequesterID: requesterID,
		AddresseeID: addresseeID,
		Status:      repo.FriendshipPending,
	})
	s.nextID++
	return nil
}

func (s *stubFriendStore) AcceptRequest(_ context.Context, friendshipID, userID int64) error {
	for i := range s.friendships {
		f := &s.friendships[i]
		if f.ID == friendshipID && f.AddresseeID == userID && f.Status == repo.FriendshipPending {
			f.Status = repo.FriendshipAccepted
			return nil
		}
	}
	return repo.ErrNotFound
}

func (s *stubFriendStore) RejectRequest(_ context.Context, friendshipID, userID int64) error {
	for i := range s.friendships {
		f := &s.friendships[i]
		if f.ID == friendshipID && f.AddresseeID == userID && f.Status == repo.FriendshipPending {
			f.Status = repo.FriendshipRejected
			return nil
		}
	}
	return repo.ErrNotFound
}

func (s *stubFriendStore) ListFriends(_ context.Context, userID int64) ([]repo.User, error) {
	var friends []repo.User
	for _, f := range s.friendships {
		if f.Status != repo.FriendshipAccepted {
			continue
		}
		if f.RequesterID == userID {
			friends = append(friends, repo.User{ID: f.AddresseeID})
		} else if f.AddresseeID == userID {
			friends = append(friends, repo.User{ID: f.RequesterID})
		}
	}
	return friends, nil
}

func (s *stubFriendStore) ListPendingRequests(_ context.Context, userID int64) ([]repo.PendingRequest, error) {
	var result []repo.PendingRequest
	for _, f := range s.friendships {
		if f.AddresseeID == userID && f.Status == repo.FriendshipPending {
			result = append(result, repo.PendingRequest{
				Friendship: f,
				Requester:  repo.User{ID: f.RequesterID, Username: "requester"},
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
			f.Status = repo.FriendshipBlocked
			f.RequesterID = blockerID
			f.AddresseeID = blockedID
			return nil
		}
	}
	s.friendships = append(s.friendships, repo.Friendship{
		ID:          s.nextID,
		RequesterID: blockerID,
		AddresseeID: blockedID,
		Status:      repo.FriendshipBlocked,
	})
	s.nextID++
	return nil
}

// ---------- in-memory stub FriendUserStore ----------

type stubFriendUserStore struct {
	users map[int64]*repo.User
}

func newStubFriendUserStore() *stubFriendUserStore {
	return &stubFriendUserStore{users: map[int64]*repo.User{
		1: {ID: 1, Username: "alice", DisplayName: "Alice"},
		2: {ID: 2, Username: "bob", DisplayName: "Bob"},
	}}
}

func (s *stubFriendUserStore) GetByID(_ context.Context, id int64) (*repo.User, error) {
	u, ok := s.users[id]
	if !ok {
		return nil, handler.ErrNotFound
	}
	return u, nil
}

func (s *stubFriendUserStore) Search(_ context.Context, q string, callerID int64) ([]repo.User, error) {
	var result []repo.User
	for _, u := range s.users {
		if u.ID != callerID && (q == "" || strings.Contains(u.Username, q)) {
			result = append(result, *u)
		}
	}
	return result, nil
}

// ---------- test helpers ----------

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
	req = req.WithContext(context.WithValue(req.Context(), handler.ClaimsKey, claims))
	return req
}

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

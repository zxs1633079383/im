package service

import (
	"context"
	"errors"
	"strings"

	"im-server/internal/repo"
)

// ErrAlreadyExists is returned by FriendService.SendRequest when a friendship
// row between the two users already exists. The repo wraps the underlying
// driver error (Postgres unique-violation, code 23505) — we detect it by
// keyword to keep the service free of an SQL driver dependency.
var ErrAlreadyExists = errors.New("already exists")

// FriendService implements friend graph mutations and queries above
// repo.FriendshipRepo and repo.UserRepo.
//
// Like the other services in this package, validation (zero IDs, etc.) lives
// in the transport layer; this service only forwards to repos and translates
// driver-style errors into stable sentinels.
type FriendService struct {
	friends repo.FriendshipRepo
	users   repo.UserRepo
}

// NewFriendService constructs a FriendService backed by the supplied repos.
func NewFriendService(friends repo.FriendshipRepo, users repo.UserRepo) *FriendService {
	return &FriendService{friends: friends, users: users}
}

// SendRequest creates a pending friendship from requesterID to addresseeID.
// Returns ErrAlreadyExists when a row between the pair already exists
// (Postgres unique-violation). Other repo errors are returned as-is.
func (s *FriendService) SendRequest(ctx context.Context, requesterID, addresseeID int64) error {
	err := s.friends.SendRequest(ctx, requesterID, addresseeID)
	if err != nil && isAlreadyExistsErr(err) {
		return ErrAlreadyExists
	}
	return err
}

// AcceptRequest marks the pending friendship friendshipID accepted, but only
// if userID is the addressee. Returns repo.ErrNotFound otherwise (so the
// transport layer can map non-addressee callers to 404).
func (s *FriendService) AcceptRequest(ctx context.Context, friendshipID, userID int64) error {
	return s.friends.AcceptRequest(ctx, friendshipID, userID)
}

// RejectRequest mirrors AcceptRequest but flips the row to rejected.
func (s *FriendService) RejectRequest(ctx context.Context, friendshipID, userID int64) error {
	return s.friends.RejectRequest(ctx, friendshipID, userID)
}

// ListFriends returns the accepted-friendship counter-parties for userID.
func (s *FriendService) ListFriends(ctx context.Context, userID int64) ([]repo.User, error) {
	return s.friends.ListFriends(ctx, userID)
}

// ListPending returns inbound pending friendship rows for userID.
func (s *FriendService) ListPending(ctx context.Context, userID int64) ([]repo.PendingRequest, error) {
	return s.friends.ListPendingRequests(ctx, userID)
}

// BlockUser upserts a Blocked friendship row from blockerID to blockedID.
func (s *FriendService) BlockUser(ctx context.Context, blockerID, blockedID int64) error {
	return s.friends.BlockUser(ctx, blockerID, blockedID)
}

// SearchUsers proxies to repo.UserRepo.Search. The repo handles trimming and
// excludes the caller from results.
func (s *FriendService) SearchUsers(ctx context.Context, query string, callerID int64) ([]repo.User, error) {
	return s.users.Search(ctx, query, callerID)
}

// isAlreadyExistsErr matches Postgres unique-violation errors surfaced through
// GORM. Mirrors the legacy handler.isAlreadyExistsErr behaviour exactly.
func isAlreadyExistsErr(err error) bool {
	if err == nil {
		return false
	}
	msg := err.Error()
	return strings.Contains(msg, "duplicate") ||
		strings.Contains(msg, "unique") ||
		strings.Contains(msg, "23505")
}

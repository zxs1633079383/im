package service

import (
	"context"
	"errors"
	"strings"

	"im-server/internal/repo"
)

// ErrAlreadyExists is returned by FriendService.SendRequest when a friendship
// row between the two users already exists.
var ErrAlreadyExists = errors.New("already exists")

// FriendService implements friend graph mutations and queries above
// repo.FriendshipRepo. M4: callers identify users by mm UserID; profile
// lookups happen client-side via the cses Redis "User" hash.
type FriendService struct {
	friends repo.FriendshipRepo
}

// NewFriendService constructs a FriendService backed by the supplied repo.
func NewFriendService(friends repo.FriendshipRepo) *FriendService {
	return &FriendService{friends: friends}
}

// SendRequest creates a pending friendship from requesterID to addresseeID.
func (s *FriendService) SendRequest(ctx context.Context, requesterID, addresseeID string) error {
	ctx, span := tracer.Start(ctx, "FriendService.SendRequest")
	defer span.End()

	err := s.friends.SendRequest(ctx, requesterID, addresseeID)
	if err != nil && isAlreadyExistsErr(err) {
		return ErrAlreadyExists
	}
	return err
}

// AcceptRequest marks the pending friendship accepted, returning the
// requester's mm UserID for downstream push fan-out.
func (s *FriendService) AcceptRequest(ctx context.Context, friendshipID string, userID string) (string, error) {
	ctx, span := tracer.Start(ctx, "FriendService.AcceptRequest")
	defer span.End()

	return s.friends.AcceptRequest(ctx, friendshipID, userID)
}

// RejectRequest mirrors AcceptRequest but flips the row to rejected.
func (s *FriendService) RejectRequest(ctx context.Context, friendshipID string, userID string) (string, error) {
	ctx, span := tracer.Start(ctx, "FriendService.RejectRequest")
	defer span.End()

	return s.friends.RejectRequest(ctx, friendshipID, userID)
}

// ListFriends returns the mm UserIDs of accepted friends for userID.
func (s *FriendService) ListFriends(ctx context.Context, userID string) ([]string, error) {
	ctx, span := tracer.Start(ctx, "FriendService.ListFriends")
	defer span.End()

	return s.friends.ListFriends(ctx, userID)
}

// ListPending returns inbound pending friendship rows for userID.
func (s *FriendService) ListPending(ctx context.Context, userID string) ([]repo.PendingRequest, error) {
	ctx, span := tracer.Start(ctx, "FriendService.ListPending")
	defer span.End()

	return s.friends.ListPendingRequests(ctx, userID)
}

// BlockUser upserts a Blocked friendship row from blockerID to blockedID.
func (s *FriendService) BlockUser(ctx context.Context, blockerID, blockedID string) error {
	ctx, span := tracer.Start(ctx, "FriendService.BlockUser")
	defer span.End()

	return s.friends.BlockUser(ctx, blockerID, blockedID)
}

// isAlreadyExistsErr matches Postgres unique-violation errors surfaced through
// GORM.
func isAlreadyExistsErr(err error) bool {
	if err == nil {
		return false
	}
	msg := err.Error()
	return strings.Contains(msg, "duplicate") ||
		strings.Contains(msg, "unique") ||
		strings.Contains(msg, "23505")
}

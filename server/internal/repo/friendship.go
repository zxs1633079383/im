package repo

import (
	"context"
	"errors"

	"gorm.io/gorm"
)

// Friendship status values (mirror internal/model.FriendshipStatus).
const (
	FriendshipPending  int16 = 1
	FriendshipAccepted int16 = 2
	FriendshipRejected int16 = 3
	FriendshipBlocked  int16 = 4
)

// PendingRequest is a friendship in pending state. M4 collapses the joined
// User payload to a bare friendship row — front-ends fetch the requester's
// profile via the mm Redis lookup.
type PendingRequest struct {
	Friendship
}

// FriendshipRepo manages friend relationships.
//
// AcceptRequest / RejectRequest return the underlying friendship's
// requester mm UserID alongside the error so the transport layer can fire a
// real-time friend_event back to the requester without a second round-trip
// to the database. Empty string is returned on any error.
type FriendshipRepo interface {
	SendRequest(ctx context.Context, requesterID, addresseeID string) error
	AcceptRequest(ctx context.Context, friendshipID string, userID string) (requesterID string, err error)
	RejectRequest(ctx context.Context, friendshipID string, userID string) (requesterID string, err error)
	ListFriends(ctx context.Context, userID string) ([]string, error)
	ListPendingRequests(ctx context.Context, userID string) ([]PendingRequest, error)
	GetFriendship(ctx context.Context, userA, userB string) (*Friendship, error)
	BlockUser(ctx context.Context, blockerID, blockedID string) error
}

type gormFriendshipRepo struct{ db *gorm.DB }

// NewFriendshipRepo returns a GORM-backed FriendshipRepo.
func NewFriendshipRepo(db *gorm.DB) FriendshipRepo { return &gormFriendshipRepo{db: db} }

func (r *gormFriendshipRepo) SendRequest(ctx context.Context, requesterID, addresseeID string) error {
	if requesterID == addresseeID {
		return errors.New("cannot friend self")
	}
	f := &Friendship{
		RequesterID: requesterID,
		AddresseeID: addresseeID,
		Status:      FriendshipPending,
	}
	return r.db.WithContext(ctx).Create(f).Error
}

// AcceptRequest flips the pending friendship to Accepted and returns the
// row's requester mm UserID so the caller can push a real-time event back
// to the original sender.
func (r *gormFriendshipRepo) AcceptRequest(ctx context.Context, friendshipID string, userID string) (string, error) {
	return r.transitionPending(ctx, friendshipID, userID, FriendshipAccepted)
}

// RejectRequest mirrors AcceptRequest but transitions the row to Rejected.
func (r *gormFriendshipRepo) RejectRequest(ctx context.Context, friendshipID string, userID string) (string, error) {
	return r.transitionPending(ctx, friendshipID, userID, FriendshipRejected)
}

// transitionPending flips a Pending friendship row to the given terminal
// status, returning the row's requester mm UserID. Gated on addressee_id =
// userID so only the request's target can act. Returns ErrNotFound if no
// matching Pending row exists.
func (r *gormFriendshipRepo) transitionPending(ctx context.Context, friendshipID string, userID string, to int16) (string, error) {
	var f Friendship
	if err := r.db.WithContext(ctx).
		Where("id = ? AND addressee_id = ? AND status = ?", friendshipID, userID, FriendshipPending).
		First(&f).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return "", ErrNotFound
		}
		return "", err
	}
	res := r.db.WithContext(ctx).Model(&Friendship{}).
		Where("id = ? AND addressee_id = ? AND status = ?", friendshipID, userID, FriendshipPending).
		Update("status", to)
	if res.Error != nil {
		return "", res.Error
	}
	if res.RowsAffected == 0 {
		return "", ErrNotFound
	}
	return f.RequesterID, nil
}

// ListFriends returns the mm UserIDs of accepted friends for userID. M4:
// the returned slice is a list of identifiers; clients fetch profile data
// from the cses Redis directly.
func (r *gormFriendshipRepo) ListFriends(ctx context.Context, userID string) ([]string, error) {
	var ids []string
	err := r.db.WithContext(ctx).Raw(`
		SELECT CASE WHEN f.requester_id = ? THEN f.addressee_id ELSE f.requester_id END AS friend_id
		FROM friendships f
		WHERE (f.requester_id = ? OR f.addressee_id = ?) AND f.status = ?
		ORDER BY friend_id
	`, userID, userID, userID, FriendshipAccepted).Scan(&ids).Error
	return ids, err
}

func (r *gormFriendshipRepo) ListPendingRequests(ctx context.Context, userID string) ([]PendingRequest, error) {
	var out []PendingRequest
	err := r.db.WithContext(ctx).
		Where("addressee_id = ? AND status = ?", userID, FriendshipPending).
		Order("created_at DESC").
		Find(&out).Error
	return out, err
}

func (r *gormFriendshipRepo) GetFriendship(ctx context.Context, userA, userB string) (*Friendship, error) {
	var f Friendship
	err := r.db.WithContext(ctx).
		Where("(requester_id = ? AND addressee_id = ?) OR (requester_id = ? AND addressee_id = ?)",
			userA, userB, userB, userA).
		First(&f).Error
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, ErrNotFound
		}
		return nil, err
	}
	return &f, nil
}

func (r *gormFriendshipRepo) BlockUser(ctx context.Context, blockerID, blockedID string) error {
	if blockerID == blockedID {
		return errors.New("cannot block self")
	}
	existing, err := r.GetFriendship(ctx, blockerID, blockedID)
	if err != nil && !errors.Is(err, ErrNotFound) {
		return err
	}
	if existing != nil {
		return r.db.WithContext(ctx).Model(&Friendship{}).
			Where("id = ?", existing.ID).
			Update("status", FriendshipBlocked).Error
	}
	return r.db.WithContext(ctx).Create(&Friendship{
		RequesterID: blockerID,
		AddresseeID: blockedID,
		Status:      FriendshipBlocked,
	}).Error
}

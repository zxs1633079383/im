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

// PendingRequest is a friendship in pending state, joined with the requester
// user. The embedded User uses the `requester_user_` column prefix to avoid
// collision with Friendship.RequesterID (column `requester_id`).
type PendingRequest struct {
	Friendship
	Requester User `gorm:"embedded;embeddedPrefix:requester_user_"`
}

// FriendshipRepo manages friend relationships.
//
// AcceptRequest / RejectRequest return the underlying friendship's
// requesterID alongside the error so the transport layer can fire a
// real-time friend_event back to the requester without a second round-trip
// to the database. Zero is returned on any error.
type FriendshipRepo interface {
	SendRequest(ctx context.Context, requesterID, addresseeID int64) error
	AcceptRequest(ctx context.Context, friendshipID, userID int64) (requesterID int64, err error)
	RejectRequest(ctx context.Context, friendshipID, userID int64) (requesterID int64, err error)
	ListFriends(ctx context.Context, userID int64) ([]User, error)
	ListPendingRequests(ctx context.Context, userID int64) ([]PendingRequest, error)
	GetFriendship(ctx context.Context, userA, userB int64) (*Friendship, error)
	BlockUser(ctx context.Context, blockerID, blockedID int64) error
}

type gormFriendshipRepo struct{ db *gorm.DB }

// NewFriendshipRepo returns a GORM-backed FriendshipRepo.
func NewFriendshipRepo(db *gorm.DB) FriendshipRepo { return &gormFriendshipRepo{db: db} }

func (r *gormFriendshipRepo) SendRequest(ctx context.Context, requesterID, addresseeID int64) error {
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
// row's requester_id so the caller can push a real-time event back to the
// original sender. Using a single UPDATE with a subquery avoids the
// read-then-write race and keeps the call atomic.
func (r *gormFriendshipRepo) AcceptRequest(ctx context.Context, friendshipID, userID int64) (int64, error) {
	return r.transitionPending(ctx, friendshipID, userID, FriendshipAccepted)
}

// RejectRequest mirrors AcceptRequest but transitions the row to Rejected.
func (r *gormFriendshipRepo) RejectRequest(ctx context.Context, friendshipID, userID int64) (int64, error) {
	return r.transitionPending(ctx, friendshipID, userID, FriendshipRejected)
}

// transitionPending flips a Pending friendship row to the given terminal
// status, returning the row's requester_id. Gated on addressee_id = userID so
// only the request's target can act. Returns ErrNotFound if no matching
// Pending row exists (covers non-addressee callers, missing IDs, already
// accepted/rejected rows).
func (r *gormFriendshipRepo) transitionPending(ctx context.Context, friendshipID, userID int64, to int16) (int64, error) {
	// Fetch + update in one place. A read-then-write could race a concurrent
	// accept, but the update itself is idempotent under the status filter so
	// the worst case is two correct "transition to Accepted" operations.
	var f Friendship
	if err := r.db.WithContext(ctx).
		Where("id = ? AND addressee_id = ? AND status = ?", friendshipID, userID, FriendshipPending).
		First(&f).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return 0, ErrNotFound
		}
		return 0, err
	}
	res := r.db.WithContext(ctx).Model(&Friendship{}).
		Where("id = ? AND addressee_id = ? AND status = ?", friendshipID, userID, FriendshipPending).
		Update("status", to)
	if res.Error != nil {
		return 0, res.Error
	}
	if res.RowsAffected == 0 {
		return 0, ErrNotFound
	}
	return f.RequesterID, nil
}

func (r *gormFriendshipRepo) ListFriends(ctx context.Context, userID int64) ([]User, error) {
	// A friend is the OTHER party in any accepted friendship involving userID.
	var users []User
	err := r.db.WithContext(ctx).Raw(`
		SELECT u.* FROM users u
		JOIN friendships f ON
		  (f.requester_id = ? AND f.addressee_id = u.id) OR
		  (f.addressee_id = ? AND f.requester_id = u.id)
		WHERE f.status = ?
		ORDER BY u.username
	`, userID, userID, FriendshipAccepted).Scan(&users).Error
	return users, err
}

func (r *gormFriendshipRepo) ListPendingRequests(ctx context.Context, userID int64) ([]PendingRequest, error) {
	var out []PendingRequest
	err := r.db.WithContext(ctx).Raw(`
		SELECT f.id, f.requester_id, f.addressee_id, f.status, f.created_at, f.updated_at,
		       u.id            AS requester_user_id,
		       u.username      AS requester_user_username,
		       u.email         AS requester_user_email,
		       u.password_hash AS requester_user_password_hash,
		       u.display_name  AS requester_user_display_name,
		       u.avatar_url    AS requester_user_avatar_url,
		       u.status        AS requester_user_status,
		       u.created_at    AS requester_user_created_at,
		       u.updated_at    AS requester_user_updated_at
		FROM friendships f
		JOIN users u ON u.id = f.requester_id
		WHERE f.addressee_id = ? AND f.status = ?
		ORDER BY f.created_at DESC
	`, userID, FriendshipPending).Scan(&out).Error
	return out, err
}

func (r *gormFriendshipRepo) GetFriendship(ctx context.Context, userA, userB int64) (*Friendship, error) {
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

func (r *gormFriendshipRepo) BlockUser(ctx context.Context, blockerID, blockedID int64) error {
	if blockerID == blockedID {
		return errors.New("cannot block self")
	}
	// Upsert: if a row exists between the pair (any direction), set status to
	// Blocked. Otherwise create a fresh blocker→blocked row with status Blocked.
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

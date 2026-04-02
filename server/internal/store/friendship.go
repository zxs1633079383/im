package store

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
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

// isUniqueViolation detects PG unique constraint errors (error code 23505).
func isUniqueViolation(err error) bool {
	var pgErr *pgconn.PgError
	return errors.As(err, &pgErr) && pgErr.Code == "23505"
}

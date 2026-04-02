package store

import (
	"context"
	"fmt"

	"github.com/jackc/pgx/v5/pgxpool"
	"im-server/internal/model"
)

type UserStore struct {
	pool *pgxpool.Pool
}

func NewUserStore(pool *pgxpool.Pool) *UserStore {
	return &UserStore{pool: pool}
}

func (s *UserStore) Create(ctx context.Context, u *model.User) error {
	err := s.pool.QueryRow(ctx,
		`INSERT INTO users (username, email, password_hash, display_name, avatar_url)
		 VALUES ($1, $2, $3, $4, $5)
		 RETURNING id, status, created_at, updated_at`,
		u.Username, u.Email, u.PasswordHash, u.DisplayName, u.AvatarURL,
	).Scan(&u.ID, &u.Status, &u.CreatedAt, &u.UpdatedAt)
	if err != nil {
		return fmt.Errorf("insert user: %w", err)
	}
	return nil
}

func (s *UserStore) GetByID(ctx context.Context, id int64) (*model.User, error) {
	u := &model.User{}
	err := s.pool.QueryRow(ctx,
		`SELECT id, username, email, password_hash, display_name, avatar_url, status, created_at, updated_at
		 FROM users WHERE id = $1`, id,
	).Scan(&u.ID, &u.Username, &u.Email, &u.PasswordHash, &u.DisplayName, &u.AvatarURL, &u.Status, &u.CreatedAt, &u.UpdatedAt)
	if err != nil {
		return nil, fmt.Errorf("get user by id: %w", err)
	}
	return u, nil
}

func (s *UserStore) GetByUsername(ctx context.Context, username string) (*model.User, error) {
	u := &model.User{}
	err := s.pool.QueryRow(ctx,
		`SELECT id, username, email, password_hash, display_name, avatar_url, status, created_at, updated_at
		 FROM users WHERE username = $1`, username,
	).Scan(&u.ID, &u.Username, &u.Email, &u.PasswordHash, &u.DisplayName, &u.AvatarURL, &u.Status, &u.CreatedAt, &u.UpdatedAt)
	if err != nil {
		return nil, fmt.Errorf("get user by username: %w", err)
	}
	return u, nil
}

func (s *UserStore) GetByEmail(ctx context.Context, email string) (*model.User, error) {
	u := &model.User{}
	err := s.pool.QueryRow(ctx,
		`SELECT id, username, email, password_hash, display_name, avatar_url, status, created_at, updated_at
		 FROM users WHERE email = $1`, email,
	).Scan(&u.ID, &u.Username, &u.Email, &u.PasswordHash, &u.DisplayName, &u.AvatarURL, &u.Status, &u.CreatedAt, &u.UpdatedAt)
	if err != nil {
		return nil, fmt.Errorf("get user by email: %w", err)
	}
	return u, nil
}

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

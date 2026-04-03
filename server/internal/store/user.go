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

// UpdateProfile updates the display_name and avatar_url for a user.
// Only non-empty fields are changed.
func (s *UserStore) UpdateProfile(ctx context.Context, userID int64, displayName, avatarURL string) (*model.User, error) {
	u := &model.User{}
	err := s.pool.QueryRow(ctx,
		`UPDATE users
		 SET display_name = CASE WHEN $2 != '' THEN $2 ELSE display_name END,
		     avatar_url   = CASE WHEN $3 != '' THEN $3 ELSE avatar_url   END,
		     updated_at   = now()
		 WHERE id = $1
		 RETURNING id, username, email, display_name, avatar_url, status, created_at, updated_at`,
		userID, displayName, avatarURL,
	).Scan(&u.ID, &u.Username, &u.Email, &u.DisplayName, &u.AvatarURL,
		&u.Status, &u.CreatedAt, &u.UpdatedAt)
	if err != nil {
		return nil, fmt.Errorf("update profile: %w", err)
	}
	return u, nil
}

// GetSettings returns the settings row for a user. If none exists, returns
// a default settings object (not persisted until UpsertSettings is called).
func (s *UserStore) GetSettings(ctx context.Context, userID int64) (*model.UserSettings, error) {
	settings := &model.UserSettings{
		UserID:              userID,
		NotificationEnabled: true,
		Theme:               "system",
		Language:            "en",
	}
	err := s.pool.QueryRow(ctx,
		`SELECT user_id, notification_enabled, theme, language, settings_json
		 FROM user_settings WHERE user_id = $1`, userID,
	).Scan(&settings.UserID, &settings.NotificationEnabled, &settings.Theme,
		&settings.Language, &settings.SettingsJSON)
	if err != nil {
		// Not found → return defaults without error
		if isNotFound(err) {
			return settings, nil
		}
		return nil, fmt.Errorf("get settings: %w", err)
	}
	return settings, nil
}

// UpsertSettings creates or updates the user_settings row.
func (s *UserStore) UpsertSettings(ctx context.Context, settings *model.UserSettings) error {
	_, err := s.pool.Exec(ctx,
		`INSERT INTO user_settings (user_id, notification_enabled, theme, language, settings_json)
		 VALUES ($1, $2, $3, $4, $5)
		 ON CONFLICT (user_id) DO UPDATE
		   SET notification_enabled = EXCLUDED.notification_enabled,
		       theme                = EXCLUDED.theme,
		       language             = EXCLUDED.language,
		       settings_json        = EXCLUDED.settings_json`,
		settings.UserID, settings.NotificationEnabled, settings.Theme,
		settings.Language, settings.SettingsJSON,
	)
	if err != nil {
		return fmt.Errorf("upsert settings: %w", err)
	}
	return nil
}

// isNotFound returns true when pgx returns pgx.ErrNoRows.
func isNotFound(err error) bool {
	return err != nil && err.Error() == "no rows in result set"
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

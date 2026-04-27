package repo

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"gorm.io/gorm"
)

// UserRepo is the persistence interface for users.
// Implementations are safe for concurrent use.
type UserRepo interface {
	Create(ctx context.Context, u *User) error
	GetByID(ctx context.Context, id int64) (*User, error)
	GetByUsername(ctx context.Context, username string) (*User, error)
	GetByEmail(ctx context.Context, email string) (*User, error)
	UpdateProfile(ctx context.Context, userID int64, displayName, avatarURL string) (*User, error)
	Search(ctx context.Context, query string, callerID int64) ([]User, error)
	// UpsertByMattermostID returns (or lazily creates) the im users row that
	// shadows the supplied Mattermost user. Used by MattermostCookieAuth so
	// cookie-only callers get a stable im int64 user_id without needing a
	// separate /api/auth/register step. The cses Java service remains the
	// source of truth for user metadata; this row is just an int64-PK shim.
	UpsertByMattermostID(ctx context.Context, params MattermostUpsertParams) (*User, error)
}

// MattermostUpsertParams carries the minimum the upsert needs so callers can
// pass it from the cookie middleware without leaking the full middleware
// type into the repo layer.
type MattermostUpsertParams struct {
	MattermostUserID string // required, the upstream UUID
	Username         string // mm UserName, used for unique-collision-safe local username
	Email            string // optional
	DisplayName      string // optional
}

type gormUserRepo struct{ db *gorm.DB }

// NewUserRepo returns a GORM-backed UserRepo.
func NewUserRepo(db *gorm.DB) UserRepo { return &gormUserRepo{db: db} }

func (r *gormUserRepo) Create(ctx context.Context, u *User) error {
	return r.db.WithContext(ctx).Create(u).Error
}

func (r *gormUserRepo) GetByID(ctx context.Context, id int64) (*User, error) {
	var u User
	if err := r.db.WithContext(ctx).First(&u, id).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, ErrNotFound
		}
		return nil, err
	}
	return &u, nil
}

func (r *gormUserRepo) GetByUsername(ctx context.Context, username string) (*User, error) {
	var u User
	if err := r.db.WithContext(ctx).Where("username = ?", username).First(&u).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, ErrNotFound
		}
		return nil, err
	}
	return &u, nil
}

func (r *gormUserRepo) GetByEmail(ctx context.Context, email string) (*User, error) {
	var u User
	if err := r.db.WithContext(ctx).Where("email = ?", email).First(&u).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, ErrNotFound
		}
		return nil, err
	}
	return &u, nil
}

func (r *gormUserRepo) UpdateProfile(ctx context.Context, userID int64, displayName, avatarURL string) (*User, error) {
	res := r.db.WithContext(ctx).Model(&User{}).Where("id = ?", userID).
		Updates(map[string]any{"display_name": displayName, "avatar_url": avatarURL})
	if res.Error != nil {
		return nil, res.Error
	}
	if res.RowsAffected == 0 {
		return nil, ErrNotFound
	}
	return r.GetByID(ctx, userID)
}

// UpsertByMattermostID returns the existing im users row mapped to params.MattermostUserID,
// or creates one when no shadow row exists yet. Concurrency: the unique
// partial index on mm_user_id makes the INSERT race-safe — losers fall
// through the SELECT branch on retry.
//
// Username collision: callers from cses may already use a name held by a
// JWT-native im user. We disambiguate by appending the last 8 chars of the
// mm UUID (`<name>-<short>`), which is good enough for log identification
// without trying to "merge" two unrelated accounts.
func (r *gormUserRepo) UpsertByMattermostID(ctx context.Context, params MattermostUpsertParams) (*User, error) {
	if params.MattermostUserID == "" {
		return nil, fmt.Errorf("upsert by mm id: empty MattermostUserID")
	}

	// Fast path: shadow row already exists.
	var existing User
	err := r.db.WithContext(ctx).
		Where("mm_user_id = ?", params.MattermostUserID).
		First(&existing).Error
	if err == nil {
		return &existing, nil
	}
	if !errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, fmt.Errorf("upsert by mm id (lookup): %w", err)
	}

	// First-seen: create the shadow row. Username + email use mm-derived
	// values with the short suffix so they survive uniqueness checks even
	// when an unrelated im user already holds the bare name.
	created, err := r.insertMattermostShadow(ctx, params)
	if err == nil {
		return created, nil
	}
	// A concurrent UpsertByMattermostID won the INSERT race — re-read.
	var afterRace User
	if reErr := r.db.WithContext(ctx).
		Where("mm_user_id = ?", params.MattermostUserID).
		First(&afterRace).Error; reErr == nil {
		return &afterRace, nil
	}
	return nil, fmt.Errorf("upsert by mm id (insert): %w", err)
}

// insertMattermostShadow does the INSERT half of the upsert. Extracted so
// UpsertByMattermostID stays under the 60-line cap.
func (r *gormUserRepo) insertMattermostShadow(ctx context.Context, params MattermostUpsertParams) (*User, error) {
	mmID := params.MattermostUserID
	short := mmID
	if len(short) > 8 {
		short = short[len(short)-8:]
	}
	username := mmShadowUsername(params.Username, short)
	email := params.Email
	if email == "" {
		email = "mm-" + mmID + "@cses.local"
	}
	display := params.DisplayName
	if display == "" {
		display = params.Username
	}
	mmIDPtr := mmID
	row := User{
		Username:         username,
		Email:            email,
		PasswordHash:     "mm-shadow", // never used — cookie auth bypasses password
		DisplayName:      display,
		Status:           UserStatusActive,
		MattermostUserID: &mmIDPtr,
	}
	if err := r.db.WithContext(ctx).Create(&row).Error; err != nil {
		return nil, err
	}
	return &row, nil
}

// mmShadowUsername builds a unique-collision-safe local username. Callers
// pass the upstream mm name (which may collide with an im-native user) and
// a short id suffix that disambiguates.
func mmShadowUsername(name, short string) string {
	base := strings.TrimSpace(name)
	if base == "" {
		base = "mm"
	}
	candidate := fmt.Sprintf("%s-%s", base, short)
	if len(candidate) > 50 {
		candidate = candidate[:50]
	}
	return candidate
}

func (r *gormUserRepo) Search(ctx context.Context, query string, callerID int64) ([]User, error) {
	q := strings.TrimSpace(query)
	if q == "" {
		return nil, nil
	}
	var users []User
	err := r.db.WithContext(ctx).
		Where("username ILIKE ? AND id <> ?", "%"+q+"%", callerID).
		Order("username").
		Limit(50).
		Find(&users).Error
	return users, err
}

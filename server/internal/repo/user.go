package repo

import (
	"context"
	"errors"
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

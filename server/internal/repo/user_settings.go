package repo

import (
	"context"
	"errors"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// UserSettingsRepo persists per-user preferences.
type UserSettingsRepo interface {
	Get(ctx context.Context, userID string) (*UserSettings, error)
	Upsert(ctx context.Context, s *UserSettings) error
}

type gormUserSettingsRepo struct{ db *gorm.DB }

// NewUserSettingsRepo returns a GORM-backed UserSettingsRepo.
func NewUserSettingsRepo(db *gorm.DB) UserSettingsRepo {
	return &gormUserSettingsRepo{db: db}
}

func (r *gormUserSettingsRepo) Get(ctx context.Context, userID string) (*UserSettings, error) {
	var s UserSettings
	if err := r.db.WithContext(ctx).First(&s, "user_id = ?", userID).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, ErrNotFound
		}
		return nil, err
	}
	return &s, nil
}

func (r *gormUserSettingsRepo) Upsert(ctx context.Context, s *UserSettings) error {
	if s == nil || s.UserID == "" {
		return errors.New("user_settings: user_id required")
	}
	return r.db.WithContext(ctx).Clauses(clause.OnConflict{
		Columns: []clause.Column{{Name: "user_id"}},
		DoUpdates: clause.Assignments(map[string]any{
			"notification_enabled": s.NotificationEnabled,
			"theme":                s.Theme,
			"language":             s.Language,
			"settings_json":        s.SettingsJSON,
			"updated_at":           gorm.Expr("now()"),
		}),
	}).Create(s).Error
}

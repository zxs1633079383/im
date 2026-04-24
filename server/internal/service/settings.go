package service

import (
	"context"
	"errors"

	"im-server/internal/repo"
)

// SettingsService reads and writes per-user preferences above
// repo.UserSettingsRepo.
//
// Like the legacy net/http handler, Get falls back to a default settings
// shape when no row exists for the user — callers therefore always observe
// a usable response without dealing with ErrNotFound.
type SettingsService struct {
	settings repo.UserSettingsRepo
}

// NewSettingsService constructs a SettingsService backed by the supplied
// UserSettingsRepo.
func NewSettingsService(s repo.UserSettingsRepo) *SettingsService {
	return &SettingsService{settings: s}
}

// Get returns the user's settings row, or a default shape when no row exists.
// The defaults mirror the legacy handler's defaultSettings() helper exactly:
// Theme="system", Language="zh", NotificationEnabled=true, SettingsJSON="{}".
func (s *SettingsService) Get(ctx context.Context, userID int64) (*repo.UserSettings, error) {
	ctx, span := tracer.Start(ctx, "SettingsService.Get")
	defer span.End()

	got, err := s.settings.Get(ctx, userID)
	if err == nil {
		return got, nil
	}
	if errors.Is(err, repo.ErrNotFound) {
		return defaultSettings(userID), nil
	}
	return nil, err
}

// Update persists settings via UserSettingsRepo.Upsert. The caller is
// responsible for populating UserID and any unchanged fields.
func (s *SettingsService) Update(ctx context.Context, settings *repo.UserSettings) error {
	ctx, span := tracer.Start(ctx, "SettingsService.Update")
	defer span.End()

	return s.settings.Upsert(ctx, settings)
}

// defaultSettings returns the runtime fallback shape used when no row exists
// yet. Mirrors the legacy handler.defaultSettings — Theme="system" and
// Language="zh" are deliberately different from the GORM column defaults
// (light / zh-CN) and must be preserved for client compatibility.
func defaultSettings(userID int64) *repo.UserSettings {
	return &repo.UserSettings{
		UserID:              userID,
		NotificationEnabled: true,
		Theme:               "system",
		Language:            "zh",
		SettingsJSON:        "{}",
	}
}

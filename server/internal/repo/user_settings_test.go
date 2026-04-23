//go:build integration

package repo

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"
	"im-server/internal/testutil/containers"
)

func newSettingsRepo(t *testing.T) (UserSettingsRepo, UserRepo, context.Context) {
	dsn := containers.StartPostgres(t)
	db, err := Open(Config{DSN: dsn})
	require.NoError(t, err)
	return NewUserSettingsRepo(db), NewUserRepo(db), context.Background()
}

func TestUserSettings_GetMissing(t *testing.T) {
	r, _, ctx := newSettingsRepo(t)
	_, err := r.Get(ctx, 1)
	require.ErrorIs(t, err, ErrNotFound)
}

func TestUserSettings_UpsertInsertThenUpdate(t *testing.T) {
	settings, users, ctx := newSettingsRepo(t)
	u := &User{Username: "u", Email: "u@x.com", PasswordHash: "h", Status: 1}
	require.NoError(t, users.Create(ctx, u))

	// insert
	s := &UserSettings{
		UserID:              u.ID,
		NotificationEnabled: true,
		Theme:               "dark",
		Language:            "en-US",
		SettingsJSON:        `{"k":"v"}`,
	}
	require.NoError(t, settings.Upsert(ctx, s))

	got, err := settings.Get(ctx, u.ID)
	require.NoError(t, err)
	require.Equal(t, "dark", got.Theme)
	require.Equal(t, "en-US", got.Language)
	require.True(t, got.NotificationEnabled)

	// update
	s.Theme = "light"
	s.NotificationEnabled = false
	require.NoError(t, settings.Upsert(ctx, s))

	got2, err := settings.Get(ctx, u.ID)
	require.NoError(t, err)
	require.Equal(t, "light", got2.Theme)
	require.False(t, got2.NotificationEnabled)
}

func TestUserSettings_Upsert_ZeroUserID_Error(t *testing.T) {
	r, _, ctx := newSettingsRepo(t)
	err := r.Upsert(ctx, &UserSettings{UserID: 0})
	require.Error(t, err)
}

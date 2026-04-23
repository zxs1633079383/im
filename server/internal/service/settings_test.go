package service_test

import (
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"

	"im-server/internal/repo"
	"im-server/internal/repo/mocks"
	"im-server/internal/service"
)

func TestSettings_Get_RowExists(t *testing.T) {
	m := mocks.NewUserSettingsRepoMock(t)
	stored := &repo.UserSettings{
		UserID:              1,
		NotificationEnabled: false,
		Theme:               "dark",
		Language:            "en",
		SettingsJSON:        `{"k":"v"}`,
	}
	m.EXPECT().Get(mock.Anything, int64(1)).Return(stored, nil)

	svc := service.NewSettingsService(m)
	got, err := svc.Get(context.Background(), 1)
	require.NoError(t, err)
	require.Same(t, stored, got)
}

func TestSettings_Get_NotFound_ReturnsDefaults(t *testing.T) {
	m := mocks.NewUserSettingsRepoMock(t)
	m.EXPECT().Get(mock.Anything, int64(7)).Return(nil, repo.ErrNotFound)

	svc := service.NewSettingsService(m)
	got, err := svc.Get(context.Background(), 7)
	require.NoError(t, err)
	require.NotNil(t, got)
	// Defaults must match the legacy handler.defaultSettings exactly.
	require.Equal(t, int64(7), got.UserID)
	require.True(t, got.NotificationEnabled)
	require.Equal(t, "system", got.Theme)
	require.Equal(t, "zh", got.Language)
	require.Equal(t, "{}", got.SettingsJSON)
}

func TestSettings_Get_RepoError(t *testing.T) {
	m := mocks.NewUserSettingsRepoMock(t)
	boom := errors.New("db down")
	m.EXPECT().Get(mock.Anything, int64(1)).Return(nil, boom)

	svc := service.NewSettingsService(m)
	_, err := svc.Get(context.Background(), 1)
	require.ErrorIs(t, err, boom)
}

func TestSettings_Update_CallsUpsert(t *testing.T) {
	m := mocks.NewUserSettingsRepoMock(t)
	in := &repo.UserSettings{UserID: 1, Theme: "light", Language: "zh", SettingsJSON: "{}", NotificationEnabled: true}
	m.EXPECT().Upsert(mock.Anything, in).Return(nil)

	svc := service.NewSettingsService(m)
	require.NoError(t, svc.Update(context.Background(), in))
}

func TestSettings_Update_RepoError(t *testing.T) {
	m := mocks.NewUserSettingsRepoMock(t)
	boom := errors.New("db down")
	m.EXPECT().Upsert(mock.Anything, mock.Anything).Return(boom)

	svc := service.NewSettingsService(m)
	err := svc.Update(context.Background(), &repo.UserSettings{UserID: 1})
	require.ErrorIs(t, err, boom)
}

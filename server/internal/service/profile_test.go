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

func TestProfile_UpdateProfile_Success(t *testing.T) {
	m := mocks.NewUserRepoMock(t)
	want := &repo.User{ID: 1, Username: "alice", DisplayName: "Alice Updated", AvatarURL: "https://x/y.png"}
	m.EXPECT().UpdateProfile(mock.Anything, int64(1), "Alice Updated", "https://x/y.png").
		Return(want, nil)

	svc := service.NewProfileService(m)
	got, err := svc.UpdateProfile(context.Background(), 1, "Alice Updated", "https://x/y.png")
	require.NoError(t, err)
	require.Same(t, want, got)
}

func TestProfile_UpdateProfile_NotFound(t *testing.T) {
	m := mocks.NewUserRepoMock(t)
	m.EXPECT().UpdateProfile(mock.Anything, int64(99), "x", "").
		Return(nil, repo.ErrNotFound)

	svc := service.NewProfileService(m)
	_, err := svc.UpdateProfile(context.Background(), 99, "x", "")
	require.ErrorIs(t, err, repo.ErrNotFound)
}

func TestProfile_UpdateProfile_RepoError(t *testing.T) {
	m := mocks.NewUserRepoMock(t)
	boom := errors.New("db down")
	m.EXPECT().UpdateProfile(mock.Anything, int64(1), "", "").Return(nil, boom)

	svc := service.NewProfileService(m)
	_, err := svc.UpdateProfile(context.Background(), 1, "", "")
	require.ErrorIs(t, err, boom)
}

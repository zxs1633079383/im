package service_test

import (
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"
	"golang.org/x/crypto/bcrypt"

	"im-server/internal/repo"
	"im-server/internal/repo/mocks"
	"im-server/internal/service"
)

const testSecret = "test-secret-32-bytes-long-enough!"

func TestAuth_Register_Success(t *testing.T) {
	m := mocks.NewUserRepoMock(t)
	m.EXPECT().GetByUsername(mock.Anything, "alice").Return(nil, repo.ErrNotFound)
	m.EXPECT().GetByEmail(mock.Anything, "a@x.com").Return(nil, repo.ErrNotFound)
	m.EXPECT().Create(mock.Anything, mock.Anything).
		Run(func(_ context.Context, u *repo.User) { u.ID = 11 }).Return(nil)

	svc := service.NewAuthService(m, testSecret)
	u, tok, err := svc.Register(context.Background(), "alice", "a@x.com", "pwd12345", "")
	require.NoError(t, err)
	require.NotNil(t, u)
	require.Equal(t, int64(11), u.ID)
	require.Equal(t, "alice", u.Username)
	require.Equal(t, "alice", u.DisplayName, "displayName defaults to username when empty")
	require.NotEmpty(t, tok)
	require.NotEqual(t, "pwd12345", u.PasswordHash, "password must be hashed")
}

func TestAuth_Register_DuplicateUsername(t *testing.T) {
	m := mocks.NewUserRepoMock(t)
	m.EXPECT().GetByUsername(mock.Anything, "alice").
		Return(&repo.User{ID: 1, Username: "alice"}, nil)

	svc := service.NewAuthService(m, testSecret)
	_, _, err := svc.Register(context.Background(), "alice", "a@x.com", "pwd12345", "Alice")
	require.ErrorIs(t, err, service.ErrUserExists)
}

func TestAuth_Register_DuplicateEmail(t *testing.T) {
	m := mocks.NewUserRepoMock(t)
	m.EXPECT().GetByUsername(mock.Anything, "alice").Return(nil, repo.ErrNotFound)
	m.EXPECT().GetByEmail(mock.Anything, "a@x.com").
		Return(&repo.User{ID: 2, Email: "a@x.com"}, nil)

	svc := service.NewAuthService(m, testSecret)
	_, _, err := svc.Register(context.Background(), "alice", "a@x.com", "pwd12345", "Alice")
	require.ErrorIs(t, err, service.ErrUserExists)
}

func TestAuth_Register_RepoError(t *testing.T) {
	m := mocks.NewUserRepoMock(t)
	boom := errors.New("db down")
	m.EXPECT().GetByUsername(mock.Anything, "alice").Return(nil, boom)

	svc := service.NewAuthService(m, testSecret)
	_, _, err := svc.Register(context.Background(), "alice", "a@x.com", "pwd12345", "")
	require.ErrorIs(t, err, boom)
}

func TestAuth_Login_OK(t *testing.T) {
	hash, err := bcrypt.GenerateFromPassword([]byte("pwd12345"), bcrypt.MinCost)
	require.NoError(t, err)

	m := mocks.NewUserRepoMock(t)
	m.EXPECT().GetByUsername(mock.Anything, "alice").
		Return(&repo.User{ID: 7, Username: "alice", PasswordHash: string(hash)}, nil)

	svc := service.NewAuthService(m, testSecret)
	u, tok, err := svc.Login(context.Background(), "alice", "pwd12345")
	require.NoError(t, err)
	require.Equal(t, int64(7), u.ID)
	require.NotEmpty(t, tok)
}

func TestAuth_Login_BadPassword(t *testing.T) {
	hash, err := bcrypt.GenerateFromPassword([]byte("pwd12345"), bcrypt.MinCost)
	require.NoError(t, err)

	m := mocks.NewUserRepoMock(t)
	m.EXPECT().GetByUsername(mock.Anything, "alice").
		Return(&repo.User{ID: 7, Username: "alice", PasswordHash: string(hash)}, nil)

	svc := service.NewAuthService(m, testSecret)
	_, _, err = svc.Login(context.Background(), "alice", "nope")
	require.ErrorIs(t, err, service.ErrBadCreds)
}

func TestAuth_Login_NoUser(t *testing.T) {
	m := mocks.NewUserRepoMock(t)
	m.EXPECT().GetByUsername(mock.Anything, "ghost").Return(nil, repo.ErrNotFound)

	svc := service.NewAuthService(m, testSecret)
	_, _, err := svc.Login(context.Background(), "ghost", "pwd12345")
	require.ErrorIs(t, err, service.ErrBadCreds)
}

func TestAuth_Login_ByEmail(t *testing.T) {
	hash, err := bcrypt.GenerateFromPassword([]byte("pwd12345"), bcrypt.MinCost)
	require.NoError(t, err)

	m := mocks.NewUserRepoMock(t)
	// IMPORTANT: only GetByEmail should fire when login contains '@'.
	m.EXPECT().GetByEmail(mock.Anything, "a@x.com").
		Return(&repo.User{ID: 9, Username: "alice", PasswordHash: string(hash)}, nil)

	svc := service.NewAuthService(m, testSecret)
	u, tok, err := svc.Login(context.Background(), "a@x.com", "pwd12345")
	require.NoError(t, err)
	require.Equal(t, int64(9), u.ID)
	require.NotEmpty(t, tok)
}

func TestAuth_Login_EmptyLogin(t *testing.T) {
	m := mocks.NewUserRepoMock(t)
	// No EXPECT calls — empty login must short-circuit before hitting the repo.

	svc := service.NewAuthService(m, testSecret)
	_, _, err := svc.Login(context.Background(), "   ", "pwd12345")
	require.ErrorIs(t, err, service.ErrBadCreds)
}

func TestAuth_Login_EmptyPassword(t *testing.T) {
	m := mocks.NewUserRepoMock(t)
	// No EXPECT calls — empty password must short-circuit before hitting the repo.

	svc := service.NewAuthService(m, testSecret)
	_, _, err := svc.Login(context.Background(), "alice", "")
	require.ErrorIs(t, err, service.ErrBadCreds)
}

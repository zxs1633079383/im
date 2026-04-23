package http_test

import (
	"context"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"
	"golang.org/x/crypto/bcrypt"

	"im-server/internal/auth"
	imhttp "im-server/internal/http"
	"im-server/internal/repo"
	"im-server/internal/repo/mocks"
	"im-server/internal/service"
	"im-server/internal/testutil"
)

const testSecret = "test-secret-32-bytes-long-enough!"

func setupAuthHandler(t *testing.T) (*gin.Engine, *mocks.UserRepoMock) {
	t.Helper()
	gin.SetMode(gin.TestMode)
	m := mocks.NewUserRepoMock(t)
	svc := service.NewAuthService(m, testSecret)
	r := gin.New()
	imhttp.RegisterAuthRoutes(r, svc, m, testSecret)
	return r, m
}

func TestAuthHandler_Register_201(t *testing.T) {
	r, m := setupAuthHandler(t)
	m.EXPECT().GetByUsername(mock.Anything, "alice").Return(nil, repo.ErrNotFound)
	m.EXPECT().GetByEmail(mock.Anything, "a@x.com").Return(nil, repo.ErrNotFound)
	m.EXPECT().Create(mock.Anything, mock.Anything).
		Run(func(_ context.Context, u *repo.User) { u.ID = 1 }).Return(nil)

	testutil.NewExpect(t, r).POST("/api/auth/register").
		WithJSON(map[string]string{
			"username": "alice",
			"email":    "a@x.com",
			"password": "pwd12345",
		}).
		Expect().Status(201).JSON().Object().
		Value("token").String().NotEmpty()
}

func TestAuthHandler_Register_409_Username(t *testing.T) {
	r, m := setupAuthHandler(t)
	m.EXPECT().GetByUsername(mock.Anything, "alice").
		Return(&repo.User{ID: 1, Username: "alice"}, nil)

	testutil.NewExpect(t, r).POST("/api/auth/register").
		WithJSON(map[string]string{
			"username": "alice",
			"email":    "a@x.com",
			"password": "pwd12345",
		}).
		Expect().Status(409)
}

func TestAuthHandler_Register_422_BadEmail(t *testing.T) {
	r, _ := setupAuthHandler(t)
	testutil.NewExpect(t, r).POST("/api/auth/register").
		WithJSON(map[string]string{
			"username": "alice",
			"email":    "bad",
			"password": "pwd12345",
		}).
		Expect().Status(422)
}

func TestAuthHandler_Register_422_ShortUsername(t *testing.T) {
	r, _ := setupAuthHandler(t)
	testutil.NewExpect(t, r).POST("/api/auth/register").
		WithJSON(map[string]string{
			"username": "ab",
			"email":    "a@x.com",
			"password": "pwd12345",
		}).
		Expect().Status(422)
}

func TestAuthHandler_Register_422_ShortPassword(t *testing.T) {
	r, _ := setupAuthHandler(t)
	testutil.NewExpect(t, r).POST("/api/auth/register").
		WithJSON(map[string]string{
			"username": "alice",
			"email":    "a@x.com",
			"password": "short",
		}).
		Expect().Status(422)
}

func TestAuthHandler_Login_OK(t *testing.T) {
	r, m := setupAuthHandler(t)
	hash, err := bcrypt.GenerateFromPassword([]byte("pwd12345"), bcrypt.MinCost)
	require.NoError(t, err)

	m.EXPECT().GetByUsername(mock.Anything, "alice").
		Return(&repo.User{ID: 7, Username: "alice", PasswordHash: string(hash)}, nil)

	testutil.NewExpect(t, r).POST("/api/auth/login").
		WithJSON(map[string]string{"login": "alice", "password": "pwd12345"}).
		Expect().Status(200).JSON().Object().
		Value("token").String().NotEmpty()
}

func TestAuthHandler_Login_401(t *testing.T) {
	r, m := setupAuthHandler(t)
	hash, err := bcrypt.GenerateFromPassword([]byte("pwd12345"), bcrypt.MinCost)
	require.NoError(t, err)

	m.EXPECT().GetByUsername(mock.Anything, "alice").
		Return(&repo.User{ID: 7, Username: "alice", PasswordHash: string(hash)}, nil)

	testutil.NewExpect(t, r).POST("/api/auth/login").
		WithJSON(map[string]string{"login": "alice", "password": "wrong"}).
		Expect().Status(401)
}

func TestAuthHandler_Login_422_MissingFields(t *testing.T) {
	r, _ := setupAuthHandler(t)
	testutil.NewExpect(t, r).POST("/api/auth/login").
		WithJSON(map[string]string{"login": "alice"}).
		Expect().Status(422)
}

func TestAuthHandler_Me_NoToken_401(t *testing.T) {
	r, _ := setupAuthHandler(t)
	testutil.NewExpect(t, r).GET("/api/auth/me").Expect().Status(401)
}

func TestAuthHandler_Me_OK(t *testing.T) {
	r, m := setupAuthHandler(t)
	tok, err := auth.GenerateToken(testSecret, 42, "alice")
	require.NoError(t, err)
	require.NotEmpty(t, tok)

	m.EXPECT().GetByID(mock.Anything, int64(42)).
		Return(&repo.User{ID: 42, Username: "alice"}, nil)

	// Legacy handler returns the bare user object (not wrapped in "user").
	testutil.NewExpect(t, r).GET("/api/auth/me").
		WithHeader("Authorization", "Bearer "+tok).
		Expect().Status(200).JSON().Object().
		Value("username").IsEqual("alice")
}

func TestAuthHandler_Me_404_UserGone(t *testing.T) {
	r, m := setupAuthHandler(t)
	tok, err := auth.GenerateToken(testSecret, 99, "ghost")
	require.NoError(t, err)

	m.EXPECT().GetByID(mock.Anything, int64(99)).Return(nil, repo.ErrNotFound)

	testutil.NewExpect(t, r).GET("/api/auth/me").
		WithHeader("Authorization", "Bearer "+tok).
		Expect().Status(404)
}

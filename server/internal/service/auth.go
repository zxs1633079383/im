// Package service holds business logic above the repo layer.
package service

import (
	"context"
	"errors"
	"strings"

	"golang.org/x/crypto/bcrypt"

	"im-server/internal/auth"
	"im-server/internal/repo"
)

// Errors returned by AuthService.
var (
	// ErrUserExists is returned by Register when the requested username or
	// email is already taken.
	ErrUserExists = errors.New("user already exists")
	// ErrBadCreds is returned by Login when the supplied login does not match
	// any user or the password does not match.
	ErrBadCreds = errors.New("invalid credentials")
)

// AuthService implements registration and login above repo.UserRepo.
//
// It is intentionally small: pre-validation (length, format) belongs to the
// transport layer, password hashing/checking and JWT issuance belong here.
type AuthService struct {
	users     repo.UserRepo
	jwtSecret string
}

// NewAuthService constructs an AuthService backed by the supplied UserRepo.
func NewAuthService(users repo.UserRepo, jwtSecret string) *AuthService {
	return &AuthService{users: users, jwtSecret: jwtSecret}
}

// Register creates a new user with a bcrypt-hashed password and returns the
// persisted user along with a freshly signed JWT.
//
// Returns ErrUserExists if either the username or the email collides with an
// existing record. The handler is responsible for length/format validation.
func (s *AuthService) Register(ctx context.Context, username, email, password, displayName string) (*repo.User, string, error) {
	// Duplicate username check.
	if _, err := s.users.GetByUsername(ctx, username); err == nil {
		return nil, "", ErrUserExists
	} else if !errors.Is(err, repo.ErrNotFound) {
		return nil, "", err
	}
	// Duplicate email check.
	if _, err := s.users.GetByEmail(ctx, email); err == nil {
		return nil, "", ErrUserExists
	} else if !errors.Is(err, repo.ErrNotFound) {
		return nil, "", err
	}

	hash, err := bcrypt.GenerateFromPassword([]byte(password), bcrypt.DefaultCost)
	if err != nil {
		return nil, "", err
	}
	if displayName == "" {
		displayName = username
	}
	u := &repo.User{
		Username:     username,
		Email:        email,
		PasswordHash: string(hash),
		DisplayName:  displayName,
		Status:       repo.UserStatusActive,
	}
	if err := s.users.Create(ctx, u); err != nil {
		return nil, "", err
	}
	tok, err := auth.GenerateToken(s.jwtSecret, u.ID, u.Username)
	if err != nil {
		return nil, "", err
	}
	return u, tok, nil
}

// Login authenticates a user by username-or-email + password, returning the
// user and a freshly signed JWT.
//
// Empty login or password, missing user, and bad password all collapse into
// ErrBadCreds to prevent user enumeration.
func (s *AuthService) Login(ctx context.Context, login, password string) (*repo.User, string, error) {
	login = strings.TrimSpace(login)
	if login == "" || password == "" {
		return nil, "", ErrBadCreds
	}

	var (
		u   *repo.User
		err error
	)
	if strings.Contains(login, "@") {
		u, err = s.users.GetByEmail(ctx, login)
	} else {
		u, err = s.users.GetByUsername(ctx, login)
	}
	if err != nil {
		if errors.Is(err, repo.ErrNotFound) {
			return nil, "", ErrBadCreds
		}
		return nil, "", err
	}
	if err := bcrypt.CompareHashAndPassword([]byte(u.PasswordHash), []byte(password)); err != nil {
		return nil, "", ErrBadCreds
	}
	tok, err := auth.GenerateToken(s.jwtSecret, u.ID, u.Username)
	if err != nil {
		return nil, "", err
	}
	return u, tok, nil
}

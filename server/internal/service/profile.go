package service

import (
	"context"

	"im-server/internal/repo"
)

// ProfileService updates user profile fields above repo.UserRepo.
//
// Pre-validation (length, URL shape) belongs to the transport layer; this
// service only forwards the request to the repository and surfaces
// repo.ErrNotFound to the caller.
type ProfileService struct {
	users repo.UserRepo
}

// NewProfileService constructs a ProfileService backed by the supplied UserRepo.
func NewProfileService(users repo.UserRepo) *ProfileService {
	return &ProfileService{users: users}
}

// UpdateProfile updates display_name and avatar_url for userID and returns the
// refreshed user row. Returns repo.ErrNotFound when the user no longer exists.
func (s *ProfileService) UpdateProfile(ctx context.Context, userID int64, displayName, avatarURL string) (*repo.User, error) {
	return s.users.UpdateProfile(ctx, userID, displayName, avatarURL)
}

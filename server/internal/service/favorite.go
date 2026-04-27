package service

import (
	"context"
	"fmt"

	"im-server/internal/repo"
)

// FavoriteStore is the subset of repo.FavoriteRepo FavoriteService consumes.
// Defined consumer-side (Go's "accept small interfaces" idiom) so the service
// surface is documented at the call site. The production binding is
// repo.FavoriteRepo.
type FavoriteStore interface {
	Add(ctx context.Context, userID string, messageID int64) error
	Remove(ctx context.Context, userID string, messageID int64) error
	List(ctx context.Context, userID string) ([]repo.FavoriteWithMessage, error)
}

// FavoriteService manages a user's favorited messages. Add is idempotent
// (the underlying repo OnConflict-DoNothings on the composite PK); Remove
// surfaces repo.ErrNotFound when the user has no such favorite, so the
// transport layer can map it to a 404.
type FavoriteService struct {
	store FavoriteStore
}

// NewFavoriteService wires the supplied store. Production passes
// repo.FavoriteRepo, which satisfies FavoriteStore by construction.
func NewFavoriteService(store FavoriteStore) *FavoriteService {
	return &FavoriteService{store: store}
}

// Add records a favorite for (userID, messageID). Idempotent: a second call
// with the same arguments is a no-op (no error).
func (s *FavoriteService) Add(ctx context.Context, userID string, messageID int64) error {
	ctx, span := tracer.Start(ctx, "FavoriteService.Add")
	defer span.End()

	if err := s.store.Add(ctx, userID, messageID); err != nil {
		return fmt.Errorf("add favorite: %w", err)
	}
	return nil
}

// Remove deletes a favorite for (userID, messageID). Returns repo.ErrNotFound
// when the user has no such favorite — callers can use errors.Is to map to a
// 404 response.
func (s *FavoriteService) Remove(ctx context.Context, userID string, messageID int64) error {
	ctx, span := tracer.Start(ctx, "FavoriteService.Remove")
	defer span.End()

	if err := s.store.Remove(ctx, userID, messageID); err != nil {
		// Don't wrap ErrNotFound — preserve the sentinel so callers can use
		// errors.Is(err, repo.ErrNotFound) to map it to a 404. Wrap other
		// errors with context.
		if err == repo.ErrNotFound {
			return err
		}
		return fmt.Errorf("remove favorite: %w", err)
	}
	return nil
}

// List returns the user's favorites paired with each message, newest first.
// A user with no favorites yields a non-nil empty slice — the transport layer
// always emits the "favorites" JSON key as an array.
func (s *FavoriteService) List(ctx context.Context, userID string) ([]repo.FavoriteWithMessage, error) {
	ctx, span := tracer.Start(ctx, "FavoriteService.List")
	defer span.End()

	favs, err := s.store.List(ctx, userID)
	if err != nil {
		return nil, fmt.Errorf("list favorites: %w", err)
	}
	if favs == nil {
		favs = []repo.FavoriteWithMessage{}
	}
	return favs, nil
}

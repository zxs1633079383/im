package repo

import (
	"context"
	"fmt"
	"time"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// FavoriteWithMessage joins a favorite row with the favorited message.
//
// Message is populated separately (rather than via embedded GORM mapping)
// to avoid column-prefix gymnastics — favorite columns and message columns
// have overlapping names (created_at) that don't cleanly destructure.
type FavoriteWithMessage struct {
	UserID    string    `json:"user_id"`
	MessageID string    `json:"message_id"`
	CreatedAt time.Time `json:"created_at"`
	Message   Message   `gorm:"-" json:"message"`
}

// FavoriteRepo manages a user's favorited messages.
//
// Add is idempotent via OnConflict DoNothing on the (user_id, message_id)
// composite PK. Remove returns ErrNotFound if no row matched.
type FavoriteRepo interface {
	Add(ctx context.Context, userID string, messageID string) error
	Remove(ctx context.Context, userID string, messageID string) error
	List(ctx context.Context, userID string) ([]FavoriteWithMessage, error)
}

type gormFavoriteRepo struct{ db *gorm.DB }

// NewFavoriteRepo returns a GORM-backed FavoriteRepo.
func NewFavoriteRepo(db *gorm.DB) FavoriteRepo { return &gormFavoriteRepo{db: db} }

func (r *gormFavoriteRepo) Add(ctx context.Context, userID string, messageID string) error {
	fav := MessageFavorite{UserID: userID, MessageID: messageID}
	if err := r.db.WithContext(ctx).
		Clauses(clause.OnConflict{DoNothing: true}).
		Create(&fav).Error; err != nil {
		return fmt.Errorf("add favorite: %w", err)
	}
	return nil
}

func (r *gormFavoriteRepo) Remove(ctx context.Context, userID string, messageID string) error {
	res := r.db.WithContext(ctx).
		Where("user_id = ? AND message_id = ?", userID, messageID).
		Delete(&MessageFavorite{})
	if res.Error != nil {
		return fmt.Errorf("remove favorite: %w", res.Error)
	}
	if res.RowsAffected == 0 {
		return ErrNotFound
	}
	return nil
}

// List returns the user's favorites paired with each message, newest first.
//
// Implementation is two-step: fetch favorites by user, then batch-load the
// referenced messages in a single IN query, then assemble. This is simpler
// (and in practice no slower for typical favorite list sizes) than
// dealing with overlapping column names in a single embedded SELECT.
func (r *gormFavoriteRepo) List(ctx context.Context, userID string) ([]FavoriteWithMessage, error) {
	var favs []MessageFavorite
	if err := r.db.WithContext(ctx).
		Where("user_id = ?", userID).
		Order("created_at DESC").
		Find(&favs).Error; err != nil {
		return nil, fmt.Errorf("list favorites: %w", err)
	}
	if len(favs) == 0 {
		return nil, nil
	}

	msgIDs := make([]string, 0, len(favs))
	for _, f := range favs {
		msgIDs = append(msgIDs, f.MessageID)
	}

	var msgs []Message
	if err := r.db.WithContext(ctx).
		Where("id IN ?", msgIDs).
		Find(&msgs).Error; err != nil {
		return nil, fmt.Errorf("load favorite messages: %w", err)
	}
	msgByID := make(map[string]Message, len(msgs))
	for _, m := range msgs {
		msgByID[m.ID] = m
	}

	out := make([]FavoriteWithMessage, 0, len(favs))
	for _, f := range favs {
		out = append(out, FavoriteWithMessage{
			UserID:    f.UserID,
			MessageID: f.MessageID,
			CreatedAt: f.CreatedAt,
			Message:   msgByID[f.MessageID],
		})
	}
	return out, nil
}

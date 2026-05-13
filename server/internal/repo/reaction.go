package repo

import (
	"context"
	"errors"
	"fmt"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// ReactionRepo encapsulates message_reactions persistence.
//
// Add and Remove are idempotent — re-adding the same (message_id, user_id,
// emoji) is a no-op (ON CONFLICT DO NOTHING), removing a non-existent row
// returns ErrNotFound. List returns reactions for one message ordered by
// created_at ASC.
type ReactionRepo interface {
	Add(ctx context.Context, r *MessageReaction) error
	Remove(ctx context.Context, messageID string, userID, emoji string) error
	List(ctx context.Context, messageID string) ([]MessageReaction, error)
}

type gormReactionRepo struct{ db *gorm.DB }

// NewReactionRepo wires the supplied GORM connection.
func NewReactionRepo(db *gorm.DB) ReactionRepo { return &gormReactionRepo{db: db} }

// Add inserts a new reaction or no-ops if (message_id, user_id, emoji)
// already exists. Returns ErrNotFound when the parent message is gone.
func (r *gormReactionRepo) Add(ctx context.Context, react *MessageReaction) error {
	if react == nil || react.MessageID == "" || react.UserID == "" || react.Emoji == "" {
		return fmt.Errorf("reaction add: message_id/user_id/emoji required")
	}
	err := r.db.WithContext(ctx).
		Clauses(clause.OnConflict{DoNothing: true}).
		Create(react).Error
	if err != nil {
		return fmt.Errorf("reaction add: %w", err)
	}
	return nil
}

// Remove deletes one (message_id, user_id, emoji) row. RowsAffected=0 →
// ErrNotFound (the row was already gone or never existed).
func (r *gormReactionRepo) Remove(ctx context.Context, messageID string, userID, emoji string) error {
	if messageID == "" || userID == "" || emoji == "" {
		return fmt.Errorf("reaction remove: message_id/user_id/emoji required")
	}
	res := r.db.WithContext(ctx).
		Where("message_id = ? AND user_id = ? AND emoji = ?", messageID, userID, emoji).
		Delete(&MessageReaction{})
	if res.Error != nil {
		return fmt.Errorf("reaction remove: %w", res.Error)
	}
	if res.RowsAffected == 0 {
		return ErrNotFound
	}
	return nil
}

// List returns every reaction on the given message, oldest first so the
// client can display them in a stable timeline order.
func (r *gormReactionRepo) List(ctx context.Context, messageID string) ([]MessageReaction, error) {
	if messageID == "" {
		return nil, errors.New("reaction list: message_id required")
	}
	var out []MessageReaction
	err := r.db.WithContext(ctx).
		Where("message_id = ?", messageID).
		Order("created_at ASC").
		Find(&out).Error
	if err != nil {
		return nil, fmt.Errorf("reaction list: %w", err)
	}
	return out, nil
}

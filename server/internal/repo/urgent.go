package repo

import (
	"context"
	"fmt"
	"time"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// UrgentConfirmation maps urgent_confirmations — a per-recipient confirmation
// that clears the "urgent unconfirmed" badge for that user on that message.
type UrgentConfirmation struct {
	MessageID   string    `gorm:"column:message_id;type:text;primaryKey"                 json:"message_id"`
	UserID      string    `gorm:"column:user_id;type:text;primaryKey"                    json:"user_id"`
	ConfirmedAt time.Time `gorm:"column:confirmed_at;not null;default:now()"             json:"confirmed_at"`
}

// TableName pins the GORM-derived table name to the migration.
func (UrgentConfirmation) TableName() string { return "urgent_confirmations" }

// UrgentRepo is the data access layer for urgent-message handling.
type UrgentRepo interface {
	SetUrgent(ctx context.Context, msgID string) error
	ClearUrgent(ctx context.Context, msgID string) error

	AddConfirmation(ctx context.Context, msgID string, userID string) error
	RemoveConfirmation(ctx context.Context, msgID string, userID string) error
	ListConfirmations(ctx context.Context, msgID string) ([]string, error)
	CountUnconfirmed(ctx context.Context, channelID string, userID string) (int64, error)
}

type gormUrgentRepo struct{ db *gorm.DB }

// NewUrgentRepo returns a GORM-backed UrgentRepo.
func NewUrgentRepo(db *gorm.DB) UrgentRepo { return &gormUrgentRepo{db: db} }

// SetUrgent flags msgID as is_urgent=TRUE. Returns ErrNotFound when the row
// doesn't exist.
func (r *gormUrgentRepo) SetUrgent(ctx context.Context, msgID string) error {
	res := r.db.WithContext(ctx).Model(&Message{}).
		Where("id = ?", msgID).
		Update("is_urgent", true)
	if res.Error != nil {
		return fmt.Errorf("set urgent: %w", res.Error)
	}
	if res.RowsAffected == 0 {
		return ErrNotFound
	}
	return nil
}

// ClearUrgent flips is_urgent back to FALSE. Idempotent.
func (r *gormUrgentRepo) ClearUrgent(ctx context.Context, msgID string) error {
	res := r.db.WithContext(ctx).Model(&Message{}).
		Where("id = ?", msgID).
		Update("is_urgent", false)
	if res.Error != nil {
		return fmt.Errorf("clear urgent: %w", res.Error)
	}
	if res.RowsAffected == 0 {
		return ErrNotFound
	}
	return nil
}

// AddConfirmation records (msgID, userID) in urgent_confirmations. Idempotent.
func (r *gormUrgentRepo) AddConfirmation(ctx context.Context, msgID string, userID string) error {
	c := &UrgentConfirmation{MessageID: msgID, UserID: userID}
	err := r.db.WithContext(ctx).Clauses(clause.OnConflict{
		Columns:   []clause.Column{{Name: "message_id"}, {Name: "user_id"}},
		DoNothing: true,
	}).Create(c).Error
	if err != nil {
		return fmt.Errorf("add confirmation: %w", err)
	}
	return nil
}

// RemoveConfirmation removes the (msgID, userID) pair. Idempotent.
func (r *gormUrgentRepo) RemoveConfirmation(ctx context.Context, msgID string, userID string) error {
	err := r.db.WithContext(ctx).
		Where("message_id = ? AND user_id = ?", msgID, userID).
		Delete(&UrgentConfirmation{}).Error
	if err != nil {
		return fmt.Errorf("remove confirmation: %w", err)
	}
	return nil
}

// ListConfirmations returns the user IDs that have confirmed msgID.
func (r *gormUrgentRepo) ListConfirmations(ctx context.Context, msgID string) ([]string, error) {
	var ids []string
	err := r.db.WithContext(ctx).Model(&UrgentConfirmation{}).
		Where("message_id = ?", msgID).
		Order("confirmed_at ASC").
		Pluck("user_id", &ids).Error
	if err != nil {
		return nil, fmt.Errorf("list confirmations: %w", err)
	}
	return ids, nil
}

// CountUnconfirmed returns the number of urgent messages in channelID that
// userID has NOT yet confirmed. Used to render per-channel urgent badges.
func (r *gormUrgentRepo) CountUnconfirmed(ctx context.Context, channelID string, userID string) (int64, error) {
	var count int64
	err := r.db.WithContext(ctx).Raw(
		`SELECT COUNT(*) FROM messages m
		 WHERE m.channel_id = ? AND m.is_urgent = TRUE AND m.deleted = FALSE
		   AND NOT EXISTS (
		       SELECT 1 FROM urgent_confirmations c
		       WHERE c.message_id = m.id AND c.user_id = ?
		   )`,
		channelID, userID,
	).Scan(&count).Error
	if err != nil {
		return 0, fmt.Errorf("count unconfirmed: %w", err)
	}
	return count, nil
}

var _ UrgentRepo = (*gormUrgentRepo)(nil)

package repo

import (
	"context"
	"errors"
	"fmt"
	"time"

	"gorm.io/gorm"
)

// Notification type enum — mirrors the migration's comment.
const (
	NotificationTypeGeneric int16 = 0
	NotificationTypeMention int16 = 1
	NotificationTypeSystem  int16 = 2
)

// Notification maps the notifications table. ReadAt is nil until the receiver
// explicitly marks it read.
type Notification struct {
	ID         string     `gorm:"primaryKey;type:text"                     json:"id"`
	SenderID   string     `gorm:"column:sender_id;type:text;not null"      json:"sender_id"`
	ReceiverID string     `gorm:"column:receiver_id;type:text;not null"    json:"receiver_id"`
	Title      string     `gorm:"not null"                                 json:"title"`
	Body       string     `gorm:"not null;default:''"                      json:"body"`
	Type       int16      `gorm:"not null;default:0"                       json:"type"`
	ReadAt     *time.Time `gorm:"column:read_at"                           json:"read_at,omitempty"`
	Props      string     `gorm:"type:jsonb;not null;default:'{}'"         json:"props"`
	CreatedAt  time.Time  `gorm:"column:created_at;not null;default:now()" json:"created_at"`
}

// TableName pins the GORM-derived table name to the migration.
func (Notification) TableName() string { return "notifications" }

// NotificationRepo is the data-access surface for per-user notifications.
type NotificationRepo interface {
	Create(ctx context.Context, n *Notification) error
	GetByID(ctx context.Context, id string) (*Notification, error)

	// ListReceived returns notifications addressed to receiverID. When
	// unreadOnly is true, only rows with read_at IS NULL are returned.
	// Pagination uses (limit, cursor) where cursor is the last id seen.
	ListReceived(ctx context.Context, receiverID string, unreadOnly bool, limit int, cursor string) ([]Notification, error)

	// ListSent returns notifications sent by senderID. Newest-first.
	ListSent(ctx context.Context, senderID string, limit int, cursor string) ([]Notification, error)

	// MarkRead stamps read_at = now() on id — but only if receiver_id matches
	// the caller. RowsAffected = 0 → ErrNotFound.
	MarkRead(ctx context.Context, id string, receiverID string) error
}

type gormNotificationRepo struct{ db *gorm.DB }

// NewNotificationRepo returns a GORM-backed NotificationRepo.
func NewNotificationRepo(db *gorm.DB) NotificationRepo {
	return &gormNotificationRepo{db: db}
}

// Create inserts a new notification. Callers set Sender/Receiver/Title/Body/
// Type/Props; the DB fills id + created_at.
func (r *gormNotificationRepo) Create(ctx context.Context, n *Notification) error {
	if n.Props == "" {
		n.Props = "{}"
	}
	if err := r.db.WithContext(ctx).Create(n).Error; err != nil {
		return fmt.Errorf("create notification: %w", err)
	}
	return nil
}

// GetByID returns the notification by primary key. ErrNotFound if missing.
func (r *gormNotificationRepo) GetByID(ctx context.Context, id string) (*Notification, error) {
	var n Notification
	if err := r.db.WithContext(ctx).First(&n, id).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, ErrNotFound
		}
		return nil, fmt.Errorf("get notification: %w", err)
	}
	return &n, nil
}

// ListReceived returns the receiver's inbox newest-first.
func (r *gormNotificationRepo) ListReceived(ctx context.Context, receiverID string, unreadOnly bool, limit int, cursor string) ([]Notification, error) {
	q := r.db.WithContext(ctx).
		Where("receiver_id = ?", receiverID).
		Order("id DESC")
	if unreadOnly {
		q = q.Where("read_at IS NULL")
	}
	if cursor != "" {
		q = q.Where("id < ?", cursor)
	}
	if limit > 0 {
		q = q.Limit(limit)
	}
	var out []Notification
	if err := q.Find(&out).Error; err != nil {
		return nil, fmt.Errorf("list received: %w", err)
	}
	return out, nil
}

// ListSent returns the sender's outbox newest-first.
func (r *gormNotificationRepo) ListSent(ctx context.Context, senderID string, limit int, cursor string) ([]Notification, error) {
	q := r.db.WithContext(ctx).
		Where("sender_id = ?", senderID).
		Order("id DESC")
	if cursor != "" {
		q = q.Where("id < ?", cursor)
	}
	if limit > 0 {
		q = q.Limit(limit)
	}
	var out []Notification
	if err := q.Find(&out).Error; err != nil {
		return nil, fmt.Errorf("list sent: %w", err)
	}
	return out, nil
}

// MarkRead stamps read_at = now() — only if (id, receiver_id) matches AND
// read_at is still NULL (idempotent second-mark, single UPDATE).
func (r *gormNotificationRepo) MarkRead(ctx context.Context, id string, receiverID string) error {
	res := r.db.WithContext(ctx).Model(&Notification{}).
		Where("id = ? AND receiver_id = ? AND read_at IS NULL", id, receiverID).
		Update("read_at", gorm.Expr("now()"))
	if res.Error != nil {
		return fmt.Errorf("mark read: %w", res.Error)
	}
	if res.RowsAffected == 0 {
		// Check whether it's not-found vs already-read.
		var exists int64
		r.db.WithContext(ctx).Model(&Notification{}).
			Where("id = ? AND receiver_id = ?", id, receiverID).
			Count(&exists)
		if exists == 0 {
			return ErrNotFound
		}
		// Already read — idempotent no-op.
	}
	return nil
}

var _ NotificationRepo = (*gormNotificationRepo)(nil)

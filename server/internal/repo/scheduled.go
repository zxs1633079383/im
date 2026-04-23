package repo

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/lib/pq"
	"gorm.io/gorm"
)

// Scheduled-message status enum.
const (
	ScheduledStatusPending   int16 = 0
	ScheduledStatusDelivered int16 = 1
	ScheduledStatusCancelled int16 = 2
	ScheduledStatusFailed    int16 = 3
)

// ScheduledMessage maps the scheduled_messages table. VisibleTo / FileIDs are
// Postgres BIGINT[] carried as pq.Int64Array for zero-alloc roundtripping.
type ScheduledMessage struct {
	ID                 int64         `gorm:"primaryKey;autoIncrement"                      json:"id"`
	ChannelID          int64         `gorm:"column:channel_id;not null"                    json:"channel_id"`
	SenderID           int64         `gorm:"column:sender_id;not null"                     json:"sender_id"`
	Content            string        `gorm:"not null"                                      json:"content"`
	MsgType            int16         `gorm:"column:msg_type;not null;default:1"            json:"msg_type"`
	VisibleTo          pq.Int64Array `gorm:"column:visible_to;type:bigint[]"               json:"visible_to,omitempty"`
	ReplyTo            *int64        `gorm:"column:reply_to"                               json:"reply_to,omitempty"`
	FileIDs            pq.Int64Array `gorm:"column:file_ids;type:bigint[]"                 json:"file_ids,omitempty"`
	ScheduledAt        time.Time     `gorm:"column:scheduled_at;not null"                  json:"scheduled_at"`
	Status             int16         `gorm:"not null;default:0"                            json:"status"`
	DeliveredMessageID *int64        `gorm:"column:delivered_message_id"                   json:"delivered_message_id,omitempty"`
	Error              string        `gorm:"column:error"                                  json:"error,omitempty"`
	CreatedAt          time.Time     `gorm:"column:created_at;not null;default:now()"      json:"created_at"`
	UpdatedAt          time.Time     `gorm:"column:updated_at;not null;default:now()"      json:"updated_at"`
}

// TableName pins the GORM-derived table name to the migration.
func (ScheduledMessage) TableName() string { return "scheduled_messages" }

// ScheduledRepo is the data-access surface for deferred sends. The worker
// polls FetchDue to pick up rows whose scheduled_at has passed AND status is
// still pending, then calls MarkDelivered / MarkFailed after handling.
type ScheduledRepo interface {
	Create(ctx context.Context, s *ScheduledMessage) error
	GetByID(ctx context.Context, id int64) (*ScheduledMessage, error)
	Cancel(ctx context.Context, id, senderID int64) error

	// ListBySender returns the caller's queue. When statusFilter >= 0, only
	// rows with that status are returned; -1 returns every status. Ordered
	// by scheduled_at DESC.
	ListBySender(ctx context.Context, senderID int64, statusFilter int16, limit int, cursor int64) ([]ScheduledMessage, error)

	// FetchDue returns up to limit pending rows with scheduled_at <= now,
	// ordered oldest-first. Worker should lock semantics come later — this
	// simple implementation relies on MarkDelivered's WHERE status=pending
	// guard to keep concurrent workers idempotent.
	FetchDue(ctx context.Context, now time.Time, limit int) ([]ScheduledMessage, error)

	MarkDelivered(ctx context.Context, id, deliveredMsgID int64) error
	MarkFailed(ctx context.Context, id int64, errMsg string) error
}

type gormScheduledRepo struct{ db *gorm.DB }

// NewScheduledRepo returns a GORM-backed ScheduledRepo.
func NewScheduledRepo(db *gorm.DB) ScheduledRepo { return &gormScheduledRepo{db: db} }

// Create inserts a pending scheduled message.
func (r *gormScheduledRepo) Create(ctx context.Context, s *ScheduledMessage) error {
	s.Status = ScheduledStatusPending
	if err := r.db.WithContext(ctx).Create(s).Error; err != nil {
		return fmt.Errorf("create scheduled: %w", err)
	}
	return nil
}

// GetByID returns a scheduled message by PK. ErrNotFound if missing.
func (r *gormScheduledRepo) GetByID(ctx context.Context, id int64) (*ScheduledMessage, error) {
	var s ScheduledMessage
	if err := r.db.WithContext(ctx).First(&s, id).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, ErrNotFound
		}
		return nil, fmt.Errorf("get scheduled: %w", err)
	}
	return &s, nil
}

// Cancel transitions a pending scheduled message to cancelled. Only the sender
// may cancel — enforced in the WHERE clause. RowsAffected = 0 → ErrNotFound
// (the row may have already delivered or never existed).
func (r *gormScheduledRepo) Cancel(ctx context.Context, id, senderID int64) error {
	res := r.db.WithContext(ctx).Model(&ScheduledMessage{}).
		Where("id = ? AND sender_id = ? AND status = ?", id, senderID, ScheduledStatusPending).
		Updates(map[string]any{
			"status":     ScheduledStatusCancelled,
			"updated_at": gorm.Expr("now()"),
		})
	if res.Error != nil {
		return fmt.Errorf("cancel scheduled: %w", res.Error)
	}
	if res.RowsAffected == 0 {
		return ErrNotFound
	}
	return nil
}

// ListBySender returns messages filed by senderID.
func (r *gormScheduledRepo) ListBySender(ctx context.Context, senderID int64, statusFilter int16, limit int, cursor int64) ([]ScheduledMessage, error) {
	q := r.db.WithContext(ctx).
		Where("sender_id = ?", senderID).
		Order("scheduled_at DESC")
	if statusFilter >= 0 {
		q = q.Where("status = ?", statusFilter)
	}
	if cursor > 0 {
		q = q.Where("id < ?", cursor)
	}
	if limit > 0 {
		q = q.Limit(limit)
	}
	var out []ScheduledMessage
	if err := q.Find(&out).Error; err != nil {
		return nil, fmt.Errorf("list scheduled: %w", err)
	}
	return out, nil
}

// FetchDue returns pending rows whose scheduled_at has passed.
func (r *gormScheduledRepo) FetchDue(ctx context.Context, now time.Time, limit int) ([]ScheduledMessage, error) {
	q := r.db.WithContext(ctx).
		Where("status = ? AND scheduled_at <= ?", ScheduledStatusPending, now).
		Order("scheduled_at ASC")
	if limit > 0 {
		q = q.Limit(limit)
	}
	var out []ScheduledMessage
	if err := q.Find(&out).Error; err != nil {
		return nil, fmt.Errorf("fetch due: %w", err)
	}
	return out, nil
}

// MarkDelivered transitions pending → delivered and records the produced
// message ID. The WHERE status = pending guard protects against concurrent
// workers double-delivering — only one UPDATE wins.
func (r *gormScheduledRepo) MarkDelivered(ctx context.Context, id, deliveredMsgID int64) error {
	res := r.db.WithContext(ctx).Model(&ScheduledMessage{}).
		Where("id = ? AND status = ?", id, ScheduledStatusPending).
		Updates(map[string]any{
			"status":               ScheduledStatusDelivered,
			"delivered_message_id": deliveredMsgID,
			"updated_at":           gorm.Expr("now()"),
		})
	if res.Error != nil {
		return fmt.Errorf("mark delivered: %w", res.Error)
	}
	if res.RowsAffected == 0 {
		return ErrNotFound
	}
	return nil
}

// MarkFailed transitions pending → failed and records the error message.
func (r *gormScheduledRepo) MarkFailed(ctx context.Context, id int64, errMsg string) error {
	res := r.db.WithContext(ctx).Model(&ScheduledMessage{}).
		Where("id = ? AND status = ?", id, ScheduledStatusPending).
		Updates(map[string]any{
			"status":     ScheduledStatusFailed,
			"error":      errMsg,
			"updated_at": gorm.Expr("now()"),
		})
	if res.Error != nil {
		return fmt.Errorf("mark failed: %w", res.Error)
	}
	if res.RowsAffected == 0 {
		return ErrNotFound
	}
	return nil
}

var _ ScheduledRepo = (*gormScheduledRepo)(nil)

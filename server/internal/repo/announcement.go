package repo

import (
	"context"
	"errors"
	"fmt"
	"time"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// Announcement maps the announcements table. Props is a JSONB blob carried
// as a string — callers decide the concrete schema. Deleted is a soft-delete
// flag (the row stays in the DB so acks remain auditable).
type Announcement struct {
	ID        string    `gorm:"primaryKey;type:text"                                      json:"id"`
	ChannelID string    `gorm:"column:channel_id;type:text;not null"                      json:"channel_id"`
	CreatorID string    `gorm:"column:creator_id;type:text;not null"                      json:"creator_id"`
	Title     string    `gorm:"not null"                                                  json:"title"`
	Content   string    `gorm:"not null"                                                  json:"content"`
	Props     string    `gorm:"type:jsonb;not null;default:'{}'"                          json:"props"`
	Deleted   bool      `gorm:"not null;default:false"                                    json:"deleted"`
	CreatedAt time.Time `gorm:"column:created_at;not null;default:now()"                  json:"created_at"`
	UpdatedAt time.Time `gorm:"column:updated_at;not null;default:now()"                  json:"updated_at"`
}

// TableName pins the GORM-derived table name to the migration.
func (Announcement) TableName() string { return "announcements" }

// AnnouncementAck maps the announcement_acknowledgements table — a user has
// acknowledged a specific announcement.
type AnnouncementAck struct {
	AnnouncementID string    `gorm:"column:announcement_id;type:text;primaryKey"                json:"announcement_id"`
	UserID         string    `gorm:"column:user_id;type:text;primaryKey"                        json:"user_id"`
	AcknowledgedAt time.Time `gorm:"column:acknowledged_at;not null;default:now()"              json:"acknowledged_at"`
}

// TableName pins the GORM-derived table name to the migration.
func (AnnouncementAck) TableName() string { return "announcement_acknowledgements" }

// AnnouncementRepo is the data access layer for channel announcements.
type AnnouncementRepo interface {
	Create(ctx context.Context, a *Announcement) error
	GetByID(ctx context.Context, id string) (*Announcement, error)
	ListByChannel(ctx context.Context, channelID string, limit, offset int) ([]Announcement, error)
	SoftDelete(ctx context.Context, id string) error

	AddAck(ctx context.Context, announcementID string, userID string) error
	ListAcks(ctx context.Context, announcementID string) ([]AnnouncementAck, error)
}

type gormAnnouncementRepo struct{ db *gorm.DB }

// NewAnnouncementRepo returns a GORM-backed AnnouncementRepo.
func NewAnnouncementRepo(db *gorm.DB) AnnouncementRepo {
	return &gormAnnouncementRepo{db: db}
}

// Create inserts an announcement. The caller must set ChannelID/CreatorID/
// Title/Content; the db fills in id/created_at/updated_at/deleted.
func (r *gormAnnouncementRepo) Create(ctx context.Context, a *Announcement) error {
	if a.Props == "" {
		a.Props = "{}"
	}
	if err := r.db.WithContext(ctx).Create(a).Error; err != nil {
		return fmt.Errorf("create announcement: %w", err)
	}
	return nil
}

// GetByID returns the announcement (including soft-deleted ones — callers
// filter as appropriate). ErrNotFound if missing.
func (r *gormAnnouncementRepo) GetByID(ctx context.Context, id string) (*Announcement, error) {
	var a Announcement
	if err := r.db.WithContext(ctx).First(&a, id).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, ErrNotFound
		}
		return nil, fmt.Errorf("get announcement: %w", err)
	}
	return &a, nil
}

// ListByChannel returns non-deleted announcements, newest first. limit/offset
// enable pagination; caller can pass limit=0 for "all".
func (r *gormAnnouncementRepo) ListByChannel(ctx context.Context, channelID string, limit, offset int) ([]Announcement, error) {
	q := r.db.WithContext(ctx).
		Where("channel_id = ? AND deleted = FALSE", channelID).
		Order("created_at DESC")
	if limit > 0 {
		q = q.Limit(limit).Offset(offset)
	}
	var out []Announcement
	if err := q.Find(&out).Error; err != nil {
		return nil, fmt.Errorf("list announcements: %w", err)
	}
	return out, nil
}

// SoftDelete flips deleted=true. Idempotent.
func (r *gormAnnouncementRepo) SoftDelete(ctx context.Context, id string) error {
	res := r.db.WithContext(ctx).Model(&Announcement{}).
		Where("id = ?", id).
		Updates(map[string]any{"deleted": true, "updated_at": gorm.Expr("now()")})
	if res.Error != nil {
		return fmt.Errorf("delete announcement: %w", res.Error)
	}
	if res.RowsAffected == 0 {
		return ErrNotFound
	}
	return nil
}

// AddAck inserts an ack row. Idempotent via ON CONFLICT DO NOTHING.
func (r *gormAnnouncementRepo) AddAck(ctx context.Context, announcementID string, userID string) error {
	ack := &AnnouncementAck{AnnouncementID: announcementID, UserID: userID}
	err := r.db.WithContext(ctx).Clauses(clause.OnConflict{
		Columns:   []clause.Column{{Name: "announcement_id"}, {Name: "user_id"}},
		DoNothing: true,
	}).Create(ack).Error
	if err != nil {
		return fmt.Errorf("add ack: %w", err)
	}
	return nil
}

// ListAcks returns acks for a given announcement, oldest-first.
func (r *gormAnnouncementRepo) ListAcks(ctx context.Context, announcementID string) ([]AnnouncementAck, error) {
	var acks []AnnouncementAck
	err := r.db.WithContext(ctx).
		Where("announcement_id = ?", announcementID).
		Order("acknowledged_at ASC").
		Find(&acks).Error
	if err != nil {
		return nil, fmt.Errorf("list acks: %w", err)
	}
	return acks, nil
}

var _ AnnouncementRepo = (*gormAnnouncementRepo)(nil)

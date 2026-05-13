package repo

import (
	"context"
	"errors"
	"fmt"
	"time"

	"gorm.io/gorm"
)

// QuickReply maps the quick_replies table — a per-user preset the client
// injects into normal messages.
type QuickReply struct {
	ID        string    `gorm:"primaryKey;type:text"                     json:"id"`
	UserID    string    `gorm:"column:user_id;type:text;not null"        json:"user_id"`
	Label     string    `gorm:"not null"                                 json:"label"`
	Content   string    `gorm:"not null"                                 json:"content"`
	SortOrder int       `gorm:"column:sort_order;not null;default:0"     json:"sort_order"`
	CreatedAt time.Time `gorm:"column:created_at;not null;default:now()" json:"created_at"`
	UpdatedAt time.Time `gorm:"column:updated_at;not null;default:now()" json:"updated_at"`
}

// TableName pins the GORM-derived table name.
func (QuickReply) TableName() string { return "quick_replies" }

// QuickReplyRepo is the data-access surface.
type QuickReplyRepo interface {
	Create(ctx context.Context, q *QuickReply) error
	GetByID(ctx context.Context, id string) (*QuickReply, error)
	ListByUser(ctx context.Context, userID string) ([]QuickReply, error)
	Update(ctx context.Context, id string, fields QuickReplyPatch) error
	Delete(ctx context.Context, id string) error
}

// QuickReplyPatch is the partial-update shape. nil field = "leave unchanged".
type QuickReplyPatch struct {
	Label     *string
	Content   *string
	SortOrder *int
}

type gormQuickReplyRepo struct{ db *gorm.DB }

// NewQuickReplyRepo returns a GORM-backed QuickReplyRepo.
func NewQuickReplyRepo(db *gorm.DB) QuickReplyRepo { return &gormQuickReplyRepo{db: db} }

// Create inserts a quick reply.
func (r *gormQuickReplyRepo) Create(ctx context.Context, q *QuickReply) error {
	if err := r.db.WithContext(ctx).Create(q).Error; err != nil {
		return fmt.Errorf("create quick reply: %w", err)
	}
	return nil
}

// GetByID returns the quick reply by PK.
func (r *gormQuickReplyRepo) GetByID(ctx context.Context, id string) (*QuickReply, error) {
	var q QuickReply
	if err := r.db.WithContext(ctx).First(&q, id).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, ErrNotFound
		}
		return nil, fmt.Errorf("get quick reply: %w", err)
	}
	return &q, nil
}

// ListByUser returns the user's quick replies ordered by sort_order ASC,
// falling back to id ASC for deterministic display when sort_order ties.
func (r *gormQuickReplyRepo) ListByUser(ctx context.Context, userID string) ([]QuickReply, error) {
	var out []QuickReply
	if err := r.db.WithContext(ctx).
		Where("user_id = ?", userID).
		Order("sort_order ASC, id ASC").
		Find(&out).Error; err != nil {
		return nil, fmt.Errorf("list quick replies: %w", err)
	}
	return out, nil
}

// Update applies the patch to a quick reply. An empty patch is a no-op.
func (r *gormQuickReplyRepo) Update(ctx context.Context, id string, fields QuickReplyPatch) error {
	updates := map[string]any{}
	if fields.Label != nil {
		updates["label"] = *fields.Label
	}
	if fields.Content != nil {
		updates["content"] = *fields.Content
	}
	if fields.SortOrder != nil {
		updates["sort_order"] = *fields.SortOrder
	}
	if len(updates) == 0 {
		return nil
	}
	updates["updated_at"] = gorm.Expr("now()")
	res := r.db.WithContext(ctx).Model(&QuickReply{}).
		Where("id = ?", id).
		Updates(updates)
	if res.Error != nil {
		return fmt.Errorf("update quick reply: %w", res.Error)
	}
	if res.RowsAffected == 0 {
		return ErrNotFound
	}
	return nil
}

// Delete removes the quick reply by PK.
func (r *gormQuickReplyRepo) Delete(ctx context.Context, id string) error {
	res := r.db.WithContext(ctx).Delete(&QuickReply{}, id)
	if res.Error != nil {
		return fmt.Errorf("delete quick reply: %w", res.Error)
	}
	if res.RowsAffected == 0 {
		return ErrNotFound
	}
	return nil
}

var _ QuickReplyRepo = (*gormQuickReplyRepo)(nil)

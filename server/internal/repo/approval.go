package repo

import (
	"context"
	"errors"
	"fmt"
	"time"

	"gorm.io/gorm"
)

// Approval status enum values — mirror the migration.
const (
	ApprovalStatusPending   int16 = 0
	ApprovalStatusApproved  int16 = 1
	ApprovalStatusRejected  int16 = 2
	ApprovalStatusCancelled int16 = 3
)

// Approval maps the approvals table. Props is a JSONB blob carried as a raw
// string — callers decide the concrete schema. DecidedAt / DecisionNote are
// filled in by Decide(); they stay nil/empty while the approval is pending.
type Approval struct {
	ID           string     `gorm:"primaryKey;type:text"                           json:"id"`
	ChannelID    string     `gorm:"column:channel_id;type:text;not null"           json:"channel_id"`
	RequesterID  string     `gorm:"column:requester_id;type:text;not null"         json:"requester_id"`
	ApproverID   string     `gorm:"column:approver_id;type:text;not null"          json:"approver_id"`
	Subject      string     `gorm:"not null"                                       json:"subject"`
	Content      string     `gorm:"not null"                                       json:"content"`
	Props        string     `gorm:"type:jsonb;not null;default:'{}'"               json:"props"`
	Status       int16      `gorm:"not null;default:0"                             json:"status"`
	DecidedAt    *time.Time `gorm:"column:decided_at"                              json:"decided_at,omitempty"`
	DecisionNote string     `gorm:"column:decision_note"                           json:"decision_note,omitempty"`
	CreatedAt    time.Time  `gorm:"column:created_at;not null;default:now()"       json:"created_at"`
	UpdatedAt    time.Time  `gorm:"column:updated_at;not null;default:now()"       json:"updated_at"`
}

// TableName pins the GORM-derived table name to the migration.
func (Approval) TableName() string { return "approvals" }

// ApprovalRepo is the data-access surface for approvals. Status transitions
// are enforced at the SQL layer (WHERE status = pending) so concurrent approve
// + cancel can't both succeed.
type ApprovalRepo interface {
	Create(ctx context.Context, a *Approval) error
	GetByID(ctx context.Context, id string) (*Approval, error)
	Decide(ctx context.Context, id string, status int16, note string, decidedAt time.Time) error
	Cancel(ctx context.Context, id string, requesterID string) error

	// ListByApprover returns approvals where approver_id = approverID. When
	// statusFilter >= 0, only rows with that status are returned; passing -1
	// returns every status. Pagination uses (limit, cursor) where cursor is
	// the last id seen (pass "" to start).
	ListByApprover(ctx context.Context, approverID string, statusFilter int16, limit int, cursor string) ([]Approval, error)

	// ListByRequester is the requester-facing inbox. Pagination semantics
	// match ListByApprover.
	ListByRequester(ctx context.Context, requesterID string, limit int, cursor string) ([]Approval, error)
}

type gormApprovalRepo struct{ db *gorm.DB }

// NewApprovalRepo returns a GORM-backed ApprovalRepo.
func NewApprovalRepo(db *gorm.DB) ApprovalRepo { return &gormApprovalRepo{db: db} }

// Create inserts a new approval in pending status. Callers set Channel/
// Requester/Approver/Subject/Content/Props; the DB fills id/timestamps/status.
func (r *gormApprovalRepo) Create(ctx context.Context, a *Approval) error {
	if a.Props == "" {
		a.Props = "{}"
	}
	a.Status = ApprovalStatusPending
	if err := r.db.WithContext(ctx).Create(a).Error; err != nil {
		return fmt.Errorf("create approval: %w", err)
	}
	return nil
}

// GetByID returns the approval by primary key. ErrNotFound if missing.
func (r *gormApprovalRepo) GetByID(ctx context.Context, id string) (*Approval, error) {
	var a Approval
	if err := r.db.WithContext(ctx).First(&a, id).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, ErrNotFound
		}
		return nil, fmt.Errorf("get approval: %w", err)
	}
	return &a, nil
}

// Decide transitions a pending approval to approved/rejected and records the
// decision metadata. The WHERE status = pending guard prevents double-decision
// races — RowsAffected = 0 means someone else won the transition; callers map
// that to ErrNotFound (the row may exist but isn't in a transitional state).
func (r *gormApprovalRepo) Decide(ctx context.Context, id string, status int16, note string, decidedAt time.Time) error {
	res := r.db.WithContext(ctx).Model(&Approval{}).
		Where("id = ? AND status = ?", id, ApprovalStatusPending).
		Updates(map[string]any{
			"status":        status,
			"decided_at":    decidedAt,
			"decision_note": note,
			"updated_at":    gorm.Expr("now()"),
		})
	if res.Error != nil {
		return fmt.Errorf("decide approval: %w", res.Error)
	}
	if res.RowsAffected == 0 {
		return ErrNotFound
	}
	return nil
}

// Cancel transitions a pending approval to cancelled. Only the original
// requester may cancel — the WHERE clause enforces this alongside the status
// guard. RowsAffected = 0 → ErrNotFound (row missing, already decided, or
// requester_id mismatch).
func (r *gormApprovalRepo) Cancel(ctx context.Context, id string, requesterID string) error {
	res := r.db.WithContext(ctx).Model(&Approval{}).
		Where("id = ? AND status = ? AND requester_id = ?", id, ApprovalStatusPending, requesterID).
		Updates(map[string]any{
			"status":     ApprovalStatusCancelled,
			"updated_at": gorm.Expr("now()"),
		})
	if res.Error != nil {
		return fmt.Errorf("cancel approval: %w", res.Error)
	}
	if res.RowsAffected == 0 {
		return ErrNotFound
	}
	return nil
}

// ListByApprover returns approvals addressed to approverID. statusFilter = -1
// means all; otherwise filter by exact status. Ordered by id DESC (stable
// cursor pagination — older rows have smaller ids).
func (r *gormApprovalRepo) ListByApprover(ctx context.Context, approverID string, statusFilter int16, limit int, cursor string) ([]Approval, error) {
	q := r.db.WithContext(ctx).
		Where("approver_id = ?", approverID).
		Order("id DESC")
	if statusFilter >= 0 {
		q = q.Where("status = ?", statusFilter)
	}
	if cursor != "" {
		q = q.Where("id < ?", cursor)
	}
	if limit > 0 {
		q = q.Limit(limit)
	}
	var out []Approval
	if err := q.Find(&out).Error; err != nil {
		return nil, fmt.Errorf("list by approver: %w", err)
	}
	return out, nil
}

// ListByRequester returns approvals filed by requesterID.
func (r *gormApprovalRepo) ListByRequester(ctx context.Context, requesterID string, limit int, cursor string) ([]Approval, error) {
	q := r.db.WithContext(ctx).
		Where("requester_id = ?", requesterID).
		Order("id DESC")
	if cursor != "" {
		q = q.Where("id < ?", cursor)
	}
	if limit > 0 {
		q = q.Limit(limit)
	}
	var out []Approval
	if err := q.Find(&out).Error; err != nil {
		return nil, fmt.Errorf("list by requester: %w", err)
	}
	return out, nil
}

var _ ApprovalRepo = (*gormApprovalRepo)(nil)

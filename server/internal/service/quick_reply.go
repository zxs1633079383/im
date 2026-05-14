package service

import (
	"context"
	"errors"

	"im-server/internal/repo"
)

// Quick-reply service sentinels.
var (
	ErrQuickReplyLabelEmpty   = errors.New("label is required")
	ErrQuickReplyContentEmpty = errors.New("content is required")
	ErrQuickReplyNotOwner     = errors.New("you do not own this quick reply")
)

// QuickReplyService enforces per-user isolation — every mutating call refuses
// when the record's user_id doesn't match the caller.
type QuickReplyService struct {
	replies repo.QuickReplyRepo
}

// NewQuickReplyService wires deps.
func NewQuickReplyService(replies repo.QuickReplyRepo) *QuickReplyService {
	return &QuickReplyService{replies: replies}
}

// CreateQuickReplyParams is the input to Create.
type CreateQuickReplyParams struct {
	UserID    string
	Label     string
	Content   string
	SortOrder int
}

// Create inserts a new quick reply owned by UserID.
func (s *QuickReplyService) Create(ctx context.Context, p CreateQuickReplyParams) (*repo.QuickReply, error) {
	ctx, span := tracer.Start(ctx, "QuickReplyService.Create")
	defer span.End()

	if p.Label == "" {
		return nil, ErrQuickReplyLabelEmpty
	}
	if p.Content == "" {
		return nil, ErrQuickReplyContentEmpty
	}
	q := &repo.QuickReply{
		UserID:    p.UserID,
		Label:     p.Label,
		Content:   p.Content,
		SortOrder: p.SortOrder,
	}
	if err := s.replies.Create(ctx, q); err != nil {
		return nil, err
	}
	return q, nil
}

// List returns the caller's quick replies.
func (s *QuickReplyService) List(ctx context.Context, callerID string) ([]repo.QuickReply, error) {
	ctx, span := tracer.Start(ctx, "QuickReplyService.List")
	defer span.End()

	return s.replies.ListByUser(ctx, callerID)
}

// Update patches fields on a quick reply. Caller must be the owner.
func (s *QuickReplyService) Update(ctx context.Context, id string, callerID string, patch repo.QuickReplyPatch) (*repo.QuickReply, error) {
	ctx, span := tracer.Start(ctx, "QuickReplyService.Update")
	defer span.End()

	q, err := s.replies.GetByID(ctx, id)
	if err != nil {
		return nil, err
	}
	if q.UserID != callerID {
		return nil, ErrQuickReplyNotOwner
	}
	if err := s.replies.Update(ctx, id, patch); err != nil {
		return nil, err
	}
	return s.replies.GetByID(ctx, id)
}

// Delete removes a quick reply. Caller must be the owner.
func (s *QuickReplyService) Delete(ctx context.Context, id string, callerID string) error {
	ctx, span := tracer.Start(ctx, "QuickReplyService.Delete")
	defer span.End()

	q, err := s.replies.GetByID(ctx, id)
	if err != nil {
		return err
	}
	if q.UserID != callerID {
		return ErrQuickReplyNotOwner
	}
	return s.replies.Delete(ctx, id)
}

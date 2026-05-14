package service

import (
	"context"
	"errors"
	"fmt"
	"time"

	"im-server/internal/repo"
)

// Approval-service sentinels.
var (
	ErrApprovalSubjectEmpty = errors.New("subject is required")
	ErrApprovalContentEmpty = errors.New("content is required")
	ErrApprovalNotApprover  = errors.New("only the designated approver may decide")
	ErrApprovalNotRequester = errors.New("only the requester may cancel")
	ErrApprovalNotPending   = errors.New("approval is not pending")
)

// ApprovalService orchestrates channel-scoped approvals. Permission shape:
//
//   - Create: requester AND approver must both be members of the channel;
//     approver must additionally be a manager+ of the channel.
//   - Approve/Reject: caller must be the designated approver_id.
//   - Cancel: caller must be the requester AND status = pending.
//   - Get / List: the requester or the approver may see; other users 403.
type ApprovalService struct {
	approvals repo.ApprovalRepo
	channels  channelMemberStore
	mgr       managerCheck
}

// NewApprovalService wires deps.
func NewApprovalService(
	approvals repo.ApprovalRepo,
	channels channelMemberStore,
	governance managerCheck,
) *ApprovalService {
	return &ApprovalService{
		approvals: approvals,
		channels:  channels,
		mgr:       governance,
	}
}

// CreateApprovalParams is the input to Create.
type CreateApprovalParams struct {
	ChannelID   string
	RequesterID string
	ApproverID  string
	Subject     string
	Content     string
	Props       string
}

// Create inserts a new approval. Both users must be channel members and the
// approver must be manager+. Returns the persisted row with ID + timestamps.
func (s *ApprovalService) Create(ctx context.Context, p CreateApprovalParams) (*repo.Approval, error) {
	ctx, span := tracer.Start(ctx, "ApprovalService.Create")
	defer span.End()

	if p.Subject == "" {
		return nil, ErrApprovalSubjectEmpty
	}
	if p.Content == "" {
		return nil, ErrApprovalContentEmpty
	}
	// Requester must be a member.
	if err := s.requireMember(ctx, p.ChannelID, p.RequesterID); err != nil {
		return nil, err
	}
	// Approver must be manager+ (which implies membership).
	ok, err := s.mgr.IsManagerOrOwner(ctx, p.ChannelID, p.ApproverID)
	if err != nil {
		return nil, fmt.Errorf("check approver manager: %w", err)
	}
	if !ok {
		return nil, ErrForbidden
	}
	a := &repo.Approval{
		ChannelID:   p.ChannelID,
		RequesterID: p.RequesterID,
		ApproverID:  p.ApproverID,
		Subject:     p.Subject,
		Content:     p.Content,
		Props:       p.Props,
	}
	if err := s.approvals.Create(ctx, a); err != nil {
		return nil, err
	}
	return a, nil
}

// Approve decides id as approved with an optional note. Only the designated
// approver may call. Returns the refreshed row so the transport can broadcast.
func (s *ApprovalService) Approve(ctx context.Context, id string, callerID, note string) (*repo.Approval, error) {
	ctx, span := tracer.Start(ctx, "ApprovalService.Approve")
	defer span.End()

	return s.decide(ctx, id, callerID, repo.ApprovalStatusApproved, note)
}

// Reject decides id as rejected with an optional note. Only the approver may
// call.
func (s *ApprovalService) Reject(ctx context.Context, id string, callerID, note string) (*repo.Approval, error) {
	ctx, span := tracer.Start(ctx, "ApprovalService.Reject")
	defer span.End()

	return s.decide(ctx, id, callerID, repo.ApprovalStatusRejected, note)
}

// decide is the shared code path for Approve/Reject. Fetches + authorises +
// transitions + re-reads. The WHERE status = pending guard inside Decide()
// protects against concurrent approve/reject/cancel.
func (s *ApprovalService) decide(ctx context.Context, id string, callerID string, status int16, note string) (*repo.Approval, error) {
	a, err := s.approvals.GetByID(ctx, id)
	if err != nil {
		return nil, err
	}
	if a.ApproverID != callerID {
		return nil, ErrApprovalNotApprover
	}
	if a.Status != repo.ApprovalStatusPending {
		return nil, ErrApprovalNotPending
	}
	if err := s.approvals.Decide(ctx, id, status, note, time.Now().UTC()); err != nil {
		// Decide returns ErrNotFound when the guarded UPDATE affected 0 rows,
		// which here means a concurrent decision won — surface as "not
		// pending" so callers get a meaningful error.
		if errors.Is(err, repo.ErrNotFound) {
			return nil, ErrApprovalNotPending
		}
		return nil, err
	}
	return s.approvals.GetByID(ctx, id)
}

// Cancel transitions the approval to cancelled. Caller must be the requester,
// status must be pending. Returns the refreshed row.
func (s *ApprovalService) Cancel(ctx context.Context, id string, callerID string) (*repo.Approval, error) {
	ctx, span := tracer.Start(ctx, "ApprovalService.Cancel")
	defer span.End()

	a, err := s.approvals.GetByID(ctx, id)
	if err != nil {
		return nil, err
	}
	if a.RequesterID != callerID {
		return nil, ErrApprovalNotRequester
	}
	if a.Status != repo.ApprovalStatusPending {
		return nil, ErrApprovalNotPending
	}
	if err := s.approvals.Cancel(ctx, id, callerID); err != nil {
		if errors.Is(err, repo.ErrNotFound) {
			return nil, ErrApprovalNotPending
		}
		return nil, err
	}
	return s.approvals.GetByID(ctx, id)
}

// Get returns the approval. Only the requester or the approver may view.
func (s *ApprovalService) Get(ctx context.Context, id string, callerID string) (*repo.Approval, error) {
	ctx, span := tracer.Start(ctx, "ApprovalService.Get")
	defer span.End()

	a, err := s.approvals.GetByID(ctx, id)
	if err != nil {
		return nil, err
	}
	if a.RequesterID != callerID && a.ApproverID != callerID {
		return nil, ErrForbidden
	}
	return a, nil
}

// ListPending returns the caller's pending-approver inbox.
func (s *ApprovalService) ListPending(ctx context.Context, callerID string, limit int, cursor string) ([]repo.Approval, error) {
	ctx, span := tracer.Start(ctx, "ApprovalService.ListPending")
	defer span.End()

	return s.approvals.ListByApprover(ctx, callerID, repo.ApprovalStatusPending, limit, cursor)
}

// ListMine returns approvals filed by the caller (any status).
func (s *ApprovalService) ListMine(ctx context.Context, callerID string, limit int, cursor string) ([]repo.Approval, error) {
	ctx, span := tracer.Start(ctx, "ApprovalService.ListMine")
	defer span.End()

	return s.approvals.ListByRequester(ctx, callerID, limit, cursor)
}

// requireMember mirrors the other services' membership check.
func (s *ApprovalService) requireMember(ctx context.Context, channelID string, callerID string) error {
	if _, err := s.channels.GetMember(ctx, channelID, callerID); err != nil {
		if errors.Is(err, repo.ErrNotFound) {
			return ErrNotMember
		}
		return fmt.Errorf("check member: %w", err)
	}
	return nil
}

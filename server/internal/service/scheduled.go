package service

import (
	"context"
	"errors"
	"fmt"
	"time"

	"im-server/internal/repo"
)

// Scheduled-service sentinels.
var (
	ErrScheduledContentEmpty   = errors.New("content is required")
	ErrScheduledTimeInPast     = errors.New("scheduled_at must be at least 60 seconds in the future")
	ErrScheduledNotSender      = errors.New("only the sender may cancel")
	ErrScheduledNotPending     = errors.New("scheduled message is not pending")
)

// scheduledMsgSender is the minimal MessageService surface the scheduled
// deliverer needs. Consumer-side small interface.
type scheduledMsgSender interface {
	SendMessage(ctx context.Context, p SendParams) (*repo.Message, error)
}

// ScheduledService owns the CRUD + deliverer for scheduled messages. The
// Deliver method is invoked by ScheduledWorker (background goroutine) once
// per pending row that's due; it calls through MessageService.SendMessage so
// the standard permission / fan-out path runs unchanged.
type ScheduledService struct {
	scheduled repo.ScheduledRepo
	channels  channelMemberStore
	sender    scheduledMsgSender
}

// NewScheduledService wires deps. sender is typically a *MessageService.
func NewScheduledService(
	scheduled repo.ScheduledRepo,
	channels channelMemberStore,
	sender scheduledMsgSender,
) *ScheduledService {
	return &ScheduledService{
		scheduled: scheduled,
		channels:  channels,
		sender:    sender,
	}
}

// ScheduledCreateParams is the input to Create.
type ScheduledCreateParams struct {
	ChannelID   int64
	SenderID    int64
	Content     string
	MsgType     int16
	VisibleTo   []int64
	ReplyTo     *int64
	FileIDs     []int64
	ScheduledAt time.Time
}

// Create validates + inserts a pending scheduled message. scheduled_at must be
// at least 60 seconds in the future; sender must be a member of channel.
func (s *ScheduledService) Create(ctx context.Context, p ScheduledCreateParams) (*repo.ScheduledMessage, error) {
	ctx, span := tracer.Start(ctx, "ScheduledService.Create")
	defer span.End()

	if p.Content == "" {
		return nil, ErrScheduledContentEmpty
	}
	if p.ScheduledAt.Before(time.Now().Add(60 * time.Second)) {
		return nil, ErrScheduledTimeInPast
	}
	if err := s.requireMember(ctx, p.ChannelID, p.SenderID); err != nil {
		return nil, err
	}
	msgType := p.MsgType
	if msgType == 0 {
		msgType = repo.MsgTypeText
	}
	sm := &repo.ScheduledMessage{
		ChannelID:   p.ChannelID,
		SenderID:    p.SenderID,
		Content:     p.Content,
		MsgType:     msgType,
		VisibleTo:   p.VisibleTo,
		ReplyTo:     p.ReplyTo,
		FileIDs:     p.FileIDs,
		ScheduledAt: p.ScheduledAt,
	}
	if err := s.scheduled.Create(ctx, sm); err != nil {
		return nil, err
	}
	return sm, nil
}

// Cancel transitions a pending scheduled message to cancelled. Only the
// sender may cancel.
func (s *ScheduledService) Cancel(ctx context.Context, id, callerID int64) error {
	ctx, span := tracer.Start(ctx, "ScheduledService.Cancel")
	defer span.End()

	sm, err := s.scheduled.GetByID(ctx, id)
	if err != nil {
		return err
	}
	if sm.SenderID != callerID {
		return ErrScheduledNotSender
	}
	if sm.Status != repo.ScheduledStatusPending {
		return ErrScheduledNotPending
	}
	if err := s.scheduled.Cancel(ctx, id, callerID); err != nil {
		if errors.Is(err, repo.ErrNotFound) {
			return ErrScheduledNotPending
		}
		return err
	}
	return nil
}

// List returns the caller's queue. statusFilter -1 = all.
func (s *ScheduledService) List(ctx context.Context, callerID int64, statusFilter int16, limit int, cursor int64) ([]repo.ScheduledMessage, error) {
	ctx, span := tracer.Start(ctx, "ScheduledService.List")
	defer span.End()

	return s.scheduled.ListBySender(ctx, callerID, statusFilter, limit, cursor)
}

// FetchDue is the worker-facing accessor — pure delegate so the worker can
// consume one tiny interface (ScheduledDeliverer).
func (s *ScheduledService) FetchDue(ctx context.Context, now time.Time, limit int) ([]repo.ScheduledMessage, error) {
	ctx, span := tracer.Start(ctx, "ScheduledService.FetchDue")
	defer span.End()

	return s.scheduled.FetchDue(ctx, now, limit)
}

// Deliver takes one pending ScheduledMessage, calls MessageService.SendMessage
// to produce the real message, then marks the scheduled row delivered. On
// SendMessage failure, marks the row failed and returns the error. Callers
// (worker + tests) may invoke Deliver directly to bypass the poller timing.
func (s *ScheduledService) Deliver(ctx context.Context, sm *repo.ScheduledMessage) (*repo.Message, error) {
	ctx, span := tracer.Start(ctx, "ScheduledService.Deliver")
	defer span.End()

	if sm == nil {
		return nil, errors.New("nil scheduled message")
	}
	if sm.Status != repo.ScheduledStatusPending {
		return nil, ErrScheduledNotPending
	}
	msg, err := s.sender.SendMessage(ctx, SendParams{
		ChannelID: sm.ChannelID,
		SenderID:  sm.SenderID,
		Content:   sm.Content,
		MsgType:   sm.MsgType,
		VisibleTo: []int64(sm.VisibleTo),
		ReplyTo:   sm.ReplyTo,
		FileIDs:   []int64(sm.FileIDs),
		// Synthesise a client_msg_id so the idempotency guard doesn't collide
		// across retries of the same scheduled row.
		ClientMsgID: fmt.Sprintf("sched-%d-%d", sm.ID, time.Now().UnixNano()),
	})
	if err != nil {
		_ = s.scheduled.MarkFailed(ctx, sm.ID, err.Error())
		return nil, err
	}
	if err := s.scheduled.MarkDelivered(ctx, sm.ID, msg.ID); err != nil {
		// The message was sent but the bookkeeping row couldn't be updated —
		// surface the error so the caller can log, but don't roll back the
		// already-delivered message.
		return msg, fmt.Errorf("mark delivered: %w", err)
	}
	return msg, nil
}

// requireMember is a local copy — same semantics as the other services.
func (s *ScheduledService) requireMember(ctx context.Context, channelID, callerID int64) error {
	if _, err := s.channels.GetMember(ctx, channelID, callerID); err != nil {
		if errors.Is(err, repo.ErrNotFound) {
			return ErrNotMember
		}
		return fmt.Errorf("check member: %w", err)
	}
	return nil
}

package service

import (
	"context"
	"errors"
	"fmt"

	"im-server/internal/repo"
)

// Urgent-service sentinels.
var (
	ErrUrgentContentEmpty = errors.New("content is required")
	ErrNotUrgentMsg       = errors.New("message is not urgent")
	ErrNotSender          = errors.New("only the sender can cancel")
)

// messageSender is the minimal MessageService surface UrgentService needs.
// Consumer-side small interface — avoids pulling in the full MessageService.
type messageSender interface {
	SendMessage(ctx context.Context, p SendParams) (*repo.Message, error)
}

// msgLookup fetches a message by ID — used for permission checks on confirm/
// cancel. Only the methods we actually need.
type msgLookup interface {
	GetByID(ctx context.Context, id int64) (*repo.Message, error)
}

// UrgentService orchestrates urgent messaging: send (which wraps the normal
// send + flip is_urgent), confirm (recipient clears their badge), and cancel
// (sender/manager clears the urgent flag).
type UrgentService struct {
	urgent   repo.UrgentRepo
	messages msgLookup
	channels channelMemberStore
	sender   messageSender
	mgr      managerCheck
}

// NewUrgentService wires the deps. sender is typically a *MessageService.
func NewUrgentService(
	urgent repo.UrgentRepo,
	messages msgLookup,
	channels channelMemberStore,
	sender messageSender,
	governance managerCheck,
) *UrgentService {
	return &UrgentService{
		urgent:   urgent,
		messages: messages,
		channels: channels,
		sender:   sender,
		mgr:      governance,
	}
}

// SendUrgent sends content as a new message, then flips is_urgent=TRUE. The
// returned message has IsUrgent set so the broadcaster payload reflects the
// flag to clients. Requires caller to be a channel member.
func (s *UrgentService) SendUrgent(
	ctx context.Context, channelID int64, senderID, content, clientMsgID string,
) (*repo.Message, error) {
	ctx, span := tracer.Start(ctx, "UrgentService.SendUrgent")
	defer span.End()

	if content == "" {
		return nil, ErrUrgentContentEmpty
	}
	msg, err := s.sender.SendMessage(ctx, SendParams{
		ChannelID:   channelID,
		SenderID:    senderID,
		Content:     content,
		ClientMsgID: clientMsgID,
		MsgType:     repo.MsgTypeText,
	})
	if err != nil {
		return nil, err
	}
	if err := s.urgent.SetUrgent(ctx, msg.ID); err != nil {
		return nil, fmt.Errorf("mark urgent: %w", err)
	}
	msg.IsUrgent = true
	return msg, nil
}

// ConfirmUrgent records the caller's confirmation for msgID. Caller must be
// a member of the message's channel.
func (s *UrgentService) ConfirmUrgent(ctx context.Context, msgID int64, callerID string) error {
	ctx, span := tracer.Start(ctx, "UrgentService.ConfirmUrgent")
	defer span.End()

	m, err := s.messages.GetByID(ctx, msgID)
	if err != nil {
		return err
	}
	if !m.IsUrgent {
		return ErrNotUrgentMsg
	}
	if err := s.requireMember(ctx, m.ChannelID, callerID); err != nil {
		return err
	}
	return s.urgent.AddConfirmation(ctx, msgID, callerID)
}

// CancelUrgent clears the urgent flag on msgID. Allowed when caller is the
// original sender, OR caller is manager/owner of the channel.
func (s *UrgentService) CancelUrgent(ctx context.Context, msgID int64, callerID string) error {
	ctx, span := tracer.Start(ctx, "UrgentService.CancelUrgent")
	defer span.End()

	m, err := s.messages.GetByID(ctx, msgID)
	if err != nil {
		return err
	}
	if !m.IsUrgent {
		return nil // idempotent no-op
	}
	if m.SenderID != callerID {
		ok, err := s.mgr.IsManagerOrOwner(ctx, m.ChannelID, callerID)
		if err != nil {
			return fmt.Errorf("check manager: %w", err)
		}
		if !ok {
			if err := s.requireMember(ctx, m.ChannelID, callerID); err != nil {
				return err
			}
			return ErrNotSender
		}
	}
	return s.urgent.ClearUrgent(ctx, msgID)
}

// ListConfirmations returns the user IDs that have confirmed msgID. Any member
// of the channel may see the list.
func (s *UrgentService) ListConfirmations(ctx context.Context, msgID int64, callerID string) ([]string, error) {
	ctx, span := tracer.Start(ctx, "UrgentService.ListConfirmations")
	defer span.End()

	m, err := s.messages.GetByID(ctx, msgID)
	if err != nil {
		return nil, err
	}
	if err := s.requireMember(ctx, m.ChannelID, callerID); err != nil {
		return nil, err
	}
	return s.urgent.ListConfirmations(ctx, msgID)
}

// requireMember mirrors the other services' member check.
func (s *UrgentService) requireMember(ctx context.Context, channelID int64, callerID string) error {
	if _, err := s.channels.GetMember(ctx, channelID, callerID); err != nil {
		if errors.Is(err, repo.ErrNotFound) {
			return ErrNotMember
		}
		return fmt.Errorf("check member: %w", err)
	}
	return nil
}

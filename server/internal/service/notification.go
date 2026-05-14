package service

import (
	"context"
	"errors"

	"im-server/internal/auth"
	"im-server/internal/repo"
)

// Notification-service sentinels.
var (
	ErrNotificationTitleEmpty  = errors.New("title is required")
	ErrNotificationBadReceiver = errors.New("receiver_id is required")
)

// NotificationService orchestrates per-user notifications. M4: receiver
// validation is purely format-based (24-hex mm UserID) — there is no longer
// a local users table to confirm against.
type NotificationService struct {
	notifications repo.NotificationRepo
}

// NewNotificationService wires deps.
func NewNotificationService(notifications repo.NotificationRepo) *NotificationService {
	return &NotificationService{notifications: notifications}
}

// NotificationSendParams is the input to Send.
type NotificationSendParams struct {
	SenderID   string
	ReceiverID string
	Title      string
	Body       string
	Type       int16
	Props      string
}

// Send inserts a new notification.
func (s *NotificationService) Send(ctx context.Context, p NotificationSendParams) (*repo.Notification, error) {
	ctx, span := tracer.Start(ctx, "NotificationService.Send")
	defer span.End()

	if p.Title == "" {
		return nil, ErrNotificationTitleEmpty
	}
	if err := auth.ValidateUserID(p.ReceiverID); err != nil {
		return nil, ErrNotificationBadReceiver
	}
	n := &repo.Notification{
		SenderID:   p.SenderID,
		ReceiverID: p.ReceiverID,
		Title:      p.Title,
		Body:       p.Body,
		Type:       p.Type,
		Props:      p.Props,
	}
	if err := s.notifications.Create(ctx, n); err != nil {
		return nil, err
	}
	return n, nil
}

// ListReceived returns the caller's inbox.
func (s *NotificationService) ListReceived(ctx context.Context, receiverID string, unreadOnly bool, limit int, cursor string) ([]repo.Notification, error) {
	ctx, span := tracer.Start(ctx, "NotificationService.ListReceived")
	defer span.End()

	return s.notifications.ListReceived(ctx, receiverID, unreadOnly, limit, cursor)
}

// ListSent returns the caller's outbox.
func (s *NotificationService) ListSent(ctx context.Context, senderID string, limit int, cursor string) ([]repo.Notification, error) {
	ctx, span := tracer.Start(ctx, "NotificationService.ListSent")
	defer span.End()

	return s.notifications.ListSent(ctx, senderID, limit, cursor)
}

// MarkRead marks a notification read. Only the receiver may mark.
func (s *NotificationService) MarkRead(ctx context.Context, id string, callerID string) error {
	ctx, span := tracer.Start(ctx, "NotificationService.MarkRead")
	defer span.End()

	return s.notifications.MarkRead(ctx, id, callerID)
}

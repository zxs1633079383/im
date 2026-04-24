package service

import (
	"context"
	"errors"

	"im-server/internal/repo"
)

// Notification-service sentinels.
var (
	ErrNotificationTitleEmpty = errors.New("title is required")
	ErrNotificationBadReceiver = errors.New("receiver_id is required")
)

// NotificationService orchestrates per-user notifications. No channel
// membership check — notifications are user→user; the caller supplies the
// receiver explicitly.
type NotificationService struct {
	notifications repo.NotificationRepo
	users         userExistsCheck
}

// userExistsCheck is the minimal UserRepo surface NotificationService needs to
// guard against sending to a non-existent receiver.
type userExistsCheck interface {
	GetByID(ctx context.Context, id int64) (*repo.User, error)
}

// NewNotificationService wires deps.
func NewNotificationService(notifications repo.NotificationRepo, users userExistsCheck) *NotificationService {
	return &NotificationService{notifications: notifications, users: users}
}

// SendParams is the input to Send.
type NotificationSendParams struct {
	SenderID   int64
	ReceiverID int64
	Title      string
	Body       string
	Type       int16
	Props      string
}

// Send inserts a new notification. Returns the persisted row so the transport
// can echo it back and fan the WS event to the receiver.
func (s *NotificationService) Send(ctx context.Context, p NotificationSendParams) (*repo.Notification, error) {
	ctx, span := tracer.Start(ctx, "NotificationService.Send")
	defer span.End()

	if p.Title == "" {
		return nil, ErrNotificationTitleEmpty
	}
	if p.ReceiverID == 0 {
		return nil, ErrNotificationBadReceiver
	}
	if _, err := s.users.GetByID(ctx, p.ReceiverID); err != nil {
		if errors.Is(err, repo.ErrNotFound) {
			return nil, ErrNotificationBadReceiver
		}
		return nil, err
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
func (s *NotificationService) ListReceived(ctx context.Context, receiverID int64, unreadOnly bool, limit int, cursor int64) ([]repo.Notification, error) {
	ctx, span := tracer.Start(ctx, "NotificationService.ListReceived")
	defer span.End()

	return s.notifications.ListReceived(ctx, receiverID, unreadOnly, limit, cursor)
}

// ListSent returns the caller's outbox.
func (s *NotificationService) ListSent(ctx context.Context, senderID int64, limit int, cursor int64) ([]repo.Notification, error) {
	ctx, span := tracer.Start(ctx, "NotificationService.ListSent")
	defer span.End()

	return s.notifications.ListSent(ctx, senderID, limit, cursor)
}

// MarkRead marks a notification read. Only the receiver may mark.
func (s *NotificationService) MarkRead(ctx context.Context, id, callerID int64) error {
	ctx, span := tracer.Start(ctx, "NotificationService.MarkRead")
	defer span.End()

	return s.notifications.MarkRead(ctx, id, callerID)
}

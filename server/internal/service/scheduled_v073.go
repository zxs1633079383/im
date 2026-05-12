package service

import (
	"context"

	"im-server/internal/repo"
)

// ScheduledEventPusher delivers schedule_created / schedule_canceled WS
// frames to the caller's other devices (v0.7.3 gap #7). Implemented in
// cmd/gateway/main.go on top of crossPodDeps.dispatch. The method name is
// intentionally distinct from http.UserEventPusher.PushToUser so a single
// hub-side adapter can satisfy both surfaces without name collision.
type ScheduledEventPusher interface {
	PushUserEvent(userID string, eventType string, payload any)
}

// ScheduledEventName aliases plain string so http.MessageEventType-style
// constants stay decoupled from the service boundary.
type ScheduledEventName = string

const (
	// ScheduledEventCreated maps to gateway.TypeScheduleCreated.
	ScheduledEventCreated ScheduledEventName = "schedule_created"
	// ScheduledEventCanceled maps to gateway.TypeScheduleCanceled.
	ScheduledEventCanceled ScheduledEventName = "schedule_canceled"
)

// ScheduledEventPayload is the wire body for schedule_created /
// schedule_canceled. cses-client uses `has_schedule_post` to flip
// `dialog.hasSchedulePost` across the sender's devices.
type ScheduledEventPayload struct {
	ChannelID       int64 `json:"channel_id"`
	ScheduledID     int64 `json:"scheduled_id"`
	HasSchedulePost bool  `json:"has_schedule_post"`
}

// AttachUserPusher wires the multi-device fan-out hook. Call once at startup.
func (s *ScheduledService) AttachUserPusher(p ScheduledEventPusher) {
	s.pusher = p
}

// fanScheduleEvent best-effort pushes a schedule_* frame to the sender's
// other devices. nil-safe: skips entirely when no pusher is wired.
func (s *ScheduledService) fanScheduleEvent(
	senderID string,
	event ScheduledEventName,
	sm *repo.ScheduledMessage,
	hasSchedulePost bool,
) {
	if s.pusher == nil || sm == nil {
		return
	}
	s.pusher.PushUserEvent(senderID, event, ScheduledEventPayload{
		ChannelID:       sm.ChannelID,
		ScheduledID:     sm.ID,
		HasSchedulePost: hasSchedulePost,
	})
}

// hasPendingForChannel reports whether the sender still owns any pending
// scheduled message in channelID. Used to decide the post-cancel value of
// `has_schedule_post` so the badge converges to false when the last pending
// row is cleared.
func (s *ScheduledService) hasPendingForChannel(
	ctx context.Context, senderID string, channelID int64,
) bool {
	list, err := s.scheduled.ListBySender(ctx, senderID, repo.ScheduledStatusPending, 1, 0)
	if err != nil {
		return true // fail-open: assume still pending so badge does not vanish on a transient DB error
	}
	for _, sm := range list {
		if sm.ChannelID == channelID {
			return true
		}
	}
	return false
}

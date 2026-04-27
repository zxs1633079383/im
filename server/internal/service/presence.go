package service

import (
	"context"
	"errors"
	"fmt"

	"im-server/internal/repo"
)

// RoutingPresence is the minimal view of the Redis routing table the presence
// service needs. *repo.Routing implements it; tests pass fakes.
type RoutingPresence interface {
	DevicesForUser(ctx context.Context, userID string) (map[string]string, error)
}

// PresenceService answers "who is currently online in channel X".
type PresenceService struct {
	channels repo.ChannelRepo
	routing  RoutingPresence
}

// NewPresenceService wires the repo + routing backend.
func NewPresenceService(channels repo.ChannelRepo, routing RoutingPresence) *PresenceService {
	return &PresenceService{channels: channels, routing: routing}
}

// OnlineUsersInChannel returns the mm UserIDs of channelID's members who have
// at least one registered gateway connection. Caller must be a channel member.
func (s *PresenceService) OnlineUsersInChannel(ctx context.Context, channelID int64, callerID string) ([]string, error) {
	ctx, span := tracer.Start(ctx, "PresenceService.OnlineUsersInChannel")
	defer span.End()

	if _, err := s.channels.GetMember(ctx, channelID, callerID); err != nil {
		if errors.Is(err, repo.ErrNotFound) {
			return nil, ErrNotMember
		}
		return nil, fmt.Errorf("presence auth: %w", err)
	}
	members, err := s.channels.ListMembers(ctx, channelID)
	if err != nil {
		return nil, fmt.Errorf("presence list members: %w", err)
	}
	online := make([]string, 0, len(members))
	for _, m := range members {
		if s.isOnline(ctx, m.UserID) {
			online = append(online, m.UserID)
		}
	}
	return online, nil
}

// isOnline hides the per-user routing call so transient Redis errors on a
// single user don't tank the whole request (best-effort: treat error as
// "not online").
func (s *PresenceService) isOnline(ctx context.Context, userID string) bool {
	devices, err := s.routing.DevicesForUser(ctx, userID)
	if err != nil {
		return false
	}
	return len(devices) > 0
}

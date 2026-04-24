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
	DevicesForUser(ctx context.Context, userID int64) (map[string]string, error)
}

// PresenceService answers "who is currently online in channel X".
//
// A user is online = their routing hash has at least one deviceID → gatewayID
// entry. The routing key TTL (45s, heartbeat 15s × 3) makes this approximate
// real-time; stale pods' entries disappear within one heartbeat cycle.
type PresenceService struct {
	channels repo.ChannelRepo
	routing  RoutingPresence
}

// NewPresenceService wires the repo + routing backend.
func NewPresenceService(channels repo.ChannelRepo, routing RoutingPresence) *PresenceService {
	return &PresenceService{channels: channels, routing: routing}
}

// OnlineUsersInChannel returns the subset of channelID's members who have at
// least one registered gateway connection. Caller must be a channel member.
//
// Per-user Redis lookups are intentional and simple — for the target
// membership size (≤1000/room) this is cheap (< 30ms on a warm cluster).
// When we hit larger rooms we'll switch to a Pipeline on EXISTS across the
// member set; the public API stays the same.
func (s *PresenceService) OnlineUsersInChannel(ctx context.Context, channelID, callerID int64) ([]int64, error) {
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
	online := make([]int64, 0, len(members))
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
func (s *PresenceService) isOnline(ctx context.Context, userID int64) bool {
	devices, err := s.routing.DevicesForUser(ctx, userID)
	if err != nil {
		return false
	}
	return len(devices) > 0
}

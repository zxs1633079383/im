package service

import (
	"context"
	"errors"
	"fmt"

	"im-server/internal/repo"
)

// RoutingPresence is the minimal view of the Redis routing table the presence
// service needs. *repo.Routing implements it; tests pass fakes.
//
// LookupBatch is the pipelined alternative used by BatchOnlineStatus —
// turns N HGETALLs into one Redis round-trip, critical when the client
// asks "online count for 50 channels at once" on first paint.
type RoutingPresence interface {
	DevicesForUser(ctx context.Context, userID string) (map[string]string, error)
	LookupBatch(ctx context.Context, userIDs []string) (map[string][]string, error)
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

// ChannelOnlineStatus is one entry of BatchOnlineStatus's result. Mirrors
// mattermost csesapi /channel/onlineStatus shape closely enough that the
// front-end's existing renderer can consume it without translation.
type ChannelOnlineStatus struct {
	ChannelID     int64    `json:"channel_id"`
	OnlineCount   int      `json:"online_count"`
	OnlineUserIDs []string `json:"online_user_ids,omitempty"`
}

// BatchOnlineStatus returns presence summaries for N channels in one call.
// Member fetch is sequential per channel (bounded by handler), but routing
// lookups are collapsed into a single pipelined LookupBatch — O(N members)
// Redis cost, not O(N×M).
//
// Caller-membership is enforced upfront: any channel where callerID is not
// a member is silently dropped from the result. The handler can compare
// len(channelIDs) vs len(returned) to decide whether to surface a 403 to
// the user, but typically the silent-skip behaviour is what the UI wants
// (a channel disappeared while the request was in flight is graceful).
func (s *PresenceService) BatchOnlineStatus(ctx context.Context, channelIDs []int64, callerID string, includeUsers bool) ([]ChannelOnlineStatus, error) {
	ctx, span := tracer.Start(ctx, "PresenceService.BatchOnlineStatus")
	defer span.End()

	if len(channelIDs) == 0 {
		return []ChannelOnlineStatus{}, nil
	}

	// Pass 1: gather member uids per channel, dedupe across channels.
	type chanInfo struct {
		uids []string
	}
	channelInfo := make(map[int64]chanInfo, len(channelIDs))
	uidSet := make(map[string]struct{})
	for _, channelID := range channelIDs {
		// Authorize: skip channels where caller isn't a member.
		if _, err := s.channels.GetMember(ctx, channelID, callerID); err != nil {
			if errors.Is(err, repo.ErrNotFound) {
				continue
			}
			return nil, fmt.Errorf("presence auth %d: %w", channelID, err)
		}
		members, err := s.channels.ListMembers(ctx, channelID)
		if err != nil {
			return nil, fmt.Errorf("presence list members %d: %w", channelID, err)
		}
		uids := make([]string, 0, len(members))
		for _, m := range members {
			uids = append(uids, m.UserID)
			uidSet[m.UserID] = struct{}{}
		}
		channelInfo[channelID] = chanInfo{uids: uids}
	}

	// Pass 2: one batched LookupBatch over the union of uids.
	allUIDs := make([]string, 0, len(uidSet))
	for uid := range uidSet {
		allUIDs = append(allUIDs, uid)
	}
	online := map[string][]string{}
	if len(allUIDs) > 0 {
		var err error
		online, err = s.routing.LookupBatch(ctx, allUIDs)
		if err != nil {
			return nil, fmt.Errorf("presence lookup batch: %w", err)
		}
	}

	// Pass 3: assemble results in the order requested.
	out := make([]ChannelOnlineStatus, 0, len(channelIDs))
	for _, channelID := range channelIDs {
		info, ok := channelInfo[channelID]
		if !ok {
			// Caller not a member — skip rather than emit a fake row.
			continue
		}
		entry := ChannelOnlineStatus{ChannelID: channelID}
		for _, uid := range info.uids {
			if len(online[uid]) > 0 {
				entry.OnlineCount++
				if includeUsers {
					entry.OnlineUserIDs = append(entry.OnlineUserIDs, uid)
				}
			}
		}
		out = append(out, entry)
	}
	return out, nil
}

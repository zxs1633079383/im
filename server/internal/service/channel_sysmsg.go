package service

import (
	"context"
	"fmt"

	"gorm.io/gorm"

	"im-server/internal/repo"
)

// postSys posts a system message on the given channel. Nil-safe when the
// service was wired without a MessageRepo — tests pass nil to skip emission.
// tx == nil runs on the repo's own connection; tx != nil reuses the caller's
// transaction (required when the system message must be atomic with a sibling
// mutation, e.g. RemoveMember).
func (s *ChannelService) postSys(
	ctx context.Context, tx *gorm.DB,
	channelID, actorID int64, props map[string]any,
) error {
	if s.messages == nil {
		return nil
	}
	if _, err := s.messages.PostSystemMessage(ctx, tx, channelID, actorID, props); err != nil {
		return fmt.Errorf("post system message: %w", err)
	}
	return nil
}

// channelUpdatedProps builds the props payload for channel_updated events.
// Empty fields are still included so clients can distinguish "cleared" from
// "unchanged" if they ever need to (current wire shape: both name and avatar
// are always sent, matching the PUT body).
func channelUpdatedProps(actorID int64, name, avatarURL string) map[string]any {
	return map[string]any{
		repo.SysTypeKey: repo.SysTypeChannelUpdated,
		"actor_id":      actorID,
		"name":          name,
		"avatar_url":    avatarURL,
	}
}

// memberJoinedProps builds the props payload for member_joined events.
// actorID is the user who performed the add (== target for self-add flows).
func memberJoinedProps(actorID, targetID int64) map[string]any {
	return map[string]any{
		repo.SysTypeKey: repo.SysTypeMemberJoined,
		"actor_id":      actorID,
		"target_id":     targetID,
	}
}

// memberRemovedProps builds the props payload for member_removed events.
func memberRemovedProps(actorID, targetID int64) map[string]any {
	return map[string]any{
		repo.SysTypeKey: repo.SysTypeMemberRemoved,
		"actor_id":      actorID,
		"target_id":     targetID,
	}
}

// memberLeftProps builds the props payload for voluntary member_left events.
// Distinct from memberRemovedProps because clients render them differently
// ("X left the channel" vs "X was removed by Y").
func memberLeftProps(actorID int64) map[string]any {
	return map[string]any{
		repo.SysTypeKey: repo.SysTypeMemberLeft,
		"actor_id":      actorID,
	}
}

// channelCreatedProps builds the props payload for channel_created events.
// Used once when a Group channel is first created so its member list has an
// anchor system message at seq=1.
func channelCreatedProps(actorID int64, name string) map[string]any {
	return map[string]any{
		repo.SysTypeKey: repo.SysTypeChannelCreated,
		"actor_id":      actorID,
		"name":          name,
	}
}

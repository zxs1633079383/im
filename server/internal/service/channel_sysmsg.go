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
//
// teamID denormalises onto messages.team_id; pass nil for "no team scope"
// (matches NULL in PG).
func (s *ChannelService) postSys(
	ctx context.Context, tx *gorm.DB,
	channelID string, actorID string, teamID *string, props map[string]any,
) error {
	if s.messages == nil {
		return nil
	}
	if _, err := s.messages.PostSystemMessage(ctx, tx, channelID, actorID, teamID, props); err != nil {
		return fmt.Errorf("post system message: %w", err)
	}
	return nil
}

// channelUpdatedProps builds the props payload for channel_updated events.
// actor_id / target_id are mm UserIDs (24-hex strings) so clients can resolve
// them via the cses Redis "User" hash.
func channelUpdatedProps(actorID, name, avatarURL string) map[string]any {
	return map[string]any{
		repo.SysTypeKey: repo.SysTypeChannelUpdated,
		"actor_id":      actorID,
		"name":          name,
		"avatar_url":    avatarURL,
	}
}

func memberJoinedProps(actorID, targetID string) map[string]any {
	return map[string]any{
		repo.SysTypeKey: repo.SysTypeMemberJoined,
		"actor_id":      actorID,
		"target_id":     targetID,
	}
}

func memberRemovedProps(actorID, targetID string) map[string]any {
	return map[string]any{
		repo.SysTypeKey: repo.SysTypeMemberRemoved,
		"actor_id":      actorID,
		"target_id":     targetID,
	}
}

func memberLeftProps(actorID string) map[string]any {
	return map[string]any{
		repo.SysTypeKey: repo.SysTypeMemberLeft,
		"actor_id":      actorID,
	}
}

func channelCreatedProps(actorID, name string) map[string]any {
	return map[string]any{
		repo.SysTypeKey: repo.SysTypeChannelCreated,
		"actor_id":      actorID,
		"name":          name,
	}
}

func channelClosedProps(actorID string) map[string]any {
	return map[string]any{
		repo.SysTypeKey: repo.SysTypeChannelClosed,
		"actor_id":      actorID,
	}
}

func memberNicknameProps(actorID, targetID, nickName string) map[string]any {
	return map[string]any{
		repo.SysTypeKey: repo.SysTypeMemberNickname,
		"actor_id":      actorID,
		"target_id":     targetID,
		"nick_name":     nickName,
	}
}

// ownerTransferredProps builds the props payload for owner-transferred system
// messages (C013). actor_id = the OLD owner who initiated the transfer;
// target_id = the NEW owner. When AlsoLeave=true, ChannelService.TransferOwner
// appends a second system message via memberLeftProps in the same transaction.
func ownerTransferredProps(actorID, targetID string) map[string]any {
	return map[string]any{
		repo.SysTypeKey: repo.SysTypeOwnerTransferred,
		"actor_id":      actorID,
		"target_id":     targetID,
	}
}

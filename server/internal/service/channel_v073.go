package service

import (
	"context"
	"errors"
	"fmt"

	"gorm.io/gorm"

	"im-server/internal/repo"
)

// ChannelMemberBroadcaster is the small interface ChannelService needs to
// fan a channel_member_updated frame out to every member of a channel.
// Implemented in cmd/gateway/main.go::hubChannelMemberBroadcaster on top of
// the existing cross-pod dispatch path. nil-safe: AttachMemberBroadcaster
// may be skipped (tests do this) and the service silently no-ops.
type ChannelMemberBroadcaster interface {
	BroadcastMemberEvent(channelID string, eventType string, payload any)
}

// MemberChangeType discriminates the WS frame body so cses-client routes the
// event to add / leave / kick / nickname branches. Mirrors the gateway-side
// constant pool — duplicated here so service stays decoupled from gateway.
type MemberChangeType string

const (
	MemberChangeJoin     MemberChangeType = "join"
	MemberChangeLeave    MemberChangeType = "leave"
	MemberChangeKick     MemberChangeType = "kick"
	MemberChangeNickname MemberChangeType = "nickname"
)

// ChannelMemberSummary is the minimal per-member projection bundled in the
// channel_member_updated payload. Stays lean — clients resolve avatar /
// display name via the cses Redis User hash.
type ChannelMemberSummary struct {
	UserID     string `json:"user_id"`
	Role       int16  `json:"role"`
	NickName   string `json:"nick_name,omitempty"`
	IsTop      bool   `json:"is_top,omitempty"`
	NotifyPref int16  `json:"notify_pref,omitempty"`
}

// ChannelMemberUpdatedPayload is the wire body of EventChannelMemberUpdated.
// The full post-change roster lets cses-client replace local membership in
// one pass without re-fetching by channelId.
type ChannelMemberUpdatedPayload struct {
	ChannelID  string                 `json:"channel_id"`
	ChangeType MemberChangeType       `json:"change_type"`
	ActorID    string                 `json:"actor_id"`
	TargetID   string                 `json:"target_id"`
	NickName   string                 `json:"nick_name,omitempty"`
	Members    []ChannelMemberSummary `json:"members"`
}

// memberEventName mirrors http.EventChannelMemberUpdated. Lives here as a
// plain string constant to keep the service/http boundary clean.
const memberEventName = "channel_member_updated"

// AttachMemberBroadcaster wires a fan-out hook so AddMember / RemoveMember /
// LeaveChannel / SetMemberNickname can publish a channel_member_updated WS
// frame to every member. Safe to call once at startup.
func (s *ChannelService) AttachMemberBroadcaster(b ChannelMemberBroadcaster) {
	s.broadcaster = b
}

// v0.7.3 sentinels for the cses-client cutover gaps #1 / #5.
var (
	// ErrChannelClosed is returned by CloseChannel when the channel was
	// already closed by a previous call — idempotent semantics.
	ErrChannelClosed = errors.New("channel already closed")
	// ErrOwnerOnly is returned when the caller is not the channel owner.
	// gap #1 — only the owner may解散群聊.
	ErrOwnerOnly = errors.New("only the channel owner may perform this action")
	// ErrNicknameTooLong is returned when the new nickname exceeds the column
	// width budget (64 chars; matches migration 017).
	ErrNicknameTooLong = errors.New("nickname must be at most 64 characters")
)

// nicknameMaxLen mirrors the VARCHAR(64) limit set in migration 017. Defined
// here so the service can reject oversize input before hitting Postgres.
const nicknameMaxLen = 64

// CloseChannel soft-deletes channelID. Only the owner may close it (gap #1).
// On success the returned Channel carries the new DeletedAt timestamp so the
// HTTP layer can wire a channel_closed WS broadcast carrying the same value.
// The companion system message (sys_type=channel_closed) is appended in the
// same transaction so /api/sync replays a coherent "channel was closed"
// signal even for offline clients.
func (s *ChannelService) CloseChannel(ctx context.Context, channelID string, callerID string) (*repo.Channel, error) {
	ctx, span := tracer.Start(ctx, "ChannelService.CloseChannel")
	defer span.End()

	if err := s.requireOwner(ctx, channelID, callerID); err != nil {
		return nil, err
	}
	if s.messages == nil {
		ch, err := s.channels.SoftDelete(ctx, channelID)
		if errors.Is(err, repo.ErrGone) {
			return ch, ErrChannelClosed
		}
		return ch, err
	}
	return s.closeChannelAtomic(ctx, channelID, callerID)
}

// closeChannelAtomic posts the system message + flips deleted_at inside a
// single transaction so /api/sync sees a coherent ordering.
func (s *ChannelService) closeChannelAtomic(ctx context.Context, channelID string, callerID string) (*repo.Channel, error) {
	ch, err := s.channels.GetByID(ctx, channelID)
	if err != nil {
		return nil, err
	}
	teamID := ch.TeamID
	err = s.channels.WithinTx(ctx, func(tx *gorm.DB) error {
		if err := s.postSys(ctx, tx, channelID, callerID, teamID,
			channelClosedProps(callerID)); err != nil {
			return err
		}
		return softDeleteWithinTx(ctx, tx, channelID)
	})
	if errors.Is(err, repo.ErrGone) {
		return ch, ErrChannelClosed
	}
	if err != nil {
		return nil, err
	}
	return s.channels.GetByID(ctx, channelID)
}

// softDeleteWithinTx runs the UPDATE channels SET deleted_at = now() inside
// the caller's transaction. Returns repo.ErrGone when the row was already
// closed (RowsAffected == 0).
func softDeleteWithinTx(ctx context.Context, tx *gorm.DB, channelID string) error {
	res := tx.WithContext(ctx).Exec(
		`UPDATE channels SET deleted_at = now(), updated_at = now()
		 WHERE id = ? AND deleted_at IS NULL`, channelID,
	)
	if res.Error != nil {
		return fmt.Errorf("soft delete in tx: %w", res.Error)
	}
	if res.RowsAffected == 0 {
		return repo.ErrGone
	}
	return nil
}

// SetMemberNickname overwrites the caller-or-target's per-channel nickname.
// caller == target is always allowed (改自己的群名片). admin/owner may also
// rename anyone else (微信 vs 钉钉 hybrid). gap #5.
func (s *ChannelService) SetMemberNickname(ctx context.Context, channelID string, callerID, targetID, nickName string) (*repo.ChannelMember, error) {
	ctx, span := tracer.Start(ctx, "ChannelService.SetMemberNickname")
	defer span.End()

	if len(nickName) > nicknameMaxLen {
		return nil, ErrNicknameTooLong
	}
	if callerID != targetID {
		if err := s.requireAdminOrOwner(ctx, channelID, callerID); err != nil {
			return nil, err
		}
	} else if err := s.requireMember(ctx, channelID, callerID); err != nil {
		return nil, err
	}
	if err := s.channels.UpdateMemberNickname(ctx, channelID, targetID, nickName); err != nil {
		return nil, err
	}
	ch, _ := s.channels.GetByID(ctx, channelID)
	var teamID *string
	if ch != nil {
		teamID = ch.TeamID
	}
	_ = s.postSys(ctx, nil, channelID, callerID, teamID,
		memberNicknameProps(callerID, targetID, nickName))
	s.fanMemberUpdate(ctx, channelID, MemberChangeNickname, callerID, targetID, nickName)
	return s.channels.GetMember(ctx, channelID, targetID)
}

// requireOwner returns ErrOwnerOnly when callerID is not the channel owner.
// Mirrors requireAdminOrOwner but with a strictly higher bar — admins cannot
// 解散群聊 (gap #1).
func (s *ChannelService) requireOwner(ctx context.Context, channelID string, callerID string) error {
	m, err := s.channels.GetMember(ctx, channelID, callerID)
	if err != nil {
		if errors.Is(err, repo.ErrNotFound) {
			return ErrNotMember
		}
		return err
	}
	if m.Role != repo.MemberRoleOwner {
		return ErrOwnerOnly
	}
	return nil
}

// requireMember returns ErrNotMember when callerID is not in channelID.
// Local copy to keep channel.go (already ~300 lines) lean.
func (s *ChannelService) requireMember(ctx context.Context, channelID string, callerID string) error {
	if _, err := s.channels.GetMember(ctx, channelID, callerID); err != nil {
		if errors.Is(err, repo.ErrNotFound) {
			return ErrNotMember
		}
		return fmt.Errorf("check member: %w", err)
	}
	return nil
}

// fanMemberUpdate broadcasts the post-change roster snapshot to every member
// of channelID. nil-safe — when no broadcaster is wired we silently no-op
// (tests don't need a hub). Failures during ListMembers are logged via the
// service tracer (span); they don't propagate up because the membership
// mutation already succeeded.
func (s *ChannelService) fanMemberUpdate(
	ctx context.Context,
	channelID string,
	change MemberChangeType,
	actorID, targetID, nickName string,
) {
	if s.broadcaster == nil {
		return
	}
	members, err := s.channels.ListMembers(ctx, channelID)
	if err != nil {
		// best-effort: log via span only — fan-out failure must not roll
		// the caller's write back.
		_, span := tracer.Start(ctx, "ChannelService.fanMemberUpdate")
		span.RecordError(err)
		span.End()
		return
	}
	summaries := make([]ChannelMemberSummary, 0, len(members))
	for _, m := range members {
		summaries = append(summaries, ChannelMemberSummary{
			UserID:     m.UserID,
			Role:       m.Role,
			NickName:   m.NickName,
			IsTop:      m.IsTop,
			NotifyPref: m.NotifyPref,
		})
	}
	s.broadcaster.BroadcastMemberEvent(channelID, memberEventName,
		ChannelMemberUpdatedPayload{
			ChannelID:  channelID,
			ChangeType: change,
			ActorID:    actorID,
			TargetID:   targetID,
			NickName:   nickName,
			Members:    summaries,
		})
}

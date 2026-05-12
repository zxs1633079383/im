package service

import (
	"context"
	"errors"
	"fmt"

	"gorm.io/gorm"

	"im-server/internal/repo"
)

// Channel-service sentinels. The HTTP transport maps these to status codes:
//
//   - ErrNotMember         → 403 Forbidden ("not a member of this channel")
//   - ErrForbidden         → 403 Forbidden (admin-or-owner / owner-cannot-leave)
//   - ErrSelfDM            → 422 Unprocessable Entity ("cannot DM yourself")
//   - ErrCannotRemoveOwner → 403 Forbidden ("cannot remove the owner")
//   - repo.ErrNotFound     → 404 Not Found (channel/member missing)
var (
	ErrNotMember         = errors.New("not a member of this channel")
	ErrForbidden         = errors.New("forbidden")
	ErrSelfDM            = errors.New("cannot DM yourself")
	ErrCannotRemoveOwner = errors.New("cannot remove the owner")
	ErrOwnerCannotLeave  = errors.New("owner cannot leave; transfer ownership first")
)

// ChannelService bundles channel + member queries and mutations on top of
// repo.ChannelRepo. M4: there is no longer a UserRepo dependency — the caller
// supplies team_id from the resolved Mattermost user on the request context,
// and member listings return mm UserIDs only (clients fetch profile data
// from the cses Redis "User" hash).
type ChannelService struct {
	channels repo.ChannelRepo
	// messages is optional. When non-nil, membership / metadata mutations
	// (Update / AddMember / RemoveMember / LeaveChannel / CreateGroup) emit a
	// system message (MsgType=System) carrying a typed props payload so
	// clients receive the event through the normal push_msg + /api/sync pipe.
	messages repo.MessageRepo
	// broadcaster is the v0.7.3 fan-out hook for channel_member_updated WS
	// frames (gap #4 + #5). nil = no real-time broadcast (the integration /
	// unit tests don't wire a hub).
	broadcaster ChannelMemberBroadcaster
}

// NewChannelService wires the supplied repos. messages may be nil — when nil,
// system-message emission is disabled (handy for older tests).
func NewChannelService(channels repo.ChannelRepo, messages repo.MessageRepo) *ChannelService {
	return &ChannelService{channels: channels, messages: messages}
}

// MemberWithUser is a thin wrapper around ChannelMember kept for transport
// shape compatibility. M4 drops the joined username/display/avatar fields —
// clients resolve those via the cses Redis "User" hash by mm UserID.
type MemberWithUser struct {
	repo.ChannelMember
}

// AddedMember is returned by CreateGroup for each non-creator added during the
// initial member fan-out.
type AddedMember struct {
	UserID string
}

// CreateGroup creates a Group channel, adds creatorID as owner, then adds the
// remaining memberIDs as plain members. teamID denormalises onto the channel
// row (frozen at creation); empty string stores SQL NULL ("public pool").
func (s *ChannelService) CreateGroup(ctx context.Context, creatorID, teamID, name string, memberIDs []string) (*repo.Channel, []AddedMember, error) {
	ctx, span := tracer.Start(ctx, "ChannelService.CreateGroup")
	defer span.End()

	ch := &repo.Channel{
		Type:      repo.ChannelTypeGroup,
		Name:      name,
		CreatorID: creatorID,
		TeamID:    nullIfEmpty(teamID),
	}
	if err := s.channels.Create(ctx, ch); err != nil {
		return nil, nil, fmt.Errorf("create group: %w", err)
	}
	if err := s.channels.AddMember(ctx, ch.ID, creatorID, repo.MemberRoleOwner); err != nil {
		return nil, nil, fmt.Errorf("add owner: %w", err)
	}

	added := make([]AddedMember, 0, len(memberIDs))
	for _, uid := range memberIDs {
		if uid == "" || uid == creatorID {
			continue
		}
		if err := s.channels.AddMember(ctx, ch.ID, uid, repo.MemberRoleMember); err != nil {
			continue
		}
		added = append(added, AddedMember{UserID: uid})
	}

	// Anchor system messages so /api/sync replays the channel's history
	// symmetrically: one channel_created at seq=1 and a member_joined per
	// non-creator member.
	_ = s.postSys(ctx, nil, ch.ID, creatorID, ch.TeamID, channelCreatedProps(creatorID, ch.Name))
	for _, m := range added {
		_ = s.postSys(ctx, nil, ch.ID, creatorID, ch.TeamID, memberJoinedProps(creatorID, m.UserID))
	}
	return ch, added, nil
}

// CreateOrGetDM returns the existing DM between callerID and otherUserID, or
// creates a fresh one. teamID is the caller's team scope, frozen onto the
// channel row.
func (s *ChannelService) CreateOrGetDM(ctx context.Context, callerID, otherUserID, teamID string) (*repo.Channel, bool, error) {
	ctx, span := tracer.Start(ctx, "ChannelService.CreateOrGetDM")
	defer span.End()

	if otherUserID == callerID {
		return nil, false, ErrSelfDM
	}

	existing, err := s.channels.FindDM(ctx, callerID, otherUserID)
	if err == nil {
		return existing, false, nil
	}
	if !errors.Is(err, repo.ErrNotFound) {
		return nil, false, fmt.Errorf("find dm: %w", err)
	}

	ch := &repo.Channel{
		Type:      repo.ChannelTypeDM,
		CreatorID: callerID,
		TeamID:    nullIfEmpty(teamID),
	}
	if err := s.channels.Create(ctx, ch); err != nil {
		return nil, false, fmt.Errorf("create dm: %w", err)
	}
	if err := s.channels.AddMember(ctx, ch.ID, callerID, repo.MemberRoleMember); err != nil {
		return nil, false, fmt.Errorf("add dm caller: %w", err)
	}
	if err := s.channels.AddMember(ctx, ch.ID, otherUserID, repo.MemberRoleMember); err != nil {
		return nil, false, fmt.Errorf("add dm peer: %w", err)
	}
	return ch, true, nil
}

// ListByUser returns the channel previews (last-msg + unread) for userID.
func (s *ChannelService) ListByUser(ctx context.Context, userID string) ([]repo.ChannelWithPreview, error) {
	ctx, span := tracer.Start(ctx, "ChannelService.ListByUser")
	defer span.End()

	return s.channels.ListByUserWithPreview(ctx, userID)
}

// GetByID returns the channel only if callerID is a member.
func (s *ChannelService) GetByID(ctx context.Context, channelID int64, callerID string) (*repo.Channel, error) {
	if _, err := s.channels.GetMember(ctx, channelID, callerID); err != nil {
		if errors.Is(err, repo.ErrNotFound) {
			return nil, ErrNotMember
		}
		return nil, err
	}
	return s.channels.GetByID(ctx, channelID)
}

// Update applies name/avatar to channelID. Requires admin or owner role.
func (s *ChannelService) Update(ctx context.Context, channelID int64, callerID, name, avatarURL string) (*repo.Channel, error) {
	if err := s.requireAdminOrOwner(ctx, channelID, callerID); err != nil {
		return nil, err
	}
	if err := s.channels.Update(ctx, channelID, name, avatarURL); err != nil {
		return nil, err
	}
	ch, err := s.channels.GetByID(ctx, channelID)
	if err != nil {
		return nil, err
	}
	_ = s.postSys(ctx, nil, channelID, callerID, ch.TeamID, channelUpdatedProps(callerID, ch.Name, ch.AvatarURL))
	return ch, nil
}

// AddMember inserts newUserID into channelID. Requires caller admin/owner.
func (s *ChannelService) AddMember(ctx context.Context, channelID int64, callerID, newUserID string) (string, error) {
	ctx, span := tracer.Start(ctx, "ChannelService.AddMember")
	defer span.End()

	if err := s.requireAdminOrOwner(ctx, channelID, callerID); err != nil {
		return "", err
	}
	if err := s.channels.AddMember(ctx, channelID, newUserID, repo.MemberRoleMember); err != nil {
		return "", err
	}
	ch, _ := s.channels.GetByID(ctx, channelID)
	var teamID *string
	if ch != nil {
		teamID = ch.TeamID
	}
	_ = s.postSys(ctx, nil, channelID, callerID, teamID, memberJoinedProps(callerID, newUserID))
	// v0.7.3 gap #4: fan a channel_member_updated frame to every member so
	// other devices see the new roster without re-fetching by channelId.
	s.fanMemberUpdate(ctx, channelID, MemberChangeJoin, callerID, newUserID, "")
	if ch == nil {
		return "", nil
	}
	return ch.Name, nil
}

// RemoveMember deletes targetUserID from channelID. Requires admin or owner.
func (s *ChannelService) RemoveMember(ctx context.Context, channelID int64, callerID, targetUserID string) error {
	ctx, span := tracer.Start(ctx, "ChannelService.RemoveMember")
	defer span.End()

	if err := s.requireAdminOrOwner(ctx, channelID, callerID); err != nil {
		return err
	}
	target, err := s.channels.GetMember(ctx, channelID, targetUserID)
	if err != nil {
		return err
	}
	if target.Role == repo.MemberRoleOwner {
		return ErrCannotRemoveOwner
	}
	return s.removeMemberAtomic(ctx, channelID, callerID, targetUserID)
}

func (s *ChannelService) removeMemberAtomic(ctx context.Context, channelID int64, actorID, targetID string) error {
	if s.messages == nil {
		if err := s.channels.RemoveMember(ctx, channelID, targetID); err != nil {
			return err
		}
		s.fanMemberUpdate(ctx, channelID, MemberChangeKick, actorID, targetID, "")
		return nil
	}
	ch, _ := s.channels.GetByID(ctx, channelID)
	var teamID *string
	if ch != nil {
		teamID = ch.TeamID
	}
	err := s.channels.WithinTx(ctx, func(tx *gorm.DB) error {
		if err := s.postSys(ctx, tx, channelID, actorID, teamID, memberRemovedProps(actorID, targetID)); err != nil {
			return err
		}
		return s.channels.RemoveMemberTx(ctx, tx, channelID, targetID)
	})
	if err != nil {
		return err
	}
	s.fanMemberUpdate(ctx, channelID, MemberChangeKick, actorID, targetID, "")
	return nil
}

// ListMembers returns all members of channelID. Caller must be a member.
// M4: profile data is no longer joined; clients resolve user_id → profile
// via the cses Redis "User" hash.
func (s *ChannelService) ListMembers(ctx context.Context, channelID int64, callerID string) ([]MemberWithUser, error) {
	if _, err := s.channels.GetMember(ctx, channelID, callerID); err != nil {
		if errors.Is(err, repo.ErrNotFound) {
			return nil, ErrNotMember
		}
		return nil, err
	}
	members, err := s.channels.ListMembers(ctx, channelID)
	if err != nil {
		return nil, err
	}
	out := make([]MemberWithUser, len(members))
	for i, m := range members {
		out[i] = MemberWithUser{ChannelMember: m}
	}
	return out, nil
}

// LeaveChannel removes callerID from channelID. Owners cannot leave.
func (s *ChannelService) LeaveChannel(ctx context.Context, channelID int64, callerID string) error {
	m, err := s.channels.GetMember(ctx, channelID, callerID)
	if err != nil {
		if errors.Is(err, repo.ErrNotFound) {
			return ErrNotMember
		}
		return err
	}
	if m.Role == repo.MemberRoleOwner {
		return ErrOwnerCannotLeave
	}
	return s.leaveChannelAtomic(ctx, channelID, callerID)
}

func (s *ChannelService) leaveChannelAtomic(ctx context.Context, channelID int64, callerID string) error {
	if s.messages == nil {
		if err := s.channels.RemoveMember(ctx, channelID, callerID); err != nil {
			return err
		}
		s.fanMemberUpdate(ctx, channelID, MemberChangeLeave, callerID, callerID, "")
		return nil
	}
	ch, _ := s.channels.GetByID(ctx, channelID)
	var teamID *string
	if ch != nil {
		teamID = ch.TeamID
	}
	err := s.channels.WithinTx(ctx, func(tx *gorm.DB) error {
		if err := s.postSys(ctx, tx, channelID, callerID, teamID, memberLeftProps(callerID)); err != nil {
			return err
		}
		return s.channels.RemoveMemberTx(ctx, tx, channelID, callerID)
	})
	if err != nil {
		return err
	}
	s.fanMemberUpdate(ctx, channelID, MemberChangeLeave, callerID, callerID, "")
	return nil
}

func (s *ChannelService) requireAdminOrOwner(ctx context.Context, channelID int64, callerID string) error {
	m, err := s.channels.GetMember(ctx, channelID, callerID)
	if err != nil {
		if errors.Is(err, repo.ErrNotFound) {
			return ErrNotMember
		}
		return err
	}
	if m.Role < repo.MemberRoleAdmin {
		return ErrForbidden
	}
	return nil
}

// nullIfEmpty wraps non-empty s in a pointer, returning nil for "". Used to
// land "" team_id values as SQL NULL rather than empty-string TEXT — matches
// the migration's `team_id TEXT NULL` semantics.
func nullIfEmpty(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}

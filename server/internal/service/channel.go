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
	// channelEvent is the per-channel PG sequence provisioner (C018 §3.2).
	// When non-nil, every newly created channel (CreateGroup, CreateOrGetDM,
	// CreateTopic) is provisioned with `channel_msg_seq_<id>` +
	// `channel_event_seq_<id>` inside the same tx — without this the channel's
	// first message INSERT fails with `relation "channel_msg_seq_<uuid>" does
	// not exist` (P3 NEED_FOLLOWUP). Wired in production via
	// cmd/gateway/main.go::AttachChannelEventRepo; left nil only in older
	// unit tests that don't exercise the per-channel sequence path.
	channelEvent repo.ChannelEventRepo
}

// NewChannelService wires the supplied repos. messages may be nil — when nil,
// system-message emission is disabled (handy for older tests). channelEvent
// stays nil until AttachChannelEventRepo is called by the wiring layer.
func NewChannelService(channels repo.ChannelRepo, messages repo.MessageRepo) *ChannelService {
	return &ChannelService{channels: channels, messages: messages}
}

// AttachChannelEventRepo wires the channel_event repo so channel creation
// paths (CreateGroup / CreateOrGetDM / CreateTopic) provision per-channel
// PG sequences inside the same tx as the channels INSERT. Production wiring
// calls this in cmd/gateway/main.go; unit tests that don't exercise the
// per-channel sequence path may safely leave it nil — in that case
// CreateGroup / CreateOrGetDM short-circuit to the legacy non-tx path that
// only writes the channel row + members (matches pre-P2 behaviour).
func (s *ChannelService) AttachChannelEventRepo(r repo.ChannelEventRepo) {
	s.channelEvent = r
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
//
// When channelEvent is attached (production), the channel INSERT + per-channel
// PG sequence provisioning (channel_msg_seq_<id>, channel_event_seq_<id>) +
// owner / member fan-out all run inside a single WithinTx so partial failure
// rolls back the channel row — see C018 §3.2. Anchor system messages stay
// outside the tx (postSys allocates its own seq), matching the legacy
// behaviour: a failure there does not undo the channel.
//
// When channelEvent is nil (older unit tests), we fall back to the legacy
// non-tx path: bare s.channels.Create + AddMember in sequence. Skipping the
// sequence provisioning here is intentional — tests that don't wire a real
// PG would fail under tx-mocked WithinTx, and they don't drive the per-
// channel-sequence read path anyway.
func (s *ChannelService) CreateGroup(ctx context.Context, creatorID, teamID, name string, memberIDs []string) (*repo.Channel, []AddedMember, error) {
	ctx, span := tracer.Start(ctx, "ChannelService.CreateGroup")
	defer span.End()

	ch := &repo.Channel{
		Type:      repo.ChannelTypeGroup,
		Name:      name,
		CreatorID: creatorID,
		TeamID:    nullIfEmpty(teamID),
	}
	added := make([]AddedMember, 0, len(memberIDs))

	if s.channelEvent != nil {
		err := s.channels.WithinTx(ctx, func(tx *gorm.DB) error {
			if err := s.channels.CreateTx(ctx, tx, ch); err != nil {
				return fmt.Errorf("create group: %w", err)
			}
			if err := s.channelEvent.CreateChannelSequences(ctx, tx, ch.ID); err != nil {
				return fmt.Errorf("create group sequences: %w", err)
			}
			if err := s.channels.AddMemberTx(ctx, tx, ch.ID, creatorID, repo.MemberRoleOwner); err != nil {
				return fmt.Errorf("add owner: %w", err)
			}
			for _, uid := range memberIDs {
				if uid == "" || uid == creatorID {
					continue
				}
				if err := s.channels.AddMemberTx(ctx, tx, ch.ID, uid, repo.MemberRoleMember); err != nil {
					// Continue on duplicate / per-user failures to match the
					// legacy "best-effort fan-out" semantics — the OnConflict
					// DO NOTHING clause keeps the tx happy.
					continue
				}
				added = append(added, AddedMember{UserID: uid})
			}
			return nil
		})
		if err != nil {
			return nil, nil, err
		}
	} else {
		// Legacy non-tx fallback (channelEvent not wired — older tests).
		if err := s.channels.Create(ctx, ch); err != nil {
			return nil, nil, fmt.Errorf("create group: %w", err)
		}
		if err := s.channels.AddMember(ctx, ch.ID, creatorID, repo.MemberRoleOwner); err != nil {
			return nil, nil, fmt.Errorf("add owner: %w", err)
		}
		for _, uid := range memberIDs {
			if uid == "" || uid == creatorID {
				continue
			}
			if err := s.channels.AddMember(ctx, ch.ID, uid, repo.MemberRoleMember); err != nil {
				continue
			}
			added = append(added, AddedMember{UserID: uid})
		}
	}

	// Anchor system messages so /api/sync replays the channel's history
	// symmetrically: one channel_created at seq=1 and a member_joined per
	// non-creator member. Kept outside the creation tx because postSys
	// allocates its own per-message seq via the new PG sequence — the
	// sequence object only exists after the CreateChannelSequences call
	// inside the tx above commits.
	_ = s.postSys(ctx, nil, ch.ID, creatorID, ch.TeamID, channelCreatedProps(creatorID, ch.Name))
	for _, m := range added {
		_ = s.postSys(ctx, nil, ch.ID, creatorID, ch.TeamID, memberJoinedProps(creatorID, m.UserID))
	}
	return ch, added, nil
}

// CreateOrGetDM returns the existing DM between callerID and otherUserID, or
// creates a fresh one. teamID is the caller's team scope, frozen onto the
// channel row.
//
// When channelEvent is attached, the new-DM path runs INSERT channel +
// CreateChannelSequences + AddMemberTx(caller) + AddMemberTx(peer) inside a
// single WithinTx — same shape as CreateGroup. Falls back to the legacy
// non-tx path when channelEvent is nil (older unit tests that mock the
// individual Create/AddMember calls separately).
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

	if s.channelEvent != nil {
		err := s.channels.WithinTx(ctx, func(tx *gorm.DB) error {
			if err := s.channels.CreateTx(ctx, tx, ch); err != nil {
				return fmt.Errorf("create dm: %w", err)
			}
			if err := s.channelEvent.CreateChannelSequences(ctx, tx, ch.ID); err != nil {
				return fmt.Errorf("create dm sequences: %w", err)
			}
			if err := s.channels.AddMemberTx(ctx, tx, ch.ID, callerID, repo.MemberRoleMember); err != nil {
				return fmt.Errorf("add dm caller: %w", err)
			}
			if err := s.channels.AddMemberTx(ctx, tx, ch.ID, otherUserID, repo.MemberRoleMember); err != nil {
				return fmt.Errorf("add dm peer: %w", err)
			}
			return nil
		})
		if err != nil {
			return nil, false, err
		}
		return ch, true, nil
	}

	// Legacy non-tx fallback (channelEvent not wired — older tests).
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
func (s *ChannelService) GetByID(ctx context.Context, channelID string, callerID string) (*repo.Channel, error) {
	if _, err := s.channels.GetMember(ctx, channelID, callerID); err != nil {
		if errors.Is(err, repo.ErrNotFound) {
			return nil, ErrNotMember
		}
		return nil, err
	}
	return s.channels.GetByID(ctx, channelID)
}

// Update applies name/avatar to channelID. Requires admin or owner role.
func (s *ChannelService) Update(ctx context.Context, channelID string, callerID, name, avatarURL string) (*repo.Channel, error) {
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
func (s *ChannelService) AddMember(ctx context.Context, channelID string, callerID, newUserID string) (string, error) {
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
func (s *ChannelService) RemoveMember(ctx context.Context, channelID string, callerID, targetUserID string) error {
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

func (s *ChannelService) removeMemberAtomic(ctx context.Context, channelID string, actorID, targetID string) error {
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
func (s *ChannelService) ListMembers(ctx context.Context, channelID string, callerID string) ([]MemberWithUser, error) {
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
func (s *ChannelService) LeaveChannel(ctx context.Context, channelID string, callerID string) error {
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

func (s *ChannelService) leaveChannelAtomic(ctx context.Context, channelID string, callerID string) error {
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

func (s *ChannelService) requireAdminOrOwner(ctx context.Context, channelID string, callerID string) error {
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

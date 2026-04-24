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
//   - ErrNotMember      → 403 Forbidden ("not a member of this channel")
//   - ErrForbidden      → 403 Forbidden (admin-or-owner / owner-cannot-leave)
//   - ErrSelfDM         → 422 Unprocessable Entity ("cannot DM yourself")
//   - ErrCannotRemoveOwner → 403 Forbidden ("cannot remove the owner")
//   - repo.ErrNotFound  → 404 Not Found (channel/member missing)
//
// Keeping these in service/ instead of leaking driver-style errors keeps the
// transport layer decoupled from gorm/postgres specifics — same pattern as
// service.ErrAlreadyExists in friend.go.
var (
	ErrNotMember          = errors.New("not a member of this channel")
	ErrForbidden          = errors.New("forbidden")
	ErrSelfDM             = errors.New("cannot DM yourself")
	ErrCannotRemoveOwner  = errors.New("cannot remove the owner")
	ErrOwnerCannotLeave   = errors.New("owner cannot leave; transfer ownership first")
)

// ChannelService bundles channel + member queries and mutations on top of
// repo.ChannelRepo and repo.UserRepo. Validation (zero IDs, empty names) is
// the transport layer's responsibility; this service enforces *semantic*
// rules — membership, role gating, owner-protection — and returns stable
// sentinels.
type ChannelService struct {
	channels repo.ChannelRepo
	users    repo.UserRepo
	// messages is optional. When non-nil, membership / metadata mutations
	// (Update / AddMember / RemoveMember / LeaveChannel / CreateGroup) emit a
	// system message (MsgType=System) carrying a typed props payload so
	// clients receive the event through the normal push_msg + /api/sync pipe.
	// Passing nil turns off system-message emission — used by unit tests that
	// don't care about the side channel.
	messages repo.MessageRepo
}

// NewChannelService wires the supplied repos. messages may be nil — when nil,
// system-message emission is disabled (handy for older tests). Production
// wires repo.MessageRepo so channel events land in the message stream.
func NewChannelService(channels repo.ChannelRepo, users repo.UserRepo, messages repo.MessageRepo) *ChannelService {
	return &ChannelService{channels: channels, users: users, messages: messages}
}

// MemberWithUser is a ChannelMember enriched with basic user profile fields.
// Mirrors the legacy handler.MemberWithUser shape so existing clients see no
// change after the cut-over.
type MemberWithUser struct {
	repo.ChannelMember
	Username    string `json:"username"`
	DisplayName string `json:"display_name"`
	AvatarURL   string `json:"avatar_url"`
}

// AddedMember is returned by CreateGroup for each non-creator added during the
// initial member fan-out. The transport layer uses this to drive the
// "added" event push without re-querying the repo.
type AddedMember struct {
	UserID int64
}

// CreateGroup creates a Group channel, adds creatorID as owner, then adds the
// remaining memberIDs as plain members. The caller (transport) owns the
// post-success event-push fan-out — it receives the list of newly added
// members back so the pusher can fire one event per user.
//
// The semantics mirror the legacy handler exactly: skip the creator if it
// appears in memberIDs; tolerate per-member AddMember failures (log + skip)
// rather than rolling back the whole channel.
func (s *ChannelService) CreateGroup(ctx context.Context, creatorID int64, name string, memberIDs []int64) (*repo.Channel, []AddedMember, error) {
	ctx, span := tracer.Start(ctx, "ChannelService.CreateGroup")
	defer span.End()

	ch := &repo.Channel{
		Type:      repo.ChannelTypeGroup,
		Name:      name,
		CreatorID: &creatorID,
	}
	if err := s.channels.Create(ctx, ch); err != nil {
		return nil, nil, fmt.Errorf("create group: %w", err)
	}
	if err := s.channels.AddMember(ctx, ch.ID, creatorID, repo.MemberRoleOwner); err != nil {
		return nil, nil, fmt.Errorf("add owner: %w", err)
	}

	added := make([]AddedMember, 0, len(memberIDs))
	for _, uid := range memberIDs {
		if uid == creatorID {
			continue // already added as owner
		}
		// Per-member failures are non-fatal — preserve legacy log-and-skip.
		if err := s.channels.AddMember(ctx, ch.ID, uid, repo.MemberRoleMember); err != nil {
			continue
		}
		added = append(added, AddedMember{UserID: uid})
	}

	// Anchor system messages so /api/sync replays the channel's history
	// symmetrically: one channel_created at seq=1 and a member_joined per
	// non-creator member, in the same order AddMember returned. Best-effort —
	// the channel + members are already in place, so losing a marker is
	// preferable to failing the whole request.
	_ = s.postSys(ctx, nil, ch.ID, creatorID, channelCreatedProps(creatorID, ch.Name))
	for _, m := range added {
		_ = s.postSys(ctx, nil, ch.ID, creatorID, memberJoinedProps(creatorID, m.UserID))
	}
	return ch, added, nil
}

// CreateOrGetDM returns the existing DM between callerID and otherUserID, or
// creates a fresh one. The bool indicates whether a new channel was created
// (true) so the transport can return 201 vs 200 to match the legacy shape.
func (s *ChannelService) CreateOrGetDM(ctx context.Context, callerID, otherUserID int64) (*repo.Channel, bool, error) {
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

	ch := &repo.Channel{Type: repo.ChannelTypeDM}
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
func (s *ChannelService) ListByUser(ctx context.Context, userID int64) ([]repo.ChannelWithPreview, error) {
	ctx, span := tracer.Start(ctx, "ChannelService.ListByUser")
	defer span.End()

	return s.channels.ListByUserWithPreview(ctx, userID)
}

// GetByID returns the channel only if callerID is a member. Non-members get
// ErrNotMember (→ 403). Missing channels get repo.ErrNotFound (→ 404).
func (s *ChannelService) GetByID(ctx context.Context, channelID, callerID int64) (*repo.Channel, error) {
	if _, err := s.channels.GetMember(ctx, channelID, callerID); err != nil {
		if errors.Is(err, repo.ErrNotFound) {
			return nil, ErrNotMember
		}
		return nil, err
	}
	return s.channels.GetByID(ctx, channelID)
}

// Update applies name/avatar to channelID. Requires admin or owner role.
// Returns the post-update channel for the response body. On success emits a
// channel_updated system message so online members see the change through
// push_msg (and offline members via /api/sync).
func (s *ChannelService) Update(ctx context.Context, channelID, callerID int64, name, avatarURL string) (*repo.Channel, error) {
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
	// Best-effort: the rename already committed; losing the system message
	// is recoverable by the next /api/channels list refresh.
	_ = s.postSys(ctx, nil, channelID, callerID, channelUpdatedProps(callerID, ch.Name, ch.AvatarURL))
	return ch, nil
}

// AddMember inserts newUserID into channelID. Requires caller to be admin or
// owner. The legacy handler always added the new member with the plain
// Member role — preserve that.
//
// Returns the channel's display name on success so the transport layer can
// fire a real-time channel_event "added" to the new member (mirrors the
// post-CreateGroup fan-out). An empty string is returned alongside any
// non-nil error.
func (s *ChannelService) AddMember(ctx context.Context, channelID, callerID, newUserID int64) (string, error) {
	ctx, span := tracer.Start(ctx, "ChannelService.AddMember")
	defer span.End()

	if err := s.requireAdminOrOwner(ctx, channelID, callerID); err != nil {
		return "", err
	}
	if err := s.channels.AddMember(ctx, channelID, newUserID, repo.MemberRoleMember); err != nil {
		return "", err
	}
	// Best-effort system message so existing members' already-read/unread
	// ratios stay consistent (the new member count changes immediately).
	_ = s.postSys(ctx, nil, channelID, callerID, memberJoinedProps(callerID, newUserID))
	// Best-effort: fetch the channel name for the push payload. A lookup
	// miss shouldn't fail the request — the membership row is already in
	// place. Transport fires the push with an empty name in that unlikely
	// case.
	ch, err := s.channels.GetByID(ctx, channelID)
	if err != nil || ch == nil {
		return "", nil
	}
	return ch.Name, nil
}

// RemoveMember deletes targetUserID from channelID. Requires admin or owner.
// Refuses to remove the channel's owner. Emits a member_removed system message
// BEFORE the DELETE inside the same transaction so the target still counts as
// a channel member when push fan-out runs (otherwise the event misses them).
func (s *ChannelService) RemoveMember(ctx context.Context, channelID, callerID, targetUserID int64) error {
	ctx, span := tracer.Start(ctx, "ChannelService.RemoveMember")
	defer span.End()

	if err := s.requireAdminOrOwner(ctx, channelID, callerID); err != nil {
		return err
	}
	target, err := s.channels.GetMember(ctx, channelID, targetUserID)
	if err != nil {
		return err // repo.ErrNotFound surfaces as 404
	}
	if target.Role == repo.MemberRoleOwner {
		return ErrCannotRemoveOwner
	}
	return s.removeMemberAtomic(ctx, channelID, callerID, targetUserID)
}

// removeMemberAtomic runs "post system message → DELETE channel_members" in a
// single transaction when s.messages is wired. When messages is nil (tests),
// it falls back to the existing non-tx DELETE to keep older tests green.
func (s *ChannelService) removeMemberAtomic(ctx context.Context, channelID, actorID, targetID int64) error {
	if s.messages == nil {
		return s.channels.RemoveMember(ctx, channelID, targetID)
	}
	return s.channels.WithinTx(ctx, func(tx *gorm.DB) error {
		if err := s.postSys(ctx, tx, channelID, actorID, memberRemovedProps(actorID, targetID)); err != nil {
			return err
		}
		return s.channels.RemoveMemberTx(ctx, tx, channelID, targetID)
	})
}

// ListMembers returns all members of channelID enriched with basic user info.
// Caller must be a member.
func (s *ChannelService) ListMembers(ctx context.Context, channelID, callerID int64) ([]MemberWithUser, error) {
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
	out := make([]MemberWithUser, 0, len(members))
	for _, m := range members {
		mwu := MemberWithUser{ChannelMember: m}
		// Best-effort enrichment — missing user rows are tolerated (matches
		// legacy: a member whose user row is missing still appears in the
		// list with empty profile fields).
		if u, err := s.users.GetByID(ctx, m.UserID); err == nil && u != nil {
			mwu.Username = u.Username
			mwu.DisplayName = u.DisplayName
			mwu.AvatarURL = u.AvatarURL
		}
		out = append(out, mwu)
	}
	return out, nil
}

// LeaveChannel removes callerID from channelID. Owners cannot leave (they
// must transfer ownership first — preserved from the legacy handler). Emits a
// member_left system message BEFORE the DELETE in the same transaction so
// remaining members see the event in real time.
func (s *ChannelService) LeaveChannel(ctx context.Context, channelID, callerID int64) error {
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

// leaveChannelAtomic mirrors removeMemberAtomic but uses member_left props so
// clients can render "X left" vs "X was removed".
func (s *ChannelService) leaveChannelAtomic(ctx context.Context, channelID, callerID int64) error {
	if s.messages == nil {
		return s.channels.RemoveMember(ctx, channelID, callerID)
	}
	return s.channels.WithinTx(ctx, func(tx *gorm.DB) error {
		if err := s.postSys(ctx, tx, channelID, callerID, memberLeftProps(callerID)); err != nil {
			return err
		}
		return s.channels.RemoveMemberTx(ctx, tx, channelID, callerID)
	})
}

// requireAdminOrOwner returns nil iff callerID is a member of channelID with
// Role >= Admin. ErrNotMember when not a member, ErrForbidden when role is
// insufficient. Mirrors handler.requireAdminOrOwner exactly.
func (s *ChannelService) requireAdminOrOwner(ctx context.Context, channelID, callerID int64) error {
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

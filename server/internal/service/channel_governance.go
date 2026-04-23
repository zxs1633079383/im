package service

import (
	"context"
	"errors"
	"fmt"

	"im-server/internal/repo"
)

// PatchChannelFields is the service-layer mirror of repo.PatchChannelFields,
// re-exported so HTTP handlers can construct it without reaching into the
// repo package directly. nil means "leave unchanged".
type PatchChannelFields = repo.PatchChannelFields

// ChannelGovernanceService layers fine-grained channel governance on top of
// the base ChannelService. It owns its own *ChannelRepo + *ChannelGovernanceRepo
// pair; the base ChannelService is re-used for the shared permission helpers.
type ChannelGovernanceService struct {
	channels   repo.ChannelRepo
	governance repo.ChannelGovernanceRepo
	users      repo.UserRepo
}

// NewChannelGovernanceService wires the repos.
func NewChannelGovernanceService(
	channels repo.ChannelRepo,
	governance repo.ChannelGovernanceRepo,
	users repo.UserRepo,
) *ChannelGovernanceService {
	return &ChannelGovernanceService{
		channels:   channels,
		governance: governance,
		users:      users,
	}
}

// PatchChannel applies p to channelID. Requires caller to be owner OR manager
// (via legacy role or via channel_managers table). Returns the post-update
// Channel so the HTTP layer can echo it back.
func (s *ChannelGovernanceService) PatchChannel(
	ctx context.Context, channelID, callerID int64, p PatchChannelFields,
) (*repo.Channel, error) {
	if err := s.requireManagerOrOwner(ctx, channelID, callerID); err != nil {
		return nil, err
	}
	if err := s.governance.PatchChannel(ctx, channelID, p); err != nil {
		return nil, err
	}
	return s.channels.GetByID(ctx, channelID)
}

// AddManager inserts targetID as a manager of channelID. Only the channel
// owner may add managers.
func (s *ChannelGovernanceService) AddManager(
	ctx context.Context, channelID, callerID, targetID int64,
) error {
	if err := s.requireOwner(ctx, channelID, callerID); err != nil {
		return err
	}
	// Target must be a member of the channel.
	if _, err := s.channels.GetMember(ctx, channelID, targetID); err != nil {
		if errors.Is(err, repo.ErrNotFound) {
			return ErrTargetNotMember
		}
		return err
	}
	return s.governance.AddManager(ctx, channelID, targetID, callerID)
}

// RemoveManager removes targetID from channel_managers. Only owner may remove.
func (s *ChannelGovernanceService) RemoveManager(
	ctx context.Context, channelID, callerID, targetID int64,
) error {
	if err := s.requireOwner(ctx, channelID, callerID); err != nil {
		return err
	}
	return s.governance.RemoveManager(ctx, channelID, targetID)
}

// ListManagers returns user IDs of managers in channelID. Callers must be
// channel members.
func (s *ChannelGovernanceService) ListManagers(
	ctx context.Context, channelID, callerID int64,
) ([]int64, error) {
	if err := s.requireMember(ctx, channelID, callerID); err != nil {
		return nil, err
	}
	return s.governance.ListManagers(ctx, channelID)
}

// PinMessage pins msgID in channelID. Manager+ only.
func (s *ChannelGovernanceService) PinMessage(
	ctx context.Context, channelID, callerID, msgID int64,
) error {
	if err := s.requireManagerOrOwner(ctx, channelID, callerID); err != nil {
		return err
	}
	return s.governance.PinMessage(ctx, channelID, msgID, callerID)
}

// UnpinMessage removes the pin for msgID. Manager+ only.
func (s *ChannelGovernanceService) UnpinMessage(
	ctx context.Context, channelID, callerID, msgID int64,
) error {
	if err := s.requireManagerOrOwner(ctx, channelID, callerID); err != nil {
		return err
	}
	return s.governance.UnpinMessage(ctx, channelID, msgID)
}

// ListPins returns pinned message IDs for channelID. Members may view.
func (s *ChannelGovernanceService) ListPins(
	ctx context.Context, channelID, callerID int64,
) ([]int64, error) {
	if err := s.requireMember(ctx, channelID, callerID); err != nil {
		return nil, err
	}
	return s.governance.ListPins(ctx, channelID)
}

// UpdateMemberRole updates a member's role. Only owner may update.
func (s *ChannelGovernanceService) UpdateMemberRole(
	ctx context.Context, channelID, callerID, targetID int64, role int16,
) error {
	if err := s.requireOwner(ctx, channelID, callerID); err != nil {
		return err
	}
	return s.governance.UpdateMemberRole(ctx, channelID, targetID, role)
}

// UpdateMemberNotifyPref lets the caller update their OWN notify_pref only.
// (A user doesn't get to change someone else's notification preference.)
func (s *ChannelGovernanceService) UpdateMemberNotifyPref(
	ctx context.Context, channelID, callerID int64, pref int16,
) error {
	if err := s.requireMember(ctx, channelID, callerID); err != nil {
		return err
	}
	if pref < repo.NotifyPrefAll || pref > repo.NotifyPrefNone {
		return ErrInvalidNotifyPref
	}
	return s.governance.UpdateMemberNotifyPref(ctx, channelID, callerID, pref)
}

// IsManagerOrOwner is exported so other services (announcements, urgent) can
// share the same admin check.
func (s *ChannelGovernanceService) IsManagerOrOwner(
	ctx context.Context, channelID, callerID int64,
) (bool, error) {
	m, err := s.channels.GetMember(ctx, channelID, callerID)
	if err != nil {
		if errors.Is(err, repo.ErrNotFound) {
			return false, nil
		}
		return false, err
	}
	// Legacy Admin/Owner roles count as manager+.
	if m.Role >= repo.MemberRoleAdmin {
		return true, nil
	}
	return s.governance.IsManager(ctx, channelID, callerID)
}

// requireMember returns ErrNotMember unless callerID is a member.
func (s *ChannelGovernanceService) requireMember(ctx context.Context, channelID, callerID int64) error {
	if _, err := s.channels.GetMember(ctx, channelID, callerID); err != nil {
		if errors.Is(err, repo.ErrNotFound) {
			return ErrNotMember
		}
		return fmt.Errorf("check member: %w", err)
	}
	return nil
}

// requireOwner returns ErrForbidden unless callerID is the channel owner
// (member with Role == MemberRoleOwner). Creator of the channel is also
// considered owner for compatibility with CreateGroup semantics.
func (s *ChannelGovernanceService) requireOwner(ctx context.Context, channelID, callerID int64) error {
	m, err := s.channels.GetMember(ctx, channelID, callerID)
	if err != nil {
		if errors.Is(err, repo.ErrNotFound) {
			return ErrNotMember
		}
		return err
	}
	if m.Role < repo.MemberRoleOwner {
		return ErrForbidden
	}
	return nil
}

// requireManagerOrOwner accepts legacy Admin/Owner roles OR an entry in
// channel_managers. Non-members get ErrNotMember; members lacking manager
// rights get ErrForbidden.
func (s *ChannelGovernanceService) requireManagerOrOwner(ctx context.Context, channelID, callerID int64) error {
	m, err := s.channels.GetMember(ctx, channelID, callerID)
	if err != nil {
		if errors.Is(err, repo.ErrNotFound) {
			return ErrNotMember
		}
		return err
	}
	if m.Role >= repo.MemberRoleAdmin {
		return nil
	}
	ok, err := s.governance.IsManager(ctx, channelID, callerID)
	if err != nil {
		return err
	}
	if !ok {
		return ErrForbidden
	}
	return nil
}

// Additional M2 sentinels.
var (
	ErrTargetNotMember   = errors.New("target user is not a member of this channel")
	ErrInvalidNotifyPref = errors.New("invalid notify_pref value")
)

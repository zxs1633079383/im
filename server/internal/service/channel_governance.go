package service

import (
	"context"
	"errors"
	"fmt"

	"im-server/internal/repo"
)

// PatchChannelFields is the service-layer mirror of repo.PatchChannelFields,
// re-exported so HTTP handlers can construct it without reaching into the
// repo package directly.
type PatchChannelFields = repo.PatchChannelFields

// ChannelGovernanceService layers fine-grained channel governance on top of
// the base ChannelService. M4 drops the UserRepo dependency — user identity
// comes from the resolved Mattermost cookie.
type ChannelGovernanceService struct {
	channels   repo.ChannelRepo
	governance repo.ChannelGovernanceRepo
}

// NewChannelGovernanceService wires the repos.
func NewChannelGovernanceService(
	channels repo.ChannelRepo,
	governance repo.ChannelGovernanceRepo,
) *ChannelGovernanceService {
	return &ChannelGovernanceService{
		channels:   channels,
		governance: governance,
	}
}

// PatchChannel applies p to channelID. Owner+manager required.
func (s *ChannelGovernanceService) PatchChannel(
	ctx context.Context, channelID int64, callerID string, p PatchChannelFields,
) (*repo.Channel, error) {
	ctx, span := tracer.Start(ctx, "ChannelGovernanceService.PatchChannel")
	defer span.End()

	if err := s.requireManagerOrOwner(ctx, channelID, callerID); err != nil {
		return nil, err
	}
	if err := s.governance.PatchChannel(ctx, channelID, p); err != nil {
		return nil, err
	}
	return s.channels.GetByID(ctx, channelID)
}

// AddManager inserts targetID as a manager of channelID. Owner-only.
func (s *ChannelGovernanceService) AddManager(
	ctx context.Context, channelID int64, callerID, targetID string,
) error {
	ctx, span := tracer.Start(ctx, "ChannelGovernanceService.AddManager")
	defer span.End()

	if err := s.requireOwner(ctx, channelID, callerID); err != nil {
		return err
	}
	if _, err := s.channels.GetMember(ctx, channelID, targetID); err != nil {
		if errors.Is(err, repo.ErrNotFound) {
			return ErrTargetNotMember
		}
		return err
	}
	return s.governance.AddManager(ctx, channelID, targetID, callerID)
}

// RemoveManager removes targetID from channel_managers. Owner-only.
func (s *ChannelGovernanceService) RemoveManager(
	ctx context.Context, channelID int64, callerID, targetID string,
) error {
	if err := s.requireOwner(ctx, channelID, callerID); err != nil {
		return err
	}
	return s.governance.RemoveManager(ctx, channelID, targetID)
}

// ListManagers returns mm UserIDs of managers in channelID. Members only.
func (s *ChannelGovernanceService) ListManagers(
	ctx context.Context, channelID int64, callerID string,
) ([]string, error) {
	if err := s.requireMember(ctx, channelID, callerID); err != nil {
		return nil, err
	}
	return s.governance.ListManagers(ctx, channelID)
}

// PinMessage pins msgID in channelID. Manager+ only.
func (s *ChannelGovernanceService) PinMessage(
	ctx context.Context, channelID int64, callerID string, msgID int64,
) error {
	ctx, span := tracer.Start(ctx, "ChannelGovernanceService.PinMessage")
	defer span.End()

	if err := s.requireManagerOrOwner(ctx, channelID, callerID); err != nil {
		return err
	}
	return s.governance.PinMessage(ctx, channelID, msgID, callerID)
}

// UnpinMessage removes the pin for msgID. Manager+ only.
func (s *ChannelGovernanceService) UnpinMessage(
	ctx context.Context, channelID int64, callerID string, msgID int64,
) error {
	if err := s.requireManagerOrOwner(ctx, channelID, callerID); err != nil {
		return err
	}
	return s.governance.UnpinMessage(ctx, channelID, msgID)
}

// ListPins returns pinned message IDs for channelID. Members may view.
func (s *ChannelGovernanceService) ListPins(
	ctx context.Context, channelID int64, callerID string,
) ([]int64, error) {
	if err := s.requireMember(ctx, channelID, callerID); err != nil {
		return nil, err
	}
	return s.governance.ListPins(ctx, channelID)
}

// UpdateMemberRole updates a member's role. Owner-only.
func (s *ChannelGovernanceService) UpdateMemberRole(
	ctx context.Context, channelID int64, callerID, targetID string, role int16,
) error {
	if err := s.requireOwner(ctx, channelID, callerID); err != nil {
		return err
	}
	return s.governance.UpdateMemberRole(ctx, channelID, targetID, role)
}

// UpdateMemberNotifyPref lets the caller update their OWN notify_pref only.
func (s *ChannelGovernanceService) UpdateMemberNotifyPref(
	ctx context.Context, channelID int64, callerID string, pref int16,
) error {
	if err := s.requireMember(ctx, channelID, callerID); err != nil {
		return err
	}
	if pref < repo.NotifyPrefAll || pref > repo.NotifyPrefNone {
		return ErrInvalidNotifyPref
	}
	return s.governance.UpdateMemberNotifyPref(ctx, channelID, callerID, pref)
}

// UpdateMemberIsTop pins or unpins the channel at the top of the caller's
// channel list. Per-user state — owners cannot change other members'
// is_top, so the API is implicitly self-only and only requires "is a
// member of this channel" as the authorization gate.
func (s *ChannelGovernanceService) UpdateMemberIsTop(
	ctx context.Context, channelID int64, callerID string, isTop bool,
) error {
	if err := s.requireMember(ctx, channelID, callerID); err != nil {
		return err
	}
	return s.governance.UpdateMemberIsTop(ctx, channelID, callerID, isTop)
}

// IsManagerOrOwner is exported so other services can share the admin check.
func (s *ChannelGovernanceService) IsManagerOrOwner(
	ctx context.Context, channelID int64, callerID string,
) (bool, error) {
	m, err := s.channels.GetMember(ctx, channelID, callerID)
	if err != nil {
		if errors.Is(err, repo.ErrNotFound) {
			return false, nil
		}
		return false, err
	}
	if m.Role >= repo.MemberRoleAdmin {
		return true, nil
	}
	return s.governance.IsManager(ctx, channelID, callerID)
}

func (s *ChannelGovernanceService) requireMember(ctx context.Context, channelID int64, callerID string) error {
	if _, err := s.channels.GetMember(ctx, channelID, callerID); err != nil {
		if errors.Is(err, repo.ErrNotFound) {
			return ErrNotMember
		}
		return fmt.Errorf("check member: %w", err)
	}
	return nil
}

func (s *ChannelGovernanceService) requireOwner(ctx context.Context, channelID int64, callerID string) error {
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

func (s *ChannelGovernanceService) requireManagerOrOwner(ctx context.Context, channelID int64, callerID string) error {
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

package service

import (
	"context"
	"errors"
	"fmt"

	"im-server/internal/repo"
)

// Announcement-service sentinels.
var (
	ErrAnnouncementTitleEmpty   = errors.New("title is required")
	ErrAnnouncementContentEmpty = errors.New("content is required")
	ErrAnnouncementDeleted      = errors.New("announcement already deleted")
)

// channelMemberStore is the minimal ChannelRepo subset the announcement
// service needs — consumer-side small interface per coding-style.
type channelMemberStore interface {
	GetMember(ctx context.Context, channelID string, userID string) (*repo.ChannelMember, error)
}

// managerCheck is a tiny interface the announcement service needs to verify
// manager-or-owner rights. Defined here so the announcement service only
// depends on what it actually uses.
type managerCheck interface {
	IsManagerOrOwner(ctx context.Context, channelID string, callerID string) (bool, error)
}

// AnnouncementService orchestrates channel announcements: create (manager+),
// read/ack (any member), list, detail, delete (creator or manager+), and ack
// listing (manager+).
type AnnouncementService struct {
	announcements repo.AnnouncementRepo
	channels      channelMemberStore
	mgr           managerCheck
}

// NewAnnouncementService wires the repos + the governance service used for
// manager checks.
func NewAnnouncementService(
	announcements repo.AnnouncementRepo,
	channels channelMemberStore,
	governance managerCheck,
) *AnnouncementService {
	return &AnnouncementService{
		announcements: announcements,
		channels:      channels,
		mgr:           governance,
	}
}

// CreateParams is the input to Create.
type CreateAnnouncementParams struct {
	ChannelID string
	CreatorID string
	Title     string
	Content   string
	Props     string // raw JSON (may be empty — defaults to "{}")
}

// Create inserts a new announcement. Manager+ only.
func (s *AnnouncementService) Create(ctx context.Context, p CreateAnnouncementParams) (*repo.Announcement, error) {
	ctx, span := tracer.Start(ctx, "AnnouncementService.Create")
	defer span.End()

	if p.Title == "" {
		return nil, ErrAnnouncementTitleEmpty
	}
	if p.Content == "" {
		return nil, ErrAnnouncementContentEmpty
	}
	ok, err := s.mgr.IsManagerOrOwner(ctx, p.ChannelID, p.CreatorID)
	if err != nil {
		return nil, fmt.Errorf("check manager: %w", err)
	}
	if !ok {
		// Must still distinguish non-member (403 "not a member") from
		// member-without-rights (403 "manager required"). The HTTP layer
		// maps both to 403 — we just need to not 500.
		if _, mErr := s.channels.GetMember(ctx, p.ChannelID, p.CreatorID); mErr != nil {
			if errors.Is(mErr, repo.ErrNotFound) {
				return nil, ErrNotMember
			}
			return nil, mErr
		}
		return nil, ErrForbidden
	}
	a := &repo.Announcement{
		ChannelID: p.ChannelID,
		CreatorID: p.CreatorID,
		Title:     p.Title,
		Content:   p.Content,
		Props:     p.Props,
	}
	if err := s.announcements.Create(ctx, a); err != nil {
		return nil, err
	}
	return a, nil
}

// Ack records an acknowledgement from userID on announcementID. Any member
// of the announcement's channel may ack.
func (s *AnnouncementService) Ack(ctx context.Context, announcementID string, userID string) error {
	ctx, span := tracer.Start(ctx, "AnnouncementService.Ack")
	defer span.End()

	a, err := s.announcements.GetByID(ctx, announcementID)
	if err != nil {
		return err
	}
	if a.Deleted {
		return ErrAnnouncementDeleted
	}
	if err := s.requireMember(ctx, a.ChannelID, userID); err != nil {
		return err
	}
	return s.announcements.AddAck(ctx, announcementID, userID)
}

// ListAcks returns the ack rows for announcementID. Manager+ only.
func (s *AnnouncementService) ListAcks(ctx context.Context, announcementID string, callerID string) ([]repo.AnnouncementAck, error) {
	ctx, span := tracer.Start(ctx, "AnnouncementService.ListAcks")
	defer span.End()

	a, err := s.announcements.GetByID(ctx, announcementID)
	if err != nil {
		return nil, err
	}
	ok, err := s.mgr.IsManagerOrOwner(ctx, a.ChannelID, callerID)
	if err != nil {
		return nil, fmt.Errorf("check manager: %w", err)
	}
	if !ok {
		if err := s.requireMember(ctx, a.ChannelID, callerID); err != nil {
			return nil, err
		}
		return nil, ErrForbidden
	}
	return s.announcements.ListAcks(ctx, announcementID)
}

// ListByChannel returns recent non-deleted announcements for channelID.
// Members only.
func (s *AnnouncementService) ListByChannel(ctx context.Context, channelID string, callerID string, limit, offset int) ([]repo.Announcement, error) {
	ctx, span := tracer.Start(ctx, "AnnouncementService.ListByChannel")
	defer span.End()

	if err := s.requireMember(ctx, channelID, callerID); err != nil {
		return nil, err
	}
	return s.announcements.ListByChannel(ctx, channelID, limit, offset)
}

// Get returns a single announcement. Members only.
func (s *AnnouncementService) Get(ctx context.Context, announcementID string, callerID string) (*repo.Announcement, error) {
	ctx, span := tracer.Start(ctx, "AnnouncementService.Get")
	defer span.End()

	a, err := s.announcements.GetByID(ctx, announcementID)
	if err != nil {
		return nil, err
	}
	if err := s.requireMember(ctx, a.ChannelID, callerID); err != nil {
		return nil, err
	}
	return a, nil
}

// Delete soft-deletes announcementID. Allowed if caller is the creator, OR
// caller is manager/owner of the channel.
func (s *AnnouncementService) Delete(ctx context.Context, announcementID string, callerID string) error {
	ctx, span := tracer.Start(ctx, "AnnouncementService.Delete")
	defer span.End()

	a, err := s.announcements.GetByID(ctx, announcementID)
	if err != nil {
		return err
	}
	if a.Deleted {
		return nil // idempotent
	}
	if a.CreatorID != callerID {
		ok, err := s.mgr.IsManagerOrOwner(ctx, a.ChannelID, callerID)
		if err != nil {
			return fmt.Errorf("check manager: %w", err)
		}
		if !ok {
			if err := s.requireMember(ctx, a.ChannelID, callerID); err != nil {
				return err
			}
			return ErrForbidden
		}
	}
	return s.announcements.SoftDelete(ctx, announcementID)
}

// requireMember is a local copy of ChannelGovernanceService.requireMember so
// the announcement service doesn't pull in the full governance struct.
func (s *AnnouncementService) requireMember(ctx context.Context, channelID string, callerID string) error {
	if _, err := s.channels.GetMember(ctx, channelID, callerID); err != nil {
		if errors.Is(err, repo.ErrNotFound) {
			return ErrNotMember
		}
		return fmt.Errorf("check member: %w", err)
	}
	return nil
}

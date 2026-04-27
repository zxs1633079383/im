package service

import (
	"context"
	"errors"
	"fmt"

	"im-server/internal/repo"
)

// CreateTopicRequest groups CreateTopic args so the method stays within the
// 5-arg limit. CallerID is the requesting mm UserID; MemberIDs are the
// initial topic members (must be a subset of parent's members). TeamID is
// the team scope inherited from the parent channel.
type CreateTopicRequest struct {
	CallerID      string
	TeamID        *string
	ParentID      int64
	RootMessageID int64
	Name          string
	MemberIDs     []string
}

// CreateTopic creates a topic channel (子群聊) under req.ParentID, enforcing
// authorization rules:
//   - caller must be a member of parent
//   - every requested MemberID must be a member of parent
func (s *ChannelService) CreateTopic(ctx context.Context, req CreateTopicRequest) (*repo.Channel, error) {
	ctx, span := tracer.Start(ctx, "ChannelService.CreateTopic")
	defer span.End()

	if _, err := s.channels.GetMember(ctx, req.ParentID, req.CallerID); err != nil {
		if errors.Is(err, repo.ErrNotFound) {
			return nil, ErrNotMember
		}
		return nil, err
	}
	if err := s.ensureMembersSubset(ctx, req.ParentID, req.MemberIDs); err != nil {
		return nil, err
	}
	return s.channels.CreateTopic(ctx, repo.CreateTopicParams{
		ParentID:      req.ParentID,
		RootMessageID: req.RootMessageID,
		Name:          req.Name,
		CreatorID:     req.CallerID,
		TeamID:        req.TeamID,
		MemberIDs:     req.MemberIDs,
	})
}

// ensureMembersSubset rejects with a clear error if any memberID is not a
// parent-channel member.
func (s *ChannelService) ensureMembersSubset(ctx context.Context, parentID int64, memberIDs []string) error {
	if len(memberIDs) == 0 {
		return nil
	}
	parentMembers, err := s.channels.ListMembers(ctx, parentID)
	if err != nil {
		return err
	}
	inParent := make(map[string]struct{}, len(parentMembers))
	for _, m := range parentMembers {
		inParent[m.UserID] = struct{}{}
	}
	for _, uid := range memberIDs {
		if _, ok := inParent[uid]; !ok {
			return fmt.Errorf("user %s not a member of parent channel %d", uid, parentID)
		}
	}
	return nil
}

// ListTopics returns all topic channels under parentID for the caller.
func (s *ChannelService) ListTopics(ctx context.Context, callerID string, parentID int64) ([]repo.Channel, error) {
	ctx, span := tracer.Start(ctx, "ChannelService.ListTopics")
	defer span.End()

	if _, err := s.channels.GetMember(ctx, parentID, callerID); err != nil {
		if errors.Is(err, repo.ErrNotFound) {
			return nil, ErrNotMember
		}
		return nil, err
	}
	return s.channels.ListTopics(ctx, parentID)
}

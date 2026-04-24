package service

import (
	"context"
	"errors"
	"fmt"

	"im-server/internal/repo"
)

// CreateTopicRequest groups CreateTopic args so the method stays within the
// 5-arg limit. CallerID must be a member of ParentID; MemberIDs must be a
// subset of ParentID's members.
type CreateTopicRequest struct {
	CallerID      int64
	ParentID      int64
	RootMessageID int64
	Name          string
	MemberIDs     []int64
}

// CreateTopic creates a topic channel (子群聊) under req.ParentID, enforcing
// authorization rules:
//   - caller must be a member of parent
//   - every requested MemberID must be a member of parent (topic members are
//     a subset of the parent channel)
//
// Authentication + name validation are the transport layer's responsibility.
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
		MemberIDs:     req.MemberIDs,
	})
}

// ensureMembersSubset rejects with a clear error if any memberID is not a
// parent-channel member. Keeps CreateTopic small and easy to read.
func (s *ChannelService) ensureMembersSubset(ctx context.Context, parentID int64, memberIDs []int64) error {
	if len(memberIDs) == 0 {
		return nil
	}
	parentMembers, err := s.channels.ListMembers(ctx, parentID)
	if err != nil {
		return err
	}
	inParent := make(map[int64]struct{}, len(parentMembers))
	for _, m := range parentMembers {
		inParent[m.UserID] = struct{}{}
	}
	for _, uid := range memberIDs {
		if _, ok := inParent[uid]; !ok {
			return fmt.Errorf("user %d not a member of parent channel %d", uid, parentID)
		}
	}
	return nil
}

// ListTopics returns all topic channels under parentID for the caller.
// Caller must be a member of parent; returns ErrNotMember otherwise.
func (s *ChannelService) ListTopics(ctx context.Context, callerID, parentID int64) ([]repo.Channel, error) {
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

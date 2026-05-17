package service

import (
	"context"
	"errors"
	"fmt"

	"gorm.io/gorm"

	"im-server/internal/repo"
)

// CreateTopicRequest groups CreateTopic args so the method stays within the
// 5-arg limit. CallerID is the requesting mm UserID; MemberIDs are the
// initial topic members (must be a subset of parent's members). TeamID is
// the team scope inherited from the parent channel.
type CreateTopicRequest struct {
	CallerID      string
	TeamID        *string
	ParentID      string
	RootMessageID string
	Name          string
	MemberIDs     []string
}

// CreateTopic creates a topic channel (子群聊) under req.ParentID, enforcing
// authorization rules:
//   - caller must be a member of parent
//   - every requested MemberID must be a member of parent
//
// 当 channelEvent 已挂载（生产路径），topic channel + 初始成员 + 每 channel
// 的 PG sequence (channel_msg_seq_<id>, channel_event_seq_<id>) 三步全部
// 跑在同一笔 WithinTx 里——这样 topic 创建后立刻发首条消息能命中已存在的
// sequence；否则会撞 `relation "channel_msg_seq_<uuid>" does not exist`
// (C018 §3.2 / P3 NEED_FOLLOWUP)。当 channelEvent 为 nil（部分老单测没接），
// 退化为原本的 CreateTopic 单 SQL 路径——这些测试本来就不走 per-channel
// sequence 路径，保持向后兼容。
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
	params := repo.CreateTopicParams{
		ParentID:      req.ParentID,
		RootMessageID: req.RootMessageID,
		Name:          req.Name,
		CreatorID:     req.CallerID,
		TeamID:        req.TeamID,
		MemberIDs:     req.MemberIDs,
	}
	if s.channelEvent == nil {
		// 老路径：channelEvent 未挂载（部分单测），仍走原 CreateTopic。
		return s.channels.CreateTopic(ctx, params)
	}

	var topic *repo.Channel
	err := s.channels.WithinTx(ctx, func(tx *gorm.DB) error {
		var inner error
		topic, inner = s.channels.CreateTopicTx(ctx, tx, params)
		if inner != nil {
			return fmt.Errorf("create topic: %w", inner)
		}
		if err := s.channelEvent.CreateChannelSequences(ctx, tx, topic.ID); err != nil {
			return fmt.Errorf("create topic sequences: %w", err)
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return topic, nil
}

// ensureMembersSubset rejects with a clear error if any memberID is not a
// parent-channel member.
func (s *ChannelService) ensureMembersSubset(ctx context.Context, parentID string, memberIDs []string) error {
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
			return fmt.Errorf("user %s not a member of parent channel %s", uid, parentID)
		}
	}
	return nil
}

// ListTopics returns all topic channels under parentID for the caller.
func (s *ChannelService) ListTopics(ctx context.Context, callerID string, parentID string) ([]repo.Channel, error) {
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

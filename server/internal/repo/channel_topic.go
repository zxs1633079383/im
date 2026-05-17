package repo

import (
	"context"
	"fmt"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// CreateTopicParams groups the arguments to ChannelRepo.CreateTopic so the
// function stays within the project's 5-arg limit.
//
// ParentID / RootMessageID pin the topic to its origin message; Name is the
// topic display name; CreatorID becomes the topic owner; MemberIDs is the
// initial member set (deduped, creator auto-added as owner). M4: CreatorID
// and MemberIDs are mm UserIDs (24-hex strings). TeamID is the topic's team
// scope inherited from the parent channel (frozen at creation).
type CreateTopicParams struct {
	ParentID      string
	RootMessageID string
	Name          string
	CreatorID     string
	TeamID        *string
	MemberIDs     []string
}

// CreateTopic creates a topic channel (子群聊) rooted at params.ParentID +
// params.RootMessageID, then atomically registers creator + memberIDs.
//
// Topic channels share the messages table + seq counter and the
// channel_members table with ordinary channels; discrimination is
// channels.root_id IS NOT NULL.
//
// 这里走 repo 自带连接开一笔事务后委托给 CreateTopicTx；CreateTopicTx 在 tx
// 非 nil 时直接复用 caller 的事务（P2.3：让 ChannelService.CreateTopic 把
// CreateChannelSequences 挂在同一事务里）。
func (r *gormChannelRepo) CreateTopic(ctx context.Context, p CreateTopicParams) (*Channel, error) {
	if p.ParentID == "" {
		return nil, fmt.Errorf("create topic: empty parent_id")
	}
	var topic *Channel
	err := r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var inner error
		topic, inner = r.CreateTopicTx(ctx, tx, p)
		return inner
	})
	if err != nil {
		return nil, err
	}
	return topic, nil
}

// CreateTopicTx 是 CreateTopic 的 tx-aware 变体（P2.3）。tx 非 nil 时
// 直接走 caller 事务，让 ChannelService.CreateTopic 能把 channel 创建 +
// CreateChannelSequences + 成员 fan-out 全部串在同一笔 tx 内。tx 为 nil
// 时退化为 r.db（仅给那些不需要事务的内部 caller 用）；外层
// gormChannelRepo.CreateTopic 还会自己开一笔 tx 包住调用，所以
// `tx == nil + 通过 CreateTopic 进入` 路径仍然原子。
func (r *gormChannelRepo) CreateTopicTx(ctx context.Context, tx *gorm.DB, p CreateTopicParams) (*Channel, error) {
	if p.ParentID == "" {
		return nil, fmt.Errorf("create topic: empty parent_id")
	}
	var topic Channel
	db := r.dbOr(ctx, tx)
	if err := insertTopicTx(db, p, &topic); err != nil {
		return nil, err
	}
	return &topic, nil
}

// insertTopicTx inserts the topic row then bulk-inserts its members in the
// same transaction. Separate from CreateTopic so the closure stays short.
func insertTopicTx(tx *gorm.DB, p CreateTopicParams, out *Channel) error {
	parent := p.ParentID
	rootMsg := p.RootMessageID
	*out = Channel{
		Type:          ChannelTypeGroup,
		Name:          p.Name,
		CreatorID:     p.CreatorID,
		TeamID:        p.TeamID,
		RootID:        &parent,
		RootMessageID: &rootMsg,
	}
	if err := tx.Create(out).Error; err != nil {
		return fmt.Errorf("insert topic channel: %w", err)
	}
	members := collectTopicMembers(out.ID, p.CreatorID, p.MemberIDs)
	if len(members) == 0 {
		return nil
	}
	return tx.Clauses(clause.OnConflict{DoNothing: true}).Create(&members).Error
}

// collectTopicMembers dedupes memberIDs and prepends the creator as owner.
// Exposed as a package-level helper so it's unit-testable without a real DB.
func collectTopicMembers(channelID string, creatorID string, memberIDs []string) []ChannelMember {
	seen := make(map[string]struct{}, len(memberIDs)+1)
	out := make([]ChannelMember, 0, len(memberIDs)+1)
	add := func(uid string, role int16) {
		if uid == "" {
			return
		}
		if _, ok := seen[uid]; ok {
			return
		}
		seen[uid] = struct{}{}
		out = append(out, ChannelMember{UserID: uid, ChannelID: channelID, Role: role})
	}
	add(creatorID, MemberRoleOwner)
	for _, uid := range memberIDs {
		add(uid, MemberRoleMember)
	}
	return out
}

// ListTopics returns all topic channels rooted at parentID, ordered by id.
// The partial index idx_channels_root_id makes this a cheap lookup even on
// large channels tables.
func (r *gormChannelRepo) ListTopics(ctx context.Context, parentID string) ([]Channel, error) {
	var out []Channel
	err := r.db.WithContext(ctx).
		Where("root_id = ?", parentID).
		Order("id ASC").
		Find(&out).Error
	if err != nil {
		return nil, fmt.Errorf("list topics: %w", err)
	}
	return out, nil
}

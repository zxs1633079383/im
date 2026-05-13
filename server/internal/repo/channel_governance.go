package repo

import (
	"context"
	"errors"
	"fmt"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// PatchChannelFields carries the subset of Channel fields that PATCH /api/channels/:id
// can update. A nil pointer means "leave this field unchanged". The service
// layer uses the zero-value-pointer convention instead of an any-keyed map to
// preserve type-safety at the caller boundary.
type PatchChannelFields struct {
	Name       *string
	AvatarURL  *string
	Notice     *string
	Purpose    *string
	PictureURL *string
	Props      *string // raw JSON string
	Orient     *int16
	Permission *int16
	IsTop      *bool
}

// ChannelGovernanceRepo exposes the M2 governance operations — fine-grained
// channel patch, managers (separate N:N table), pinned messages, and member
// role/notify_pref extensions. A separate interface keeps the base ChannelRepo
// surface small; callers compose the two at the service layer.
type ChannelGovernanceRepo interface {
	PatchChannel(ctx context.Context, channelID string, fields PatchChannelFields) error

	AddManager(ctx context.Context, channelID string, userID, addedBy string) error
	RemoveManager(ctx context.Context, channelID string, userID string) error
	ListManagers(ctx context.Context, channelID string) ([]string, error)
	IsManager(ctx context.Context, channelID string, userID string) (bool, error)

	PinMessage(ctx context.Context, channelID string, msgID string, pinnedBy string) error
	UnpinMessage(ctx context.Context, channelID string, msgID string) error
	ListPins(ctx context.Context, channelID string) ([]string, error)

	UpdateMemberRole(ctx context.Context, channelID string, userID string, role int16) error
	UpdateMemberNotifyPref(ctx context.Context, channelID string, userID string, pref int16) error
	// UpdateMemberIsTop flips the per-user "pin this channel to the top of
	// my list" state. Per-user (channel_members.is_top) — distinct from the
	// global channels.is_top column which stays for legacy compatibility but
	// is not surfaced by the API.
	UpdateMemberIsTop(ctx context.Context, channelID string, userID string, isTop bool) error
}

// gormChannelGovernanceRepo implements ChannelGovernanceRepo against GORM.
type gormChannelGovernanceRepo struct{ db *gorm.DB }

// NewChannelGovernanceRepo returns a GORM-backed ChannelGovernanceRepo.
func NewChannelGovernanceRepo(db *gorm.DB) ChannelGovernanceRepo {
	return &gormChannelGovernanceRepo{db: db}
}

// PatchChannel applies only the non-nil fields in p to channelID. Always bumps
// updated_at. Returns ErrNotFound if no row matches channelID.
func (r *gormChannelGovernanceRepo) PatchChannel(ctx context.Context, channelID string, p PatchChannelFields) error {
	updates := map[string]any{}
	if p.Name != nil {
		updates["name"] = *p.Name
	}
	if p.AvatarURL != nil {
		updates["avatar_url"] = *p.AvatarURL
	}
	if p.Notice != nil {
		updates["notice"] = *p.Notice
	}
	if p.Purpose != nil {
		updates["purpose"] = *p.Purpose
	}
	if p.PictureURL != nil {
		updates["picture_url"] = *p.PictureURL
	}
	if p.Props != nil {
		updates["props"] = *p.Props
	}
	if p.Orient != nil {
		updates["orient"] = *p.Orient
	}
	if p.Permission != nil {
		updates["permission"] = *p.Permission
	}
	if p.IsTop != nil {
		updates["is_top"] = *p.IsTop
	}
	if len(updates) == 0 {
		// Nothing to change — still confirm the channel exists so the caller
		// can surface 404 on a stale id.
		var count int64
		if err := r.db.WithContext(ctx).Model(&Channel{}).
			Where("id = ?", channelID).Count(&count).Error; err != nil {
			return fmt.Errorf("patch channel count: %w", err)
		}
		if count == 0 {
			return ErrNotFound
		}
		return nil
	}
	// updated_at has a trigger in 001_init, but be explicit for clarity.
	res := r.db.WithContext(ctx).Model(&Channel{}).
		Where("id = ?", channelID).Updates(updates)
	if res.Error != nil {
		return fmt.Errorf("patch channel: %w", res.Error)
	}
	if res.RowsAffected == 0 {
		return ErrNotFound
	}
	return nil
}

// AddManager inserts (channelID, userID) into channel_managers. Idempotent:
// if the row already exists, the insert is a no-op.
func (r *gormChannelGovernanceRepo) AddManager(ctx context.Context, channelID string, userID, addedBy string) error {
	m := &ChannelManager{ChannelID: channelID, UserID: userID, AddedBy: addedBy}
	err := r.db.WithContext(ctx).Clauses(clause.OnConflict{
		Columns:   []clause.Column{{Name: "channel_id"}, {Name: "user_id"}},
		DoNothing: true,
	}).Create(m).Error
	if err != nil {
		return fmt.Errorf("add manager: %w", err)
	}
	return nil
}

// RemoveManager is idempotent — no error if the pair didn't exist.
func (r *gormChannelGovernanceRepo) RemoveManager(ctx context.Context, channelID string, userID string) error {
	err := r.db.WithContext(ctx).
		Where("channel_id = ? AND user_id = ?", channelID, userID).
		Delete(&ChannelManager{}).Error
	if err != nil {
		return fmt.Errorf("remove manager: %w", err)
	}
	return nil
}

// ListManagers returns the mm UserIDs of every manager in channelID.
func (r *gormChannelGovernanceRepo) ListManagers(ctx context.Context, channelID string) ([]string, error) {
	var ids []string
	err := r.db.WithContext(ctx).Model(&ChannelManager{}).
		Where("channel_id = ?", channelID).
		Order("added_at ASC").
		Pluck("user_id", &ids).Error
	if err != nil {
		return nil, fmt.Errorf("list managers: %w", err)
	}
	return ids, nil
}

// IsManager returns true when userID has a manager row in channelID.
func (r *gormChannelGovernanceRepo) IsManager(ctx context.Context, channelID string, userID string) (bool, error) {
	var count int64
	err := r.db.WithContext(ctx).Model(&ChannelManager{}).
		Where("channel_id = ? AND user_id = ?", channelID, userID).
		Count(&count).Error
	if err != nil {
		return false, fmt.Errorf("is manager: %w", err)
	}
	return count > 0, nil
}

// PinMessage pins msgID in channelID. Idempotent on conflict.
func (r *gormChannelGovernanceRepo) PinMessage(ctx context.Context, channelID string, msgID string, pinnedBy string) error {
	p := &ChannelPinnedMessage{ChannelID: channelID, MessageID: msgID, PinnedBy: pinnedBy}
	err := r.db.WithContext(ctx).Clauses(clause.OnConflict{
		Columns:   []clause.Column{{Name: "channel_id"}, {Name: "message_id"}},
		DoNothing: true,
	}).Create(p).Error
	if err != nil {
		return fmt.Errorf("pin message: %w", err)
	}
	return nil
}

// UnpinMessage is idempotent — no error if the pin didn't exist.
func (r *gormChannelGovernanceRepo) UnpinMessage(ctx context.Context, channelID string, msgID string) error {
	err := r.db.WithContext(ctx).
		Where("channel_id = ? AND message_id = ?", channelID, msgID).
		Delete(&ChannelPinnedMessage{}).Error
	if err != nil {
		return fmt.Errorf("unpin message: %w", err)
	}
	return nil
}

// ListPins returns message IDs pinned in channelID, oldest-first.
func (r *gormChannelGovernanceRepo) ListPins(ctx context.Context, channelID string) ([]string, error) {
	var ids []string
	err := r.db.WithContext(ctx).Model(&ChannelPinnedMessage{}).
		Where("channel_id = ?", channelID).
		Order("pinned_at ASC").
		Pluck("message_id", &ids).Error
	if err != nil {
		return nil, fmt.Errorf("list pins: %w", err)
	}
	return ids, nil
}

// UpdateMemberRole sets channel_members.role for (channelID, userID). Returns
// ErrNotFound when the member row doesn't exist.
func (r *gormChannelGovernanceRepo) UpdateMemberRole(ctx context.Context, channelID string, userID string, role int16) error {
	res := r.db.WithContext(ctx).Model(&ChannelMember{}).
		Where("channel_id = ? AND user_id = ?", channelID, userID).
		Update("role", role)
	if res.Error != nil {
		return fmt.Errorf("update member role: %w", res.Error)
	}
	if res.RowsAffected == 0 {
		return ErrNotFound
	}
	return nil
}

// UpdateMemberNotifyPref sets channel_members.notify_pref for (channelID, userID).
func (r *gormChannelGovernanceRepo) UpdateMemberNotifyPref(ctx context.Context, channelID string, userID string, pref int16) error {
	res := r.db.WithContext(ctx).Model(&ChannelMember{}).
		Where("channel_id = ? AND user_id = ?", channelID, userID).
		Update("notify_pref", pref)
	if res.Error != nil {
		return fmt.Errorf("update notify pref: %w", res.Error)
	}
	if res.RowsAffected == 0 {
		return ErrNotFound
	}
	return nil
}

// UpdateMemberIsTop flips channel_members.is_top for (channelID, userID).
// Per-user state distinct from channels.is_top (legacy global). ErrNotFound
// when the member row is missing.
func (r *gormChannelGovernanceRepo) UpdateMemberIsTop(ctx context.Context, channelID string, userID string, isTop bool) error {
	res := r.db.WithContext(ctx).Model(&ChannelMember{}).
		Where("channel_id = ? AND user_id = ?", channelID, userID).
		Update("is_top", isTop)
	if res.Error != nil {
		return fmt.Errorf("update member is_top: %w", res.Error)
	}
	if res.RowsAffected == 0 {
		return ErrNotFound
	}
	return nil
}

// Ensure the interface is implemented (compile-time check).
var _ ChannelGovernanceRepo = (*gormChannelGovernanceRepo)(nil)

// Guard against a common copy-paste mistake — ListByUser-style scans that
// forget to filter by channel_id will see every row. Keep the error alias
// alive so callers can still match it the same way.
var _ = errors.Is

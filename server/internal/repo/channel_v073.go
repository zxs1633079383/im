package repo

import (
	"context"
	"fmt"
	"time"

	"gorm.io/gorm"
)

// SoftDelete stamps deleted_at = now() on the channel. The single statement
// guards against a re-close race by filtering `deleted_at IS NULL` in the
// WHERE clause — rows that lost the race return ErrGone so callers can skip
// the WS fan-out cleanly. (v0.7.3 gap #1+#3)
func (r *gormChannelRepo) SoftDelete(ctx context.Context, channelID string) (*Channel, error) {
	now := time.Now().UTC()
	res := r.db.WithContext(ctx).Model(&Channel{}).
		Where("id = ? AND deleted_at IS NULL", channelID).
		Updates(map[string]any{"deleted_at": now, "updated_at": now})
	if res.Error != nil {
		return nil, fmt.Errorf("soft delete channel: %w", res.Error)
	}
	if res.RowsAffected == 0 {
		// Either the channel does not exist or it is already closed. The
		// service-layer caller did GetByID + role check first, so the
		// remaining case is "already closed" — surface ErrGone to match the
		// idempotent semantics used by Message.SoftDelete.
		existing, err := r.GetByID(ctx, channelID)
		if err != nil {
			return nil, err
		}
		return existing, ErrGone
	}
	ch, err := r.GetByID(ctx, channelID)
	if err != nil {
		return nil, err
	}
	return ch, nil
}

// UpdateMemberNickname overwrites channel_members.nick_name. Returns
// ErrNotFound when no member row matches. Empty new value is allowed —
// callers use it to "clear" the override. (v0.7.3 gap #5)
func (r *gormChannelRepo) UpdateMemberNickname(ctx context.Context, channelID string, userID, nickName string) error {
	res := r.db.WithContext(ctx).Model(&ChannelMember{}).
		Where("user_id = ? AND channel_id = ?", userID, channelID).
		Update("nick_name", nickName)
	if res.Error != nil {
		return fmt.Errorf("update member nickname: %w", res.Error)
	}
	if res.RowsAffected == 0 {
		return ErrNotFound
	}
	return nil
}

// _ = gorm.DB is silenced — kept for the build-time check that the gorm
// import is exercised even when callers stub the package out in tests.
var _ = (*gorm.DB)(nil)

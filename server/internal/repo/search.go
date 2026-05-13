package repo

import (
	"context"
	"strings"

	"gorm.io/gorm"
)

// SearchRepo provides full-text search across messages and channels. M4
// removes user search from im — cses owns the user directory; the front-end
// queries cses-side endpoints (or pulls profiles via the Redis HASH "User")
// for that data, and im no longer keeps a local users table.
type SearchRepo interface {
	SearchMessages(ctx context.Context, q string, userID string, channelID string, limit int) ([]MessageSearchResult, error)
	SearchChannels(ctx context.Context, q string, callerID string, limit int) ([]Channel, error)
}

// MessageSearchResult embeds Message and adds the joined channel name.
type MessageSearchResult struct {
	Message
	ChannelName string `gorm:"column:channel_name" json:"channel_name"`
}

type gormSearchRepo struct {
	db *gorm.DB
}

// NewSearchRepo wires a SearchRepo over a gorm.DB.
func NewSearchRepo(db *gorm.DB) SearchRepo {
	return &gormSearchRepo{db: db}
}

// clampLimit normalises the caller's limit to the [1, 50] range used by the
// legacy store; out-of-range values fall back to 20.
func clampLimit(limit int) int {
	if limit <= 0 || limit > 50 {
		return 20
	}
	return limit
}

// SearchMessages returns messages whose content matches q (Postgres FTS,
// 'simple' config) inside channels where userID is a member. When channelID
// > 0 the search is scoped to that channel.
func (r *gormSearchRepo) SearchMessages(ctx context.Context, q string, userID string, channelID string, limit int) ([]MessageSearchResult, error) {
	limit = clampLimit(limit)
	if strings.TrimSpace(q) == "" {
		return nil, nil
	}

	const baseSelect = `
		SELECT m.id, m.channel_id, m.seq, m.client_msg_id, m.sender_id, m.team_id, m.msg_type,
		       m.content, m.visible_to, m.reply_to, m.forwarded_from, m.created_at,
		       c.name AS channel_name
		FROM messages m
		JOIN channels c ON c.id = m.channel_id
		JOIN channel_members cm ON cm.channel_id = m.channel_id AND cm.user_id = ?
		WHERE to_tsvector('simple', m.content) @@ plainto_tsquery('simple', ?)
	`

	var rows []MessageSearchResult
	var err error
	if channelID != "" {
		err = r.db.WithContext(ctx).Raw(
			baseSelect+` AND m.channel_id = ? ORDER BY m.created_at DESC LIMIT ?`,
			userID, q, channelID, limit,
		).Scan(&rows).Error
	} else {
		err = r.db.WithContext(ctx).Raw(
			baseSelect+` ORDER BY m.created_at DESC LIMIT ?`,
			userID, q, limit,
		).Scan(&rows).Error
	}
	if err != nil {
		return nil, err
	}
	return rows, nil
}

// SearchChannels returns group channels (type=2) the caller belongs to whose
// name matches q (ILIKE). DM channels are excluded.
func (r *gormSearchRepo) SearchChannels(ctx context.Context, q string, callerID string, limit int) ([]Channel, error) {
	limit = clampLimit(limit)
	q = strings.TrimSpace(q)
	if q == "" {
		return nil, nil
	}

	pattern := "%" + q + "%"
	var channels []Channel
	err := r.db.WithContext(ctx).Raw(`
		SELECT c.id, c.type, c.name, c.avatar_url, c.seq, c.creator_id, c.team_id,
		       c.created_at, c.updated_at
		FROM channels c
		JOIN channel_members cm ON cm.channel_id = c.id AND cm.user_id = ?
		WHERE c.type = ?
		  AND c.name ILIKE ?
		ORDER BY c.name
		LIMIT ?
	`, callerID, ChannelTypeGroup, pattern, limit).Scan(&channels).Error
	if err != nil {
		return nil, err
	}
	return channels, nil
}

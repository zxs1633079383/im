package repo

import (
	"context"
	"fmt"
)

// FetchRepliesPage returns up to limit replies to rootID with the standard
// visibility filter, starting at offset. Ordered by seq ASC so the result is
// chronological. (v0.7.3 gap #2 — paginated /posts/getReplyBranch replacement.)
func (r *gormMessageRepo) FetchRepliesPage(
	ctx context.Context,
	rootID string,
	userID string,
	offset, limit int,
) ([]Message, error) {
	if limit <= 0 {
		return []Message{}, nil
	}
	if offset < 0 {
		offset = 0
	}
	var out []Message
	err := r.db.WithContext(ctx).Raw(
		`SELECT id, channel_id, seq, client_msg_id, sender_id, msg_type, content,
		        visible_to, reply_to, forwarded_from, props, created_at, updated_at,
		        deleted, deleted_at
		 FROM messages
		 WHERE reply_to = ? AND deleted = FALSE
		   AND (visible_to IS NULL OR ? = ANY(visible_to) OR sender_id = ?)
		 ORDER BY seq ASC
		 OFFSET ? LIMIT ?`,
		rootID, userID, userID, offset, limit,
	).Scan(&out).Error
	if err != nil {
		return nil, fmt.Errorf("fetch replies page: %w", err)
	}
	return out, nil
}

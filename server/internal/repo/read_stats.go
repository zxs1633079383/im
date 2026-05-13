package repo

import (
	"context"
	"fmt"

	"github.com/lib/pq"
)

// ReadStat is the per-message read summary returned by GetReadStatsBatch.
// UnreadUserIDs is truncated to UnreadUserPreviewLimit; HasMoreUnread is
// true when the truncation lost data so the UI can show a "+N more" hint
// instead of a misleading complete list.
type ReadStat struct {
	MessageID     string   `json:"messageId"`
	TotalMembers  int      `json:"totalMembers"`
	ReadCount     int      `json:"readCount"`
	UnreadCount   int      `json:"unreadCount"`
	UnreadUserIDs []string `json:"unreadUserIds"`
	HasMoreUnread bool     `json:"hasMoreUnread"`
}

// UnreadUserPreviewLimit caps how many unread user IDs ride along in a
// single ReadStat. Picked at 50 so the JSON payload stays under ~3 KB even
// for the worst-case batch (100 messages × 50 IDs × 26 bytes).
const UnreadUserPreviewLimit = 50

// GetReadStatsBatch returns per-message read statistics for every message in
// msgIDs that lives in a channel callerID is a member of. Messages not in
// the caller's channels (and soft-deleted messages) are silently dropped —
// the response is keyed by messageId so the caller can detect missing
// entries.
//
// The single SQL query joins messages → channel_members and uses FILTER
// aggregates so PostgreSQL computes read_count / unread_count / unread_users
// in one pass per group; the array_agg is sorted by user_id so results are
// deterministic across calls (helpful for snapshot tests and stable UI).
//
// The Go-side truncation (UnreadUserPreviewLimit) keeps the response bounded
// regardless of group size; truncated lists set HasMoreUnread=true.
func (r *gormMessageRepo) GetReadStatsBatch(
	ctx context.Context,
	callerID string,
	msgIDs []string,
) ([]ReadStat, error) {
	ctx, span := tracer.Start(ctx, "MessageRepo.GetReadStatsBatch")
	defer span.End()

	if len(msgIDs) == 0 {
		return []ReadStat{}, nil
	}

	type row struct {
		MsgID        string         `gorm:"column:msg_id"`
		TotalMembers int            `gorm:"column:total_members"`
		ReadCount    int            `gorm:"column:read_count"`
		UnreadCount  int            `gorm:"column:unread_count"`
		UnreadUsers  pq.StringArray `gorm:"column:unread_users;type:text[]"`
	}

	var rows []row
	err := r.db.WithContext(ctx).Raw(`
		WITH msg AS (
			SELECT id, channel_id, seq FROM messages
			WHERE id = ANY(?::text[])
			  AND channel_id IN (
			      SELECT channel_id FROM channel_members WHERE user_id = ?
			  )
			  AND deleted = FALSE
		)
		SELECT msg.id AS msg_id,
		       COUNT(cm.user_id) AS total_members,
		       COUNT(*) FILTER (WHERE cm.last_read_seq >= msg.seq) AS read_count,
		       COUNT(*) FILTER (WHERE cm.last_read_seq <  msg.seq) AS unread_count,
		       COALESCE(
		           array_agg(cm.user_id ORDER BY cm.user_id)
		               FILTER (WHERE cm.last_read_seq < msg.seq),
		           '{}'
		       ) AS unread_users
		FROM msg
		JOIN channel_members cm ON cm.channel_id = msg.channel_id
		GROUP BY msg.id`,
		pq.Array(msgIDs), callerID,
	).Scan(&rows).Error
	if err != nil {
		return nil, fmt.Errorf("get read stats batch: %w", err)
	}

	out := make([]ReadStat, 0, len(rows))
	for _, r := range rows {
		stat := ReadStat{
			MessageID:    r.MsgID,
			TotalMembers: r.TotalMembers,
			ReadCount:    r.ReadCount,
			UnreadCount:  r.UnreadCount,
		}
		all := []string(r.UnreadUsers)
		if len(all) > UnreadUserPreviewLimit {
			stat.UnreadUserIDs = append([]string(nil), all[:UnreadUserPreviewLimit]...)
			stat.HasMoreUnread = true
		} else {
			stat.UnreadUserIDs = append([]string(nil), all...)
		}
		out = append(out, stat)
	}
	return out, nil
}

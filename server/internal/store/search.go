package store

import (
	"context"
	"fmt"

	"github.com/jackc/pgx/v5/pgxpool"
	"im-server/internal/model"
)

// SearchStore provides full-text and fuzzy search across messages, users, channels.
type SearchStore struct {
	pool *pgxpool.Pool
}

func NewSearchStore(pool *pgxpool.Pool) *SearchStore {
	return &SearchStore{pool: pool}
}

// MessageSearchResult extends model.Message with the channel name for display.
type MessageSearchResult struct {
	model.Message
	ChannelName string `json:"channel_name"`
}

// SearchMessages uses the GIN index on to_tsvector('simple', content) and
// falls back to ILIKE for short queries.  Only messages in channels where
// userID is a member are returned.
//
// If channelID > 0, results are restricted to that channel.
func (s *SearchStore) SearchMessages(ctx context.Context, q string, userID int64, channelID int64, limit int) ([]MessageSearchResult, error) {
	if limit <= 0 || limit > 50 {
		limit = 20
	}

	var (
		pgRows interface {
			Next() bool
			Scan(dest ...any) error
			Err() error
			Close()
		}
		err error
	)

	if channelID > 0 {
		pgRows, err = s.pool.Query(ctx,
			`SELECT m.id, m.channel_id, m.seq, m.client_msg_id, m.sender_id, m.msg_type,
			        m.content, m.visible_to, m.reply_to, m.forwarded_from, m.created_at,
			        c.name AS channel_name
			 FROM messages m
			 JOIN channels c ON c.id = m.channel_id
			 JOIN channel_members cm ON cm.channel_id = m.channel_id AND cm.user_id = $2
			 WHERE m.channel_id = $3
			   AND to_tsvector('simple', m.content) @@ plainto_tsquery('simple', $1)
			 ORDER BY m.created_at DESC
			 LIMIT $4`,
			q, userID, channelID, limit,
		)
	} else {
		pgRows, err = s.pool.Query(ctx,
			`SELECT m.id, m.channel_id, m.seq, m.client_msg_id, m.sender_id, m.msg_type,
			        m.content, m.visible_to, m.reply_to, m.forwarded_from, m.created_at,
			        c.name AS channel_name
			 FROM messages m
			 JOIN channels c ON c.id = m.channel_id
			 JOIN channel_members cm ON cm.channel_id = m.channel_id AND cm.user_id = $2
			 WHERE to_tsvector('simple', m.content) @@ plainto_tsquery('simple', $1)
			 ORDER BY m.created_at DESC
			 LIMIT $3`,
			q, userID, limit,
		)
	}
	if err != nil {
		return nil, fmt.Errorf("search messages: %w", err)
	}
	defer pgRows.Close()

	var results []MessageSearchResult
	for pgRows.Next() {
		var r MessageSearchResult
		var clientMsgID *string
		if err := pgRows.Scan(
			&r.ID, &r.ChannelID, &r.Seq, &clientMsgID, &r.SenderID, &r.MsgType,
			&r.Content, &r.VisibleTo, &r.ReplyTo, &r.ForwardedFrom, &r.CreatedAt,
			&r.ChannelName,
		); err != nil {
			return nil, fmt.Errorf("scan message result: %w", err)
		}
		if clientMsgID != nil {
			r.ClientMsgID = *clientMsgID
		}
		results = append(results, r)
	}
	return results, pgRows.Err()
}

// SearchUsers returns up to limit users whose username or display_name match q
// (ILIKE). The calling user is excluded.
func (s *SearchStore) SearchUsers(ctx context.Context, q string, callerID int64, limit int) ([]model.User, error) {
	if limit <= 0 || limit > 50 {
		limit = 20
	}
	pattern := "%" + q + "%"
	rows, err := s.pool.Query(ctx,
		`SELECT id, username, email, display_name, avatar_url, status, created_at, updated_at
		 FROM users
		 WHERE id != $1
		   AND status = 1
		   AND (username ILIKE $2 OR display_name ILIKE $2)
		 ORDER BY username
		 LIMIT $3`,
		callerID, pattern, limit,
	)
	if err != nil {
		return nil, fmt.Errorf("search users: %w", err)
	}
	defer rows.Close()

	var users []model.User
	for rows.Next() {
		var u model.User
		if err := rows.Scan(&u.ID, &u.Username, &u.Email, &u.DisplayName, &u.AvatarURL,
			&u.Status, &u.CreatedAt, &u.UpdatedAt); err != nil {
			return nil, fmt.Errorf("scan user: %w", err)
		}
		users = append(users, u)
	}
	return users, rows.Err()
}

// SearchChannels returns group channels (type=2) the caller belongs to whose
// name matches q (ILIKE). DM channels are excluded.
func (s *SearchStore) SearchChannels(ctx context.Context, q string, callerID int64, limit int) ([]model.Channel, error) {
	if limit <= 0 || limit > 50 {
		limit = 20
	}
	pattern := "%" + q + "%"
	rows, err := s.pool.Query(ctx,
		`SELECT c.id, c.type, c.name, c.avatar_url, c.seq, c.creator_id, c.created_at, c.updated_at
		 FROM channels c
		 JOIN channel_members cm ON cm.channel_id = c.id AND cm.user_id = $1
		 WHERE c.type = 2
		   AND c.name ILIKE $2
		 ORDER BY c.name
		 LIMIT $3`,
		callerID, pattern, limit,
	)
	if err != nil {
		return nil, fmt.Errorf("search channels: %w", err)
	}
	defer rows.Close()

	var channels []model.Channel
	for rows.Next() {
		var ch model.Channel
		if err := rows.Scan(&ch.ID, &ch.Type, &ch.Name, &ch.AvatarURL, &ch.Seq,
			&ch.CreatorID, &ch.CreatedAt, &ch.UpdatedAt); err != nil {
			return nil, fmt.Errorf("scan channel: %w", err)
		}
		channels = append(channels, ch)
	}
	return channels, rows.Err()
}

package store

import (
	"context"
	"fmt"

	"github.com/jackc/pgx/v5/pgxpool"
	"im-server/internal/model"
)

// FavoriteStore manages message favorites for users.
type FavoriteStore struct {
	pool *pgxpool.Pool
}

func NewFavoriteStore(pool *pgxpool.Pool) *FavoriteStore {
	return &FavoriteStore{pool: pool}
}

// FavoriteWithMessage extends MessageFavorite with the full message for display.
type FavoriteWithMessage struct {
	model.MessageFavorite
	Message model.Message `json:"message"`
}

// Add adds a message to the user's favorites. Idempotent.
func (s *FavoriteStore) Add(ctx context.Context, userID, messageID int64) error {
	_, err := s.pool.Exec(ctx,
		`INSERT INTO message_favorites (user_id, message_id)
		 VALUES ($1, $2)
		 ON CONFLICT (user_id, message_id) DO NOTHING`,
		userID, messageID,
	)
	if err != nil {
		return fmt.Errorf("add favorite: %w", err)
	}
	return nil
}

// Remove removes a message from the user's favorites. Idempotent.
func (s *FavoriteStore) Remove(ctx context.Context, userID, messageID int64) error {
	_, err := s.pool.Exec(ctx,
		`DELETE FROM message_favorites WHERE user_id = $1 AND message_id = $2`,
		userID, messageID,
	)
	if err != nil {
		return fmt.Errorf("remove favorite: %w", err)
	}
	return nil
}

// List returns all favorites for a user with the associated message, newest first.
func (s *FavoriteStore) List(ctx context.Context, userID int64) ([]FavoriteWithMessage, error) {
	rows, err := s.pool.Query(ctx,
		`SELECT mf.user_id, mf.message_id, mf.created_at,
		        m.id, m.channel_id, m.seq, m.client_msg_id, m.sender_id, m.msg_type,
		        m.content, m.visible_to, m.reply_to, m.forwarded_from, m.created_at
		 FROM message_favorites mf
		 JOIN messages m ON m.id = mf.message_id
		 WHERE mf.user_id = $1
		 ORDER BY mf.created_at DESC`,
		userID,
	)
	if err != nil {
		return nil, fmt.Errorf("list favorites: %w", err)
	}
	defer rows.Close()

	var results []FavoriteWithMessage
	for rows.Next() {
		var fw FavoriteWithMessage
		var clientMsgID *string
		if err := rows.Scan(
			&fw.UserID, &fw.MessageID, &fw.CreatedAt,
			&fw.Message.ID, &fw.Message.ChannelID, &fw.Message.Seq, &clientMsgID,
			&fw.Message.SenderID, &fw.Message.MsgType, &fw.Message.Content,
			&fw.Message.VisibleTo, &fw.Message.ReplyTo, &fw.Message.ForwardedFrom,
			&fw.Message.CreatedAt,
		); err != nil {
			return nil, fmt.Errorf("scan favorite: %w", err)
		}
		if clientMsgID != nil {
			fw.Message.ClientMsgID = *clientMsgID
		}
		results = append(results, fw)
	}
	return results, rows.Err()
}

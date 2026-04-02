package store

import (
	"context"
	"fmt"

	"github.com/jackc/pgx/v5/pgxpool"
	"im-server/internal/model"
)

type MessageStore struct {
	pool    *pgxpool.Pool
	channel *ChannelStore
}

func NewMessageStore(pool *pgxpool.Pool) *MessageStore {
	return &MessageStore{pool: pool, channel: NewChannelStore(pool)}
}

func (s *MessageStore) Send(ctx context.Context, msg *model.Message) error {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("begin tx: %w", err)
	}
	defer tx.Rollback(ctx)

	if msg.ClientMsgID != "" {
		var existingSeq int64
		var existingID int64
		err := tx.QueryRow(ctx,
			`SELECT id, seq FROM messages WHERE channel_id = $1 AND client_msg_id = $2`,
			msg.ChannelID, msg.ClientMsgID,
		).Scan(&existingID, &existingSeq)
		if err == nil {
			msg.ID = existingID
			msg.Seq = existingSeq
			return nil
		}
	}

	seq, err := s.channel.IncrementSeq(ctx, tx, msg.ChannelID)
	if err != nil {
		return err
	}
	msg.Seq = seq

	err = tx.QueryRow(ctx,
		`INSERT INTO messages (channel_id, seq, client_msg_id, sender_id, msg_type, content, visible_to, reply_to, forwarded_from)
		 VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9)
		 RETURNING id, created_at`,
		msg.ChannelID, msg.Seq, nilIfEmpty(msg.ClientMsgID), msg.SenderID, msg.MsgType,
		msg.Content, msg.VisibleTo, msg.ReplyTo, msg.ForwardedFrom,
	).Scan(&msg.ID, &msg.CreatedAt)
	if err != nil {
		return fmt.Errorf("insert message: %w", err)
	}

	if msg.VisibleTo != nil {
		visibleWithSender := append(msg.VisibleTo, msg.SenderID)
		if err := s.channel.IncrementPhantomCount(ctx, tx, msg.ChannelID, visibleWithSender); err != nil {
			return fmt.Errorf("update phantom count: %w", err)
		}
	}

	return tx.Commit(ctx)
}

func (s *MessageStore) FetchAfter(ctx context.Context, channelID int64, afterSeq int64, limit int) ([]model.Message, error) {
	rows, err := s.pool.Query(ctx,
		`SELECT id, channel_id, seq, client_msg_id, sender_id, msg_type, content, visible_to, reply_to, forwarded_from, created_at
		 FROM messages
		 WHERE channel_id = $1 AND seq > $2
		 ORDER BY seq
		 LIMIT $3`,
		channelID, afterSeq, limit,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanMessages(rows)
}

func (s *MessageStore) FetchForUser(ctx context.Context, channelID, userID int64, afterSeq int64, limit int) ([]model.Message, error) {
	rows, err := s.pool.Query(ctx,
		`SELECT id, channel_id, seq, client_msg_id, sender_id, msg_type, content, visible_to, reply_to, forwarded_from, created_at
		 FROM messages
		 WHERE channel_id = $1 AND seq > $2
		 ORDER BY seq
		 LIMIT $3`,
		channelID, afterSeq, limit,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	all, err := scanMessages(rows)
	if err != nil {
		return nil, err
	}

	result := make([]model.Message, 0, len(all))
	for _, m := range all {
		if m.IsVisibleTo(userID) {
			result = append(result, m)
		} else {
			result = append(result, model.Message{
				ChannelID: m.ChannelID,
				Seq:       m.Seq,
				MsgType:   model.MsgTypePhantom,
				CreatedAt: m.CreatedAt,
			})
		}
	}
	return result, nil
}

func (s *MessageStore) FetchBefore(ctx context.Context, channelID, userID int64, beforeSeq int64, limit int) ([]model.Message, error) {
	rows, err := s.pool.Query(ctx,
		`SELECT id, channel_id, seq, client_msg_id, sender_id, msg_type, content, visible_to, reply_to, forwarded_from, created_at
		 FROM messages
		 WHERE channel_id = $1 AND seq < $2
		 ORDER BY seq DESC
		 LIMIT $3`,
		channelID, beforeSeq, limit,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	all, err := scanMessages(rows)
	if err != nil {
		return nil, err
	}

	result := make([]model.Message, 0, len(all))
	for _, m := range all {
		if m.IsVisibleTo(userID) {
			result = append(result, m)
		} else {
			result = append(result, model.Message{
				ChannelID: m.ChannelID,
				Seq:       m.Seq,
				MsgType:   model.MsgTypePhantom,
				CreatedAt: m.CreatedAt,
			})
		}
	}
	return result, nil
}

func (s *MessageStore) FetchAround(ctx context.Context, channelID, userID int64, aroundSeq int64, limit int) ([]model.Message, error) {
	half := limit / 2
	rows, err := s.pool.Query(ctx,
		`(SELECT id, channel_id, seq, client_msg_id, sender_id, msg_type, content, visible_to, reply_to, forwarded_from, created_at
		  FROM messages WHERE channel_id = $1 AND seq <= $2 ORDER BY seq DESC LIMIT $3)
		 UNION ALL
		 (SELECT id, channel_id, seq, client_msg_id, sender_id, msg_type, content, visible_to, reply_to, forwarded_from, created_at
		  FROM messages WHERE channel_id = $1 AND seq > $2 ORDER BY seq LIMIT $3)
		 ORDER BY seq`,
		channelID, aroundSeq, half,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	all, err := scanMessages(rows)
	if err != nil {
		return nil, err
	}

	result := make([]model.Message, 0, len(all))
	for _, m := range all {
		if m.IsVisibleTo(userID) {
			result = append(result, m)
		} else {
			result = append(result, model.Message{
				ChannelID: m.ChannelID,
				Seq:       m.Seq,
				MsgType:   model.MsgTypePhantom,
				CreatedAt: m.CreatedAt,
			})
		}
	}
	return result, nil
}

type scannable interface {
	Next() bool
	Scan(dest ...any) error
	Err() error
}

func scanMessages(rows scannable) ([]model.Message, error) {
	var messages []model.Message
	for rows.Next() {
		var m model.Message
		var clientMsgID *string
		if err := rows.Scan(&m.ID, &m.ChannelID, &m.Seq, &clientMsgID, &m.SenderID, &m.MsgType,
			&m.Content, &m.VisibleTo, &m.ReplyTo, &m.ForwardedFrom, &m.CreatedAt); err != nil {
			return nil, err
		}
		if clientMsgID != nil {
			m.ClientMsgID = *clientMsgID
		}
		messages = append(messages, m)
	}
	return messages, rows.Err()
}

func nilIfEmpty(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}

package repo

import (
	"context"
	"errors"
	"fmt"

	"gorm.io/gorm"
)

// MessageRepo persists chat messages.
//
// Send is transactional: it allocates the next channel.seq via
// ChannelRepo.IncrementSeq, inserts the message, and (for directed messages
// with VisibleTo set) bumps phantom_count for excluded members via
// ChannelRepo.IncrementPhantomCount — all in a single GORM transaction so
// seq allocation and the insert never desync.
//
// Send is idempotent on (channel_id, client_msg_id): a second call with the
// same client_msg_id no-ops and returns the original ID/Seq.
type MessageRepo interface {
	Send(ctx context.Context, msg *Message) error
	GetByID(ctx context.Context, id int64) (*Message, error)
	FetchAfter(ctx context.Context, channelID, afterSeq int64, limit int) ([]Message, error)
	FetchForUser(ctx context.Context, channelID, userID, afterSeq int64, limit int) ([]Message, error)
	FetchBefore(ctx context.Context, channelID, userID, beforeSeq int64, limit int) ([]Message, error)
	FetchAround(ctx context.Context, channelID, userID, aroundSeq int64, limit int) ([]Message, error)
}

type gormMessageRepo struct {
	db      *gorm.DB
	channel ChannelRepo
}

// NewMessageRepo returns a GORM-backed MessageRepo. The ChannelRepo is used
// to compose IncrementSeq + IncrementPhantomCount inside the Send transaction.
func NewMessageRepo(db *gorm.DB, channel ChannelRepo) MessageRepo {
	return &gormMessageRepo{db: db, channel: channel}
}

// Send runs idempotency check, seq allocation, insert, and phantom_count
// bump in a single transaction. See MessageRepo.Send for semantics.
func (r *gormMessageRepo) Send(ctx context.Context, msg *Message) error {
	return r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		// 1. Idempotency: short-circuit if (channel_id, client_msg_id)
		//    already exists.
		if msg.ClientMsgID != "" {
			var existing Message
			err := tx.Select("id", "seq").
				Where("channel_id = ? AND client_msg_id = ?", msg.ChannelID, msg.ClientMsgID).
				First(&existing).Error
			if err == nil {
				msg.ID = existing.ID
				msg.Seq = existing.Seq
				return nil
			}
			if !errors.Is(err, gorm.ErrRecordNotFound) {
				return fmt.Errorf("idempotency check: %w", err)
			}
		}

		// 2. Allocate the next per-channel seq inside the same tx so seq
		//    monotonicity holds even under concurrent Send.
		seq, err := r.channel.IncrementSeq(ctx, tx, msg.ChannelID)
		if err != nil {
			return err
		}
		msg.Seq = seq

		// 3. Insert. Empty client_msg_id must land as SQL NULL (the column
		//    is nullable and the unique index treats NULLs as distinct);
		//    Omit() skips the column so Postgres applies its default.
		insert := tx
		if msg.ClientMsgID == "" {
			insert = insert.Omit("ClientMsgID")
		}
		if err := insert.Create(msg).Error; err != nil {
			return fmt.Errorf("insert message: %w", err)
		}

		// 4. Directed message: bump phantom_count for every member NOT in
		//    visible_to (sender stays included so they don't see a phantom
		//    of their own message).
		if msg.VisibleTo != nil {
			visibleWithSender := append([]int64(msg.VisibleTo), msg.SenderID)
			if err := r.channel.IncrementPhantomCount(ctx, tx, msg.ChannelID, visibleWithSender); err != nil {
				return fmt.Errorf("phantom: %w", err)
			}
		}
		return nil
	})
}

func (r *gormMessageRepo) GetByID(ctx context.Context, id int64) (*Message, error) {
	var m Message
	if err := r.db.WithContext(ctx).First(&m, id).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, ErrNotFound
		}
		return nil, fmt.Errorf("get message by id: %w", err)
	}
	return &m, nil
}

func (r *gormMessageRepo) FetchAfter(ctx context.Context, channelID, afterSeq int64, limit int) ([]Message, error) {
	var out []Message
	err := r.db.WithContext(ctx).
		Where("channel_id = ? AND seq > ?", channelID, afterSeq).
		Order("seq").
		Limit(limit).
		Find(&out).Error
	if err != nil {
		return nil, fmt.Errorf("fetch after: %w", err)
	}
	return out, nil
}

// FetchForUser returns messages for channelID with seq > afterSeq, filtered
// to those visible to userID: visible_to IS NULL (broadcast), userID is in
// visible_to, or userID is the sender.
func (r *gormMessageRepo) FetchForUser(ctx context.Context, channelID, userID, afterSeq int64, limit int) ([]Message, error) {
	var out []Message
	err := r.db.WithContext(ctx).Raw(
		`SELECT id, channel_id, seq, client_msg_id, sender_id, msg_type, content,
		        visible_to, reply_to, forwarded_from, created_at
		 FROM messages
		 WHERE channel_id = ? AND seq > ?
		   AND (visible_to IS NULL OR ? = ANY(visible_to) OR sender_id = ?)
		 ORDER BY seq
		 LIMIT ?`,
		channelID, afterSeq, userID, userID, limit,
	).Scan(&out).Error
	if err != nil {
		return nil, fmt.Errorf("fetch for user: %w", err)
	}
	return out, nil
}

// FetchBefore returns up to limit messages with seq < beforeSeq, filtered
// by visible_to for userID. Result is ordered by seq ASC (oldest first) so
// callers get a contiguous chronological window when concatenated with
// FetchAfter.
func (r *gormMessageRepo) FetchBefore(ctx context.Context, channelID, userID, beforeSeq int64, limit int) ([]Message, error) {
	var out []Message
	err := r.db.WithContext(ctx).Raw(
		`SELECT * FROM (
		    SELECT id, channel_id, seq, client_msg_id, sender_id, msg_type, content,
		           visible_to, reply_to, forwarded_from, created_at
		    FROM messages
		    WHERE channel_id = ? AND seq < ?
		      AND (visible_to IS NULL OR ? = ANY(visible_to) OR sender_id = ?)
		    ORDER BY seq DESC
		    LIMIT ?
		 ) t ORDER BY seq`,
		channelID, beforeSeq, userID, userID, limit,
	).Scan(&out).Error
	if err != nil {
		return nil, fmt.Errorf("fetch before: %w", err)
	}
	return out, nil
}

// FetchAround returns up to limit messages centered on aroundSeq (half before,
// half after, both halves filtered by visible_to for userID). Ordered by seq.
func (r *gormMessageRepo) FetchAround(ctx context.Context, channelID, userID, aroundSeq int64, limit int) ([]Message, error) {
	half := limit / 2
	var out []Message
	err := r.db.WithContext(ctx).Raw(
		`(SELECT id, channel_id, seq, client_msg_id, sender_id, msg_type, content,
		         visible_to, reply_to, forwarded_from, created_at
		  FROM messages
		  WHERE channel_id = ? AND seq <= ?
		    AND (visible_to IS NULL OR ? = ANY(visible_to) OR sender_id = ?)
		  ORDER BY seq DESC LIMIT ?)
		 UNION ALL
		 (SELECT id, channel_id, seq, client_msg_id, sender_id, msg_type, content,
		         visible_to, reply_to, forwarded_from, created_at
		  FROM messages
		  WHERE channel_id = ? AND seq > ?
		    AND (visible_to IS NULL OR ? = ANY(visible_to) OR sender_id = ?)
		  ORDER BY seq LIMIT ?)
		 ORDER BY seq`,
		channelID, aroundSeq, userID, userID, half,
		channelID, aroundSeq, userID, userID, half,
	).Scan(&out).Error
	if err != nil {
		return nil, fmt.Errorf("fetch around: %w", err)
	}
	return out, nil
}

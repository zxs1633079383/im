package store

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"im-server/internal/model"
)

type ChannelStore struct {
	pool *pgxpool.Pool
}

func NewChannelStore(pool *pgxpool.Pool) *ChannelStore {
	return &ChannelStore{pool: pool}
}

func (s *ChannelStore) Create(ctx context.Context, ch *model.Channel) error {
	return s.pool.QueryRow(ctx,
		`INSERT INTO channels (type, name, avatar_url, creator_id)
		 VALUES ($1, $2, $3, $4)
		 RETURNING id, seq, created_at, updated_at`,
		ch.Type, ch.Name, ch.AvatarURL, ch.CreatorID,
	).Scan(&ch.ID, &ch.Seq, &ch.CreatedAt, &ch.UpdatedAt)
}

func (s *ChannelStore) GetByID(ctx context.Context, id int64) (*model.Channel, error) {
	ch := &model.Channel{}
	err := s.pool.QueryRow(ctx,
		`SELECT id, type, name, avatar_url, seq, creator_id, created_at, updated_at
		 FROM channels WHERE id = $1`, id,
	).Scan(&ch.ID, &ch.Type, &ch.Name, &ch.AvatarURL, &ch.Seq, &ch.CreatorID, &ch.CreatedAt, &ch.UpdatedAt)
	if err != nil {
		return nil, fmt.Errorf("get channel: %w", err)
	}
	return ch, nil
}

func (s *ChannelStore) IncrementSeq(ctx context.Context, tx pgx.Tx, channelID int64) (int64, error) {
	var seq int64
	q := `UPDATE channels SET seq = seq + 1 WHERE id = $1 RETURNING seq`
	var err error
	if tx != nil {
		err = tx.QueryRow(ctx, q, channelID).Scan(&seq)
	} else {
		err = s.pool.QueryRow(ctx, q, channelID).Scan(&seq)
	}
	if err != nil {
		return 0, fmt.Errorf("increment seq: %w", err)
	}
	return seq, nil
}

func (s *ChannelStore) AddMember(ctx context.Context, channelID, userID int64, role model.MemberRole) error {
	_, err := s.pool.Exec(ctx,
		`INSERT INTO channel_members (user_id, channel_id, role)
		 VALUES ($1, $2, $3)
		 ON CONFLICT (user_id, channel_id) DO NOTHING`,
		userID, channelID, role,
	)
	return err
}

func (s *ChannelStore) RemoveMember(ctx context.Context, channelID, userID int64) error {
	_, err := s.pool.Exec(ctx,
		`DELETE FROM channel_members WHERE user_id = $1 AND channel_id = $2`,
		userID, channelID,
	)
	return err
}

func (s *ChannelStore) GetMember(ctx context.Context, channelID, userID int64) (*model.ChannelMember, error) {
	m := &model.ChannelMember{}
	err := s.pool.QueryRow(ctx,
		`SELECT user_id, channel_id, role, last_read_seq, phantom_count, phantom_at_read, joined_at
		 FROM channel_members WHERE user_id = $1 AND channel_id = $2`,
		userID, channelID,
	).Scan(&m.UserID, &m.ChannelID, &m.Role, &m.LastReadSeq, &m.PhantomCount, &m.PhantomAtRead, &m.JoinedAt)
	if err != nil {
		return nil, fmt.Errorf("get member: %w", err)
	}
	return m, nil
}

func (s *ChannelStore) ListMembers(ctx context.Context, channelID int64) ([]model.ChannelMember, error) {
	rows, err := s.pool.Query(ctx,
		`SELECT user_id, channel_id, role, last_read_seq, phantom_count, phantom_at_read, joined_at
		 FROM channel_members WHERE channel_id = $1`, channelID,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var members []model.ChannelMember
	for rows.Next() {
		var m model.ChannelMember
		if err := rows.Scan(&m.UserID, &m.ChannelID, &m.Role, &m.LastReadSeq, &m.PhantomCount, &m.PhantomAtRead, &m.JoinedAt); err != nil {
			return nil, err
		}
		members = append(members, m)
	}
	return members, rows.Err()
}

func (s *ChannelStore) ListByUser(ctx context.Context, userID int64) ([]model.Channel, error) {
	rows, err := s.pool.Query(ctx,
		`SELECT c.id, c.type, c.name, c.avatar_url, c.seq, c.creator_id, c.created_at, c.updated_at
		 FROM channels c
		 JOIN channel_members cm ON cm.channel_id = c.id
		 WHERE cm.user_id = $1
		 ORDER BY c.updated_at DESC`, userID,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var channels []model.Channel
	for rows.Next() {
		var ch model.Channel
		if err := rows.Scan(&ch.ID, &ch.Type, &ch.Name, &ch.AvatarURL, &ch.Seq, &ch.CreatorID, &ch.CreatedAt, &ch.UpdatedAt); err != nil {
			return nil, err
		}
		channels = append(channels, ch)
	}
	return channels, rows.Err()
}

func (s *ChannelStore) MarkRead(ctx context.Context, channelID, userID, seq int64) error {
	_, err := s.pool.Exec(ctx,
		`UPDATE channel_members
		 SET last_read_seq = $3, phantom_at_read = phantom_count
		 WHERE user_id = $1 AND channel_id = $2`,
		userID, channelID, seq,
	)
	return err
}

func (s *ChannelStore) IncrementPhantomCount(ctx context.Context, tx pgx.Tx, channelID int64, excludeUserIDs []int64) error {
	q := `UPDATE channel_members SET phantom_count = phantom_count + 1
	      WHERE channel_id = $1 AND user_id != ALL($2)`
	var err error
	if tx != nil {
		_, err = tx.Exec(ctx, q, channelID, excludeUserIDs)
	} else {
		_, err = s.pool.Exec(ctx, q, channelID, excludeUserIDs)
	}
	return err
}

// ChannelWithPreview is a Channel enriched with last-message info and unread count.
type ChannelWithPreview struct {
	model.Channel
	LastMsgContent string    `json:"last_msg_content"`
	LastMsgAt      time.Time `json:"last_msg_at"`
	UnreadCount    int64     `json:"unread_count"`
}

// FindDM returns the DM channel that exists between userA and userB.
// Returns ErrNotFound if no such channel exists.
func (s *ChannelStore) FindDM(ctx context.Context, userA, userB int64) (*model.Channel, error) {
	ch := &model.Channel{}
	err := s.pool.QueryRow(ctx,
		`SELECT c.id, c.type, c.name, c.avatar_url, c.seq, c.creator_id, c.created_at, c.updated_at
		 FROM channels c
		 JOIN channel_members ma ON ma.channel_id = c.id AND ma.user_id = $1
		 JOIN channel_members mb ON mb.channel_id = c.id AND mb.user_id = $2
		 WHERE c.type = $3
		 LIMIT 1`,
		userA, userB, model.ChannelTypeDM,
	).Scan(&ch.ID, &ch.Type, &ch.Name, &ch.AvatarURL, &ch.Seq, &ch.CreatorID, &ch.CreatedAt, &ch.UpdatedAt)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, ErrNotFound
		}
		return nil, fmt.Errorf("find dm: %w", err)
	}
	return ch, nil
}

// ListByUserWithPreview returns channels for userID enriched with the last
// message preview and the caller's unread count.
// Channels are ordered by last activity (last message time, or channel created_at).
func (s *ChannelStore) ListByUserWithPreview(ctx context.Context, userID int64) ([]ChannelWithPreview, error) {
	rows, err := s.pool.Query(ctx,
		`SELECT
		    c.id, c.type,
		    CASE WHEN c.type = 1 THEN (
		        SELECT u.display_name FROM users u
		        JOIN channel_members peer_cm ON peer_cm.channel_id = c.id AND peer_cm.user_id = u.id
		        WHERE u.id != $1
		        LIMIT 1
		    ) ELSE c.name END AS name,
		    c.avatar_url, c.seq, c.creator_id, c.created_at, c.updated_at,
		    COALESCE(m.content, '')                         AS last_msg_content,
		    COALESCE(m.created_at, c.created_at)            AS last_msg_at,
		    GREATEST(
		        (c.seq - cm.last_read_seq) - (cm.phantom_count - cm.phantom_at_read),
		        0
		    )                                               AS unread_count
		 FROM channels c
		 JOIN channel_members cm ON cm.channel_id = c.id AND cm.user_id = $1
		 LEFT JOIN LATERAL (
		     SELECT content, created_at
		     FROM messages
		     WHERE channel_id = c.id
		     ORDER BY seq DESC
		     LIMIT 1
		 ) m ON true
		 ORDER BY COALESCE(m.created_at, c.created_at) DESC`,
		userID,
	)
	if err != nil {
		return nil, fmt.Errorf("list by user with preview: %w", err)
	}
	defer rows.Close()

	var result []ChannelWithPreview
	for rows.Next() {
		var cp ChannelWithPreview
		if err := rows.Scan(
			&cp.ID, &cp.Type, &cp.Name, &cp.AvatarURL, &cp.Seq, &cp.CreatorID,
			&cp.CreatedAt, &cp.UpdatedAt,
			&cp.LastMsgContent, &cp.LastMsgAt, &cp.UnreadCount,
		); err != nil {
			return nil, fmt.Errorf("scan channel preview: %w", err)
		}
		result = append(result, cp)
	}
	return result, rows.Err()
}

// GetMemberChannelSeqs returns the current server seq for every channel the
// user belongs to. Used by the heartbeat to compute the pong diff.
func (s *ChannelStore) GetMemberChannelSeqs(ctx context.Context, userID int64) (map[int64]int64, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT c.id, c.seq
		FROM channels c
		JOIN channel_members cm ON cm.channel_id = c.id
		WHERE cm.user_id = $1
	`, userID)
	if err != nil {
		return nil, fmt.Errorf("get member channel seqs: %w", err)
	}
	defer rows.Close()

	m := make(map[int64]int64)
	for rows.Next() {
		var id, seq int64
		if err := rows.Scan(&id, &seq); err != nil {
			return nil, fmt.Errorf("scan channel seq: %w", err)
		}
		m[id] = seq
	}
	return m, rows.Err()
}

// Update sets the name and/or avatar_url of a channel.
// Pass empty string to leave a field unchanged.
func (s *ChannelStore) Update(ctx context.Context, channelID int64, name, avatarURL string) error {
	_, err := s.pool.Exec(ctx,
		`UPDATE channels
		 SET name       = CASE WHEN $2 <> '' THEN $2 ELSE name END,
		     avatar_url = CASE WHEN $3 <> '' THEN $3 ELSE avatar_url END,
		     updated_at = now()
		 WHERE id = $1`,
		channelID, name, avatarURL,
	)
	if err != nil {
		return fmt.Errorf("update channel: %w", err)
	}
	return nil
}

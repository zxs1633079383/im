package repo

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/lib/pq"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// Channel type constants (mirror internal/model.ChannelType).
const (
	ChannelTypeDM    int16 = 1
	ChannelTypeGroup int16 = 2
)

// Channel member role constants (mirror internal/model.MemberRole).
const (
	MemberRoleMember int16 = 1
	MemberRoleAdmin  int16 = 2
	MemberRoleOwner  int16 = 3
)

// ChannelWithPreview is a Channel enriched with last-message info and unread
// count for the calling user. Used to render channel lists in the UI.
type ChannelWithPreview struct {
	Channel
	LastMsgContent string    `json:"last_msg_content"`
	LastMsgAt      time.Time `json:"last_msg_at"`
	UnreadCount    int64     `json:"unread_count"`
}

// ChannelRepo manages channels and their members.
//
// IncrementSeq and IncrementPhantomCount accept an optional *gorm.DB (nil ⇒
// the repo's own connection). Pass a transaction to compose them inside a
// MessageRepo write.
type ChannelRepo interface {
	Create(ctx context.Context, ch *Channel) error
	GetByID(ctx context.Context, id int64) (*Channel, error)
	Update(ctx context.Context, channelID int64, name, avatarURL string) error
	IncrementSeq(ctx context.Context, tx *gorm.DB, channelID int64) (int64, error)
	AddMember(ctx context.Context, channelID, userID int64, role int16) error
	RemoveMember(ctx context.Context, channelID, userID int64) error
	GetMember(ctx context.Context, channelID, userID int64) (*ChannelMember, error)
	ListMembers(ctx context.Context, channelID int64) ([]ChannelMember, error)
	ListByUser(ctx context.Context, userID int64) ([]Channel, error)
	MarkRead(ctx context.Context, channelID, userID, seq int64) error
	IncrementPhantomCount(ctx context.Context, tx *gorm.DB, channelID int64, excludeUserIDs []int64) error
	FindDM(ctx context.Context, userA, userB int64) (*Channel, error)
	ListByUserWithPreview(ctx context.Context, userID int64) ([]ChannelWithPreview, error)
	GetMemberChannelSeqs(ctx context.Context, userID int64) (map[int64]int64, error)
}

type gormChannelRepo struct{ db *gorm.DB }

// NewChannelRepo returns a GORM-backed ChannelRepo.
func NewChannelRepo(db *gorm.DB) ChannelRepo { return &gormChannelRepo{db: db} }

// dbOr returns tx if non-nil, otherwise the repo's own *gorm.DB. Both are
// then chained with WithContext so the caller doesn't have to.
func (r *gormChannelRepo) dbOr(ctx context.Context, tx *gorm.DB) *gorm.DB {
	if tx != nil {
		return tx.WithContext(ctx)
	}
	return r.db.WithContext(ctx)
}

func (r *gormChannelRepo) Create(ctx context.Context, ch *Channel) error {
	if err := r.db.WithContext(ctx).Create(ch).Error; err != nil {
		return fmt.Errorf("create channel: %w", err)
	}
	return nil
}

func (r *gormChannelRepo) GetByID(ctx context.Context, id int64) (*Channel, error) {
	var ch Channel
	if err := r.db.WithContext(ctx).First(&ch, id).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, ErrNotFound
		}
		return nil, fmt.Errorf("get channel: %w", err)
	}
	return &ch, nil
}

// Update sets name and/or avatar_url. Empty strings leave the field unchanged.
// Always bumps updated_at to now().
func (r *gormChannelRepo) Update(ctx context.Context, channelID int64, name, avatarURL string) error {
	err := r.db.WithContext(ctx).Exec(
		`UPDATE channels
		 SET name       = CASE WHEN ? <> '' THEN ? ELSE name END,
		     avatar_url = CASE WHEN ? <> '' THEN ? ELSE avatar_url END,
		     updated_at = now()
		 WHERE id = ?`,
		name, name, avatarURL, avatarURL, channelID,
	).Error
	if err != nil {
		return fmt.Errorf("update channel: %w", err)
	}
	return nil
}

// IncrementSeq atomically bumps channels.seq and returns the new value.
// If tx is nil, runs against the repo's own connection.
func (r *gormChannelRepo) IncrementSeq(ctx context.Context, tx *gorm.DB, channelID int64) (int64, error) {
	var seq int64
	err := r.dbOr(ctx, tx).Raw(
		`UPDATE channels SET seq = seq + 1 WHERE id = ? RETURNING seq`,
		channelID,
	).Scan(&seq).Error
	if err != nil {
		return 0, fmt.Errorf("increment seq: %w", err)
	}
	return seq, nil
}

func (r *gormChannelRepo) AddMember(ctx context.Context, channelID, userID int64, role int16) error {
	m := &ChannelMember{
		UserID:    userID,
		ChannelID: channelID,
		Role:      role,
	}
	// Match existing INSERT ... ON CONFLICT (user_id, channel_id) DO NOTHING.
	err := r.db.WithContext(ctx).Clauses(clause.OnConflict{
		Columns:   []clause.Column{{Name: "user_id"}, {Name: "channel_id"}},
		DoNothing: true,
	}).Create(m).Error
	if err != nil {
		return fmt.Errorf("add member: %w", err)
	}
	return nil
}

func (r *gormChannelRepo) RemoveMember(ctx context.Context, channelID, userID int64) error {
	// Mirror the existing pgx semantics: DELETE is idempotent — no error
	// when no row matches.
	err := r.db.WithContext(ctx).
		Where("user_id = ? AND channel_id = ?", userID, channelID).
		Delete(&ChannelMember{}).Error
	if err != nil {
		return fmt.Errorf("remove member: %w", err)
	}
	return nil
}

func (r *gormChannelRepo) GetMember(ctx context.Context, channelID, userID int64) (*ChannelMember, error) {
	var m ChannelMember
	err := r.db.WithContext(ctx).
		Where("user_id = ? AND channel_id = ?", userID, channelID).
		First(&m).Error
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, ErrNotFound
		}
		return nil, fmt.Errorf("get member: %w", err)
	}
	return &m, nil
}

func (r *gormChannelRepo) ListMembers(ctx context.Context, channelID int64) ([]ChannelMember, error) {
	var members []ChannelMember
	err := r.db.WithContext(ctx).
		Where("channel_id = ?", channelID).
		Find(&members).Error
	if err != nil {
		return nil, fmt.Errorf("list members: %w", err)
	}
	return members, nil
}

func (r *gormChannelRepo) ListByUser(ctx context.Context, userID int64) ([]Channel, error) {
	var channels []Channel
	err := r.db.WithContext(ctx).Raw(
		`SELECT c.id, c.type, c.name, c.avatar_url, c.seq, c.creator_id, c.created_at, c.updated_at
		 FROM channels c
		 JOIN channel_members cm ON cm.channel_id = c.id
		 WHERE cm.user_id = ?
		 ORDER BY c.updated_at DESC`,
		userID,
	).Scan(&channels).Error
	if err != nil {
		return nil, fmt.Errorf("list by user: %w", err)
	}
	return channels, nil
}

func (r *gormChannelRepo) MarkRead(ctx context.Context, channelID, userID, seq int64) error {
	err := r.db.WithContext(ctx).Exec(
		`UPDATE channel_members
		 SET last_read_seq = ?, phantom_at_read = phantom_count
		 WHERE user_id = ? AND channel_id = ?`,
		seq, userID, channelID,
	).Error
	if err != nil {
		return fmt.Errorf("mark read: %w", err)
	}
	return nil
}

// IncrementPhantomCount bumps phantom_count for every member of channelID
// EXCEPT the users in excludeUserIDs. excludeUserIDs may be empty/nil.
// If tx is nil, runs against the repo's own connection.
func (r *gormChannelRepo) IncrementPhantomCount(ctx context.Context, tx *gorm.DB, channelID int64, excludeUserIDs []int64) error {
	// Normalise nil → empty slice so pq sends '{}'::bigint[] (not NULL).
	// `user_id != ALL(NULL)` evaluates to NULL and matches no rows, breaking
	// the "exclude nobody" case.
	if excludeUserIDs == nil {
		excludeUserIDs = []int64{}
	}
	err := r.dbOr(ctx, tx).Exec(
		`UPDATE channel_members SET phantom_count = phantom_count + 1
		 WHERE channel_id = ? AND user_id != ALL(?)`,
		channelID, pq.Int64Array(excludeUserIDs),
	).Error
	if err != nil {
		return fmt.Errorf("increment phantom count: %w", err)
	}
	return nil
}

// FindDM returns the DM channel that exists between userA and userB.
// Returns ErrNotFound if no such channel exists.
func (r *gormChannelRepo) FindDM(ctx context.Context, userA, userB int64) (*Channel, error) {
	var ch Channel
	err := r.db.WithContext(ctx).Raw(
		`SELECT c.id, c.type, c.name, c.avatar_url, c.seq, c.creator_id, c.created_at, c.updated_at
		 FROM channels c
		 JOIN channel_members ma ON ma.channel_id = c.id AND ma.user_id = ?
		 JOIN channel_members mb ON mb.channel_id = c.id AND mb.user_id = ?
		 WHERE c.type = ?
		 LIMIT 1`,
		userA, userB, ChannelTypeDM,
	).Scan(&ch).Error
	if err != nil {
		return nil, fmt.Errorf("find dm: %w", err)
	}
	if ch.ID == 0 {
		return nil, ErrNotFound
	}
	return &ch, nil
}

// ListByUserWithPreview returns channels for userID enriched with the last
// message preview and the caller's unread count. Channels are ordered by
// last activity (last message time, falling back to channel created_at).
func (r *gormChannelRepo) ListByUserWithPreview(ctx context.Context, userID int64) ([]ChannelWithPreview, error) {
	var result []ChannelWithPreview
	err := r.db.WithContext(ctx).Raw(
		`SELECT
		    c.id, c.type,
		    CASE WHEN c.type = 1 THEN (
		        SELECT u.display_name FROM users u
		        JOIN channel_members peer_cm ON peer_cm.channel_id = c.id AND peer_cm.user_id = u.id
		        WHERE u.id != ?
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
		 JOIN channel_members cm ON cm.channel_id = c.id AND cm.user_id = ?
		 LEFT JOIN LATERAL (
		     SELECT content, created_at
		     FROM messages
		     WHERE channel_id = c.id
		     ORDER BY seq DESC
		     LIMIT 1
		 ) m ON true
		 ORDER BY COALESCE(m.created_at, c.created_at) DESC`,
		userID, userID,
	).Scan(&result).Error
	if err != nil {
		return nil, fmt.Errorf("list by user with preview: %w", err)
	}
	return result, nil
}

// GetMemberChannelSeqs returns {channel_id: seq} for every channel the user
// belongs to. Used by the heartbeat to compute the pong diff.
func (r *gormChannelRepo) GetMemberChannelSeqs(ctx context.Context, userID int64) (map[int64]int64, error) {
	type row struct {
		ID  int64
		Seq int64
	}
	var rows []row
	err := r.db.WithContext(ctx).Raw(
		`SELECT c.id, c.seq
		 FROM channels c
		 JOIN channel_members cm ON cm.channel_id = c.id
		 WHERE cm.user_id = ?`,
		userID,
	).Scan(&rows).Error
	if err != nil {
		return nil, fmt.Errorf("get member channel seqs: %w", err)
	}
	out := make(map[int64]int64, len(rows))
	for _, r := range rows {
		out[r.ID] = r.Seq
	}
	return out, nil
}

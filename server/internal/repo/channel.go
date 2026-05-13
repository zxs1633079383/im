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
//
// M4: PeerUserID replaces the joined display_name; for DM channels it carries
// the mm UserID of the OTHER party so the front-end can fetch the profile
// from the cses Redis. For group channels PeerUserID is empty and the
// channel's own Name applies.
type ChannelWithPreview struct {
	Channel
	PeerUserID     string    `gorm:"column:peer_user_id" json:"peer_user_id,omitempty"`
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
	GetByID(ctx context.Context, id string) (*Channel, error)
	Update(ctx context.Context, channelID string, name, avatarURL string) error
	IncrementSeq(ctx context.Context, tx *gorm.DB, channelID string) (int64, error)
	AddMember(ctx context.Context, channelID string, userID string, role int16) error
	RemoveMember(ctx context.Context, channelID string, userID string) error
	GetMember(ctx context.Context, channelID string, userID string) (*ChannelMember, error)
	ListMembers(ctx context.Context, channelID string) ([]ChannelMember, error)
	ListByUser(ctx context.Context, userID string) ([]Channel, error)
	MarkRead(ctx context.Context, channelID string, userID string, seq int64) error
	IncrementPhantomCount(ctx context.Context, tx *gorm.DB, channelID string, excludeUserIDs []string) error
	FindDM(ctx context.Context, userA, userB string) (*Channel, error)
	ListByUserWithPreview(ctx context.Context, userID string) ([]ChannelWithPreview, error)
	GetMemberChannelSeqs(ctx context.Context, userID string) (map[string]int64, error)

	// M3-A Topic (子群聊) 能力。CreateTopic 原子地创建 topic channel + 批量
	// 注册成员；ListTopics 返回 parentID 下所有 topic（按 id 排序）。
	CreateTopic(ctx context.Context, params CreateTopicParams) (*Channel, error)
	ListTopics(ctx context.Context, parentID string) ([]Channel, error)

	// AddMemberTx / RemoveMemberTx are tx-aware siblings of AddMember /
	// RemoveMember. They exist so service-layer code can compose a system
	// message insert + the membership mutation inside a single transaction.
	AddMemberTx(ctx context.Context, tx *gorm.DB, channelID string, userID string, role int16) error
	RemoveMemberTx(ctx context.Context, tx *gorm.DB, channelID string, userID string) error

	// WithinTx runs fn inside a gorm transaction, exposing the tx handle so
	// the caller can thread it through AddMemberTx / RemoveMemberTx /
	// MessageRepo.AllocSeqAndInsert.
	WithinTx(ctx context.Context, fn func(tx *gorm.DB) error) error

	// SoftDelete marks the channel as closed by stamping deleted_at = now().
	// Idempotent: rows with deleted_at already set return ErrGone so the
	// service layer can skip re-broadcasting channel_closed. (v0.7.3 gap #1)
	SoftDelete(ctx context.Context, channelID string) (*Channel, error)

	// UpdateMemberNickname overwrites channel_members.nick_name for the given
	// (channel_id, user_id) tuple. Empty string is allowed (clears the
	// override → falls back to global display name). Returns ErrNotFound when
	// the member row does not exist. (v0.7.3 gap #5)
	UpdateMemberNickname(ctx context.Context, channelID string, userID, nickName string) error
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

func (r *gormChannelRepo) GetByID(ctx context.Context, id string) (*Channel, error) {
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
func (r *gormChannelRepo) Update(ctx context.Context, channelID string, name, avatarURL string) error {
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
func (r *gormChannelRepo) IncrementSeq(ctx context.Context, tx *gorm.DB, channelID string) (int64, error) {
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

func (r *gormChannelRepo) AddMember(ctx context.Context, channelID string, userID string, role int16) error {
	return r.AddMemberTx(ctx, nil, channelID, userID, role)
}

// AddMemberTx is the tx-aware variant of AddMember. When tx is nil it falls
// back to the repo's own connection; otherwise the INSERT runs inside the
// caller's transaction so membership changes can be atomic with sibling
// writes (see ChannelService.AddMember → system message).
func (r *gormChannelRepo) AddMemberTx(ctx context.Context, tx *gorm.DB, channelID string, userID string, role int16) error {
	m := &ChannelMember{
		UserID:    userID,
		ChannelID: channelID,
		Role:      role,
	}
	err := r.dbOr(ctx, tx).Clauses(clause.OnConflict{
		Columns:   []clause.Column{{Name: "user_id"}, {Name: "channel_id"}},
		DoNothing: true,
	}).Create(m).Error
	if err != nil {
		return fmt.Errorf("add member: %w", err)
	}
	return nil
}

func (r *gormChannelRepo) RemoveMember(ctx context.Context, channelID string, userID string) error {
	return r.RemoveMemberTx(ctx, nil, channelID, userID)
}

// RemoveMemberTx is the tx-aware variant of RemoveMember. DELETE is idempotent
// — zero rows matched is not an error, matching the existing pgx semantics.
func (r *gormChannelRepo) RemoveMemberTx(ctx context.Context, tx *gorm.DB, channelID string, userID string) error {
	err := r.dbOr(ctx, tx).
		Where("user_id = ? AND channel_id = ?", userID, channelID).
		Delete(&ChannelMember{}).Error
	if err != nil {
		return fmt.Errorf("remove member: %w", err)
	}
	return nil
}

// WithinTx runs fn inside a GORM transaction.
func (r *gormChannelRepo) WithinTx(ctx context.Context, fn func(tx *gorm.DB) error) error {
	return r.db.WithContext(ctx).Transaction(fn)
}

func (r *gormChannelRepo) GetMember(ctx context.Context, channelID string, userID string) (*ChannelMember, error) {
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

func (r *gormChannelRepo) ListMembers(ctx context.Context, channelID string) ([]ChannelMember, error) {
	var members []ChannelMember
	err := r.db.WithContext(ctx).
		Where("channel_id = ?", channelID).
		Find(&members).Error
	if err != nil {
		return nil, fmt.Errorf("list members: %w", err)
	}
	return members, nil
}

func (r *gormChannelRepo) ListByUser(ctx context.Context, userID string) ([]Channel, error) {
	var channels []Channel
	err := r.db.WithContext(ctx).Raw(
		`SELECT c.id, c.type, c.name, c.avatar_url, c.seq, c.creator_id, c.team_id, c.created_at, c.updated_at
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

func (r *gormChannelRepo) MarkRead(ctx context.Context, channelID string, userID string, seq int64) error {
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
func (r *gormChannelRepo) IncrementPhantomCount(ctx context.Context, tx *gorm.DB, channelID string, excludeUserIDs []string) error {
	// Normalise nil → empty slice so pq sends '{}'::text[] (not NULL).
	// `user_id != ALL(NULL)` evaluates to NULL and matches no rows, breaking
	// the "exclude nobody" case.
	if excludeUserIDs == nil {
		excludeUserIDs = []string{}
	}
	err := r.dbOr(ctx, tx).Exec(
		`UPDATE channel_members SET phantom_count = phantom_count + 1
		 WHERE channel_id = ? AND user_id != ALL(?)`,
		channelID, pq.StringArray(excludeUserIDs),
	).Error
	if err != nil {
		return fmt.Errorf("increment phantom count: %w", err)
	}
	return nil
}

// FindDM returns the DM channel that exists between userA and userB.
// Returns ErrNotFound if no such channel exists.
//
// Query shape: drive from channel_members(user_id=A)[PK] → filter DM channel
// → EXISTS lookup on channel_members(channel_id, user_id=B) via the M3-C
// index idx_channel_members_channel_user.
func (r *gormChannelRepo) FindDM(ctx context.Context, userA, userB string) (*Channel, error) {
	var ch Channel
	err := r.db.WithContext(ctx).Raw(
		`SELECT c.id, c.type, c.name, c.avatar_url, c.seq, c.creator_id, c.team_id, c.created_at, c.updated_at
		 FROM channel_members ma
		 JOIN channels c ON c.id = ma.channel_id AND c.type = ?
		 WHERE ma.user_id = ?
		   AND EXISTS (
		     SELECT 1 FROM channel_members mb
		     WHERE mb.channel_id = ma.channel_id AND mb.user_id = ?
		   )
		 LIMIT 1`,
		ChannelTypeDM, userA, userB,
	).Scan(&ch).Error
	if err != nil {
		return nil, fmt.Errorf("find dm: %w", err)
	}
	if ch.ID == "" {
		return nil, ErrNotFound
	}
	return &ch, nil
}

// ListByUserWithPreview returns channels for userID enriched with the last
// message preview and the caller's unread count. Channels are ordered by
// last activity (last message time, falling back to channel created_at).
//
// M4: For DM channels, peer_user_id is the OTHER member's mm UserID (the
// caller resolves the display name from cses Redis). Group channels keep
// their own name and peer_user_id is empty.
func (r *gormChannelRepo) ListByUserWithPreview(ctx context.Context, userID string) ([]ChannelWithPreview, error) {
	var result []ChannelWithPreview
	err := r.db.WithContext(ctx).Raw(
		`SELECT
		    c.id, c.type, c.name, c.avatar_url, c.seq, c.creator_id, c.team_id,
		    c.created_at, c.updated_at,
		    CASE WHEN c.type = 1 THEN (
		        SELECT peer_cm.user_id FROM channel_members peer_cm
		        WHERE peer_cm.channel_id = c.id AND peer_cm.user_id != ?
		        LIMIT 1
		    ) ELSE '' END                                   AS peer_user_id,
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
func (r *gormChannelRepo) GetMemberChannelSeqs(ctx context.Context, userID string) (map[string]int64, error) {
	type row struct {
		ID  string
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
	out := make(map[string]int64, len(rows))
	for _, r := range rows {
		out[r.ID] = r.Seq
	}
	return out, nil
}

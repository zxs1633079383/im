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

// ChannelWithPreview is a Channel enriched with the caller's view (last-msg
// preview, unread count, read cursor, mute/top prefs, nickname, phantom
// counters) plus per-channel FIFO slices for urgent + mention queues.
//
// Designed to be the *single* request that powers connection bootstrap +
// reconnect bootstrap on the desktop client (analogous to Telegram's
// `messages.dialogs` payload — see cses-client harness C007).
//
// Shape:
//
//	┌─ Channel meta (id/name/avatar/seq/...)
//	├─ Peer (DM only)
//	├─ Last message preview (content + created_at + top_message_id)
//	├─ Member view (last_read_seq, phantom_count, phantom_at_read,
//	│   notify_pref, is_top, nick_name) — from channel_members where uid=caller
//	├─ Channel-max-seq (= channels.seq aliased for client clarity)
//	└─ FIFO slices:
//	    UrgentInChannel  — unconfirmed urgent msgs (ASC by created_at, ≤50/channel)
//	    MentionInChannel — @-caller / @all msgs (DESC by created_at, ≤50/channel)
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

	// ── member view (channel_members where user_id = caller) ──
	LastReadSeq   int64  `gorm:"column:last_read_seq"   json:"last_read_seq"`
	PhantomCount  int64  `gorm:"column:phantom_count"   json:"phantom_count"`
	PhantomAtRead int64  `gorm:"column:phantom_at_read" json:"phantom_at_read"`
	NotifyPref    int16  `gorm:"column:notify_pref"     json:"notify_pref"`
	IsTop         bool   `gorm:"column:is_top"          json:"is_top"`
	NickName      string `gorm:"column:nick_name"       json:"nick_name"`

	// ── channel-wide cursor + top msg id ──
	TopMessageID  string `gorm:"column:top_message_id"  json:"top_message_id"`
	ChannelMaxSeq int64  `gorm:"column:channel_max_seq" json:"channel_max_seq"`

	// ── FIFO slices (filled in Go after Q2 + Q3, NOT scanned from Q1) ──
	UrgentInChannel  []UrgentItem  `gorm:"-" json:"urgent_in_channel"`
	MentionInChannel []MentionItem `gorm:"-" json:"mention_in_channel"`
}

// UrgentItem is a single entry of ChannelWithPreview.UrgentInChannel.
// SenderID = the urger (whoever called POST /api/messages/urgent).
// Content is the raw message text (UI may truncate for the banner).
type UrgentItem struct {
	MsgID     string    `gorm:"column:msg_id"     json:"msg_id"`
	ChannelID string    `gorm:"column:channel_id" json:"-"` // scan key, not exposed
	Seq       int64     `gorm:"column:seq"        json:"seq"`
	SenderID  string    `gorm:"column:sender_id"  json:"sender_id"`
	Content   string    `gorm:"column:content"    json:"content"`
	CreatedAt time.Time `gorm:"column:created_at" json:"created_at"`
}

// MentionItem is a single entry of ChannelWithPreview.MentionInChannel.
// MentionAll is the cached check "did the source message's mention_list
// contain 'all'?" so the client doesn't have to inspect the array itself.
type MentionItem struct {
	MsgID      string         `gorm:"column:msg_id"       json:"msg_id"`
	ChannelID  string         `gorm:"column:channel_id"   json:"-"` // scan key, not exposed
	Seq        int64          `gorm:"column:seq"          json:"seq"`
	SenderID   string         `gorm:"column:sender_id"    json:"sender_id"`
	MentionAll bool           `gorm:"column:mention_all"  json:"mention_all"`
	CreatedAt  time.Time      `gorm:"column:created_at"   json:"created_at"`
	// raw mention_list kept for debugging; not part of the public contract.
	MentionList pq.StringArray `gorm:"column:mention_list" json:"-"`
}

// ChannelRepo manages channels and their members.
//
// IncrementSeq and IncrementPhantomCount accept an optional *gorm.DB (nil ⇒
// the repo's own connection). Pass a transaction to compose them inside a
// MessageRepo write.
//
// C018: IncrementSeq + NextMessageSeq both back onto the per-channel
// `channel_msg_seq_<sanitized-id>` PG sequence object. The old row-lock
// `UPDATE channels SET seq=seq+1 RETURNING` form is retired — the public
// signature stays the same so message.AllocSeqAndInsert needs no change.
type ChannelRepo interface {
	Create(ctx context.Context, ch *Channel) error
	// CreateTx is the tx-aware variant of Create. The INSERT runs inside the
	// caller's transaction so the channel row creation can be composed
	// atomically with sibling writes (per-channel PG sequence provisioning
	// via ChannelEventRepo.CreateChannelSequences, owner / initial member
	// AddMemberTx fan-out, anchor system messages). Falls back to the repo's
	// own connection when tx is nil so existing non-tx callers stay safe.
	// (P2-followup: CreateGroup / CreateOrGetDM / CreateTopic compose this
	// with CreateChannelSequences in one tx — see C018 §3.2.)
	CreateTx(ctx context.Context, tx *gorm.DB, ch *Channel) error
	GetByID(ctx context.Context, id string) (*Channel, error)
	Update(ctx context.Context, channelID string, name, avatarURL string) error
	IncrementSeq(ctx context.Context, tx *gorm.DB, channelID string) (int64, error)
	NextMessageSeq(ctx context.Context, tx *gorm.DB, channelID string) (int64, error)
	AddMember(ctx context.Context, channelID string, userID string, role int16) error
	RemoveMember(ctx context.Context, channelID string, userID string) error
	GetMember(ctx context.Context, channelID string, userID string) (*ChannelMember, error)
	ListMembers(ctx context.Context, channelID string) ([]ChannelMember, error)
	ListByUser(ctx context.Context, userID string) ([]Channel, error)
	MarkRead(ctx context.Context, channelID string, userID string, seq int64) error
	// MarkReadTx is the tx-aware variant of MarkRead. Composed with
	// ChannelEventRepo.AppendEvent at the service layer (MessageService.MarkRead)
	// so the channel_members UPDATE and the EventTypeReadMark channel_event
	// INSERT share one transaction (C017 §3.1). Returns the row count actually
	// touched so callers can detect "no-op" cases (non-member, already past
	// seq) and skip the event append.
	MarkReadTx(ctx context.Context, tx *gorm.DB, channelID string, userID string, seq int64) (int64, error)
	IncrementPhantomCount(ctx context.Context, tx *gorm.DB, channelID string, excludeUserIDs []string) error
	FindDM(ctx context.Context, userA, userB string) (*Channel, error)
	ListByUserWithPreview(ctx context.Context, userID string) ([]ChannelWithPreview, error)
	GetMemberChannelSeqs(ctx context.Context, userID string) (map[string]int64, error)

	// M3-A Topic (子群聊) 能力。CreateTopic 原子地创建 topic channel + 批量
	// 注册成员；ListTopics 返回 parentID 下所有 topic（按 id 排序）。
	CreateTopic(ctx context.Context, params CreateTopicParams) (*Channel, error)
	// CreateTopicTx 是 CreateTopic 的 tx-aware 形态：topic channel + 初始成员
	// 全部用 caller 提供的 tx 写入，让 service 层把 CreateChannelSequences
	// 也挂在同一个 tx 内（C018 §3.2）。topic 创建后第一条消息走
	// `nextval('"channel_msg_seq_<id>"')`，sequence 对象必须在 channel 行
	// commit 同时存在，否则首条消息 INSERT 失败
	// `relation "channel_msg_seq_<uuid>" does not exist`。
	// tx 为 nil 时退化为 CreateTopic 的现有非事务行为（兼容老 caller）。
	CreateTopicTx(ctx context.Context, tx *gorm.DB, params CreateTopicParams) (*Channel, error)
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

	// SetMemberRoleTx sets channel_members.role inside the caller's tx.
	// Returns ErrNotFound when the (channel_id, user_id) row is missing. Used
	// by TransferOwner to swap (old owner → member, new owner → owner) atomically
	// alongside the channels.creator_id update + system messages. (C013)
	SetMemberRoleTx(ctx context.Context, tx *gorm.DB, channelID string, userID string, role int16) error

	// SetCreatorTx flips channels.creator_id inside the caller's tx. The owner
	// concept is materialised as both channel_members.role=Owner AND
	// channels.creator_id; TransferOwner keeps both in sync. ErrNotFound when
	// channelID is missing. (C013)
	SetCreatorTx(ctx context.Context, tx *gorm.DB, channelID string, newCreatorID string) error
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
	return r.CreateTx(ctx, nil, ch)
}

// CreateTx implements ChannelRepo.CreateTx — INSERT against the caller's tx
// (or the repo's own connection when tx is nil). Pure delegate to GORM's
// Create with no extra normalisation; see the interface doc for why callers
// thread this inside WithinTx.
func (r *gormChannelRepo) CreateTx(ctx context.Context, tx *gorm.DB, ch *Channel) error {
	if err := r.dbOr(ctx, tx).Create(ch).Error; err != nil {
		return fmt.Errorf("create channel: %w", err)
	}
	return nil
}

func (r *gormChannelRepo) GetByID(ctx context.Context, id string) (*Channel, error) {
	var ch Channel
	if err := r.db.WithContext(ctx).First(&ch, "id = ?", id).Error; err != nil {
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

// IncrementSeq atomically bumps the channel's message seq and returns the
// new value. Kept as a name-stable wrapper around NextMessageSeq so the
// many existing callers (MessageRepo.AllocSeqAndInsert and friends) don't
// need to change.
//
// If tx is nil, runs against the repo's own connection. C018: see
// NextMessageSeq for the underlying PG sequence semantics.
func (r *gormChannelRepo) IncrementSeq(ctx context.Context, tx *gorm.DB, channelID string) (int64, error) {
	return r.NextMessageSeq(ctx, tx, channelID)
}

// NextMessageSeq allocates the next message seq for channelID via the
// per-channel PG sequence `channel_msg_seq_<sanitisedChannelID>`. Single
// SELECT nextval(...) on a CACHE 50 sequence sustains 10k+ TPS per channel
// (C018 §3), replacing the row-lock `UPDATE channels SET seq=seq+1
// RETURNING` form that capped throughput at ~500 TPS.
//
// Identifier safety: PG cannot parameterise an identifier, so the seq
// name is built via sanitizeID (defined in channel_event.go) which strips
// every character outside [A-Za-z0-9_-]. An empty sanitised id is a
// caller bug; we error out rather than fall through to a `nextval('')`.
//
// Transactionality caveat: sequence increments are NOT rolled back when
// the surrounding tx aborts (PG sequences are intentionally
// non-transactional to avoid lock contention). The result is occasional
// gaps in messages.seq — sync semantics tolerate gaps because cursor
// comparisons use `> last_seq` rather than `= last_seq + 1`.
//
// 2026-05-18 (E2E fix): also UPDATE channels.seq = GREATEST(seq, new_seq)
// in the same tx so legacy readers (ChannelService.MarkRead returning
// ch.Seq, ListByUserWithPreview unread_count using c.seq) stay correct.
// Pre-fix: channels.seq was a stale column (last bumped pre-C018), causing
// MarkRead → 0 and unread_count always 0 in production. Cost: 1 extra
// UPDATE per Send (negligible vs message INSERT). The GREATEST guard makes
// the write idempotent across concurrent Send paths.
func (r *gormChannelRepo) NextMessageSeq(ctx context.Context, tx *gorm.DB, channelID string) (int64, error) {
	safe := sanitizeID(channelID)
	if safe == "" {
		return 0, fmt.Errorf("next message seq: channelID sanitises to empty")
	}
	seqName := "channel_msg_seq_" + safe
	var seq int64
	// Double-quote inside the single-quoted text literal — UUID-shaped
	// channel ids carry hyphens which PG would otherwise parse as the
	// subtraction operator during the text→regclass implicit cast.
	err := r.dbOr(ctx, tx).Raw(
		fmt.Sprintf(`SELECT nextval('"%s"')`, seqName),
	).Scan(&seq).Error
	if err != nil {
		return 0, fmt.Errorf("nextval msg: %w", err)
	}
	// Mirror to channels.seq so legacy readers stay correct. GREATEST
	// guards against concurrent Send paths that may advance the sequence
	// out of order (PG sequences are non-transactional).
	if err := r.dbOr(ctx, tx).Exec(
		`UPDATE channels SET seq = GREATEST(seq, ?) WHERE id = ?`,
		seq, channelID,
	).Error; err != nil {
		return 0, fmt.Errorf("mirror channels.seq: %w", err)
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

// SetMemberRoleTx implements ChannelRepo.SetMemberRoleTx — updates
// channel_members.role inside the caller's transaction. ErrNotFound when the
// (channel_id, user_id) row is missing. (C013)
func (r *gormChannelRepo) SetMemberRoleTx(ctx context.Context, tx *gorm.DB, channelID string, userID string, role int16) error {
	res := r.dbOr(ctx, tx).Model(&ChannelMember{}).
		Where("channel_id = ? AND user_id = ?", channelID, userID).
		Update("role", role)
	if res.Error != nil {
		return fmt.Errorf("set member role: %w", res.Error)
	}
	if res.RowsAffected == 0 {
		return ErrNotFound
	}
	return nil
}

// SetCreatorTx implements ChannelRepo.SetCreatorTx — flips channels.creator_id
// inside the caller's transaction. updated_at gets bumped via the row trigger
// installed in migration 001. ErrNotFound when channelID is missing. (C013)
func (r *gormChannelRepo) SetCreatorTx(ctx context.Context, tx *gorm.DB, channelID string, newCreatorID string) error {
	res := r.dbOr(ctx, tx).Model(&Channel{}).
		Where("id = ?", channelID).
		Updates(map[string]any{
			"creator_id": newCreatorID,
			"updated_at": gorm.Expr("now()"),
		})
	if res.Error != nil {
		return fmt.Errorf("set creator: %w", res.Error)
	}
	if res.RowsAffected == 0 {
		return ErrNotFound
	}
	return nil
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
	metrics().ChannelMembersCount.Record(ctx, int64(len(members)))
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

// MarkReadTx implements ChannelRepo.MarkReadTx. Same UPDATE as MarkRead but
// runs inside the caller's tx and returns rows-affected so callers can
// short-circuit the channel_event append when the row didn't move.
//
// Composition site: service.MessageService.MarkRead — see C017 §3.2 for the
// rationale (channel_members UPDATE + EventTypeReadMark INSERT must be
// co-transactional or other devices may see a phantom read cursor that
// doesn't actually move the server-side read position).
func (r *gormChannelRepo) MarkReadTx(ctx context.Context, tx *gorm.DB, channelID string, userID string, seq int64) (int64, error) {
	res := r.dbOr(ctx, tx).Exec(
		`UPDATE channel_members
		 SET last_read_seq = ?, phantom_at_read = phantom_count
		 WHERE user_id = ? AND channel_id = ?`,
		seq, userID, channelID,
	)
	if res.Error != nil {
		return 0, fmt.Errorf("mark read tx: %w", res.Error)
	}
	return res.RowsAffected, nil
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

// ListByUserWithPreview returns channels for userID enriched with the caller's
// full member view (read cursor, mute/top prefs, nickname, phantom counters),
// the channel-wide top message preview + top_message_id + channel_max_seq,
// and per-channel FIFO slices for unconfirmed urgent + @-caller mention
// messages.
//
// Implemented as 3 round-trips (channels+member view+top-msg / urgent batch /
// mention batch) to keep the SQL readable. Urgent + mention slices use
// ROW_NUMBER() PARTITION BY channel_id to cap at 50 entries per channel
// without N+1.
//
// M4: For DM channels, peer_user_id is the OTHER member's mm UserID (the
// caller resolves the display name from cses Redis). Group channels keep
// their own name and peer_user_id is empty.
//
// C007: client expects this single response to be sufficient for both cold
// start and reconnect bootstrap — see harness card.
func (r *gormChannelRepo) ListByUserWithPreview(ctx context.Context, userID string) ([]ChannelWithPreview, error) {
	db := r.db.WithContext(ctx)

	// ── Q1: channels + member view + top msg preview ─────────────────────
	var result []ChannelWithPreview
	err := db.Raw(
		`SELECT
		    c.id, c.type, c.name, c.avatar_url, c.seq, c.creator_id, c.team_id,
		    c.notice, c.purpose, c.picture_url,
		    c.picture::text AS picture, c.picture_type,
		    c.props::text  AS props,
		    c.orient, c.permission, c.root_id, c.root_message_id,
		    c.deleted_at,
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
		    )                                               AS unread_count,
		    cm.last_read_seq                                AS last_read_seq,
		    cm.phantom_count                                AS phantom_count,
		    cm.phantom_at_read                              AS phantom_at_read,
		    cm.notify_pref                                  AS notify_pref,
		    cm.is_top                                       AS is_top,
		    cm.nick_name                                    AS nick_name,
		    COALESCE(m.id, '')                              AS top_message_id,
		    c.seq                                           AS channel_max_seq
		 FROM channels c
		 JOIN channel_members cm ON cm.channel_id = c.id AND cm.user_id = ?
		 LEFT JOIN LATERAL (
		     SELECT id, content, created_at
		     FROM messages
		     WHERE channel_id = c.id AND deleted = FALSE
		     ORDER BY seq DESC
		     LIMIT 1
		 ) m ON true
		 ORDER BY COALESCE(m.created_at, c.created_at) DESC`,
		userID, userID,
	).Scan(&result).Error
	if err != nil {
		return nil, fmt.Errorf("list by user with preview Q1: %w", err)
	}
	if len(result) == 0 {
		return result, nil
	}

	// ── Q2: urgent slice — unconfirmed urgent msgs across the caller's
	//        channels, capped at 50 per channel (ASC by created_at).
	var urgents []UrgentItem
	err = db.Raw(
		`SELECT msg_id, channel_id, seq, sender_id, content, created_at FROM (
		    SELECT
		        m.id          AS msg_id,
		        m.channel_id  AS channel_id,
		        m.seq         AS seq,
		        m.sender_id   AS sender_id,
		        m.content     AS content,
		        m.created_at  AS created_at,
		        ROW_NUMBER() OVER (PARTITION BY m.channel_id ORDER BY m.created_at ASC) AS rn
		    FROM messages m
		    JOIN channel_members cm ON cm.channel_id = m.channel_id AND cm.user_id = ?
		    WHERE m.is_urgent = TRUE
		      AND m.deleted   = FALSE
		      AND NOT EXISTS (
		          SELECT 1 FROM urgent_confirmations uc
		          WHERE uc.message_id = m.id AND uc.user_id = ?
		      )
		 ) sub WHERE rn <= 50`,
		userID, userID,
	).Scan(&urgents).Error
	if err != nil {
		return nil, fmt.Errorf("list by user with preview Q2 urgent: %w", err)
	}

	// ── Q3: mention slice — msgs whose mention_list overlaps {caller, "all"},
	//        capped at 50 per channel (DESC by created_at).
	var mentions []MentionItem
	err = db.Raw(
		`SELECT msg_id, channel_id, seq, sender_id, mention_list, mention_all, created_at FROM (
		    SELECT
		        m.id            AS msg_id,
		        m.channel_id    AS channel_id,
		        m.seq           AS seq,
		        m.sender_id     AS sender_id,
		        m.mention_list  AS mention_list,
		        (m.mention_list @> ARRAY['all']::text[]) AS mention_all,
		        m.created_at    AS created_at,
		        ROW_NUMBER() OVER (PARTITION BY m.channel_id ORDER BY m.created_at DESC) AS rn
		    FROM messages m
		    JOIN channel_members cm ON cm.channel_id = m.channel_id AND cm.user_id = ?
		    WHERE m.deleted     = FALSE
		      AND m.mention_list IS NOT NULL
		      AND m.mention_list && ARRAY[?, 'all']::text[]
		 ) sub WHERE rn <= 50`,
		userID, userID,
	).Scan(&mentions).Error
	if err != nil {
		return nil, fmt.Errorf("list by user with preview Q3 mention: %w", err)
	}

	// ── Stitch urgent + mention slices back onto their channel rows ──
	idx := make(map[string]int, len(result))
	for i := range result {
		idx[result[i].ID] = i
		result[i].UrgentInChannel = []UrgentItem{}
		result[i].MentionInChannel = []MentionItem{}
	}
	for _, u := range urgents {
		if i, ok := idx[u.ChannelID]; ok {
			result[i].UrgentInChannel = append(result[i].UrgentInChannel, u)
		}
	}
	for _, m := range mentions {
		if i, ok := idx[m.ChannelID]; ok {
			result[i].MentionInChannel = append(result[i].MentionInChannel, m)
		}
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

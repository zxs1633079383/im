// Package repo — channel_event append-only event log + per-channel PG sequence
// allocation, per harness C017 / C018 / C019.
//
// The channel_event table is the single source of truth for the "what changed
// in this channel" timeline that powers offline sync (POST /api/sync). Every
// mutation that affects message existence / ordering / read position /
// reaction / pin / urgent / membership MUST `INSERT channel_event` in the
// SAME transaction as the underlying business row mutation; otherwise the
// client's incremental sync misses the change (C017 §3.1).
//
// Per-channel sequences (`channel_msg_seq_<id>` for `messages.seq` and
// `channel_event_seq_<id>` for `channel_event.event_seq`) replace the
// row-lock UPDATE … RETURNING pattern (C018 §3). The names are sanitised via
// sanitizeID to keep SQL injection out of the dynamic identifier; the seq
// metadata is tracked in `channel_sequence_meta` so ops can audit / drop
// them.
package repo

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"time"

	"gorm.io/gorm"
)

// EventType enumerates the event kinds appended to channel_event. The int16
// cardinality is bounded on purpose — handlers must switch on the typed
// constants below (C017 §3.3).
type EventType int16

// EventType constants — keep in lock-step with cses-client
// handlers_v2/sync.rs dispatch table. Renumbering is a wire break.
const (
	EventTypeNew      EventType = 1 // 新消息
	EventTypeEdit     EventType = 2 // 编辑
	EventTypeDelete   EventType = 3 // 撤回
	EventTypeReaction EventType = 4 // 反应（预留）
	EventTypePin      EventType = 5 // 钉选（预留）
	EventTypeReadMark EventType = 6 // 已读推进 echo
	EventTypeMember   EventType = 7 // 成员变化
)

// ChannelEvent maps the channel_event table — append-only per-channel event
// log. PRIMARY KEY (channel_id, event_seq); hash-partitioned by channel_id
// across 16 physical tables (see migration 024).
//
// MsgID is nullable so reaction / read_mark / member events can refer to
// no specific message. Payload is a raw JSONB blob — event-type-specific
// extra fields (reaction emoji, member event kind, etc.). Stored as []byte
// to avoid pulling in datatypes.JSON; callers serialise/deserialise with
// json.Marshal / json.Unmarshal on their own struct shape.
type ChannelEvent struct {
	ChannelID string    `gorm:"column:channel_id;type:text;primaryKey"           json:"channel_id"`
	EventSeq  int64     `gorm:"column:event_seq;primaryKey"                      json:"event_seq"`
	EventType EventType `gorm:"column:event_type;type:smallint;not null"         json:"event_type"`
	MsgID     *string   `gorm:"column:msg_id;type:text"                          json:"msg_id,omitempty"`
	ActorID   string    `gorm:"column:actor_id;type:text;not null"               json:"actor_id"`
	Payload   []byte    `gorm:"column:payload;type:jsonb"                        json:"payload,omitempty"`
	CreatedAt int64     `gorm:"column:created_at;not null"                       json:"created_at"`
}

// TableName pins the GORM-derived table name to the migration. Note the
// table is partitioned — INSERT/SELECT against `channel_event` is the only
// supported path; talking to `channel_event_pXX` directly is not portable.
func (ChannelEvent) TableName() string { return "channel_event" }

// MarshalJSON ensures the Payload field is emitted as raw JSON, not as a
// base64-encoded string.
//
// 2026-05-18 (root-cause fix): Go's encoding/json default behaviour for
// `[]byte` fields is to base64-encode them as a JSON string — fine for opaque
// binary blobs but breaks our jsonb wire contract, since cses-client's
// dispatch_sync_delta (handlers_v2/sync.rs) deserialises Payload directly
// as `MemberEventPayload` / `ReadMarkPayload` typed structs. Without this
// override the client sees `payload: "eyJtZW1iZXJzIjogW3siLi4uIn0="` (base64
// string) and fails with `invalid type: string, expected struct ...`.
//
// The alias-embedding trick (type Alias ChannelEvent; struct { Alias; Payload
// json.RawMessage }) shadows the inherited []byte field with a json.RawMessage
// at the marshal layer, which writes the bytes verbatim. Storage (PG jsonb
// column) and GORM unmarshal stay on []byte so no schema or read-path change
// is required.
func (e ChannelEvent) MarshalJSON() ([]byte, error) {
	type Alias ChannelEvent
	var raw json.RawMessage
	if len(e.Payload) > 0 {
		raw = json.RawMessage(e.Payload)
	}
	return json.Marshal(struct {
		Alias
		Payload json.RawMessage `json:"payload,omitempty"`
	}{
		Alias:   Alias(e),
		Payload: raw,
	})
}

// ChannelSequenceMeta maps the channel_sequence_meta table — bookkeeping
// for the dynamic per-channel PG sequences created by CreateChannelSequences.
// Used by ops tooling to enumerate sequences for backup / drop after a
// channel is hard-deleted.
type ChannelSequenceMeta struct {
	ChannelID    string `gorm:"column:channel_id;type:text;primaryKey" json:"channel_id"`
	MsgSeqName   string `gorm:"column:msg_seq_name;not null"           json:"msg_seq_name"`
	EventSeqName string `gorm:"column:event_seq_name;not null"         json:"event_seq_name"`
	CreatedAt    int64  `gorm:"column:created_at;not null"             json:"created_at"`
}

// TableName pins the GORM-derived table name to the migration.
func (ChannelSequenceMeta) TableName() string { return "channel_sequence_meta" }

// ChannelEventRepo is the persistence interface for the channel_event log
// and per-channel sequences. All write methods accept *gorm.DB so callers
// compose them inside the same tx that mutates the business row.
//
// Method contracts:
//   - AppendEvent reuses the caller's tx; nil tx → ErrTxRequired (the append
//     MUST be co-transactional with the business mutation; see C017 §3.1).
//   - NextEventSeq allocates a per-channel monotonic event_seq via PG
//     sequence (C018). Caller-supplied tx is reused; nil tx falls back to
//     the repo's own connection.
//   - FetchAfter is the read path used by the sync algorithm (P4). PK index
//     `(channel_id, event_seq)` covers the WHERE + ORDER BY without sort.
//   - GetMemberChannelEventSeqs returns the current per-channel
//     channel_event_seq value for every channel the user belongs to. Used by
//     reconnect bootstrap to seed the client's per-channel sync cursor.
//   - CreateChannelSequences is called by ChannelService.Create — it creates
//     `channel_msg_seq_<id>` + `channel_event_seq_<id>` PG sequences and
//     inserts the meta row. Idempotent (IF NOT EXISTS).
type ChannelEventRepo interface {
	AppendEvent(ctx context.Context, tx *gorm.DB, event *ChannelEvent) error
	NextEventSeq(ctx context.Context, tx *gorm.DB, channelID string) (int64, error)
	FetchAfter(ctx context.Context, channelID string, afterEventSeq int64, limit int) ([]ChannelEvent, error)
	GetMemberChannelEventSeqs(ctx context.Context, userID string) (map[string]int64, error)
	CreateChannelSequences(ctx context.Context, tx *gorm.DB, channelID string) error
}

// ErrTxRequired is returned by AppendEvent when the caller did not provide
// a transaction. channel_event INSERTs MUST be co-transactional with the
// business mutation they describe (C017 §3.1) — silently opening a fresh
// connection would let the mutation commit while the event INSERT failed,
// producing an "orphan mutation" that sync can never replay.
var ErrTxRequired = errors.New("channel_event: tx required")

type gormChannelEventRepo struct{ db *gorm.DB }

// NewChannelEventRepo returns a GORM-backed ChannelEventRepo.
func NewChannelEventRepo(db *gorm.DB) ChannelEventRepo {
	return &gormChannelEventRepo{db: db}
}

// dbOr returns tx if non-nil, otherwise the repo's own *gorm.DB. Both are
// chained with WithContext so the caller doesn't have to.
func (r *gormChannelEventRepo) dbOr(ctx context.Context, tx *gorm.DB) *gorm.DB {
	if tx != nil {
		return tx.WithContext(ctx)
	}
	return r.db.WithContext(ctx)
}

// AppendEvent persists a single ChannelEvent inside the caller's tx.
//
// Defaults applied for convenience:
//   - event.CreatedAt is set to time.Now().UnixMilli() when zero.
//
// Returns ErrTxRequired when tx is nil — the channel_event INSERT MUST be
// co-transactional with the business mutation (C017 §3.1).
func (r *gormChannelEventRepo) AppendEvent(ctx context.Context, tx *gorm.DB, event *ChannelEvent) error {
	if tx == nil {
		return ErrTxRequired
	}
	if event == nil {
		return fmt.Errorf("channel_event: event is nil")
	}
	if event.CreatedAt == 0 {
		event.CreatedAt = time.Now().UnixMilli()
	}
	if err := tx.WithContext(ctx).Create(event).Error; err != nil {
		return fmt.Errorf("append channel_event: %w", err)
	}
	return nil
}

// NextEventSeq allocates the next per-channel event_seq via PG sequence
// `channel_event_seq_<sanitisedChannelID>`. Single SELECT nextval(...) on a
// pre-cached sequence sustains 10k+ TPS per channel (C018 §3).
//
// Caller-supplied tx is reused (so the seq advance is rolled back if the
// surrounding transaction aborts); nil tx falls back to the repo's own
// connection — note however that **sequence increments are not
// transactional**: a rolled-back tx still consumes the seq number, leaving
// a gap. This is acceptable because gaps in event_seq only matter for the
// "what's the max" cursor, not "is every number contiguous"; the sync
// algorithm reads `> after_event_seq` not `= after_event_seq + 1`.
func (r *gormChannelEventRepo) NextEventSeq(ctx context.Context, tx *gorm.DB, channelID string) (int64, error) {
	safe := sanitizeID(channelID)
	if safe == "" {
		return 0, fmt.Errorf("nextval event: channelID sanitises to empty")
	}
	seqName := "channel_event_seq_" + safe
	var seq int64
	// PG identifiers cannot be parameterised; sanitizeID already restricts
	// the name charset to [A-Za-z0-9_-] so direct interpolation is safe.
	// The seq name must be double-quoted *inside* the single-quoted text
	// literal because UUID-shaped channel ids carry hyphens, which PG
	// otherwise parses as the subtraction operator during the implicit
	// text→regclass cast. CREATE SEQUENCE uses the same quoting (see
	// CreateChannelSequences below).
	err := r.dbOr(ctx, tx).Raw(
		fmt.Sprintf(`SELECT nextval('"%s"')`, seqName),
	).Scan(&seq).Error
	if err != nil {
		return 0, fmt.Errorf("nextval event: %w", err)
	}
	return seq, nil
}

// FetchAfter returns the next ≤limit events with event_seq > afterEventSeq
// for channelID, ascending by event_seq. The PK index (channel_id,
// event_seq) covers the WHERE + ORDER BY so no sort node is emitted.
//
// Used by the P4 sync algorithm to walk the per-channel timeline from a
// client-supplied cursor. limit ≤ 0 is treated as "no limit" — callers
// should always pass a positive bound (the sync service uses 200).
func (r *gormChannelEventRepo) FetchAfter(
	ctx context.Context, channelID string, afterEventSeq int64, limit int,
) ([]ChannelEvent, error) {
	q := r.db.WithContext(ctx).
		Where("channel_id = ? AND event_seq > ?", channelID, afterEventSeq).
		Order("event_seq ASC")
	if limit > 0 {
		q = q.Limit(limit)
	}
	var out []ChannelEvent
	if err := q.Find(&out).Error; err != nil {
		return nil, fmt.Errorf("fetch channel_event after: %w", err)
	}
	return out, nil
}

// GetMemberChannelEventSeqs returns {channel_id: max(event_seq)} for every
// channel the user belongs to. Channels with no events yet are reported
// with seq=0 so the caller can still seed a per-channel cursor.
//
// Implementation: drive from channel_members.user_id; the correlated MAX
// subquery on channel_event is cheap because the hash-partitioned PK
// supports a descending index seek inside the single partition belonging
// to that channel_id.
func (r *gormChannelEventRepo) GetMemberChannelEventSeqs(
	ctx context.Context, userID string,
) (map[string]int64, error) {
	type row struct {
		ChannelID string `gorm:"column:channel_id"`
		EventSeq  int64  `gorm:"column:event_seq"`
	}
	var rows []row
	err := r.db.WithContext(ctx).Raw(
		`SELECT cm.channel_id AS channel_id,
		        COALESCE(
		            (SELECT MAX(event_seq) FROM channel_event WHERE channel_id = cm.channel_id),
		            0
		        ) AS event_seq
		 FROM channel_members cm
		 WHERE cm.user_id = ?`,
		userID,
	).Scan(&rows).Error
	if err != nil {
		return nil, fmt.Errorf("get member channel_event seqs: %w", err)
	}
	out := make(map[string]int64, len(rows))
	for _, r := range rows {
		out[r.ChannelID] = r.EventSeq
	}
	return out, nil
}

// CreateChannelSequences provisions the dynamic PG sequences for a freshly-
// created channel and records them in channel_sequence_meta. Called by
// ChannelService.Create inside the channel-creation transaction so partial
// failure rolls the whole thing back.
//
// Idempotent: both `CREATE SEQUENCE IF NOT EXISTS` and the meta upsert
// tolerate repeated invocation (the meta row uses ON CONFLICT DO NOTHING).
//
// SQL identifiers cannot be parameterised, so the sequence name is built
// from sanitizeID(channelID) — the regex strips everything outside
// [A-Za-z0-9_-]. Any other character is dropped, not escaped; the meta row
// records the actual name created so ops can locate it later.
func (r *gormChannelEventRepo) CreateChannelSequences(
	ctx context.Context, tx *gorm.DB, channelID string,
) error {
	safe := sanitizeID(channelID)
	if safe == "" {
		return fmt.Errorf("channel_event: channelID sanitises to empty")
	}
	msgSeq := "channel_msg_seq_" + safe
	eventSeq := "channel_event_seq_" + safe

	db := r.dbOr(ctx, tx)
	// %s injection-safe — safe is already sanitised; identifiers are not
	// parameterisable in PG, so format-then-Exec is the only option. We
	// double-quote the identifier so UUID-shaped channel ids (which carry
	// hyphens, otherwise parsed as the subtraction operator) survive.
	if err := db.Exec(
		fmt.Sprintf(`CREATE SEQUENCE IF NOT EXISTS "%s" START 1 CACHE 50`, msgSeq),
	).Error; err != nil {
		return fmt.Errorf("create msg sequence: %w", err)
	}
	if err := db.Exec(
		fmt.Sprintf(`CREATE SEQUENCE IF NOT EXISTS "%s" START 1 CACHE 100`, eventSeq),
	).Error; err != nil {
		return fmt.Errorf("create event sequence: %w", err)
	}
	// Insert meta row; ON CONFLICT DO NOTHING makes the whole call idempotent.
	if err := db.Exec(
		`INSERT INTO channel_sequence_meta (channel_id, msg_seq_name, event_seq_name, created_at)
		 VALUES (?, ?, ?, ?)
		 ON CONFLICT (channel_id) DO NOTHING`,
		channelID, msgSeq, eventSeq, time.Now().UnixMilli(),
	).Error; err != nil {
		return fmt.Errorf("insert sequence meta: %w", err)
	}
	return nil
}

// sanitizeIDRe matches every character NOT in the allowlist [A-Za-z0-9_-].
// Pre-compiled at package load so the per-call cost is just a regex scan.
var sanitizeIDRe = regexp.MustCompile(`[^A-Za-z0-9_-]`)

// sanitizeID strips every character outside [A-Za-z0-9_-] from id and
// returns the result. Used to build dynamic PG sequence names from
// caller-supplied channel IDs without exposing the identifier to SQL
// injection (PG identifiers cannot be parameterised).
//
// Empty result is a caller error — callers should reject it (see
// CreateChannelSequences / NextEventSeq / NextMessageSeq).
func sanitizeID(id string) string {
	return sanitizeIDRe.ReplaceAllString(id, "")
}

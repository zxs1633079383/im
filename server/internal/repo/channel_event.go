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
	"errors"

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

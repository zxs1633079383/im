package repo

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/lib/pq"
	"gorm.io/gorm"
)

// SysTypeKey is the mandatory discriminator key on every system-message props
// payload. Callers use the constants below so sys_type strings never drift.
const SysTypeKey = "sys_type"

// System-message sys_type values. Kept as untyped string constants so callers
// drop them straight into props maps without conversion. v0.7.3 adds three
// flavours for cses-client cutover gaps #1/#4/#5:
//   - channel_closed       owner 解散群聊（gap #1+#3）
//   - member_nickname      per-channel 昵称变更（gap #5）
//   - 现有 member_joined / member_removed / member_left 已覆盖 gap #4
//     之外，新增 ChannelMemberUpdatedPayload WS 把完整 channel snapshot 推全员。
const (
	SysTypeChannelCreated    = "channel_created"
	SysTypeChannelUpdated    = "channel_updated"
	SysTypeChannelClosed     = "channel_closed"
	SysTypeMemberJoined      = "member_joined"
	SysTypeMemberRemoved     = "member_removed"
	SysTypeMemberLeft        = "member_left"
	SysTypeMemberNickname    = "member_nickname"
	// SysTypeOwnerTransferred is the sys_type emitted by ChannelService.TransferOwner
	// (C013). props carries actor_id (old owner) + target_id (new owner). When
	// the transfer is accompanied by AlsoLeave=true a second SysTypeMemberLeft
	// row is written in the same transaction after the role swap.
	SysTypeOwnerTransferred  = "owner_transferred"
)

// ErrInvalidSystemProps is returned by PostSystemMessage when the props map
// lacks the required "sys_type" string key.
var ErrInvalidSystemProps = errors.New("system message props must contain non-empty sys_type")

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
//
// AllocSeqAndInsert is the low-level primitive: it combines
// UPDATE channels SET seq=seq+1 RETURNING seq with INSERT messages inside the
// same transaction so seq monotonicity holds even under concurrent writers.
// It exposes the optional external-tx hook so Service layer callers can
// compose it inside a bigger transaction — see docs/BACKEND.md §4.1.
type MessageRepo interface {
	Send(ctx context.Context, msg *Message) error
	AllocSeqAndInsert(ctx context.Context, tx *gorm.DB, msg *Message) (int64, error)
	// PostSystemMessage inserts a msg_type=System message whose body is a typed
	// JSON props payload (stored in messages.props). Used by channel-level
	// events (member joined/removed, channel renamed, ...) so clients receive
	// them via the normal push_msg + /api/sync pipe instead of bespoke events.
	//
	// props MUST contain a non-empty "sys_type" string key; otherwise
	// ErrInvalidSystemProps is returned. tx != nil reuses the caller's
	// transaction (required when combining with a sibling mutation such as
	// RemoveMember to keep them atomic).
	PostSystemMessage(ctx context.Context, tx *gorm.DB, channelID string, senderID string, teamID *string, props map[string]any) (*Message, error)
	UpdateContent(ctx context.Context, msgID string, callerID string, content string) (*Message, error)
	// UpdateMessageProps overwrites messages.props with the given JSON string
	// and bumps updated_at. See message_props.go for behaviour and concurrency
	// notes.
	UpdateMessageProps(ctx context.Context, msgID string, newProps string) (*Message, error)
	// GetReadStatsBatch returns per-message read summaries for callers who
	// need to render "X read / Y unread" UI on multiple messages at once.
	// See read_stats.go for the SQL shape and the truncation policy.
	GetReadStatsBatch(ctx context.Context, callerID string, msgIDs []string) ([]ReadStat, error)
	SoftDelete(ctx context.Context, msgID string, callerID string) (*Message, error)
	GetByID(ctx context.Context, id string) (*Message, error)
	// GetByIDsForUser bulk-fetches messages by primary key with the same
	// visibility filter FetchForUser applies (visible_to IS NULL OR userID is
	// in visible_to OR userID is the sender). Used by Phase P4 sync v2 to
	// hydrate the `messages` snapshot map referenced by `channel_event` rows
	// (C019 §3.2 algorithm step 3). Returns an empty slice (not nil) when
	// every id is filtered out; preserves caller-supplied id order is NOT
	// guaranteed — callers must re-index by id (sync v2 builds a map keyed
	// by `Message.ID`).
	GetByIDsForUser(ctx context.Context, userID string, ids []string) ([]Message, error)
	FetchAfter(ctx context.Context, channelID string, afterSeq int64, limit int) ([]Message, error)
	FetchForUser(ctx context.Context, channelID string, userID string, afterSeq int64, limit int) ([]Message, error)
	FetchBefore(ctx context.Context, channelID string, userID string, beforeSeq int64, limit int) ([]Message, error)
	FetchAround(ctx context.Context, channelID string, userID string, aroundSeq int64, limit int) ([]Message, error)
	FetchAroundTimestamp(ctx context.Context, channelID string, userID string, ts time.Time, limit int) (older []Message, newer []Message, err error)
	FetchReplies(ctx context.Context, rootID string, userID string) ([]Message, error)
	// FetchRepliesPage is the page-aware sibling of FetchReplies — used by
	// the cses-client reply-branch pagination (v0.7.3 gap #2). offset / limit
	// are pre-validated by the service layer; passing limit <= 0 returns
	// an empty slice.
	FetchRepliesPage(ctx context.Context, rootID string, userID string, offset, limit int) ([]Message, error)
	GetReaders(ctx context.Context, channelID string, seq int64, cursor string, limit int) (readers []string, nextCursor string, err error)
}

type gormMessageRepo struct {
	db           *gorm.DB
	channel      ChannelRepo
	channelEvent ChannelEventRepo
}

// NewMessageRepo returns a GORM-backed MessageRepo. The ChannelRepo is used
// to compose IncrementSeq + IncrementPhantomCount inside the Send transaction;
// the optional ChannelEventRepo is composed inside Send / UpdateContent /
// SoftDelete / PostSystemMessage to append a row to the channel_event
// append-only log in the SAME transaction as the underlying business
// mutation (harness C017 §3.1). Passing nil disables event append — callers
// that wire nil opt out of the offline-sync timeline entirely (only the M4
// integration harness and a handful of legacy tests still do).
func NewMessageRepo(db *gorm.DB, channel ChannelRepo, channelEvent ChannelEventRepo) MessageRepo {
	return &gormMessageRepo{db: db, channel: channel, channelEvent: channelEvent}
}

// Send runs idempotency check, seq allocation, insert, phantom_count bump,
// and channel_event append (EventTypeNew) — all in a single transaction.
// See MessageRepo.Send for semantics.
//
// Send delegates the UPDATE channels + INSERT messages atomic pair to
// AllocSeqAndInsert so there is a single primitive owning seq monotonicity.
// The channel_event row is appended in the SAME tx so a crash between the
// message INSERT and the event INSERT rolls both back; this is the C017 §3.1
// "co-transactional" guarantee that powers offline-sync correctness.
func (r *gormMessageRepo) Send(ctx context.Context, msg *Message) error {
	return r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		// 1. Idempotency: short-circuit if (channel_id, client_msg_id)
		//    already exists. Idempotent retries MUST NOT append a second
		//    channel_event row — the original Send already appended one.
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

		// 2+3. AllocSeqAndInsert does nextval seq + INSERT messages inside the
		//      passed tx — see docs/BACKEND.md §4.1.
		if _, err := r.AllocSeqAndInsert(ctx, tx, msg); err != nil {
			return err
		}

		// 4. Append EventTypeNew in the SAME tx (C017 §3.1). Failure here
		//    rolls the message INSERT back, so the client never sees a
		//    "ghost message" that sync can't replay.
		if err := r.appendMessageEvent(ctx, tx, msg.ChannelID, msg.ID, msg.SenderID, EventTypeNew); err != nil {
			return err
		}

		// 5. Directed message: bump phantom_count for every member NOT in
		//    visible_to (sender stays included so they don't see a phantom
		//    of their own message).
		if msg.VisibleTo != nil {
			visibleWithSender := append([]string(msg.VisibleTo), msg.SenderID)
			if err := r.channel.IncrementPhantomCount(ctx, tx, msg.ChannelID, visibleWithSender); err != nil {
				return fmt.Errorf("phantom: %w", err)
			}
		}
		return nil
	})
}

// appendMessageEvent is the C017 §3.2 helper used by every mutation handler
// that needs to write a single ChannelEvent row inside the caller's tx. The
// channelEvent field is optional (nil-safe) for legacy wiring; callers that
// pass nil ChannelEventRepo at construction time keep their pre-channel-event
// behaviour. msgID may be empty when the event has no associated message
// (reaction / read_mark — currently unused at the message-repo layer but the
// signature already accommodates it).
func (r *gormMessageRepo) appendMessageEvent(
	ctx context.Context, tx *gorm.DB,
	channelID string, msgID string, actorID string, eventType EventType,
) error {
	if r.channelEvent == nil {
		return nil
	}
	seq, err := r.channelEvent.NextEventSeq(ctx, tx, channelID)
	if err != nil {
		return fmt.Errorf("alloc event seq: %w", err)
	}
	evt := &ChannelEvent{
		ChannelID: channelID,
		EventSeq:  seq,
		EventType: eventType,
		ActorID:   actorID,
		CreatedAt: time.Now().UnixMilli(),
	}
	if msgID != "" {
		evt.MsgID = &msgID
	}
	return r.channelEvent.AppendEvent(ctx, tx, evt)
}

// AllocSeqAndInsert is the unique entry point for allocating the next
// per-channel seq and inserting the message. See docs/BACKEND.md §4.1.
//
// Transaction reuse:
//   - tx != nil  → reuse the caller's tx (compose with other writes)
//   - tx == nil  → open an internal transaction (standalone send path)
//
// Regardless of which path is taken, UPDATE channels SET seq = seq + 1 and
// INSERT messages share the same transaction so a crash between them rolls
// back cleanly and never produces a seq gap.
//
// Service/HTTP layers MUST NOT run their own UPDATE channels SET seq = …
// statements — CI grep will enforce this as a follow-up.
func (r *gormMessageRepo) AllocSeqAndInsert(ctx context.Context, tx *gorm.DB, msg *Message) (int64, error) {
	ctx, span := tracer.Start(ctx, "MessageRepo.AllocSeqAndInsert")
	defer span.End()

	start := time.Now()
	defer func() {
		if m := metrics(); m.AllocSeqDur != nil {
			m.AllocSeqDur.Record(ctx, float64(time.Since(start).Milliseconds()))
		}
	}()

	run := func(db *gorm.DB) error {
		// UPDATE ... RETURNING seq — atomic row-lock on channels(id).
		seq, err := r.channel.IncrementSeq(ctx, db, msg.ChannelID)
		if err != nil {
			return fmt.Errorf("alloc seq: %w", err)
		}
		msg.Seq = seq

		// Empty client_msg_id must land as SQL NULL (the column is nullable
		// and the unique index treats NULLs as distinct); Omit() skips the
		// column so Postgres applies its default.
		insert := db
		if msg.ClientMsgID == "" {
			insert = insert.Omit("ClientMsgID")
		}
		if err := insert.Create(msg).Error; err != nil {
			return fmt.Errorf("insert message: %w", err)
		}
		return nil
	}

	if tx != nil {
		if err := run(tx.WithContext(ctx)); err != nil {
			return 0, err
		}
		return msg.Seq, nil
	}
	err := r.db.WithContext(ctx).Transaction(func(newTx *gorm.DB) error { return run(newTx) })
	if err != nil {
		return 0, err
	}
	return msg.Seq, nil
}

// PostSystemMessage implements MessageRepo.PostSystemMessage.
//
// It validates props["sys_type"] is a non-empty string, marshals props to JSON
// (stored in messages.props), constructs a Message with MsgType=System,
// delegates to AllocSeqAndInsert so seq monotonicity is preserved, and
// appends a channel_event row of EventTypeMember in the SAME transaction
// (C017 §3.1) so member joined/left/kicked + channel rename/close events are
// visible to the offline-sync timeline. tx != nil reuses the caller's
// transaction; tx == nil opens a fresh transaction internally so the message
// INSERT + the event INSERT remain co-transactional.
//
// Empty content is intentional — the client renders from props["sys_type"]
// + the remaining fields.
func (r *gormMessageRepo) PostSystemMessage(
	ctx context.Context, tx *gorm.DB,
	channelID string, senderID string, teamID *string, props map[string]any,
) (*Message, error) {
	ctx, span := tracer.Start(ctx, "MessageRepo.PostSystemMessage")
	defer span.End()

	sysType, _ := props[SysTypeKey].(string)
	if sysType == "" {
		return nil, ErrInvalidSystemProps
	}
	payload, err := json.Marshal(props)
	if err != nil {
		return nil, fmt.Errorf("marshal system props: %w", err)
	}
	propsStr := string(payload)

	msg := &Message{
		ChannelID: channelID,
		SenderID:  senderID,
		TeamID:    teamID,
		MsgType:   MsgTypeSystem,
		Props:     &propsStr,
	}

	// Compose insert + event append inside one tx. When the caller already
	// supplies a tx, we reuse it directly; otherwise open a fresh one.
	insertAndEvent := func(innerTx *gorm.DB) error {
		if _, err := r.AllocSeqAndInsert(ctx, innerTx, msg); err != nil {
			return fmt.Errorf("post system message: %w", err)
		}
		return r.appendMessageEvent(ctx, innerTx, channelID, msg.ID, senderID, EventTypeMember)
	}
	if tx != nil {
		if err := insertAndEvent(tx.WithContext(ctx)); err != nil {
			return nil, err
		}
		return msg, nil
	}
	if err := r.db.WithContext(ctx).Transaction(func(newTx *gorm.DB) error {
		return insertAndEvent(newTx)
	}); err != nil {
		return nil, err
	}
	return msg, nil
}

// UpdateContent sets content + updated_at=now() for msgID when callerID is the
// sender and the message is not already soft-deleted, and appends a
// channel_event row of EventTypeEdit in the SAME transaction (C017 §3.1).
// Returns the refreshed row. Errors:
//   - ErrNotFound when the message does not exist.
//   - ErrForbidden when the caller is not the sender.
//   - ErrGone when the message is already soft-deleted.
//
// The returned *Message reflects the post-update state (including the new
// updated_at value) so callers can echo it in the WS msg_updated payload.
func (r *gormMessageRepo) UpdateContent(ctx context.Context, msgID string, callerID string, content string) (*Message, error) {
	ctx, span := tracer.Start(ctx, "MessageRepo.UpdateContent")
	defer span.End()

	existing, err := r.GetByID(ctx, msgID)
	if err != nil {
		return nil, err
	}
	if existing.SenderID != callerID {
		return nil, ErrForbidden
	}
	if existing.Deleted {
		return nil, ErrGone
	}
	now := time.Now().UTC()
	err = r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		res := tx.Model(&Message{}).
			Where("id = ? AND sender_id = ? AND deleted = FALSE", msgID, callerID).
			Updates(map[string]any{"content": content, "updated_at": now})
		if res.Error != nil {
			return fmt.Errorf("update content: %w", res.Error)
		}
		if res.RowsAffected == 0 {
			return ErrNotFound
		}
		// Same-tx channel_event append: failure rolls the CAS UPDATE back.
		return r.appendMessageEvent(ctx, tx, existing.ChannelID, msgID, callerID, EventTypeEdit)
	})
	if err != nil {
		return nil, err
	}
	existing.Content = content
	existing.UpdatedAt = &now
	return existing, nil
}

// SoftDelete sets deleted=true + deleted_at=now() for msgID when callerID is
// the sender, and appends a channel_event row of EventTypeDelete in the
// SAME transaction (C017 §3.1). Returns the refreshed row so the caller
// can fan out the msg_deleted WS event. Errors:
//   - ErrNotFound when the message does not exist.
//   - ErrForbidden when the caller is not the sender.
//   - ErrGone when the message is already soft-deleted (idempotent no-op,
//     no second channel_event row is appended).
func (r *gormMessageRepo) SoftDelete(ctx context.Context, msgID string, callerID string) (*Message, error) {
	ctx, span := tracer.Start(ctx, "MessageRepo.SoftDelete")
	defer span.End()

	existing, err := r.GetByID(ctx, msgID)
	if err != nil {
		return nil, err
	}
	if existing.SenderID != callerID {
		return nil, ErrForbidden
	}
	if existing.Deleted {
		// Idempotent: treat as success, but signal "already gone" so the
		// transport layer can skip the push fan-out. No channel_event is
		// appended because the original SoftDelete already appended one.
		return existing, ErrGone
	}
	now := time.Now().UTC()
	var raced bool
	err = r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		res := tx.Model(&Message{}).
			Where("id = ? AND sender_id = ? AND deleted = FALSE", msgID, callerID).
			Updates(map[string]any{"deleted": true, "deleted_at": now})
		if res.Error != nil {
			return fmt.Errorf("soft delete: %w", res.Error)
		}
		if res.RowsAffected == 0 {
			// Concurrent delete lost the race — equivalent to ErrGone.
			// Surface via a sentinel boolean so we can still commit the
			// (empty) tx and return ErrGone outside it.
			raced = true
			return nil
		}
		return r.appendMessageEvent(ctx, tx, existing.ChannelID, msgID, callerID, EventTypeDelete)
	})
	if err != nil {
		return nil, err
	}
	if raced {
		existing.Deleted = true
		return existing, ErrGone
	}
	existing.Deleted = true
	existing.DeletedAt = &now
	return existing, nil
}

// FetchAroundTimestamp returns messages centered on ts for channelID filtered
// by visible_to for userID. Half the limit is returned on each side (older +
// newer). Soft-deleted messages are excluded.
//
// older is ordered by seq ASC (oldest first); newer is ordered by seq ASC.
// Callers may concatenate older + newer for a chronologically ordered window.
func (r *gormMessageRepo) FetchAroundTimestamp(ctx context.Context, channelID string, userID string, ts time.Time, limit int) ([]Message, []Message, error) {
	if limit <= 0 {
		limit = 2
	}
	half := limit / 2
	if half == 0 {
		half = 1
	}

	var older []Message
	err := r.db.WithContext(ctx).Raw(
		`SELECT * FROM (
		    SELECT id, channel_id, seq, client_msg_id, sender_id, msg_type, content,
		           visible_to, reply_to, forwarded_from, props, created_at, updated_at,
		           deleted, deleted_at
		    FROM messages
		    WHERE channel_id = ? AND created_at <= ? AND deleted = FALSE
		      AND (visible_to IS NULL OR ? = ANY(visible_to) OR sender_id = ?)
		    ORDER BY created_at DESC, seq DESC
		    LIMIT ?
		 ) t ORDER BY seq`,
		channelID, ts, userID, userID, half,
	).Scan(&older).Error
	if err != nil {
		return nil, nil, fmt.Errorf("fetch around ts (older): %w", err)
	}

	var newer []Message
	err = r.db.WithContext(ctx).Raw(
		`SELECT id, channel_id, seq, client_msg_id, sender_id, msg_type, content,
		        visible_to, reply_to, forwarded_from, props, created_at, updated_at,
		        deleted, deleted_at
		 FROM messages
		 WHERE channel_id = ? AND created_at > ? AND deleted = FALSE
		   AND (visible_to IS NULL OR ? = ANY(visible_to) OR sender_id = ?)
		 ORDER BY created_at ASC, seq ASC
		 LIMIT ?`,
		channelID, ts, userID, userID, half,
	).Scan(&newer).Error
	if err != nil {
		return nil, nil, fmt.Errorf("fetch around ts (newer): %w", err)
	}
	return older, newer, nil
}

// FetchReplies returns every non-deleted reply to rootID, ordered by seq ASC.
// The caller is not membership-checked here — the service layer enforces it.
func (r *gormMessageRepo) FetchReplies(ctx context.Context, rootID string, userID string) ([]Message, error) {
	var out []Message
	err := r.db.WithContext(ctx).Raw(
		`SELECT id, channel_id, seq, client_msg_id, sender_id, msg_type, content,
		        visible_to, reply_to, forwarded_from, props, created_at, updated_at,
		        deleted, deleted_at
		 FROM messages
		 WHERE reply_to = ? AND deleted = FALSE
		   AND (visible_to IS NULL OR ? = ANY(visible_to) OR sender_id = ?)
		 ORDER BY seq ASC`,
		rootID, userID, userID,
	).Scan(&out).Error
	if err != nil {
		return nil, fmt.Errorf("fetch replies: %w", err)
	}
	return out, nil
}

// GetReaders returns the user_ids of channel members whose last_read_seq has
// advanced past the given seq. cursor is a user_id pagination anchor (0 to
// start). nextCursor is the last returned user_id (0 if the page is empty).
func (r *gormMessageRepo) GetReaders(ctx context.Context, channelID string, seq int64, cursor string, limit int) ([]string, string, error) {
	if limit <= 0 || limit > 200 {
		limit = 50
	}
	type row struct {
		UserID string `gorm:"column:user_id"`
	}
	var rows []row
	err := r.db.WithContext(ctx).Raw(
		`SELECT user_id
		 FROM channel_members
		 WHERE channel_id = ? AND last_read_seq >= ? AND user_id > ?
		 ORDER BY user_id ASC
		 LIMIT ?`,
		channelID, seq, cursor, limit,
	).Scan(&rows).Error
	if err != nil {
		return nil, "", fmt.Errorf("get readers: %w", err)
	}
	readers := make([]string, len(rows))
	for i, r := range rows {
		readers[i] = r.UserID
	}
	var next string
	if len(readers) == limit && len(readers) > 0 {
		next = readers[len(readers)-1]
	}
	return readers, next, nil
}

func (r *gormMessageRepo) GetByID(ctx context.Context, id string) (*Message, error) {
	var m Message
	if err := r.db.WithContext(ctx).First(&m, "id = ?", id).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, ErrNotFound
		}
		return nil, fmt.Errorf("get message by id: %w", err)
	}
	return &m, nil
}

// GetByIDsForUser bulk-loads messages by id with the same visibility predicate
// as FetchForUser. Empty `ids` short-circuits to an empty slice (cheap).
// Result order is dictated by Postgres — callers must re-index by id.
func (r *gormMessageRepo) GetByIDsForUser(ctx context.Context, userID string, ids []string) ([]Message, error) {
	ctx, span := tracer.Start(ctx, "MessageRepo.GetByIDsForUser")
	defer span.End()

	if len(ids) == 0 {
		return []Message{}, nil
	}
	var out []Message
	err := r.db.WithContext(ctx).Raw(
		`SELECT id, channel_id, seq, client_msg_id, sender_id, msg_type, content,
		        visible_to, reply_to, forwarded_from, props, created_at
		 FROM messages
		 WHERE id = ANY(?)
		   AND (visible_to IS NULL OR ? = ANY(visible_to) OR sender_id = ?)`,
		pq.Array(ids), userID, userID,
	).Scan(&out).Error
	if err != nil {
		return nil, fmt.Errorf("get messages by ids for user: %w", err)
	}
	return out, nil
}

func (r *gormMessageRepo) FetchAfter(ctx context.Context, channelID string, afterSeq int64, limit int) ([]Message, error) {
	ctx, span := tracer.Start(ctx, "MessageRepo.FetchAfter")
	defer span.End()

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
func (r *gormMessageRepo) FetchForUser(ctx context.Context, channelID string, userID string, afterSeq int64, limit int) ([]Message, error) {
	ctx, span := tracer.Start(ctx, "MessageRepo.FetchForUser")
	defer span.End()

	var out []Message
	err := r.db.WithContext(ctx).Raw(
		`SELECT id, channel_id, seq, client_msg_id, sender_id, msg_type, content,
		        visible_to, reply_to, forwarded_from, props, created_at
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
func (r *gormMessageRepo) FetchBefore(ctx context.Context, channelID string, userID string, beforeSeq int64, limit int) ([]Message, error) {
	var out []Message
	err := r.db.WithContext(ctx).Raw(
		`SELECT * FROM (
		    SELECT id, channel_id, seq, client_msg_id, sender_id, msg_type, content,
		           visible_to, reply_to, forwarded_from, props, created_at
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
func (r *gormMessageRepo) FetchAround(ctx context.Context, channelID string, userID string, aroundSeq int64, limit int) ([]Message, error) {
	ctx, span := tracer.Start(ctx, "MessageRepo.FetchAround")
	defer span.End()

	half := limit / 2
	var out []Message
	err := r.db.WithContext(ctx).Raw(
		`(SELECT id, channel_id, seq, client_msg_id, sender_id, msg_type, content,
		         visible_to, reply_to, forwarded_from, props, created_at
		  FROM messages
		  WHERE channel_id = ? AND seq <= ?
		    AND (visible_to IS NULL OR ? = ANY(visible_to) OR sender_id = ?)
		  ORDER BY seq DESC LIMIT ?)
		 UNION ALL
		 (SELECT id, channel_id, seq, client_msg_id, sender_id, msg_type, content,
		         visible_to, reply_to, forwarded_from, props, created_at
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

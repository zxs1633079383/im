//go:build integration

package repo

import (
	"context"
	"sync"
	"testing"

	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
	"im-server/internal/testutil/containers"
)

// messageEventTestEnv wires gormMessageRepo + gormChannelRepo + gormChannelEventRepo
// on a freshly migrated Postgres testcontainer. The harness mirrors the
// production wiring (cmd/gateway/main.go) so we exercise the same code paths
// that fan out to clients in real deployments.
//
// All four mutation handlers (Send / UpdateContent / SoftDelete /
// PostSystemMessage) plus MarkRead must INSERT exactly one channel_event row
// per mutation in the same transaction as the underlying business write
// (C017 §3.1). The tests below assert that property and that a rollback
// removes the event row (no orphan events).
type messageEventTestEnv struct {
	db        *gorm.DB
	channels  ChannelRepo
	events    ChannelEventRepo
	messages  MessageRepo
	channelID string
}

func newMessageEventTestEnv(t *testing.T) *messageEventTestEnv {
	t.Helper()
	dsn := containers.StartPostgres(t)
	db, err := Open(Config{DSN: dsn})
	require.NoError(t, err)

	channels := NewChannelRepo(db)
	events := NewChannelEventRepo(db)
	messages := NewMessageRepo(db, channels, events)

	// Seed a channel + the per-channel PG sequences (event_seq, msg_seq).
	ch := &Channel{
		Type:      ChannelTypeGroup,
		Name:      "test-mutation-events",
		CreatorID: "u-creator",
	}
	require.NoError(t, db.Create(ch).Error)
	require.NoError(t, db.Transaction(func(tx *gorm.DB) error {
		return events.CreateChannelSequences(context.Background(), tx, ch.ID)
	}))

	return &messageEventTestEnv{
		db:        db,
		channels:  channels,
		events:    events,
		messages:  messages,
		channelID: ch.ID,
	}
}

// countEvents returns the total channel_event rows for the test channel.
func (e *messageEventTestEnv) countEvents(t *testing.T) int {
	t.Helper()
	rows, err := e.events.FetchAfter(context.Background(), e.channelID, 0, 0)
	require.NoError(t, err)
	return len(rows)
}

// eventTypesFor returns the EventType of every event row for the channel,
// in event_seq ASC order. Empty when there are no events.
func (e *messageEventTestEnv) eventTypesFor(t *testing.T) []EventType {
	t.Helper()
	rows, err := e.events.FetchAfter(context.Background(), e.channelID, 0, 0)
	require.NoError(t, err)
	out := make([]EventType, len(rows))
	for i, r := range rows {
		out[i] = r.EventType
	}
	return out
}

// TestSend_AppendsEventTypeNew asserts every successful Send appends exactly
// one channel_event row of EventTypeNew with the message id + sender id.
// (C017 §3.2 Send template)
func TestSend_AppendsEventTypeNew(t *testing.T) {
	env := newMessageEventTestEnv(t)
	ctx := context.Background()

	require.Equal(t, 0, env.countEvents(t), "no events before any send")

	msg := &Message{
		ChannelID: env.channelID,
		SenderID:  "u-sender-1",
		MsgType:   MsgTypeText,
		Content:   "hello world",
	}
	require.NoError(t, env.messages.Send(ctx, msg))
	require.NotEmpty(t, msg.ID, "Send must hydrate the message id")

	rows, err := env.events.FetchAfter(ctx, env.channelID, 0, 0)
	require.NoError(t, err)
	require.Len(t, rows, 1, "exactly one event must be appended per Send")
	require.Equal(t, EventTypeNew, rows[0].EventType)
	require.NotNil(t, rows[0].MsgID)
	require.Equal(t, msg.ID, *rows[0].MsgID, "event row must point at the new message id")
	require.Equal(t, "u-sender-1", rows[0].ActorID)
}

// TestSend_IdempotentNoSecondEvent asserts a retry with the same
// (channel_id, client_msg_id) is short-circuited at the idempotency check
// and does NOT append a second channel_event row.
func TestSend_IdempotentNoSecondEvent(t *testing.T) {
	env := newMessageEventTestEnv(t)
	ctx := context.Background()

	msg := &Message{
		ChannelID:   env.channelID,
		SenderID:    "u-sender-1",
		MsgType:     MsgTypeText,
		Content:     "first",
		ClientMsgID: "client-id-shared",
	}
	require.NoError(t, env.messages.Send(ctx, msg))

	// Second Send with the same client_msg_id: must short-circuit, must NOT
	// append a second event.
	retry := &Message{
		ChannelID:   env.channelID,
		SenderID:    "u-sender-1",
		MsgType:     MsgTypeText,
		Content:     "first",
		ClientMsgID: "client-id-shared",
	}
	require.NoError(t, env.messages.Send(ctx, retry))
	require.Equal(t, msg.ID, retry.ID, "retry must adopt the existing id")
	require.Equal(t, msg.Seq, retry.Seq, "retry must adopt the existing seq")

	require.Equal(t, 1, env.countEvents(t),
		"idempotent retry must NOT append a second channel_event row (C017 §3.2)")
}

// TestUpdateContent_AppendsEventTypeEdit asserts a successful edit appends
// exactly one EventTypeEdit row in the same tx as the messages UPDATE.
func TestUpdateContent_AppendsEventTypeEdit(t *testing.T) {
	env := newMessageEventTestEnv(t)
	ctx := context.Background()

	msg := &Message{ChannelID: env.channelID, SenderID: "u1", MsgType: MsgTypeText, Content: "v1"}
	require.NoError(t, env.messages.Send(ctx, msg))
	require.Equal(t, 1, env.countEvents(t), "one Send → one event")

	updated, err := env.messages.UpdateContent(ctx, msg.ID, "u1", "v2-edited")
	require.NoError(t, err)
	require.Equal(t, "v2-edited", updated.Content)

	got := env.eventTypesFor(t)
	require.Equal(t, []EventType{EventTypeNew, EventTypeEdit}, got,
		"event sequence must be [New, Edit] after one send + one edit")
}

// TestUpdateContent_NotSenderNoEvent asserts a failed edit (non-sender) does
// NOT append an event row.
func TestUpdateContent_NotSenderNoEvent(t *testing.T) {
	env := newMessageEventTestEnv(t)
	ctx := context.Background()

	msg := &Message{ChannelID: env.channelID, SenderID: "u-owner", MsgType: MsgTypeText, Content: "hi"}
	require.NoError(t, env.messages.Send(ctx, msg))

	_, err := env.messages.UpdateContent(ctx, msg.ID, "u-other", "hijack")
	require.ErrorIs(t, err, ErrForbidden)
	require.Equal(t, 1, env.countEvents(t),
		"failed edit must NOT append an event row (only the original New remains)")
}

// TestSoftDelete_AppendsEventTypeDelete asserts a successful delete appends
// exactly one EventTypeDelete row in the same tx as the messages UPDATE.
func TestSoftDelete_AppendsEventTypeDelete(t *testing.T) {
	env := newMessageEventTestEnv(t)
	ctx := context.Background()

	msg := &Message{ChannelID: env.channelID, SenderID: "u1", MsgType: MsgTypeText, Content: "byebye"}
	require.NoError(t, env.messages.Send(ctx, msg))

	deleted, err := env.messages.SoftDelete(ctx, msg.ID, "u1")
	require.NoError(t, err)
	require.True(t, deleted.Deleted)

	got := env.eventTypesFor(t)
	require.Equal(t, []EventType{EventTypeNew, EventTypeDelete}, got)
}

// TestSoftDelete_IdempotentNoSecondEvent asserts a second delete on an
// already-deleted message returns ErrGone and does NOT append a second event.
func TestSoftDelete_IdempotentNoSecondEvent(t *testing.T) {
	env := newMessageEventTestEnv(t)
	ctx := context.Background()

	msg := &Message{ChannelID: env.channelID, SenderID: "u1", MsgType: MsgTypeText, Content: "byebye"}
	require.NoError(t, env.messages.Send(ctx, msg))

	_, err := env.messages.SoftDelete(ctx, msg.ID, "u1")
	require.NoError(t, err)
	require.Equal(t, 2, env.countEvents(t), "New + Delete")

	// Second delete: idempotent ErrGone, no new event row.
	_, err = env.messages.SoftDelete(ctx, msg.ID, "u1")
	require.ErrorIs(t, err, ErrGone)
	require.Equal(t, 2, env.countEvents(t),
		"idempotent second delete must NOT append a second Delete event")
}

// TestPostSystemMessage_AppendsEventTypeMember asserts a system message (e.g.
// member joined) appends exactly one EventTypeMember row alongside the
// messages INSERT, both inside one tx whether or not the caller supplies one.
func TestPostSystemMessage_AppendsEventTypeMember(t *testing.T) {
	env := newMessageEventTestEnv(t)
	ctx := context.Background()

	// Path A: caller does NOT supply a tx — repo opens one internally.
	props := map[string]any{"sys_type": "member_joined", "actor_id": "u-actor", "target_id": "u-target"}
	msg, err := env.messages.PostSystemMessage(ctx, nil, env.channelID, "u-actor", nil, props)
	require.NoError(t, err)
	require.Equal(t, MsgTypeSystem, msg.MsgType)
	require.Equal(t, 1, env.countEvents(t), "PostSystemMessage(nil tx) must still append an event")
	rows, err := env.events.FetchAfter(ctx, env.channelID, 0, 10)
	require.NoError(t, err)
	require.Equal(t, EventTypeMember, rows[0].EventType)

	// Path B: caller supplies a tx — same event append must run inside it.
	require.NoError(t, env.db.Transaction(func(tx *gorm.DB) error {
		_, perr := env.messages.PostSystemMessage(ctx, tx, env.channelID, "u-actor", nil,
			map[string]any{"sys_type": "channel_renamed", "new_name": "renamed"})
		return perr
	}))
	require.Equal(t, 2, env.countEvents(t), "PostSystemMessage(tx) must also append an event")
}

// TestPostSystemMessage_RollbackRemovesEvent asserts a rollback on the
// caller-supplied tx removes BOTH the system message row AND the event row
// (the co-transactional guarantee, C017 §3.1).
func TestPostSystemMessage_RollbackRemovesEvent(t *testing.T) {
	env := newMessageEventTestEnv(t)
	ctx := context.Background()

	const sentinel = "rollback-trigger"
	err := env.db.Transaction(func(tx *gorm.DB) error {
		_, perr := env.messages.PostSystemMessage(ctx, tx, env.channelID, "u-actor", nil,
			map[string]any{"sys_type": "member_left", "actor_id": "u-actor"})
		if perr != nil {
			return perr
		}
		return &rollbackSentinel{msg: sentinel}
	})
	require.Error(t, err)
	require.Equal(t, 0, env.countEvents(t),
		"tx rollback must leave NO channel_event rows (no orphan events)")
}

// rollbackSentinel is a sentinel error used to force a gorm tx rollback in
// the test above without triggering a noisy real error from a downstream
// layer. gorm rolls the tx back on any non-nil error returned from the fn.
type rollbackSentinel struct{ msg string }

func (e *rollbackSentinel) Error() string { return e.msg }

// TestSend_ConcurrentEventSeqUnique asserts 50 concurrent Sends produce 50
// distinct event_seq values. PG sequence is non-transactional so even if
// many goroutines collide, each gets a unique number (C018 §3.2).
func TestSend_ConcurrentEventSeqUnique(t *testing.T) {
	env := newMessageEventTestEnv(t)
	ctx := context.Background()

	const N = 50
	var wg sync.WaitGroup
	errs := make(chan error, N)
	for i := 0; i < N; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			msg := &Message{
				ChannelID: env.channelID,
				SenderID:  "u-sender",
				MsgType:   MsgTypeText,
				Content:   "concurrent",
			}
			if err := env.messages.Send(ctx, msg); err != nil {
				errs <- err
			}
		}(i)
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		require.NoError(t, err)
	}

	rows, err := env.events.FetchAfter(ctx, env.channelID, 0, 0)
	require.NoError(t, err)
	require.Len(t, rows, N, "every concurrent Send must produce exactly one event row")

	seen := make(map[int64]struct{}, N)
	for _, r := range rows {
		_, dup := seen[r.EventSeq]
		require.False(t, dup, "event_seq %d duplicated under concurrent Send", r.EventSeq)
		seen[r.EventSeq] = struct{}{}
	}
}

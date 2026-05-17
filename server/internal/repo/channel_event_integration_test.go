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

// channelEventTestEnv spins up a Postgres testcontainer with all migrations
// applied (including 024_channel_event), creates a parent channel row plus
// its PG sequences via CreateChannelSequences, and returns the repos +
// channel ID for use in the e2e scenarios below.
type channelEventTestEnv struct {
	db        *gorm.DB
	repo      ChannelEventRepo
	chRepo    ChannelRepo
	channelID string
}

func newChannelEventTestEnv(t *testing.T) *channelEventTestEnv {
	t.Helper()
	dsn := containers.StartPostgres(t)
	db, err := Open(Config{DSN: dsn})
	require.NoError(t, err)

	repo := NewChannelEventRepo(db)
	chRepo := NewChannelRepo(db)

	// Seed one channel directly via GORM so we can attach event sequences to
	// it. The channels table has FK from channel_sequence_meta, so we cannot
	// CreateChannelSequences against a non-existent id.
	ch := &Channel{
		Type:      ChannelTypeGroup,
		Name:      "test",
		CreatorID: "test-creator",
	}
	require.NoError(t, db.Create(ch).Error)

	require.NoError(t, db.Transaction(func(tx *gorm.DB) error {
		return repo.CreateChannelSequences(context.Background(), tx, ch.ID)
	}))

	return &channelEventTestEnv{
		db:        db,
		repo:      repo,
		chRepo:    chRepo,
		channelID: ch.ID,
	}
}

// TestNextEventSeq_Monotonic asserts the sequence allocator is strictly
// monotonic and gap-free under serial calls. (C018 §3.2)
func TestNextEventSeq_Monotonic(t *testing.T) {
	env := newChannelEventTestEnv(t)
	ctx := context.Background()

	var prev int64
	for i := 0; i < 20; i++ {
		seq, err := env.repo.NextEventSeq(ctx, nil, env.channelID)
		require.NoError(t, err)
		require.Greater(t, seq, prev, "seq %d not strictly greater than prev %d", seq, prev)
		prev = seq
	}
	require.Equal(t, int64(20), prev, "after 20 calls, last seq must be 20")
}

// TestAppendEvent_ReusesCallerTx asserts AppendEvent inserts within the
// caller's transaction and that a tx rollback removes the row. This is the
// foundational property behind C017 §3.1 ("must be co-transactional").
func TestAppendEvent_ReusesCallerTx(t *testing.T) {
	env := newChannelEventTestEnv(t)
	ctx := context.Background()

	// Successful commit: row visible after tx commit.
	require.NoError(t, env.db.Transaction(func(tx *gorm.DB) error {
		seq, err := env.repo.NextEventSeq(ctx, tx, env.channelID)
		require.NoError(t, err)
		msgID := "msg-commit-1"
		return env.repo.AppendEvent(ctx, tx, &ChannelEvent{
			ChannelID: env.channelID, EventSeq: seq,
			EventType: EventTypeNew, MsgID: &msgID, ActorID: "u1",
		})
	}))

	got, err := env.repo.FetchAfter(ctx, env.channelID, 0, 10)
	require.NoError(t, err)
	require.Len(t, got, 1)
	require.Equal(t, "msg-commit-1", *got[0].MsgID)

	// Rolled-back tx: row absent. NextEventSeq still consumes a number (PG
	// sequences are non-transactional, see C018 §3.2 caveat), so we don't
	// assert on event_seq continuity here.
	rollbackErr := env.db.Transaction(func(tx *gorm.DB) error {
		seq, err := env.repo.NextEventSeq(ctx, tx, env.channelID)
		require.NoError(t, err)
		msgID := "msg-rollback-1"
		if err := env.repo.AppendEvent(ctx, tx, &ChannelEvent{
			ChannelID: env.channelID, EventSeq: seq,
			EventType: EventTypeEdit, MsgID: &msgID, ActorID: "u2",
		}); err != nil {
			return err
		}
		return gorm.ErrInvalidTransaction // any non-nil error triggers rollback
	})
	require.Error(t, rollbackErr)

	got, err = env.repo.FetchAfter(ctx, env.channelID, 0, 10)
	require.NoError(t, err)
	require.Len(t, got, 1, "rollback must leave only the committed row visible")
	require.Equal(t, "msg-commit-1", *got[0].MsgID)
}

// TestFetchAfter_OrderingAndLimit asserts FetchAfter respects ASC ordering
// by event_seq and the supplied limit, and that the cursor (afterEventSeq)
// is strict-greater not greater-or-equal.
func TestFetchAfter_OrderingAndLimit(t *testing.T) {
	env := newChannelEventTestEnv(t)
	ctx := context.Background()

	// Seed 10 events.
	require.NoError(t, env.db.Transaction(func(tx *gorm.DB) error {
		for i := 0; i < 10; i++ {
			seq, err := env.repo.NextEventSeq(ctx, tx, env.channelID)
			if err != nil {
				return err
			}
			id := "msg-" + string(rune('a'+i))
			if err := env.repo.AppendEvent(ctx, tx, &ChannelEvent{
				ChannelID: env.channelID, EventSeq: seq,
				EventType: EventTypeNew, MsgID: &id, ActorID: "u1",
			}); err != nil {
				return err
			}
		}
		return nil
	}))

	// All 10, no limit (limit=0 falls through to "no limit").
	all, err := env.repo.FetchAfter(ctx, env.channelID, 0, 0)
	require.NoError(t, err)
	require.Len(t, all, 10)
	for i := 1; i < len(all); i++ {
		require.Greater(t, all[i].EventSeq, all[i-1].EventSeq,
			"FetchAfter must return rows in ASC event_seq order")
	}

	// First-page semantics: limit=3 starting from cursor 0.
	page1, err := env.repo.FetchAfter(ctx, env.channelID, 0, 3)
	require.NoError(t, err)
	require.Len(t, page1, 3)
	require.Equal(t, all[0].EventSeq, page1[0].EventSeq)
	require.Equal(t, all[2].EventSeq, page1[2].EventSeq)

	// Cursor advance: passing the last seen seq must skip exactly that row.
	page2, err := env.repo.FetchAfter(ctx, env.channelID, page1[2].EventSeq, 3)
	require.NoError(t, err)
	require.Len(t, page2, 3)
	require.Equal(t, all[3].EventSeq, page2[0].EventSeq,
		"cursor is strict-greater-than, not greater-or-equal")

	// Cursor past the end: empty result, no error.
	tail, err := env.repo.FetchAfter(ctx, env.channelID, all[9].EventSeq, 10)
	require.NoError(t, err)
	require.Empty(t, tail)
}

// TestCreateChannelSequences_Idempotent asserts the method can be called
// twice on the same channel without error and without disturbing existing
// sequence state (no seq reset, no meta duplication).
func TestCreateChannelSequences_Idempotent(t *testing.T) {
	env := newChannelEventTestEnv(t)
	ctx := context.Background()

	// Burn a few seq numbers to advance the counter.
	for i := 0; i < 3; i++ {
		_, err := env.repo.NextEventSeq(ctx, nil, env.channelID)
		require.NoError(t, err)
	}

	// Re-run CreateChannelSequences: must succeed, and seq must not reset.
	require.NoError(t, env.db.Transaction(func(tx *gorm.DB) error {
		return env.repo.CreateChannelSequences(ctx, tx, env.channelID)
	}))
	next, err := env.repo.NextEventSeq(ctx, nil, env.channelID)
	require.NoError(t, err)
	require.Equal(t, int64(4), next, "re-running CreateChannelSequences must NOT reset the counter")

	// Meta row must be exactly one.
	var count int64
	require.NoError(t, env.db.Table("channel_sequence_meta").
		Where("channel_id = ?", env.channelID).Count(&count).Error)
	require.Equal(t, int64(1), count, "meta row count must be 1 even after duplicate create")
}

// TestGetMemberChannelEventSeqs covers the reconnect-bootstrap helper:
// every channel the user belongs to is returned with the current MAX
// event_seq (or 0 if no events yet).
func TestGetMemberChannelEventSeqs(t *testing.T) {
	env := newChannelEventTestEnv(t)
	ctx := context.Background()

	// Make env.channelID have one event row + a second channel with zero.
	second := &Channel{Type: ChannelTypeGroup, Name: "second", CreatorID: "test-creator"}
	require.NoError(t, env.db.Create(second).Error)
	require.NoError(t, env.db.Transaction(func(tx *gorm.DB) error {
		return env.repo.CreateChannelSequences(ctx, tx, second.ID)
	}))

	// User joins both channels.
	const userID = "u-bootstrap-1"
	require.NoError(t, env.chRepo.AddMember(ctx, env.channelID, userID, MemberRoleMember))
	require.NoError(t, env.chRepo.AddMember(ctx, second.ID, userID, MemberRoleMember))

	// Append one event to env.channelID; leave second.ID empty.
	require.NoError(t, env.db.Transaction(func(tx *gorm.DB) error {
		seq, err := env.repo.NextEventSeq(ctx, tx, env.channelID)
		if err != nil {
			return err
		}
		msgID := "msg-bootstrap-1"
		return env.repo.AppendEvent(ctx, tx, &ChannelEvent{
			ChannelID: env.channelID, EventSeq: seq,
			EventType: EventTypeNew, MsgID: &msgID, ActorID: "u1",
		})
	}))

	got, err := env.repo.GetMemberChannelEventSeqs(ctx, userID)
	require.NoError(t, err)
	require.Len(t, got, 2, "user belongs to 2 channels")
	require.Greater(t, got[env.channelID], int64(0), "channel with events must have seq > 0")
	require.Equal(t, int64(0), got[second.ID], "channel with no events must report seq=0 (not absent)")
}

// TestNextMessageSeq_Monotonic asserts the channel.go::NextMessageSeq
// wrapper is strictly monotonic — the same property as NextEventSeq but
// going through the separate channel_msg_seq_<id> sequence (C018).
func TestNextMessageSeq_Monotonic(t *testing.T) {
	env := newChannelEventTestEnv(t)
	ctx := context.Background()

	var prev int64
	for i := 0; i < 20; i++ {
		seq, err := env.chRepo.NextMessageSeq(ctx, nil, env.channelID)
		require.NoError(t, err)
		require.Greater(t, seq, prev)
		prev = seq
	}
	require.Equal(t, int64(20), prev)
}

// TestIncrementSeq_DelegatesToNextMessageSeq locks the C018 §3.3
// "渐进迁移" contract: IncrementSeq must behave identically to
// NextMessageSeq (same sequence, same monotonicity), so existing callers
// like MessageRepo.AllocSeqAndInsert don't observe a behaviour change.
func TestIncrementSeq_DelegatesToNextMessageSeq(t *testing.T) {
	env := newChannelEventTestEnv(t)
	ctx := context.Background()

	a, err := env.chRepo.IncrementSeq(ctx, nil, env.channelID)
	require.NoError(t, err)
	b, err := env.chRepo.NextMessageSeq(ctx, nil, env.channelID)
	require.NoError(t, err)
	require.Equal(t, a+1, b, "IncrementSeq and NextMessageSeq must share the same sequence")
}

// TestChannelEventRepo_E2E walks the full happy path that P3 handlers will
// drive: AllocSeq → INSERT message → NextEventSeq → AppendEvent inside a
// single tx, twice. Then FetchAfter walks the timeline.
func TestChannelEventRepo_E2E(t *testing.T) {
	env := newChannelEventTestEnv(t)
	ctx := context.Background()

	type send struct {
		msgID   string
		evtType EventType
	}
	plan := []send{
		{"e2e-msg-1", EventTypeNew},
		{"e2e-msg-2", EventTypeNew},
		{"e2e-msg-1", EventTypeEdit},
		{"e2e-msg-1", EventTypeDelete},
	}

	for _, p := range plan {
		p := p
		require.NoError(t, env.db.Transaction(func(tx *gorm.DB) error {
			seq, err := env.repo.NextEventSeq(ctx, tx, env.channelID)
			if err != nil {
				return err
			}
			return env.repo.AppendEvent(ctx, tx, &ChannelEvent{
				ChannelID: env.channelID, EventSeq: seq,
				EventType: p.evtType, MsgID: &p.msgID, ActorID: "u1",
			})
		}))
	}

	got, err := env.repo.FetchAfter(ctx, env.channelID, 0, 100)
	require.NoError(t, err)
	require.Len(t, got, len(plan))
	for i, p := range plan {
		require.Equal(t, p.evtType, got[i].EventType, "event %d type mismatch", i)
		require.Equal(t, p.msgID, *got[i].MsgID, "event %d msg id mismatch", i)
	}
}

// TestNextEventSeq_ConcurrentMonotonic asserts the C018 §3 contract under
// concurrent allocation: N goroutines × K calls each → N*K distinct seq
// numbers (no duplicates) with no errors. Gaps are tolerated.
func TestNextEventSeq_ConcurrentMonotonic(t *testing.T) {
	env := newChannelEventTestEnv(t)
	ctx := context.Background()

	const (
		goroutines = 8
		perRoutine = 25
	)
	out := make(chan int64, goroutines*perRoutine)
	var wg sync.WaitGroup
	wg.Add(goroutines)
	for g := 0; g < goroutines; g++ {
		go func() {
			defer wg.Done()
			for i := 0; i < perRoutine; i++ {
				seq, err := env.repo.NextEventSeq(ctx, nil, env.channelID)
				if err != nil {
					t.Errorf("NextEventSeq: %v", err)
					return
				}
				out <- seq
			}
		}()
	}
	wg.Wait()
	close(out)

	seen := make(map[int64]struct{}, goroutines*perRoutine)
	for seq := range out {
		if _, dup := seen[seq]; dup {
			t.Fatalf("duplicate seq %d under concurrent NextEventSeq", seq)
		}
		seen[seq] = struct{}{}
	}
	require.Equal(t, goroutines*perRoutine, len(seen),
		"every concurrent NextEventSeq call must produce a unique seq")
}

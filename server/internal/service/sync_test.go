package service

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/require"

	"im-server/internal/repo"
)

// fakeChannelStore stubs SyncChannelStore for fillDeltaPayload tests.
// fillDeltaPayload doesn't touch GetMemberChannelSeqs / GetMember (those run
// in the outer Sync orchestration), so we only need a no-op impl here.
type fakeChannelStore struct{}

func (fakeChannelStore) GetMemberChannelSeqs(_ context.Context, _ string) (map[string]int64, error) {
	return nil, nil
}
func (fakeChannelStore) GetMember(_ context.Context, _, _ string) (*repo.ChannelMember, error) {
	return nil, nil
}

// fakeMsgStore returns a configurable slice for FetchForUser. Calls are
// recorded so tests can assert which arm of the decision tree ran.
//
// byIDsOut feeds the v2-only GetByIDsForUser path — fillDeltaPayload (v1)
// tests leave it nil so the empty default returns []; the SyncV2 fixtures
// populate it to verify per-channel hydration.
type fakeMsgStore struct {
	out       []repo.Message
	lastAfter int64
	lastLimit int
	callCount int

	byIDsOut    []repo.Message
	byIDsLast   []string
	byIDsCalled int
}

func (f *fakeMsgStore) FetchForUser(
	_ context.Context, _ string, _ string, afterSeq int64, limit int,
) ([]repo.Message, error) {
	f.callCount++
	f.lastAfter = afterSeq
	f.lastLimit = limit
	// Honour the limit on returned slice so tests can probe slice/full toggle.
	if limit < len(f.out) {
		return f.out[:limit], nil
	}
	return f.out, nil
}

func (f *fakeMsgStore) GetByIDsForUser(
	_ context.Context, _ string, ids []string,
) ([]repo.Message, error) {
	f.byIDsCalled++
	f.byIDsLast = ids
	return f.byIDsOut, nil
}

func buildDeltaFixture(serverSeq int64) *SyncChannelDelta {
	return &SyncChannelDelta{ID: "ch1", ServerSeq: serverSeq}
}

// ── kind=empty: small gap with no missed messages ────────────────────────────
func TestFillDeltaPayload_Empty_NoMissedMessages(t *testing.T) {
	s := NewSyncService(fakeChannelStore{}, &fakeMsgStore{out: nil})
	delta := buildDeltaFixture(10)
	// clientSeq=10 (== serverSeq, but we still test the small-gap-zero case
	// at clientSeq=9 → gap=1, FetchForUser returns []).
	s.fillDeltaPayload(context.Background(), delta, "ch1", "u1", 9, true)

	require.NotNil(t, delta.Kind)
	require.Equal(t, "empty", delta.Kind.Type)
	require.Zero(t, delta.Kind.ResetTo, "empty 分支不该带 reset_to")
	require.Nil(t, delta.NextCursor)
	require.Empty(t, delta.Messages)
	require.False(t, delta.HasMore)
}

// ── kind=full: small gap with N missed messages ──────────────────────────────
func TestFillDeltaPayload_Full_SmallGap(t *testing.T) {
	msgs := []repo.Message{
		{Seq: 8}, {Seq: 9}, {Seq: 10},
	}
	s := NewSyncService(fakeChannelStore{}, &fakeMsgStore{out: msgs})
	delta := buildDeltaFixture(10)
	s.fillDeltaPayload(context.Background(), delta, "ch1", "u1", 7, true)

	require.NotNil(t, delta.Kind)
	require.Equal(t, "full", delta.Kind.Type)
	require.Nil(t, delta.NextCursor)
	require.Len(t, delta.Messages, 3)
	require.False(t, delta.HasMore, "Full 分支不发 has_more")
}

// ── kind=full: unknown channel (first sync) ──────────────────────────────────
func TestFillDeltaPayload_Full_UnknownChannel(t *testing.T) {
	msgs := []repo.Message{{Seq: 8}, {Seq: 9}, {Seq: 10}}
	s := NewSyncService(fakeChannelStore{}, &fakeMsgStore{out: msgs})
	delta := buildDeltaFixture(10)
	// known=false (channel 不在 clientSeqs)
	s.fillDeltaPayload(context.Background(), delta, "ch1", "u1", 0, false)

	require.NotNil(t, delta.Kind)
	require.Equal(t, "full", delta.Kind.Type)
	require.Len(t, delta.Messages, 3)
}

// ── kind=slice: mid gap (SyncGapThreshold < gap ≤ SyncTooLongSeqDiff) ────────
func TestFillDeltaPayload_Slice_MidGap(t *testing.T) {
	msgs := make([]repo.Message, SyncMsgLimit)
	for i := 0; i < SyncMsgLimit; i++ {
		msgs[i] = repo.Message{Seq: int64(i + 1)} // seq 1..50
	}
	s := NewSyncService(fakeChannelStore{}, &fakeMsgStore{out: msgs})
	delta := buildDeltaFixture(SyncGapThreshold + 500) // gap = 600 - 0 = 600 > 100, < 10000

	s.fillDeltaPayload(context.Background(), delta, "ch1", "u1", 0, true)

	require.NotNil(t, delta.Kind)
	require.Equal(t, "slice", delta.Kind.Type)
	require.True(t, delta.HasMore, "Slice 分支必须 has_more=true 给旧客户端")
	require.NotNil(t, delta.NextCursor)
	require.Equal(t, int64(SyncMsgLimit), *delta.NextCursor,
		"next_cursor 应为切片中最高 seq (msgs[len-1].Seq)")
	require.Len(t, delta.Messages, SyncMsgLimit)
}

// ── kind=too_long: gap > SyncTooLongSeqDiff ──────────────────────────────────
func TestFillDeltaPayload_TooLong_HugeGap(t *testing.T) {
	s := NewSyncService(fakeChannelStore{}, &fakeMsgStore{out: nil})
	delta := buildDeltaFixture(20000)
	// clientSeq=0 → gap = 20000 > SyncTooLongSeqDiff(10000)
	s.fillDeltaPayload(context.Background(), delta, "ch1", "u1", 0, true)

	require.NotNil(t, delta.Kind)
	require.Equal(t, "too_long", delta.Kind.Type)
	require.Equal(t, int64(20000), delta.Kind.ResetTo,
		"too_long.reset_to 必须 == serverSeq 让客户端清表并 force_init")
	require.Nil(t, delta.NextCursor)
	require.Empty(t, delta.Messages, "too_long 不带 messages — 客户端清表后重拉首屏")
	require.False(t, delta.HasMore)
}

// ── kind=too_long 不应对 unknown channel 触发（新 channel 总是 full+fetchLatest）
func TestFillDeltaPayload_UnknownChannel_DoesNotTriggerTooLong(t *testing.T) {
	msgs := []repo.Message{{Seq: 19999}, {Seq: 20000}}
	s := NewSyncService(fakeChannelStore{}, &fakeMsgStore{out: msgs})
	delta := buildDeltaFixture(20000)
	// known=false + 大数 serverSeq → 仍走 unknown 分支（不进 too_long）
	s.fillDeltaPayload(context.Background(), delta, "ch1", "u1", 0, false)

	require.NotNil(t, delta.Kind)
	require.Equal(t, "full", delta.Kind.Type,
		"新 channel 即使 serverSeq 极大也走 full+fetchLatest 不走 too_long")
}

// ── slice 切片 next_cursor 为空切片时不写 next_cursor (防 panic) ─────────────
func TestFillDeltaPayload_Slice_EmptyMessages(t *testing.T) {
	s := NewSyncService(fakeChannelStore{}, &fakeMsgStore{out: nil})
	delta := buildDeltaFixture(SyncGapThreshold + 500)
	s.fillDeltaPayload(context.Background(), delta, "ch1", "u1", 0, true)

	require.NotNil(t, delta.Kind)
	require.Equal(t, "slice", delta.Kind.Type)
	require.True(t, delta.HasMore)
	require.Nil(t, delta.NextCursor, "空切片不应写 next_cursor")
}

// ── 常量值锁定（前端 / docs 依赖）──────────────────────────────────────────────
func TestSyncTunables_LockedValues(t *testing.T) {
	require.Equal(t, 100, SyncGapThreshold)
	require.Equal(t, 50, SyncMsgLimit)
	require.Equal(t, 500, MaxChannelsPerCall)
	require.Equal(t, 10000, SyncTooLongSeqDiff,
		"TooLong 阈值变更必须同步 docs/BACKEND.md §3.3 + 客户端 harness C006")
}

// ── SyncEntryKind JSON wire 锁定（前端 internally tagged enum 解码依赖） ─────
func TestSyncEntryKind_JSONWire(t *testing.T) {
	// 通过 encoding/json 验证序列化与客户端 wire 期望一致
	cases := []struct {
		name string
		in   SyncEntryKind
		want string
	}{
		{"empty", SyncEntryKind{Type: "empty"}, `{"type":"empty"}`},
		{"full", SyncEntryKind{Type: "full"}, `{"type":"full"}`},
		{"slice", SyncEntryKind{Type: "slice"}, `{"type":"slice"}`},
		{"too_long", SyncEntryKind{Type: "too_long", ResetTo: 12345},
			`{"type":"too_long","reset_to":12345}`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			b, err := json.Marshal(tc.in)
			require.NoError(t, err)
			require.Equal(t, tc.want, string(b))
		})
	}
}

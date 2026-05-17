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
	require.Equal(t, 200, EventLimitPerChannel,
		"per-channel event 上限变更必须同步 C019 §3.2 + 客户端 C009")
	require.Equal(t, 10000, EventTooLongThreshold,
		"v2 too_long 阈值变更必须同步 C019 §3.2 + 客户端 C009")
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

// ── v2 wire (C019 §3.1) — JSON 锁定 ─────────────────────────────────────────
func TestSyncEntryKindV2_JSONWire(t *testing.T) {
	cases := []struct {
		name string
		in   SyncEntryKindV2
		want string
	}{
		{"empty", SyncEntryKindV2{Type: KindEmpty}, `{"type":"empty"}`},
		{"events", SyncEntryKindV2{Type: KindEvents}, `{"type":"events"}`},
		{"slice", SyncEntryKindV2{Type: KindSlice}, `{"type":"slice"}`},
		{"too_long", SyncEntryKindV2{Type: KindTooLong, ResetTo: 42},
			`{"type":"too_long","reset_to":42}`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			b, err := json.Marshal(tc.in)
			require.NoError(t, err)
			require.Equal(t, tc.want, string(b))
		})
	}
}

// ── fakeEventStore — SyncV2 测试 stub ────────────────────────────────────────
//
// fetchAfterOut 按 channelID 返回事件切片；memberSeqs 返回每 channel 的
// max(event_seq)。两者都允许测试逐 case 配置。
type fakeEventStore struct {
	fetchAfterOut map[string][]repo.ChannelEvent
	memberSeqs    map[string]int64
	fetchErr      error
}

func (f *fakeEventStore) FetchAfter(
	_ context.Context, channelID string, afterEventSeq int64, limit int,
) ([]repo.ChannelEvent, error) {
	if f.fetchErr != nil {
		return nil, f.fetchErr
	}
	all := f.fetchAfterOut[channelID]
	out := make([]repo.ChannelEvent, 0, len(all))
	for _, e := range all {
		if e.EventSeq <= afterEventSeq {
			continue
		}
		out = append(out, e)
		if limit > 0 && len(out) >= limit {
			break
		}
	}
	return out, nil
}

func (f *fakeEventStore) GetMemberChannelEventSeqs(
	_ context.Context, _ string,
) (map[string]int64, error) {
	return f.memberSeqs, nil
}

// fakeChannelStoreV2 returns configurable member-channel-seq map and a
// resolved ChannelMember (so unread compute path can run end-to-end).
type fakeChannelStoreV2 struct {
	memberMsgSeqs map[string]int64
	member        *repo.ChannelMember
}

func (f fakeChannelStoreV2) GetMemberChannelSeqs(
	_ context.Context, _ string,
) (map[string]int64, error) {
	return f.memberMsgSeqs, nil
}
func (f fakeChannelStoreV2) GetMember(
	_ context.Context, _, _ string,
) (*repo.ChannelMember, error) {
	return f.member, nil
}

// buildSyncV2Service composes the four collaborators with the supplied
// fakes. callerID + chID 等是测试 case 共享常量。
func buildSyncV2Service(
	channels *fakeChannelStoreV2, msgs *fakeMsgStore, events *fakeEventStore,
) *SyncService {
	if channels == nil {
		channels = &fakeChannelStoreV2{}
	}
	if msgs == nil {
		msgs = &fakeMsgStore{}
	}
	return NewSyncServiceV2(*channels, msgs, events)
}

// ── v2 kind=empty: client cursor 已追平 server ────────────────────────────────
func TestSyncV2_EmptyKind_ClientCaughtUp(t *testing.T) {
	events := &fakeEventStore{
		memberSeqs: map[string]int64{"ch1": 50},
	}
	svc := buildSyncV2Service(nil, nil, events)

	res, err := svc.SyncV2(context.Background(), "u1", SyncParamsV2{
		Cursors: []SyncCursorV2{{ID: "ch1", EventSeq: 50}},
	})
	require.NoError(t, err)
	require.Len(t, res.Channels, 1)
	d := res.Channels[0]
	require.Equal(t, "ch1", d.ID)
	require.Equal(t, int64(50), d.ServerEventSeq)
	require.NotNil(t, d.Kind)
	require.Equal(t, KindEmpty, d.Kind.Type)
	require.Zero(t, d.Kind.ResetTo, "empty 不带 reset_to")
	require.Empty(t, d.Events)
	require.Empty(t, d.Messages)
	require.Nil(t, d.NextCursor)
}

// ── v2 kind=events: 普通 incremental（gap 在限内）─────────────────────────────
func TestSyncV2_EventsKind_IncrementalDelivery(t *testing.T) {
	msgID := "m-100"
	otherMsgID := "m-101"
	events := &fakeEventStore{
		memberSeqs: map[string]int64{"ch1": 12},
		fetchAfterOut: map[string][]repo.ChannelEvent{
			"ch1": {
				{ChannelID: "ch1", EventSeq: 11, EventType: repo.EventTypeNew, MsgID: &msgID, ActorID: "u9"},
				{ChannelID: "ch1", EventSeq: 12, EventType: repo.EventTypeEdit, MsgID: &otherMsgID, ActorID: "u9"},
			},
		},
	}
	msgs := &fakeMsgStore{
		byIDsOut: []repo.Message{
			{ID: msgID, Seq: 50, ChannelID: "ch1"},
			{ID: otherMsgID, Seq: 51, ChannelID: "ch1"},
		},
	}
	channels := &fakeChannelStoreV2{
		memberMsgSeqs: map[string]int64{"ch1": 51},
		member:        &repo.ChannelMember{LastReadSeq: 48, PhantomCount: 0, PhantomAtRead: 0},
	}
	svc := buildSyncV2Service(channels, msgs, events)

	res, err := svc.SyncV2(context.Background(), "u1", SyncParamsV2{
		Cursors: []SyncCursorV2{{ID: "ch1", EventSeq: 10}},
	})
	require.NoError(t, err)
	require.Len(t, res.Channels, 1)
	d := res.Channels[0]
	require.NotNil(t, d.Kind)
	require.Equal(t, KindEvents, d.Kind.Type)
	require.Nil(t, d.NextCursor, "events 不需要 next_cursor")
	require.Len(t, d.Events, 2)
	require.Equal(t, int64(11), d.Events[0].EventSeq)
	require.Equal(t, int64(12), d.Events[1].EventSeq)
	require.Len(t, d.Messages, 2, "snapshot 必须按 message id 索引")
	require.Equal(t, int64(50), d.Messages[msgID].Seq)
	require.Equal(t, int64(51), d.Messages[otherMsgID].Seq)
	require.Equal(t, int64(3), d.Unread, "unread = serverMsgSeq(51) - lastRead(48)")
	require.Equal(t, 1, msgs.byIDsCalled, "events hydration 应只调一次 bulk GetByIDsForUser")
}

// ── v2 kind=slice: gap 超 EventLimitPerChannel → 切片 + NextCursor ───────────
func TestSyncV2_SliceKind_SaturatedBatch(t *testing.T) {
	// 构造 EventLimitPerChannel 条事件，server 高水位远超切片末尾
	batch := make([]repo.ChannelEvent, EventLimitPerChannel)
	for i := 0; i < EventLimitPerChannel; i++ {
		batch[i] = repo.ChannelEvent{
			ChannelID: "ch1", EventSeq: int64(i + 1),
			EventType: repo.EventTypeNew, ActorID: "u9",
		}
	}
	events := &fakeEventStore{
		memberSeqs: map[string]int64{"ch1": int64(EventLimitPerChannel + 500)},
		fetchAfterOut: map[string][]repo.ChannelEvent{
			"ch1": batch,
		},
	}
	svc := buildSyncV2Service(nil, nil, events)

	res, err := svc.SyncV2(context.Background(), "u1", SyncParamsV2{
		Cursors: []SyncCursorV2{{ID: "ch1", EventSeq: 0}},
	})
	require.NoError(t, err)
	require.Len(t, res.Channels, 1)
	d := res.Channels[0]
	require.NotNil(t, d.Kind)
	require.Equal(t, KindSlice, d.Kind.Type)
	require.Len(t, d.Events, EventLimitPerChannel)
	require.NotNil(t, d.NextCursor)
	require.Equal(t, int64(EventLimitPerChannel), *d.NextCursor,
		"NextCursor = events[len-1].EventSeq → client 下次以此为新 cursor 递归")
}

// ── v2 kind=too_long: gap > EventTooLongThreshold → reset 信号 ──────────────
func TestSyncV2_TooLongKind_HugeGap(t *testing.T) {
	events := &fakeEventStore{
		memberSeqs: map[string]int64{"ch1": EventTooLongThreshold + 1},
		fetchAfterOut: map[string][]repo.ChannelEvent{
			"ch1": {{ChannelID: "ch1", EventSeq: 1}}, // should be ignored — too_long short-circuits
		},
	}
	svc := buildSyncV2Service(nil, nil, events)

	res, err := svc.SyncV2(context.Background(), "u1", SyncParamsV2{
		Cursors: []SyncCursorV2{{ID: "ch1", EventSeq: 0}},
	})
	require.NoError(t, err)
	require.Len(t, res.Channels, 1)
	d := res.Channels[0]
	require.NotNil(t, d.Kind)
	require.Equal(t, KindTooLong, d.Kind.Type)
	require.Equal(t, int64(EventTooLongThreshold+1), d.Kind.ResetTo,
		"too_long.reset_to == serverEventSeq → 客户端清表 + 以此 cursor 重起")
	require.Empty(t, d.Events, "too_long 不传 events — 客户端清表后走 /messagesAround 重拉首屏")
	require.Empty(t, d.Messages)
	require.Nil(t, d.NextCursor)
}

// ── v2: unknown channel (client 没 cursor) 不应触发 too_long ─────────────────
//
// 与 v1 fillDeltaPayload_UnknownChannel_DoesNotTriggerTooLong 对齐 ——
// 新 channel 即使 server 端 event_seq 极大也走 events / slice 分支让
// 客户端拿到内容，不发 too_long（客户端尚无本地状态可以"清"）。
func TestSyncV2_UnknownChannel_DoesNotTriggerTooLong(t *testing.T) {
	events := &fakeEventStore{
		memberSeqs: map[string]int64{"ch1": EventTooLongThreshold + 1},
		fetchAfterOut: map[string][]repo.ChannelEvent{
			"ch1": {{ChannelID: "ch1", EventSeq: 1}},
		},
	}
	svc := buildSyncV2Service(nil, nil, events)

	// 传空 cursors → 该 channel 在 clientEventSeqs 里就是未知
	res, err := svc.SyncV2(context.Background(), "u1", SyncParamsV2{Cursors: nil})
	require.NoError(t, err)
	require.Len(t, res.Channels, 1)
	d := res.Channels[0]
	require.NotNil(t, d.Kind)
	require.NotEqual(t, KindTooLong, d.Kind.Type,
		"新 channel 即使 server 端 event_seq 极大也不能进 too_long 分支")
}

// ── v2: ErrSyncV2Unconfigured 触发条件 ──────────────────────────────────────
func TestSyncV2_ReturnsErrUnconfigured_WhenChannelEventStoreNil(t *testing.T) {
	svc := NewSyncService(fakeChannelStore{}, &fakeMsgStore{}) // v1 only
	_, err := svc.SyncV2(context.Background(), "u1", SyncParamsV2{})
	require.ErrorIs(t, err, ErrSyncV2Unconfigured)
}

// ── v2: uniqueMsgIDs dedup + 跳过 nil MsgID ──────────────────────────────────
func TestUniqueMsgIDs_DedupsAndSkipsNil(t *testing.T) {
	mA := "a"
	mB := "b"
	ev := []repo.ChannelEvent{
		{EventSeq: 1, MsgID: &mA},
		{EventSeq: 2, MsgID: &mB},
		{EventSeq: 3, MsgID: &mA}, // dup → skip
		{EventSeq: 4, MsgID: nil}, // ReadMark / Member → skip
		{EventSeq: 5, MsgID: &mB}, // dup → skip
	}
	got := uniqueMsgIDs(ev)
	require.Equal(t, []string{"a", "b"}, got,
		"按 first-occurrence 去重，nil MsgID 不参与")
}

// ── v2: indexMessagesByID 索引完整性 ─────────────────────────────────────────
func TestIndexMessagesByID(t *testing.T) {
	msgs := []repo.Message{
		{ID: "a", Seq: 1},
		{ID: "b", Seq: 2},
	}
	idx := indexMessagesByID(msgs)
	require.Len(t, idx, 2)
	require.Equal(t, int64(1), idx["a"].Seq)
	require.Equal(t, int64(2), idx["b"].Seq)

	// 空切片 → nil 而不是 empty map（让 omitempty 工作）
	require.Nil(t, indexMessagesByID(nil))
	require.Nil(t, indexMessagesByID([]repo.Message{}))
}

package service

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/require"

	"im-server/internal/repo"
)

// fakeMsgStore stubs SyncMsgStore.GetByIDsForUser — the only msg-store
// method Sync touches (FetchForUser was a v1-only path, deleted with the
// 2026-05-17 cutover).
type fakeMsgStore struct {
	byIDsOut    []repo.Message
	byIDsLast   []string
	byIDsCalled int
}

func (f *fakeMsgStore) GetByIDsForUser(
	_ context.Context, _ string, ids []string,
) ([]repo.Message, error) {
	f.byIDsCalled++
	f.byIDsLast = ids
	return f.byIDsOut, nil
}

// ── 常量值锁定（前端 / docs 依赖）──────────────────────────────────────────────
func TestSyncTunables_LockedValues(t *testing.T) {
	require.Equal(t, 500, MaxChannelsPerCall)
	require.Equal(t, 200, EventLimitPerChannel,
		"per-channel event 上限变更必须同步 C019 §3.2 + 客户端 C009")
	require.Equal(t, 10000, EventTooLongThreshold,
		"too_long 阈值变更必须同步 C019 §3.2 + 客户端 C009")
}

// ── SyncEntryKind JSON wire 锁定（前端 internally tagged enum 解码依赖） ─────
func TestSyncEntryKind_JSONWire(t *testing.T) {
	cases := []struct {
		name string
		in   SyncEntryKind
		want string
	}{
		{"empty", SyncEntryKind{Type: KindEmpty}, `{"type":"empty"}`},
		{"events", SyncEntryKind{Type: KindEvents}, `{"type":"events"}`},
		{"slice", SyncEntryKind{Type: KindSlice}, `{"type":"slice"}`},
		{"too_long", SyncEntryKind{Type: KindTooLong, ResetTo: 42},
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

// ── fakeEventStore — Sync 测试 stub ──────────────────────────────────────────
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

// fakeChannelStore returns configurable member-channel-seq map and a
// resolved ChannelMember (so unread compute path can run end-to-end).
type fakeChannelStore struct {
	memberMsgSeqs map[string]int64
	member        *repo.ChannelMember
}

func (f fakeChannelStore) GetMemberChannelSeqs(
	_ context.Context, _ string,
) (map[string]int64, error) {
	return f.memberMsgSeqs, nil
}
func (f fakeChannelStore) GetMember(
	_ context.Context, _, _ string,
) (*repo.ChannelMember, error) {
	return f.member, nil
}

// buildSyncService composes the three collaborators with the supplied
// fakes. callerID + chID 等是测试 case 共享常量。
func buildSyncService(
	channels *fakeChannelStore, msgs *fakeMsgStore, events *fakeEventStore,
) *SyncService {
	if channels == nil {
		channels = &fakeChannelStore{}
	}
	if msgs == nil {
		msgs = &fakeMsgStore{}
	}
	return NewSyncService(*channels, msgs, events)
}

// ── kind=empty: client cursor 已追平 server ──────────────────────────────────
func TestSync_EmptyKind_ClientCaughtUp(t *testing.T) {
	events := &fakeEventStore{
		memberSeqs: map[string]int64{"ch1": 50},
	}
	svc := buildSyncService(nil, nil, events)

	res, err := svc.Sync(context.Background(), "u1", SyncParams{
		Cursors: []SyncCursor{{ID: "ch1", EventSeq: 50}},
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

// ── kind=events: 普通 incremental（gap 在限内）────────────────────────────────
func TestSync_EventsKind_IncrementalDelivery(t *testing.T) {
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
	channels := &fakeChannelStore{
		memberMsgSeqs: map[string]int64{"ch1": 51},
		member:        &repo.ChannelMember{LastReadSeq: 48, PhantomCount: 0, PhantomAtRead: 0},
	}
	svc := buildSyncService(channels, msgs, events)

	res, err := svc.Sync(context.Background(), "u1", SyncParams{
		Cursors: []SyncCursor{{ID: "ch1", EventSeq: 10}},
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

// ── kind=slice: gap 超 EventLimitPerChannel → 切片 + NextCursor ──────────────
func TestSync_SliceKind_SaturatedBatch(t *testing.T) {
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
	svc := buildSyncService(nil, nil, events)

	res, err := svc.Sync(context.Background(), "u1", SyncParams{
		Cursors: []SyncCursor{{ID: "ch1", EventSeq: 0}},
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

// ── kind=too_long: gap > EventTooLongThreshold → reset 信号 ─────────────────
func TestSync_TooLongKind_HugeGap(t *testing.T) {
	events := &fakeEventStore{
		memberSeqs: map[string]int64{"ch1": EventTooLongThreshold + 1},
		fetchAfterOut: map[string][]repo.ChannelEvent{
			"ch1": {{ChannelID: "ch1", EventSeq: 1}}, // should be ignored — too_long short-circuits
		},
	}
	svc := buildSyncService(nil, nil, events)

	res, err := svc.Sync(context.Background(), "u1", SyncParams{
		Cursors: []SyncCursor{{ID: "ch1", EventSeq: 0}},
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

// ── unknown channel (client 没 cursor) 不应触发 too_long ──────────────────────
//
// 新 channel 即使 server 端 event_seq 极大也走 events / slice 分支让
// 客户端拿到内容，不发 too_long（客户端尚无本地状态可以"清"）。
func TestSync_UnknownChannel_DoesNotTriggerTooLong(t *testing.T) {
	events := &fakeEventStore{
		memberSeqs: map[string]int64{"ch1": EventTooLongThreshold + 1},
		fetchAfterOut: map[string][]repo.ChannelEvent{
			"ch1": {{ChannelID: "ch1", EventSeq: 1}},
		},
	}
	svc := buildSyncService(nil, nil, events)

	// 传空 cursors → 该 channel 在 clientEventSeqs 里就是未知
	res, err := svc.Sync(context.Background(), "u1", SyncParams{Cursors: nil})
	require.NoError(t, err)
	require.Len(t, res.Channels, 1)
	d := res.Channels[0]
	require.NotNil(t, d.Kind)
	require.NotEqual(t, KindTooLong, d.Kind.Type,
		"新 channel 即使 server 端 event_seq 极大也不能进 too_long 分支")
}

// ── NewSyncService panics when SyncEventStore is nil ─────────────────────────
func TestNewSyncService_PanicsOnNilEventStore(t *testing.T) {
	require.Panics(t, func() {
		NewSyncService(fakeChannelStore{}, &fakeMsgStore{}, nil)
	}, "nil SyncEventStore 必须 panic — 不再保留 v1 fallback")
}

// ── uniqueMsgIDs dedup + 跳过 nil MsgID ──────────────────────────────────────
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

// ── indexMessagesByID 索引完整性 ──────────────────────────────────────────────
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

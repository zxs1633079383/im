//go:build integration

// Package integration — V5.4 assertion helpers used by the V5 group-level
// scenarios (v5_groups_test.go). These helpers wrap common sequences (sync
// state queries, event counting, de-dup checks) behind one-line calls so the
// group tests read as business flows instead of HTTP chatter.
package integration

import (
	"strconv"
	"testing"

	"im-server/internal/repo"
)

// AssertSyncState queries /api/sync for a single channel cursor and asserts
// the server_seq and unread fields match expectations. Returns the delta so
// the caller can make follow-up assertions.
func AssertSyncState(
	t *testing.T,
	env *v5env,
	tok string,
	channelID int64,
	clientSeq int64,
	wantServerSeq int64,
	wantUnread int64,
) map[string]any {
	t.Helper()
	resp := env.httpExpect.POST("/api/sync").
		WithHeader("Authorization", bearer(tok)).
		WithJSON(map[string]any{
			"channels": []any{map[string]any{"id": channelID, "seq": clientSeq}},
		}).Expect().Status(200).JSON().Object()

	arr := resp.Value("channels").Array()
	if arr.Length().Raw() == 0 {
		// Channel fully caught up → server returns no delta.
		if wantServerSeq == clientSeq && wantUnread == 0 {
			return nil
		}
		t.Fatalf("sync returned no delta for channel %d; want server_seq=%d unread=%d",
			channelID, wantServerSeq, wantUnread)
	}

	d := arr.Value(0).Object()
	gotServerSeq := int64(d.Value("server_seq").Number().Raw())
	gotUnread := int64(d.Value("unread").Number().Raw())
	if gotServerSeq != wantServerSeq {
		t.Fatalf("channel %d server_seq=%d, want %d", channelID, gotServerSeq, wantServerSeq)
	}
	if gotUnread != wantUnread {
		t.Fatalf("channel %d unread=%d, want %d", channelID, gotUnread, wantUnread)
	}
	return d.Raw()
}

// AssertNoDuplicatePushID asserts that no user received two pushes for the
// same (channel_id, seq). The gateway's real dedup key is push_id, but at
// this layer pusher.PushMessage gets the raw message so (channel, seq) is
// the equivalent: one logical send should produce exactly one event per
// target user, not two.
func AssertNoDuplicatePushID(t *testing.T, events []PushEvent) {
	t.Helper()
	seen := make(map[string]int, len(events))
	for _, ev := range events {
		if ev.Msg == nil {
			continue
		}
		key := strconv.FormatInt(ev.UserID, 10) + "|" +
			strconv.FormatInt(ev.Msg.ChannelID, 10) + "|" +
			strconv.FormatInt(ev.Msg.Seq, 10)
		seen[key]++
	}
	for k, n := range seen {
		if n > 1 {
			t.Fatalf("duplicate push detected: key=%s count=%d", k, n)
		}
	}
}

// CountBroadcastsByType returns how many broadcasts of eventType were
// emitted for channelID.
func CountBroadcastsByType(events []BroadcastEvent, channelID int64, eventType string) int {
	n := 0
	for _, ev := range events {
		if ev.ChannelID == channelID && string(ev.EventType) == eventType {
			n++
		}
	}
	return n
}

// FindMessageInSync returns the first message in a sync delta for channelID
// whose seq == want. nil if not present.
func FindMessageInSync(env *v5env, tok string, channelID, clientSeq, want int64) *repo.Message {
	resp := env.httpExpect.POST("/api/sync").
		WithHeader("Authorization", bearer(tok)).
		WithJSON(map[string]any{
			"channels": []any{map[string]any{"id": channelID, "seq": clientSeq}},
		}).Expect().Status(200).JSON().Object()
	arr := resp.Value("channels").Array()
	if arr.Length().Raw() == 0 {
		return nil
	}
	msgs := arr.Value(0).Object().Value("messages").Array()
	for i := 0; i < int(msgs.Length().Raw()); i++ {
		obj := msgs.Value(i).Object()
		if int64(obj.Value("seq").Number().Raw()) == want {
			// The response is JSON; we just return a summary message so
			// the caller can assert on the common fields.
			return &repo.Message{
				ID:        int64(obj.Value("id").Number().Raw()),
				ChannelID: channelID,
				Seq:       want,
				Content:   obj.Value("content").String().Raw(),
			}
		}
	}
	return nil
}

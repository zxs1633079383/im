//go:build integration

package integration

import (
	"context"
	"strconv"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"im-server/internal/middleware"
	"im-server/internal/repo"
)

// TestM4ReadStatsBatch_HappyPath — sender posts two messages in a DM, the
// receiver reads up to seq=1, then queries read-stats for both. The first
// message should report 2 read / 0 unread; the second should report 1 read
// (sender) / 1 unread (receiver).
//
// This validates the SQL JOIN + FILTER aggregates and the per-message ordering
// of unread_user_ids in one shot, amortising the testcontainer cost.
func TestM4ReadStatsBatch_HappyPath(t *testing.T) {
	env := newM4Env(t)
	cookieSender, senderID := env.seedUser(50)
	cookieRecv, recvID := env.seedUser(51)

	dm := successBody(env.expect.POST("/api/channels/dm").
		WithHeader(middleware.MMCookieHeader, cookieSender).
		WithJSON(map[string]any{"peer_id": recvID}).
		Expect().Status(201))
	channelID := int64(dm.Value("id").Number().Raw())

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	// Two messages — seq will be 1 and 2.
	msg1 := &repo.Message{ChannelID: channelID, SenderID: senderID, MsgType: repo.MsgTypeText, Content: "hi 1"}
	msg2 := &repo.Message{ChannelID: channelID, SenderID: senderID, MsgType: repo.MsgTypeText, Content: "hi 2"}
	require.NoError(t, env.messages.Send(ctx, msg1))
	require.NoError(t, env.messages.Send(ctx, msg2))

	// Receiver reads up to current seq (=2 since both are posted).
	// Then we manually rewind their last_read_seq to 1 to exercise the
	// "partially read" path on msg2.
	env.expect.POST("/api/channels/"+strconv.FormatInt(channelID, 10)+"/read").
		WithHeader(middleware.MMCookieHeader, cookieRecv).
		Expect().Status(200)
	require.NoError(t, env.channels.MarkRead(ctx, channelID, recvID, 1))

	// Both senders auto-read their own messages — sender's last_read_seq = 2.
	env.expect.POST("/api/channels/"+strconv.FormatInt(channelID, 10)+"/read").
		WithHeader(middleware.MMCookieHeader, cookieSender).
		Expect().Status(200)

	idsParam := strconv.FormatInt(msg1.ID, 10) + "," + strconv.FormatInt(msg2.ID, 10)
	resp := successBody(env.expect.GET("/api/messages/read-stats").
		WithHeader(middleware.MMCookieHeader, cookieSender).
		WithQuery("ids", idsParam).
		Expect().Status(200))

	stats := resp.Value("stats").Array()
	stats.Length().IsEqual(2)

	byID := map[int64]map[string]any{}
	for i := 0; i < 2; i++ {
		entry := stats.Value(i).Object().Raw()
		byID[int64(entry["messageId"].(float64))] = entry
	}

	m1 := byID[msg1.ID]
	require.EqualValues(t, 2, m1["totalMembers"])
	require.EqualValues(t, 2, m1["readCount"])
	require.EqualValues(t, 0, m1["unreadCount"])

	m2 := byID[msg2.ID]
	require.EqualValues(t, 2, m2["totalMembers"])
	require.EqualValues(t, 1, m2["readCount"])
	require.EqualValues(t, 1, m2["unreadCount"])
	unread2, _ := m2["unreadUserIds"].([]any)
	require.Equal(t, []any{recvID}, unread2)
}

// TestM4ReadStatsBatch_NonMemberFiltered — caller asks for a message from a
// channel they're not in. The repo SQL filters it out via the membership
// sub-query, so the response simply omits that messageId rather than 403.
func TestM4ReadStatsBatch_NonMemberFiltered(t *testing.T) {
	env := newM4Env(t)
	cookieA, idA := env.seedUser(52)
	cookieB, idB := env.seedUser(53)
	_, idC := env.seedUser(54)

	// A and B share a DM; C is unrelated.
	dmAB := successBody(env.expect.POST("/api/channels/dm").
		WithHeader(middleware.MMCookieHeader, cookieA).
		WithJSON(map[string]any{"peer_id": idB}).
		Expect().Status(201))
	channelAB := int64(dmAB.Value("id").Number().Raw())

	// A and C share another DM; B is not a member.
	dmAC := successBody(env.expect.POST("/api/channels/dm").
		WithHeader(middleware.MMCookieHeader, cookieA).
		WithJSON(map[string]any{"peer_id": idC}).
		Expect().Status(201))
	channelAC := int64(dmAC.Value("id").Number().Raw())

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	visible := &repo.Message{ChannelID: channelAB, SenderID: idA, MsgType: repo.MsgTypeText, Content: "AB"}
	hidden := &repo.Message{ChannelID: channelAC, SenderID: idA, MsgType: repo.MsgTypeText, Content: "AC"}
	require.NoError(t, env.messages.Send(ctx, visible))
	require.NoError(t, env.messages.Send(ctx, hidden))

	// B asks for both — only the AB one comes back.
	idsParam := strconv.FormatInt(visible.ID, 10) + "," + strconv.FormatInt(hidden.ID, 10)
	resp := successBody(env.expect.GET("/api/messages/read-stats").
		WithHeader(middleware.MMCookieHeader, cookieB).
		WithQuery("ids", idsParam).
		Expect().Status(200))

	stats := resp.Value("stats").Array()
	stats.Length().IsEqual(1)
	stats.Value(0).Object().Value("messageId").Number().IsEqual(float64(visible.ID))
}

// TestM4ReadStatsBatch_BadInput — empty / malformed / oversized id lists are
// rejected with 400, never reaching the DB.
func TestM4ReadStatsBatch_BadInput(t *testing.T) {
	env := newM4Env(t)
	cookieA, _ := env.seedUser(55)

	cases := []struct {
		name string
		ids  string
	}{
		{"missing", ""},
		{"non-integer", "1,abc,3"},
		{"only commas", ",,,"},
	}
	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			env.expect.GET("/api/messages/read-stats").
				WithHeader(middleware.MMCookieHeader, cookieA).
				WithQuery("ids", tc.ids).
				Expect().Status(400)
		})
	}
}

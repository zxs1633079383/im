//go:build integration

package integration

import (
	"strconv"
	"testing"

	"im-server/internal/middleware"
	"im-server/internal/testutil"
)

// TestM4MessageSendThenSync — sender creates a DM, posts a message, then
// the peer pulls it back via /api/sync. Verifies:
//   - message persists with sender_id (TEXT) + team_id denormalised
//   - visible_to TEXT[] round-trips when set
//   - /sync returns the message in the gap-fill (server_seq=1 from cursor=0)
func TestM4MessageSendThenSync(t *testing.T) {
	env := newM4Env(t)
	cookieSender, senderID := env.seedUser(30)
	cookieRecv, recvID := env.seedUser(31)

	// Two-sided DM creation: sender posts, then peer pulls. Both must end
	// up as members of the same channel for /sync to return data.
	dm := env.expect.POST("/api/channels/dm").
		WithHeader(middleware.MMCookieHeader, cookieSender).
		WithJSON(map[string]any{"peer_id": recvID}).
		Expect().Status(201).JSON().Object()
	channelID := int64(dm.Value("id").Number().Raw())

	// Send a message; the response is the persisted repo.Message — assert on
	// the M4-shaped TEXT user-id fields directly.
	sent := env.expect.POST("/api/channels/"+strconv.FormatInt(channelID, 10)+"/messages").
		WithHeader(middleware.MMCookieHeader, cookieSender).
		WithJSON(map[string]any{
			"content":     "hello from m4",
			"msg_type":    1,
			"visible_to":  []string{senderID, recvID},
		}).
		Expect().Status(201).JSON().Object()

	sent.Value("sender_id").IsEqual(senderID)
	sent.Value("team_id").IsEqual(testutil.RealCompanyID)
	sent.Value("seq").Number().IsEqual(1)
	sent.Value("content").IsEqual("hello from m4")
	sent.Value("visible_to").Array().ContainsAll(senderID, recvID)

	// Receiver pulls via /sync from cursor=0 → must see the message.
	sync := env.expect.POST("/api/sync").
		WithHeader(middleware.MMCookieHeader, cookieRecv).
		WithJSON(map[string]any{
			"channels": []map[string]any{{"id": channelID, "seq": 0}},
		}).
		Expect().Status(200).JSON().Object()

	channels := sync.Value("channels").Array()
	channels.Length().IsEqual(1)
	first := channels.Value(0).Object()
	first.Value("id").Number().IsEqual(float64(channelID))
	first.Value("server_seq").Number().IsEqual(1)
	msgs := first.Value("messages").Array()
	msgs.Length().IsEqual(1)
	msgs.Value(0).Object().Value("sender_id").IsEqual(senderID)
}

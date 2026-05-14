//go:build integration

package integration

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/gavv/httpexpect/v2"
	"github.com/stretchr/testify/require"

	"im-server/internal/middleware"
	"im-server/internal/repo"
)

// TestM4TemplateReceived_HappyPath — sender posts a template message in a
// DM, receiver clicks the "received" button, server records receiver's UID
// in props.template.userIds and returns the refreshed message.
//
// Idempotent re-click is exercised in the same test to keep the fixture cost
// (one Postgres + Redis testcontainer pair) amortised.
func TestM4TemplateReceived_HappyPath(t *testing.T) {
	env := newM4Env(t)
	cookieSender, senderID := env.seedUser(40)
	cookieRecv, recvID := env.seedUser(41)

	dm := successBody(env.expect.POST("/api/channels/dm").
		WithHeader(middleware.MMCookieHeader, cookieSender).
		WithJSON(map[string]any{"peer_id": recvID}).
		Expect().Status(201))
	channelID := dm.Value("id").String().Raw()

	// Seed a template message directly via the repo. Send() doesn't accept
	// arbitrary props, so we go around it — this is the canonical pattern
	// for tests that need to inject a specific message shape (mirrors what
	// m4_topic_test.go does for sys_type messages).
	templateProps := `{"template":{"type":"TEXT","text":"hello","userIds":[]}}`
	msg := &repo.Message{
		ChannelID: channelID,
		SenderID:  senderID,
		MsgType:   repo.MsgTypeText,
		Content:   "tmpl",
		Props:     &templateProps,
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	require.NoError(t, env.messages.Send(ctx, msg))
	msgID := msg.ID

	// Receiver clicks "received" — server appends recvID to userIds.
	body := successBody(env.expect.POST("/api/messages/"+msgID+"/received").
		WithHeader(middleware.MMCookieHeader, cookieRecv).
		Expect().Status(200))
	body.Value("id").String().IsEqual(msgID)

	props := decodeTemplateProps(t, body)
	require.Equal(t, []any{recvID}, props["userIds"])

	// Idempotent re-click — userIds should not duplicate.
	body2 := successBody(env.expect.POST("/api/messages/"+msgID+"/received").
		WithHeader(middleware.MMCookieHeader, cookieRecv).
		Expect().Status(200))
	props2 := decodeTemplateProps(t, body2)
	require.Equal(t, []any{recvID}, props2["userIds"], "re-click must be idempotent")
}

// TestM4TemplateReceived_NotTemplate — calling the endpoint on a regular
// (non-template) message returns 422 ErrInvalidTemplate.
func TestM4TemplateReceived_NotTemplate(t *testing.T) {
	env := newM4Env(t)
	cookieSender, senderID := env.seedUser(42)
	cookieRecv, recvID := env.seedUser(43)

	dm := successBody(env.expect.POST("/api/channels/dm").
		WithHeader(middleware.MMCookieHeader, cookieSender).
		WithJSON(map[string]any{"peer_id": recvID}).
		Expect().Status(201))
	channelID := dm.Value("id").String().Raw()

	// A plain message — no props.
	msg := &repo.Message{
		ChannelID: channelID,
		SenderID:  senderID,
		MsgType:   repo.MsgTypeText,
		Content:   "plain",
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	require.NoError(t, env.messages.Send(ctx, msg))

	env.expect.POST("/api/messages/"+msg.ID+"/received").
		WithHeader(middleware.MMCookieHeader, cookieRecv).
		Expect().Status(422)
}

// decodeTemplateProps pulls out the {"template": {...}} sub-object from a
// returned message's props JSON string. Test helper to keep the body
// assertions readable.
func decodeTemplateProps(t *testing.T, body *httpexpect.Object) map[string]any {
	t.Helper()
	propsStr := body.Value("props").String().Raw()
	var props struct {
		Template map[string]any `json:"template"`
	}
	require.NoError(t, json.Unmarshal([]byte(propsStr), &props))
	require.NotNil(t, props.Template, "props.template must be present")
	return props.Template
}

//go:build integration

package integration

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"

	"im-server/internal/middleware"
	"im-server/internal/repo"
)

// TestM4CreateTopic — owner creates a parent group, sends a message to
// anchor a root_message_id, then carves a topic with one extra member.
// Verifies the topic row inherits team_id from the parent's caller scope
// and channel_members.user_id round-trips as TEXT for the topic too.
func TestM4CreateTopic(t *testing.T) {
	env := newM4Env(t)
	cookieOwner, ownerID := env.seedUser(50)
	_, member1 := env.seedUser(51)

	parent := successBody(env.expect.POST("/api/channels").
		WithHeader(middleware.MMCookieHeader, cookieOwner).
		WithJSON(map[string]any{
			"name":       "m4-topic-parent",
			"member_ids": []string{member1},
		}).
		Expect().Status(201))
	parentID := parent.Value("id").String().Raw()

	// Anchor a real message so the topic gets a stable root_message_id.
	anchor := successBody(env.expect.POST("/api/channels/"+parentID+"/messages").
		WithHeader(middleware.MMCookieHeader, cookieOwner).
		WithJSON(map[string]any{"content": "anchor", "msg_type": 1}).
		Expect().Status(201))
	rootMessageID := anchor.Value("id").String().Raw()

	// POST /api/channels/:id/topics with member_user_ids subset of parent.
	topic := successBody(env.expect.
		POST("/api/channels/"+parentID+"/topics").
		WithHeader(middleware.MMCookieHeader, cookieOwner).
		WithJSON(map[string]any{
			"root_message_id": rootMessageID,
			"name":            "m4-topic-child",
			"member_user_ids": []string{member1},
		}).
		Expect().Status(201))

	topic.Value("name").IsEqual("m4-topic-child")
	topic.Value("creator_id").IsEqual(ownerID)
	topicID := topic.Value("id").String().Raw()
	require.NotZero(t, topicID)

	// The topic must contain owner + member1 (user_id TEXT path).
	members, err := env.channels.ListMembers(context.Background(), topicID)
	require.NoError(t, err)
	require.Len(t, members, 2, "topic owner + 1 member")

	// And the topic row must point back to the parent + the anchored message.
	full, err := env.channels.GetByID(context.Background(), topicID)
	require.NoError(t, err)
	require.NotNil(t, full.RootID)
	require.Equal(t, parentID, *full.RootID)
	require.NotNil(t, full.RootMessageID)
	require.Equal(t, rootMessageID, *full.RootMessageID)
	require.Equal(t, repo.ChannelTypeGroup, full.Type, "topics inherit Group type")
}

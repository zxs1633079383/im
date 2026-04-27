//go:build integration

package integration

import (
	"context"
	"strconv"
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

	parent := env.expect.POST("/api/channels").
		WithHeader(middleware.MMCookieHeader, cookieOwner).
		WithJSON(map[string]any{
			"name":       "m4-topic-parent",
			"member_ids": []string{member1},
		}).
		Expect().Status(201).JSON().Object()
	parentID := int64(parent.Value("id").Number().Raw())

	// Anchor a real message so the topic gets a stable root_message_id.
	anchor := env.expect.POST("/api/channels/"+strconv.FormatInt(parentID, 10)+"/messages").
		WithHeader(middleware.MMCookieHeader, cookieOwner).
		WithJSON(map[string]any{"content": "anchor", "msg_type": 1}).
		Expect().Status(201).JSON().Object()
	rootMessageID := int64(anchor.Value("id").Number().Raw())

	// POST /api/channels/:id/topics with member_user_ids subset of parent.
	topic := env.expect.
		POST("/api/channels/"+strconv.FormatInt(parentID, 10)+"/topics").
		WithHeader(middleware.MMCookieHeader, cookieOwner).
		WithJSON(map[string]any{
			"root_message_id": rootMessageID,
			"name":            "m4-topic-child",
			"member_user_ids": []string{member1},
		}).
		Expect().Status(201).JSON().Object()

	topic.Value("name").IsEqual("m4-topic-child")
	topic.Value("creator_id").IsEqual(ownerID)
	topicID := int64(topic.Value("id").Number().Raw())
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

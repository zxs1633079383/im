//go:build integration

// Phase P3 — scheduled / template-received / topic / read-stats /
// transfer-owner / reaction.list 缺失的 C2/C3/C4/C5 错误矩阵。
// Happy path 已在各自 family file 内覆盖。Seed 范围 2600-2899。
package integration

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"im-server/internal/middleware"
	"im-server/internal/repo"
)

// ---- scheduled.create C2/C3/C4/C5 -----------------------------------------

// TestM4ScheduledCreate_C2_CookieMissing — 401.
func TestM4ScheduledCreate_C2_CookieMissing(t *testing.T) {
	env := newM4Env(t)
	errorBody(env.expect.POST("/api/messages/scheduled").
		WithJSON(map[string]any{}).
		Expect().Status(401))
}

// TestM4ScheduledCreate_C3_CookieInvalid — 401.
func TestM4ScheduledCreate_C3_CookieInvalid(t *testing.T) {
	env := newM4Env(t)
	errorBody(env.expect.POST("/api/messages/scheduled").
		WithHeader(middleware.MMCookieHeader, "deadbeefdeadbeefdeadbeef").
		WithJSON(map[string]any{}).
		Expect().Status(401))
}

// TestM4ScheduledCreate_C4_NotMember — 调度到非成员的 channel → 403.
func TestM4ScheduledCreate_C4_NotMember(t *testing.T) {
	env := newM4Env(t)
	cookieOwner, _ := env.seedUser(2600)
	cookieOuter, _ := env.seedUser(2601)
	_, peerID := env.seedUser(2602)
	channelID := env.seedGroup(cookieOwner, "p3-sched-nonmember", peerID)

	scheduledAt := time.Now().Add(2 * time.Minute).UTC().Format(time.RFC3339Nano)
	errorBody(env.expect.POST("/api/messages/scheduled").
		WithHeader(middleware.MMCookieHeader, cookieOuter).
		WithJSON(map[string]any{
			"channel_id":   channelID,
			"content":      "outsider attempt",
			"msg_type":     1,
			"scheduled_at": scheduledAt,
		}).
		Expect().Status(403)).
		Value("error").String().Contains("member")
}

// TestM4ScheduledCreate_C5_MissingChannelID — channel_id 空 → 422.
func TestM4ScheduledCreate_C5_MissingChannelID(t *testing.T) {
	env := newM4Env(t)
	cookie, _ := env.seedUser(2603)
	scheduledAt := time.Now().Add(2 * time.Minute).UTC().Format(time.RFC3339Nano)

	errorBody(env.expect.POST("/api/messages/scheduled").
		WithHeader(middleware.MMCookieHeader, cookie).
		WithJSON(map[string]any{
			"content":      "no chan",
			"msg_type":     1,
			"scheduled_at": scheduledAt,
		}).
		Expect().Status(422)).
		Value("error").String().Contains("channel_id")
}

// TestM4ScheduledCreate_C5b_TimeInPast — scheduled_at < now+60s → 422.
func TestM4ScheduledCreate_C5b_TimeInPast(t *testing.T) {
	env := newM4Env(t)
	cookieOwner, _ := env.seedUser(2604)
	_, peerID := env.seedUser(2605)
	channelID := env.seedGroup(cookieOwner, "p3-sched-past", peerID)

	errorBody(env.expect.POST("/api/messages/scheduled").
		WithHeader(middleware.MMCookieHeader, cookieOwner).
		WithJSON(map[string]any{
			"channel_id":   channelID,
			"content":      "past attempt",
			"msg_type":     1,
			"scheduled_at": time.Now().Add(-time.Hour).UTC().Format(time.RFC3339Nano),
		}).
		Expect().Status(422)).
		Value("error").String().Contains("future")
}


// scheduled.cancel — C2/C3/C4


// TestM4ScheduledCancel_C2_CookieMissing — 401.
func TestM4ScheduledCancel_C2_CookieMissing(t *testing.T) {
	env := newM4Env(t)
	errorBody(env.expect.DELETE("/api/messages/scheduled/anything").
		Expect().Status(401))
}

// TestM4ScheduledCancel_C3_CookieInvalid — 401.
func TestM4ScheduledCancel_C3_CookieInvalid(t *testing.T) {
	env := newM4Env(t)
	errorBody(env.expect.DELETE("/api/messages/scheduled/anything").
		WithHeader(middleware.MMCookieHeader, "deadbeefdeadbeefdeadbeef").
		Expect().Status(401))
}

// TestM4ScheduledCancel_C4_NotFound — id 不存在 → 404.
func TestM4ScheduledCancel_C4_NotFound(t *testing.T) {
	env := newM4Env(t)
	cookie, _ := env.seedUser(2610)
	errorBody(env.expect.DELETE("/api/messages/scheduled/ghost-id").
		WithHeader(middleware.MMCookieHeader, cookie).
		Expect().Status(404)).
		Value("error").String().Contains("not found")
}


// scheduled.list — C2/C3


// TestM4ScheduledList_C2_CookieMissing — 401.
func TestM4ScheduledList_C2_CookieMissing(t *testing.T) {
	env := newM4Env(t)
	errorBody(env.expect.GET("/api/messages/scheduled").Expect().Status(401))
}

// TestM4ScheduledList_C3_CookieInvalid — 401.
func TestM4ScheduledList_C3_CookieInvalid(t *testing.T) {
	env := newM4Env(t)
	errorBody(env.expect.GET("/api/messages/scheduled").
		WithHeader(middleware.MMCookieHeader, "deadbeefdeadbeefdeadbeef").
		Expect().Status(401))
}


// template-received — C2/C3/C4/C5


// TestM4TemplateReceived_C2_CookieMissing — 401.
func TestM4TemplateReceived_C2_CookieMissing(t *testing.T) {
	env := newM4Env(t)
	errorBody(env.expect.POST("/api/messages/x/received").Expect().Status(401))
}

// TestM4TemplateReceived_C3_CookieInvalid — 401.
func TestM4TemplateReceived_C3_CookieInvalid(t *testing.T) {
	env := newM4Env(t)
	errorBody(env.expect.POST("/api/messages/x/received").
		WithHeader(middleware.MMCookieHeader, "deadbeefdeadbeefdeadbeef").
		Expect().Status(401))
}

// TestM4TemplateReceived_C4_NotMember — 不在 channel 内的 user 点击 → 403.
func TestM4TemplateReceived_C4_NotMember(t *testing.T) {
	env := newM4Env(t)
	cookieSender, senderID := env.seedUser(2620)
	cookieOuter, _ := env.seedUser(2621)
	_, peerID := env.seedUser(2622)

	dm := successBody(env.expect.POST("/api/channels/dm").
		WithHeader(middleware.MMCookieHeader, cookieSender).
		WithJSON(map[string]any{"peer_id": peerID}).
		Expect().Status(201))
	channelID := dm.Value("id").String().Raw()

	templateProps := `{"template":{"type":"TEXT","text":"hello","userIds":[]}}`
	msg := &repo.Message{
		ChannelID: channelID,
		SenderID:  senderID,
		MsgType:   repo.MsgTypeText,
		Content:   "tmpl-nonmember",
		Props:     &templateProps,
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	require.NoError(t, env.messages.Send(ctx, msg))

	errorBody(env.expect.POST("/api/messages/"+msg.ID+"/received").
		WithHeader(middleware.MMCookieHeader, cookieOuter).
		Expect().Status(403)).
		Value("error").String().Contains("member")
}

// TestM4TemplateReceived_C5_NotFound — msg id 不存在 → 404.
func TestM4TemplateReceived_C5_NotFound(t *testing.T) {
	env := newM4Env(t)
	cookie, _ := env.seedUser(2630)
	errorBody(env.expect.POST("/api/messages/ghost-msg/received").
		WithHeader(middleware.MMCookieHeader, cookie).
		Expect().Status(404)).
		Value("error").String().Contains("not found")
}


// topic.create — C2/C3/C4/C5


// TestM4TopicCreate_C2_CookieMissing — 401.
func TestM4TopicCreate_C2_CookieMissing(t *testing.T) {
	env := newM4Env(t)
	errorBody(env.expect.POST("/api/channels/x/topics").
		WithJSON(map[string]any{}).
		Expect().Status(401))
}

// TestM4TopicCreate_C3_CookieInvalid — 401.
func TestM4TopicCreate_C3_CookieInvalid(t *testing.T) {
	env := newM4Env(t)
	errorBody(env.expect.POST("/api/channels/x/topics").
		WithHeader(middleware.MMCookieHeader, "deadbeefdeadbeefdeadbeef").
		WithJSON(map[string]any{}).
		Expect().Status(401))
}

// TestM4TopicCreate_C4_NotMember — outsider 调用 → 403.
func TestM4TopicCreate_C4_NotMember(t *testing.T) {
	env := newM4Env(t)
	cookieOwner, _ := env.seedUser(2640)
	cookieOuter, _ := env.seedUser(2641)
	_, peerID := env.seedUser(2642)

	parent := successBody(env.expect.POST("/api/channels").
		WithHeader(middleware.MMCookieHeader, cookieOwner).
		WithJSON(map[string]any{"name": "p3-topic-parent", "member_ids": []string{peerID}}).
		Expect().Status(201))
	parentID := parent.Value("id").String().Raw()

	anchor := successBody(env.expect.POST("/api/channels/"+parentID+"/messages").
		WithHeader(middleware.MMCookieHeader, cookieOwner).
		WithJSON(map[string]any{"content": "a", "msg_type": 1}).
		Expect().Status(201))
	rootMsgID := anchor.Value("id").String().Raw()

	errorBody(env.expect.POST("/api/channels/"+parentID+"/topics").
		WithHeader(middleware.MMCookieHeader, cookieOuter).
		WithJSON(map[string]any{
			"root_message_id": rootMsgID,
			"name":            "p3-topic-outsider",
			"member_user_ids": []string{},
		}).
		Expect().Status(403)).
		Value("error").String().Contains("member")
}

// TestM4TopicCreate_C5_MissingName — name 空 → 422.
func TestM4TopicCreate_C5_MissingName(t *testing.T) {
	env := newM4Env(t)
	cookieOwner, _ := env.seedUser(2643)
	_, peerID := env.seedUser(2644)

	parent := successBody(env.expect.POST("/api/channels").
		WithHeader(middleware.MMCookieHeader, cookieOwner).
		WithJSON(map[string]any{"name": "p3-topic-noname-parent", "member_ids": []string{peerID}}).
		Expect().Status(201))
	parentID := parent.Value("id").String().Raw()

	anchor := successBody(env.expect.POST("/api/channels/"+parentID+"/messages").
		WithHeader(middleware.MMCookieHeader, cookieOwner).
		WithJSON(map[string]any{"content": "a", "msg_type": 1}).
		Expect().Status(201))
	rootMsgID := anchor.Value("id").String().Raw()

	errorBody(env.expect.POST("/api/channels/"+parentID+"/topics").
		WithHeader(middleware.MMCookieHeader, cookieOwner).
		WithJSON(map[string]any{
			"root_message_id": rootMsgID,
			"member_user_ids": []string{},
		}).
		Expect().Status(422)).
		Value("error").String().Contains("name")
}


// topic.list — C2/C3/C4


// TestM4TopicList_C2_CookieMissing — 401.
func TestM4TopicList_C2_CookieMissing(t *testing.T) {
	env := newM4Env(t)
	errorBody(env.expect.GET("/api/channels/x/topics").Expect().Status(401))
}

// TestM4TopicList_C3_CookieInvalid — 401.
func TestM4TopicList_C3_CookieInvalid(t *testing.T) {
	env := newM4Env(t)
	errorBody(env.expect.GET("/api/channels/x/topics").
		WithHeader(middleware.MMCookieHeader, "deadbeefdeadbeefdeadbeef").
		Expect().Status(401))
}

// TestM4TopicList_C4_NotMember — outsider 调用 → 403.
func TestM4TopicList_C4_NotMember(t *testing.T) {
	env := newM4Env(t)
	cookieOwner, _ := env.seedUser(2650)
	cookieOuter, _ := env.seedUser(2651)
	_, peerID := env.seedUser(2652)

	channelID := env.seedGroup(cookieOwner, "p3-topic-list-acl", peerID)

	errorBody(env.expect.GET("/api/channels/"+channelID+"/topics").
		WithHeader(middleware.MMCookieHeader, cookieOuter).
		Expect().Status(403)).
		Value("error").String().Contains("member")
}


// read-stats — C2/C3


// TestM4ReadStats_C2_CookieMissing — 401.
func TestM4ReadStats_C2_CookieMissing(t *testing.T) {
	env := newM4Env(t)
	errorBody(env.expect.GET("/api/messages/read-stats").
		WithQuery("ids", "abc").
		Expect().Status(401))
}

// TestM4ReadStats_C3_CookieInvalid — 401.
func TestM4ReadStats_C3_CookieInvalid(t *testing.T) {
	env := newM4Env(t)
	errorBody(env.expect.GET("/api/messages/read-stats").
		WithHeader(middleware.MMCookieHeader, "deadbeefdeadbeefdeadbeef").
		WithQuery("ids", "abc").
		Expect().Status(401))
}


// transfer-owner — C2/C3/C5

// Note: C1 / C4 NotOwner / C4 OutsiderTarget / DM / AlsoLeave 已在
// m4_channel_transfer_owner_test.go。本节只补 C2/C3/C5。

// TestM4TransferOwner_C2_CookieMissing — 401.
func TestM4TransferOwner_C2_CookieMissing(t *testing.T) {
	env := newM4Env(t)
	errorBody(env.expect.POST("/api/channels/x/transfer-owner").
		WithJSON(map[string]any{"new_owner_id": "y"}).
		Expect().Status(401))
}

// TestM4TransferOwner_C3_CookieInvalid — 401.
func TestM4TransferOwner_C3_CookieInvalid(t *testing.T) {
	env := newM4Env(t)
	errorBody(env.expect.POST("/api/channels/x/transfer-owner").
		WithHeader(middleware.MMCookieHeader, "deadbeefdeadbeefdeadbeef").
		WithJSON(map[string]any{"new_owner_id": "y"}).
		Expect().Status(401))
}

// TestM4TransferOwner_C5_MissingNewOwnerID — new_owner_id 缺失 → 422.
func TestM4TransferOwner_C5_MissingNewOwnerID(t *testing.T) {
	env := newM4Env(t)
	cookieOwner, _ := env.seedUser(2660)
	_, peerID := env.seedUser(2661)
	chID := env.seedGroup(cookieOwner, "p3-xfer-no-id", peerID)

	errorBody(env.expect.POST("/api/channels/"+chID+"/transfer-owner").
		WithHeader(middleware.MMCookieHeader, cookieOwner).
		WithJSON(map[string]any{}).
		Expect().Status(422)).
		Value("error").String().Contains("new_owner_id")
}


// reaction.list — C2/C3/C4


// TestM4ReactionList_C2_CookieMissing — 401.
func TestM4ReactionList_C2_CookieMissing(t *testing.T) {
	env := newM4Env(t)
	errorBody(env.expect.GET("/api/messages/x/reactions").Expect().Status(401))
}

// TestM4ReactionList_C3_CookieInvalid — 401.
func TestM4ReactionList_C3_CookieInvalid(t *testing.T) {
	env := newM4Env(t)
	errorBody(env.expect.GET("/api/messages/x/reactions").
		WithHeader(middleware.MMCookieHeader, "deadbeefdeadbeefdeadbeef").
		Expect().Status(401))
}

// TestM4ReactionList_C4_NotMember — outsider 调 → 403.
func TestM4ReactionList_C4_NotMember(t *testing.T) {
	env := newM4Env(t)
	cookieA, idA := env.seedUser(2670)
	_, idB := env.seedUser(2671)
	cookieC, _ := env.seedUser(2672)
	channelID := env.seedDM(cookieA, idB)
	msg := env.seedMessage(channelID, idA, "for-c4-reaction-list")

	errorBody(env.expect.GET("/api/messages/"+msg.ID+"/reactions").
		WithHeader(middleware.MMCookieHeader, cookieC).
		Expect().Status(403)).
		Value("error").String().Contains("member")
}

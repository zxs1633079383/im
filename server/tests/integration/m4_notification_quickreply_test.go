//go:build integration

// Package integration — Batch-C 简化版：notification (4 路由) + quick_reply
// (4 路由) 共 8 个 happy-path 集成测试。
//
// 范围：每个 endpoint 一条 HappyPath（不做 5-case 矩阵，失败路径已由 handler
// 单测 + service 单测覆盖；这一组测试只验证「路由→service→repo→envelope」
// 全链路通顺）。
//
// 路由注册：buildEngine（m4_harness_test.go）已统一装配 notification +
// quick_reply。测试只起 newM4Env(t) 然后直接打 endpoint。
//
// Seed 范围：700–799 — C3 worker 专属，避免与 C1/C2 (500-699) 撞库。
package integration

import (
	"testing"

	"im-server/internal/middleware"
	"im-server/internal/repo"
)

// ---------------------------------------------------------------------------
// notification.go — 4 happy paths
// ---------------------------------------------------------------------------

// TestM4NotificationPost_HappyPath — 发送方 POST /api/notifications，
// service 写一行 → 201 + envelope.data.id > 0 + receiver_id / title 字段回显。
func TestM4NotificationPost_HappyPath(t *testing.T) {
	env := newM4Env(t)
	cookieSender, _ := env.seedUser(700)
	_, recvID := env.seedUser(701)

	data := successBody(env.expect.POST("/api/notifications").
		WithHeader(middleware.MMCookieHeader, cookieSender).
		WithJSON(map[string]any{
			"receiver_id": recvID,
			"title":       "hello",
			"body":        "world",
			"type":        repo.NotificationTypeGeneric,
		}).
		Expect().Status(201))

	data.Value("id").String().NotEmpty()
	data.Value("receiver_id").String().IsEqual(recvID)
	data.Value("title").String().IsEqual("hello")
}

// TestM4NotificationRead_HappyPath — 收件人 POST /:id/read 把通知标记为已读
// → 200 + envelope.data.status == "read"。
func TestM4NotificationRead_HappyPath(t *testing.T) {
	env := newM4Env(t)
	cookieSender, _ := env.seedUser(702)
	cookieRecv, recvID := env.seedUser(703)

	created := successBody(env.expect.POST("/api/notifications").
		WithHeader(middleware.MMCookieHeader, cookieSender).
		WithJSON(map[string]any{
			"receiver_id": recvID,
			"title":       "ping",
			"type":        repo.NotificationTypeGeneric,
		}).
		Expect().Status(201))
	notifID := created.Value("id").String().Raw()

	read := successBody(env.expect.POST("/api/notifications/"+notifID+"/read").
		WithHeader(middleware.MMCookieHeader, cookieRecv).
		Expect().Status(200))
	read.Value("status").String().IsEqual("read")
}

// TestM4NotificationListSent_HappyPath — 发送方先发一条，再 GET /sent
// → 200 + envelope.data.notifications 至少 1 条。
func TestM4NotificationListSent_HappyPath(t *testing.T) {
	env := newM4Env(t)
	cookieSender, _ := env.seedUser(704)
	_, recvID := env.seedUser(705)

	env.expect.POST("/api/notifications").
		WithHeader(middleware.MMCookieHeader, cookieSender).
		WithJSON(map[string]any{
			"receiver_id": recvID,
			"title":       "outbox-1",
			"type":        repo.NotificationTypeGeneric,
		}).
		Expect().Status(201)

	data := successBody(env.expect.GET("/api/notifications/sent").
		WithHeader(middleware.MMCookieHeader, cookieSender).
		Expect().Status(200))
	data.Value("notifications").Array().Length().Gt(0)
}

// TestM4NotificationListReceived_HappyPath — 收件人 GET /received
// → 200 + envelope.data.notifications 至少 1 条。
func TestM4NotificationListReceived_HappyPath(t *testing.T) {
	env := newM4Env(t)
	cookieSender, _ := env.seedUser(706)
	cookieRecv, recvID := env.seedUser(707)

	env.expect.POST("/api/notifications").
		WithHeader(middleware.MMCookieHeader, cookieSender).
		WithJSON(map[string]any{
			"receiver_id": recvID,
			"title":       "inbox-1",
			"type":        repo.NotificationTypeGeneric,
		}).
		Expect().Status(201)

	data := successBody(env.expect.GET("/api/notifications/received").
		WithHeader(middleware.MMCookieHeader, cookieRecv).
		Expect().Status(200))
	data.Value("notifications").Array().Length().Gt(0)
}

// ---------------------------------------------------------------------------
// quick_reply.go — 4 happy paths
// ---------------------------------------------------------------------------

// TestM4QuickReplyCreate_HappyPath — 创建一条预设
// → 201 + envelope.data.id > 0 + label / content 字段回显。
func TestM4QuickReplyCreate_HappyPath(t *testing.T) {
	env := newM4Env(t)
	cookie, _ := env.seedUser(710)

	data := successBody(env.expect.POST("/api/quick-replies").
		WithHeader(middleware.MMCookieHeader, cookie).
		WithJSON(map[string]any{
			"label":      "hi",
			"content":    "hello!",
			"sort_order": 0,
		}).
		Expect().Status(201))

	data.Value("id").String().NotEmpty()
	data.Value("label").String().IsEqual("hi")
	data.Value("content").String().IsEqual("hello!")
}

// TestM4QuickReplyList_HappyPath — 先创建一条 → GET /quick-replies
// → 200 + envelope.data.quick_replies 至少 1 条。
func TestM4QuickReplyList_HappyPath(t *testing.T) {
	env := newM4Env(t)
	cookie, _ := env.seedUser(720)

	env.expect.POST("/api/quick-replies").
		WithHeader(middleware.MMCookieHeader, cookie).
		WithJSON(map[string]any{"label": "l", "content": "c"}).
		Expect().Status(201)

	data := successBody(env.expect.GET("/api/quick-replies").
		WithHeader(middleware.MMCookieHeader, cookie).
		Expect().Status(200))
	data.Value("quick_replies").Array().Length().Gt(0)
}

// TestM4QuickReplyUpdate_HappyPath — PATCH 改 label
// → 200 + envelope.data.label == 新值。
func TestM4QuickReplyUpdate_HappyPath(t *testing.T) {
	env := newM4Env(t)
	cookie, _ := env.seedUser(730)

	created := successBody(env.expect.POST("/api/quick-replies").
		WithHeader(middleware.MMCookieHeader, cookie).
		WithJSON(map[string]any{"label": "old", "content": "c"}).
		Expect().Status(201))
	id := created.Value("id").String().Raw()

	data := successBody(env.expect.PATCH("/api/quick-replies/"+id).
		WithHeader(middleware.MMCookieHeader, cookie).
		WithJSON(map[string]any{"label": "new"}).
		Expect().Status(200))
	data.Value("label").String().IsEqual("new")
	data.Value("id").String().IsEqual(id)
}

// TestM4QuickReplyDelete_HappyPath — 创建 → DELETE
// → 200 + envelope.data.status == "deleted"。
func TestM4QuickReplyDelete_HappyPath(t *testing.T) {
	env := newM4Env(t)
	cookie, _ := env.seedUser(740)

	created := successBody(env.expect.POST("/api/quick-replies").
		WithHeader(middleware.MMCookieHeader, cookie).
		WithJSON(map[string]any{"label": "del", "content": "c"}).
		Expect().Status(201))
	id := created.Value("id").String().Raw()

	data := successBody(env.expect.DELETE("/api/quick-replies/"+id).
		WithHeader(middleware.MMCookieHeader, cookie).
		Expect().Status(200))
	data.Value("status").String().IsEqual("deleted")
}

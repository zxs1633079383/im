//go:build integration

package integration

import (
	"fmt"
	"testing"
)

// TestM2_QuickReplyCRUD: create → list → update → delete round-trip for one user.
func TestM2_QuickReplyCRUD(t *testing.T) {
	env := newV5Env(t)
	_, aliceTok := env.CreateUserAndToken("m2qr_alice", "m2qr_a@x.com")

	// Empty list.
	env.httpExpect.GET("/api/quick-replies").
		WithHeader("Authorization", bearer(aliceTok)).
		Expect().Status(200).JSON().Object().
		Value("quick_replies").Array().Length().IsEqual(0)

	// Create two.
	obj := env.httpExpect.POST("/api/quick-replies").
		WithHeader("Authorization", bearer(aliceTok)).
		WithJSON(map[string]any{
			"label":      "greeting",
			"content":    "hi there",
			"sort_order": 1,
		}).
		Expect().Status(201).JSON().Object()
	obj.Value("label").String().IsEqual("greeting")
	id1 := int64(obj.Value("id").Number().Raw())

	env.httpExpect.POST("/api/quick-replies").
		WithHeader("Authorization", bearer(aliceTok)).
		WithJSON(map[string]any{
			"label":      "bye",
			"content":    "see you",
			"sort_order": 2,
		}).
		Expect().Status(201)

	// List ordered by sort_order.
	ls := env.httpExpect.GET("/api/quick-replies").
		WithHeader("Authorization", bearer(aliceTok)).
		Expect().Status(200).JSON().Object()
	arr := ls.Value("quick_replies").Array()
	arr.Length().IsEqual(2)
	arr.Value(0).Object().Value("label").String().IsEqual("greeting")
	arr.Value(1).Object().Value("label").String().IsEqual("bye")

	// Update.
	updated := env.httpExpect.PATCH(fmt.Sprintf("/api/quick-replies/%d", id1)).
		WithHeader("Authorization", bearer(aliceTok)).
		WithJSON(map[string]any{
			"content": "hello!",
		}).
		Expect().Status(200).JSON().Object()
	updated.Value("content").String().IsEqual("hello!")
	updated.Value("label").String().IsEqual("greeting") // unchanged

	// Delete.
	env.httpExpect.DELETE(fmt.Sprintf("/api/quick-replies/%d", id1)).
		WithHeader("Authorization", bearer(aliceTok)).
		Expect().Status(200)

	env.httpExpect.GET("/api/quick-replies").
		WithHeader("Authorization", bearer(aliceTok)).
		Expect().Status(200).JSON().Object().
		Value("quick_replies").Array().Length().IsEqual(1)
}

// TestM2_QuickReplyIsolation: bob can't see alice's presets and can't mutate
// them.
func TestM2_QuickReplyIsolation(t *testing.T) {
	env := newV5Env(t)
	_, aliceTok := env.CreateUserAndToken("m2qri_alice", "m2qri_a@x.com")
	_, bobTok := env.CreateUserAndToken("m2qri_bob", "m2qri_b@x.com")

	// Alice creates one.
	obj := env.httpExpect.POST("/api/quick-replies").
		WithHeader("Authorization", bearer(aliceTok)).
		WithJSON(map[string]any{
			"label":   "private",
			"content": "secret",
		}).
		Expect().Status(201).JSON().Object()
	aliceID := int64(obj.Value("id").Number().Raw())

	// Bob's list is empty.
	env.httpExpect.GET("/api/quick-replies").
		WithHeader("Authorization", bearer(bobTok)).
		Expect().Status(200).JSON().Object().
		Value("quick_replies").Array().Length().IsEqual(0)

	// Bob tries to update → 403.
	env.httpExpect.PATCH(fmt.Sprintf("/api/quick-replies/%d", aliceID)).
		WithHeader("Authorization", bearer(bobTok)).
		WithJSON(map[string]any{"content": "hacked"}).
		Expect().Status(403)

	// Bob tries to delete → 403.
	env.httpExpect.DELETE(fmt.Sprintf("/api/quick-replies/%d", aliceID)).
		WithHeader("Authorization", bearer(bobTok)).
		Expect().Status(403)

	// Alice still sees her reply untouched.
	env.httpExpect.GET("/api/quick-replies").
		WithHeader("Authorization", bearer(aliceTok)).
		Expect().Status(200).JSON().Object().
		Value("quick_replies").Array().Value(0).Object().
		Value("content").String().IsEqual("secret")
}

// TestM2_QuickReplyValidation: empty label/content → 422.
func TestM2_QuickReplyValidation(t *testing.T) {
	env := newV5Env(t)
	_, tok := env.CreateUserAndToken("m2qrv", "m2qrv@x.com")

	env.httpExpect.POST("/api/quick-replies").
		WithHeader("Authorization", bearer(tok)).
		WithJSON(map[string]any{"content": "c"}).
		Expect().Status(422)

	env.httpExpect.POST("/api/quick-replies").
		WithHeader("Authorization", bearer(tok)).
		WithJSON(map[string]any{"label": "l"}).
		Expect().Status(422)
}

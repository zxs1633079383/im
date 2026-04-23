//go:build integration

// Package integration — M2-A channel governance scenarios.
//
// Each TestM2_* validates one semantic of the fine-grained channel
// endpoints added in the M2 round (PATCH /api/channels/:id, managers,
// pins, member role/notify_pref). The harness seeds one Postgres
// testcontainer per subtest via newV5Env (see v5_harness_test.go), so
// tests can run -parallel without cross-talk.
package integration

import (
	"fmt"
	"strconv"
	"testing"
)

// TestM2_PatchChannelFields verifies each new fine-grained field round-trips
// through PATCH → GET.
func TestM2_PatchChannelFields(t *testing.T) {
	env := newV5Env(t)
	_, aliceTok := env.CreateUserAndToken("m2a_alice", "m2a_a@x.com")

	chID := env.CreateGroup(aliceTok, "m2a channel")

	// PATCH the new fields.
	env.httpExpect.PATCH("/api/channels/"+strconv.FormatInt(chID, 10)).
		WithHeader("Authorization", bearer(aliceTok)).
		WithJSON(map[string]any{
			"notice":      "hello all",
			"purpose":     "team sync",
			"picture_url": "https://example.com/p.png",
			"props":       map[string]any{"color": "blue"},
			"orient":      1,
			"permission":  1,
			"is_top":      true,
		}).
		Expect().Status(200)

	// Fetch and assert.
	obj := env.httpExpect.GET("/api/channels/"+strconv.FormatInt(chID, 10)).
		WithHeader("Authorization", bearer(aliceTok)).
		Expect().Status(200).JSON().Object()
	obj.Value("notice").String().IsEqual("hello all")
	obj.Value("purpose").String().IsEqual("team sync")
	obj.Value("picture_url").String().IsEqual("https://example.com/p.png")
	obj.Value("orient").Number().IsEqual(1)
	obj.Value("permission").Number().IsEqual(1)
	obj.Value("is_top").Boolean().IsEqual(true)
}

// TestM2_ManagerPermissions covers the owner-only gating of manager
// add/remove, and that non-owners get 403.
func TestM2_ManagerPermissions(t *testing.T) {
	env := newV5Env(t)
	_, aliceTok := env.CreateUserAndToken("m2mp_alice", "m2mp_a@x.com")
	bobID, bobTok := env.CreateUserAndToken("m2mp_bob", "m2mp_b@x.com")
	carolID, _ := env.CreateUserAndToken("m2mp_carol", "m2mp_c@x.com")

	chID := env.CreateGroup(aliceTok, "m2 mgr", bobID, carolID)

	// Owner (alice) adds bob as manager.
	env.httpExpect.POST(fmt.Sprintf("/api/channels/%d/managers/%d", chID, bobID)).
		WithHeader("Authorization", bearer(aliceTok)).
		Expect().Status(201)

	// List managers — bob should appear.
	obj := env.httpExpect.GET(fmt.Sprintf("/api/channels/%d/managers", chID)).
		WithHeader("Authorization", bearer(aliceTok)).
		Expect().Status(200).JSON().Object()
	obj.Value("managers").Array().Length().IsEqual(1)

	// Non-owner (bob) tries to add carol — must 403.
	env.httpExpect.POST(fmt.Sprintf("/api/channels/%d/managers/%d", chID, carolID)).
		WithHeader("Authorization", bearer(bobTok)).
		Expect().Status(403)

	// Non-owner (bob) tries to remove himself — must 403 (owner-only).
	env.httpExpect.DELETE(fmt.Sprintf("/api/channels/%d/managers/%d", chID, bobID)).
		WithHeader("Authorization", bearer(bobTok)).
		Expect().Status(403)

	// Owner removes bob.
	env.httpExpect.DELETE(fmt.Sprintf("/api/channels/%d/managers/%d", chID, bobID)).
		WithHeader("Authorization", bearer(aliceTok)).
		Expect().Status(200)

	// Empty list.
	obj = env.httpExpect.GET(fmt.Sprintf("/api/channels/%d/managers", chID)).
		WithHeader("Authorization", bearer(aliceTok)).
		Expect().Status(200).JSON().Object()
	obj.Value("managers").Array().Length().IsEqual(0)
}

// TestM2_PinUnpinMessage: manager/owner can pin/unpin; regular member cannot.
func TestM2_PinUnpinMessage(t *testing.T) {
	env := newV5Env(t)
	_, aliceTok := env.CreateUserAndToken("m2pin_alice", "m2pin_a@x.com")
	bobID, bobTok := env.CreateUserAndToken("m2pin_bob", "m2pin_b@x.com")

	chID := env.CreateGroup(aliceTok, "m2 pin", bobID)
	msgID := env.MustSendAndReturnMsgID(aliceTok, chID, "pin me", "m2pin-1")

	// Owner pins.
	env.httpExpect.POST(fmt.Sprintf("/api/channels/%d/pins/%d", chID, msgID)).
		WithHeader("Authorization", bearer(aliceTok)).
		Expect().Status(201)

	// Any member can list pins.
	obj := env.httpExpect.GET(fmt.Sprintf("/api/channels/%d/pins", chID)).
		WithHeader("Authorization", bearer(bobTok)).
		Expect().Status(200).JSON().Object()
	obj.Value("pins").Array().Length().IsEqual(1)

	// Non-manager bob tries to pin another message — 403.
	msgID2 := env.MustSendAndReturnMsgID(aliceTok, chID, "another", "m2pin-2")
	env.httpExpect.POST(fmt.Sprintf("/api/channels/%d/pins/%d", chID, msgID2)).
		WithHeader("Authorization", bearer(bobTok)).
		Expect().Status(403)

	// Owner unpins.
	env.httpExpect.DELETE(fmt.Sprintf("/api/channels/%d/pins/%d", chID, msgID)).
		WithHeader("Authorization", bearer(aliceTok)).
		Expect().Status(200)

	obj = env.httpExpect.GET(fmt.Sprintf("/api/channels/%d/pins", chID)).
		WithHeader("Authorization", bearer(aliceTok)).
		Expect().Status(200).JSON().Object()
	obj.Value("pins").Array().Length().IsEqual(0)
}

// TestM2_UpdateMemberRole: owner can update a member's role.
func TestM2_UpdateMemberRole(t *testing.T) {
	env := newV5Env(t)
	_, aliceTok := env.CreateUserAndToken("m2role_alice", "m2role_a@x.com")
	bobID, bobTok := env.CreateUserAndToken("m2role_bob", "m2role_b@x.com")

	chID := env.CreateGroup(aliceTok, "m2 role", bobID)

	// Non-owner can't bump role.
	env.httpExpect.PATCH(fmt.Sprintf("/api/channels/%d/members/%d", chID, bobID)).
		WithHeader("Authorization", bearer(bobTok)).
		WithJSON(map[string]any{"role": 2}).
		Expect().Status(403)

	// Owner bumps bob to Admin (2).
	env.httpExpect.PATCH(fmt.Sprintf("/api/channels/%d/members/%d", chID, bobID)).
		WithHeader("Authorization", bearer(aliceTok)).
		WithJSON(map[string]any{"role": 2}).
		Expect().Status(200)

	// Members list shows bob with role=2.
	members := env.httpExpect.GET(fmt.Sprintf("/api/channels/%d/members", chID)).
		WithHeader("Authorization", bearer(aliceTok)).
		Expect().Status(200).JSON().Array()
	found := false
	for i := 0; i < int(members.Length().Raw()); i++ {
		m := members.Value(i).Object()
		uid := int64(m.Value("user_id").Number().Raw())
		if uid == bobID {
			m.Value("role").Number().IsEqual(2)
			found = true
		}
	}
	if !found {
		t.Fatalf("bob not found in members list")
	}
}

// TestM2_UpdateMemberNotifyPref: caller updates own notify_pref; cross-user
// update is forbidden; out-of-range value is 422.
func TestM2_UpdateMemberNotifyPref(t *testing.T) {
	env := newV5Env(t)
	_, aliceTok := env.CreateUserAndToken("m2np_alice", "m2np_a@x.com")
	bobID, bobTok := env.CreateUserAndToken("m2np_bob", "m2np_b@x.com")

	chID := env.CreateGroup(aliceTok, "m2 notify", bobID)

	// Bob updates his own pref to "mentions" (1).
	env.httpExpect.PATCH(fmt.Sprintf("/api/channels/%d/members/%d", chID, bobID)).
		WithHeader("Authorization", bearer(bobTok)).
		WithJSON(map[string]any{"notify_pref": 1}).
		Expect().Status(200)

	// Alice tries to update bob's pref — 403.
	env.httpExpect.PATCH(fmt.Sprintf("/api/channels/%d/members/%d", chID, bobID)).
		WithHeader("Authorization", bearer(aliceTok)).
		WithJSON(map[string]any{"notify_pref": 2}).
		Expect().Status(403)

	// Out-of-range pref — 422.
	env.httpExpect.PATCH(fmt.Sprintf("/api/channels/%d/members/%d", chID, bobID)).
		WithHeader("Authorization", bearer(bobTok)).
		WithJSON(map[string]any{"notify_pref": 42}).
		Expect().Status(422)
}

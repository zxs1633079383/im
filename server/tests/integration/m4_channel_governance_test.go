//go:build integration

// Batch-B G3 — channel governance integration tests.
//
// Covers the 8 endpoints registered by RegisterChannelGovernanceRoutes:
//
//   PATCH  /api/channels/:id
//   POST   /api/channels/:id/managers/:user_id
//   DELETE /api/channels/:id/managers/:user_id
//   GET    /api/channels/:id/managers
//   POST   /api/channels/:id/pins/:message_id
//   DELETE /api/channels/:id/pins/:message_id
//   GET    /api/channels/:id/pins
//   PATCH  /api/channels/:id/members/:user_id
//
// Each endpoint has 5 cases (C1..C5):
//   C1 HappyPath     — owner/manager/member with proper input → 2xx
//   C2 CookieMissing — no MMCookieHeader stamped               → 401
//   C3 CookieInvalid — cookieId not in Redis                   → 401
//   C4 Forbidden     — wrong role (member vs owner-only, etc.) → 403
//   C5 BadRequest    — bad payload / non-numeric path / etc.   → 400/422
//
// Seed range 300-379 is reserved for G3; do not collide with other Batch-B
// authors.
package integration

import (
	"strconv"
	"testing"

	"im-server/internal/middleware"
)

// ---- helpers ----------------------------------------------------------------

// pathInt64s is sugar for the noisy strconv.FormatInt that shows up in every
// path-parametrised assertion below. Keeps the test bodies readable.
func pathInt64s(v int64) string { return strconv.FormatInt(v, 10) }

// ============================================================================
// PATCH /api/channels/:id  — fine-grained channel patch (manager+ required)
// ============================================================================

// TestM4ChannelPatch_C1_HappyPath — owner patches name + notice and gets the
// refreshed channel back with the new fields.
func TestM4ChannelPatch_C1_HappyPath(t *testing.T) {
	env := newM4Env(t)
	cookieOwner, _ := env.seedUser(300)
	_, m1 := env.seedUser(301)
	chID := env.seedGroup(cookieOwner, "g3-patch-happy", m1)

	body := env.expect.PATCH("/api/channels/" + pathInt64s(chID)).
		WithHeader(middleware.MMCookieHeader, cookieOwner).
		WithJSON(map[string]any{
			"name":   "g3-patch-renamed",
			"notice": "be nice",
		}).
		Expect().Status(200).JSON().Object()
	body.Value("name").IsEqual("g3-patch-renamed")
	body.Value("notice").IsEqual("be nice")
}

// TestM4ChannelPatch_C2_CookieMissing — no header → 401.
func TestM4ChannelPatch_C2_CookieMissing(t *testing.T) {
	env := newM4Env(t)
	cookieOwner, _ := env.seedUser(302)
	_, m1 := env.seedUser(303)
	chID := env.seedGroup(cookieOwner, "g3-patch-no-cookie", m1)

	env.expect.PATCH("/api/channels/" + pathInt64s(chID)).
		WithJSON(map[string]any{"name": "x"}).
		Expect().Status(401)
}

// TestM4ChannelPatch_C3_CookieInvalid — cookie not in Redis → 401.
func TestM4ChannelPatch_C3_CookieInvalid(t *testing.T) {
	env := newM4Env(t)
	cookieOwner, _ := env.seedUser(304)
	_, m1 := env.seedUser(305)
	chID := env.seedGroup(cookieOwner, "g3-patch-bad-cookie", m1)

	env.expect.PATCH("/api/channels/" + pathInt64s(chID)).
		WithHeader(middleware.MMCookieHeader, "deadbeefdeadbeefdeadbeef").
		WithJSON(map[string]any{"name": "x"}).
		Expect().Status(401)
}

// TestM4ChannelPatch_C4_Forbidden — plain member (not manager/owner) PATCHes
// the channel → 403 (manager or owner required).
func TestM4ChannelPatch_C4_Forbidden(t *testing.T) {
	env := newM4Env(t)
	cookieOwner, _ := env.seedUser(306)
	cookieMember, m1 := env.seedUser(307)
	chID := env.seedGroup(cookieOwner, "g3-patch-forbid", m1)

	env.expect.PATCH("/api/channels/" + pathInt64s(chID)).
		WithHeader(middleware.MMCookieHeader, cookieMember).
		WithJSON(map[string]any{"name": "blocked"}).
		Expect().Status(403)
}

// TestM4ChannelPatch_C5_BadRequest — malformed JSON body → 400.
func TestM4ChannelPatch_C5_BadRequest(t *testing.T) {
	env := newM4Env(t)
	cookieOwner, _ := env.seedUser(308)
	_, m1 := env.seedUser(309)
	chID := env.seedGroup(cookieOwner, "g3-patch-bad-body", m1)

	env.expect.PATCH("/api/channels/" + pathInt64s(chID)).
		WithHeader(middleware.MMCookieHeader, cookieOwner).
		WithText("{not json").
		WithHeader("Content-Type", "application/json").
		Expect().Status(400)
}

// ============================================================================
// POST /api/channels/:id/managers/:user_id  — owner-only add manager
// ============================================================================

// TestM4ChannelAddManager_C1_HappyPath — owner promotes a member, returns 201
// + status:manager_added, and ListManagers reflects it.
func TestM4ChannelAddManager_C1_HappyPath(t *testing.T) {
	env := newM4Env(t)
	cookieOwner, _ := env.seedUser(310)
	_, m1 := env.seedUser(311)
	chID := env.seedGroup(cookieOwner, "g3-add-mgr-happy", m1)

	env.expect.POST("/api/channels/" + pathInt64s(chID) + "/managers/" + m1).
		WithHeader(middleware.MMCookieHeader, cookieOwner).
		Expect().Status(201).
		JSON().Object().Value("status").IsEqual("manager_added")

	mgrs := env.expect.GET("/api/channels/" + pathInt64s(chID) + "/managers").
		WithHeader(middleware.MMCookieHeader, cookieOwner).
		Expect().Status(200).JSON().Object()
	mgrs.Value("managers").Array().ContainsAll(m1)
}

// TestM4ChannelAddManager_C2_CookieMissing — no header → 401.
func TestM4ChannelAddManager_C2_CookieMissing(t *testing.T) {
	env := newM4Env(t)
	cookieOwner, _ := env.seedUser(312)
	_, m1 := env.seedUser(313)
	chID := env.seedGroup(cookieOwner, "g3-add-mgr-no-cookie", m1)

	env.expect.POST("/api/channels/" + pathInt64s(chID) + "/managers/" + m1).
		Expect().Status(401)
}

// TestM4ChannelAddManager_C3_CookieInvalid — cookie not in Redis → 401.
func TestM4ChannelAddManager_C3_CookieInvalid(t *testing.T) {
	env := newM4Env(t)
	cookieOwner, _ := env.seedUser(314)
	_, m1 := env.seedUser(315)
	chID := env.seedGroup(cookieOwner, "g3-add-mgr-bad-cookie", m1)

	env.expect.POST("/api/channels/" + pathInt64s(chID) + "/managers/" + m1).
		WithHeader(middleware.MMCookieHeader, "cafebabecafebabecafebabe").
		Expect().Status(401)
}

// TestM4ChannelAddManager_C4_NotOwner — caller is a plain member (not owner)
// and tries to promote another member → 403.
func TestM4ChannelAddManager_C4_NotOwner(t *testing.T) {
	env := newM4Env(t)
	cookieOwner, _ := env.seedUser(316)
	cookieMember, m1 := env.seedUser(317)
	_, m2 := env.seedUser(318)
	chID := env.seedGroup(cookieOwner, "g3-add-mgr-not-owner", m1, m2)

	env.expect.POST("/api/channels/" + pathInt64s(chID) + "/managers/" + m2).
		WithHeader(middleware.MMCookieHeader, cookieMember).
		Expect().Status(403)
}

// TestM4ChannelAddManager_C5_TargetNotMember — owner tries to promote a user
// who is NOT a member of the channel → 422 ErrTargetNotMember.
func TestM4ChannelAddManager_C5_TargetNotMember(t *testing.T) {
	env := newM4Env(t)
	cookieOwner, _ := env.seedUser(319)
	_, peer := env.seedUser(361) // membership-free outsider, distinct from m1
	chID := env.seedGroup(cookieOwner, "g3-add-mgr-not-member")

	env.expect.POST("/api/channels/" + pathInt64s(chID) + "/managers/" + peer).
		WithHeader(middleware.MMCookieHeader, cookieOwner).
		Expect().Status(422)
}

// ============================================================================
// DELETE /api/channels/:id/managers/:user_id  — owner-only remove manager
// ============================================================================

// TestM4ChannelRemoveManager_C1_HappyPath — promote then remove; status 200
// + status:manager_removed; subsequent ListManagers no longer contains them.
func TestM4ChannelRemoveManager_C1_HappyPath(t *testing.T) {
	env := newM4Env(t)
	cookieOwner, _ := env.seedUser(320)
	_, m1 := env.seedUser(321)
	chID := env.seedGroup(cookieOwner, "g3-rm-mgr-happy", m1)

	// Promote first.
	env.expect.POST("/api/channels/" + pathInt64s(chID) + "/managers/" + m1).
		WithHeader(middleware.MMCookieHeader, cookieOwner).
		Expect().Status(201)

	env.expect.DELETE("/api/channels/" + pathInt64s(chID) + "/managers/" + m1).
		WithHeader(middleware.MMCookieHeader, cookieOwner).
		Expect().Status(200).
		JSON().Object().Value("status").IsEqual("manager_removed")

	mgrs := env.expect.GET("/api/channels/" + pathInt64s(chID) + "/managers").
		WithHeader(middleware.MMCookieHeader, cookieOwner).
		Expect().Status(200).JSON().Object()
	mgrs.Value("managers").Array().NotContainsAny(m1)
}

// TestM4ChannelRemoveManager_C2_CookieMissing — no header → 401.
func TestM4ChannelRemoveManager_C2_CookieMissing(t *testing.T) {
	env := newM4Env(t)
	cookieOwner, _ := env.seedUser(322)
	_, m1 := env.seedUser(323)
	chID := env.seedGroup(cookieOwner, "g3-rm-mgr-no-cookie", m1)

	env.expect.DELETE("/api/channels/" + pathInt64s(chID) + "/managers/" + m1).
		Expect().Status(401)
}

// TestM4ChannelRemoveManager_C3_CookieInvalid — bad cookie → 401.
func TestM4ChannelRemoveManager_C3_CookieInvalid(t *testing.T) {
	env := newM4Env(t)
	cookieOwner, _ := env.seedUser(324)
	_, m1 := env.seedUser(325)
	chID := env.seedGroup(cookieOwner, "g3-rm-mgr-bad-cookie", m1)

	env.expect.DELETE("/api/channels/" + pathInt64s(chID) + "/managers/" + m1).
		WithHeader(middleware.MMCookieHeader, "0badc0de0badc0de0badc0de").
		Expect().Status(401)
}

// TestM4ChannelRemoveManager_C4_NotOwner — plain member tries to demote a
// manager → 403 (owner required).
func TestM4ChannelRemoveManager_C4_NotOwner(t *testing.T) {
	env := newM4Env(t)
	cookieOwner, _ := env.seedUser(326)
	cookieMember, m1 := env.seedUser(327)
	_, m2 := env.seedUser(328)
	chID := env.seedGroup(cookieOwner, "g3-rm-mgr-not-owner", m1, m2)

	// Owner promotes m2 so there's something to try to remove.
	env.expect.POST("/api/channels/" + pathInt64s(chID) + "/managers/" + m2).
		WithHeader(middleware.MMCookieHeader, cookieOwner).
		Expect().Status(201)

	env.expect.DELETE("/api/channels/" + pathInt64s(chID) + "/managers/" + m2).
		WithHeader(middleware.MMCookieHeader, cookieMember).
		Expect().Status(403)
}

// TestM4ChannelRemoveManager_C5_BadRequest — non-numeric channel id in path
// → 400 (pathInt64 fails before role check).
func TestM4ChannelRemoveManager_C5_BadRequest(t *testing.T) {
	env := newM4Env(t)
	cookieOwner, _ := env.seedUser(329)

	env.expect.DELETE("/api/channels/not-a-number/managers/abc").
		WithHeader(middleware.MMCookieHeader, cookieOwner).
		Expect().Status(400)
}

// ============================================================================
// GET /api/channels/:id/managers  — member-visible list
// ============================================================================

// TestM4ChannelListManagers_C1_HappyPath — owner of an empty-managers channel
// gets {managers: []}.
func TestM4ChannelListManagers_C1_HappyPath(t *testing.T) {
	env := newM4Env(t)
	cookieOwner, _ := env.seedUser(330)
	_, m1 := env.seedUser(331)
	chID := env.seedGroup(cookieOwner, "g3-list-mgr-happy", m1)

	mgrs := env.expect.GET("/api/channels/" + pathInt64s(chID) + "/managers").
		WithHeader(middleware.MMCookieHeader, cookieOwner).
		Expect().Status(200).JSON().Object()
	mgrs.Value("managers").Array().Length().IsEqual(0)
}

// TestM4ChannelListManagers_C2_CookieMissing — 401 without header.
func TestM4ChannelListManagers_C2_CookieMissing(t *testing.T) {
	env := newM4Env(t)
	cookieOwner, _ := env.seedUser(332)
	_, m1 := env.seedUser(333)
	chID := env.seedGroup(cookieOwner, "g3-list-mgr-no-cookie", m1)

	env.expect.GET("/api/channels/" + pathInt64s(chID) + "/managers").
		Expect().Status(401)
}

// TestM4ChannelListManagers_C3_CookieInvalid — 401 with bogus cookie.
func TestM4ChannelListManagers_C3_CookieInvalid(t *testing.T) {
	env := newM4Env(t)
	cookieOwner, _ := env.seedUser(334)
	_, m1 := env.seedUser(335)
	chID := env.seedGroup(cookieOwner, "g3-list-mgr-bad-cookie", m1)

	env.expect.GET("/api/channels/" + pathInt64s(chID) + "/managers").
		WithHeader(middleware.MMCookieHeader, "feedfacefeedfacefeedface").
		Expect().Status(401)
}

// TestM4ChannelListManagers_C4_NotMember — outsider (valid cookie, not in the
// channel) gets 403 (member required).
func TestM4ChannelListManagers_C4_NotMember(t *testing.T) {
	env := newM4Env(t)
	cookieOwner, _ := env.seedUser(336)
	_, m1 := env.seedUser(337)
	cookieOutsider, _ := env.seedUser(338)
	chID := env.seedGroup(cookieOwner, "g3-list-mgr-outsider", m1)

	env.expect.GET("/api/channels/" + pathInt64s(chID) + "/managers").
		WithHeader(middleware.MMCookieHeader, cookieOutsider).
		Expect().Status(403)
}

// TestM4ChannelListManagers_C5_BadRequest — non-numeric channel id → 400.
func TestM4ChannelListManagers_C5_BadRequest(t *testing.T) {
	env := newM4Env(t)
	cookieOwner, _ := env.seedUser(339)

	env.expect.GET("/api/channels/not-a-number/managers").
		WithHeader(middleware.MMCookieHeader, cookieOwner).
		Expect().Status(400)
}

// ============================================================================
// POST /api/channels/:id/pins/:message_id  — manager+ pin
// ============================================================================

// TestM4ChannelPinMessage_C1_HappyPath — owner pins a message; subsequent
// GET /pins includes the message id.
func TestM4ChannelPinMessage_C1_HappyPath(t *testing.T) {
	env := newM4Env(t)
	cookieOwner, ownerID := env.seedUser(340)
	_, m1 := env.seedUser(341)
	chID := env.seedGroup(cookieOwner, "g3-pin-happy", m1)
	msg := env.seedMessage(chID, ownerID, "pin me")

	env.expect.POST("/api/channels/" + pathInt64s(chID) + "/pins/" + pathInt64s(msg.ID)).
		WithHeader(middleware.MMCookieHeader, cookieOwner).
		Expect().Status(201).
		JSON().Object().Value("status").IsEqual("pinned")

	pins := env.expect.GET("/api/channels/" + pathInt64s(chID) + "/pins").
		WithHeader(middleware.MMCookieHeader, cookieOwner).
		Expect().Status(200).JSON().Object()
	pins.Value("pins").Array().ContainsAll(float64(msg.ID))
}

// TestM4ChannelPinMessage_C2_CookieMissing — 401 without header.
func TestM4ChannelPinMessage_C2_CookieMissing(t *testing.T) {
	env := newM4Env(t)
	cookieOwner, ownerID := env.seedUser(342)
	_, m1 := env.seedUser(343)
	chID := env.seedGroup(cookieOwner, "g3-pin-no-cookie", m1)
	msg := env.seedMessage(chID, ownerID, "pin")

	env.expect.POST("/api/channels/" + pathInt64s(chID) + "/pins/" + pathInt64s(msg.ID)).
		Expect().Status(401)
}

// TestM4ChannelPinMessage_C3_CookieInvalid — 401 with bad cookie.
func TestM4ChannelPinMessage_C3_CookieInvalid(t *testing.T) {
	env := newM4Env(t)
	cookieOwner, ownerID := env.seedUser(344)
	_, m1 := env.seedUser(345)
	chID := env.seedGroup(cookieOwner, "g3-pin-bad-cookie", m1)
	msg := env.seedMessage(chID, ownerID, "pin")

	env.expect.POST("/api/channels/" + pathInt64s(chID) + "/pins/" + pathInt64s(msg.ID)).
		WithHeader(middleware.MMCookieHeader, "1234567890abcdef12345678").
		Expect().Status(401)
}

// TestM4ChannelPinMessage_C4_Forbidden — plain member (not manager) pins
// a message → 403 (manager or owner required).
func TestM4ChannelPinMessage_C4_Forbidden(t *testing.T) {
	env := newM4Env(t)
	cookieOwner, ownerID := env.seedUser(346)
	cookieMember, m1 := env.seedUser(347)
	chID := env.seedGroup(cookieOwner, "g3-pin-forbid", m1)
	msg := env.seedMessage(chID, ownerID, "pin")

	env.expect.POST("/api/channels/" + pathInt64s(chID) + "/pins/" + pathInt64s(msg.ID)).
		WithHeader(middleware.MMCookieHeader, cookieMember).
		Expect().Status(403)
}

// TestM4ChannelPinMessage_C5_BadRequest — non-numeric message id → 400.
func TestM4ChannelPinMessage_C5_BadRequest(t *testing.T) {
	env := newM4Env(t)
	cookieOwner, _ := env.seedUser(348)
	_, m1 := env.seedUser(349)
	chID := env.seedGroup(cookieOwner, "g3-pin-bad-msg", m1)

	env.expect.POST("/api/channels/" + pathInt64s(chID) + "/pins/not-numeric").
		WithHeader(middleware.MMCookieHeader, cookieOwner).
		Expect().Status(400)
}

// ============================================================================
// DELETE /api/channels/:id/pins/:message_id  — manager+ unpin
// ============================================================================

// TestM4ChannelUnpinMessage_C1_HappyPath — pin then unpin; GET /pins becomes
// empty.
func TestM4ChannelUnpinMessage_C1_HappyPath(t *testing.T) {
	env := newM4Env(t)
	cookieOwner, ownerID := env.seedUser(350)
	_, m1 := env.seedUser(351)
	chID := env.seedGroup(cookieOwner, "g3-unpin-happy", m1)
	msg := env.seedMessage(chID, ownerID, "to be unpinned")

	env.expect.POST("/api/channels/" + pathInt64s(chID) + "/pins/" + pathInt64s(msg.ID)).
		WithHeader(middleware.MMCookieHeader, cookieOwner).
		Expect().Status(201)

	env.expect.DELETE("/api/channels/" + pathInt64s(chID) + "/pins/" + pathInt64s(msg.ID)).
		WithHeader(middleware.MMCookieHeader, cookieOwner).
		Expect().Status(200).
		JSON().Object().Value("status").IsEqual("unpinned")

	pins := env.expect.GET("/api/channels/" + pathInt64s(chID) + "/pins").
		WithHeader(middleware.MMCookieHeader, cookieOwner).
		Expect().Status(200).JSON().Object()
	pins.Value("pins").Array().Length().IsEqual(0)
}

// TestM4ChannelUnpinMessage_C2_CookieMissing — 401 without header.
func TestM4ChannelUnpinMessage_C2_CookieMissing(t *testing.T) {
	env := newM4Env(t)
	cookieOwner, ownerID := env.seedUser(352)
	_, m1 := env.seedUser(353)
	chID := env.seedGroup(cookieOwner, "g3-unpin-no-cookie", m1)
	msg := env.seedMessage(chID, ownerID, "x")

	env.expect.DELETE("/api/channels/" + pathInt64s(chID) + "/pins/" + pathInt64s(msg.ID)).
		Expect().Status(401)
}

// TestM4ChannelUnpinMessage_C3_CookieInvalid — 401 with bad cookie.
func TestM4ChannelUnpinMessage_C3_CookieInvalid(t *testing.T) {
	env := newM4Env(t)
	cookieOwner, ownerID := env.seedUser(354)
	_, m1 := env.seedUser(355)
	chID := env.seedGroup(cookieOwner, "g3-unpin-bad-cookie", m1)
	msg := env.seedMessage(chID, ownerID, "x")

	env.expect.DELETE("/api/channels/" + pathInt64s(chID) + "/pins/" + pathInt64s(msg.ID)).
		WithHeader(middleware.MMCookieHeader, "abadbeefabadbeefabadbeef").
		Expect().Status(401)
}

// TestM4ChannelUnpinMessage_C4_Forbidden — plain member unpins → 403.
func TestM4ChannelUnpinMessage_C4_Forbidden(t *testing.T) {
	env := newM4Env(t)
	cookieOwner, ownerID := env.seedUser(356)
	cookieMember, m1 := env.seedUser(357)
	chID := env.seedGroup(cookieOwner, "g3-unpin-forbid", m1)
	msg := env.seedMessage(chID, ownerID, "x")

	// Owner pins so there's a real pin to attempt to unpin.
	env.expect.POST("/api/channels/" + pathInt64s(chID) + "/pins/" + pathInt64s(msg.ID)).
		WithHeader(middleware.MMCookieHeader, cookieOwner).
		Expect().Status(201)

	env.expect.DELETE("/api/channels/" + pathInt64s(chID) + "/pins/" + pathInt64s(msg.ID)).
		WithHeader(middleware.MMCookieHeader, cookieMember).
		Expect().Status(403)
}

// TestM4ChannelUnpinMessage_C5_BadRequest — non-numeric channel id → 400.
func TestM4ChannelUnpinMessage_C5_BadRequest(t *testing.T) {
	env := newM4Env(t)
	cookieOwner, _ := env.seedUser(358)

	env.expect.DELETE("/api/channels/abc/pins/123").
		WithHeader(middleware.MMCookieHeader, cookieOwner).
		Expect().Status(400)
}

// ============================================================================
// GET /api/channels/:id/pins  — member-visible list
// ============================================================================

// TestM4ChannelListPins_C1_HappyPath — fresh channel returns {pins: []}.
func TestM4ChannelListPins_C1_HappyPath(t *testing.T) {
	env := newM4Env(t)
	cookieOwner, _ := env.seedUser(360)
	_, m1 := env.seedUser(362)
	chID := env.seedGroup(cookieOwner, "g3-listpin-happy", m1)

	pins := env.expect.GET("/api/channels/" + pathInt64s(chID) + "/pins").
		WithHeader(middleware.MMCookieHeader, cookieOwner).
		Expect().Status(200).JSON().Object()
	pins.Value("pins").Array().Length().IsEqual(0)
}

// TestM4ChannelListPins_C2_CookieMissing — 401 without header.
func TestM4ChannelListPins_C2_CookieMissing(t *testing.T) {
	env := newM4Env(t)
	cookieOwner, _ := env.seedUser(363)
	_, m1 := env.seedUser(364)
	chID := env.seedGroup(cookieOwner, "g3-listpin-no-cookie", m1)

	env.expect.GET("/api/channels/" + pathInt64s(chID) + "/pins").
		Expect().Status(401)
}

// TestM4ChannelListPins_C3_CookieInvalid — 401 with bad cookie.
func TestM4ChannelListPins_C3_CookieInvalid(t *testing.T) {
	env := newM4Env(t)
	cookieOwner, _ := env.seedUser(365)
	_, m1 := env.seedUser(366)
	chID := env.seedGroup(cookieOwner, "g3-listpin-bad-cookie", m1)

	env.expect.GET("/api/channels/" + pathInt64s(chID) + "/pins").
		WithHeader(middleware.MMCookieHeader, "f00dbabef00dbabef00dbabe").
		Expect().Status(401)
}

// TestM4ChannelListPins_C4_NotMember — outsider gets 403.
func TestM4ChannelListPins_C4_NotMember(t *testing.T) {
	env := newM4Env(t)
	cookieOwner, _ := env.seedUser(367)
	_, m1 := env.seedUser(368)
	cookieOutsider, _ := env.seedUser(369)
	chID := env.seedGroup(cookieOwner, "g3-listpin-outsider", m1)

	env.expect.GET("/api/channels/" + pathInt64s(chID) + "/pins").
		WithHeader(middleware.MMCookieHeader, cookieOutsider).
		Expect().Status(403)
}

// TestM4ChannelListPins_C5_BadRequest — non-numeric channel id → 400.
func TestM4ChannelListPins_C5_BadRequest(t *testing.T) {
	env := newM4Env(t)
	cookieOwner, _ := env.seedUser(359) // 359 unused above; safe in 300-379 range.

	env.expect.GET("/api/channels/oops/pins").
		WithHeader(middleware.MMCookieHeader, cookieOwner).
		Expect().Status(400)
}

// ============================================================================
// PATCH /api/channels/:id/members/:user_id  — role / notify_pref / is_top
// ============================================================================

// TestM4ChannelPatchMember_C1_HappyPath — caller flips their OWN is_top to
// true; handler responds with status:updated.
func TestM4ChannelPatchMember_C1_HappyPath(t *testing.T) {
	env := newM4Env(t)
	cookieOwner, ownerID := env.seedUser(370)
	_, m1 := env.seedUser(371)
	chID := env.seedGroup(cookieOwner, "g3-patch-mem-happy", m1)

	env.expect.PATCH("/api/channels/" + pathInt64s(chID) + "/members/" + ownerID).
		WithHeader(middleware.MMCookieHeader, cookieOwner).
		WithJSON(map[string]any{"is_top": true}).
		Expect().Status(200).
		JSON().Object().Value("status").IsEqual("updated")
}

// TestM4ChannelPatchMember_C2_CookieMissing — 401 without header.
func TestM4ChannelPatchMember_C2_CookieMissing(t *testing.T) {
	env := newM4Env(t)
	cookieOwner, ownerID := env.seedUser(372)
	_, m1 := env.seedUser(373)
	chID := env.seedGroup(cookieOwner, "g3-patch-mem-no-cookie", m1)

	env.expect.PATCH("/api/channels/" + pathInt64s(chID) + "/members/" + ownerID).
		WithJSON(map[string]any{"is_top": true}).
		Expect().Status(401)
}

// TestM4ChannelPatchMember_C3_CookieInvalid — 401 with bogus cookie.
func TestM4ChannelPatchMember_C3_CookieInvalid(t *testing.T) {
	env := newM4Env(t)
	cookieOwner, ownerID := env.seedUser(374)
	_, m1 := env.seedUser(375)
	chID := env.seedGroup(cookieOwner, "g3-patch-mem-bad-cookie", m1)

	env.expect.PATCH("/api/channels/" + pathInt64s(chID) + "/members/" + ownerID).
		WithHeader(middleware.MMCookieHeader, "decafbaddecafbaddecafbad").
		WithJSON(map[string]any{"is_top": true}).
		Expect().Status(401)
}

// TestM4ChannelPatchMember_C4_Forbidden — caller tries to flip ANOTHER
// member's is_top → 403 (is_top is strictly self-only).
func TestM4ChannelPatchMember_C4_Forbidden(t *testing.T) {
	env := newM4Env(t)
	cookieOwner, _ := env.seedUser(376)
	_, m1 := env.seedUser(377)
	chID := env.seedGroup(cookieOwner, "g3-patch-mem-forbid", m1)

	env.expect.PATCH("/api/channels/" + pathInt64s(chID) + "/members/" + m1).
		WithHeader(middleware.MMCookieHeader, cookieOwner).
		WithJSON(map[string]any{"is_top": true}).
		Expect().Status(403)
}

// TestM4ChannelPatchMember_C5_NoFields — body has no role/notify_pref/is_top
// → 422 ("no fields to update").
func TestM4ChannelPatchMember_C5_NoFields(t *testing.T) {
	env := newM4Env(t)
	cookieOwner, ownerID := env.seedUser(378)
	_, m1 := env.seedUser(379)
	chID := env.seedGroup(cookieOwner, "g3-patch-mem-no-fields", m1)

	env.expect.PATCH("/api/channels/" + pathInt64s(chID) + "/members/" + ownerID).
		WithHeader(middleware.MMCookieHeader, cookieOwner).
		WithJSON(map[string]any{}).
		Expect().Status(422)
}


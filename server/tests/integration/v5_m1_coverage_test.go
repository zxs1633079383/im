//go:build integration

// Package integration — M1/M2 endpoint coverage fillers.
//
// These tests exercise three detail/read endpoints that previously had no
// dedicated coverage:
//
//   - GET /api/messages/:id/readers  (paginated user_id list of readers)
//   - GET /api/announcements/:id     (announcement detail)
//   - GET /api/approvals/:id         (approval detail — requester/approver only)
//
// The tests are small, HTTP-only, and rely on the v5env harness in
// v5_harness_test.go. They do not assert on recorders; event-broadcast
// strengthening lives in the existing flow tests.
package integration

import (
	"fmt"
	"testing"
)

// TestM1_GetReaders: alice sends a message, bob + carol mark it read; the
// readers endpoint returns the two reader user_ids (alice is NOT in the list
// because the send path does not auto-advance her last_read_seq).
func TestM1_GetReaders(t *testing.T) {
	env := newV5Env(t)
	_, aliceTok := env.CreateUserAndToken("rdr_alice", "rdr_a@x.com")
	bobID, bobTok := env.CreateUserAndToken("rdr_bob", "rdr_b@x.com")
	carolID, carolTok := env.CreateUserAndToken("rdr_carol", "rdr_c@x.com")
	chID := env.CreateGroup(aliceTok, "readers_ch", bobID, carolID)

	msgID := env.MustSendAndReturnMsgID(aliceTok, chID, "hello", "rdr-1")

	// Bob + Carol both mark read.
	env.MarkRead(bobTok, chID)
	env.MarkRead(carolTok, chID)

	obj := env.httpExpect.GET(fmt.Sprintf("/api/messages/%d/readers", msgID)).
		WithHeader("Authorization", bearer(aliceTok)).
		Expect().Status(200).JSON().Object()
	readers := obj.Value("readers").Array()

	foundBob, foundCarol := false, false
	for i := 0; i < int(readers.Length().Raw()); i++ {
		uid := int64(readers.Value(i).Number().Raw())
		if uid == bobID {
			foundBob = true
		}
		if uid == carolID {
			foundCarol = true
		}
	}
	if !foundBob || !foundCarol {
		t.Fatalf("readers missing: bob=%v carol=%v, got=%v", foundBob, foundCarol, readers.Raw())
	}

	// next_cursor key must be present (0 when a single page finishes the set).
	obj.ContainsKey("next_cursor")
}

// TestM1_GetReadersPagination: with >limit readers, cursor-based paging
// assembles a complete set across multiple pages.
func TestM1_GetReadersPagination(t *testing.T) {
	env := newV5Env(t)
	_, ownerTok := env.CreateUserAndToken("rdrp_owner", "rdrp_o@x.com")

	// Build 12 member users (> limit=5 so we get at least 2 pages).
	memberIDs := []int64{}
	memberToks := []string{}
	for i := 0; i < 12; i++ {
		id, tok := env.CreateUserAndToken(
			fmt.Sprintf("rdrp_m%d", i),
			fmt.Sprintf("rdrp_m%d@x.com", i),
		)
		memberIDs = append(memberIDs, id)
		memberToks = append(memberToks, tok)
	}
	chID := env.CreateGroup(ownerTok, "rdrp_ch", memberIDs...)

	msgID := env.MustSendAndReturnMsgID(ownerTok, chID, "p1", "rdrp-1")

	// First 10 members mark read.
	for i := 0; i < 10; i++ {
		env.MarkRead(memberToks[i], chID)
	}

	// Walk pages with limit=5 and assemble the full set.
	collected := map[int64]bool{}
	cursor := int64(0)
	for page := 0; page < 5; page++ {
		req := env.httpExpect.GET(fmt.Sprintf("/api/messages/%d/readers", msgID)).
			WithHeader("Authorization", bearer(ownerTok)).
			WithQuery("limit", "5")
		if cursor > 0 {
			req = req.WithQuery("cursor", cursor)
		}
		obj := req.Expect().Status(200).JSON().Object()
		rs := obj.Value("readers").Array()
		n := int(rs.Length().Raw())
		for i := 0; i < n; i++ {
			collected[int64(rs.Value(i).Number().Raw())] = true
		}
		nextCursor := int64(obj.Value("next_cursor").Number().Raw())
		if nextCursor == 0 || n == 0 {
			break
		}
		cursor = nextCursor
	}
	if len(collected) < 10 {
		t.Fatalf("paginated readers count=%d, want >= 10: %v", len(collected), collected)
	}
}

// TestM2_AnnouncementDetail: owner creates an announcement; owner + member see
// identical detail; a non-member receives 403.
func TestM2_AnnouncementDetail(t *testing.T) {
	env := newV5Env(t)
	_, ownerTok := env.CreateUserAndToken("ad_owner", "ad_o@x.com")
	memberID, memberTok := env.CreateUserAndToken("ad_mem", "ad_m@x.com")
	chID := env.CreateGroup(ownerTok, "ad_ch", memberID)

	createResp := env.httpExpect.POST("/api/announcements").
		WithHeader("Authorization", bearer(ownerTok)).
		WithJSON(map[string]any{
			"channel_id": chID,
			"title":      "weekly",
			"content":    "team meeting Friday",
		}).
		Expect().Status(201).JSON().Object()
	annID := int64(createResp.Value("id").Number().Raw())

	// Owner detail.
	ownerView := env.httpExpect.GET(fmt.Sprintf("/api/announcements/%d", annID)).
		WithHeader("Authorization", bearer(ownerTok)).
		Expect().Status(200).JSON().Object()
	ownerView.Value("title").String().IsEqual("weekly")
	ownerView.Value("content").String().IsEqual("team meeting Friday")
	ownerView.Value("channel_id").Number().IsEqual(chID)

	// Member detail.
	memberView := env.httpExpect.GET(fmt.Sprintf("/api/announcements/%d", annID)).
		WithHeader("Authorization", bearer(memberTok)).
		Expect().Status(200).JSON().Object()
	memberView.Value("title").String().IsEqual("weekly")
	memberView.Value("content").String().IsEqual("team meeting Friday")

	// Non-member receives 403.
	_, outsiderTok := env.CreateUserAndToken("ad_outsider", "ad_x@x.com")
	env.httpExpect.GET(fmt.Sprintf("/api/announcements/%d", annID)).
		WithHeader("Authorization", bearer(outsiderTok)).
		Expect().Status(403)
}

// TestM2_ApprovalDetail: requester + approver can view the row; an unrelated
// user receives 403 (approval.Get is strictly requester/approver-gated,
// independent of channel membership). The approver is also the channel owner
// so they qualify as manager+ — which is a prerequisite for Create.
func TestM2_ApprovalDetail(t *testing.T) {
	env := newV5Env(t)
	approverID, approverTok := env.CreateUserAndToken("apd_app", "apd_a@x.com")
	requesterID, requesterTok := env.CreateUserAndToken("apd_req", "apd_r@x.com")

	// Approver creates the group (they become owner → manager+), requester
	// joins as a plain member.
	chID := env.CreateGroup(approverTok, "apd_ch", requesterID)

	createResp := env.httpExpect.POST("/api/approvals").
		WithHeader("Authorization", bearer(requesterTok)).
		WithJSON(map[string]any{
			"channel_id":  chID,
			"approver_id": approverID,
			"subject":     "purchase request",
			"content":     "need new laptop",
		}).
		Expect().Status(201).JSON().Object()
	apID := int64(createResp.Value("id").Number().Raw())

	// Requester view.
	reqView := env.httpExpect.GET(fmt.Sprintf("/api/approvals/%d", apID)).
		WithHeader("Authorization", bearer(requesterTok)).
		Expect().Status(200).JSON().Object()
	reqView.Value("subject").String().IsEqual("purchase request")
	reqView.Value("content").String().IsEqual("need new laptop")
	reqView.Value("requester_id").Number().IsEqual(requesterID)
	reqView.Value("approver_id").Number().IsEqual(approverID)

	// Approver view.
	appView := env.httpExpect.GET(fmt.Sprintf("/api/approvals/%d", apID)).
		WithHeader("Authorization", bearer(approverTok)).
		Expect().Status(200).JSON().Object()
	appView.Value("status").Number().IsEqual(0) // pending

	// Outsider (neither requester nor approver) — 403.
	_, outsiderTok := env.CreateUserAndToken("apd_outsider", "apd_x@x.com")
	env.httpExpect.GET(fmt.Sprintf("/api/approvals/%d", apID)).
		WithHeader("Authorization", bearer(outsiderTok)).
		Expect().Status(403)
}

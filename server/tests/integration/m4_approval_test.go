//go:build integration

// Batch-C C2 — approval family integration tests (simplified: 1 happy path
// per endpoint, no error-case matrix).
//
// Covers the 7 endpoints registered by RegisterApprovalRoutes:
//
//   POST /api/approvals                  (submit)
//   POST /api/approvals/:id/approve      (approver decides)
//   POST /api/approvals/:id/reject       (approver decides)
//   POST /api/approvals/:id/cancel       (requester cancels pending)
//   GET  /api/approvals/pending          (approver inbox)
//   GET  /api/approvals/mine             (requester filed list)
//   GET  /api/approvals/:id              (detail; requester or approver only)
//
// Routes are wired centrally by buildEngine (m4_harness_test.go); tests only
// seed users + a group channel and exercise the published surface.
//
// Seed range 600-699 is reserved for C2 (per Batch-C dispatch); each test
// allocates a contiguous pair of seeds (submitter + approver/owner).
//
// Permission shape exercised by every test: a group channel with owner =
// approver (MemberRoleOwner ≥ MemberRoleAdmin satisfies IsManagerOrOwner) and
// a regular member = submitter. ApprovalService.Create therefore accepts the
// (channel, submitter, approver) triple without further governance setup.
package integration

import (
	"strconv"
	"testing"

	"im-server/internal/middleware"
	"im-server/internal/repo"
)

// approvalEnv bundles env with the two cookie/userID pairs the approval flow
// needs (submitter posts approvals, approver decides on them) plus the group
// channel they share. Reduces boilerplate across the 7 happy-path tests.
// C012 P-D: channelID is TEXT (string) post-migration.
type approvalEnv struct {
	*m4env
	submitterCookie string
	submitterID     string
	approverCookie  string
	approverID      string
	channelID       string
}

// newApprovalEnv builds a fresh m4 env, seeds a (submitter, approver) pair on
// disjoint seed slots and creates a group channel where approver = owner /
// submitter = member. seedBase must be even — the function uses seedBase and
// seedBase+1.
func newApprovalEnv(t *testing.T, seedBase int) *approvalEnv {
	t.Helper()
	env := newM4Env(t)

	approverCookie, approverID := env.seedUser(seedBase)
	submitterCookie, submitterID := env.seedUser(seedBase + 1)
	channelID := env.seedGroup(approverCookie, "c2-approval-"+strconv.Itoa(seedBase), submitterID)

	return &approvalEnv{
		m4env:           env,
		submitterCookie: submitterCookie,
		submitterID:     submitterID,
		approverCookie:  approverCookie,
		approverID:      approverID,
		channelID:       channelID,
	}
}

// submitApproval is the canonical "create one approval" helper used by the
// decision / list / detail tests as a precondition. Returns the approval id.
// C012 P-D: approval id is now TEXT (string).
func (a *approvalEnv) submitApproval(t *testing.T, subject, content string) string {
	t.Helper()
	body := successBody(a.expect.POST("/api/approvals").
		WithHeader(middleware.MMCookieHeader, a.submitterCookie).
		WithJSON(map[string]any{
			"channel_id":  a.channelID,
			"approver_id": a.approverID,
			"subject":     subject,
			"content":     content,
		}).
		Expect().Status(201))
	return body.Value("id").String().Raw()
}

// ---- HappyPath tests --------------------------------------------------------

// TestM4ApprovalSubmit_HappyPath — submitter files an approval against a
// channel where the approver is owner. Server returns 201 with the
// persisted row (status = pending = 0).
func TestM4ApprovalSubmit_HappyPath(t *testing.T) {
	a := newApprovalEnv(t, 600)

	body := successBody(a.expect.POST("/api/approvals").
		WithHeader(middleware.MMCookieHeader, a.submitterCookie).
		WithJSON(map[string]any{
			"channel_id":  a.channelID,
			"approver_id": a.approverID,
			"subject":     "leave request",
			"content":     "out friday",
		}).
		Expect().Status(201))
	// C012 P-D: id is TEXT (string) post-migration.
	body.Value("id").String().NotEmpty()
	body.Value("status").Number().IsEqual(float64(repo.ApprovalStatusPending))
	body.Value("requester_id").String().IsEqual(a.submitterID)
	body.Value("approver_id").String().IsEqual(a.approverID)
}

// TestM4ApprovalApprove_HappyPath — approver approves a pending approval and
// gets the refreshed row back with status = approved (1).
func TestM4ApprovalApprove_HappyPath(t *testing.T) {
	a := newApprovalEnv(t, 610)
	id := a.submitApproval(t, "subj approve", "body approve")

	body := successBody(a.expect.POST("/api/approvals/"+id+"/approve").
		WithHeader(middleware.MMCookieHeader, a.approverCookie).
		WithJSON(map[string]any{"note": "ok"}).
		Expect().Status(200))
	body.Value("id").String().IsEqual(id)
	body.Value("status").Number().IsEqual(float64(repo.ApprovalStatusApproved))
}

// TestM4ApprovalReject_HappyPath — approver rejects a pending approval; row
// transitions to status = rejected (2).
func TestM4ApprovalReject_HappyPath(t *testing.T) {
	a := newApprovalEnv(t, 620)
	id := a.submitApproval(t, "subj reject", "body reject")

	body := successBody(a.expect.POST("/api/approvals/"+id+"/reject").
		WithHeader(middleware.MMCookieHeader, a.approverCookie).
		WithJSON(map[string]any{"note": "no"}).
		Expect().Status(200))
	body.Value("id").String().IsEqual(id)
	body.Value("status").Number().IsEqual(float64(repo.ApprovalStatusRejected))
}

// TestM4ApprovalCancel_HappyPath — requester cancels their own pending
// approval; row transitions to status = cancelled (3).
func TestM4ApprovalCancel_HappyPath(t *testing.T) {
	a := newApprovalEnv(t, 630)
	id := a.submitApproval(t, "subj cancel", "body cancel")

	body := successBody(a.expect.POST("/api/approvals/"+id+"/cancel").
		WithHeader(middleware.MMCookieHeader, a.submitterCookie).
		Expect().Status(200))
	body.Value("id").String().IsEqual(id)
	body.Value("status").Number().IsEqual(float64(repo.ApprovalStatusCancelled))
}

// TestM4ApprovalListPending_HappyPath — approver lists their pending inbox
// after a fresh submission and sees the new approval. Response shape is
// {"approvals":[...]} (gin.H wrapper, not raw array).
func TestM4ApprovalListPending_HappyPath(t *testing.T) {
	a := newApprovalEnv(t, 640)
	id := a.submitApproval(t, "subj pending", "body pending")

	body := successBody(a.expect.GET("/api/approvals/pending").
		WithHeader(middleware.MMCookieHeader, a.approverCookie).
		Expect().Status(200))
	arr := body.Value("approvals").Array()
	arr.Length().Gt(0)
	arr.Value(0).Object().Value("id").String().IsEqual(id)
}

// TestM4ApprovalListMine_HappyPath — requester lists everything they've
// filed and sees the new submission. Response shape mirrors /pending:
// {"approvals":[...]}.
func TestM4ApprovalListMine_HappyPath(t *testing.T) {
	a := newApprovalEnv(t, 650)
	id := a.submitApproval(t, "subj mine", "body mine")

	body := successBody(a.expect.GET("/api/approvals/mine").
		WithHeader(middleware.MMCookieHeader, a.submitterCookie).
		Expect().Status(200))
	arr := body.Value("approvals").Array()
	arr.Length().Gt(0)
	arr.Value(0).Object().Value("id").String().IsEqual(id)
}

// TestM4ApprovalGet_HappyPath — either party (here the approver) can fetch
// the approval detail by id; envelope wraps the raw approval row.
func TestM4ApprovalGet_HappyPath(t *testing.T) {
	a := newApprovalEnv(t, 660)
	id := a.submitApproval(t, "subj get", "body get")

	body := successBody(a.expect.GET("/api/approvals/"+id).
		WithHeader(middleware.MMCookieHeader, a.approverCookie).
		Expect().Status(200))
	body.Value("id").String().IsEqual(id)
	body.Value("status").Number().IsEqual(float64(repo.ApprovalStatusPending))
	body.Value("subject").String().IsEqual("subj get")
}

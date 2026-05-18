//go:build integration

// Phase P4 — approval 7 endpoint × C2-C5 错误矩阵。Happy path 已在
// m4_approval_test.go。Seed 范围 2900-3099。
package integration

import (
	"strconv"
	"testing"

	"im-server/internal/middleware"
)

// helper: 复用 approvalEnv，新建带不同 seed 的 (submitter, approver, channel)。
func newApprovalEnvMatrix(t *testing.T, base int) *approvalEnv {
	t.Helper()
	env := newM4Env(t)
	approverCookie, approverID := env.seedUser(base)
	submitterCookie, submitterID := env.seedUser(base + 1)
	channelID := env.seedGroup(approverCookie, "p4-app-"+strconv.Itoa(base), submitterID)
	return &approvalEnv{
		m4env:           env,
		submitterCookie: submitterCookie,
		submitterID:     submitterID,
		approverCookie:  approverCookie,
		approverID:      approverID,
		channelID:       channelID,
	}
}

// ---- POST /api/approvals (submit) ------------------------------------------

// TestM4ApprovalSubmit_C2_CookieMissing — 401.
func TestM4ApprovalSubmit_C2_CookieMissing(t *testing.T) {
	env := newM4Env(t)
	errorBody(env.expect.POST("/api/approvals").
		WithJSON(map[string]any{}).
		Expect().Status(401))
}

// TestM4ApprovalSubmit_C3_CookieInvalid — 401.
func TestM4ApprovalSubmit_C3_CookieInvalid(t *testing.T) {
	env := newM4Env(t)
	errorBody(env.expect.POST("/api/approvals").
		WithHeader(middleware.MMCookieHeader, "deadbeefdeadbeefdeadbeef").
		WithJSON(map[string]any{}).
		Expect().Status(401))
}

// TestM4ApprovalSubmit_C4_NotMember — outsider 提交 → 403.
func TestM4ApprovalSubmit_C4_NotMember(t *testing.T) {
	a := newApprovalEnvMatrix(t, 2900)
	cookieOuter, _ := a.seedUser(2999)

	errorBody(a.expect.POST("/api/approvals").
		WithHeader(middleware.MMCookieHeader, cookieOuter).
		WithJSON(map[string]any{
			"channel_id":  a.channelID,
			"approver_id": a.approverID,
			"subject":     "outsider",
			"content":     "content",
		}).
		Expect().Status(403)).
		Value("error").String().Contains("member")
}

// TestM4ApprovalSubmit_C5_MissingChannelID — channel_id 缺 → 422.
func TestM4ApprovalSubmit_C5_MissingChannelID(t *testing.T) {
	a := newApprovalEnvMatrix(t, 2910)
	errorBody(a.expect.POST("/api/approvals").
		WithHeader(middleware.MMCookieHeader, a.submitterCookie).
		WithJSON(map[string]any{
			"approver_id": a.approverID,
			"subject":     "subj",
			"content":     "ctn",
		}).
		Expect().Status(422)).
		Value("error").String().Contains("channel_id")
}

// TestM4ApprovalSubmit_C5b_MissingSubject — subject 空 → 422.
func TestM4ApprovalSubmit_C5b_MissingSubject(t *testing.T) {
	a := newApprovalEnvMatrix(t, 2920)
	errorBody(a.expect.POST("/api/approvals").
		WithHeader(middleware.MMCookieHeader, a.submitterCookie).
		WithJSON(map[string]any{
			"channel_id":  a.channelID,
			"approver_id": a.approverID,
			"content":     "ctn",
		}).
		Expect().Status(422)).
		Value("error").String().Contains("subject")
}

// ---- POST /api/approvals/:id/approve ---------------------------------------

// TestM4ApprovalApprove_C2_CookieMissing — 401.
func TestM4ApprovalApprove_C2_CookieMissing(t *testing.T) {
	env := newM4Env(t)
	errorBody(env.expect.POST("/api/approvals/x/approve").
		WithJSON(map[string]any{}).
		Expect().Status(401))
}

// TestM4ApprovalApprove_C3_CookieInvalid — 401.
func TestM4ApprovalApprove_C3_CookieInvalid(t *testing.T) {
	env := newM4Env(t)
	errorBody(env.expect.POST("/api/approvals/x/approve").
		WithHeader(middleware.MMCookieHeader, "deadbeefdeadbeefdeadbeef").
		WithJSON(map[string]any{}).
		Expect().Status(401))
}

// TestM4ApprovalApprove_C4_NotApprover — submitter 自己 approve → 403.
func TestM4ApprovalApprove_C4_NotApprover(t *testing.T) {
	a := newApprovalEnvMatrix(t, 2930)
	id := a.submitApproval(t, "subj", "ctn")

	errorBody(a.expect.POST("/api/approvals/"+id+"/approve").
		WithHeader(middleware.MMCookieHeader, a.submitterCookie).
		WithJSON(map[string]any{}).
		Expect().Status(403)).
		Value("error").String().Contains("approver")
}

// TestM4ApprovalApprove_C5_NotFound — id 不存在 → 404.
func TestM4ApprovalApprove_C5_NotFound(t *testing.T) {
	a := newApprovalEnvMatrix(t, 2940)
	errorBody(a.expect.POST("/api/approvals/ghost/approve").
		WithHeader(middleware.MMCookieHeader, a.approverCookie).
		WithJSON(map[string]any{}).
		Expect().Status(404)).
		Value("error").String().Contains("not found")
}

// ---- POST /api/approvals/:id/reject ----------------------------------------

// TestM4ApprovalReject_C2_CookieMissing — 401.
func TestM4ApprovalReject_C2_CookieMissing(t *testing.T) {
	env := newM4Env(t)
	errorBody(env.expect.POST("/api/approvals/x/reject").
		WithJSON(map[string]any{}).
		Expect().Status(401))
}

// TestM4ApprovalReject_C3_CookieInvalid — 401.
func TestM4ApprovalReject_C3_CookieInvalid(t *testing.T) {
	env := newM4Env(t)
	errorBody(env.expect.POST("/api/approvals/x/reject").
		WithHeader(middleware.MMCookieHeader, "deadbeefdeadbeefdeadbeef").
		WithJSON(map[string]any{}).
		Expect().Status(401))
}

// TestM4ApprovalReject_C4_NotApprover — submitter 自 reject → 403.
func TestM4ApprovalReject_C4_NotApprover(t *testing.T) {
	a := newApprovalEnvMatrix(t, 2950)
	id := a.submitApproval(t, "r-subj", "r-ctn")

	errorBody(a.expect.POST("/api/approvals/"+id+"/reject").
		WithHeader(middleware.MMCookieHeader, a.submitterCookie).
		WithJSON(map[string]any{}).
		Expect().Status(403)).
		Value("error").String().Contains("approver")
}

// TestM4ApprovalReject_C5_NotFound — id 不存在 → 404.
func TestM4ApprovalReject_C5_NotFound(t *testing.T) {
	a := newApprovalEnvMatrix(t, 2960)
	errorBody(a.expect.POST("/api/approvals/ghost/reject").
		WithHeader(middleware.MMCookieHeader, a.approverCookie).
		WithJSON(map[string]any{}).
		Expect().Status(404)).
		Value("error").String().Contains("not found")
}

// ---- POST /api/approvals/:id/cancel ----------------------------------------

// TestM4ApprovalCancel_C2_CookieMissing — 401.
func TestM4ApprovalCancel_C2_CookieMissing(t *testing.T) {
	env := newM4Env(t)
	errorBody(env.expect.POST("/api/approvals/x/cancel").Expect().Status(401))
}

// TestM4ApprovalCancel_C3_CookieInvalid — 401.
func TestM4ApprovalCancel_C3_CookieInvalid(t *testing.T) {
	env := newM4Env(t)
	errorBody(env.expect.POST("/api/approvals/x/cancel").
		WithHeader(middleware.MMCookieHeader, "deadbeefdeadbeefdeadbeef").
		Expect().Status(401))
}

// TestM4ApprovalCancel_C4_NotRequester — approver 取消他人的 → 403.
func TestM4ApprovalCancel_C4_NotRequester(t *testing.T) {
	a := newApprovalEnvMatrix(t, 2970)
	id := a.submitApproval(t, "c-subj", "c-ctn")

	errorBody(a.expect.POST("/api/approvals/"+id+"/cancel").
		WithHeader(middleware.MMCookieHeader, a.approverCookie).
		Expect().Status(403)).
		Value("error").String().Contains("requester")
}

// TestM4ApprovalCancel_C5_NotFound — 404.
func TestM4ApprovalCancel_C5_NotFound(t *testing.T) {
	a := newApprovalEnvMatrix(t, 2980)
	errorBody(a.expect.POST("/api/approvals/ghost/cancel").
		WithHeader(middleware.MMCookieHeader, a.submitterCookie).
		Expect().Status(404)).
		Value("error").String().Contains("not found")
}

// ---- GET /api/approvals/pending --------------------------------------------

// TestM4ApprovalListPending_C2_CookieMissing — 401.
func TestM4ApprovalListPending_C2_CookieMissing(t *testing.T) {
	env := newM4Env(t)
	errorBody(env.expect.GET("/api/approvals/pending").Expect().Status(401))
}

// TestM4ApprovalListPending_C3_CookieInvalid — 401.
func TestM4ApprovalListPending_C3_CookieInvalid(t *testing.T) {
	env := newM4Env(t)
	errorBody(env.expect.GET("/api/approvals/pending").
		WithHeader(middleware.MMCookieHeader, "deadbeefdeadbeefdeadbeef").
		Expect().Status(401))
}

// ---- GET /api/approvals/mine -----------------------------------------------

// TestM4ApprovalListMine_C2_CookieMissing — 401.
func TestM4ApprovalListMine_C2_CookieMissing(t *testing.T) {
	env := newM4Env(t)
	errorBody(env.expect.GET("/api/approvals/mine").Expect().Status(401))
}

// TestM4ApprovalListMine_C3_CookieInvalid — 401.
func TestM4ApprovalListMine_C3_CookieInvalid(t *testing.T) {
	env := newM4Env(t)
	errorBody(env.expect.GET("/api/approvals/mine").
		WithHeader(middleware.MMCookieHeader, "deadbeefdeadbeefdeadbeef").
		Expect().Status(401))
}

// ---- GET /api/approvals/:id ------------------------------------------------

// TestM4ApprovalGet_C2_CookieMissing — 401.
func TestM4ApprovalGet_C2_CookieMissing(t *testing.T) {
	env := newM4Env(t)
	errorBody(env.expect.GET("/api/approvals/x").Expect().Status(401))
}

// TestM4ApprovalGet_C3_CookieInvalid — 401.
func TestM4ApprovalGet_C3_CookieInvalid(t *testing.T) {
	env := newM4Env(t)
	errorBody(env.expect.GET("/api/approvals/x").
		WithHeader(middleware.MMCookieHeader, "deadbeefdeadbeefdeadbeef").
		Expect().Status(401))
}

// TestM4ApprovalGet_C4_NotAllowed — 第三方 user 查 → 403.
func TestM4ApprovalGet_C4_NotAllowed(t *testing.T) {
	a := newApprovalEnvMatrix(t, 2990)
	cookieOuter, _ := a.seedUser(2998)
	id := a.submitApproval(t, "g-subj", "g-ctn")

	errorBody(a.expect.GET("/api/approvals/"+id).
		WithHeader(middleware.MMCookieHeader, cookieOuter).
		Expect().Status(403)).
		Value("error").String().Contains("allowed")
}

// TestM4ApprovalGet_C5_NotFound — 404.
func TestM4ApprovalGet_C5_NotFound(t *testing.T) {
	a := newApprovalEnvMatrix(t, 3000)
	errorBody(a.expect.GET("/api/approvals/ghost").
		WithHeader(middleware.MMCookieHeader, a.submitterCookie).
		Expect().Status(404)).
		Value("error").String().Contains("not found")
}

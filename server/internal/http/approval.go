package http

import (
	"context"
	"encoding/json"
	"errors"

	"github.com/gin-gonic/gin"

	"im-server/internal/repo"
	"im-server/internal/service"
)

// Approval-related WS event type. Plain string keeps this package decoupled
// from the gateway package; the adapter in cmd/gateway/main.go converts back
// to gateway.WSMessageType on dispatch.
const (
	EventApprovalUpdated MessageEventType = "approval_updated"
)

// UserEventPusher fans a single event to one user (by id). Used for per-user
// notifications where the audience is an arbitrary user set, not a channel
// membership (e.g. approval updates go to requester + approver only). nil-safe
// at call sites.
type UserEventPusher interface {
	PushToUser(userID int64, eventType MessageEventType, payload any)
}

// createApprovalReq is POST /api/approvals body.
type createApprovalReq struct {
	ChannelID  int64            `json:"channel_id"`
	ApproverID int64            `json:"approver_id"`
	Subject    string           `json:"subject"`
	Content    string           `json:"content"`
	Props      *json.RawMessage `json:"props,omitempty"`
}

// decisionReq is POST /api/approvals/:id/approve|reject body.
type decisionReq struct {
	Note string `json:"note"`
}

// approvalDeciderFn matches the svc.Approve / svc.Reject signatures. Used to
// share the handler body between the two decision endpoints.
type approvalDeciderFn func(ctx context.Context, id, callerID int64, note string) (*repo.Approval, error)

// RegisterApprovalRoutes wires the 7 approval endpoints. pusher may be nil
// (tests / offline mode). When non-nil, state-changing endpoints fire an
// approval_updated event to both the requester and the approver.
func RegisterApprovalRoutes(
	authed *gin.RouterGroup,
	svc *service.ApprovalService,
	pusher UserEventPusher,
) {
	// POST /api/approvals — file a new approval.
	authed.POST("/approvals", func(c *gin.Context) {
		uid, ok := userIDFromCtx(c)
		if !ok {
			return
		}
		var in createApprovalReq
		if err := c.ShouldBindJSON(&in); err != nil {
			c.JSON(400, gin.H{"error": "invalid JSON"})
			return
		}
		if in.ChannelID == 0 || in.ApproverID == 0 {
			c.JSON(422, gin.H{"error": "channel_id and approver_id are required"})
			return
		}
		props := ""
		if in.Props != nil {
			props = string(*in.Props)
		}
		a, err := svc.Create(c.Request.Context(), service.CreateApprovalParams{
			ChannelID:   in.ChannelID,
			RequesterID: uid,
			ApproverID:  in.ApproverID,
			Subject:     in.Subject,
			Content:     in.Content,
			Props:       props,
		})
		switch {
		case errors.Is(err, service.ErrApprovalSubjectEmpty):
			c.JSON(422, gin.H{"error": "subject is required"})
		case errors.Is(err, service.ErrApprovalContentEmpty):
			c.JSON(422, gin.H{"error": "content is required"})
		case errors.Is(err, service.ErrNotMember):
			c.JSON(403, gin.H{"error": "not a member of this channel"})
		case errors.Is(err, service.ErrForbidden):
			c.JSON(403, gin.H{"error": "approver must be a manager of this channel"})
		case err != nil:
			c.JSON(500, gin.H{"error": "internal error"})
		default:
			pushApprovalUpdated(pusher, a)
			c.JSON(201, a)
		}
	})

	// POST /api/approvals/:id/approve — approver approves.
	authed.POST("/approvals/:id/approve", decisionEndpoint(svc.Approve, pusher))

	// POST /api/approvals/:id/reject — approver rejects.
	authed.POST("/approvals/:id/reject", decisionEndpoint(svc.Reject, pusher))

	// POST /api/approvals/:id/cancel — requester cancels pending.
	authed.POST("/approvals/:id/cancel", func(c *gin.Context) {
		uid, ok := userIDFromCtx(c)
		if !ok {
			return
		}
		id, ok := pathInt64(c, "id")
		if !ok {
			return
		}
		a, err := svc.Cancel(c.Request.Context(), id, uid)
		switch {
		case errors.Is(err, repo.ErrNotFound):
			c.JSON(404, gin.H{"error": "approval not found"})
		case errors.Is(err, service.ErrApprovalNotRequester):
			c.JSON(403, gin.H{"error": "only the requester may cancel"})
		case errors.Is(err, service.ErrApprovalNotPending):
			c.JSON(409, gin.H{"error": "approval is not pending"})
		case err != nil:
			c.JSON(500, gin.H{"error": "internal error"})
		default:
			pushApprovalUpdated(pusher, a)
			c.JSON(200, a)
		}
	})

	// GET /api/approvals/pending — my pending-to-decide inbox (as approver).
	authed.GET("/approvals/pending", func(c *gin.Context) {
		uid, ok := userIDFromCtx(c)
		if !ok {
			return
		}
		limit := queryIntDefault(c, "limit", 50)
		cursor := int64(queryIntDefault(c, "cursor", 0))
		ls, err := svc.ListPending(c.Request.Context(), uid, limit, cursor)
		if err != nil {
			c.JSON(500, gin.H{"error": "internal error"})
			return
		}
		if ls == nil {
			ls = []repo.Approval{}
		}
		c.JSON(200, gin.H{"approvals": ls})
	})

	// GET /api/approvals/mine — approvals I've filed (any status).
	authed.GET("/approvals/mine", func(c *gin.Context) {
		uid, ok := userIDFromCtx(c)
		if !ok {
			return
		}
		limit := queryIntDefault(c, "limit", 50)
		cursor := int64(queryIntDefault(c, "cursor", 0))
		ls, err := svc.ListMine(c.Request.Context(), uid, limit, cursor)
		if err != nil {
			c.JSON(500, gin.H{"error": "internal error"})
			return
		}
		if ls == nil {
			ls = []repo.Approval{}
		}
		c.JSON(200, gin.H{"approvals": ls})
	})

	// GET /api/approvals/:id — detail (requester or approver only).
	authed.GET("/approvals/:id", func(c *gin.Context) {
		uid, ok := userIDFromCtx(c)
		if !ok {
			return
		}
		id, ok := pathInt64(c, "id")
		if !ok {
			return
		}
		a, err := svc.Get(c.Request.Context(), id, uid)
		switch {
		case errors.Is(err, repo.ErrNotFound):
			c.JSON(404, gin.H{"error": "approval not found"})
		case errors.Is(err, service.ErrForbidden):
			c.JSON(403, gin.H{"error": "not allowed to view this approval"})
		case err != nil:
			c.JSON(500, gin.H{"error": "internal error"})
		default:
			c.JSON(200, a)
		}
	})
}

// decisionEndpoint produces a Gin handler for the approve/reject endpoints.
// The body shape + status mapping is identical — only the service call differs.
func decisionEndpoint(fn approvalDeciderFn, pusher UserEventPusher) gin.HandlerFunc {
	return func(c *gin.Context) {
		uid, ok := userIDFromCtx(c)
		if !ok {
			return
		}
		id, ok := pathInt64(c, "id")
		if !ok {
			return
		}
		var in decisionReq
		// Body is optional (note-only); tolerate empty/missing body.
		_ = c.ShouldBindJSON(&in)
		a, err := fn(c.Request.Context(), id, uid, in.Note)
		switch {
		case errors.Is(err, repo.ErrNotFound):
			c.JSON(404, gin.H{"error": "approval not found"})
		case errors.Is(err, service.ErrApprovalNotApprover):
			c.JSON(403, gin.H{"error": "only the designated approver may decide"})
		case errors.Is(err, service.ErrApprovalNotPending):
			c.JSON(409, gin.H{"error": "approval is not pending"})
		case err != nil:
			c.JSON(500, gin.H{"error": "internal error"})
		default:
			pushApprovalUpdated(pusher, a)
			c.JSON(200, a)
		}
	}
}

// pushApprovalUpdated fans the approval_updated event to the requester and
// approver. Safe to call with pusher == nil (no-op).
func pushApprovalUpdated(pusher UserEventPusher, a *repo.Approval) {
	if pusher == nil || a == nil {
		return
	}
	pusher.PushToUser(a.RequesterID, EventApprovalUpdated, a)
	if a.ApproverID != a.RequesterID {
		pusher.PushToUser(a.ApproverID, EventApprovalUpdated, a)
	}
}

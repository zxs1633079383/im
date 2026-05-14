package http

import (
	"errors"

	"github.com/gin-gonic/gin"

	"im-server/internal/repo"
	"im-server/internal/service"
)

// ChannelEventPusher pushes channel events (e.g. "added") to online users via
// the gateway hub. Mirrors the legacy handler.ChannelEventPusher contract:
// nil = no real-time notifications (the integration / unit tests don't need
// a live hub).
type ChannelEventPusher interface {
	PushChannelEvent(targetUserID string, eventType string, channelID string, name string)
}

// Request bodies. M4: user-id fields are mm UserIDs (24-hex strings).
type createGroupReq struct {
	Name      string   `json:"name"`
	MemberIDs []string `json:"member_ids"`
}

type createDMReq struct {
	PeerID string `json:"peer_id"`
}

type updateChannelReq struct {
	Name      string `json:"name"`
	AvatarURL string `json:"avatar_url"`
}

type addMemberReq struct {
	UserID string `json:"user_id"`
}

// pathInt64 parses :name as a non-empty path parameter.
//
// C012 P-D: post-migration, all entity IDs are TEXT (string) — this helper
// retains its name for diff minimization but now returns (string, bool).
// Returns ok=false and writes a 400 on missing param so the caller can
// early-return — same pattern as userIDFromCtx.
func pathInt64(c *gin.Context, name string) (string, bool) {
	s := c.Param(name)
	if s == "" {
		c.JSON(400, gin.H{"error": "invalid " + name})
		return "", false
	}
	return s, true
}

// RegisterChannelRoutes wires the nine channel endpoints onto authed. authed
// must already have JWT middleware applied (see RegisterProfileRoutes for
// the contract).
//
// pusher is optional; pass nil to disable the real-time WebSocket push that
// notifies newly added members. The legacy handler exposed the same
// WithEventPusher hook — preserving it keeps the gateway/main.go
// hubChannelEventPusher wiring unchanged.
func RegisterChannelRoutes(authed *gin.RouterGroup, svc *service.ChannelService, pusher ChannelEventPusher) {
	// POST /api/channels — create a group, caller becomes owner.
	authed.POST("/channels", func(c *gin.Context) {
		uid, ok := userIDFromCtx(c)
		if !ok {
			return
		}
		var in createGroupReq
		if err := c.ShouldBindJSON(&in); err != nil {
			c.JSON(400, gin.H{"error": "invalid JSON"})
			return
		}
		if in.Name == "" {
			c.JSON(422, gin.H{"error": "name is required"})
			return
		}
		teamID := teamIDFromCtx(c)
		ch, added, err := svc.CreateGroup(c.Request.Context(), uid, teamID, in.Name, in.MemberIDs)
		if err != nil {
			c.JSON(500, gin.H{"error": "internal error"})
			return
		}
		// Fire one "added" event per non-creator member (post-success, like
		// the legacy handler).
		if pusher != nil {
			for _, m := range added {
				pusher.PushChannelEvent(m.UserID, "added", ch.ID, ch.Name)
			}
		}
		c.JSON(201, ch)
	})

	// POST /api/channels/dm — create-or-get a DM. 201 on create, 200 on
	// existing — preserves legacy two-status behaviour.
	authed.POST("/channels/dm", func(c *gin.Context) {
		uid, ok := userIDFromCtx(c)
		if !ok {
			return
		}
		var in createDMReq
		if err := c.ShouldBindJSON(&in); err != nil {
			c.JSON(400, gin.H{"error": "invalid JSON"})
			return
		}
		if in.PeerID == "" {
			c.JSON(422, gin.H{"error": "peer_id is required"})
			return
		}
		teamID := teamIDFromCtx(c)
		ch, created, err := svc.CreateOrGetDM(c.Request.Context(), uid, in.PeerID, teamID)
		switch {
		case errors.Is(err, service.ErrSelfDM):
			c.JSON(422, gin.H{"error": "cannot DM yourself"})
		case err != nil:
			c.JSON(500, gin.H{"error": "internal error"})
		case created:
			c.JSON(201, ch)
		default:
			c.JSON(200, ch)
		}
	})

	// GET /api/channels — list channels for the caller (with previews).
	authed.GET("/channels", func(c *gin.Context) {
		uid, ok := userIDFromCtx(c)
		if !ok {
			return
		}
		previews, err := svc.ListByUser(c.Request.Context(), uid)
		if err != nil {
			c.JSON(500, gin.H{"error": "internal error"})
			return
		}
		if previews == nil {
			previews = []repo.ChannelWithPreview{}
		}
		c.JSON(200, previews)
	})

	// GET /api/channels/:id — single channel (caller must be member).
	authed.GET("/channels/:id", func(c *gin.Context) {
		uid, ok := userIDFromCtx(c)
		if !ok {
			return
		}
		channelID, ok := pathInt64(c, "id")
		if !ok {
			return
		}
		ch, err := svc.GetByID(c.Request.Context(), channelID, uid)
		switch {
		case errors.Is(err, service.ErrNotMember):
			c.JSON(403, gin.H{"error": "not a member of this channel"})
		case errors.Is(err, repo.ErrNotFound):
			c.JSON(404, gin.H{"error": "channel not found"})
		case err != nil:
			c.JSON(500, gin.H{"error": "internal error"})
		default:
			c.JSON(200, ch)
		}
	})

	// PUT /api/channels/:id — update name/avatar (admin or owner only).
	authed.PUT("/channels/:id", func(c *gin.Context) {
		uid, ok := userIDFromCtx(c)
		if !ok {
			return
		}
		channelID, ok := pathInt64(c, "id")
		if !ok {
			return
		}
		var in updateChannelReq
		if err := c.ShouldBindJSON(&in); err != nil {
			c.JSON(400, gin.H{"error": "invalid JSON"})
			return
		}
		ch, err := svc.Update(c.Request.Context(), channelID, uid, in.Name, in.AvatarURL)
		switch {
		case errors.Is(err, service.ErrNotMember):
			c.JSON(403, gin.H{"error": "not a member of this channel"})
		case errors.Is(err, service.ErrForbidden):
			c.JSON(403, gin.H{"error": "admin or owner required"})
		case err != nil:
			c.JSON(500, gin.H{"error": "internal error"})
		default:
			c.JSON(200, ch)
		}
	})

	// POST /api/channels/:id/members — add a member (admin or owner only).
	authed.POST("/channels/:id/members", func(c *gin.Context) {
		uid, ok := userIDFromCtx(c)
		if !ok {
			return
		}
		channelID, ok := pathInt64(c, "id")
		if !ok {
			return
		}
		var in addMemberReq
		if err := c.ShouldBindJSON(&in); err != nil {
			c.JSON(400, gin.H{"error": "invalid JSON"})
			return
		}
		if in.UserID == "" {
			c.JSON(422, gin.H{"error": "user_id is required"})
			return
		}
		chName, err := svc.AddMember(c.Request.Context(), channelID, uid, in.UserID)
		switch {
		case errors.Is(err, service.ErrNotMember):
			c.JSON(403, gin.H{"error": "not a member of this channel"})
		case errors.Is(err, service.ErrForbidden):
			c.JSON(403, gin.H{"error": "admin or owner required"})
		case err != nil:
			c.JSON(500, gin.H{"error": "internal error"})
		default:
			// Fire "added" to the newcomer — same payload shape the
			// group-create fan-out uses (§BACKEND 1.1 channel_event).
			if pusher != nil {
				pusher.PushChannelEvent(in.UserID, "added", channelID, chName)
			}
			c.JSON(201, gin.H{"status": "added"})
		}
	})

	// DELETE /api/channels/:id/members/:user_id — remove a member.
	authed.DELETE("/channels/:id/members/:user_id", func(c *gin.Context) {
		uid, ok := userIDFromCtx(c)
		if !ok {
			return
		}
		channelID, ok := pathInt64(c, "id")
		if !ok {
			return
		}
		targetID := c.Param("user_id")
		if targetID == "" {
			c.JSON(400, gin.H{"error": "invalid user_id"})
			return
		}
		err := svc.RemoveMember(c.Request.Context(), channelID, uid, targetID)
		switch {
		case errors.Is(err, service.ErrNotMember):
			c.JSON(403, gin.H{"error": "not a member of this channel"})
		case errors.Is(err, service.ErrForbidden):
			c.JSON(403, gin.H{"error": "admin or owner required"})
		case errors.Is(err, service.ErrCannotRemoveOwner):
			c.JSON(403, gin.H{"error": "cannot remove the owner"})
		case errors.Is(err, repo.ErrNotFound):
			c.JSON(404, gin.H{"error": "member not found"})
		case err != nil:
			c.JSON(500, gin.H{"error": "internal error"})
		default:
			c.JSON(200, gin.H{"status": "removed"})
		}
	})

	// GET /api/channels/:id/members — list members (caller must be member).
	authed.GET("/channels/:id/members", func(c *gin.Context) {
		uid, ok := userIDFromCtx(c)
		if !ok {
			return
		}
		channelID, ok := pathInt64(c, "id")
		if !ok {
			return
		}
		members, err := svc.ListMembers(c.Request.Context(), channelID, uid)
		switch {
		case errors.Is(err, service.ErrNotMember):
			c.JSON(403, gin.H{"error": "not a member of this channel"})
		case err != nil:
			c.JSON(500, gin.H{"error": "internal error"})
		default:
			if members == nil {
				members = []service.MemberWithUser{}
			}
			c.JSON(200, members)
		}
	})

	registerTopicRoutes(authed, svc)

	// POST /api/channels/:id/leave — remove the caller (owners blocked).
	authed.POST("/channels/:id/leave", func(c *gin.Context) {
		uid, ok := userIDFromCtx(c)
		if !ok {
			return
		}
		channelID, ok := pathInt64(c, "id")
		if !ok {
			return
		}
		err := svc.LeaveChannel(c.Request.Context(), channelID, uid)
		switch {
		case errors.Is(err, service.ErrNotMember):
			c.JSON(403, gin.H{"error": "not a member of this channel"})
		case errors.Is(err, service.ErrOwnerCannotLeave):
			c.JSON(403, gin.H{"error": "owner cannot leave; transfer ownership first"})
		case err != nil:
			c.JSON(500, gin.H{"error": "internal error"})
		default:
			c.JSON(200, gin.H{"status": "left"})
		}
	})
}

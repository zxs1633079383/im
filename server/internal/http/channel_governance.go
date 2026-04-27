package http

import (
	"encoding/json"
	"errors"

	"github.com/gin-gonic/gin"

	"im-server/internal/repo"
	"im-server/internal/service"
)

// patchChannelReq is the body of PATCH /api/channels/:id. Every field is a
// pointer so we can distinguish "omitted" from "explicit zero value". Props
// arrives as a raw JSON object we pass through verbatim to the repo layer.
type patchChannelReq struct {
	Name       *string          `json:"name,omitempty"`
	AvatarURL  *string          `json:"avatar_url,omitempty"`
	Notice     *string          `json:"notice,omitempty"`
	Purpose    *string          `json:"purpose,omitempty"`
	PictureURL *string          `json:"picture_url,omitempty"`
	Props      *json.RawMessage `json:"props,omitempty"`
	Orient     *int16           `json:"orient,omitempty"`
	Permission *int16           `json:"permission,omitempty"`
	IsTop      *bool            `json:"is_top,omitempty"`
}

// patchMemberReq is the body of PATCH /api/channels/:id/members/:user_id.
// is_top is per-user (channel_members.is_top, v0.7.0+); only the caller may
// flip their own row. Role is owner-only; notify_pref is self-only.
type patchMemberReq struct {
	Role       *int16 `json:"role,omitempty"`
	NotifyPref *int16 `json:"notify_pref,omitempty"`
	IsTop      *bool  `json:"is_top,omitempty"`
}

// toRepoFields converts the wire request to the repo/service PatchChannelFields
// struct. Props is stored as a jsonb string so we re-marshal the raw json.
func (p patchChannelReq) toRepoFields() service.PatchChannelFields {
	f := service.PatchChannelFields{
		Name:       p.Name,
		AvatarURL:  p.AvatarURL,
		Notice:     p.Notice,
		Purpose:    p.Purpose,
		PictureURL: p.PictureURL,
		Orient:     p.Orient,
		Permission: p.Permission,
		IsTop:      p.IsTop,
	}
	if p.Props != nil {
		s := string(*p.Props)
		f.Props = &s
	}
	return f
}

// RegisterChannelGovernanceRoutes wires the M2 fine-grained channel endpoints.
// authed must already have JWT middleware applied. svc is the governance
// service; pusher is an optional ChannelEventPusher for notifying members of
// manager/role changes (nil disables notifications).
func RegisterChannelGovernanceRoutes(
	authed *gin.RouterGroup,
	svc *service.ChannelGovernanceService,
	pusher ChannelEventPusher,
) {
	// PATCH /api/channels/:id — fine-grained field patch.
	authed.PATCH("/channels/:id", func(c *gin.Context) {
		uid, ok := userIDFromCtx(c)
		if !ok {
			return
		}
		channelID, ok := pathInt64(c, "id")
		if !ok {
			return
		}
		var in patchChannelReq
		if err := c.ShouldBindJSON(&in); err != nil {
			c.JSON(400, gin.H{"error": "invalid JSON"})
			return
		}
		ch, err := svc.PatchChannel(c.Request.Context(), channelID, uid, in.toRepoFields())
		switch {
		case errors.Is(err, service.ErrNotMember):
			c.JSON(403, gin.H{"error": "not a member of this channel"})
		case errors.Is(err, service.ErrForbidden):
			c.JSON(403, gin.H{"error": "manager or owner required"})
		case errors.Is(err, repo.ErrNotFound):
			c.JSON(404, gin.H{"error": "channel not found"})
		case err != nil:
			c.JSON(500, gin.H{"error": "internal error"})
		default:
			c.JSON(200, ch)
		}
	})

	// POST /api/channels/:id/managers/:user_id — owner adds a manager.
	authed.POST("/channels/:id/managers/:user_id", func(c *gin.Context) {
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
		err := svc.AddManager(c.Request.Context(), channelID, uid, targetID)
		switch {
		case errors.Is(err, service.ErrNotMember):
			c.JSON(403, gin.H{"error": "not a member of this channel"})
		case errors.Is(err, service.ErrForbidden):
			c.JSON(403, gin.H{"error": "owner required"})
		case errors.Is(err, service.ErrTargetNotMember):
			c.JSON(422, gin.H{"error": "target user is not a member of this channel"})
		case err != nil:
			c.JSON(500, gin.H{"error": "internal error"})
		default:
			c.JSON(201, gin.H{"status": "manager_added"})
			if pusher != nil {
				pusher.PushChannelEvent(targetID, "manager_added", channelID, "")
			}
		}
	})

	// DELETE /api/channels/:id/managers/:user_id — owner removes a manager.
	authed.DELETE("/channels/:id/managers/:user_id", func(c *gin.Context) {
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
		err := svc.RemoveManager(c.Request.Context(), channelID, uid, targetID)
		switch {
		case errors.Is(err, service.ErrNotMember):
			c.JSON(403, gin.H{"error": "not a member of this channel"})
		case errors.Is(err, service.ErrForbidden):
			c.JSON(403, gin.H{"error": "owner required"})
		case err != nil:
			c.JSON(500, gin.H{"error": "internal error"})
		default:
			c.JSON(200, gin.H{"status": "manager_removed"})
			if pusher != nil {
				pusher.PushChannelEvent(targetID, "manager_removed", channelID, "")
			}
		}
	})

	// GET /api/channels/:id/managers — list manager user IDs.
	authed.GET("/channels/:id/managers", func(c *gin.Context) {
		uid, ok := userIDFromCtx(c)
		if !ok {
			return
		}
		channelID, ok := pathInt64(c, "id")
		if !ok {
			return
		}
		ids, err := svc.ListManagers(c.Request.Context(), channelID, uid)
		switch {
		case errors.Is(err, service.ErrNotMember):
			c.JSON(403, gin.H{"error": "not a member of this channel"})
		case err != nil:
			c.JSON(500, gin.H{"error": "internal error"})
		default:
			if ids == nil {
				ids = []string{}
			}
			c.JSON(200, gin.H{"managers": ids})
		}
	})

	// POST /api/channels/:id/pins/:message_id — pin a message.
	authed.POST("/channels/:id/pins/:message_id", func(c *gin.Context) {
		uid, ok := userIDFromCtx(c)
		if !ok {
			return
		}
		channelID, ok := pathInt64(c, "id")
		if !ok {
			return
		}
		msgID, ok := pathInt64(c, "message_id")
		if !ok {
			return
		}
		err := svc.PinMessage(c.Request.Context(), channelID, uid, msgID)
		switch {
		case errors.Is(err, service.ErrNotMember):
			c.JSON(403, gin.H{"error": "not a member of this channel"})
		case errors.Is(err, service.ErrForbidden):
			c.JSON(403, gin.H{"error": "manager or owner required"})
		case err != nil:
			c.JSON(500, gin.H{"error": "internal error"})
		default:
			c.JSON(201, gin.H{"status": "pinned"})
		}
	})

	// DELETE /api/channels/:id/pins/:message_id — unpin a message.
	authed.DELETE("/channels/:id/pins/:message_id", func(c *gin.Context) {
		uid, ok := userIDFromCtx(c)
		if !ok {
			return
		}
		channelID, ok := pathInt64(c, "id")
		if !ok {
			return
		}
		msgID, ok := pathInt64(c, "message_id")
		if !ok {
			return
		}
		err := svc.UnpinMessage(c.Request.Context(), channelID, uid, msgID)
		switch {
		case errors.Is(err, service.ErrNotMember):
			c.JSON(403, gin.H{"error": "not a member of this channel"})
		case errors.Is(err, service.ErrForbidden):
			c.JSON(403, gin.H{"error": "manager or owner required"})
		case err != nil:
			c.JSON(500, gin.H{"error": "internal error"})
		default:
			c.JSON(200, gin.H{"status": "unpinned"})
		}
	})

	// GET /api/channels/:id/pins — list pinned message IDs.
	authed.GET("/channels/:id/pins", func(c *gin.Context) {
		uid, ok := userIDFromCtx(c)
		if !ok {
			return
		}
		channelID, ok := pathInt64(c, "id")
		if !ok {
			return
		}
		ids, err := svc.ListPins(c.Request.Context(), channelID, uid)
		switch {
		case errors.Is(err, service.ErrNotMember):
			c.JSON(403, gin.H{"error": "not a member of this channel"})
		case err != nil:
			c.JSON(500, gin.H{"error": "internal error"})
		default:
			if ids == nil {
				ids = []int64{}
			}
			c.JSON(200, gin.H{"pins": ids})
		}
	})

	// PATCH /api/channels/:id/members/:user_id — role (owner-only) and/or
	// notify_pref (self-only) update. We split the two sub-operations by
	// authority: caller-editing-own-pref is allowed; caller-editing-role
	// requires owner.
	authed.PATCH("/channels/:id/members/:user_id", func(c *gin.Context) {
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
		var in patchMemberReq
		if err := c.ShouldBindJSON(&in); err != nil {
			c.JSON(400, gin.H{"error": "invalid JSON"})
			return
		}
		if in.Role == nil && in.NotifyPref == nil && in.IsTop == nil {
			c.JSON(422, gin.H{"error": "no fields to update"})
			return
		}
		if in.Role != nil {
			err := svc.UpdateMemberRole(c.Request.Context(), channelID, uid, targetID, *in.Role)
			switch {
			case errors.Is(err, service.ErrNotMember):
				c.JSON(403, gin.H{"error": "not a member of this channel"})
				return
			case errors.Is(err, service.ErrForbidden):
				c.JSON(403, gin.H{"error": "owner required"})
				return
			case errors.Is(err, repo.ErrNotFound):
				c.JSON(404, gin.H{"error": "member not found"})
				return
			case err != nil:
				c.JSON(500, gin.H{"error": "internal error"})
				return
			}
		}
		if in.NotifyPref != nil {
			// Only allow self-update for notify_pref.
			if targetID != uid {
				c.JSON(403, gin.H{"error": "notify_pref can only be updated for the caller"})
				return
			}
			err := svc.UpdateMemberNotifyPref(c.Request.Context(), channelID, uid, *in.NotifyPref)
			switch {
			case errors.Is(err, service.ErrNotMember):
				c.JSON(403, gin.H{"error": "not a member of this channel"})
				return
			case errors.Is(err, service.ErrInvalidNotifyPref):
				c.JSON(422, gin.H{"error": "invalid notify_pref"})
				return
			case err != nil:
				c.JSON(500, gin.H{"error": "internal error"})
				return
			}
		}
		if in.IsTop != nil {
			// is_top is strictly self-only — pinning a channel to the top
			// of the caller's list, not other members'.
			if targetID != uid {
				c.JSON(403, gin.H{"error": "is_top can only be updated for the caller"})
				return
			}
			err := svc.UpdateMemberIsTop(c.Request.Context(), channelID, uid, *in.IsTop)
			switch {
			case errors.Is(err, service.ErrNotMember):
				c.JSON(403, gin.H{"error": "not a member of this channel"})
				return
			case err != nil:
				c.JSON(500, gin.H{"error": "internal error"})
				return
			}
		}
		c.JSON(200, gin.H{"status": "updated"})
	})
}

package http

import (
	"errors"
	"log/slog"

	"github.com/gin-gonic/gin"

	"im-server/internal/repo"
	"im-server/internal/service"
)

// transferOwnerReq is the body of POST /api/channels/:id/transfer-owner.
// also_leave (optional, default false) flags the spec's "owner clicks 退出群组
// → pick new owner" flow: when true the old owner is removed from the channel
// in the same transaction as the role swap. (C013)
type transferOwnerReq struct {
	NewOwnerID string `json:"new_owner_id" binding:"required"`
	AlsoLeave  bool   `json:"also_leave,omitempty"`
}

// RegisterChannelTransferOwnerRoute wires POST /api/channels/:id/transfer-owner.
// service-side fan-out for channel_member_updated WS is attached separately via
// ChannelService.AttachMemberBroadcaster (same wiring as gap #5 / nickname) so
// this register fn doesn't take a broadcaster — kept symmetric with the
// nickname route.
func RegisterChannelTransferOwnerRoute(
	authed *gin.RouterGroup,
	svc *service.ChannelService,
	log *slog.Logger,
) {
	if log == nil {
		log = slog.Default()
	}
	authed.POST("/channels/:id/transfer-owner",
		func(c *gin.Context) { transferOwnerEndpoint(c, svc, log) })
}

// transferOwnerEndpoint splits the handler body so the closure inside
// RegisterChannelTransferOwnerRoute stays under the 60-line cap.
func transferOwnerEndpoint(c *gin.Context, svc *service.ChannelService, log *slog.Logger) {
	uid, ok := userIDFromCtx(c)
	if !ok {
		return
	}
	channelID, ok := pathInt64(c, "id")
	if !ok {
		return
	}
	var in transferOwnerReq
	if err := c.ShouldBindJSON(&in); err != nil {
		c.JSON(400, gin.H{"error": "invalid JSON"})
		return
	}
	if in.NewOwnerID == "" {
		c.JSON(422, gin.H{"error": "new_owner_id is required"})
		return
	}
	if in.NewOwnerID == uid {
		c.JSON(400, gin.H{"error": "cannot transfer ownership to self"})
		return
	}
	result, err := svc.TransferOwner(c.Request.Context(), service.TransferOwnerParams{
		ChannelID:  channelID,
		CallerID:   uid,
		NewOwnerID: in.NewOwnerID,
		AlsoLeave:  in.AlsoLeave,
	})
	switch {
	case errors.Is(err, service.ErrNotMember):
		c.JSON(403, gin.H{"error": "not a member of this channel"})
	case errors.Is(err, service.ErrOwnerOnly):
		c.JSON(403, gin.H{"error": "only the owner may transfer ownership"})
	case errors.Is(err, service.ErrTargetNotMember):
		c.JSON(404, gin.H{"error": "new owner is not a member of this channel"})
	case errors.Is(err, service.ErrDMNoOwner):
		c.JSON(400, gin.H{"error": "DM channels have no owner"})
	case errors.Is(err, service.ErrTransferToSelf):
		c.JSON(400, gin.H{"error": "cannot transfer ownership to self"})
	case errors.Is(err, repo.ErrGone):
		c.JSON(410, gin.H{"error": "channel already closed"})
	case errors.Is(err, repo.ErrNotFound):
		c.JSON(404, gin.H{"error": "channel not found"})
	case err != nil:
		log.Error("transfer owner", "error", err,
			"channel_id", channelID, "caller_id", uid, "new_owner_id", in.NewOwnerID)
		c.JSON(500, gin.H{"error": "internal error"})
	default:
		c.JSON(200, result)
	}
}

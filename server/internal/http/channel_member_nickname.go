package http

import (
	"errors"
	"log/slog"

	"github.com/gin-gonic/gin"

	"im-server/internal/repo"
	"im-server/internal/service"
)

// memberNicknameReq is the PATCH /channels/:id/members/:user_id/nickname body.
// Empty string clears the per-channel override → fall back to global display
// name. (v0.7.3 gap #5)
type memberNicknameReq struct {
	NickName string `json:"nick_name"`
}

// RegisterMemberNicknameRoute wires PATCH /api/channels/:id/members/:user_id/nickname.
// broadcaster is optional; the service-side fan-out hook (attached separately
// via ChannelService.AttachMemberBroadcaster) ships the channel_member_updated
// WS frame so we don't broadcast twice here.
//
// gap #5 — per-(channel, user) NickName so the client can render "群名片" /
// "群内昵称" without re-fetching the global profile.
func RegisterMemberNicknameRoute(
	authed *gin.RouterGroup,
	svc *service.ChannelService,
	_ MessageEventBroadcaster, // service emits the WS frame; arg kept for symmetry
	log *slog.Logger,
) {
	if log == nil {
		log = slog.Default()
	}
	authed.PATCH("/channels/:id/members/:user_id/nickname",
		func(c *gin.Context) { setNicknameEndpoint(c, svc, log) })
}

// setNicknameEndpoint splits the handler body to keep the request-time
// closure inside RegisterMemberNicknameRoute under the 60-line cap.
func setNicknameEndpoint(c *gin.Context, svc *service.ChannelService, log *slog.Logger) {
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
	var in memberNicknameReq
	if err := c.ShouldBindJSON(&in); err != nil {
		c.JSON(400, gin.H{"error": "invalid JSON"})
		return
	}
	member, err := svc.SetMemberNickname(c.Request.Context(), channelID, uid, targetID, in.NickName)
	switch {
	case errors.Is(err, service.ErrNotMember):
		c.JSON(403, gin.H{"error": "not a member of this channel"})
	case errors.Is(err, service.ErrForbidden):
		c.JSON(403, gin.H{"error": "admin or owner required"})
	case errors.Is(err, service.ErrNicknameTooLong):
		c.JSON(422, gin.H{"error": "nickname must be at most 64 characters"})
	case errors.Is(err, repo.ErrNotFound):
		c.JSON(404, gin.H{"error": "member not found"})
	case err != nil:
		log.Error("set nickname", "error", err,
			"channel_id", channelID, "user_id", uid, "target_id", targetID)
		c.JSON(500, gin.H{"error": "internal error"})
	default:
		c.JSON(200, member)
	}
}

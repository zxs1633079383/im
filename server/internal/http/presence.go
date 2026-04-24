package http

import (
	"errors"
	"net/http"
	"strconv"

	"github.com/gin-gonic/gin"

	"im-server/internal/service"
)

// RegisterPresenceRoutes wires GET /api/presence onto authed.
//
// The only endpoint in this handler is a query for "who is online in this
// channel". Auth is scoped to channel membership — see
// service.PresenceService.OnlineUsersInChannel.
func RegisterPresenceRoutes(authed *gin.RouterGroup, svc *service.PresenceService) {
	authed.GET("/presence", func(c *gin.Context) {
		channelID, err := strconv.ParseInt(c.Query("channel_id"), 10, 64)
		if err != nil || channelID <= 0 {
			c.JSON(http.StatusBadRequest, gin.H{"error": "invalid channel_id"})
			return
		}
		uid, ok := userIDFromCtx(c)
		if !ok {
			return
		}
		online, err := svc.OnlineUsersInChannel(c.Request.Context(), channelID, uid)
		if err != nil {
			writePresenceErr(c, err)
			return
		}
		c.JSON(http.StatusOK, gin.H{"online_user_ids": online})
	})
}

// writePresenceErr maps service-layer sentinels to HTTP status codes. Kept
// out of the handler closure so the mapping is easy to evolve and test.
func writePresenceErr(c *gin.Context, err error) {
	switch {
	case errors.Is(err, service.ErrNotMember):
		c.JSON(http.StatusForbidden, gin.H{"error": err.Error()})
	default:
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
	}
}

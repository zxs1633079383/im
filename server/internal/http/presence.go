package http

import (
	"errors"
	"net/http"
	"strconv"
	"strings"

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

	// GET /api/channels/online-status?channel_ids=1,2,3&include_users=true —
	// batch presence summary, replaces mattermost csesapi /channel/onlineStatus.
	// channel_ids: csv of int64; cap at 50 to bound Redis lookup size.
	// include_users (bool, default false): when true, response carries
	// online_user_ids per entry; when false (or omitted) only online_count.
	authed.GET("/channels/online-status", func(c *gin.Context) {
		uid, ok := userIDFromCtx(c)
		if !ok {
			return
		}
		raw := c.Query("channel_ids")
		if raw == "" {
			c.JSON(http.StatusBadRequest, gin.H{"error": "channel_ids required (csv of int64)"})
			return
		}
		parts := strings.Split(raw, ",")
		if len(parts) > 50 {
			c.JSON(http.StatusBadRequest, gin.H{"error": "at most 50 channel ids per call"})
			return
		}
		channelIDs := make([]int64, 0, len(parts))
		for _, p := range parts {
			id, err := strconv.ParseInt(strings.TrimSpace(p), 10, 64)
			if err != nil || id <= 0 {
				c.JSON(http.StatusBadRequest, gin.H{"error": "invalid channel id: " + p})
				return
			}
			channelIDs = append(channelIDs, id)
		}
		includeUsers := c.Query("include_users") == "true"
		entries, err := svc.BatchOnlineStatus(c.Request.Context(), channelIDs, uid, includeUsers)
		if err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "internal error"})
			return
		}
		c.JSON(http.StatusOK, gin.H{"channels": entries})
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

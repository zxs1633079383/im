package http

import (
	"encoding/json"
	"errors"
	"strconv"

	"github.com/gin-gonic/gin"

	"im-server/internal/repo"
	"im-server/internal/service"
)

// Announcement-related WS event types — plain strings so this package stays
// decoupled from the gateway package. The broadcaster adapter in
// cmd/gateway/main.go converts back to gateway.WSMessageType.
const (
	EventAnnouncementPosted MessageEventType = "announcement_posted"
)

// saveAnnouncementReq is the POST /api/announcements body.
type saveAnnouncementReq struct {
	ChannelID string           `json:"channel_id"`
	Title     string           `json:"title"`
	Content   string           `json:"content"`
	Props     *json.RawMessage `json:"props,omitempty"`
}

// RegisterAnnouncementRoutes wires the 6 announcement endpoints.
//
// broadcaster may be nil (tests / offline mode). When non-nil, Create pushes
// an announcement_posted event to every member of the channel.
func RegisterAnnouncementRoutes(
	authed *gin.RouterGroup,
	svc *service.AnnouncementService,
	broadcaster MessageEventBroadcaster,
) {
	// POST /api/announcements — create a new announcement.
	authed.POST("/announcements", func(c *gin.Context) {
		uid, ok := userIDFromCtx(c)
		if !ok {
			return
		}
		var in saveAnnouncementReq
		if err := c.ShouldBindJSON(&in); err != nil {
			c.JSON(400, gin.H{"error": "invalid JSON"})
			return
		}
		if in.ChannelID == "" {
			c.JSON(422, gin.H{"error": "channel_id is required"})
			return
		}
		props := ""
		if in.Props != nil {
			props = string(*in.Props)
		}
		a, err := svc.Create(c.Request.Context(), service.CreateAnnouncementParams{
			ChannelID: in.ChannelID,
			CreatorID: uid,
			Title:     in.Title,
			Content:   in.Content,
			Props:     props,
		})
		switch {
		case errors.Is(err, service.ErrAnnouncementTitleEmpty):
			c.JSON(422, gin.H{"error": "title is required"})
		case errors.Is(err, service.ErrAnnouncementContentEmpty):
			c.JSON(422, gin.H{"error": "content is required"})
		case errors.Is(err, service.ErrNotMember):
			c.JSON(403, gin.H{"error": "not a member of this channel"})
		case errors.Is(err, service.ErrForbidden):
			c.JSON(403, gin.H{"error": "manager or owner required"})
		case err != nil:
			c.JSON(500, gin.H{"error": "internal error"})
		default:
			if broadcaster != nil {
				broadcaster.BroadcastToMembers(a.ChannelID, EventAnnouncementPosted, a)
			}
			c.JSON(201, a)
		}
	})

	// POST /api/announcements/:id/read — current user acks.
	authed.POST("/announcements/:id/read", func(c *gin.Context) {
		uid, ok := userIDFromCtx(c)
		if !ok {
			return
		}
		id, ok := pathInt64(c, "id")
		if !ok {
			return
		}
		err := svc.Ack(c.Request.Context(), id, uid)
		switch {
		case errors.Is(err, repo.ErrNotFound):
			c.JSON(404, gin.H{"error": "announcement not found"})
		case errors.Is(err, service.ErrAnnouncementDeleted):
			c.JSON(410, gin.H{"error": "announcement deleted"})
		case errors.Is(err, service.ErrNotMember):
			c.JSON(403, gin.H{"error": "not a member of this channel"})
		case err != nil:
			c.JSON(500, gin.H{"error": "internal error"})
		default:
			c.JSON(200, gin.H{"status": "acked"})
		}
	})

	// GET /api/announcements/:id/acks — manager+ views ack list.
	authed.GET("/announcements/:id/acks", func(c *gin.Context) {
		uid, ok := userIDFromCtx(c)
		if !ok {
			return
		}
		id, ok := pathInt64(c, "id")
		if !ok {
			return
		}
		acks, err := svc.ListAcks(c.Request.Context(), id, uid)
		switch {
		case errors.Is(err, repo.ErrNotFound):
			c.JSON(404, gin.H{"error": "announcement not found"})
		case errors.Is(err, service.ErrNotMember):
			c.JSON(403, gin.H{"error": "not a member of this channel"})
		case errors.Is(err, service.ErrForbidden):
			c.JSON(403, gin.H{"error": "manager or owner required"})
		case err != nil:
			c.JSON(500, gin.H{"error": "internal error"})
		default:
			if acks == nil {
				acks = []repo.AnnouncementAck{}
			}
			c.JSON(200, gin.H{"acks": acks})
		}
	})

	// DELETE /api/announcements/:id — creator or manager+ soft-deletes.
	authed.DELETE("/announcements/:id", func(c *gin.Context) {
		uid, ok := userIDFromCtx(c)
		if !ok {
			return
		}
		id, ok := pathInt64(c, "id")
		if !ok {
			return
		}
		err := svc.Delete(c.Request.Context(), id, uid)
		switch {
		case errors.Is(err, repo.ErrNotFound):
			c.JSON(404, gin.H{"error": "announcement not found"})
		case errors.Is(err, service.ErrNotMember):
			c.JSON(403, gin.H{"error": "not a member of this channel"})
		case errors.Is(err, service.ErrForbidden):
			c.JSON(403, gin.H{"error": "creator or manager required"})
		case err != nil:
			c.JSON(500, gin.H{"error": "internal error"})
		default:
			c.JSON(200, gin.H{"status": "deleted"})
		}
	})

	// GET /api/channels/:id/announcements — list for a channel.
	authed.GET("/channels/:id/announcements", func(c *gin.Context) {
		uid, ok := userIDFromCtx(c)
		if !ok {
			return
		}
		channelID, ok := pathInt64(c, "id")
		if !ok {
			return
		}
		limit := queryIntDefault(c, "limit", 50)
		offset := queryIntDefault(c, "offset", 0)
		ann, err := svc.ListByChannel(c.Request.Context(), channelID, uid, limit, offset)
		switch {
		case errors.Is(err, service.ErrNotMember):
			c.JSON(403, gin.H{"error": "not a member of this channel"})
		case err != nil:
			c.JSON(500, gin.H{"error": "internal error"})
		default:
			if ann == nil {
				ann = []repo.Announcement{}
			}
			c.JSON(200, gin.H{"announcements": ann})
		}
	})

	// GET /api/announcements/:id — detail.
	authed.GET("/announcements/:id", func(c *gin.Context) {
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
			c.JSON(404, gin.H{"error": "announcement not found"})
		case errors.Is(err, service.ErrNotMember):
			c.JSON(403, gin.H{"error": "not a member of this channel"})
		case err != nil:
			c.JSON(500, gin.H{"error": "internal error"})
		default:
			c.JSON(200, a)
		}
	})
}

// queryIntDefault parses an int query param with a fallback when missing or
// malformed. Used for pagination limits where a bad value shouldn't 400.
func queryIntDefault(c *gin.Context, name string, def int) int {
	s := c.Query(name)
	if s == "" {
		return def
	}
	v, err := strconv.Atoi(s)
	if err != nil {
		return def
	}
	return v
}

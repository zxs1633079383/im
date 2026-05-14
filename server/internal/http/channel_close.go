package http

import (
	"errors"
	"log/slog"
	"time"

	"github.com/gin-gonic/gin"

	"im-server/internal/repo"
	"im-server/internal/service"
)

// ChannelClosedPayload is the wire body of EventChannelClosed. The cses-client
// reads `deleted_at` directly into `dialog.deleteAt` (channel-level soft
// delete marker) so the conversation list across every device converges
// without re-fetching. Keep the shape stable — the cses-client `ws-normalizer`
// branches on it.
type ChannelClosedPayload struct {
	ChannelID string    `json:"channel_id"`
	ActorID   string    `json:"actor_id"`
	DeletedAt time.Time `json:"deleted_at"`
}

// RegisterChannelCloseRoute wires DELETE /api/channels/:id. The broadcaster
// hook is optional — passing nil keeps the endpoint usable in integration
// tests that don't spin up a hub (analogous to MessageRouteOpts). v0.7.3 gap
// #1 + #3.
func RegisterChannelCloseRoute(
	authed *gin.RouterGroup,
	svc *service.ChannelService,
	broadcaster MessageEventBroadcaster,
	log *slog.Logger,
) {
	if log == nil {
		log = slog.Default()
	}
	authed.DELETE("/channels/:id", func(c *gin.Context) {
		closeChannelEndpoint(c, svc, broadcaster, log)
	})
}

// closeChannelEndpoint is split out so the handler body stays under the
// 60-line per-function rule from rules/golang/coding-style.md.
func closeChannelEndpoint(
	c *gin.Context,
	svc *service.ChannelService,
	broadcaster MessageEventBroadcaster,
	log *slog.Logger,
) {
	uid, ok := userIDFromCtx(c)
	if !ok {
		return
	}
	channelID, ok := pathInt64(c, "id")
	if !ok {
		return
	}
	ch, err := svc.CloseChannel(c.Request.Context(), channelID, uid)
	switch {
	case errors.Is(err, service.ErrNotMember):
		c.JSON(403, gin.H{"error": "not a member of this channel"})
	case errors.Is(err, service.ErrOwnerOnly):
		c.JSON(403, gin.H{"error": "only the owner may close the channel"})
	case errors.Is(err, repo.ErrNotFound):
		c.JSON(404, gin.H{"error": "channel not found"})
	case errors.Is(err, service.ErrChannelClosed):
		// Idempotent: channel was closed by an earlier call. Skip fan-out so
		// repeat callers don't double-broadcast, but still return 200 with
		// the post-close snapshot so the client converges.
		c.JSON(200, ch)
	case err != nil:
		log.Error("close channel", "error", err, "channel_id", channelID, "user_id", uid)
		c.JSON(500, gin.H{"error": "internal error"})
	default:
		broadcastChannelClosed(broadcaster, ch, uid)
		c.JSON(200, ch)
	}
}

// broadcastChannelClosed fans the channel_closed WS frame to every member of
// the now-closed channel. nil-safe: when no broadcaster is wired (test mode)
// it is a no-op. DeletedAt is never nil here because CloseChannel only
// returns a non-error path after stamping the column.
func broadcastChannelClosed(b MessageEventBroadcaster, ch *repo.Channel, actorID string) {
	if b == nil || ch == nil || ch.DeletedAt == nil {
		return
	}
	b.BroadcastToMembers(ch.ID, EventChannelClosed, ChannelClosedPayload{
		ChannelID: ch.ID,
		ActorID:   actorID,
		DeletedAt: *ch.DeletedAt,
	})
}

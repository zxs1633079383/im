package http

import (
	"context"
	"errors"
	"log/slog"
	"strconv"
	"time"

	"github.com/gin-gonic/gin"

	"im-server/internal/repo"
	"im-server/internal/service"
)

// MessagePusher pushes a message to an online user via the gateway hub.
// Mirrors handler.MessagePusher exactly so the existing
// hubMessagePusher adapter in cmd/gateway/main.go works unchanged.
type MessagePusher interface {
	PushMessage(userID int64, msg *repo.Message)
}

// ReadSyncPusher pushes read-receipt sync events to other devices of the same
// user. Mirrors handler.ReadSyncPusher.
type ReadSyncPusher interface {
	PushReadSync(userID int64, channelID int64, readSeq int64)
}

// MessageEventType is a typed alias for WS event name strings. The http
// package stays decoupled from the gateway package by using plain strings;
// the broadcaster implementation in cmd/gateway/main.go converts back to
// gateway.WSMessageType.
type MessageEventType string

const (
	// EventMsgUpdated is fired when a message is edited.
	EventMsgUpdated MessageEventType = "msg_updated"
	// EventMsgDeleted is fired when a message is soft-deleted (revoke).
	EventMsgDeleted MessageEventType = "msg_deleted"
)

// MessageEventBroadcaster fans arbitrary WS events out to every member of a
// channel. Used by edit/delete endpoints where we push a full snapshot of the
// mutated message rather than a PushMsgPayload.
type MessageEventBroadcaster interface {
	BroadcastToMembers(channelID int64, eventType MessageEventType, payload any)
}

// MessageRouteOpts bundles the optional dependency hooks. All hooks are
// nil-safe — pass a zero MessageRouteOpts to disable every side-channel (the
// integration test does this; production wires both).
type MessageRouteOpts struct {
	// Pusher fans out new messages to online channel members. Member listing
	// goes through MessageService.ListMembers (the service already owns its
	// channel store).
	Pusher MessagePusher
	// ReadSyncer pushes read_sync events to the caller's other devices on
	// MarkRead. nil disables cross-device read sync.
	ReadSyncer ReadSyncPusher
	// Broadcaster fans msg_updated / msg_deleted events out to channel
	// members. nil disables edit/revoke broadcasts (endpoints still return
	// success; clients fall back to sync).
	Broadcaster MessageEventBroadcaster
	// Logger is used for non-fatal background errors (push fan-out failures,
	// route-level 500 detail). nil falls back to slog.Default().
	Logger *slog.Logger
}

// sendMessageReq mirrors the legacy handler's body shape exactly so existing
// clients continue to work after the cut-over.
type sendMessageReq struct {
	Content     string  `json:"content"`
	ClientMsgID string  `json:"client_msg_id"`
	MsgType     int16   `json:"msg_type"`
	VisibleTo   []int64 `json:"visible_to"`
	ReplyTo     *int64  `json:"reply_to"`
	FileIDs     []int64 `json:"file_ids"`
}

// fetchMessagesResp wraps the slice in a {"messages": [...]} envelope to match
// the legacy handler's response shape.
type fetchMessagesResp struct {
	Messages []repo.Message `json:"messages"`
}

// editMessageReq is the PATCH /messages/:id body.
type editMessageReq struct {
	Content string `json:"content"`
}

// forwardMessageReq matches the legacy handler's body shape.
type forwardMessageReq struct {
	MessageID        int64   `json:"message_id"`
	TargetChannelIDs []int64 `json:"target_channel_ids"`
}

// RegisterMessageRoutes wires the four message endpoints onto authed. authed
// must already have JWT middleware applied (see RegisterProfileRoutes for the
// contract).
//
// opts.Pusher / opts.ReadSyncer are optional — pass a zero MessageRouteOpts to
// disable all real-time side-channels (tests do this). Production wires both
// hub-backed implementations from cmd/gateway/main.go.
func RegisterMessageRoutes(authed *gin.RouterGroup, svc *service.MessageService, opts MessageRouteOpts) {
	log := opts.Logger
	if log == nil {
		log = slog.Default()
	}

	// POST /api/channels/:id/messages — send a new message.
	authed.POST("/channels/:id/messages", func(c *gin.Context) {
		uid, ok := userIDFromCtx(c)
		if !ok {
			return
		}
		channelID, ok := pathInt64(c, "id")
		if !ok {
			return
		}
		var in sendMessageReq
		if err := c.ShouldBindJSON(&in); err != nil {
			c.JSON(400, gin.H{"error": "invalid JSON"})
			return
		}
		if in.Content == "" {
			c.JSON(422, gin.H{"error": "content is required"})
			return
		}

		msg, err := svc.SendMessage(c.Request.Context(), service.SendParams{
			ChannelID:   channelID,
			SenderID:    uid,
			Content:     in.Content,
			MsgType:     in.MsgType,
			ClientMsgID: in.ClientMsgID,
			VisibleTo:   in.VisibleTo,
			ReplyTo:     in.ReplyTo,
			FileIDs:     in.FileIDs,
		})
		switch {
		case errors.Is(err, service.ErrNotMember):
			c.JSON(403, gin.H{"error": "not a member of this channel"})
			return
		case err != nil:
			log.Error("send message", "error", err, "channel_id", channelID, "user_id", uid)
			c.JSON(500, gin.H{"error": "internal error"})
			return
		}

		// Push to online channel members via WebSocket (non-blocking,
		// best-effort — same goroutine pattern as the legacy handler).
		// Use context.Background() because the request context will be
		// cancelled as soon as the response is written; the goroutine
		// continues independently.
		if opts.Pusher != nil {
			go pushToMembers(context.Background(), svc, opts.Pusher, msg, log)
		}

		c.JSON(201, msg)
	})

	// GET /api/channels/:id/messages — fetch messages with paging.
	// Exactly one of after_seq, before_seq, around_seq may be provided;
	// missing → default "latest N" path.
	authed.GET("/channels/:id/messages", func(c *gin.Context) {
		uid, ok := userIDFromCtx(c)
		if !ok {
			return
		}
		channelID, ok := pathInt64(c, "id")
		if !ok {
			return
		}

		limit := parseLimit(c.Query("limit"))

		var (
			msgs []repo.Message
			err  error
			ctx  = c.Request.Context()
		)
		switch {
		case c.Query("after_seq") != "":
			afterSeq := parseInt64(c.Query("after_seq"), 0)
			msgs, err = svc.FetchAfter(ctx, channelID, uid, afterSeq, limit)
		case c.Query("before_seq") != "":
			beforeSeq := parseInt64(c.Query("before_seq"), 0)
			msgs, err = svc.FetchMessages(ctx, channelID, uid, beforeSeq, limit)
		case c.Query("around_seq") != "":
			aroundSeq := parseInt64(c.Query("around_seq"), 0)
			msgs, err = svc.FetchAround(ctx, channelID, uid, aroundSeq, limit)
		default:
			// Default: latest `limit` messages (before seq=MaxInt64-ish).
			msgs, err = svc.FetchMessages(ctx, channelID, uid, 1<<62, limit)
		}

		switch {
		case errors.Is(err, service.ErrNotMember):
			c.JSON(403, gin.H{"error": "not a member of this channel"})
			return
		case err != nil:
			log.Error("fetch messages", "error", err, "channel_id", channelID, "user_id", uid)
			c.JSON(500, gin.H{"error": "internal error"})
			return
		}
		if msgs == nil {
			msgs = []repo.Message{}
		}
		c.JSON(200, fetchMessagesResp{Messages: msgs})
	})

	// POST /api/channels/:id/read — mark caller's last_read_seq to current seq.
	authed.POST("/channels/:id/read", func(c *gin.Context) {
		uid, ok := userIDFromCtx(c)
		if !ok {
			return
		}
		channelID, ok := pathInt64(c, "id")
		if !ok {
			return
		}

		seq, err := svc.MarkRead(c.Request.Context(), channelID, uid)
		switch {
		case errors.Is(err, service.ErrNotMember):
			c.JSON(403, gin.H{"error": "not a member of this channel"})
			return
		case errors.Is(err, repo.ErrNotFound):
			c.JSON(404, gin.H{"error": "channel not found"})
			return
		case err != nil:
			log.Error("mark read", "error", err, "channel_id", channelID, "user_id", uid)
			c.JSON(500, gin.H{"error": "internal error"})
			return
		}

		if opts.ReadSyncer != nil {
			opts.ReadSyncer.PushReadSync(uid, channelID, seq)
		}
		c.JSON(200, gin.H{"seq": seq})
	})

	// GET /api/channels/:id/messages/around?timestamp=<ms>&limit=<N>
	// Fetch a chronological window centered on a wall-clock timestamp.
	// Half the limit is returned before the timestamp, half after.
	authed.GET("/channels/:id/messages/around", func(c *gin.Context) {
		uid, ok := userIDFromCtx(c)
		if !ok {
			return
		}
		channelID, ok := pathInt64(c, "id")
		if !ok {
			return
		}
		tsMs := parseInt64(c.Query("timestamp"), 0)
		if tsMs <= 0 {
			c.JSON(400, gin.H{"error": "timestamp (unix ms) is required"})
			return
		}
		limit := parseLimit(c.Query("limit"))
		ts := time.UnixMilli(tsMs).UTC()

		msgs, hasOlder, hasNewer, err := svc.FetchAroundTimestamp(
			c.Request.Context(), channelID, uid, ts, limit,
		)
		switch {
		case errors.Is(err, service.ErrNotMember):
			c.JSON(403, gin.H{"error": "not a member of this channel"})
			return
		case err != nil:
			log.Error("fetch around timestamp",
				"error", err, "channel_id", channelID, "user_id", uid)
			c.JSON(500, gin.H{"error": "internal error"})
			return
		}
		if msgs == nil {
			msgs = []repo.Message{}
		}
		c.JSON(200, gin.H{
			"messages":   msgs,
			"has_older":  hasOlder,
			"has_newer":  hasNewer,
		})
	})

	// GET /api/messages/:id/readers — list user IDs who have read up to (or
	// past) this message. Cursor-paginated on user_id; default limit 50.
	authed.GET("/messages/:id/readers", func(c *gin.Context) {
		uid, ok := userIDFromCtx(c)
		if !ok {
			return
		}
		msgID, ok := pathInt64(c, "id")
		if !ok {
			return
		}
		limit := parseLimit(c.Query("limit"))
		cursor := parseInt64(c.Query("cursor"), 0)

		readers, next, err := svc.GetReaders(c.Request.Context(), msgID, uid, limit, cursor)
		switch {
		case errors.Is(err, repo.ErrNotFound):
			c.JSON(404, gin.H{"error": "message not found"})
			return
		case errors.Is(err, service.ErrNotMember):
			c.JSON(403, gin.H{"error": "not a member of this channel"})
			return
		case err != nil:
			log.Error("get readers", "error", err, "msg_id", msgID, "user_id", uid)
			c.JSON(500, gin.H{"error": "internal error"})
			return
		}
		if readers == nil {
			readers = []int64{}
		}
		c.JSON(200, gin.H{"readers": readers, "next_cursor": next})
	})

	// GET /api/messages/:id/replies — list every non-deleted reply to the
	// given root message. Caller must be a member of the channel.
	authed.GET("/messages/:id/replies", func(c *gin.Context) {
		uid, ok := userIDFromCtx(c)
		if !ok {
			return
		}
		rootID, ok := pathInt64(c, "id")
		if !ok {
			return
		}
		msgs, err := svc.GetReplies(c.Request.Context(), rootID, uid)
		switch {
		case errors.Is(err, repo.ErrNotFound):
			c.JSON(404, gin.H{"error": "message not found"})
			return
		case errors.Is(err, service.ErrNotMember):
			c.JSON(403, gin.H{"error": "not a member of this channel"})
			return
		case err != nil:
			log.Error("fetch replies", "error", err, "root_id", rootID, "user_id", uid)
			c.JSON(500, gin.H{"error": "internal error"})
			return
		}
		if msgs == nil {
			msgs = []repo.Message{}
		}
		c.JSON(200, fetchMessagesResp{Messages: msgs})
	})

	// PATCH /api/messages/:id — edit the content of a message.
	// Caller must be the original sender and the message must not already be
	// soft-deleted. Returns the refreshed message snapshot.
	authed.PATCH("/messages/:id", func(c *gin.Context) {
		uid, ok := userIDFromCtx(c)
		if !ok {
			return
		}
		msgID, ok := pathInt64(c, "id")
		if !ok {
			return
		}
		var in editMessageReq
		if err := c.ShouldBindJSON(&in); err != nil {
			c.JSON(400, gin.H{"error": "invalid JSON"})
			return
		}
		if in.Content == "" {
			c.JSON(422, gin.H{"error": "content is required"})
			return
		}

		msg, err := svc.EditMessage(c.Request.Context(), msgID, uid, in.Content)
		switch {
		case errors.Is(err, repo.ErrNotFound):
			c.JSON(404, gin.H{"error": "message not found"})
			return
		case errors.Is(err, repo.ErrForbidden):
			c.JSON(403, gin.H{"error": "not the message sender"})
			return
		case errors.Is(err, repo.ErrGone):
			c.JSON(410, gin.H{"error": "message already deleted"})
			return
		case err != nil:
			log.Error("edit message", "error", err, "msg_id", msgID, "user_id", uid)
			c.JSON(500, gin.H{"error": "internal error"})
			return
		}

		// Broadcast msg_updated carrying the full refreshed message.
		if opts.Broadcaster != nil {
			opts.Broadcaster.BroadcastToMembers(msg.ChannelID, EventMsgUpdated, msg)
		}
		c.JSON(200, msg)
	})

	// DELETE /api/messages/:id — soft-delete a message (revoke).
	// Caller must be the original sender. Already-deleted messages are
	// idempotent no-ops (200 OK, skip fan-out).
	authed.DELETE("/messages/:id", func(c *gin.Context) {
		uid, ok := userIDFromCtx(c)
		if !ok {
			return
		}
		msgID, ok := pathInt64(c, "id")
		if !ok {
			return
		}

		msg, err := svc.DeleteMessage(c.Request.Context(), msgID, uid)
		switch {
		case errors.Is(err, repo.ErrNotFound):
			c.JSON(404, gin.H{"error": "message not found"})
			return
		case errors.Is(err, repo.ErrForbidden):
			c.JSON(403, gin.H{"error": "not the message sender"})
			return
		case errors.Is(err, repo.ErrGone):
			// Already deleted — idempotent success, no fan-out.
			c.JSON(200, gin.H{"ok": true, "already_deleted": true})
			return
		case err != nil:
			log.Error("delete message", "error", err, "msg_id", msgID, "user_id", uid)
			c.JSON(500, gin.H{"error": "internal error"})
			return
		}

		// Broadcast msg_deleted to every member of the channel.
		if opts.Broadcaster != nil {
			opts.Broadcaster.BroadcastToMembers(msg.ChannelID, EventMsgDeleted, gin.H{
				"msg_id":     msg.ID,
				"channel_id": msg.ChannelID,
				"deleted_at": msg.DeletedAt,
			})
		}
		c.JSON(200, gin.H{"ok": true})
	})

	// POST /api/messages/forward — copy a source message into N target channels.
	authed.POST("/messages/forward", func(c *gin.Context) {
		uid, ok := userIDFromCtx(c)
		if !ok {
			return
		}
		var in forwardMessageReq
		if err := c.ShouldBindJSON(&in); err != nil {
			c.JSON(400, gin.H{"error": "invalid JSON"})
			return
		}
		if in.MessageID == 0 {
			c.JSON(422, gin.H{"error": "message_id is required"})
			return
		}
		if len(in.TargetChannelIDs) == 0 {
			c.JSON(422, gin.H{"error": "target_channel_ids must not be empty"})
			return
		}
		if len(in.TargetChannelIDs) > 10 {
			c.JSON(422, gin.H{"error": "at most 10 target channels allowed"})
			return
		}

		forwarded, err := svc.ForwardMessages(c.Request.Context(), uid, service.ForwardParams{
			MessageID:        in.MessageID,
			TargetChannelIDs: in.TargetChannelIDs,
		})
		switch {
		case errors.Is(err, service.ErrSourceNotFound):
			c.JSON(404, gin.H{"error": "source message not found"})
			return
		case errors.Is(err, service.ErrSourceNotMember):
			c.JSON(403, gin.H{"error": "not a member of the source channel"})
			return
		case err != nil:
			log.Error("forward messages", "error", err, "user_id", uid)
			c.JSON(500, gin.H{"error": "internal error"})
			return
		}
		c.JSON(201, gin.H{"messages": forwarded})
	})
}

// pushToMembers fans out a push notification to all online channel members.
// Directed messages get a phantom for non-visible users — same semantics as
// the legacy handler.pushToMembers.
func pushToMembers(ctx context.Context, svc *service.MessageService, pusher MessagePusher, msg *repo.Message, log *slog.Logger) {
	members, err := svc.ListMembers(ctx, msg.ChannelID)
	if err != nil {
		log.Error("list members for push", "channel_id", msg.ChannelID, "error", err)
		return
	}
	for _, m := range members {
		pushMsg := msg
		if msg.VisibleTo != nil && !msg.IsVisibleTo(m.UserID) {
			pushMsg = &repo.Message{
				ChannelID: msg.ChannelID,
				Seq:       msg.Seq,
				MsgType:   repo.MsgTypePhantom,
				CreatedAt: msg.CreatedAt,
			}
		}
		pusher.PushMessage(m.UserID, pushMsg)
	}
}

// parseLimit returns the messages-per-page limit from the query string,
// clamping to [1, 100] with a default of 50 to match the legacy handler.
func parseLimit(s string) int {
	v := parseInt64(s, 50)
	if v > 100 {
		v = 100
	}
	if v < 1 {
		v = 1
	}
	return int(v)
}

// parseInt64 parses s as int64, returning def on empty or invalid input.
func parseInt64(s string, def int64) int64 {
	if s == "" {
		return def
	}
	v, err := strconv.ParseInt(s, 10, 64)
	if err != nil {
		return def
	}
	return v
}

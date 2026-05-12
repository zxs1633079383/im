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

// MessagePusher fans a chat message out to a group of online users on the
// given channel. BroadcastMessage is the batch primitive: it carries N user
// IDs so the gateway can collapse routing.Lookup + producer.Send to one
// round-trip per destination pod.
//
// Callers that need distinct payloads per user (e.g. directed messages with
// phantom stripping) invoke BroadcastMessage twice — once for visible users
// with the real message, once for the phantom bucket with a stripped variant.
type MessagePusher interface {
	BroadcastMessage(channelID int64, userIDs []string, msg *repo.Message)
}

// ReadSyncPusher pushes read-receipt sync events to other devices of the same
// user. Mirrors handler.ReadSyncPusher.
type ReadSyncPusher interface {
	PushReadSync(userID string, channelID, readSeq int64)
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
	// EventChannelClosed is fired when an owner解散群聊 (v0.7.3 gap #1+#3).
	EventChannelClosed MessageEventType = "channel_closed"
	// EventChannelMemberUpdated is fired on add / remove / nickname (gap #4+#5).
	EventChannelMemberUpdated MessageEventType = "channel_member_updated"
	// EventScheduleCreated is pushed to the sender's other devices when a
	// scheduled message is enqueued (v0.7.3 gap #7).
	EventScheduleCreated MessageEventType = "schedule_created"
	// EventScheduleCanceled is pushed to the sender's other devices when a
	// pending scheduled message is cancelled (v0.7.3 gap #7).
	EventScheduleCanceled MessageEventType = "schedule_canceled"
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
	VisibleTo   []string `json:"visible_to"`
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
		fanoutStart := time.Now()
		defer func() {
			if m := metrics(); m.FanoutE2E != nil {
				m.FanoutE2E.Record(c.Request.Context(),
					float64(time.Since(fanoutStart).Milliseconds()))
			}
		}()
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

		teamID := teamIDFromCtx(c)
		var teamPtr *string
		if teamID != "" {
			teamPtr = &teamID
		}
		msg, err := svc.SendMessage(c.Request.Context(), service.SendParams{
			ChannelID:   channelID,
			SenderID:    uid,
			TeamID:      teamPtr,
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
		cursor := c.Query("cursor")

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
			readers = []string{}
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

	// POST /api/messages/batch — fan a single content out to N channels.
	// Replaces mattermost csesapi /posts/createPosts. ChannelIDs the caller
	// is not a member of are silently skipped (best-effort, mirroring
	// ForwardMessages). 201 with the inserted messages slice.
	authed.POST("/messages/batch", func(c *gin.Context) {
		uid, ok := userIDFromCtx(c)
		if !ok {
			return
		}
		var in struct {
			ChannelIDs  []int64 `json:"channel_ids"`
			Content     string  `json:"content"`
			MsgType     int16   `json:"msg_type"`
			ClientMsgID string  `json:"client_msg_id"`
		}
		if err := c.ShouldBindJSON(&in); err != nil {
			c.JSON(400, gin.H{"error": "invalid JSON"})
			return
		}
		if len(in.ChannelIDs) == 0 {
			c.JSON(422, gin.H{"error": "channel_ids must not be empty"})
			return
		}
		if len(in.ChannelIDs) > 50 {
			c.JSON(422, gin.H{"error": "at most 50 target channels allowed"})
			return
		}
		if in.Content == "" {
			c.JSON(422, gin.H{"error": "content is required"})
			return
		}
		teamID := teamIDFromCtx(c)
		var teamPtr *string
		if teamID != "" {
			teamPtr = &teamID
		}
		out, err := svc.BatchSendMessages(c.Request.Context(), service.BatchSendParams{
			ChannelIDs:  in.ChannelIDs,
			SenderID:    uid,
			TeamID:      teamPtr,
			Content:     in.Content,
			MsgType:     in.MsgType,
			ClientMsgID: in.ClientMsgID,
		})
		if err != nil {
			log.Error("batch send", "error", err, "user_id", uid)
			c.JSON(422, gin.H{"error": err.Error()})
			return
		}
		// Fan out push to online members per inserted row, same hook as the
		// single-send path so realtime delivery stays consistent.
		if opts.Pusher != nil {
			for _, m := range out {
				go pushToMembers(context.Background(), svc, opts.Pusher, m, log)
			}
		}
		c.JSON(201, gin.H{"messages": out})
	})

	// GET /api/messages/:id/after?limit=N — return up to N messages with seq
	// strictly greater than the given message's seq, same channel. Replaces
	// mattermost csesapi /posts/getPostsAfterFromSegment. limit defaults to
	// 50, capped at 200 in the service to bound memory.
	authed.GET("/messages/:id/after", func(c *gin.Context) {
		uid, ok := userIDFromCtx(c)
		if !ok {
			return
		}
		messageID, ok := pathInt64(c, "id")
		if !ok {
			return
		}
		limit := parseLimit(c.Query("limit"))
		msgs, err := svc.MessagesAfter(c.Request.Context(), messageID, uid, limit)
		switch {
		case errors.Is(err, service.ErrSourceNotFound):
			c.JSON(404, gin.H{"error": "message not found"})
		case errors.Is(err, service.ErrSourceNotMember):
			c.JSON(403, gin.H{"error": "not a member of the channel"})
		case err != nil:
			log.Error("messages after", "error", err, "user_id", uid)
			c.JSON(500, gin.H{"error": "internal error"})
		default:
			if msgs == nil {
				msgs = []repo.Message{}
			}
			c.JSON(200, fetchMessagesResp{Messages: msgs})
		}
	})

	// POST /api/messages/:id/received — template-message receipt. Wired in
	// message_template.go to keep the handler near its decision notes. See
	// decisions/no-traffic-rollback + GOAL §4 #1 for why we reuse
	// msg_updated rather than introduce a new WS event.
	registerTemplateReceivedRoute(authed, svc, opts)

	// GET /api/messages/read-stats?ids=1,2,3 — batched per-message read
	// summary. Replaces the cses-client readBits bitmap; see read_stats.go
	// for the contract and docs/CSES_CLIENT_CUTOVER.md decision 1c for the
	// design rationale.
	registerReadStatsRoute(authed, svc, opts)
}

// pushToMembers fans out a push notification to all online channel members.
// Directed messages bucket recipients into a visible group and a phantom
// group; each bucket ships in one BroadcastMessage call so the gateway can
// collapse per-member routing.Lookup + producer.Send into one aggregated
// Pulsar message per destination pod.
func pushToMembers(ctx context.Context, svc *service.MessageService, pusher MessagePusher, msg *repo.Message, log *slog.Logger) {
	members, err := svc.ListMembers(ctx, msg.ChannelID)
	if err != nil {
		log.Error("list members for push", "channel_id", msg.ChannelID, "error", err)
		return
	}
	if len(members) == 0 {
		return
	}
	if msg.VisibleTo == nil {
		uids := extractMemberUIDs(members)
		pusher.BroadcastMessage(msg.ChannelID, uids, msg)
		return
	}
	visible, phantom := bucketByVisibility(members, msg)
	if len(visible) > 0 {
		pusher.BroadcastMessage(msg.ChannelID, visible, msg)
	}
	if len(phantom) > 0 {
		pusher.BroadcastMessage(msg.ChannelID, phantom, phantomVariant(msg))
	}
}

// extractMemberUIDs returns the mm UserIDs of the given channel members.
func extractMemberUIDs(members []repo.ChannelMember) []string {
	out := make([]string, 0, len(members))
	for _, m := range members {
		out = append(out, m.UserID)
	}
	return out
}

// bucketByVisibility splits directed-message recipients into users who can
// see the full content and users who only see a phantom placeholder. The
// sender always lands in the visible bucket so they see their own message.
func bucketByVisibility(members []repo.ChannelMember, msg *repo.Message) (visible, phantom []string) {
	for _, m := range members {
		if msg.IsVisibleTo(m.UserID) || m.UserID == msg.SenderID {
			visible = append(visible, m.UserID)
		} else {
			phantom = append(phantom, m.UserID)
		}
	}
	return visible, phantom
}

// phantomVariant returns a stripped copy of msg that carries only the seq
// skeleton a phantom recipient needs (no content, no visible_to, msg_type
// flipped to MsgTypePhantom).
func phantomVariant(msg *repo.Message) *repo.Message {
	return &repo.Message{
		ChannelID: msg.ChannelID,
		Seq:       msg.Seq,
		MsgType:   repo.MsgTypePhantom,
		CreatedAt: msg.CreatedAt,
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

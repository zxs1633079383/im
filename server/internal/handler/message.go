package handler

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"strconv"

	"im-server/internal/model"
)

// ---------- store interfaces ----------

// MsgStore is the subset of store.MessageStore used by MessageHandler.
type MsgStore interface {
	Send(ctx context.Context, msg *model.Message) error
	GetByID(ctx context.Context, id int64) (*model.Message, error)
	FetchForUser(ctx context.Context, channelID, userID int64, afterSeq int64, limit int) ([]model.Message, error)
	FetchBefore(ctx context.Context, channelID, userID int64, beforeSeq int64, limit int) ([]model.Message, error)
	FetchAround(ctx context.Context, channelID, userID int64, aroundSeq int64, limit int) ([]model.Message, error)
}

// MsgChannelStore is the subset of store.ChannelStore used by MessageHandler.
type MsgChannelStore interface {
	GetMember(ctx context.Context, channelID, userID int64) (*model.ChannelMember, error)
	MarkRead(ctx context.Context, channelID, userID, seq int64) error
	GetByID(ctx context.Context, id int64) (*model.Channel, error)
}

// ReadSyncPusher pushes read_sync events to other devices of the same user.
// Implemented by *gateway.Hub (via an adapter in main.go).
type ReadSyncPusher interface {
	PushReadSync(userID int64, channelID int64, readSeq int64)
}

// MsgAttachStore links files to messages.
type MsgAttachStore interface {
	AttachToMessage(ctx context.Context, messageID, fileID int64) error
}

// ---------- handler ----------

// MessageHandler serves message send/fetch/read endpoints.
type MessageHandler struct {
	messages    MsgStore
	channels    MsgChannelStore
	readSyncer  ReadSyncPusher // nil = no cross-device read sync (e.g. in tests)
	attachments MsgAttachStore // nil = no attachment support
	log         *slog.Logger
}

func NewMessageHandler(messages MsgStore, channels MsgChannelStore, log *slog.Logger) *MessageHandler {
	return &MessageHandler{messages: messages, channels: channels, log: log}
}

// WithReadSyncer sets the cross-device read sync pusher. Call after construction.
func (h *MessageHandler) WithReadSyncer(rs ReadSyncPusher) *MessageHandler {
	h.readSyncer = rs
	return h
}

// WithAttachments sets the attachment store. Call after construction.
func (h *MessageHandler) WithAttachments(a MsgAttachStore) *MessageHandler {
	h.attachments = a
	return h
}

// ---------- request/response types ----------

type sendMessageBody struct {
	Content     string  `json:"content"`
	ClientMsgID string  `json:"client_msg_id"`
	MsgType     int16   `json:"msg_type"`
	VisibleTo   []int64 `json:"visible_to"`
	ReplyTo     *int64  `json:"reply_to"`
	FileIDs     []int64 `json:"file_ids"` // optional: pre-uploaded file IDs to attach
}

type fetchMessagesResponse struct {
	Messages []model.Message `json:"messages"`
}

// ---------- POST /api/channels/{id}/messages ----------

// SendMessage persists a new message in the channel.
// Caller must be a channel member.
// Body: { content, client_msg_id, msg_type, visible_to[], reply_to }
// Returns the persisted message (with server-assigned seq and id).
func (h *MessageHandler) SendMessage(w http.ResponseWriter, r *http.Request) {
	claims, ok := claimsFromCtx(r)
	if !ok {
		writeError(w, http.StatusUnauthorized, "unauthorized")
		return
	}

	channelID, ok := pathID(r, "id")
	if !ok {
		writeError(w, http.StatusBadRequest, "invalid channel id")
		return
	}

	// Verify membership
	if _, err := h.channels.GetMember(r.Context(), channelID, claims.UserID); err != nil {
		writeError(w, http.StatusForbidden, "not a member of this channel")
		return
	}

	var body sendMessageBody
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON")
		return
	}
	if body.Content == "" {
		writeError(w, http.StatusUnprocessableEntity, "content is required")
		return
	}
	msgType := model.MsgType(body.MsgType)
	if msgType == 0 {
		msgType = model.MsgTypeText
	}

	msg := &model.Message{
		ChannelID:   channelID,
		SenderID:    claims.UserID,
		ClientMsgID: body.ClientMsgID,
		MsgType:     msgType,
		Content:     body.Content,
		VisibleTo:   body.VisibleTo,
		ReplyTo:     body.ReplyTo,
	}

	if err := h.messages.Send(r.Context(), msg); err != nil {
		h.log.Error("send message", "error", err)
		writeError(w, http.StatusInternalServerError, "internal error")
		return
	}

	// Link file attachments if provided
	if h.attachments != nil && len(body.FileIDs) > 0 {
		for _, fid := range body.FileIDs {
			if err := h.attachments.AttachToMessage(r.Context(), msg.ID, fid); err != nil {
				h.log.Error("attach file to message", "file_id", fid, "error", err)
				// Non-fatal: message already sent, log and continue
			}
		}
	}

	writeJSON(w, http.StatusCreated, msg)
}

// ---------- GET /api/channels/{id}/messages ----------

// FetchMessages returns messages from the channel.
// Caller must be a channel member.
// Exactly one of after_seq, before_seq, around_seq may be provided.
// Optional query param: limit (default 50, max 100).
// Phantom messages appear for directed messages the caller cannot see.
func (h *MessageHandler) FetchMessages(w http.ResponseWriter, r *http.Request) {
	claims, ok := claimsFromCtx(r)
	if !ok {
		writeError(w, http.StatusUnauthorized, "unauthorized")
		return
	}

	channelID, ok := pathID(r, "id")
	if !ok {
		writeError(w, http.StatusBadRequest, "invalid channel id")
		return
	}

	if _, err := h.channels.GetMember(r.Context(), channelID, claims.UserID); err != nil {
		writeError(w, http.StatusForbidden, "not a member of this channel")
		return
	}

	q := r.URL.Query()
	limit := parseIntParam(q.Get("limit"), 50)
	if limit > 100 {
		limit = 100
	}
	if limit < 1 {
		limit = 1
	}

	var (
		msgs []model.Message
		err  error
	)

	switch {
	case q.Get("after_seq") != "":
		afterSeq := parseIntParam(q.Get("after_seq"), 0)
		msgs, err = h.messages.FetchForUser(r.Context(), channelID, claims.UserID, afterSeq, int(limit))
	case q.Get("before_seq") != "":
		beforeSeq := parseIntParam(q.Get("before_seq"), 0)
		msgs, err = h.messages.FetchBefore(r.Context(), channelID, claims.UserID, beforeSeq, int(limit))
	case q.Get("around_seq") != "":
		aroundSeq := parseIntParam(q.Get("around_seq"), 0)
		msgs, err = h.messages.FetchAround(r.Context(), channelID, claims.UserID, aroundSeq, int(limit))
	default:
		// Default: fetch the latest `limit` messages (before seq=MaxInt64)
		msgs, err = h.messages.FetchBefore(r.Context(), channelID, claims.UserID, 1<<62, int(limit))
	}

	if err != nil {
		h.log.Error("fetch messages", "error", err)
		writeError(w, http.StatusInternalServerError, "internal error")
		return
	}
	if msgs == nil {
		msgs = []model.Message{}
	}
	writeJSON(w, http.StatusOK, fetchMessagesResponse{Messages: msgs})
}

// ---------- POST /api/channels/{id}/read ----------

// MarkRead updates the caller's last_read_seq to the channel's current seq.
// Caller must be a channel member.
func (h *MessageHandler) MarkRead(w http.ResponseWriter, r *http.Request) {
	claims, ok := claimsFromCtx(r)
	if !ok {
		writeError(w, http.StatusUnauthorized, "unauthorized")
		return
	}

	channelID, ok := pathID(r, "id")
	if !ok {
		writeError(w, http.StatusBadRequest, "invalid channel id")
		return
	}

	if _, err := h.channels.GetMember(r.Context(), channelID, claims.UserID); err != nil {
		writeError(w, http.StatusForbidden, "not a member of this channel")
		return
	}

	ch, err := h.channels.GetByID(r.Context(), channelID)
	if err != nil {
		writeError(w, http.StatusNotFound, "channel not found")
		return
	}

	if err := h.channels.MarkRead(r.Context(), channelID, claims.UserID, ch.Seq); err != nil {
		h.log.Error("mark read", "error", err)
		writeError(w, http.StatusInternalServerError, "internal error")
		return
	}

	if h.readSyncer != nil {
		h.readSyncer.PushReadSync(claims.UserID, channelID, ch.Seq)
	}

	writeJSON(w, http.StatusOK, map[string]int64{"seq": ch.Seq})
}

// ---------- POST /api/messages/forward ----------

type forwardMessageBody struct {
	MessageID        int64   `json:"message_id"`
	TargetChannelIDs []int64 `json:"target_channel_ids"`
}

// ForwardMessages handles POST /api/messages/forward.
// It copies the source message (with forwarded_from set) to each target channel
// provided the caller is a member of each target channel.
// Returns a list of newly created messages.
func (h *MessageHandler) ForwardMessages(w http.ResponseWriter, r *http.Request) {
	claims, ok := claimsFromCtx(r)
	if !ok {
		writeError(w, http.StatusUnauthorized, "unauthorized")
		return
	}

	var body forwardMessageBody
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON")
		return
	}
	if body.MessageID == 0 {
		writeError(w, http.StatusUnprocessableEntity, "message_id is required")
		return
	}
	if len(body.TargetChannelIDs) == 0 {
		writeError(w, http.StatusUnprocessableEntity, "target_channel_ids must not be empty")
		return
	}
	if len(body.TargetChannelIDs) > 10 {
		writeError(w, http.StatusUnprocessableEntity, "at most 10 target channels allowed")
		return
	}

	// Fetch source message
	source, err := h.messages.GetByID(r.Context(), body.MessageID)
	if err != nil {
		writeError(w, http.StatusNotFound, "source message not found")
		return
	}

	// Verify caller is a member of the source channel
	if _, err := h.channels.GetMember(r.Context(), source.ChannelID, claims.UserID); err != nil {
		writeError(w, http.StatusForbidden, "not a member of the source channel")
		return
	}

	forwarded := make([]*model.Message, 0, len(body.TargetChannelIDs))
	for _, targetID := range body.TargetChannelIDs {
		// Verify caller is a member of the target channel
		if _, err := h.channels.GetMember(r.Context(), targetID, claims.UserID); err != nil {
			// Skip channels the caller is not a member of (silent skip)
			h.log.Warn("forward skipped: not a member", "channel_id", targetID, "user_id", claims.UserID)
			continue
		}

		fwd := &model.Message{
			ChannelID:     targetID,
			SenderID:      claims.UserID,
			MsgType:       source.MsgType,
			Content:       source.Content,
			ForwardedFrom: &source.ID,
		}

		if err := h.messages.Send(r.Context(), fwd); err != nil {
			h.log.Error("forward send", "error", err, "target_channel", targetID)
			// Non-fatal: continue with remaining targets
			continue
		}
		forwarded = append(forwarded, fwd)
	}

	writeJSON(w, http.StatusCreated, map[string][]*model.Message{"messages": forwarded})
}

// ---------- helpers ----------

// parseIntParam parses a query parameter string as int64, returning def on error.
func parseIntParam(s string, def int64) int64 {
	if s == "" {
		return def
	}
	v, err := strconv.ParseInt(s, 10, 64)
	if err != nil {
		return def
	}
	return v
}

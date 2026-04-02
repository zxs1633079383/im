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

// ---------- handler ----------

// MessageHandler serves message send/fetch/read endpoints.
type MessageHandler struct {
	messages MsgStore
	channels MsgChannelStore
	log      *slog.Logger
}

func NewMessageHandler(messages MsgStore, channels MsgChannelStore, log *slog.Logger) *MessageHandler {
	return &MessageHandler{messages: messages, channels: channels, log: log}
}

// ---------- request/response types ----------

type sendMessageBody struct {
	Content     string  `json:"content"`
	ClientMsgID string  `json:"client_msg_id"`
	MsgType     int16   `json:"msg_type"`
	VisibleTo   []int64 `json:"visible_to"`
	ReplyTo     *int64  `json:"reply_to"`
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

	writeJSON(w, http.StatusOK, map[string]int64{"seq": ch.Seq})
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

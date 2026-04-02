package handler

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"

	"im-server/internal/model"
)

const (
	syncGapThreshold = 100 // gaps larger than this return has_more instead of full messages
	syncMsgLimit     = 50  // messages to return per channel for large gaps / new channels
)

// SyncChannelStore is the store interface needed by SyncHandler.
type SyncChannelStore interface {
	// GetMemberChannelSeqs returns the current server seq for every channel
	// the user belongs to. Returns map[channel_id]seq.
	GetMemberChannelSeqs(ctx context.Context, userID int64) (map[int64]int64, error)

	// GetMember returns membership info (including last_read_seq, phantom counts).
	GetMember(ctx context.Context, channelID, userID int64) (*model.ChannelMember, error)

	// GetByID returns channel metadata (for building the response).
	GetByID(ctx context.Context, id int64) (*model.Channel, error)
}

// SyncMsgStore is the store interface needed by SyncHandler.
type SyncMsgStore interface {
	// FetchForUser fetches messages seq > afterSeq for (channelID, userID),
	// applying phantom visibility. Returns in ascending seq order.
	FetchForUser(ctx context.Context, channelID, userID int64, afterSeq int64, limit int) ([]model.Message, error)
}

// ---------- wire types ----------

// SyncRequest is the body of POST /api/sync.
type SyncRequest struct {
	// Channels contains every channel the client knows about with its local max seq.
	Channels []SyncChannelEntry `json:"channels"`
}

// SyncChannelEntry is one channel state from the client.
type SyncChannelEntry struct {
	ID  int64 `json:"id"`
	Seq int64 `json:"seq"` // client's local max seq for this channel
}

// SyncResponse is the body returned by POST /api/sync.
type SyncResponse struct {
	// Channels contains one entry per channel that has changes, plus any
	// new channels the client didn't know about.
	Channels []SyncChannelResult `json:"channels"`
}

// SyncChannelResult is the sync state for one channel.
type SyncChannelResult struct {
	ID        int64           `json:"id"`
	ServerSeq int64           `json:"server_seq"`
	Unread    int64           `json:"unread"`
	Messages  []model.Message `json:"messages,omitempty"` // nil / empty = no messages in response
	HasMore   bool            `json:"has_more,omitempty"` // true when gap > syncGapThreshold
}

// ---------- handler ----------

// SyncHandler serves POST /api/sync.
type SyncHandler struct {
	channels SyncChannelStore
	messages SyncMsgStore
	log      *slog.Logger
}

// NewSyncHandler creates a SyncHandler.
func NewSyncHandler(channels SyncChannelStore, messages SyncMsgStore, log *slog.Logger) *SyncHandler {
	return &SyncHandler{channels: channels, messages: messages, log: log}
}

// Sync handles POST /api/sync.
// Request body: { "channels": [{"id": 1, "seq": 520}, ...] }
// Response body: { "channels": [...SyncChannelResult] }
//
// Algorithm:
//  1. Load all channel seqs for the user from the DB.
//  2. Build a map of client-known seqs from the request.
//  3. For each server channel:
//     - If client_seq == server_seq → no change, skip.
//     - If server_seq - client_seq <= syncGapThreshold → fetch incremental messages.
//     - If gap > threshold → return has_more=true + last syncMsgLimit messages.
//     - If channel not in client map → new channel, return last syncMsgLimit messages.
//  4. Compute unread for every returned channel.
func (h *SyncHandler) Sync(w http.ResponseWriter, r *http.Request) {
	claims, ok := claimsFromCtx(r)
	if !ok {
		writeError(w, http.StatusUnauthorized, "unauthorized")
		return
	}

	var req SyncRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON")
		return
	}

	ctx := r.Context()

	// 1. Server state: all channels this user belongs to.
	serverSeqs, err := h.channels.GetMemberChannelSeqs(ctx, claims.UserID)
	if err != nil {
		h.log.Error("sync: get member channel seqs", "error", err)
		writeError(w, http.StatusInternalServerError, "internal error")
		return
	}

	// 2. Client state map.
	clientSeqs := make(map[int64]int64, len(req.Channels))
	for _, ch := range req.Channels {
		clientSeqs[ch.ID] = ch.Seq
	}

	// 3. Build result.
	var results []SyncChannelResult

	for chID, serverSeq := range serverSeqs {
		clientSeq, known := clientSeqs[chID]

		// Skip channels where the client is already up-to-date.
		if known && clientSeq >= serverSeq {
			continue
		}

		result := SyncChannelResult{
			ID:        chID,
			ServerSeq: serverSeq,
		}

		// Compute unread count from membership state.
		member, err := h.channels.GetMember(ctx, chID, claims.UserID)
		if err == nil {
			unread := (serverSeq - member.LastReadSeq) - (member.PhantomCount - member.PhantomAtRead)
			if unread < 0 {
				unread = 0
			}
			result.Unread = unread
		}

		gap := serverSeq - clientSeq
		if !known {
			// New channel: return latest syncMsgLimit messages.
			msgs, err := h.fetchLatest(ctx, chID, claims.UserID, serverSeq, syncMsgLimit)
			if err != nil {
				h.log.Warn("sync: fetch latest for new channel", "channel_id", chID, "error", err)
			} else {
				result.Messages = msgs
			}
			// has_more is true when there are more messages than we returned
			// (i.e. the channel has more history than the last syncMsgLimit msgs).
			result.HasMore = serverSeq > int64(len(result.Messages))
		} else if gap <= syncGapThreshold {
			// Small gap: return all missed messages.
			msgs, err := h.messages.FetchForUser(ctx, chID, claims.UserID, clientSeq, syncGapThreshold)
			if err != nil {
				h.log.Warn("sync: fetch incremental", "channel_id", chID, "error", err)
			} else {
				result.Messages = msgs
			}
		} else {
			// Large gap: only return the latest syncMsgLimit messages + has_more.
			msgs, err := h.fetchLatest(ctx, chID, claims.UserID, serverSeq, syncMsgLimit)
			if err != nil {
				h.log.Warn("sync: fetch latest for large gap", "channel_id", chID, "error", err)
			} else {
				result.Messages = msgs
			}
			result.HasMore = true
		}

		results = append(results, result)
	}

	if results == nil {
		results = []SyncChannelResult{}
	}

	writeJSON(w, http.StatusOK, SyncResponse{Channels: results})
}

// fetchLatest returns the most recent `limit` messages for a channel (before serverSeq+1),
// ordered ascending by seq (oldest first within the window).
func (h *SyncHandler) fetchLatest(ctx context.Context, chID, userID, serverSeq int64, limit int) ([]model.Message, error) {
	// FetchForUser fetches seq > afterSeq. To get the latest `limit` messages
	// we compute afterSeq = serverSeq - limit (floored at 0).
	afterSeq := serverSeq - int64(limit)
	if afterSeq < 0 {
		afterSeq = 0
	}
	return h.messages.FetchForUser(ctx, chID, userID, afterSeq, limit)
}

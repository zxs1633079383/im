package handler

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"strconv"

	"im-server/internal/repo"
)

// ---------- store interfaces ----------

// ChannelStore is the subset of repo.ChannelRepo used by ChannelHandler.
type ChannelStore interface {
	Create(ctx context.Context, ch *repo.Channel) error
	GetByID(ctx context.Context, id int64) (*repo.Channel, error)
	Update(ctx context.Context, channelID int64, name, avatarURL string) error
	AddMember(ctx context.Context, channelID, userID int64, role int16) error
	RemoveMember(ctx context.Context, channelID, userID int64) error
	GetMember(ctx context.Context, channelID, userID int64) (*repo.ChannelMember, error)
	ListMembers(ctx context.Context, channelID int64) ([]repo.ChannelMember, error)
	ListByUserWithPreview(ctx context.Context, userID int64) ([]repo.ChannelWithPreview, error)
	FindDM(ctx context.Context, userA, userB int64) (*repo.Channel, error)
}

// ChannelUserStore is the minimal user lookup used by ChannelHandler.
type ChannelUserStore interface {
	GetByID(ctx context.Context, id int64) (*repo.User, error)
}

// ChannelEventPusher pushes channel events (e.g. "added") to online users.
// Implemented by *gateway.Hub (via an adapter in main.go).
type ChannelEventPusher interface {
	PushChannelEvent(targetUserID int64, eventType string, channelID int64, name string)
}

// ---------- handler ----------

// ChannelHandler serves all channel-related HTTP endpoints.
type ChannelHandler struct {
	channels    ChannelStore
	users       ChannelUserStore
	eventPusher ChannelEventPusher // nil = no real-time notifications (e.g. in tests)
	log         *slog.Logger
}

func NewChannelHandler(channels ChannelStore, users ChannelUserStore, log *slog.Logger) *ChannelHandler {
	return &ChannelHandler{channels: channels, users: users, log: log}
}

// WithEventPusher sets the channel event pusher. Call after construction.
func (h *ChannelHandler) WithEventPusher(p ChannelEventPusher) *ChannelHandler {
	h.eventPusher = p
	return h
}

// ---------- request body types ----------

type createGroupBody struct {
	Name      string  `json:"name"`
	MemberIDs []int64 `json:"member_ids"`
}

type createDMBody struct {
	PeerID int64 `json:"peer_id"`
}

type updateChannelBody struct {
	Name      string `json:"name"`
	AvatarURL string `json:"avatar_url"`
}

type addMemberBody struct {
	UserID int64 `json:"user_id"`
}

// MemberWithUser enriches a ChannelMember with basic user profile fields.
type MemberWithUser struct {
	repo.ChannelMember
	Username    string `json:"username"`
	DisplayName string `json:"display_name"`
	AvatarURL   string `json:"avatar_url"`
}

// ---------- helpers ----------

// pathID extracts a named path segment as int64.
// For Go 1.22 pattern routes like /api/channels/{id}, use r.PathValue("id").
func pathID(r *http.Request, key string) (int64, bool) {
	s := r.PathValue(key)
	if s == "" {
		return 0, false
	}
	id, err := strconv.ParseInt(s, 10, 64)
	return id, err == nil
}

// requireMember checks that callerID is a member of channelID.
// Returns (member, true) on success; writes error and returns (nil, false) on failure.
func (h *ChannelHandler) requireMember(w http.ResponseWriter, r *http.Request, channelID, callerID int64) (*repo.ChannelMember, bool) {
	m, err := h.channels.GetMember(r.Context(), channelID, callerID)
	if err != nil {
		writeError(w, http.StatusForbidden, "not a member of this channel")
		return nil, false
	}
	return m, true
}

// requireAdminOrOwner checks that caller has admin or owner role.
func (h *ChannelHandler) requireAdminOrOwner(w http.ResponseWriter, r *http.Request, channelID, callerID int64) bool {
	m, err := h.channels.GetMember(r.Context(), channelID, callerID)
	if err != nil {
		writeError(w, http.StatusForbidden, "not a member of this channel")
		return false
	}
	if m.Role < repo.MemberRoleAdmin {
		writeError(w, http.StatusForbidden, "admin or owner required")
		return false
	}
	return true
}

// ---------- POST /api/channels ----------

// CreateGroup creates a new group channel.
// Body: { name: string, member_ids: number[] }
// The caller is automatically added as owner.
func (h *ChannelHandler) CreateGroup(w http.ResponseWriter, r *http.Request) {
	claims, ok := claimsFromCtx(r)
	if !ok {
		writeError(w, http.StatusUnauthorized, "unauthorized")
		return
	}

	var body createGroupBody
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON")
		return
	}
	if body.Name == "" {
		writeError(w, http.StatusUnprocessableEntity, "name is required")
		return
	}

	ch := &repo.Channel{
		Type:      repo.ChannelTypeGroup,
		Name:      body.Name,
		CreatorID: &claims.UserID,
	}
	if err := h.channels.Create(r.Context(), ch); err != nil {
		h.log.Error("create group", "error", err)
		writeError(w, http.StatusInternalServerError, "internal error")
		return
	}

	// Add creator as owner
	if err := h.channels.AddMember(r.Context(), ch.ID, claims.UserID, repo.MemberRoleOwner); err != nil {
		h.log.Error("add owner", "error", err)
		writeError(w, http.StatusInternalServerError, "internal error")
		return
	}

	// Add additional members and notify each one
	for _, uid := range body.MemberIDs {
		if uid == claims.UserID {
			continue // already added
		}
		if err := h.channels.AddMember(r.Context(), ch.ID, uid, repo.MemberRoleMember); err != nil {
			h.log.Warn("add member skipped", "user_id", uid, "error", err)
			continue
		}
		if h.eventPusher != nil {
			h.eventPusher.PushChannelEvent(uid, "added", ch.ID, ch.Name)
		}
	}

	writeJSON(w, http.StatusCreated, ch)
}

// ---------- POST /api/channels/dm ----------

// CreateOrGetDM returns an existing DM between the caller and peer,
// or creates a new one if none exists.
// Body: { peer_id: number }
func (h *ChannelHandler) CreateOrGetDM(w http.ResponseWriter, r *http.Request) {
	claims, ok := claimsFromCtx(r)
	if !ok {
		writeError(w, http.StatusUnauthorized, "unauthorized")
		return
	}

	var body createDMBody
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON")
		return
	}
	if body.PeerID == 0 {
		writeError(w, http.StatusUnprocessableEntity, "peer_id is required")
		return
	}
	if body.PeerID == claims.UserID {
		writeError(w, http.StatusUnprocessableEntity, "cannot DM yourself")
		return
	}

	// Check if DM already exists
	existing, err := h.channels.FindDM(r.Context(), claims.UserID, body.PeerID)
	if err == nil {
		writeJSON(w, http.StatusOK, existing)
		return
	}
	if !errors.Is(err, repo.ErrNotFound) {
		h.log.Error("find dm", "error", err)
		writeError(w, http.StatusInternalServerError, "internal error")
		return
	}

	// Create new DM channel (no name for DMs)
	ch := &repo.Channel{Type: repo.ChannelTypeDM}
	if err := h.channels.Create(r.Context(), ch); err != nil {
		h.log.Error("create dm", "error", err)
		writeError(w, http.StatusInternalServerError, "internal error")
		return
	}

	if err := h.channels.AddMember(r.Context(), ch.ID, claims.UserID, repo.MemberRoleMember); err != nil {
		h.log.Error("add dm member self", "error", err)
		writeError(w, http.StatusInternalServerError, "internal error")
		return
	}
	if err := h.channels.AddMember(r.Context(), ch.ID, body.PeerID, repo.MemberRoleMember); err != nil {
		h.log.Error("add dm member peer", "error", err)
		writeError(w, http.StatusInternalServerError, "internal error")
		return
	}

	writeJSON(w, http.StatusCreated, ch)
}

// ---------- GET /api/channels ----------

// ListChannels returns all channels the caller belongs to, with preview info.
func (h *ChannelHandler) ListChannels(w http.ResponseWriter, r *http.Request) {
	claims, ok := claimsFromCtx(r)
	if !ok {
		writeError(w, http.StatusUnauthorized, "unauthorized")
		return
	}

	previews, err := h.channels.ListByUserWithPreview(r.Context(), claims.UserID)
	if err != nil {
		h.log.Error("list channels", "error", err)
		writeError(w, http.StatusInternalServerError, "internal error")
		return
	}
	if previews == nil {
		previews = []repo.ChannelWithPreview{}
	}
	writeJSON(w, http.StatusOK, previews)
}

// ---------- GET /api/channels/{id} ----------

// GetChannel returns a single channel's details.
// Caller must be a member.
func (h *ChannelHandler) GetChannel(w http.ResponseWriter, r *http.Request) {
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

	if _, ok := h.requireMember(w, r, channelID, claims.UserID); !ok {
		return
	}

	ch, err := h.channels.GetByID(r.Context(), channelID)
	if err != nil {
		writeError(w, http.StatusNotFound, "channel not found")
		return
	}
	writeJSON(w, http.StatusOK, ch)
}

// ---------- PUT /api/channels/{id} ----------

// UpdateChannel updates the name and/or avatar of a group channel.
// Only admins and owners may update.
func (h *ChannelHandler) UpdateChannel(w http.ResponseWriter, r *http.Request) {
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

	if !h.requireAdminOrOwner(w, r, channelID, claims.UserID) {
		return
	}

	var body updateChannelBody
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON")
		return
	}

	if err := h.channels.Update(r.Context(), channelID, body.Name, body.AvatarURL); err != nil {
		h.log.Error("update channel", "error", err)
		writeError(w, http.StatusInternalServerError, "internal error")
		return
	}

	ch, err := h.channels.GetByID(r.Context(), channelID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal error")
		return
	}
	writeJSON(w, http.StatusOK, ch)
}

// ---------- POST /api/channels/{id}/members ----------

// AddMember adds a user to the channel.
// Only admins and owners may add members.
func (h *ChannelHandler) AddMember(w http.ResponseWriter, r *http.Request) {
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

	if !h.requireAdminOrOwner(w, r, channelID, claims.UserID) {
		return
	}

	var body addMemberBody
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON")
		return
	}
	if body.UserID == 0 {
		writeError(w, http.StatusUnprocessableEntity, "user_id is required")
		return
	}

	if err := h.channels.AddMember(r.Context(), channelID, body.UserID, repo.MemberRoleMember); err != nil {
		h.log.Error("add member", "error", err)
		writeError(w, http.StatusInternalServerError, "internal error")
		return
	}
	writeJSON(w, http.StatusCreated, map[string]string{"status": "added"})
}

// ---------- DELETE /api/channels/{id}/members/{user_id} ----------

// RemoveMember removes a user from the channel.
// Only admins and owners may remove members (cannot remove owner).
func (h *ChannelHandler) RemoveMember(w http.ResponseWriter, r *http.Request) {
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

	targetID, ok := pathID(r, "user_id")
	if !ok {
		writeError(w, http.StatusBadRequest, "invalid user_id")
		return
	}

	if !h.requireAdminOrOwner(w, r, channelID, claims.UserID) {
		return
	}

	// Prevent removing the owner
	target, err := h.channels.GetMember(r.Context(), channelID, targetID)
	if err != nil {
		writeError(w, http.StatusNotFound, "member not found")
		return
	}
	if target.Role == repo.MemberRoleOwner {
		writeError(w, http.StatusForbidden, "cannot remove the owner")
		return
	}

	if err := h.channels.RemoveMember(r.Context(), channelID, targetID); err != nil {
		h.log.Error("remove member", "error", err)
		writeError(w, http.StatusInternalServerError, "internal error")
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "removed"})
}

// ---------- GET /api/channels/{id}/members ----------

// ListMembers returns all members of the channel.
// Caller must be a member.
func (h *ChannelHandler) ListMembers(w http.ResponseWriter, r *http.Request) {
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

	if _, ok := h.requireMember(w, r, channelID, claims.UserID); !ok {
		return
	}

	members, err := h.channels.ListMembers(r.Context(), channelID)
	if err != nil {
		h.log.Error("list members", "error", err)
		writeError(w, http.StatusInternalServerError, "internal error")
		return
	}

	result := make([]MemberWithUser, 0, len(members))
	for _, m := range members {
		mwu := MemberWithUser{ChannelMember: m}
		if u, err := h.users.GetByID(r.Context(), m.UserID); err == nil {
			mwu.Username = u.Username
			mwu.DisplayName = u.DisplayName
			mwu.AvatarURL = u.AvatarURL
		}
		result = append(result, mwu)
	}
	writeJSON(w, http.StatusOK, result)
}

// ---------- POST /api/channels/{id}/leave ----------

// LeaveChannel removes the caller from the channel.
// Owners may not leave until they transfer ownership.
func (h *ChannelHandler) LeaveChannel(w http.ResponseWriter, r *http.Request) {
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

	m, ok := h.requireMember(w, r, channelID, claims.UserID)
	if !ok {
		return
	}
	if m.Role == repo.MemberRoleOwner {
		writeError(w, http.StatusForbidden, "owner cannot leave; transfer ownership first")
		return
	}

	if err := h.channels.RemoveMember(r.Context(), channelID, claims.UserID); err != nil {
		h.log.Error("leave channel", "error", err)
		writeError(w, http.StatusInternalServerError, "internal error")
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "left"})
}

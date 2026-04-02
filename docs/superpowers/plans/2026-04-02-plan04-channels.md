# Plan 4: 频道管理 Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** 实现群聊/私聊频道的创建、管理、成员操作，以及客户端频道列表和管理页面

**Architecture:** ChannelStore 扩展查询能力，HTTP handlers 提供 RESTful API，客户端实现经典 IM 左侧边栏布局。

**Tech Stack:** Go net/http, pgx, Angular HttpClient, Angular Signals

---

## 目录结构（Plan 4 新增/修改文件）

```
server/
└── internal/
    ├── store/
    │   ├── channel.go              # add FindDM, ListByUserWithPreview, Update
    │   └── channel_test.go         # add tests for new methods
    ├── handler/
    │   ├── channel.go              # NEW: ChannelHandler with all endpoints
    │   └── channel_test.go         # NEW: unit tests with stub stores
    └── cmd/gateway/main.go         # wire channel routes

client/src/app/
├── core/
│   └── channels/
│       └── channel.service.ts      # NEW: ChannelService (signals + API)
└── features/
    ├── main-layout/
    │   ├── main-layout.component.ts    # NEW: sidebar + router-outlet shell
    │   ├── main-layout.component.html
    │   └── main-layout.component.scss
    ├── channel-list/
    │   ├── channel-list.component.ts   # NEW: left sidebar channel list
    │   ├── channel-list.component.html
    │   └── channel-list.component.scss
    ├── create-group/
    │   ├── create-group.component.ts   # NEW: dialog/page to create group
    │   ├── create-group.component.html
    │   └── create-group.component.scss
    └── channel-settings/
        ├── channel-settings.component.ts   # NEW: name/avatar/members/leave
        ├── channel-settings.component.html
        └── channel-settings.component.scss
```

---

## Task 1: ChannelStore additions + tests

**Files to modify:**
- `server/internal/store/channel.go` — add `FindDM`, `ListByUserWithPreview`, `Update`
- `server/internal/store/channel_test.go` — add tests for all three new methods

### Overview

Three new methods on `ChannelStore`:

| Method | SQL | Purpose |
|--------|-----|---------|
| `FindDM(ctx, userA, userB)` | subquery on `channel_members` | return existing DM channel or `ErrNotFound` |
| `ListByUserWithPreview(ctx, userID)` | LEFT JOIN messages + member row | return list with last message + unread count |
| `Update(ctx, channelID, name, avatarURL)` | UPDATE channels | rename / re-avatar group channel |

### 1.1 Add methods to `server/internal/store/channel.go`

Append the following after the existing `IncrementPhantomCount` function:

```go
// ChannelWithPreview is a Channel enriched with last-message info and unread count.
type ChannelWithPreview struct {
	model.Channel
	LastMsgContent string    `json:"last_msg_content"`
	LastMsgAt      time.Time `json:"last_msg_at"`
	UnreadCount    int64     `json:"unread_count"`
}

// FindDM returns the DM channel that exists between userA and userB.
// Returns ErrNotFound if no such channel exists.
func (s *ChannelStore) FindDM(ctx context.Context, userA, userB int64) (*model.Channel, error) {
	ch := &model.Channel{}
	err := s.pool.QueryRow(ctx,
		`SELECT c.id, c.type, c.name, c.avatar_url, c.seq, c.creator_id, c.created_at, c.updated_at
		 FROM channels c
		 JOIN channel_members ma ON ma.channel_id = c.id AND ma.user_id = $1
		 JOIN channel_members mb ON mb.channel_id = c.id AND mb.user_id = $2
		 WHERE c.type = $3
		 LIMIT 1`,
		userA, userB, model.ChannelTypeDM,
	).Scan(&ch.ID, &ch.Type, &ch.Name, &ch.AvatarURL, &ch.Seq, &ch.CreatorID, &ch.CreatedAt, &ch.UpdatedAt)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, ErrNotFound
		}
		return nil, fmt.Errorf("find dm: %w", err)
	}
	return ch, nil
}

// ListByUserWithPreview returns channels for userID enriched with the last
// message preview and the caller's unread count.
// Channels are ordered by last activity (last message time, or channel created_at).
func (s *ChannelStore) ListByUserWithPreview(ctx context.Context, userID int64) ([]ChannelWithPreview, error) {
	rows, err := s.pool.Query(ctx,
		`SELECT
		    c.id, c.type, c.name, c.avatar_url, c.seq, c.creator_id, c.created_at, c.updated_at,
		    COALESCE(m.content, '')                         AS last_msg_content,
		    COALESCE(m.created_at, c.created_at)            AS last_msg_at,
		    GREATEST(
		        (c.seq - cm.last_read_seq) - (cm.phantom_count - cm.phantom_at_read),
		        0
		    )                                               AS unread_count
		 FROM channels c
		 JOIN channel_members cm ON cm.channel_id = c.id AND cm.user_id = $1
		 LEFT JOIN LATERAL (
		     SELECT content, created_at
		     FROM messages
		     WHERE channel_id = c.id
		     ORDER BY seq DESC
		     LIMIT 1
		 ) m ON true
		 ORDER BY COALESCE(m.created_at, c.created_at) DESC`,
		userID,
	)
	if err != nil {
		return nil, fmt.Errorf("list by user with preview: %w", err)
	}
	defer rows.Close()

	var result []ChannelWithPreview
	for rows.Next() {
		var cp ChannelWithPreview
		if err := rows.Scan(
			&cp.ID, &cp.Type, &cp.Name, &cp.AvatarURL, &cp.Seq, &cp.CreatorID,
			&cp.CreatedAt, &cp.UpdatedAt,
			&cp.LastMsgContent, &cp.LastMsgAt, &cp.UnreadCount,
		); err != nil {
			return nil, fmt.Errorf("scan channel preview: %w", err)
		}
		result = append(result, cp)
	}
	return result, rows.Err()
}

// Update sets the name and/or avatar_url of a channel.
// Pass empty string to leave a field unchanged.
func (s *ChannelStore) Update(ctx context.Context, channelID int64, name, avatarURL string) error {
	_, err := s.pool.Exec(ctx,
		`UPDATE channels
		 SET name       = CASE WHEN $2 <> '' THEN $2 ELSE name END,
		     avatar_url = CASE WHEN $3 <> '' THEN $3 ELSE avatar_url END,
		     updated_at = now()
		 WHERE id = $1`,
		channelID, name, avatarURL,
	)
	if err != nil {
		return fmt.Errorf("update channel: %w", err)
	}
	return nil
}
```

**Required imports to add** at the top of `channel.go`:

```go
import (
	"context"
	"errors"           // add
	"fmt"
	"time"             // add

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"im-server/internal/model"
)
```

Also add `ErrNotFound` sentinel in `channel.go` if not already present from `store` package level.
Check `friendship.go` — `ErrNotFound` is already declared there in package `store`, so do NOT redeclare it. `channel.go` simply uses it.

### 1.2 Add tests to `server/internal/store/channel_test.go`

Append:

```go
func TestChannelStore_FindDM_Found(t *testing.T) {
	pool := testutil.PGPool(t)
	cs := store.NewChannelStore(pool)
	us := store.NewUserStore(pool)
	ctx := context.Background()

	alice := &model.User{Username: "dm_alice", Email: "dma@test.com", PasswordHash: "h", DisplayName: "A"}
	bob := &model.User{Username: "dm_bob", Email: "dmb@test.com", PasswordHash: "h", DisplayName: "B"}
	us.Create(ctx, alice)
	us.Create(ctx, bob)

	ch := &model.Channel{Type: model.ChannelTypeDM}
	if err := cs.Create(ctx, ch); err != nil {
		t.Fatal(err)
	}
	cs.AddMember(ctx, ch.ID, alice.ID, model.MemberRoleMember)
	cs.AddMember(ctx, ch.ID, bob.ID, model.MemberRoleMember)

	found, err := cs.FindDM(ctx, alice.ID, bob.ID)
	if err != nil {
		t.Fatalf("FindDM: %v", err)
	}
	if found.ID != ch.ID {
		t.Errorf("found.ID = %d, want %d", found.ID, ch.ID)
	}
}

func TestChannelStore_FindDM_NotFound(t *testing.T) {
	pool := testutil.PGPool(t)
	cs := store.NewChannelStore(pool)
	us := store.NewUserStore(pool)
	ctx := context.Background()

	alice := &model.User{Username: "nd_alice", Email: "nda@test.com", PasswordHash: "h", DisplayName: "A"}
	bob := &model.User{Username: "nd_bob", Email: "ndb@test.com", PasswordHash: "h", DisplayName: "B"}
	us.Create(ctx, alice)
	us.Create(ctx, bob)

	_, err := cs.FindDM(ctx, alice.ID, bob.ID)
	if err != store.ErrNotFound {
		t.Fatalf("expected ErrNotFound, got %v", err)
	}
}

func TestChannelStore_ListByUserWithPreview(t *testing.T) {
	pool := testutil.PGPool(t)
	cs := store.NewChannelStore(pool)
	us := store.NewUserStore(pool)
	ctx := context.Background()

	alice := &model.User{Username: "prev_alice", Email: "pa@test.com", PasswordHash: "h", DisplayName: "A"}
	us.Create(ctx, alice)

	ch := &model.Channel{Type: model.ChannelTypeGroup, Name: "preview-group", CreatorID: &alice.ID}
	if err := cs.Create(ctx, ch); err != nil {
		t.Fatal(err)
	}
	cs.AddMember(ctx, ch.ID, alice.ID, model.MemberRoleOwner)

	previews, err := cs.ListByUserWithPreview(ctx, alice.ID)
	if err != nil {
		t.Fatalf("ListByUserWithPreview: %v", err)
	}
	if len(previews) == 0 {
		t.Fatal("expected at least 1 channel preview")
	}
	if previews[0].ID != ch.ID {
		t.Errorf("preview channel ID = %d, want %d", previews[0].ID, ch.ID)
	}
	if previews[0].UnreadCount < 0 {
		t.Errorf("unread count < 0: %d", previews[0].UnreadCount)
	}
}

func TestChannelStore_Update(t *testing.T) {
	pool := testutil.PGPool(t)
	cs := store.NewChannelStore(pool)
	us := store.NewUserStore(pool)
	ctx := context.Background()

	alice := &model.User{Username: "upd_alice", Email: "ua@test.com", PasswordHash: "h", DisplayName: "A"}
	us.Create(ctx, alice)

	ch := &model.Channel{Type: model.ChannelTypeGroup, Name: "old-name", CreatorID: &alice.ID}
	if err := cs.Create(ctx, ch); err != nil {
		t.Fatal(err)
	}

	if err := cs.Update(ctx, ch.ID, "new-name", ""); err != nil {
		t.Fatalf("Update: %v", err)
	}

	updated, err := cs.GetByID(ctx, ch.ID)
	if err != nil {
		t.Fatal(err)
	}
	if updated.Name != "new-name" {
		t.Errorf("Name = %q, want %q", updated.Name, "new-name")
	}
}
```

### 1.3 Run tests

```bash
cd /Users/mac17/workspace/ai/im/server
go test ./internal/store/... -run TestChannelStore -v
```

Expected: all 4 new tests pass alongside the 2 existing ones.

### 1.4 Commit

```
git add server/internal/store/channel.go server/internal/store/channel_test.go
git commit -m "feat(store): add FindDM, ListByUserWithPreview, Update to ChannelStore"
```

---

## Task 2: Channel HTTP handlers + tests

**Files to create:**
- `server/internal/handler/channel.go`
- `server/internal/handler/channel_test.go`

### Overview

`ChannelHandler` depends on two store interfaces defined locally in the handler file (following the friend.go pattern). All endpoints require JWT auth via `claimsFromCtx`.

Endpoints:

| Method | Path | Handler method |
|--------|------|---------------|
| POST | /api/channels | CreateGroup |
| POST | /api/channels/dm | CreateOrGetDM |
| GET | /api/channels | ListChannels |
| GET | /api/channels/{id} | GetChannel |
| PUT | /api/channels/{id} | UpdateChannel |
| POST | /api/channels/{id}/members | AddMember |
| DELETE | /api/channels/{id}/members/{user_id} | RemoveMember |
| GET | /api/channels/{id}/members | ListMembers |
| POST | /api/channels/{id}/leave | LeaveChannel |

### 2.1 Create `server/internal/handler/channel.go`

```go
package handler

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"strconv"

	"im-server/internal/model"
	"im-server/internal/store"
)

// ---------- store interfaces ----------

// ChannelStore is the subset of store.ChannelStore used by ChannelHandler.
type ChannelStore interface {
	Create(ctx context.Context, ch *model.Channel) error
	GetByID(ctx context.Context, id int64) (*model.Channel, error)
	Update(ctx context.Context, channelID int64, name, avatarURL string) error
	AddMember(ctx context.Context, channelID, userID int64, role model.MemberRole) error
	RemoveMember(ctx context.Context, channelID, userID int64) error
	GetMember(ctx context.Context, channelID, userID int64) (*model.ChannelMember, error)
	ListMembers(ctx context.Context, channelID int64) ([]model.ChannelMember, error)
	ListByUserWithPreview(ctx context.Context, userID int64) ([]store.ChannelWithPreview, error)
	FindDM(ctx context.Context, userA, userB int64) (*model.Channel, error)
}

// ChannelUserStore is the minimal user lookup used by ChannelHandler.
type ChannelUserStore interface {
	GetByID(ctx context.Context, id int64) (*model.User, error)
}

// ---------- handler ----------

// ChannelHandler serves all channel-related HTTP endpoints.
type ChannelHandler struct {
	channels ChannelStore
	users    ChannelUserStore
	log      *slog.Logger
}

func NewChannelHandler(channels ChannelStore, users ChannelUserStore, log *slog.Logger) *ChannelHandler {
	return &ChannelHandler{channels: channels, users: users, log: log}
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

// ---------- helpers ----------

// pathID extracts the last path segment as int64.
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
func (h *ChannelHandler) requireMember(w http.ResponseWriter, r *http.Request, channelID, callerID int64) (*model.ChannelMember, bool) {
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
	if m.Role < model.MemberRoleAdmin {
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

	ch := &model.Channel{
		Type:      model.ChannelTypeGroup,
		Name:      body.Name,
		CreatorID: &claims.UserID,
	}
	if err := h.channels.Create(r.Context(), ch); err != nil {
		h.log.Error("create group", "error", err)
		writeError(w, http.StatusInternalServerError, "internal error")
		return
	}

	// Add creator as owner
	if err := h.channels.AddMember(r.Context(), ch.ID, claims.UserID, model.MemberRoleOwner); err != nil {
		h.log.Error("add owner", "error", err)
		writeError(w, http.StatusInternalServerError, "internal error")
		return
	}

	// Add additional members
	for _, uid := range body.MemberIDs {
		if uid == claims.UserID {
			continue // already added
		}
		if err := h.channels.AddMember(r.Context(), ch.ID, uid, model.MemberRoleMember); err != nil {
			h.log.Warn("add member skipped", "user_id", uid, "error", err)
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
	if !errors.Is(err, store.ErrNotFound) {
		h.log.Error("find dm", "error", err)
		writeError(w, http.StatusInternalServerError, "internal error")
		return
	}

	// Create new DM channel (no name for DMs)
	ch := &model.Channel{Type: model.ChannelTypeDM}
	if err := h.channels.Create(r.Context(), ch); err != nil {
		h.log.Error("create dm", "error", err)
		writeError(w, http.StatusInternalServerError, "internal error")
		return
	}

	if err := h.channels.AddMember(r.Context(), ch.ID, claims.UserID, model.MemberRoleMember); err != nil {
		h.log.Error("add dm member self", "error", err)
		writeError(w, http.StatusInternalServerError, "internal error")
		return
	}
	if err := h.channels.AddMember(r.Context(), ch.ID, body.PeerID, model.MemberRoleMember); err != nil {
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
		previews = []store.ChannelWithPreview{}
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

	if err := h.channels.AddMember(r.Context(), channelID, body.UserID, model.MemberRoleMember); err != nil {
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
	if target.Role == model.MemberRoleOwner {
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
	if members == nil {
		members = []model.ChannelMember{}
	}
	writeJSON(w, http.StatusOK, members)
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
	if m.Role == model.MemberRoleOwner {
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
```

### 2.2 Create `server/internal/handler/channel_test.go`

```go
package handler_test

import (
	"context"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"

	"im-server/internal/handler"
	"im-server/internal/model"
	"im-server/internal/store"
)

// ---------- in-memory stub ChannelStore ----------

type stubChannelStore struct {
	channels []model.Channel
	members  []model.ChannelMember
	nextID   int64
}

func newStubChannelStore() *stubChannelStore {
	return &stubChannelStore{nextID: 1}
}

func (s *stubChannelStore) Create(_ context.Context, ch *model.Channel) error {
	ch.ID = s.nextID
	s.nextID++
	s.channels = append(s.channels, *ch)
	return nil
}

func (s *stubChannelStore) GetByID(_ context.Context, id int64) (*model.Channel, error) {
	for i := range s.channels {
		if s.channels[i].ID == id {
			c := s.channels[i]
			return &c, nil
		}
	}
	return nil, handler.ErrNotFound
}

func (s *stubChannelStore) Update(_ context.Context, channelID int64, name, avatarURL string) error {
	for i := range s.channels {
		if s.channels[i].ID == channelID {
			if name != "" {
				s.channels[i].Name = name
			}
			if avatarURL != "" {
				s.channels[i].AvatarURL = avatarURL
			}
			return nil
		}
	}
	return handler.ErrNotFound
}

func (s *stubChannelStore) AddMember(_ context.Context, channelID, userID int64, role model.MemberRole) error {
	s.members = append(s.members, model.ChannelMember{
		ChannelID: channelID,
		UserID:    userID,
		Role:      role,
	})
	return nil
}

func (s *stubChannelStore) RemoveMember(_ context.Context, channelID, userID int64) error {
	var kept []model.ChannelMember
	for _, m := range s.members {
		if !(m.ChannelID == channelID && m.UserID == userID) {
			kept = append(kept, m)
		}
	}
	s.members = kept
	return nil
}

func (s *stubChannelStore) GetMember(_ context.Context, channelID, userID int64) (*model.ChannelMember, error) {
	for i := range s.members {
		if s.members[i].ChannelID == channelID && s.members[i].UserID == userID {
			m := s.members[i]
			return &m, nil
		}
	}
	return nil, handler.ErrNotFound
}

func (s *stubChannelStore) ListMembers(_ context.Context, channelID int64) ([]model.ChannelMember, error) {
	var result []model.ChannelMember
	for _, m := range s.members {
		if m.ChannelID == channelID {
			result = append(result, m)
		}
	}
	return result, nil
}

func (s *stubChannelStore) ListByUserWithPreview(_ context.Context, userID int64) ([]store.ChannelWithPreview, error) {
	var result []store.ChannelWithPreview
	for _, m := range s.members {
		if m.UserID == userID {
			for _, ch := range s.channels {
				if ch.ID == m.ChannelID {
					result = append(result, store.ChannelWithPreview{Channel: ch})
					break
				}
			}
		}
	}
	return result, nil
}

func (s *stubChannelStore) FindDM(_ context.Context, userA, userB int64) (*model.Channel, error) {
	// Find channels where both userA and userB are members and type == DM
	memberOf := func(uid, chID int64) bool {
		for _, m := range s.members {
			if m.UserID == uid && m.ChannelID == chID {
				return true
			}
		}
		return false
	}
	for _, ch := range s.channels {
		if ch.Type == model.ChannelTypeDM && memberOf(userA, ch.ID) && memberOf(userB, ch.ID) {
			c := ch
			return &c, nil
		}
	}
	return nil, store.ErrNotFound
}

// ---------- stub ChannelUserStore ----------

type stubChannelUserStore struct{}

func (s *stubChannelUserStore) GetByID(_ context.Context, id int64) (*model.User, error) {
	return &model.User{ID: id, Username: "user", DisplayName: "User"}, nil
}

// ---------- test helpers ----------

func newChannelHandler(t *testing.T) (*handler.ChannelHandler, *stubChannelStore) {
	t.Helper()
	cs := newStubChannelStore()
	log := slog.New(slog.NewTextHandler(os.Stderr, nil))
	h := handler.NewChannelHandler(cs, &stubChannelUserStore{}, log)
	return h, cs
}

// ---------- tests ----------

func TestChannelHandler_CreateGroup_Success(t *testing.T) {
	h, _ := newChannelHandler(t)
	req := requestWithClaims("POST", "/api/channels", 1, map[string]any{
		"name":       "test-group",
		"member_ids": []int64{2, 3},
	})
	rr := httptest.NewRecorder()
	h.CreateGroup(rr, req)
	if rr.Code != http.StatusCreated {
		t.Fatalf("expected 201, got %d: %s", rr.Code, rr.Body.String())
	}
}

func TestChannelHandler_CreateGroup_NoName(t *testing.T) {
	h, _ := newChannelHandler(t)
	req := requestWithClaims("POST", "/api/channels", 1, map[string]any{"name": ""})
	rr := httptest.NewRecorder()
	h.CreateGroup(rr, req)
	if rr.Code != http.StatusUnprocessableEntity {
		t.Fatalf("expected 422, got %d", rr.Code)
	}
}

func TestChannelHandler_CreateOrGetDM_CreatesNew(t *testing.T) {
	h, _ := newChannelHandler(t)
	req := requestWithClaims("POST", "/api/channels/dm", 1, map[string]any{"peer_id": 2})
	rr := httptest.NewRecorder()
	h.CreateOrGetDM(rr, req)
	if rr.Code != http.StatusCreated {
		t.Fatalf("expected 201, got %d: %s", rr.Code, rr.Body.String())
	}
}

func TestChannelHandler_CreateOrGetDM_ReturnsExisting(t *testing.T) {
	h, cs := newChannelHandler(t)
	// Create the DM first
	req1 := requestWithClaims("POST", "/api/channels/dm", 1, map[string]any{"peer_id": 2})
	rr1 := httptest.NewRecorder()
	h.CreateOrGetDM(rr1, req1)
	if rr1.Code != http.StatusCreated {
		t.Fatalf("first DM: expected 201, got %d", rr1.Code)
	}
	_ = cs

	// Call again — should return existing (200)
	req2 := requestWithClaims("POST", "/api/channels/dm", 1, map[string]any{"peer_id": 2})
	rr2 := httptest.NewRecorder()
	h.CreateOrGetDM(rr2, req2)
	if rr2.Code != http.StatusOK {
		t.Fatalf("second DM: expected 200, got %d: %s", rr2.Code, rr2.Body.String())
	}
}

func TestChannelHandler_CreateOrGetDM_SelfError(t *testing.T) {
	h, _ := newChannelHandler(t)
	req := requestWithClaims("POST", "/api/channels/dm", 1, map[string]any{"peer_id": 1})
	rr := httptest.NewRecorder()
	h.CreateOrGetDM(rr, req)
	if rr.Code != http.StatusUnprocessableEntity {
		t.Fatalf("expected 422, got %d", rr.Code)
	}
}

func TestChannelHandler_ListChannels_Empty(t *testing.T) {
	h, _ := newChannelHandler(t)
	req := requestWithClaims("GET", "/api/channels", 1, nil)
	rr := httptest.NewRecorder()
	h.ListChannels(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rr.Code)
	}
}

func TestChannelHandler_ListMembers_NonMemberForbidden(t *testing.T) {
	h, cs := newChannelHandler(t)
	// Create a group as user 1
	ch := &model.Channel{Type: model.ChannelTypeGroup, Name: "g"}
	cs.Create(context.Background(), ch)
	cs.AddMember(context.Background(), ch.ID, 1, model.MemberRoleOwner)

	// User 2 (not a member) tries to list members
	req := requestWithClaims("GET", "/api/channels/1/members", 2, nil)
	req.SetPathValue("id", "1")
	rr := httptest.NewRecorder()
	h.ListMembers(rr, req)
	if rr.Code != http.StatusForbidden {
		t.Fatalf("expected 403, got %d: %s", rr.Code, rr.Body.String())
	}
}

func TestChannelHandler_LeaveChannel_OwnerBlocked(t *testing.T) {
	h, cs := newChannelHandler(t)
	ch := &model.Channel{Type: model.ChannelTypeGroup, Name: "g"}
	cs.Create(context.Background(), ch)
	cs.AddMember(context.Background(), ch.ID, 1, model.MemberRoleOwner)

	req := requestWithClaims("POST", "/api/channels/1/leave", 1, nil)
	req.SetPathValue("id", "1")
	rr := httptest.NewRecorder()
	h.LeaveChannel(rr, req)
	if rr.Code != http.StatusForbidden {
		t.Fatalf("expected 403 for owner leave, got %d: %s", rr.Code, rr.Body.String())
	}
}

func TestChannelHandler_NoAuth(t *testing.T) {
	h, _ := newChannelHandler(t)
	req := httptest.NewRequest("GET", "/api/channels", nil)
	rr := httptest.NewRecorder()
	h.ListChannels(rr, req)
	if rr.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401, got %d", rr.Code)
	}
}
```

**Note on `ErrNotFound` in handler package:** `handler.ErrNotFound` is referenced in stub tests. Check if it is already declared in `handler/auth.go` or `handler/friend.go`. If not, add to `handler/channel.go`:

```go
// ErrNotFound is a sentinel used by handler stub tests and referenced by handlers.
var ErrNotFound = errors.New("not found")
```

If it already exists in the handler package, skip this declaration.

### 2.3 Run tests

```bash
cd /Users/mac17/workspace/ai/im/server
go test ./internal/handler/... -run TestChannelHandler -v
```

Expected: all 9 new handler tests pass.

### 2.4 Commit

```
git add server/internal/handler/channel.go server/internal/handler/channel_test.go
git commit -m "feat(handler): add ChannelHandler with group/DM/member endpoints"
```

---

## Task 3: Wire channel routes in gateway

**File to modify:** `server/cmd/gateway/main.go`

### 3.1 Add channel routes to `run()` in `main.go`

In the `run()` function, after the existing friend store wiring, add:

```go
channelStore := store.NewChannelStore(pool)
channelHandler := handler.NewChannelHandler(channelStore, userStore, log)
```

Then, after the existing friend route registrations, add:

```go
// Channel routes (JWT protected)
mux.Handle("POST /api/channels",                              jwtMiddleware(http.HandlerFunc(channelHandler.CreateGroup)))
mux.Handle("POST /api/channels/dm",                           jwtMiddleware(http.HandlerFunc(channelHandler.CreateOrGetDM)))
mux.Handle("GET /api/channels",                               jwtMiddleware(http.HandlerFunc(channelHandler.ListChannels)))
mux.Handle("GET /api/channels/{id}",                          jwtMiddleware(http.HandlerFunc(channelHandler.GetChannel)))
mux.Handle("PUT /api/channels/{id}",                          jwtMiddleware(http.HandlerFunc(channelHandler.UpdateChannel)))
mux.Handle("POST /api/channels/{id}/members",                 jwtMiddleware(http.HandlerFunc(channelHandler.AddMember)))
mux.Handle("DELETE /api/channels/{id}/members/{user_id}",     jwtMiddleware(http.HandlerFunc(channelHandler.RemoveMember)))
mux.Handle("GET /api/channels/{id}/members",                  jwtMiddleware(http.HandlerFunc(channelHandler.ListMembers)))
mux.Handle("POST /api/channels/{id}/leave",                   jwtMiddleware(http.HandlerFunc(channelHandler.LeaveChannel)))
```

**Note:** Go 1.22 `net/http` supports `{id}` path patterns natively — no external router needed.

### 3.2 Build to verify

```bash
cd /Users/mac17/workspace/ai/im/server
go build ./cmd/gateway/...
```

Expected: zero errors.

### 3.3 Commit

```
git add server/cmd/gateway/main.go
git commit -m "feat(gateway): wire channel HTTP routes"
```

---

## Task 4: Client ChannelService

**File to create:** `client/src/app/core/channels/channel.service.ts`

### Overview

`ChannelService` follows the exact same pattern as `FriendService`:
- Injectable singleton (`providedIn: 'root'`)
- Signal-based state: `channels` signal holds `ChannelWithPreview[]`
- `firstValueFrom` wrapper around `HttpClient` calls
- Constant `API_BASE = 'http://localhost:8080/api'`

### 4.1 Create `client/src/app/core/channels/channel.service.ts`

```typescript
import { Injectable, signal } from '@angular/core';
import { HttpClient } from '@angular/common/http';
import { firstValueFrom } from 'rxjs';

// ---------- types ----------

export interface Channel {
  id: number;
  type: number;       // 1=DM, 2=GROUP
  name: string;
  avatar_url: string;
  seq: number;
  creator_id: number | null;
  created_at: string;
  updated_at: string;
}

export interface ChannelWithPreview extends Channel {
  last_msg_content: string;
  last_msg_at: string;
  unread_count: number;
}

export interface ChannelMember {
  user_id: number;
  channel_id: number;
  role: number;       // 1=member, 2=admin, 3=owner
  last_read_seq: number;
  phantom_count: number;
  phantom_at_read: number;
  joined_at: string;
}

const API_BASE = 'http://localhost:8080/api';

@Injectable({ providedIn: 'root' })
export class ChannelService {
  /** Reactive signal: channel list with preview info */
  readonly channels = signal<ChannelWithPreview[]>([]);

  constructor(private http: HttpClient) {}

  // ---------- channel operations ----------

  async createGroup(name: string, memberIds: number[]): Promise<Channel> {
    return firstValueFrom(
      this.http.post<Channel>(`${API_BASE}/channels`, { name, member_ids: memberIds })
    );
  }

  async createOrGetDM(peerId: number): Promise<Channel> {
    return firstValueFrom(
      this.http.post<Channel>(`${API_BASE}/channels/dm`, { peer_id: peerId })
    );
  }

  async getChannel(id: number): Promise<Channel> {
    return firstValueFrom(
      this.http.get<Channel>(`${API_BASE}/channels/${id}`)
    );
  }

  async updateChannel(id: number, name: string, avatarUrl: string): Promise<Channel> {
    return firstValueFrom(
      this.http.put<Channel>(`${API_BASE}/channels/${id}`, { name, avatar_url: avatarUrl })
    );
  }

  async addMember(channelId: number, userId: number): Promise<void> {
    await firstValueFrom(
      this.http.post(`${API_BASE}/channels/${channelId}/members`, { user_id: userId })
    );
  }

  async removeMember(channelId: number, userId: number): Promise<void> {
    await firstValueFrom(
      this.http.delete(`${API_BASE}/channels/${channelId}/members/${userId}`)
    );
  }

  async listMembers(channelId: number): Promise<ChannelMember[]> {
    return firstValueFrom(
      this.http.get<ChannelMember[]>(`${API_BASE}/channels/${channelId}/members`)
    );
  }

  async leaveChannel(channelId: number): Promise<void> {
    await firstValueFrom(
      this.http.post(`${API_BASE}/channels/${channelId}/leave`, {})
    );
    await this.loadChannels();
  }

  // ---------- data loading ----------

  async loadChannels(): Promise<void> {
    const data = await firstValueFrom(
      this.http.get<ChannelWithPreview[]>(`${API_BASE}/channels`)
    );
    this.channels.set(data ?? []);
  }
}
```

### 4.2 Commit

```
git add client/src/app/core/channels/channel.service.ts
git commit -m "feat(client): add ChannelService with signal-based channel list"
```

---

## Task 5: Client main layout (sidebar + content area)

**Files to create:**
- `client/src/app/features/main-layout/main-layout.component.ts`
- `client/src/app/features/main-layout/main-layout.component.html`
- `client/src/app/features/main-layout/main-layout.component.scss`

Also **modify:**
- `client/src/app/app.routes.ts` — replace the home route with main-layout as shell; nest channels and contacts as children

### Overview

Classic IM two-panel layout:
- Left sidebar: `<app-channel-list>` (always visible when authenticated)
- Right panel: `<router-outlet>` showing the active route (home placeholder, contacts, channel-settings, etc.)

The main layout is the authenticated shell route. All protected pages become children.

### 5.1 Create `client/src/app/features/main-layout/main-layout.component.ts`

```typescript
import { Component, inject, OnInit } from '@angular/core';
import { RouterOutlet } from '@angular/router';
import { ChannelListComponent } from '../channel-list/channel-list.component';
import { ChannelService } from '../../core/channels/channel.service';

@Component({
  selector: 'app-main-layout',
  standalone: true,
  imports: [RouterOutlet, ChannelListComponent],
  templateUrl: './main-layout.component.html',
  styleUrl: './main-layout.component.scss',
})
export class MainLayoutComponent implements OnInit {
  private channelService = inject(ChannelService);

  async ngOnInit(): Promise<void> {
    await this.channelService.loadChannels();
  }
}
```

### 5.2 Create `client/src/app/features/main-layout/main-layout.component.html`

```html
<div class="main-layout">
  <aside class="sidebar">
    <app-channel-list />
  </aside>
  <main class="content">
    <router-outlet />
  </main>
</div>
```

### 5.3 Create `client/src/app/features/main-layout/main-layout.component.scss`

```scss
.main-layout {
  display: flex;
  height: 100vh;
  overflow: hidden;
}

.sidebar {
  width: 260px;
  min-width: 200px;
  max-width: 320px;
  background: #1e1e2e;
  border-right: 1px solid #313244;
  display: flex;
  flex-direction: column;
  overflow: hidden;
}

.content {
  flex: 1;
  overflow: auto;
  background: #181825;
}
```

### 5.4 Update `client/src/app/app.routes.ts`

Replace the entire file content:

```typescript
import { Routes } from '@angular/router';
import { authGuard } from './shared/guards/auth.guard';

export const routes: Routes = [
  {
    path: 'login',
    loadComponent: () =>
      import('./features/login/login.component').then((m) => m.LoginComponent),
  },
  {
    path: 'register',
    loadComponent: () =>
      import('./features/register/register.component').then((m) => m.RegisterComponent),
  },
  {
    path: '',
    canActivate: [authGuard],
    loadComponent: () =>
      import('./features/main-layout/main-layout.component').then(
        (m) => m.MainLayoutComponent
      ),
    children: [
      {
        path: '',
        loadComponent: () =>
          import('./features/home/home.component').then((m) => m.HomeComponent),
      },
      {
        path: 'contacts',
        loadComponent: () =>
          import('./features/contacts/contacts.component').then(
            (m) => m.ContactsComponent
          ),
      },
      {
        path: 'channels/:id/settings',
        loadComponent: () =>
          import('./features/channel-settings/channel-settings.component').then(
            (m) => m.ChannelSettingsComponent
          ),
      },
    ],
  },
  {
    path: '**',
    redirectTo: '',
  },
];
```

### 5.5 Commit

```
git add client/src/app/features/main-layout/ client/src/app/app.routes.ts
git commit -m "feat(client): add MainLayout shell with sidebar+router-outlet; restructure routes"
```

---

## Task 6: Client channel list sidebar component

**Files to create:**
- `client/src/app/features/channel-list/channel-list.component.ts`
- `client/src/app/features/channel-list/channel-list.component.html`
- `client/src/app/features/channel-list/channel-list.component.scss`

### Overview

The channel list sidebar:
- Displays all channels from `ChannelService.channels` signal
- Shows channel name (for group) or "DM" placeholder for type=1
- Shows `last_msg_content` preview (truncated)
- Shows `unread_count` badge when > 0
- "New Group" button opens `CreateGroupComponent`
- Clicking a channel navigates to that channel (placeholder for Plan 5 messaging)

### 6.1 Create `client/src/app/features/channel-list/channel-list.component.ts`

```typescript
import { Component, inject, signal } from '@angular/core';
import { CommonModule } from '@angular/common';
import { Router, RouterLink } from '@angular/router';
import { ChannelService, ChannelWithPreview } from '../../core/channels/channel.service';
import { AuthService } from '../../core/auth/auth.service';
import { CreateGroupComponent } from '../create-group/create-group.component';

@Component({
  selector: 'app-channel-list',
  standalone: true,
  imports: [CommonModule, RouterLink, CreateGroupComponent],
  templateUrl: './channel-list.component.html',
  styleUrl: './channel-list.component.scss',
})
export class ChannelListComponent {
  channelService = inject(ChannelService);
  private auth = inject(AuthService);
  private router = inject(Router);

  showCreateGroup = signal(false);

  channelLabel(ch: ChannelWithPreview): string {
    if (ch.type === 2) {
      return ch.name || 'Group';
    }
    // DM: show "DM" until Plan 5 resolves peer name
    return 'DM';
  }

  previewText(ch: ChannelWithPreview): string {
    const msg = ch.last_msg_content;
    if (!msg) return 'No messages yet';
    return msg.length > 40 ? msg.slice(0, 40) + '…' : msg;
  }

  openChannel(ch: ChannelWithPreview): void {
    // Placeholder: in Plan 5 this will open the message view
    // For now navigate to settings as a stub
    this.router.navigate(['channels', ch.id, 'settings']);
  }

  openCreateGroup(): void {
    this.showCreateGroup.set(true);
  }

  onGroupCreated(): void {
    this.showCreateGroup.set(false);
    this.channelService.loadChannels();
  }

  onGroupCancelled(): void {
    this.showCreateGroup.set(false);
  }

  logout(): void {
    this.auth.logout();
    this.router.navigate(['/login']);
  }
}
```

### 6.2 Create `client/src/app/features/channel-list/channel-list.component.html`

```html
<div class="channel-list">
  <div class="sidebar-header">
    <span class="app-title">IM</span>
    <button class="btn-icon" title="New Group" (click)="openCreateGroup()">＋</button>
  </div>

  <nav class="nav-links">
    <a routerLink="/contacts" class="nav-link">Contacts</a>
  </nav>

  <div class="section-label">Chats</div>

  <ul class="channels">
    @for (ch of channelService.channels(); track ch.id) {
      <li class="channel-row" (click)="openChannel(ch)">
        <div class="channel-avatar">
          {{ channelLabel(ch)[0] | uppercase }}
        </div>
        <div class="channel-info">
          <div class="channel-name-row">
            <span class="channel-name">{{ channelLabel(ch) }}</span>
            @if (ch.unread_count > 0) {
              <span class="badge">{{ ch.unread_count }}</span>
            }
          </div>
          <span class="preview">{{ previewText(ch) }}</span>
        </div>
      </li>
    } @empty {
      <li class="empty">No channels yet. Create a group or start a DM.</li>
    }
  </ul>

  <div class="sidebar-footer">
    <button class="btn-logout" (click)="logout()">Sign out</button>
  </div>

  @if (showCreateGroup()) {
    <app-create-group
      (created)="onGroupCreated()"
      (cancelled)="onGroupCancelled()"
    />
  }
</div>
```

### 6.3 Create `client/src/app/features/channel-list/channel-list.component.scss`

```scss
.channel-list {
  display: flex;
  flex-direction: column;
  height: 100%;
  color: #cdd6f4;
}

.sidebar-header {
  display: flex;
  align-items: center;
  justify-content: space-between;
  padding: 1rem;
  font-weight: 700;
  font-size: 1.1rem;
  border-bottom: 1px solid #313244;
}

.btn-icon {
  background: none;
  border: none;
  color: #89b4fa;
  font-size: 1.4rem;
  cursor: pointer;
  padding: 0 0.25rem;
  line-height: 1;

  &:hover { color: #cdd6f4; }
}

.nav-links {
  padding: 0.5rem 0.75rem;
  border-bottom: 1px solid #313244;
}

.nav-link {
  display: block;
  padding: 0.4rem 0.5rem;
  color: #a6adc8;
  text-decoration: none;
  border-radius: 4px;
  font-size: 0.9rem;

  &:hover { background: #313244; color: #cdd6f4; }
}

.section-label {
  padding: 0.6rem 1rem 0.2rem;
  font-size: 0.72rem;
  text-transform: uppercase;
  letter-spacing: 0.08em;
  color: #6c7086;
}

.channels {
  flex: 1;
  overflow-y: auto;
  list-style: none;
  margin: 0;
  padding: 0;
}

.channel-row {
  display: flex;
  align-items: center;
  gap: 0.75rem;
  padding: 0.6rem 1rem;
  cursor: pointer;
  border-radius: 6px;
  margin: 0 0.25rem;

  &:hover { background: #313244; }
}

.channel-avatar {
  width: 36px;
  height: 36px;
  border-radius: 50%;
  background: #45475a;
  display: flex;
  align-items: center;
  justify-content: center;
  font-weight: 700;
  font-size: 0.9rem;
  flex-shrink: 0;
}

.channel-info {
  flex: 1;
  min-width: 0;
}

.channel-name-row {
  display: flex;
  align-items: center;
  justify-content: space-between;
  gap: 0.25rem;
}

.channel-name {
  font-size: 0.9rem;
  font-weight: 600;
  white-space: nowrap;
  overflow: hidden;
  text-overflow: ellipsis;
}

.badge {
  background: #89b4fa;
  color: #1e1e2e;
  border-radius: 10px;
  padding: 0 6px;
  font-size: 0.7rem;
  font-weight: 700;
  min-width: 18px;
  text-align: center;
  flex-shrink: 0;
}

.preview {
  font-size: 0.78rem;
  color: #6c7086;
  white-space: nowrap;
  overflow: hidden;
  text-overflow: ellipsis;
  display: block;
}

.empty {
  padding: 1rem;
  color: #6c7086;
  font-size: 0.85rem;
  text-align: center;
  list-style: none;
}

.sidebar-footer {
  padding: 0.75rem 1rem;
  border-top: 1px solid #313244;
}

.btn-logout {
  background: none;
  border: none;
  color: #f38ba8;
  cursor: pointer;
  font-size: 0.85rem;
  padding: 0;

  &:hover { text-decoration: underline; }
}
```

### 6.4 Commit

```
git add client/src/app/features/channel-list/
git commit -m "feat(client): add ChannelList sidebar component with unread badges"
```

---

## Task 7: Client create group dialog

**Files to create:**
- `client/src/app/features/create-group/create-group.component.ts`
- `client/src/app/features/create-group/create-group.component.html`
- `client/src/app/features/create-group/create-group.component.scss`

### Overview

A modal overlay with:
- Group name input (required)
- Friend search input to add members (uses `FriendService.friends` signal)
- Checkbox list of friends to add
- Create / Cancel buttons
- Emits `(created)` and `(cancelled)` Output events (no router navigation — parent controls visibility)

### 7.1 Create `client/src/app/features/create-group/create-group.component.ts`

```typescript
import { Component, inject, signal, Output, EventEmitter } from '@angular/core';
import { CommonModule } from '@angular/common';
import { FormsModule } from '@angular/forms';
import { ChannelService } from '../../core/channels/channel.service';
import { FriendService } from '../../core/friends/friend.service';

@Component({
  selector: 'app-create-group',
  standalone: true,
  imports: [CommonModule, FormsModule],
  templateUrl: './create-group.component.html',
  styleUrl: './create-group.component.scss',
})
export class CreateGroupComponent {
  @Output() created = new EventEmitter<void>();
  @Output() cancelled = new EventEmitter<void>();

  private channelService = inject(ChannelService);
  friendService = inject(FriendService);

  groupName = signal('');
  selectedIds = signal<Set<number>>(new Set());
  creating = signal(false);
  error = signal('');

  toggleMember(id: number): void {
    const set = new Set(this.selectedIds());
    if (set.has(id)) {
      set.delete(id);
    } else {
      set.add(id);
    }
    this.selectedIds.set(set);
  }

  isSelected(id: number): boolean {
    return this.selectedIds().has(id);
  }

  async onCreate(): Promise<void> {
    const name = this.groupName().trim();
    if (!name) {
      this.error.set('Group name is required.');
      return;
    }
    this.creating.set(true);
    this.error.set('');
    try {
      await this.channelService.createGroup(name, [...this.selectedIds()]);
      this.created.emit();
    } catch (err: any) {
      this.error.set(err?.error?.error ?? 'Failed to create group.');
    } finally {
      this.creating.set(false);
    }
  }

  onCancel(): void {
    this.cancelled.emit();
  }
}
```

### 7.2 Create `client/src/app/features/create-group/create-group.component.html`

```html
<div class="overlay" (click)="onCancel()">
  <div class="dialog" (click)="$event.stopPropagation()">
    <h3>Create Group</h3>

    @if (error()) {
      <p class="error">{{ error() }}</p>
    }

    <label class="field-label">Group name</label>
    <input
      class="input"
      type="text"
      placeholder="e.g. Team Alpha"
      [ngModel]="groupName()"
      (ngModelChange)="groupName.set($event)"
    />

    <label class="field-label">Add friends</label>
    <ul class="friend-list">
      @for (friend of friendService.friends(); track friend.id) {
        <li class="friend-row" (click)="toggleMember(friend.id)">
          <input type="checkbox" [checked]="isSelected(friend.id)" (click)="$event.stopPropagation()" />
          <div class="avatar">{{ friend.display_name[0] | uppercase }}</div>
          <span>{{ friend.display_name }}</span>
        </li>
      } @empty {
        <li class="empty">No friends to add. Add friends in Contacts first.</li>
      }
    </ul>

    <div class="actions">
      <button class="btn-cancel" (click)="onCancel()">Cancel</button>
      <button class="btn-create" (click)="onCreate()" [disabled]="creating()">
        {{ creating() ? 'Creating…' : 'Create' }}
      </button>
    </div>
  </div>
</div>
```

### 7.3 Create `client/src/app/features/create-group/create-group.component.scss`

```scss
.overlay {
  position: fixed;
  inset: 0;
  background: rgba(0, 0, 0, 0.6);
  display: flex;
  align-items: center;
  justify-content: center;
  z-index: 1000;
}

.dialog {
  background: #1e1e2e;
  border: 1px solid #313244;
  border-radius: 8px;
  padding: 1.5rem;
  width: 380px;
  max-width: 90vw;
  color: #cdd6f4;
  display: flex;
  flex-direction: column;
  gap: 0.75rem;
}

h3 {
  margin: 0;
  font-size: 1.1rem;
}

.error {
  color: #f38ba8;
  font-size: 0.85rem;
  margin: 0;
}

.field-label {
  font-size: 0.8rem;
  color: #a6adc8;
  text-transform: uppercase;
  letter-spacing: 0.06em;
}

.input {
  background: #313244;
  border: 1px solid #45475a;
  border-radius: 4px;
  color: #cdd6f4;
  padding: 0.5rem 0.75rem;
  font-size: 0.95rem;
  width: 100%;
  box-sizing: border-box;

  &:focus { outline: none; border-color: #89b4fa; }
}

.friend-list {
  list-style: none;
  margin: 0;
  padding: 0;
  max-height: 200px;
  overflow-y: auto;
  border: 1px solid #313244;
  border-radius: 4px;
}

.friend-row {
  display: flex;
  align-items: center;
  gap: 0.6rem;
  padding: 0.5rem 0.75rem;
  cursor: pointer;

  &:hover { background: #313244; }

  input[type="checkbox"] { accent-color: #89b4fa; cursor: pointer; }
}

.avatar {
  width: 28px;
  height: 28px;
  border-radius: 50%;
  background: #45475a;
  display: flex;
  align-items: center;
  justify-content: center;
  font-size: 0.8rem;
  font-weight: 700;
}

.empty {
  padding: 0.75rem;
  color: #6c7086;
  font-size: 0.85rem;
}

.actions {
  display: flex;
  justify-content: flex-end;
  gap: 0.5rem;
  margin-top: 0.5rem;
}

.btn-cancel {
  background: none;
  border: 1px solid #45475a;
  color: #a6adc8;
  border-radius: 4px;
  padding: 0.4rem 1rem;
  cursor: pointer;

  &:hover { background: #313244; }
}

.btn-create {
  background: #89b4fa;
  border: none;
  color: #1e1e2e;
  border-radius: 4px;
  padding: 0.4rem 1.2rem;
  font-weight: 600;
  cursor: pointer;

  &:disabled { opacity: 0.5; cursor: not-allowed; }
  &:not(:disabled):hover { background: #b4d0ff; }
}
```

### 7.4 Commit

```
git add client/src/app/features/create-group/
git commit -m "feat(client): add CreateGroup dialog component"
```

---

## Task 8: Client channel settings page

**Files to create:**
- `client/src/app/features/channel-settings/channel-settings.component.ts`
- `client/src/app/features/channel-settings/channel-settings.component.html`
- `client/src/app/features/channel-settings/channel-settings.component.scss`

### Overview

Route: `/channels/:id/settings`

Features:
- Load channel detail + members on init
- Display channel name and avatar (editable for admins/owners)
- List members with their roles
- Add member by ID input (admin/owner only)
- Remove member button (admin/owner only, grayed out for owner row)
- Leave channel button (non-owners only)
- "Back" navigation to root

### 8.1 Create `client/src/app/features/channel-settings/channel-settings.component.ts`

```typescript
import { Component, inject, signal, OnInit } from '@angular/core';
import { CommonModule } from '@angular/common';
import { FormsModule } from '@angular/forms';
import { ActivatedRoute, Router, RouterLink } from '@angular/router';
import { ChannelService, Channel, ChannelMember } from '../../core/channels/channel.service';
import { AuthService } from '../../core/auth/auth.service';

@Component({
  selector: 'app-channel-settings',
  standalone: true,
  imports: [CommonModule, FormsModule, RouterLink],
  templateUrl: './channel-settings.component.html',
  styleUrl: './channel-settings.component.scss',
})
export class ChannelSettingsComponent implements OnInit {
  private route = inject(ActivatedRoute);
  private router = inject(Router);
  private channelService = inject(ChannelService);
  auth = inject(AuthService);

  channel = signal<Channel | null>(null);
  members = signal<ChannelMember[]>([]);
  loading = signal(true);
  error = signal('');
  success = signal('');

  // Edit name state
  editName = signal('');
  savingName = signal(false);

  // Add member state
  newMemberIdStr = signal('');
  addingMember = signal(false);

  private channelId = 0;

  async ngOnInit(): Promise<void> {
    const id = Number(this.route.snapshot.paramMap.get('id'));
    this.channelId = id;
    await this.reload();
  }

  private async reload(): Promise<void> {
    this.loading.set(true);
    this.error.set('');
    try {
      const [ch, members] = await Promise.all([
        this.channelService.getChannel(this.channelId),
        this.channelService.listMembers(this.channelId),
      ]);
      this.channel.set(ch);
      this.members.set(members);
      this.editName.set(ch.name);
    } catch {
      this.error.set('Failed to load channel.');
    } finally {
      this.loading.set(false);
    }
  }

  get myMember(): ChannelMember | undefined {
    const me = this.auth.currentUser();
    return this.members().find((m) => m.user_id === me?.id);
  }

  get isAdminOrOwner(): boolean {
    return (this.myMember?.role ?? 0) >= 2;
  }

  get isOwner(): boolean {
    return (this.myMember?.role ?? 0) === 3;
  }

  roleName(role: number): string {
    return role === 3 ? 'Owner' : role === 2 ? 'Admin' : 'Member';
  }

  async saveName(): Promise<void> {
    const name = this.editName().trim();
    if (!name) return;
    this.savingName.set(true);
    this.error.set('');
    try {
      const updated = await this.channelService.updateChannel(this.channelId, name, '');
      this.channel.set(updated);
      this.success.set('Channel name updated.');
    } catch {
      this.error.set('Failed to update name.');
    } finally {
      this.savingName.set(false);
    }
  }

  async addMember(): Promise<void> {
    const id = Number(this.newMemberIdStr().trim());
    if (!id) { this.error.set('Enter a valid user ID.'); return; }
    this.addingMember.set(true);
    this.error.set('');
    try {
      await this.channelService.addMember(this.channelId, id);
      this.newMemberIdStr.set('');
      this.success.set('Member added.');
      await this.reload();
    } catch (err: any) {
      this.error.set(err?.error?.error ?? 'Failed to add member.');
    } finally {
      this.addingMember.set(false);
    }
  }

  async removeMember(userId: number): Promise<void> {
    this.error.set('');
    try {
      await this.channelService.removeMember(this.channelId, userId);
      this.success.set('Member removed.');
      await this.reload();
    } catch (err: any) {
      this.error.set(err?.error?.error ?? 'Failed to remove member.');
    }
  }

  async leave(): Promise<void> {
    this.error.set('');
    try {
      await this.channelService.leaveChannel(this.channelId);
      this.router.navigate(['/']);
    } catch (err: any) {
      this.error.set(err?.error?.error ?? 'Failed to leave channel.');
    }
  }
}
```

### 8.2 Create `client/src/app/features/channel-settings/channel-settings.component.html`

```html
<div class="settings-page">
  <div class="page-header">
    <a routerLink="/" class="back-link">← Back</a>
    <h2>Channel Settings</h2>
  </div>

  @if (loading()) {
    <p class="loading">Loading…</p>
  } @else if (channel(); as ch) {
    @if (success()) {
      <p class="msg-success">{{ success() }}</p>
    }
    @if (error()) {
      <p class="msg-error">{{ error() }}</p>
    }

    <!-- Channel name section -->
    <section class="section">
      <h3>Channel Name</h3>
      @if (isAdminOrOwner) {
        <div class="row">
          <input
            class="input"
            type="text"
            [ngModel]="editName()"
            (ngModelChange)="editName.set($event)"
          />
          <button class="btn-primary" (click)="saveName()" [disabled]="savingName()">
            {{ savingName() ? 'Saving…' : 'Save' }}
          </button>
        </div>
      } @else {
        <p>{{ ch.name || '(unnamed)' }}</p>
      }
      <p class="meta">Type: {{ ch.type === 1 ? 'Direct Message' : 'Group' }}</p>
    </section>

    <!-- Members section -->
    <section class="section">
      <h3>Members ({{ members().length }})</h3>
      <ul class="member-list">
        @for (m of members(); track m.user_id) {
          <li class="member-row">
            <div class="member-avatar">{{ m.user_id }}</div>
            <div class="member-info">
              <span class="member-id">User #{{ m.user_id }}</span>
              <span class="member-role">{{ roleName(m.role) }}</span>
            </div>
            @if (isAdminOrOwner && m.role !== 3 && m.user_id !== auth.currentUser()?.id) {
              <button class="btn-remove" (click)="removeMember(m.user_id)">Remove</button>
            }
          </li>
        }
      </ul>

      @if (isAdminOrOwner) {
        <div class="add-member">
          <input
            class="input"
            type="number"
            placeholder="User ID to add"
            [ngModel]="newMemberIdStr()"
            (ngModelChange)="newMemberIdStr.set($event)"
          />
          <button class="btn-primary" (click)="addMember()" [disabled]="addingMember()">
            {{ addingMember() ? 'Adding…' : 'Add Member' }}
          </button>
        </div>
      }
    </section>

    <!-- Leave channel -->
    @if (!isOwner) {
      <section class="section danger-zone">
        <h3>Leave Channel</h3>
        <p>You will be removed from this channel.</p>
        <button class="btn-danger" (click)="leave()">Leave Channel</button>
      </section>
    }
  }
</div>
```

### 8.3 Create `client/src/app/features/channel-settings/channel-settings.component.scss`

```scss
.settings-page {
  max-width: 600px;
  margin: 0 auto;
  padding: 1.5rem;
  color: #cdd6f4;
}

.page-header {
  display: flex;
  align-items: center;
  gap: 1rem;
  margin-bottom: 1.5rem;

  h2 { margin: 0; }
}

.back-link {
  color: #89b4fa;
  text-decoration: none;
  font-size: 0.9rem;

  &:hover { text-decoration: underline; }
}

.loading { color: #6c7086; }

.msg-success {
  color: #a6e3a1;
  background: rgba(166, 227, 161, 0.1);
  border-radius: 4px;
  padding: 0.5rem 0.75rem;
  margin-bottom: 1rem;
}

.msg-error {
  color: #f38ba8;
  background: rgba(243, 139, 168, 0.1);
  border-radius: 4px;
  padding: 0.5rem 0.75rem;
  margin-bottom: 1rem;
}

.section {
  background: #1e1e2e;
  border: 1px solid #313244;
  border-radius: 8px;
  padding: 1.25rem;
  margin-bottom: 1rem;

  h3 {
    margin: 0 0 0.75rem;
    font-size: 0.95rem;
    text-transform: uppercase;
    letter-spacing: 0.06em;
    color: #a6adc8;
  }
}

.row {
  display: flex;
  gap: 0.5rem;
}

.input {
  flex: 1;
  background: #313244;
  border: 1px solid #45475a;
  border-radius: 4px;
  color: #cdd6f4;
  padding: 0.45rem 0.75rem;
  font-size: 0.9rem;

  &:focus { outline: none; border-color: #89b4fa; }
}

.meta {
  margin: 0.5rem 0 0;
  font-size: 0.82rem;
  color: #6c7086;
}

.member-list {
  list-style: none;
  margin: 0 0 0.75rem;
  padding: 0;
}

.member-row {
  display: flex;
  align-items: center;
  gap: 0.75rem;
  padding: 0.5rem 0;
  border-bottom: 1px solid #313244;

  &:last-child { border-bottom: none; }
}

.member-avatar {
  width: 32px;
  height: 32px;
  border-radius: 50%;
  background: #45475a;
  display: flex;
  align-items: center;
  justify-content: center;
  font-size: 0.75rem;
  color: #a6adc8;
  flex-shrink: 0;
}

.member-info {
  flex: 1;
  display: flex;
  flex-direction: column;
}

.member-id { font-size: 0.9rem; }

.member-role {
  font-size: 0.75rem;
  color: #6c7086;
}

.btn-remove {
  background: none;
  border: 1px solid #f38ba8;
  color: #f38ba8;
  border-radius: 4px;
  padding: 0.25rem 0.6rem;
  font-size: 0.8rem;
  cursor: pointer;

  &:hover { background: rgba(243, 139, 168, 0.1); }
}

.add-member {
  display: flex;
  gap: 0.5rem;
}

.btn-primary {
  background: #89b4fa;
  border: none;
  color: #1e1e2e;
  border-radius: 4px;
  padding: 0.45rem 1rem;
  font-weight: 600;
  cursor: pointer;
  white-space: nowrap;

  &:disabled { opacity: 0.5; cursor: not-allowed; }
  &:not(:disabled):hover { background: #b4d0ff; }
}

.danger-zone {
  border-color: rgba(243, 139, 168, 0.3);

  h3 { color: #f38ba8; }
}

.btn-danger {
  background: none;
  border: 1px solid #f38ba8;
  color: #f38ba8;
  border-radius: 4px;
  padding: 0.5rem 1.2rem;
  cursor: pointer;
  font-size: 0.9rem;

  &:hover { background: rgba(243, 139, 168, 0.1); }
}
```

### 8.4 Commit

```
git add client/src/app/features/channel-settings/
git commit -m "feat(client): add ChannelSettings page with member management and leave"
```

---

## Task 9: Integration verification

### 9.1 Run all server tests

```bash
cd /Users/mac17/workspace/ai/im/server
go test ./...
```

Expected: all existing tests plus new store and handler tests pass, zero failures.

### 9.2 Build server

```bash
cd /Users/mac17/workspace/ai/im/server
go build ./cmd/gateway/...
```

Expected: clean build.

### 9.3 Build Angular client

```bash
cd /Users/mac17/workspace/ai/im/client
npm run build
```

Expected: successful build, zero TypeScript errors.

If `npm run build` does not exist, check `package.json` and use the appropriate build command (e.g., `npx ng build`).

### 9.4 Manual smoke test (optional if server is running)

Start the server:
```bash
cd /Users/mac17/workspace/ai/im/server
go run ./cmd/gateway/...
```

Then test channel endpoints with curl:

```bash
# Register + login to get TOKEN
TOKEN=$(curl -s -X POST http://localhost:8080/api/auth/login \
  -H 'Content-Type: application/json' \
  -d '{"login":"alice","password":"secret"}' | jq -r '.token')

# Create a group
curl -s -X POST http://localhost:8080/api/channels \
  -H "Authorization: Bearer $TOKEN" \
  -H 'Content-Type: application/json' \
  -d '{"name":"my-group","member_ids":[]}' | jq .

# List channels
curl -s http://localhost:8080/api/channels \
  -H "Authorization: Bearer $TOKEN" | jq .

# Get channel 1
curl -s http://localhost:8080/api/channels/1 \
  -H "Authorization: Bearer $TOKEN" | jq .
```

Expected responses: structured JSON with channel data; no 5xx errors.

### 9.5 Commit (if any fixups needed)

```
git add -p
git commit -m "fix: integration fixes after Plan 4 verification"
```

---

## Summary of all Plan 4 files

### Server (new/modified)
| File | Action |
|------|--------|
| `server/internal/store/channel.go` | Modified: add `FindDM`, `ListByUserWithPreview`, `Update`, `ChannelWithPreview` type |
| `server/internal/store/channel_test.go` | Modified: append 4 new test functions |
| `server/internal/handler/channel.go` | Created: `ChannelHandler` with 9 endpoints |
| `server/internal/handler/channel_test.go` | Created: 9 unit tests with stub stores |
| `server/cmd/gateway/main.go` | Modified: add channel store + 9 route registrations |

### Client (new/modified)
| File | Action |
|------|--------|
| `client/src/app/core/channels/channel.service.ts` | Created: `ChannelService` |
| `client/src/app/features/main-layout/main-layout.component.ts` | Created |
| `client/src/app/features/main-layout/main-layout.component.html` | Created |
| `client/src/app/features/main-layout/main-layout.component.scss` | Created |
| `client/src/app/features/channel-list/channel-list.component.ts` | Created |
| `client/src/app/features/channel-list/channel-list.component.html` | Created |
| `client/src/app/features/channel-list/channel-list.component.scss` | Created |
| `client/src/app/features/create-group/create-group.component.ts` | Created |
| `client/src/app/features/create-group/create-group.component.html` | Created |
| `client/src/app/features/create-group/create-group.component.scss` | Created |
| `client/src/app/features/channel-settings/channel-settings.component.ts` | Created |
| `client/src/app/features/channel-settings/channel-settings.component.html` | Created |
| `client/src/app/features/channel-settings/channel-settings.component.scss` | Created |
| `client/src/app/app.routes.ts` | Modified: nest protected routes under `MainLayoutComponent` |

---

## Design decisions and notes

1. **`ErrNotFound` in handler package**: `friend.go` does not export `ErrNotFound` from the handler package — it only uses `store.ErrNotFound`. The handler tests reference `handler.ErrNotFound`. Declare it once in `channel.go` if it does not already exist.

2. **`pathID` uses `r.PathValue`**: Go 1.22+ `net/http` supports `{id}` wildcards and `r.PathValue("id")`. Verify server Go version with `go version`. If < 1.22, use a path-splitting utility instead.

3. **DM idempotency**: `CreateOrGetDM` is fully idempotent — repeat calls always return the same channel. The client can call this whenever opening a friend's profile.

4. **Owner cannot leave**: This prevents channels with no owner. A future plan can add ownership transfer.

5. **`ListByUserWithPreview` uses `LATERAL`**: Requires PostgreSQL 9.3+. The `GREATEST(..., 0)` guard prevents negative unread counts from phantom message edge cases.

6. **Main layout restructure**: The `home` route becomes a child of `main-layout`. The existing `HomeComponent` is unchanged but now renders inside the right panel next to the sidebar. The `/contacts` route similarly becomes a child, giving it the same sidebar context.

7. **`FriendService.loadFriends()` in `CreateGroup`**: The create-group dialog relies on `friendService.friends` signal already being populated. `ChannelListComponent` does not call `loadFriends()` — that responsibility stays with `ContactsComponent.ngOnInit`. For the create-group dialog to show friends, the app must have already navigated to contacts at least once, OR `MainLayoutComponent.ngOnInit` should also call `friendService.loadFriends()`. Add this to `MainLayoutComponent.ngOnInit` for robustness:

   ```typescript
   async ngOnInit(): Promise<void> {
     await Promise.all([
       this.channelService.loadChannels(),
       this.friendService.loadFriends(),   // ensure friends are available for CreateGroup
     ]);
   }
   ```

   Inject `FriendService` into `MainLayoutComponent` accordingly.

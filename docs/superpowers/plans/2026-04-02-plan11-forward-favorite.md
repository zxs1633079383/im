# Plan 11: 消息转发与收藏 Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** 实现消息转发（将已有消息复制到一个或多个目标频道）和消息收藏（收藏/取消收藏消息，查看收藏列表并点击跳转）。

**Architecture:** 服务端：转发在 `handler.MessageHandler` 扩展一个新端点（POST /api/messages/forward），直接调用已有 `Send`；收藏新增 `store.FavoriteStore` + `handler.FavoriteHandler`（POST/DELETE /api/favorites/{message_id}, GET /api/favorites）。客户端：聊天消息右键菜单新增"转发"和"收藏"，转发弹窗选择目标频道，收藏页面列出收藏并支持跳转。

**Tech Stack:** Go (pgx/v5), Angular 17+ (signals, standalone components), SCSS

---

## 目录结构（Plan 11 新增/修改文件）

```
server/
├── internal/
│   ├── store/
│   │   ├── favorite.go            # NEW: FavoriteStore
│   │   └── favorite_test.go       # NEW
│   └── handler/
│       ├── favorite.go            # NEW: FavoriteHandler
│       ├── favorite_test.go       # NEW
│       └── message.go             # MODIFY: add ForwardMessages endpoint
│       └── message_test.go        # MODIFY: add forward tests
└── cmd/gateway/main.go            # MODIFY: wire new handlers + routes

client/src/app/
├── core/
│   └── favorites/
│       └── favorite.service.ts    # NEW
├── features/
│   ├── chat/
│   │   ├── chat.component.ts      # MODIFY: context menu, forward dialog trigger
│   │   ├── chat.component.html    # MODIFY: context menu, forward dialog
│   │   └── chat.component.scss    # MODIFY: context menu, dialog styles
│   └── favorites/
│       ├── favorites.component.ts   # NEW
│       ├── favorites.component.html # NEW
│       └── favorites.component.scss # NEW
└── app.routes.ts                    # MODIFY: add /favorites route
```

---

## Task 1: Forward Handler + Tests

**Goal:** `POST /api/messages/forward` — copy a message to one or more target channels.

### 1.1 Modify `server/internal/handler/message.go`

Add a `ForwardChannelStore` interface and a `forwardMessageBody` type:

```go
// ForwardChannelStore is used by MessageHandler to validate target channel membership.
type ForwardChannelStore interface {
	GetMember(ctx context.Context, channelID, userID int64) (*model.ChannelMember, error)
}
```

> **Note:** `MsgChannelStore` already includes `GetMember`, so no new field is needed on `MessageHandler` — the existing `h.channels` satisfies both.

Add `ForwardMessages` method to `MessageHandler`:

```go
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
```

Also add a `GetByID` method to the `MsgStore` interface:

```go
// MsgStore updated — add GetByID:
type MsgStore interface {
	Send(ctx context.Context, msg *model.Message) error
	GetByID(ctx context.Context, id int64) (*model.Message, error)
	FetchForUser(ctx context.Context, channelID, userID int64, afterSeq int64, limit int) ([]model.Message, error)
	FetchBefore(ctx context.Context, channelID, userID int64, beforeSeq int64, limit int) ([]model.Message, error)
	FetchAround(ctx context.Context, channelID, userID int64, aroundSeq int64, limit int) ([]model.Message, error)
}
```

Add `GetByID` to `store.MessageStore` (`server/internal/store/message.go`):

```go
// GetByID returns a single message by primary key.
func (s *MessageStore) GetByID(ctx context.Context, id int64) (*model.Message, error) {
	var m model.Message
	var clientMsgID *string
	err := s.pool.QueryRow(ctx,
		`SELECT id, channel_id, seq, client_msg_id, sender_id, msg_type, content, visible_to, reply_to, forwarded_from, created_at
		 FROM messages WHERE id = $1`, id,
	).Scan(&m.ID, &m.ChannelID, &m.Seq, &clientMsgID, &m.SenderID, &m.MsgType,
		&m.Content, &m.VisibleTo, &m.ReplyTo, &m.ForwardedFrom, &m.CreatedAt)
	if err != nil {
		return nil, fmt.Errorf("get message by id: %w", err)
	}
	if clientMsgID != nil {
		m.ClientMsgID = *clientMsgID
	}
	return &m, nil
}
```

### 1.2 Add forward tests to `server/internal/handler/message_test.go`

```go
func TestForwardMessages_RequiresAuth(t *testing.T) {
	h := newMessageHandler()
	body := `{"message_id":1,"target_channel_ids":[2]}`
	req := httptest.NewRequest(http.MethodPost, "/api/messages/forward",
		strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	h.ForwardMessages(w, req)
	if w.Code != http.StatusUnauthorized {
		t.Errorf("want 401, got %d", w.Code)
	}
}

func TestForwardMessages_MissingMessageID(t *testing.T) {
	h := newMessageHandler()
	req := httptest.NewRequest(http.MethodPost, "/api/messages/forward",
		strings.NewReader(`{"target_channel_ids":[2]}`))
	req.Header.Set("Content-Type", "application/json")
	req = withClaims(req, 1, "alice")
	w := httptest.NewRecorder()
	h.ForwardMessages(w, req)
	if w.Code != http.StatusUnprocessableEntity {
		t.Errorf("want 422, got %d", w.Code)
	}
}
```

**Commands:**
```bash
cd /Users/mac17/workspace/ai/im/server
go build ./internal/...
go test ./internal/handler/... -run TestForward -v
```

**Commit:**
```bash
cd /Users/mac17/workspace/ai/im
git add server/internal/handler/message.go server/internal/handler/message_test.go server/internal/store/message.go
git commit -m "feat(handler): add ForwardMessages endpoint + GetByID to MessageStore"
```

---

## Task 2: Favorite Store + Handler + Tests

**Goal:** `store.FavoriteStore` wraps the `message_favorites` table. `handler.FavoriteHandler` exposes POST/DELETE /api/favorites/{message_id} and GET /api/favorites.

### 2.1 Create `server/internal/store/favorite.go`

```go
package store

import (
	"context"
	"fmt"

	"github.com/jackc/pgx/v5/pgxpool"
	"im-server/internal/model"
)

// FavoriteStore manages message favorites for users.
type FavoriteStore struct {
	pool *pgxpool.Pool
}

func NewFavoriteStore(pool *pgxpool.Pool) *FavoriteStore {
	return &FavoriteStore{pool: pool}
}

// FavoriteWithMessage extends MessageFavorite with the full message for display.
type FavoriteWithMessage struct {
	model.MessageFavorite
	Message model.Message `json:"message"`
}

// Add adds a message to the user's favorites. Idempotent.
func (s *FavoriteStore) Add(ctx context.Context, userID, messageID int64) error {
	_, err := s.pool.Exec(ctx,
		`INSERT INTO message_favorites (user_id, message_id)
		 VALUES ($1, $2)
		 ON CONFLICT (user_id, message_id) DO NOTHING`,
		userID, messageID,
	)
	if err != nil {
		return fmt.Errorf("add favorite: %w", err)
	}
	return nil
}

// Remove removes a message from the user's favorites. Idempotent.
func (s *FavoriteStore) Remove(ctx context.Context, userID, messageID int64) error {
	_, err := s.pool.Exec(ctx,
		`DELETE FROM message_favorites WHERE user_id = $1 AND message_id = $2`,
		userID, messageID,
	)
	if err != nil {
		return fmt.Errorf("remove favorite: %w", err)
	}
	return nil
}

// List returns all favorites for a user with the associated message, newest first.
func (s *FavoriteStore) List(ctx context.Context, userID int64) ([]FavoriteWithMessage, error) {
	rows, err := s.pool.Query(ctx,
		`SELECT mf.user_id, mf.message_id, mf.created_at,
		        m.id, m.channel_id, m.seq, m.client_msg_id, m.sender_id, m.msg_type,
		        m.content, m.visible_to, m.reply_to, m.forwarded_from, m.created_at
		 FROM message_favorites mf
		 JOIN messages m ON m.id = mf.message_id
		 WHERE mf.user_id = $1
		 ORDER BY mf.created_at DESC`,
		userID,
	)
	if err != nil {
		return nil, fmt.Errorf("list favorites: %w", err)
	}
	defer rows.Close()

	var results []FavoriteWithMessage
	for rows.Next() {
		var fw FavoriteWithMessage
		var clientMsgID *string
		if err := rows.Scan(
			&fw.UserID, &fw.MessageID, &fw.CreatedAt,
			&fw.Message.ID, &fw.Message.ChannelID, &fw.Message.Seq, &clientMsgID,
			&fw.Message.SenderID, &fw.Message.MsgType, &fw.Message.Content,
			&fw.Message.VisibleTo, &fw.Message.ReplyTo, &fw.Message.ForwardedFrom,
			&fw.Message.CreatedAt,
		); err != nil {
			return nil, fmt.Errorf("scan favorite: %w", err)
		}
		if clientMsgID != nil {
			fw.Message.ClientMsgID = *clientMsgID
		}
		results = append(results, fw)
	}
	return results, rows.Err()
}
```

### 2.2 Create `server/internal/store/favorite_test.go`

```go
package store_test

import (
	"context"
	"testing"

	"im-server/internal/model"
	"im-server/internal/store"
)

func TestFavoriteStore_AddRemoveList(t *testing.T) {
	pool := mustOpenTestDB(t)
	fs := store.NewFavoriteStore(pool)
	ms := store.NewMessageStore(pool)
	ctx := context.Background()

	userID := seedUser(t, pool, "fav_user")
	chID := seedChannel(t, pool, userID)
	seedMembership(t, pool, chID, userID)
	msg := &model.Message{ChannelID: chID, SenderID: userID, MsgType: model.MsgTypeText, Content: "fav me"}
	if err := ms.Send(ctx, msg); err != nil {
		t.Fatalf("Send: %v", err)
	}

	// Add
	if err := fs.Add(ctx, userID, msg.ID); err != nil {
		t.Fatalf("Add: %v", err)
	}

	// List
	favs, err := fs.List(ctx, userID)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(favs) == 0 {
		t.Fatal("expected at least one favorite")
	}
	if favs[0].MessageID != msg.ID {
		t.Errorf("want message ID %d, got %d", msg.ID, favs[0].MessageID)
	}

	// Idempotent add
	if err := fs.Add(ctx, userID, msg.ID); err != nil {
		t.Fatalf("duplicate Add should be idempotent: %v", err)
	}

	// Remove
	if err := fs.Remove(ctx, userID, msg.ID); err != nil {
		t.Fatalf("Remove: %v", err)
	}
	favs2, _ := fs.List(ctx, userID)
	for _, f := range favs2 {
		if f.MessageID == msg.ID {
			t.Fatal("message should have been removed from favorites")
		}
	}
}
```

### 2.3 Create `server/internal/handler/favorite.go`

```go
package handler

import (
	"context"
	"log/slog"
	"net/http"

	"im-server/internal/store"
)

// ---------- store interface ----------

// FavStore is the subset of store.FavoriteStore used by FavoriteHandler.
type FavStore interface {
	Add(ctx context.Context, userID, messageID int64) error
	Remove(ctx context.Context, userID, messageID int64) error
	List(ctx context.Context, userID int64) ([]store.FavoriteWithMessage, error)
}

// ---------- handler ----------

// FavoriteHandler serves favorite add/remove/list endpoints.
type FavoriteHandler struct {
	favs FavStore
	log  *slog.Logger
}

func NewFavoriteHandler(favs FavStore, log *slog.Logger) *FavoriteHandler {
	return &FavoriteHandler{favs: favs, log: log}
}

// ---------- POST /api/favorites/{message_id} ----------

// AddFavorite handles POST /api/favorites/{message_id}.
func (h *FavoriteHandler) AddFavorite(w http.ResponseWriter, r *http.Request) {
	claims, ok := claimsFromCtx(r)
	if !ok {
		writeError(w, http.StatusUnauthorized, "unauthorized")
		return
	}

	messageID, ok := pathID(r, "message_id")
	if !ok {
		writeError(w, http.StatusBadRequest, "invalid message_id")
		return
	}

	if err := h.favs.Add(r.Context(), claims.UserID, messageID); err != nil {
		h.log.Error("add favorite", "error", err)
		writeError(w, http.StatusInternalServerError, "internal error")
		return
	}

	writeJSON(w, http.StatusCreated, map[string]string{"status": "ok"})
}

// ---------- DELETE /api/favorites/{message_id} ----------

// RemoveFavorite handles DELETE /api/favorites/{message_id}.
func (h *FavoriteHandler) RemoveFavorite(w http.ResponseWriter, r *http.Request) {
	claims, ok := claimsFromCtx(r)
	if !ok {
		writeError(w, http.StatusUnauthorized, "unauthorized")
		return
	}

	messageID, ok := pathID(r, "message_id")
	if !ok {
		writeError(w, http.StatusBadRequest, "invalid message_id")
		return
	}

	if err := h.favs.Remove(r.Context(), claims.UserID, messageID); err != nil {
		h.log.Error("remove favorite", "error", err)
		writeError(w, http.StatusInternalServerError, "internal error")
		return
	}

	w.WriteHeader(http.StatusNoContent)
}

// ---------- GET /api/favorites ----------

// ListFavorites handles GET /api/favorites.
func (h *FavoriteHandler) ListFavorites(w http.ResponseWriter, r *http.Request) {
	claims, ok := claimsFromCtx(r)
	if !ok {
		writeError(w, http.StatusUnauthorized, "unauthorized")
		return
	}

	favs, err := h.favs.List(r.Context(), claims.UserID)
	if err != nil {
		h.log.Error("list favorites", "error", err)
		writeError(w, http.StatusInternalServerError, "internal error")
		return
	}
	if favs == nil {
		favs = []store.FavoriteWithMessage{}
	}

	writeJSON(w, http.StatusOK, map[string]any{"favorites": favs})
}
```

### 2.4 Create `server/internal/handler/favorite_test.go`

```go
package handler_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"im-server/internal/handler"
	"im-server/internal/store"
)

// ---------- stubs ----------

type stubFavStore struct {
	items []store.FavoriteWithMessage
}

func (s *stubFavStore) Add(_ context.Context, _, _ int64) error    { return nil }
func (s *stubFavStore) Remove(_ context.Context, _, _ int64) error { return nil }
func (s *stubFavStore) List(_ context.Context, _ int64) ([]store.FavoriteWithMessage, error) {
	return s.items, nil
}

// ---------- tests ----------

func TestFavoriteAdd_RequiresAuth(t *testing.T) {
	h := handler.NewFavoriteHandler(&stubFavStore{}, testLogger())
	req := httptest.NewRequest(http.MethodPost, "/api/favorites/1", nil)
	req.SetPathValue("message_id", "1")
	w := httptest.NewRecorder()
	h.AddFavorite(w, req)
	if w.Code != http.StatusUnauthorized {
		t.Errorf("want 401, got %d", w.Code)
	}
}

func TestFavoriteAdd_Success(t *testing.T) {
	h := handler.NewFavoriteHandler(&stubFavStore{}, testLogger())
	req := httptest.NewRequest(http.MethodPost, "/api/favorites/42", nil)
	req.SetPathValue("message_id", "42")
	req = withClaims(req, 1, "alice")
	w := httptest.NewRecorder()
	h.AddFavorite(w, req)
	if w.Code != http.StatusCreated {
		t.Errorf("want 201, got %d", w.Code)
	}
}

func TestFavoriteRemove_Success(t *testing.T) {
	h := handler.NewFavoriteHandler(&stubFavStore{}, testLogger())
	req := httptest.NewRequest(http.MethodDelete, "/api/favorites/42", nil)
	req.SetPathValue("message_id", "42")
	req = withClaims(req, 1, "alice")
	w := httptest.NewRecorder()
	h.RemoveFavorite(w, req)
	if w.Code != http.StatusNoContent {
		t.Errorf("want 204, got %d", w.Code)
	}
}

func TestFavoriteList_ReturnsEmpty(t *testing.T) {
	h := handler.NewFavoriteHandler(&stubFavStore{}, testLogger())
	req := httptest.NewRequest(http.MethodGet, "/api/favorites", nil)
	req = withClaims(req, 1, "alice")
	w := httptest.NewRecorder()
	h.ListFavorites(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("want 200, got %d", w.Code)
	}
	var resp map[string]any
	_ = json.NewDecoder(w.Body).Decode(&resp)
	if _, ok := resp["favorites"]; !ok {
		t.Error("response missing 'favorites' key")
	}
}
```

**Commands:**
```bash
cd /Users/mac17/workspace/ai/im/server
go build ./internal/...
go test ./internal/handler/... -run TestFavorite -v
go test ./internal/store/... -run TestFavoriteStore -v
```

**Commit:**
```bash
cd /Users/mac17/workspace/ai/im
git add server/internal/store/favorite.go server/internal/store/favorite_test.go
git add server/internal/handler/favorite.go server/internal/handler/favorite_test.go
git commit -m "feat: add FavoriteStore + FavoriteHandler (add/remove/list favorites)"
```

---

## Task 3: Wire Routes in Gateway

### 3.1 Modify `server/cmd/gateway/main.go`

After `messageStore`, add:

```go
favoriteStore := store.NewFavoriteStore(pool)
favoriteHandler := handler.NewFavoriteHandler(favoriteStore, log)
```

Add routes:

```go
// Forward route (JWT protected)
mux.Handle("POST /api/messages/forward", jwtMiddleware(http.HandlerFunc(messageHandler.ForwardMessages)))

// Favorite routes (JWT protected)
mux.Handle("POST /api/favorites/{message_id}", jwtMiddleware(http.HandlerFunc(favoriteHandler.AddFavorite)))
mux.Handle("DELETE /api/favorites/{message_id}", jwtMiddleware(http.HandlerFunc(favoriteHandler.RemoveFavorite)))
mux.Handle("GET /api/favorites", jwtMiddleware(http.HandlerFunc(favoriteHandler.ListFavorites)))
```

**Commands:**
```bash
cd /Users/mac17/workspace/ai/im/server
go build ./cmd/gateway/
```

**Commit:**
```bash
cd /Users/mac17/workspace/ai/im
git add server/cmd/gateway/main.go
git commit -m "feat(gateway): wire forward and favorite routes"
```

---

## Task 4: Client Forward Dialog

**Goal:** A modal dialog that lets the user select one or more channels to forward a message to.

### 4.1 Create `client/src/app/core/favorites/favorite.service.ts`

```typescript
import { Injectable, inject, signal } from '@angular/core';
import { HttpClient } from '@angular/common/http';
import { firstValueFrom } from 'rxjs';
import { Message } from '../messages/message.service';

const API_BASE = 'http://localhost:8080/api';

export interface FavoriteWithMessage {
  user_id: number;
  message_id: number;
  created_at: string;
  message: Message;
}

@Injectable({ providedIn: 'root' })
export class FavoriteService {
  private http = inject(HttpClient);

  /** Cached list of favorites — loaded on demand. */
  readonly favorites = signal<FavoriteWithMessage[]>([]);

  async add(messageId: number): Promise<void> {
    await firstValueFrom(
      this.http.post(`${API_BASE}/favorites/${messageId}`, {}),
    );
    await this.load(); // refresh list
  }

  async remove(messageId: number): Promise<void> {
    await firstValueFrom(
      this.http.delete(`${API_BASE}/favorites/${messageId}`),
    );
    this.favorites.update(favs => favs.filter(f => f.message_id !== messageId));
  }

  async load(): Promise<void> {
    const resp = await firstValueFrom(
      this.http.get<{ favorites: FavoriteWithMessage[] }>(`${API_BASE}/favorites`),
    );
    this.favorites.set(resp.favorites ?? []);
  }

  isFavorited(messageId: number): boolean {
    return this.favorites().some(f => f.message_id === messageId);
  }

  async forward(messageId: number, targetChannelIds: number[]): Promise<void> {
    await firstValueFrom(
      this.http.post(`${API_BASE}/messages/forward`, {
        message_id: messageId,
        target_channel_ids: targetChannelIds,
      }),
    );
  }
}
```

### 4.2 Modify `client/src/app/features/chat/chat.component.ts`

Add context menu state and forward dialog state:

```typescript
import { FavoriteService } from '../../core/favorites/favorite.service';
import { ChannelWithPreview } from '../../core/channels/channel.service';

// Inject
private favoriteService = inject(FavoriteService);

// Context menu
contextMenuMessage = signal<Message | null>(null);
contextMenuPos = signal<{ x: number; y: number }>({ x: 0, y: 0 });

// Forward dialog
forwardDialogVisible = signal(false);
forwardSource = signal<Message | null>(null);
selectedForwardChannels = signal<Set<number>>(new Set());
forwarding = signal(false);

showContextMenu(event: MouseEvent, msg: Message): void {
  event.preventDefault();
  this.contextMenuMessage.set(msg);
  this.contextMenuPos.set({ x: event.clientX, y: event.clientY });
}

closeContextMenu(): void {
  this.contextMenuMessage.set(null);
}

async favoriteMessage(msg: Message): Promise<void> {
  this.closeContextMenu();
  try {
    if (this.favoriteService.isFavorited(msg.id)) {
      await this.favoriteService.remove(msg.id);
    } else {
      await this.favoriteService.add(msg.id);
    }
  } catch { /* TODO: toast */ }
}

openForwardDialog(msg: Message): void {
  this.closeContextMenu();
  this.forwardSource.set(msg);
  this.selectedForwardChannels.set(new Set());
  this.forwardDialogVisible.set(true);
}

toggleForwardChannel(channelId: number): void {
  this.selectedForwardChannels.update(set => {
    const copy = new Set(set);
    copy.has(channelId) ? copy.delete(channelId) : copy.add(channelId);
    return copy;
  });
}

async confirmForward(): Promise<void> {
  const msg = this.forwardSource();
  const ids = [...this.selectedForwardChannels()];
  if (!msg || ids.length === 0) return;
  this.forwarding.set(true);
  try {
    await this.favoriteService.forward(msg.id, ids);
    this.forwardDialogVisible.set(false);
  } catch { /* TODO: toast */ } finally {
    this.forwarding.set(false);
  }
}
```

### 4.3 Add context menu and forward dialog to `chat.component.html`

In the message list, add `(contextmenu)` handler to each message bubble:

```html
<div
  class="message-bubble"
  (contextmenu)="showContextMenu($event, msg)"
>
  <!-- existing message content -->
</div>
```

Add context menu overlay at the bottom of the template:

```html
<!-- Context menu -->
@if (contextMenuMessage()) {
  <div
    class="context-menu-overlay"
    (click)="closeContextMenu()"
  ></div>
  <div
    class="context-menu"
    [style.left.px]="contextMenuPos().x"
    [style.top.px]="contextMenuPos().y"
  >
    <button class="ctx-item" (click)="favoriteMessage(contextMenuMessage()!)">
      {{ favoriteService.isFavorited(contextMenuMessage()!.id) ? 'Unfavorite' : 'Favorite' }}
    </button>
    <button class="ctx-item" (click)="openForwardDialog(contextMenuMessage()!)">
      Forward
    </button>
  </div>
}

<!-- Forward dialog -->
@if (forwardDialogVisible()) {
  <div class="dialog-overlay" (click)="forwardDialogVisible.set(false)"></div>
  <div class="forward-dialog" (click)="$event.stopPropagation()">
    <div class="dialog-header">
      <h3>Forward Message</h3>
      <button class="dialog-close" (click)="forwardDialogVisible.set(false)">×</button>
    </div>
    <div class="dialog-body">
      <p class="forward-preview">{{ forwardSource()?.content }}</p>
      <p class="select-label">Select channels to forward to:</p>
      <div class="channel-list">
        @for (ch of channelService.channels(); track ch.id) {
          @if (ch.type === 2) {
            <label class="channel-option">
              <input
                type="checkbox"
                [checked]="selectedForwardChannels().has(ch.id)"
                (change)="toggleForwardChannel(ch.id)"
              />
              <span># {{ ch.name }}</span>
            </label>
          }
        }
      </div>
    </div>
    <div class="dialog-footer">
      <button
        class="btn-primary"
        [disabled]="selectedForwardChannels().size === 0 || forwarding()"
        (click)="confirmForward()"
      >
        {{ forwarding() ? 'Forwarding...' : 'Forward' }}
      </button>
    </div>
  </div>
}
```

**Commit:**
```bash
cd /Users/mac17/workspace/ai/im
git add client/src/app/core/favorites/favorite.service.ts
git add client/src/app/features/chat/
git commit -m "feat(client): add forward dialog and context menu to chat"
```

---

## Task 5: Client Favorites Page

**Goal:** `/favorites` — list all favorited messages, click to jump to the channel.

### 5.1 Create `client/src/app/features/favorites/favorites.component.ts`

```typescript
import { Component, OnInit, inject, signal } from '@angular/core';
import { CommonModule } from '@angular/common';
import { Router } from '@angular/router';
import { FavoriteService, FavoriteWithMessage } from '../../core/favorites/favorite.service';

@Component({
  selector: 'app-favorites',
  standalone: true,
  imports: [CommonModule],
  templateUrl: './favorites.component.html',
  styleUrls: ['./favorites.component.scss'],
})
export class FavoritesComponent implements OnInit {
  private favoriteService = inject(FavoriteService);
  private router = inject(Router);

  favorites = this.favoriteService.favorites;
  loading = signal(false);
  error = signal<string | null>(null);

  async ngOnInit(): Promise<void> {
    this.loading.set(true);
    try {
      await this.favoriteService.load();
    } catch {
      this.error.set('Failed to load favorites.');
    } finally {
      this.loading.set(false);
    }
  }

  jumpToMessage(fav: FavoriteWithMessage): void {
    this.router.navigate(['/channels', fav.message.channel_id], {
      queryParams: { around_seq: fav.message.seq },
    });
  }

  async removeFavorite(fav: FavoriteWithMessage): Promise<void> {
    try {
      await this.favoriteService.remove(fav.message_id);
    } catch { /* TODO: toast */ }
  }
}
```

### 5.2 Create `client/src/app/features/favorites/favorites.component.html`

```html
<div class="favorites-page">
  <div class="page-header">
    <h2>Favorites</h2>
  </div>

  @if (loading()) {
    <div class="loading">Loading favorites...</div>
  }

  @if (error()) {
    <div class="error">{{ error() }}</div>
  }

  @if (!loading()) {
    @if (favorites().length === 0) {
      <div class="empty-state">
        No favorited messages yet.<br />
        Right-click any message in a chat to favorite it.
      </div>
    } @else {
      <div class="favorites-list">
        @for (fav of favorites(); track fav.message_id) {
          <div class="favorite-item">
            <div class="fav-content" (click)="jumpToMessage(fav)">
              <div class="fav-meta">
                <span class="fav-time">{{ fav.message.created_at | date:'medium' }}</span>
                <span class="fav-channel">Channel #{{ fav.message.channel_id }}</span>
              </div>
              <div class="fav-text">{{ fav.message.content }}</div>
            </div>
            <button class="unfav-btn" (click)="removeFavorite(fav)" title="Remove favorite">
              ★
            </button>
          </div>
        }
      </div>
    }
  }
</div>
```

### 5.3 Create `client/src/app/features/favorites/favorites.component.scss`

```scss
.favorites-page {
  padding: 24px;
  max-width: 720px;
  margin: 0 auto;
}

.page-header {
  margin-bottom: 20px;

  h2 {
    font-size: 22px;
    font-weight: 700;
    color: var(--text-primary, #111);
    margin: 0;
  }
}

.loading, .error, .empty-state {
  text-align: center;
  padding: 48px 24px;
  color: var(--text-secondary, #888);
  font-size: 14px;
  line-height: 1.6;
}

.error { color: var(--danger, #e74c3c); }

.favorites-list {
  display: flex;
  flex-direction: column;
  gap: 12px;
}

.favorite-item {
  display: flex;
  align-items: flex-start;
  gap: 12px;
  padding: 14px 16px;
  border: 1px solid var(--border-color, #e0e0e0);
  border-radius: 10px;
  background: var(--card-bg, #fff);
  transition: box-shadow 0.15s;

  &:hover {
    box-shadow: 0 2px 8px rgba(0, 0, 0, 0.08);
  }

  .fav-content {
    flex: 1;
    cursor: pointer;
    min-width: 0;

    .fav-meta {
      display: flex;
      gap: 12px;
      margin-bottom: 6px;
      font-size: 12px;
      color: var(--text-secondary, #888);

      .fav-channel {
        color: var(--accent, #5865f2);
        font-weight: 600;
      }
    }

    .fav-text {
      font-size: 14px;
      color: var(--text-primary, #111);
      white-space: pre-wrap;
      word-break: break-word;
    }
  }

  .unfav-btn {
    flex-shrink: 0;
    background: none;
    border: none;
    font-size: 18px;
    color: var(--accent, #5865f2);
    cursor: pointer;
    padding: 4px;
    transition: transform 0.15s, color 0.15s;

    &:hover {
      color: var(--danger, #e74c3c);
      transform: scale(1.2);
    }
  }
}
```

### 5.4 Add route to `client/src/app/app.routes.ts`

```typescript
{
  path: 'favorites',
  loadComponent: () =>
    import('./features/favorites/favorites.component').then(m => m.FavoritesComponent),
},
```

**Commit:**
```bash
cd /Users/mac17/workspace/ai/im
git add client/src/app/features/favorites/ client/src/app/app.routes.ts
git commit -m "feat(client): add favorites page with jump-to-message"
```

---

## Task 6: Context Menu Styles

### 6.1 Add to `client/src/app/features/chat/chat.component.scss`

```scss
// Context menu
.context-menu-overlay {
  position: fixed;
  inset: 0;
  z-index: 100;
}

.context-menu {
  position: fixed;
  z-index: 101;
  background: var(--card-bg, #fff);
  border: 1px solid var(--border-color, #ddd);
  border-radius: 8px;
  box-shadow: 0 4px 16px rgba(0, 0, 0, 0.12);
  padding: 6px 0;
  min-width: 160px;

  .ctx-item {
    display: block;
    width: 100%;
    padding: 8px 16px;
    background: none;
    border: none;
    text-align: left;
    font-size: 14px;
    color: var(--text-primary, #111);
    cursor: pointer;
    transition: background 0.1s;

    &:hover {
      background: var(--hover-bg, #f0f0f0);
    }
  }
}

// Forward dialog
.dialog-overlay {
  position: fixed;
  inset: 0;
  background: rgba(0, 0, 0, 0.4);
  z-index: 200;
}

.forward-dialog {
  position: fixed;
  z-index: 201;
  top: 50%;
  left: 50%;
  transform: translate(-50%, -50%);
  width: 400px;
  max-width: 90vw;
  max-height: 80vh;
  background: var(--card-bg, #fff);
  border-radius: 12px;
  box-shadow: 0 8px 32px rgba(0, 0, 0, 0.2);
  display: flex;
  flex-direction: column;
  overflow: hidden;

  .dialog-header {
    display: flex;
    align-items: center;
    justify-content: space-between;
    padding: 16px 20px;
    border-bottom: 1px solid var(--border-color, #e0e0e0);

    h3 {
      margin: 0;
      font-size: 16px;
      font-weight: 700;
    }

    .dialog-close {
      background: none;
      border: none;
      font-size: 20px;
      cursor: pointer;
      color: var(--text-secondary, #888);
    }
  }

  .dialog-body {
    flex: 1;
    overflow-y: auto;
    padding: 16px 20px;

    .forward-preview {
      background: var(--input-bg, #f5f5f5);
      border-radius: 6px;
      padding: 10px 12px;
      font-size: 13px;
      color: var(--text-primary, #111);
      margin-bottom: 16px;
      word-break: break-word;
    }

    .select-label {
      font-size: 12px;
      font-weight: 600;
      color: var(--text-secondary, #888);
      margin-bottom: 8px;
    }

    .channel-list {
      display: flex;
      flex-direction: column;
      gap: 6px;

      .channel-option {
        display: flex;
        align-items: center;
        gap: 8px;
        padding: 8px 10px;
        border-radius: 6px;
        cursor: pointer;
        transition: background 0.1s;
        font-size: 14px;

        &:hover {
          background: var(--hover-bg, #f0f0f0);
        }
      }
    }
  }

  .dialog-footer {
    padding: 12px 20px;
    border-top: 1px solid var(--border-color, #e0e0e0);
    display: flex;
    justify-content: flex-end;

    .btn-primary {
      padding: 8px 20px;
      background: var(--accent, #5865f2);
      color: #fff;
      border: none;
      border-radius: 6px;
      font-size: 14px;
      font-weight: 600;
      cursor: pointer;
      transition: opacity 0.15s;

      &:disabled {
        opacity: 0.5;
        cursor: not-allowed;
      }

      &:hover:not(:disabled) {
        opacity: 0.9;
      }
    }
  }
}
```

**Commit:**
```bash
cd /Users/mac17/workspace/ai/im
git add client/src/app/features/chat/chat.component.scss
git commit -m "feat(client): add context menu and forward dialog styles"
```

---

## Task 7: Integration Verification

- [ ] Server builds: `cd server && go build ./...`
- [ ] Store tests pass: `go test ./internal/store/... -run TestFavoriteStore -v`
- [ ] Handler tests pass: `go test ./internal/handler/... -run "TestFavorite|TestForward" -v`
- [ ] Client builds: `cd client && npm run build`
- [ ] Right-click a message — context menu appears with "Favorite" and "Forward" options
- [ ] Click "Favorite" — star appears, re-clicking removes it
- [ ] Navigate to `/favorites` — favorited message appears; click to jump to channel
- [ ] Click "Forward" — dialog opens with group channels listed
- [ ] Select channel(s), click Forward — new message appears in target channel with `forwarded_from` set
- [ ] Forwarding to 0 channels shows button disabled
- [ ] Unauthenticated calls return 401

**Commit:**
```bash
cd /Users/mac17/workspace/ai/im
git add .
git commit -m "feat: Plan 11 forward & favorites — server + client integration complete"
```

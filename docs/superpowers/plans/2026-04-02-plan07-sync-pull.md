# Plan 7: 同步 + 拉取路径 Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** 实现批量同步 API、多设备已读同步、客户端空洞检测和完整的重连同步流程

**Architecture:** 批量 sync 端点替代逐个 channel 同步，多设备已读通过 WebSocket 推送同步。客户端使用 count 检测法发现空洞并按需补齐。

**Tech Stack:** Go net/http, pgx, WebSocket, Angular

---

## 目录结构（Plan 7 新增/修改文件）

```
server/
├── internal/
│   ├── handler/
│   │   ├── sync.go              # NEW: SyncHandler (POST /api/sync)
│   │   └── sync_test.go         # NEW: unit tests
│   └── gateway/
│       └── types.go             # MODIFY: add ReadSyncPayload + TypeReadSync constant
├── cmd/
│   └── gateway/main.go          # MODIFY: wire POST /api/sync route; publish read_sync after MarkRead

client/src/app/
├── core/
│   ├── ws/
│   │   └── websocket.models.ts  # MODIFY: add ReadSyncPayload interface + 'read_sync' type
│   │   └── websocket.service.ts # MODIFY: emit readSync$ subject; trigger batch sync on connect
│   ├── messages/
│   │   └── message.service.ts   # MODIFY: replace syncAllChannels with batchSync; add hole detection
│   └── db/
│       └── schema.ts            # MODIFY: add local_sync_ranges helper view (optional) — or skip
└── features/
    └── chat/
        └── chat.component.ts    # MODIFY: call detectAndFillHole on scroll-up
```

---

## Task 1: Batch Sync Handler (POST /api/sync)

**Files to create:**
- `server/internal/handler/sync.go`
- `server/internal/handler/sync_test.go`

### Overview

`SyncHandler` accepts a list of `(channel_id, local_max_seq)` pairs from the client and returns:
- Channels where `server_seq > client_seq` (with incremental messages if gap ≤ 100, `has_more` flag if gap > 100)
- Channels the client does not know about (joined while offline) with latest 50 messages
- Unread count for every changed channel

The handler keeps sync logic as an HTTP endpoint on the Gateway — no separate Sync Service binary needed at this scale.

### 1.1 Store interface additions needed

Before writing the handler, check what `ChannelStore` already provides:

- `GetMemberChannelSeqs(ctx, userID)` → `map[int64]int64` — already exists in `store/channel.go`
- `ListByUserWithPreview(ctx, userID)` → `[]ChannelWithPreview` — already exists
- `GetMember(ctx, channelID, userID)` — already exists

New store method needed on `MessageStore`:

```go
// FetchAfterForUser fetches messages in (afterSeq, ∞) for channelID, applying
// phantom visibility for userID. Returns at most `limit` messages in ascending seq order.
// This is the same as FetchForUser but uses a lower bound on seq.
// NOTE: FetchForUser already does this — it is the right method.
// Use: store.FetchForUser(ctx, channelID, userID, afterSeq, limit)
```

No new store methods needed — `MessageStore.FetchForUser` already fetches `seq > afterSeq` up to `limit`.

### 1.2 Create `server/internal/handler/sync.go`

```go
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
```

### 1.3 Create `server/internal/handler/sync_test.go`

```go
package handler

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"im-server/internal/auth"
	"im-server/internal/model"
)

// ---------- stub stores ----------

type stubSyncChannelStore struct {
	seqs   map[int64]int64
	member *model.ChannelMember
	getErr error
}

func (s *stubSyncChannelStore) GetMemberChannelSeqs(_ context.Context, _ int64) (map[int64]int64, error) {
	return s.seqs, s.getErr
}

func (s *stubSyncChannelStore) GetMember(_ context.Context, _, _ int64) (*model.ChannelMember, error) {
	if s.member != nil {
		return s.member, nil
	}
	return &model.ChannelMember{}, nil
}

func (s *stubSyncChannelStore) GetByID(_ context.Context, _ int64) (*model.Channel, error) {
	return &model.Channel{}, nil
}

type stubSyncMsgStore struct {
	messages []model.Message
}

func (s *stubSyncMsgStore) FetchForUser(_ context.Context, _, _ int64, afterSeq int64, limit int) ([]model.Message, error) {
	var result []model.Message
	for _, m := range s.messages {
		if m.Seq > afterSeq && len(result) < limit {
			result = append(result, m)
		}
	}
	return result, nil
}

// ---------- helpers ----------

func makeSyncRequest(t *testing.T, userID int64, jwtSecret string, reqBody SyncRequest) *http.Request {
	t.Helper()
	token, err := auth.GenerateToken(jwtSecret, userID)
	if err != nil {
		t.Fatalf("generate token: %v", err)
	}
	body, _ := json.Marshal(reqBody)
	r := httptest.NewRequest(http.MethodPost, "/api/sync", bytes.NewReader(body))
	r.Header.Set("Authorization", "Bearer "+token)
	return r
}

// ---------- tests ----------

func TestSyncHandler_NoChanges(t *testing.T) {
	chStore := &stubSyncChannelStore{
		seqs: map[int64]int64{1: 100, 2: 200},
	}
	msgStore := &stubSyncMsgStore{}
	h := NewSyncHandler(chStore, msgStore, testLogger())

	// Client is up-to-date on both channels.
	req := makeSyncRequest(t, 42, testSecret, SyncRequest{
		Channels: []SyncChannelEntry{{ID: 1, Seq: 100}, {ID: 2, Seq: 200}},
	})
	req = req.WithContext(ctxWithClaims(req.Context(), 42))
	w := httptest.NewRecorder()
	h.Sync(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", w.Code)
	}
	var resp SyncResponse
	json.NewDecoder(w.Body).Decode(&resp)
	if len(resp.Channels) != 0 {
		t.Errorf("expected 0 channel results, got %d", len(resp.Channels))
	}
}

func TestSyncHandler_SmallGap(t *testing.T) {
	chStore := &stubSyncChannelStore{
		seqs:   map[int64]int64{1: 105},
		member: &model.ChannelMember{LastReadSeq: 100, PhantomCount: 0, PhantomAtRead: 0},
	}
	// Pretend messages 101–105 exist.
	var msgs []model.Message
	for i := int64(101); i <= 105; i++ {
		msgs = append(msgs, model.Message{ChannelID: 1, Seq: i, Content: "msg"})
	}
	msgStore := &stubSyncMsgStore{messages: msgs}
	h := NewSyncHandler(chStore, msgStore, testLogger())

	req := makeSyncRequest(t, 42, testSecret, SyncRequest{
		Channels: []SyncChannelEntry{{ID: 1, Seq: 100}},
	})
	req = req.WithContext(ctxWithClaims(req.Context(), 42))
	w := httptest.NewRecorder()
	h.Sync(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", w.Code)
	}
	var resp SyncResponse
	json.NewDecoder(w.Body).Decode(&resp)
	if len(resp.Channels) != 1 {
		t.Fatalf("expected 1 channel result, got %d", len(resp.Channels))
	}
	ch := resp.Channels[0]
	if ch.HasMore {
		t.Error("small gap should not set has_more")
	}
	if len(ch.Messages) != 5 {
		t.Errorf("expected 5 messages, got %d", len(ch.Messages))
	}
	if ch.Unread != 5 {
		t.Errorf("expected unread=5, got %d", ch.Unread)
	}
}

func TestSyncHandler_LargeGap(t *testing.T) {
	chStore := &stubSyncChannelStore{
		seqs: map[int64]int64{1: 300},
	}
	// Build 300 messages (seq 1–300).
	var msgs []model.Message
	for i := int64(1); i <= 300; i++ {
		msgs = append(msgs, model.Message{ChannelID: 1, Seq: i, Content: "msg"})
	}
	msgStore := &stubSyncMsgStore{messages: msgs}
	h := NewSyncHandler(chStore, msgStore, testLogger())

	// Client is at seq 0 (never synced this channel). Gap = 300 > syncGapThreshold=100.
	req := makeSyncRequest(t, 42, testSecret, SyncRequest{
		Channels: []SyncChannelEntry{{ID: 1, Seq: 0}},
	})
	req = req.WithContext(ctxWithClaims(req.Context(), 42))
	w := httptest.NewRecorder()
	h.Sync(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", w.Code)
	}
	var resp SyncResponse
	json.NewDecoder(w.Body).Decode(&resp)
	if len(resp.Channels) != 1 {
		t.Fatalf("expected 1 result, got %d", len(resp.Channels))
	}
	ch := resp.Channels[0]
	if !ch.HasMore {
		t.Error("large gap should set has_more=true")
	}
	if len(ch.Messages) > syncMsgLimit {
		t.Errorf("should return at most %d messages, got %d", syncMsgLimit, len(ch.Messages))
	}
}

func TestSyncHandler_NewChannel(t *testing.T) {
	// Server knows about channel 99 (joined while client was offline).
	chStore := &stubSyncChannelStore{
		seqs: map[int64]int64{99: 10},
	}
	var msgs []model.Message
	for i := int64(1); i <= 10; i++ {
		msgs = append(msgs, model.Message{ChannelID: 99, Seq: i, Content: "welcome"})
	}
	msgStore := &stubSyncMsgStore{messages: msgs}
	h := NewSyncHandler(chStore, msgStore, testLogger())

	// Client sends empty channel list (doesn't know about channel 99).
	req := makeSyncRequest(t, 42, testSecret, SyncRequest{Channels: []SyncChannelEntry{}})
	req = req.WithContext(ctxWithClaims(req.Context(), 42))
	w := httptest.NewRecorder()
	h.Sync(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", w.Code)
	}
	var resp SyncResponse
	json.NewDecoder(w.Body).Decode(&resp)
	if len(resp.Channels) != 1 {
		t.Fatalf("expected 1 new-channel result, got %d", len(resp.Channels))
	}
	ch := resp.Channels[0]
	if ch.ID != 99 {
		t.Errorf("expected channel 99, got %d", ch.ID)
	}
	if len(ch.Messages) == 0 {
		t.Error("expected messages for new channel")
	}
}

func TestSyncHandler_Unauthorized(t *testing.T) {
	h := NewSyncHandler(&stubSyncChannelStore{}, &stubSyncMsgStore{}, testLogger())
	r := httptest.NewRequest(http.MethodPost, "/api/sync", bytes.NewReader([]byte("{}")))
	// No claims in context — simulates missing JWT.
	w := httptest.NewRecorder()
	h.Sync(w, r)
	if w.Code != http.StatusUnauthorized {
		t.Errorf("expected 401, got %d", w.Code)
	}
}
```

> **Note on test helpers:** The test file reuses `testLogger()`, `testSecret`, and `ctxWithClaims()` which are already defined in `server/internal/handler/auth_test.go` (or whichever file first declares them in the `handler` package test files). If they live in a `_test.go` file, they are accessible across the package's test files automatically. Verify with `grep -rn "func testLogger" server/internal/handler/`.

### Commands

```bash
cd /Users/mac17/workspace/ai/im/server
go build ./internal/handler/...
go test ./internal/handler/... -run TestSync -v
```

### Commit

```bash
cd /Users/mac17/workspace/ai/im
git add server/internal/handler/sync.go server/internal/handler/sync_test.go
git commit -m "feat(server): add POST /api/sync batch sync handler"
```

---

## Task 2: Wire Sync Route in Gateway

**Files to modify:**
- `server/cmd/gateway/main.go`

### 2.1 Add SyncHandler wiring

In `run()`, after `messageHandler` is constructed, add:

```go
syncHandler := handler.NewSyncHandler(channelStore, messageStore, log)
```

Then in the mux registration block, add after the message routes:

```go
// Sync route (JWT protected)
mux.Handle("POST /api/sync", jwtMiddleware(http.HandlerFunc(syncHandler.Sync)))
```

The `store.ChannelStore` and `store.MessageStore` already satisfy `SyncChannelStore` and `SyncMsgStore` because `GetMemberChannelSeqs`, `GetMember`, `GetByID`, and `FetchForUser` all exist. No adapter needed.

### Commands

```bash
cd /Users/mac17/workspace/ai/im/server
go build ./cmd/gateway/...
```

### Commit

```bash
cd /Users/mac17/workspace/ai/im
git add server/cmd/gateway/main.go
git commit -m "feat(server): wire POST /api/sync route in gateway"
```

---

## Task 3: Multi-Device Read Sync — Server Side

**Files to modify:**
- `server/internal/gateway/types.go` — add `ReadSyncPayload` and `TypeReadSync`
- `server/internal/handler/message.go` — publish read_sync event after `MarkRead` succeeds
- `server/cmd/gateway/main.go` — pass Hub to `MessageHandler`

### Overview

When device A marks a channel as read:
1. `MarkRead` handler updates DB (already done in Plan 5).
2. Handler pushes a `read_sync` frame to all other devices of the same user via the Hub.
3. No Pulsar needed — within the same Gateway pod the Hub handles it directly.
4. Cross-pod reads sync is left to the heartbeat pong (pong includes channel seq, client can infer read state on channel open).

**Why no Pulsar here?** Read sync is best-effort (spec §九: "已读同步不需要 ACK"). Using the Hub directly avoids Pulsar publishing overhead for a low-stakes event. For multi-pod read sync, the heartbeat pong diff provides eventual consistency.

### 3.1 Modify `server/internal/gateway/types.go`

Add after the existing `TypeSyncResp` constant:

```go
// TypeReadSync is pushed server→client when the same user marks read on another device.
TypeReadSync WSMessageType = "read_sync"
```

Add the payload type at the bottom of the file:

```go
// ReadSyncPayload is pushed to the user's other devices when they mark a channel read.
type ReadSyncPayload struct {
	ChannelID int64 `json:"channel_id"`
	ReadSeq   int64 `json:"read_seq"` // the seq that was just marked as read
}
```

### 3.2 Define a Hub interface in handler package

To keep `MessageHandler` testable without importing `gateway`, define a minimal interface:

In `server/internal/handler/message.go`, add after the existing interface declarations:

```go
// ReadSyncPusher pushes read_sync events to other devices of the same user.
// Implemented by *gateway.Hub (via an adapter in main.go).
type ReadSyncPusher interface {
	PushReadSync(userID int64, channelID int64, readSeq int64)
}
```

Modify `MessageHandler` struct to include the pusher:

```go
type MessageHandler struct {
	messages   MsgStore
	channels   MsgChannelStore
	readSyncer ReadSyncPusher // nil = no cross-device read sync (e.g. in tests)
	log        *slog.Logger
}

func NewMessageHandler(messages MsgStore, channels MsgChannelStore, log *slog.Logger) *MessageHandler {
	return &MessageHandler{messages: messages, channels: channels, log: log}
}

// WithReadSyncer sets the cross-device read sync pusher. Call after construction.
func (h *MessageHandler) WithReadSyncer(rs ReadSyncPusher) *MessageHandler {
	h.readSyncer = rs
	return h
}
```

Modify `MarkRead` to push the sync event after DB update succeeds:

```go
// In MarkRead, after h.channels.MarkRead succeeds:
if h.readSyncer != nil {
    h.readSyncer.PushReadSync(claims.UserID, channelID, ch.Seq)
}
```

### 3.3 Create Hub adapter in `server/cmd/gateway/main.go`

The Hub's `PushToUser` method takes a `WSMessageType` and `any`, but `ReadSyncPusher` has a domain-specific signature. Create a small adapter inline in main.go (no new file needed):

```go
// hubReadSyncer adapts *gateway.Hub to handler.ReadSyncPusher.
type hubReadSyncer struct {
	hub *gateway.Hub
}

func (s *hubReadSyncer) PushReadSync(userID, channelID, readSeq int64) {
	s.hub.PushToUser(userID, gateway.TypeReadSync, gateway.ReadSyncPayload{
		ChannelID: channelID,
		ReadSeq:   readSeq,
	})
}
```

Wire it:

```go
// After messageHandler is created:
messageHandler.WithReadSyncer(&hubReadSyncer{hub: hub})
```

### Commands

```bash
cd /Users/mac17/workspace/ai/im/server
go build ./...
go test ./internal/handler/... -v
```

### Commit

```bash
cd /Users/mac17/workspace/ai/im
git add server/internal/gateway/types.go \
        server/internal/handler/message.go \
        server/cmd/gateway/main.go
git commit -m "feat(server): push read_sync to other devices after mark-read"
```

---

## Task 4: Client Read Sync Handler

**Files to modify:**
- `client/src/app/core/ws/websocket.models.ts` — add `ReadSyncPayload` + `'read_sync'` to union type
- `client/src/app/core/ws/websocket.service.ts` — emit `readSync$` subject on `read_sync` frame
- `client/src/app/core/channels/channel.service.ts` — subscribe to `readSync$`, update local unread count

### 4.1 Modify `client/src/app/core/ws/websocket.models.ts`

Change the `WSMessageType` union to include `'read_sync'`:

```typescript
export type WSMessageType =
  | 'ping' | 'pong'
  | 'push_msg' | 'push_ack'
  | 'send' | 'send_ack'
  | 'sync' | 'sync_resp'
  | 'read_sync';               // NEW
```

Add the payload interface at the end of the file:

```typescript
export interface ReadSyncPayload {
  channel_id: number;
  read_seq: number;
}
```

### 4.2 Modify `client/src/app/core/ws/websocket.service.ts`

Add import:

```typescript
import {
  WSFrame, WSMessageType,
  PingPayload, PongPayload,
  PushMsgPayload, PushACKPayload,
  SendACKPayload,
  ReadSyncPayload,      // NEW
} from './websocket.models';
```

Add subject field in the class:

```typescript
/** Emits when another device of the same user marks a channel as read. */
readonly readSync$ = new Subject<ReadSyncPayload>();
```

Add a case in `onMessage`'s switch statement, after the `'send_ack'` case:

```typescript
case 'read_sync': {
  this.readSync$.next(frame.payload as ReadSyncPayload);
  break;
}
```

### 4.3 Modify `client/src/app/core/channels/channel.service.ts`

Read `ChannelService` first to understand its structure, then add subscription to `readSync$`.

In the constructor (or `ngOnInit` if applicable), after existing WS subscriptions:

```typescript
// When another device marks a channel as read, update our local unread count.
this.ws.readSync$.subscribe(event => this.handleReadSync(event));
```

Add the handler method:

```typescript
private handleReadSync(event: ReadSyncPayload): void {
  // Update the channel's unread count to 0 (the other device read everything
  // up to event.read_seq). We set unread=0 as a best-effort approximation;
  // any messages arriving after read_seq will increment it again via push_msg.
  this.channels.update(channels =>
    channels.map(ch =>
      ch.id === event.channel_id
        ? { ...ch, unread_count: 0 }
        : ch
    )
  );
}
```

> **Dependency check:** `ChannelService` must inject `WebSocketService`. Read the file first — if it doesn't already inject it, add `private ws = inject(WebSocketService);` and the relevant import.

### Commands

```bash
cd /Users/mac17/workspace/ai/im/client
ng build --configuration development 2>&1 | head -40
```

### Commit

```bash
cd /Users/mac17/workspace/ai/im
git add client/src/app/core/ws/websocket.models.ts \
        client/src/app/core/ws/websocket.service.ts \
        client/src/app/core/channels/channel.service.ts
git commit -m "feat(client): handle read_sync WS frame to update unread count on other devices"
```

---

## Task 5: Client Batch Sync on Reconnect

**Files to modify:**
- `client/src/app/core/ws/websocket.models.ts` — add `SyncResponse` / `SyncChannelResult` interfaces
- `client/src/app/core/messages/message.service.ts` — replace `syncAllChannels` with `batchSync`

### Overview

Currently `syncAllChannels()` loops over channels and issues one HTTP `GET /api/channels/{id}/messages` per channel. Replace this with a single `POST /api/sync` call that sends all channel states at once.

### 5.1 Add sync response types to `websocket.models.ts`

The existing `SyncPayload` / `SyncChannelState` cover the request shape. Add response types:

```typescript
export interface SyncChannelResult {
  id: number;
  server_seq: number;
  unread: number;
  messages?: Message[];   // import Message from message.service or define inline
  has_more: boolean;
}

export interface SyncResponse {
  channels: SyncChannelResult[];
}
```

> **Note:** `Message` is defined in `message.service.ts`. To avoid a circular import, define a minimal `LocalMessage` type in `websocket.models.ts`, or import from a shared types file. Simplest approach: define the interface inline in `websocket.models.ts` mirroring `message.service.ts`'s `Message`.

Add to `websocket.models.ts`:

```typescript
// Mirror of message.service.ts Message — kept here to avoid circular deps.
export interface SyncMessage {
  id: number;
  channel_id: number;
  seq: number;
  client_msg_id?: string;
  sender_id: number;
  msg_type: number;
  content: string;
  visible_to?: number[];
  created_at: string;
}

export interface SyncChannelResult {
  id: number;
  server_seq: number;
  unread: number;
  messages?: SyncMessage[];
  has_more: boolean;
}

export interface SyncResponse {
  channels: SyncChannelResult[];
}
```

### 5.2 Modify `client/src/app/core/messages/message.service.ts`

Add imports at top:

```typescript
import { SyncChannelState, SyncResponse } from '../ws/websocket.models';
```

Replace the `syncAllChannels` private method with `batchSync`:

```typescript
/**
 * On reconnect: POST /api/sync with all known channel states.
 * Server returns incremental messages for channels with small gaps,
 * has_more flag for large gaps, and any new channels joined while offline.
 *
 * This replaces the old per-channel fetchAndAppendMissed loop.
 */
private async batchSync(): Promise<void> {
  // Build the channel state list from our WS seq tracker.
  const channels: SyncChannelState[] = Object.entries(this.ws.channelSeqs).map(
    ([idStr, seq]) => ({ id: Number(idStr), seq })
  );

  try {
    const resp = await firstValueFrom(
      this.http.post<SyncResponse>(`${API_BASE}/sync`, { channels })
    );

    for (const result of resp.channels ?? []) {
      // Update the WS seq tracker so heartbeat pong diffs stay accurate.
      this.ws.updateChannelSeq(result.id, result.server_seq);

      // If this is the active channel and we got messages, merge them in.
      if (result.messages && result.messages.length > 0) {
        if (this.activeChannelId() === result.id) {
          const existingSeqs = new Set(this.messages().map(m => m.seq));
          const newMsgs = result.messages
            .filter(m => !existingSeqs.has(m.seq))
            .map(m => m as unknown as Message); // SyncMessage is structurally compatible
          if (newMsgs.length > 0) {
            this.messages.update(existing =>
              [...existing, ...newMsgs].sort((a, b) => a.seq - b.seq)
            );
          }
        }
      }

      // Update channel unread counts via ChannelService.
      // Channels with has_more=true need a full re-fetch when opened.
      this.channelService.updateUnread(result.id, result.unread);
    }
  } catch (err) {
    console.warn('[MessageService] batchSync failed, falling back to individual pulls', err);
    // Fallback: old behavior for resilience.
    await this.syncAllChannelsFallback();
  }
}

/** Legacy per-channel sync; used as fallback when POST /api/sync fails. */
private async syncAllChannelsFallback(): Promise<void> {
  const channels = this.channelService.channels();
  for (const ch of channels) {
    const localSeq = this.ws.channelSeqs[String(ch.id)] ?? -1;
    if (localSeq >= 0 && ch.seq > localSeq) {
      await this.fetchAndAppendMissed(ch.id, localSeq);
    }
  }
}
```

Change the constructor subscription to call `batchSync`:

```typescript
// Replace:
).subscribe(() => this.syncAllChannels());
// With:
).subscribe(() => this.batchSync());
```

### 5.3 Add `updateUnread` to ChannelService

Read `channel.service.ts` first, then add:

```typescript
/** Update the unread count for a single channel (called after batch sync). */
updateUnread(channelId: number, unread: number): void {
  this.channels.update(channels =>
    channels.map(ch =>
      ch.id === channelId ? { ...ch, unread_count: unread } : ch
    )
  );
}
```

### Commands

```bash
cd /Users/mac17/workspace/ai/im/client
ng build --configuration development 2>&1 | head -50
```

### Commit

```bash
cd /Users/mac17/workspace/ai/im
git add client/src/app/core/ws/websocket.models.ts \
        client/src/app/core/messages/message.service.ts \
        client/src/app/core/channels/channel.service.ts
git commit -m "feat(client): replace per-channel sync with batch POST /api/sync on reconnect"
```

---

## Task 6: Client Hole Detection (Count-Based)

**Files to modify:**
- `client/src/app/core/messages/message.service.ts` — add `detectAndFillHole` method
- `client/src/app/features/chat/chat.component.ts` — call `detectAndFillHole` on scroll-up

### Overview

When a user scrolls up to load older messages, the client checks whether the local SQLite store has a continuous sequence. If it detects a gap, it fetches from the server to fill it.

**Algorithm (spec §八):**
1. Query `local_messages` for N messages going upward from `pivotSeq`.
2. If fewer than N messages are returned and we haven't reached the channel start → gap exists.
3. Additionally, check seq continuity: if consecutive `seq` values jump by more than 1 (ignoring phantom slots), a gap is present at the break point.
4. Trigger `GET /api/channels/{id}/messages?before_seq={gapSeq}&limit=50` to fill.

**Important:** The `local_messages` table is the SQLite store managed by `DatabaseService`. The current `MessageService` only loads from HTTP and keeps messages in an in-memory signal. Plan 7 introduces the SQLite-backed scroll-up path.

### 6.1 Add `detectAndFillHole` to `MessageService`

Add to `message.service.ts`:

```typescript
/**
 * Called when the user scrolls up past the oldest message currently displayed.
 * Uses count-based hole detection against local SQLite, then fetches from server if needed.
 *
 * @param channelId  The channel being viewed.
 * @param pivotSeq   The seq of the oldest currently-displayed message.
 * @param pageSize   How many older messages to load (default 30).
 * @returns          The older messages to prepend (empty if already at the beginning).
 */
async detectAndFillHole(channelId: number, pivotSeq: number, pageSize = 30): Promise<Message[]> {
  // 1. Check local SQLite for messages older than pivotSeq.
  const localMsgs = await this.db.query<{seq: number; content: string; sender_id: string; msg_type: number; created_at: number}>(
    `SELECT seq, content, sender_id, msg_type, created_at
     FROM local_messages
     WHERE channel_id = $1 AND seq < $2
     ORDER BY seq DESC
     LIMIT $3`,
    [String(channelId), pivotSeq, pageSize]
  );

  // Reverse to ascending order.
  const localSeqs = localMsgs.map(r => r.seq).reverse();

  // 2. Check continuity: are there gaps in the local sequence?
  const hasGap = this.hasSequenceGap(localSeqs, pivotSeq);

  // 3. If we have enough continuous messages locally, return them directly.
  if (!hasGap && localMsgs.length >= pageSize) {
    return localMsgs.reverse().map(r => this.rowToMessage(channelId, r));
  }

  // 4. Gap detected (or not enough messages): fetch from server.
  // Fetch before pivotSeq to get older messages.
  try {
    const serverMsgs = await this.fetchMessages(channelId, {
      before_seq: pivotSeq,
      limit: pageSize,
    });

    // Store fetched messages in local SQLite for future scroll-up.
    await this.storeMessagesLocally(channelId, serverMsgs);

    // Return in ascending order (oldest first).
    return [...serverMsgs].reverse();
  } catch (err) {
    console.warn('[MessageService] hole fill fetch failed', err);
    // Return whatever we have locally as a best-effort fallback.
    return localMsgs.reverse().map(r => this.rowToMessage(channelId, r));
  }
}

/**
 * Returns true if there is a gap in `seqs` (ascending) before `pivotSeq`.
 * A gap exists when consecutive seqs are not adjacent (seq[i+1] !== seq[i] + 1)
 * OR when the count is less than what we requested.
 *
 * Note: phantom messages are stored locally with visible=0 and still occupy a seq
 * slot, so seq continuity is intact — no special phantom handling needed.
 */
private hasSequenceGap(ascSeqs: number[], pivotSeq: number): boolean {
  if (ascSeqs.length === 0) return true;

  // Check continuity between consecutive elements.
  for (let i = 1; i < ascSeqs.length; i++) {
    if (ascSeqs[i] !== ascSeqs[i - 1] + 1) {
      return true; // non-contiguous seq detected
    }
  }

  // Check that the last local seq connects to pivotSeq without a gap.
  const highestLocal = ascSeqs[ascSeqs.length - 1];
  if (pivotSeq - highestLocal > 1) {
    return true; // gap between local data and pivot
  }

  return false;
}

/** Convert a SQLite row from local_messages to the in-memory Message type. */
private rowToMessage(channelId: number, row: {
  seq: number; content: string; sender_id: string; msg_type: number; created_at: number;
}): Message {
  return {
    id: 0,               // not stored in local_messages
    channel_id: channelId,
    seq: row.seq,
    sender_id: Number(row.sender_id),
    msg_type: row.msg_type,
    content: row.content,
    created_at: new Date(row.created_at).toISOString(),
  };
}

/** Persist server-fetched messages into local SQLite so future scroll-up is fast. */
private async storeMessagesLocally(channelId: number, msgs: Message[]): Promise<void> {
  for (const m of msgs) {
    const visible = m.msg_type === 2 ? 0 : 1; // phantom → invisible
    await this.db.execute(
      `INSERT OR IGNORE INTO local_messages
         (channel_id, seq, server_id, client_id, sender_id, msg_type, content, visible, created_at)
       VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9)`,
      [
        String(channelId),
        m.seq,
        String(m.id),
        m.client_msg_id ?? '',
        String(m.sender_id),
        m.msg_type,
        m.content,
        visible,
        new Date(m.created_at).getTime(),
      ]
    );
  }
}
```

Add `DatabaseService` injection to `MessageService` (if not already present):

```typescript
private db = inject(DatabaseService);
```

And add the import:

```typescript
import { DatabaseService } from '../db/database.service';
```

### 6.2 Modify `chat.component.ts` — trigger hole detection on scroll-up

Read `chat.component.ts` first to understand its current scroll handling. Then add:

```typescript
/** The seq of the oldest message currently displayed. */
get oldestSeq(): number {
  const msgs = this.messageService.messages();
  return msgs.length > 0 ? msgs[0].seq : 0;
}

/**
 * Called when the message list scroll container reaches the top.
 * Triggers hole detection and prepends older messages.
 */
async onScrolledToTop(): Promise<void> {
  const channelId = this.messageService.activeChannelId();
  if (!channelId || this.isLoadingOlder) return;

  const pivot = this.oldestSeq;
  if (pivot <= 1) return; // already at the beginning

  this.isLoadingOlder = true;
  try {
    const older = await this.messageService.detectAndFillHole(channelId, pivot);
    if (older.length > 0) {
      // Prepend to the message list, preserving scroll position.
      this.messageService.messages.update(current => {
        const existingSeqs = new Set(current.map(m => m.seq));
        const newOnes = older.filter(m => !existingSeqs.has(m.seq));
        return [...newOnes, ...current];
      });
    }
  } finally {
    this.isLoadingOlder = false;
  }
}
```

Add `isLoadingOlder = false;` as a class field.

Wire the scroll event in `chat.component.html` — add a scroll listener on the message container:

```html
<!-- On the scrollable message list container: -->
<div class="message-list" #messageList (scroll)="onScroll($event)">
  ...
</div>
```

Add `onScroll` in the component:

```typescript
onScroll(event: Event): void {
  const el = event.target as HTMLElement;
  if (el.scrollTop === 0) {
    this.onScrolledToTop();
  }
}
```

### Commands

```bash
cd /Users/mac17/workspace/ai/im/client
ng build --configuration development 2>&1 | head -50
```

### Commit

```bash
cd /Users/mac17/workspace/ai/im
git add client/src/app/core/messages/message.service.ts \
        client/src/app/features/chat/chat.component.ts \
        client/src/app/features/chat/chat.component.html
git commit -m "feat(client): add count-based hole detection and fill on scroll-up"
```

---

## Task 7: Integration Verification

**Goal:** Confirm all Plan 7 features work together end-to-end.

### 7.1 Server tests

```bash
cd /Users/mac17/workspace/ai/im/server

# Run all handler tests (includes sync tests from Task 1).
go test ./internal/handler/... -v -count=1

# Run all gateway tests (hub, routing, heartbeat).
go test ./internal/gateway/... -v -count=1

# Full build to catch any compilation errors.
go build ./...
```

Expected: all tests pass, no build errors.

### 7.2 Verify sync endpoint manually (with running stack)

Precondition: gateway and postgres running locally.

```bash
# 1. Login to get a token.
TOKEN=$(curl -s -X POST http://localhost:8080/api/auth/login \
  -H 'Content-Type: application/json' \
  -d '{"username":"testuser","password":"testpass"}' | jq -r '.token')

# 2. POST /api/sync with an empty channel list (simulates fresh client).
curl -s -X POST http://localhost:8080/api/sync \
  -H "Authorization: Bearer $TOKEN" \
  -H 'Content-Type: application/json' \
  -d '{"channels":[]}' | jq .

# Expected: { "channels": [...] } with all channels the user belongs to.
# Each entry should have server_seq, unread, and messages (last 50 for new channels).
```

### 7.3 Verify read sync across devices (manual)

1. Open the app in two browser tabs (different localStorage / deviceIDs).
2. In tab A: open a channel with unread messages. Verify unread badge.
3. In tab B: mark the channel as read.
4. Observe tab A: unread badge should drop to 0 within the WebSocket frame delivery time (~0ms on localhost).

### 7.4 Verify hole detection (manual)

1. Open a channel with many messages (>30).
2. Scroll to the very top of the message list.
3. Observe: `onScrolledToTop` fires, `detectAndFillHole` is called.
4. Open browser DevTools → Network: confirm a `GET /api/channels/{id}/messages?before_seq=...` request fires.
5. Older messages appear above the current scroll position.

### 7.5 Verify batch sync on reconnect

1. Disconnect the app (network tab → offline in DevTools).
2. Send messages to the user from another session (via curl or a second client).
3. Reconnect (network tab → online).
4. Observe: `POST /api/sync` fires in the Network tab.
5. The messages received offline appear in the channel within seconds of reconnect.

### Commit

```bash
cd /Users/mac17/workspace/ai/im
git add -p  # stage any verification-related fixes
git commit -m "test(plan7): integration verification pass — sync, read-sync, hole detection"
```

---

## Summary of Files Changed / Created

| File | Status | Purpose |
|------|--------|---------|
| `server/internal/handler/sync.go` | NEW | POST /api/sync handler |
| `server/internal/handler/sync_test.go` | NEW | Unit tests for SyncHandler |
| `server/internal/gateway/types.go` | MODIFY | Add `TypeReadSync`, `ReadSyncPayload` |
| `server/internal/handler/message.go` | MODIFY | Add `ReadSyncPusher` interface; push after MarkRead |
| `server/cmd/gateway/main.go` | MODIFY | Wire SyncHandler route; wire hubReadSyncer |
| `client/src/app/core/ws/websocket.models.ts` | MODIFY | Add `ReadSyncPayload`, `SyncMessage`, `SyncChannelResult`, `SyncResponse` |
| `client/src/app/core/ws/websocket.service.ts` | MODIFY | Add `readSync$` subject; dispatch `read_sync` frames |
| `client/src/app/core/channels/channel.service.ts` | MODIFY | Handle `readSync$`; add `updateUnread()` |
| `client/src/app/core/messages/message.service.ts` | MODIFY | Replace `syncAllChannels` with `batchSync`; add `detectAndFillHole`, `storeMessagesLocally` |
| `client/src/app/features/chat/chat.component.ts` | MODIFY | Add `onScrolledToTop`, `onScroll`, `isLoadingOlder` |
| `client/src/app/features/chat/chat.component.html` | MODIFY | Wire `(scroll)` event on message container |

## Key Design Decisions

1. **No separate Sync Service binary** — sync logic runs as HTTP endpoints on the Gateway (`cmd/gateway/main.go`). Avoids operational overhead at medium scale. If sync becomes a bottleneck, extract later.

2. **Gap threshold = 100** — gaps ≤ 100 messages return all missed messages inline; larger gaps return `has_more=true` with the latest 50 messages. Client must re-fetch history from the top when entering a `has_more` channel.

3. **Read sync via Hub, not Pulsar** — read events are best-effort (spec §九 explicitly says no ACK needed). Using the Hub's in-process broadcast is simpler and faster. Cross-pod read sync is eventual via heartbeat pong.

4. **Hole detection uses both count and seq continuity** — count-only check misses gaps in the middle of the local range; seq continuity check catches them. Phantoms (visible=0) still occupy seq slots so they don't create false positives.

5. **SQLite-backed scroll history** — `storeMessagesLocally` persists server-fetched messages so the next scroll-up is served from SQLite without a network round-trip.

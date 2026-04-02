# Plan 5: 消息写入路径 Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** 实现消息发送、拉取、已读标记的 HTTP API，为后续 WebSocket 推送和 Pulsar 管道做好准备

**Architecture:** HTTP 端点直接调用 MessageStore.Send 完成消息持久化和 seq 分配。消息拉取支持 after/before/around 三种模式，自动做 phantom 过滤。Pulsar 基础设施搭建但暂不在热路径中使用。

**Tech Stack:** Go net/http, pgx, Apache Pulsar client

---

## 目录结构（Plan 5 新增/修改文件）

```
server/
├── internal/
│   ├── handler/
│   │   ├── message.go           # NEW: MessageHandler (send/fetch/read)
│   │   └── message_test.go      # NEW: unit tests with stub stores
│   └── pulsar/
│       └── client.go            # NEW: PulsarClient wrapper (producer + consumer)
├── cmd/
│   ├── gateway/main.go          # MODIFY: wire message routes
│   └── message/main.go          # MODIFY: Pulsar consumer loop (MessageService)

client/src/app/
├── core/
│   └── messages/
│       └── message.service.ts   # NEW: MessageService (HTTP API + signal state)
└── features/
    └── chat/
        ├── chat.component.ts    # NEW: chat window (message list + send box)
        ├── chat.component.html
        └── chat.component.scss
```

---

## Task 1: MessageHandler — HTTP handlers for send/fetch/read

**Files to create:**
- `server/internal/handler/message.go`
- `server/internal/handler/message_test.go`

### Overview

`MessageHandler` exposes three endpoints:

| Method | Path | Purpose |
|--------|------|---------|
| `POST` | `/api/channels/{id}/messages` | Send a message; calls `MessageStore.Send` directly |
| `GET`  | `/api/channels/{id}/messages` | Fetch messages (after/before/around modes) |
| `POST` | `/api/channels/{id}/read` | Mark channel as read (update `last_read_seq`) |

The handler uses two store interfaces to stay testable without a real database.

### 1.1 Create `server/internal/handler/message.go`

```go
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

type sendMessageResponse struct {
	*model.Message
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
// Exactly one of after_seq, before_seq, around_seq must be provided.
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
		msgs, err = h.messages.FetchForUser(r.Context(), channelID, claims.UserID, afterSeq, limit)
	case q.Get("before_seq") != "":
		beforeSeq := parseIntParam(q.Get("before_seq"), 0)
		msgs, err = h.messages.FetchBefore(r.Context(), channelID, claims.UserID, beforeSeq, limit)
	case q.Get("around_seq") != "":
		aroundSeq := parseIntParam(q.Get("around_seq"), 0)
		msgs, err = h.messages.FetchAround(r.Context(), channelID, claims.UserID, aroundSeq, limit)
	default:
		// Default: fetch the latest `limit` messages (before seq=MaxInt64)
		msgs, err = h.messages.FetchBefore(r.Context(), channelID, claims.UserID, 1<<62, limit)
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
```

**Note:** `claimsFromCtx`, `writeJSON`, `writeError`, `pathID`, and `ErrNotFound` are all already defined in the `handler` package (in `auth.go` and `channel.go`). Do not redeclare them.

### 1.2 Create `server/internal/handler/message_test.go`

Use in-memory stub stores — same pattern as `channel_test.go`. The test file must declare `package handler_test`.

```go
package handler_test

import (
	"context"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"
	"time"

	"im-server/internal/handler"
	"im-server/internal/model"
)

// ---------- stub MsgStore ----------

type stubMsgStore struct {
	messages []model.Message
	nextSeq  int64
}

func newStubMsgStore() *stubMsgStore { return &stubMsgStore{nextSeq: 1} }

func (s *stubMsgStore) Send(_ context.Context, msg *model.Message) error {
	// Idempotent: return existing if client_msg_id matches
	if msg.ClientMsgID != "" {
		for _, m := range s.messages {
			if m.ChannelID == msg.ChannelID && m.ClientMsgID == msg.ClientMsgID {
				msg.ID = m.ID
				msg.Seq = m.Seq
				return nil
			}
		}
	}
	msg.ID = s.nextSeq
	msg.Seq = s.nextSeq
	msg.CreatedAt = time.Now()
	s.nextSeq++
	s.messages = append(s.messages, *msg)
	return nil
}

func (s *stubMsgStore) FetchForUser(_ context.Context, channelID, userID int64, afterSeq int64, limit int) ([]model.Message, error) {
	var result []model.Message
	for _, m := range s.messages {
		if m.ChannelID == channelID && m.Seq > afterSeq {
			if m.IsVisibleTo(userID) {
				result = append(result, m)
			} else {
				result = append(result, model.Message{ChannelID: m.ChannelID, Seq: m.Seq, MsgType: model.MsgTypePhantom})
			}
			if len(result) >= limit {
				break
			}
		}
	}
	return result, nil
}

func (s *stubMsgStore) FetchBefore(_ context.Context, channelID, userID int64, beforeSeq int64, limit int) ([]model.Message, error) {
	var result []model.Message
	for i := len(s.messages) - 1; i >= 0; i-- {
		m := s.messages[i]
		if m.ChannelID == channelID && m.Seq < beforeSeq {
			if m.IsVisibleTo(userID) {
				result = append(result, m)
			} else {
				result = append(result, model.Message{ChannelID: m.ChannelID, Seq: m.Seq, MsgType: model.MsgTypePhantom})
			}
			if len(result) >= limit {
				break
			}
		}
	}
	return result, nil
}

func (s *stubMsgStore) FetchAround(_ context.Context, channelID, userID int64, aroundSeq int64, limit int) ([]model.Message, error) {
	return s.FetchForUser(nil, channelID, userID, 0, limit)
}

// ---------- stub MsgChannelStore ----------

type stubMsgChannelStore struct {
	members  map[int64]map[int64]*model.ChannelMember // channelID -> userID -> member
	channels map[int64]*model.Channel
}

func newStubMsgChannelStore() *stubMsgChannelStore {
	return &stubMsgChannelStore{
		members:  make(map[int64]map[int64]*model.ChannelMember),
		channels: make(map[int64]*model.Channel),
	}
}

func (s *stubMsgChannelStore) addMember(channelID, userID int64) {
	if s.members[channelID] == nil {
		s.members[channelID] = make(map[int64]*model.ChannelMember)
	}
	s.members[channelID][userID] = &model.ChannelMember{ChannelID: channelID, UserID: userID}
}

func (s *stubMsgChannelStore) addChannel(ch *model.Channel) {
	s.channels[ch.ID] = ch
}

func (s *stubMsgChannelStore) GetMember(_ context.Context, channelID, userID int64) (*model.ChannelMember, error) {
	if cm := s.members[channelID][userID]; cm != nil {
		return cm, nil
	}
	return nil, handler.ErrNotFound
}

func (s *stubMsgChannelStore) MarkRead(_ context.Context, channelID, userID, seq int64) error {
	if cm := s.members[channelID][userID]; cm != nil {
		cm.LastReadSeq = seq
	}
	return nil
}

func (s *stubMsgChannelStore) GetByID(_ context.Context, id int64) (*model.Channel, error) {
	if ch, ok := s.channels[id]; ok {
		return ch, nil
	}
	return nil, handler.ErrNotFound
}

// ---------- helpers ----------

func newMessageHandler(t *testing.T) (*handler.MessageHandler, *stubMsgStore, *stubMsgChannelStore) {
	t.Helper()
	ms := newStubMsgStore()
	cs := newStubMsgChannelStore()
	log := slog.New(slog.NewTextHandler(os.Stderr, nil))
	h := handler.NewMessageHandler(ms, cs, log)
	return h, ms, cs
}

// ---------- tests ----------

func TestMessageHandler_SendMessage_Success(t *testing.T) {
	h, _, cs := newMessageHandler(t)
	cs.addMember(1, 42)

	req := requestWithClaims("POST", "/api/channels/1/messages", 42, map[string]any{
		"content":      "hello",
		"client_msg_id": "uuid-001",
	})
	req.SetPathValue("id", "1")
	w := httptest.NewRecorder()

	h.SendMessage(w, req)

	if w.Code != http.StatusCreated {
		t.Fatalf("expected 201, got %d: %s", w.Code, w.Body.String())
	}
}

func TestMessageHandler_SendMessage_NotMember(t *testing.T) {
	h, _, _ := newMessageHandler(t) // no members added

	req := requestWithClaims("POST", "/api/channels/1/messages", 42, map[string]any{
		"content": "hello",
	})
	req.SetPathValue("id", "1")
	w := httptest.NewRecorder()

	h.SendMessage(w, req)

	if w.Code != http.StatusForbidden {
		t.Fatalf("expected 403, got %d", w.Code)
	}
}

func TestMessageHandler_SendMessage_EmptyContent(t *testing.T) {
	h, _, cs := newMessageHandler(t)
	cs.addMember(1, 42)

	req := requestWithClaims("POST", "/api/channels/1/messages", 42, map[string]any{
		"content": "",
	})
	req.SetPathValue("id", "1")
	w := httptest.NewRecorder()

	h.SendMessage(w, req)

	if w.Code != http.StatusUnprocessableEntity {
		t.Fatalf("expected 422, got %d", w.Code)
	}
}

func TestMessageHandler_SendMessage_Idempotent(t *testing.T) {
	h, ms, cs := newMessageHandler(t)
	cs.addMember(1, 42)

	body := map[string]any{"content": "hello", "client_msg_id": "uuid-dup"}

	req1 := requestWithClaims("POST", "/api/channels/1/messages", 42, body)
	req1.SetPathValue("id", "1")
	w1 := httptest.NewRecorder()
	h.SendMessage(w1, req1)

	req2 := requestWithClaims("POST", "/api/channels/1/messages", 42, body)
	req2.SetPathValue("id", "1")
	w2 := httptest.NewRecorder()
	h.SendMessage(w2, req2)

	if len(ms.messages) != 1 {
		t.Errorf("expected 1 message (idempotent), got %d", len(ms.messages))
	}
	if w1.Code != http.StatusCreated || w2.Code != http.StatusCreated {
		t.Errorf("both sends should be 201")
	}
}

func TestMessageHandler_FetchMessages_AfterSeq(t *testing.T) {
	h, ms, cs := newMessageHandler(t)
	cs.addMember(1, 42)
	// Pre-populate store with two messages
	ms.Send(context.Background(), &model.Message{ChannelID: 1, SenderID: 42, Content: "a", MsgType: model.MsgTypeText})
	ms.Send(context.Background(), &model.Message{ChannelID: 1, SenderID: 42, Content: "b", MsgType: model.MsgTypeText})

	req := requestWithClaims("GET", "/api/channels/1/messages?after_seq=0", 42, nil)
	req.SetPathValue("id", "1")
	w := httptest.NewRecorder()

	h.FetchMessages(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}
}

func TestMessageHandler_FetchMessages_NotMember(t *testing.T) {
	h, _, _ := newMessageHandler(t)

	req := requestWithClaims("GET", "/api/channels/1/messages?after_seq=0", 99, nil)
	req.SetPathValue("id", "1")
	w := httptest.NewRecorder()

	h.FetchMessages(w, req)

	if w.Code != http.StatusForbidden {
		t.Fatalf("expected 403, got %d", w.Code)
	}
}

func TestMessageHandler_MarkRead_Success(t *testing.T) {
	h, _, cs := newMessageHandler(t)
	cs.addMember(10, 7)
	cs.addChannel(&model.Channel{ID: 10, Seq: 5})

	req := requestWithClaims("POST", "/api/channels/10/read", 7, nil)
	req.SetPathValue("id", "10")
	w := httptest.NewRecorder()

	h.MarkRead(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}
	if cs.members[10][7].LastReadSeq != 5 {
		t.Errorf("LastReadSeq = %d, want 5", cs.members[10][7].LastReadSeq)
	}
}
```

**Note on test helper reuse:** `requestWithClaims` is already defined in `auth_test.go` within `package handler_test`. It can be reused directly since all `*_test.go` files in the same package share helpers.

### Commands

```bash
cd /Users/mac17/workspace/ai/im/server
go vet ./internal/handler/...
go test ./internal/handler/... -run TestMessage -v
```

### Commit

```bash
cd /Users/mac17/workspace/ai/im
git add server/internal/handler/message.go server/internal/handler/message_test.go
git commit -m "feat(server): add MessageHandler with send/fetch/read endpoints"
```

---

## Task 2: Wire message routes in Gateway

**File to modify:** `server/cmd/gateway/main.go`

### Overview

Add `MessageStore`, instantiate `MessageHandler`, and register three new routes in the existing `mux`. No other changes are needed.

### 2.1 Changes to `server/cmd/gateway/main.go`

**Add to imports:**
```go
// (no new imports needed — all dependencies are already in scope)
```

**After the existing `channelStore`/`channelHandler` block, add:**

```go
messageStore := store.NewMessageStore(pool)
messageHandler := handler.NewMessageHandler(messageStore, channelStore, log)
```

**Register routes (after the channel routes block):**

```go
// Message routes (JWT protected)
mux.Handle("POST /api/channels/{id}/messages", jwtMiddleware(http.HandlerFunc(messageHandler.SendMessage)))
mux.Handle("GET /api/channels/{id}/messages",  jwtMiddleware(http.HandlerFunc(messageHandler.FetchMessages)))
mux.Handle("POST /api/channels/{id}/read",     jwtMiddleware(http.HandlerFunc(messageHandler.MarkRead)))
```

**Note:** `store.NewMessageStore` takes a `*pgxpool.Pool` and is already implemented. The second argument to `NewMessageHandler` is `MsgChannelStore` — `*store.ChannelStore` satisfies this interface because it already has `GetMember`, `MarkRead`, and `GetByID`.

### Commands

```bash
cd /Users/mac17/workspace/ai/im/server
go build ./cmd/gateway/...
```

### Commit

```bash
cd /Users/mac17/workspace/ai/im
git add server/cmd/gateway/main.go
git commit -m "feat(gateway): register message send/fetch/read routes"
```

---

## Task 3: Pulsar client wrapper

**File to create:** `server/internal/pulsar/client.go`

### Overview

A thin wrapper around the Apache Pulsar Go client that provides:
- `Producer` — wraps a Pulsar producer, sends JSON-encoded payloads to a topic
- `Consumer` — wraps a Pulsar consumer, delivers messages to a handler callback

This is infrastructure only. The gateway will use `Producer` to publish to `msg.incoming` in Plan 6. MessageService will use `Consumer` to read from `msg.incoming`.

### 3.1 Add Pulsar Go client dependency

```bash
cd /Users/mac17/workspace/ai/im/server
go get github.com/apache/pulsar-client-go/pulsar@latest
```

This adds `github.com/apache/pulsar-client-go` to `go.mod`.

### 3.2 Create `server/internal/pulsar/client.go`

```go
package pulsar

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"

	"github.com/apache/pulsar-client-go/pulsar"
)

// ---------- Client ----------

// Client owns the Pulsar connection. Create one per process and close on shutdown.
type Client struct {
	inner pulsar.Client
	log   *slog.Logger
}

// New creates a connected Pulsar client.
// url example: "pulsar://localhost:6650"
func New(url string, log *slog.Logger) (*Client, error) {
	c, err := pulsar.NewClient(pulsar.ClientOptions{URL: url})
	if err != nil {
		return nil, fmt.Errorf("pulsar new client: %w", err)
	}
	return &Client{inner: c, log: log}, nil
}

// Close releases the underlying connection.
func (c *Client) Close() {
	c.inner.Close()
}

// ---------- Producer ----------

// Producer sends JSON-encoded messages to a single Pulsar topic.
type Producer struct {
	inner pulsar.Producer
	log   *slog.Logger
}

// NewProducer creates a producer bound to the given topic.
// partitionKey, if non-empty, is applied to every message (ensures ordering per key).
func (c *Client) NewProducer(topic string) (*Producer, error) {
	p, err := c.inner.CreateProducer(pulsar.ProducerOptions{
		Topic: topic,
	})
	if err != nil {
		return nil, fmt.Errorf("create producer for %s: %w", topic, err)
	}
	return &Producer{inner: p, log: c.log}, nil
}

// Send JSON-encodes payload and publishes it to the topic.
// key is used as the partition routing key (e.g. channel_id as string ensures
// per-channel ordering).
func (p *Producer) Send(ctx context.Context, key string, payload any) error {
	data, err := json.Marshal(payload)
	if err != nil {
		return fmt.Errorf("marshal payload: %w", err)
	}
	msg := &pulsar.ProducerMessage{
		Payload: data,
	}
	if key != "" {
		msg.Key = key
	}
	_, err = p.inner.Send(ctx, msg)
	return err
}

// Close releases the producer.
func (p *Producer) Close() {
	p.inner.Close()
}

// ---------- Consumer ----------

// HandlerFunc is the callback invoked for each incoming Pulsar message.
// If it returns nil, the message is ACKed. If it returns an error, the message
// is NACKed and will be redelivered.
type HandlerFunc func(ctx context.Context, data []byte) error

// Consumer reads messages from a Pulsar topic and dispatches them to a handler.
type Consumer struct {
	inner   pulsar.Consumer
	handler HandlerFunc
	log     *slog.Logger
}

// NewConsumer creates a consumer subscribed to the given topic.
// subscriptionName should be stable across restarts for at-least-once delivery.
func (c *Client) NewConsumer(topic, subscriptionName string, handler HandlerFunc) (*Consumer, error) {
	consumer, err := c.inner.Subscribe(pulsar.ConsumerOptions{
		Topic:            topic,
		SubscriptionName: subscriptionName,
		Type:             pulsar.Shared,
	})
	if err != nil {
		return nil, fmt.Errorf("subscribe to %s: %w", topic, err)
	}
	return &Consumer{inner: consumer, handler: handler, log: c.log}, nil
}

// Consume starts a blocking consume loop. It stops when ctx is cancelled.
// Each message is dispatched to the handler; ACK on success, NACk on error.
func (cs *Consumer) Consume(ctx context.Context) error {
	for {
		msg, err := cs.inner.Receive(ctx)
		if err != nil {
			if ctx.Err() != nil {
				return nil // clean shutdown
			}
			return fmt.Errorf("receive: %w", err)
		}
		if err := cs.handler(ctx, msg.Payload()); err != nil {
			cs.log.Warn("message handler error, nacking", "error", err)
			cs.inner.Nack(msg)
			continue
		}
		cs.inner.Ack(msg)
	}
}

// Close releases the consumer.
func (cs *Consumer) Close() {
	cs.inner.Close()
}
```

### Commands

```bash
cd /Users/mac17/workspace/ai/im/server
go build ./internal/pulsar/...
```

### Commit

```bash
cd /Users/mac17/workspace/ai/im
git add server/internal/pulsar/client.go server/go.mod server/go.sum
git commit -m "feat(server): add Pulsar client wrapper (producer + consumer)"
```

---

## Task 4: MessageService Pulsar consumer loop

**File to modify:** `server/cmd/message/main.go`

### Overview

The MessageService binary:
1. Connects to PG and Pulsar
2. Consumes from `msg.incoming` topic
3. For each message: JSON-decode → call `MessageStore.Send` → publish ACK event to `msg.deliver.{gateway_id}` topic

For Plan 5, the delivery publish step is a stub (`TODO: Plan 6`) because Gateway's WebSocket push isn't implemented yet. The consumer loop itself must be fully functional.

### Topic conventions

| Topic | Direction | Key |
|-------|-----------|-----|
| `msg.incoming` | Gateway → MessageService | `channel_id` (string) |
| `msg.deliver.{gateway_id}` | MessageService → Gateway | `user_id` (string) |

### Payload types

```go
// IncomingMessage is the wire format published by Gateway to msg.incoming.
// Fields mirror model.Message plus a gateway_id for the ACK return path.
type IncomingMessage struct {
	GatewayID   string  `json:"gateway_id"`
	ChannelID   int64   `json:"channel_id"`
	SenderID    int64   `json:"sender_id"`
	ClientMsgID string  `json:"client_msg_id"`
	MsgType     int16   `json:"msg_type"`
	Content     string  `json:"content"`
	VisibleTo   []int64 `json:"visible_to,omitempty"`
	ReplyTo     *int64  `json:"reply_to,omitempty"`
}

// DeliveryEvent is published to msg.deliver.{gateway_id} after a message is persisted.
// The Gateway uses this to push the ACK back to the sender's WebSocket (Plan 6).
type DeliveryEvent struct {
	ClientMsgID string `json:"client_msg_id"`
	ServerMsgID int64  `json:"server_msg_id"`
	ChannelID   int64  `json:"channel_id"`
	Seq         int64  `json:"seq"`
	SenderID    int64  `json:"sender_id"`
}
```

### 4.1 Replace `server/cmd/message/main.go`

```go
package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"syscall"
	"time"

	"im-server/internal/config"
	"im-server/internal/model"
	imPulsar "im-server/internal/pulsar"
	"im-server/internal/store"
)

func main() {
	fmt.Println("message service starting...")
	os.Exit(run())
}

// ---------- wire types ----------

type incomingMessage struct {
	GatewayID   string  `json:"gateway_id"`
	ChannelID   int64   `json:"channel_id"`
	SenderID    int64   `json:"sender_id"`
	ClientMsgID string  `json:"client_msg_id"`
	MsgType     int16   `json:"msg_type"`
	Content     string  `json:"content"`
	VisibleTo   []int64 `json:"visible_to,omitempty"`
	ReplyTo     *int64  `json:"reply_to,omitempty"`
}

type deliveryEvent struct {
	ClientMsgID string `json:"client_msg_id"`
	ServerMsgID int64  `json:"server_msg_id"`
	ChannelID   int64  `json:"channel_id"`
	Seq         int64  `json:"seq"`
	SenderID    int64  `json:"sender_id"`
}

// ---------- service ----------

type messageService struct {
	store    *store.MessageStore
	producer *imPulsar.Producer // publishes to msg.deliver.{gateway_id}
	log      *slog.Logger
}

func (svc *messageService) handle(ctx context.Context, data []byte) error {
	var in incomingMessage
	if err := json.Unmarshal(data, &in); err != nil {
		return fmt.Errorf("unmarshal incoming: %w", err)
	}

	msgType := model.MsgType(in.MsgType)
	if msgType == 0 {
		msgType = model.MsgTypeText
	}

	msg := &model.Message{
		ChannelID:   in.ChannelID,
		SenderID:    in.SenderID,
		ClientMsgID: in.ClientMsgID,
		MsgType:     msgType,
		Content:     in.Content,
		VisibleTo:   in.VisibleTo,
		ReplyTo:     in.ReplyTo,
	}

	if err := svc.store.Send(ctx, msg); err != nil {
		return fmt.Errorf("store.Send: %w", err)
	}

	svc.log.Info("message persisted",
		"channel_id", msg.ChannelID,
		"seq", msg.Seq,
		"client_msg_id", msg.ClientMsgID,
	)

	// Publish delivery event so Gateway can ACK the sender (Plan 6 will consume this).
	if in.GatewayID != "" && svc.producer != nil {
		event := deliveryEvent{
			ClientMsgID: msg.ClientMsgID,
			ServerMsgID: msg.ID,
			ChannelID:   msg.ChannelID,
			Seq:         msg.Seq,
			SenderID:    msg.SenderID,
		}
		topic := "msg.deliver." + in.GatewayID
		key := fmt.Sprintf("%d", msg.SenderID)
		if err := svc.producer.Send(ctx, key, event); err != nil {
			// Non-fatal: log and continue. The sender will get their ACK via
			// the HTTP response (Plan 5) or pong heartbeat (Plan 6).
			svc.log.Warn("publish delivery event failed", "topic", topic, "error", err)
		}
	}

	return nil
}

// ---------- run ----------

func run() int {
	log := slog.New(slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelInfo}))

	cfgPath := os.Getenv("IM_CONFIG")
	if cfgPath == "" {
		cfgPath = "config.yaml"
	}

	cfg, err := config.Load(cfgPath)
	if err != nil {
		log.Error("load config", "error", err)
		return 1
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	pool, err := store.NewPGPool(ctx, cfg.PG.DSN, cfg.PG.MaxConns)
	cancel()
	if err != nil {
		log.Error("connect to postgres", "error", err)
		return 1
	}
	defer pool.Close()

	msgStore := store.NewMessageStore(pool)

	// Connect to Pulsar
	pulsarClient, err := imPulsar.New(cfg.Pulsar.URL, log)
	if err != nil {
		log.Error("connect to pulsar", "error", err)
		return 1
	}
	defer pulsarClient.Close()

	// Producer for delivery ACK events (best-effort, Plan 6 consumes)
	deliverProducer, err := pulsarClient.NewProducer("msg.deliver.ack")
	if err != nil {
		log.Warn("could not create delivery producer (non-fatal)", "error", err)
		deliverProducer = nil
	} else {
		defer deliverProducer.Close()
	}

	svc := &messageService{
		store:    msgStore,
		producer: deliverProducer,
		log:      log,
	}

	consumer, err := pulsarClient.NewConsumer("msg.incoming", "message-service", svc.handle)
	if err != nil {
		log.Error("create consumer", "error", err)
		return 1
	}
	defer consumer.Close()

	// Graceful shutdown
	runCtx, runCancel := context.WithCancel(context.Background())
	quit := make(chan os.Signal, 1)
	signal.Notify(quit, syscall.SIGINT, syscall.SIGTERM)

	go func() {
		<-quit
		log.Info("shutting down...")
		runCancel()
	}()

	log.Info("consuming from msg.incoming")
	if err := consumer.Consume(runCtx); err != nil {
		log.Error("consumer error", "error", err)
		return 1
	}

	return 0
}
```

### Commands

```bash
cd /Users/mac17/workspace/ai/im/server
go build ./cmd/message/...
```

### Commit

```bash
cd /Users/mac17/workspace/ai/im
git add server/cmd/message/main.go
git commit -m "feat(message-service): implement Pulsar consumer loop for message persistence"
```

---

## Task 5: Client MessageService (Angular service for message API)

**File to create:** `client/src/app/core/messages/message.service.ts`

### Overview

`MessageService` provides:
- `sendMessage(channelId, content, clientMsgId?, msgType?, visibleTo?, replyTo?)` → POST request
- `fetchMessages(channelId, opts)` → GET with after/before/around params
- `markRead(channelId)` → POST /read
- `messages` signal — reactive store of messages for the currently open channel
- `loadMessages(channelId)` — load latest 50 messages and set the signal

```typescript
import { Injectable, signal } from '@angular/core';
import { HttpClient } from '@angular/common/http';
import { firstValueFrom } from 'rxjs';

// ---------- types ----------

export interface Message {
  id: number;
  channel_id: number;
  seq: number;
  client_msg_id?: string;
  sender_id: number;
  msg_type: number;   // 1=text, 2=image, 3=file, 4=system, 99=phantom
  content: string;
  visible_to?: number[];
  reply_to?: number;
  forwarded_from?: number;
  created_at: string;
}

export interface SendMessagePayload {
  content: string;
  client_msg_id?: string;
  msg_type?: number;
  visible_to?: number[];
  reply_to?: number;
}

export interface FetchOptions {
  after_seq?: number;
  before_seq?: number;
  around_seq?: number;
  limit?: number;
}

export interface FetchMessagesResponse {
  messages: Message[];
}

const API_BASE = 'http://localhost:8080/api';

@Injectable({ providedIn: 'root' })
export class MessageService {
  /** Messages for the currently-open channel, newest last. */
  readonly messages = signal<Message[]>([]);

  /** The channel ID whose messages are currently loaded. */
  readonly activeChannelId = signal<number | null>(null);

  constructor(private http: HttpClient) {}

  // ---------- API calls ----------

  async sendMessage(channelId: number, payload: SendMessagePayload): Promise<Message> {
    return firstValueFrom(
      this.http.post<Message>(`${API_BASE}/channels/${channelId}/messages`, payload)
    );
  }

  async fetchMessages(channelId: number, opts: FetchOptions = {}): Promise<Message[]> {
    const params: Record<string, string> = {};
    if (opts.after_seq !== undefined) params['after_seq'] = String(opts.after_seq);
    if (opts.before_seq !== undefined) params['before_seq'] = String(opts.before_seq);
    if (opts.around_seq !== undefined) params['around_seq'] = String(opts.around_seq);
    if (opts.limit !== undefined) params['limit'] = String(opts.limit);

    const resp = await firstValueFrom(
      this.http.get<FetchMessagesResponse>(`${API_BASE}/channels/${channelId}/messages`, {
        params,
      })
    );
    return resp.messages ?? [];
  }

  async markRead(channelId: number): Promise<void> {
    await firstValueFrom(
      this.http.post(`${API_BASE}/channels/${channelId}/read`, {})
    );
  }

  // ---------- state management ----------

  /** Load the latest 50 messages for a channel and update the messages signal. */
  async loadMessages(channelId: number): Promise<void> {
    const msgs = await this.fetchMessages(channelId, { limit: 50 });
    // FetchBefore (default) returns newest-first; reverse for display order.
    this.messages.set([...msgs].reverse());
    this.activeChannelId.set(channelId);
  }

  /** Append a locally-sent message optimistically (before ACK). */
  appendOptimistic(msg: Message): void {
    this.messages.update(msgs => [...msgs, msg]);
  }

  /** Replace an optimistic message (matched by client_msg_id) with the ACK'd version. */
  confirmSent(clientMsgId: string, confirmed: Message): void {
    this.messages.update(msgs =>
      msgs.map(m => (m.client_msg_id === clientMsgId ? confirmed : m))
    );
  }

  /** Clear messages when navigating away from a channel. */
  clear(): void {
    this.messages.set([]);
    this.activeChannelId.set(null);
  }
}
```

### Commit

```bash
cd /Users/mac17/workspace/ai/im
git add client/src/app/core/messages/message.service.ts
git commit -m "feat(client): add MessageService with send/fetch/read/optimistic API"
```

---

## Task 6: Client chat window component

**Files to create:**
- `client/src/app/features/chat/chat.component.ts`
- `client/src/app/features/chat/chat.component.html`
- `client/src/app/features/chat/chat.component.scss`

**Files to modify:**
- `client/src/app/app.routes.ts` — add `channels/:id` route
- `client/src/app/features/channel-list/channel-list.component.ts` — fix `openChannel` to navigate to chat

### Overview

A chat window that:
1. Loads the last 50 messages when entering the channel
2. Displays messages (phantom messages are silently filtered from the display)
3. Sends new messages via the input box with optimistic UI
4. Marks the channel as read on open
5. Renders sender name as user ID for now (Plan 6 will add user caching)

### 6.1 Create `client/src/app/features/chat/chat.component.ts`

```typescript
import { Component, inject, OnInit, OnDestroy, signal, ViewChild, ElementRef, AfterViewChecked } from '@angular/core';
import { CommonModule } from '@angular/common';
import { ActivatedRoute } from '@angular/router';
import { FormsModule } from '@angular/forms';
import { MessageService, Message } from '../../core/messages/message.service';
import { AuthService } from '../../core/auth/auth.service';
import { ChannelService } from '../../core/channels/channel.service';

@Component({
  selector: 'app-chat',
  standalone: true,
  imports: [CommonModule, FormsModule],
  templateUrl: './chat.component.html',
  styleUrl: './chat.component.scss',
})
export class ChatComponent implements OnInit, OnDestroy, AfterViewChecked {
  private route = inject(ActivatedRoute);
  private messageService = inject(MessageService);
  private authService = inject(AuthService);
  private channelService = inject(ChannelService);

  @ViewChild('messageList') messageListRef!: ElementRef<HTMLDivElement>;

  readonly messages = this.messageService.messages;
  readonly currentUser = this.authService.currentUser;

  channelId = 0;
  channelName = signal('');
  inputText = '';
  sending = signal(false);
  error = signal('');
  private shouldScroll = false;

  async ngOnInit(): Promise<void> {
    const idParam = this.route.snapshot.paramMap.get('id');
    this.channelId = idParam ? parseInt(idParam, 10) : 0;
    if (!this.channelId) return;

    // Load channel name
    try {
      const ch = await this.channelService.getChannel(this.channelId);
      this.channelName.set(ch.name || 'DM');
    } catch {
      this.channelName.set('Channel');
    }

    // Load messages + mark read
    await this.messageService.loadMessages(this.channelId);
    await this.messageService.markRead(this.channelId).catch(() => {});
    await this.channelService.loadChannels(); // refresh unread counts in sidebar
    this.shouldScroll = true;
  }

  ngOnDestroy(): void {
    this.messageService.clear();
  }

  ngAfterViewChecked(): void {
    if (this.shouldScroll) {
      this.scrollToBottom();
      this.shouldScroll = false;
    }
  }

  /** Returns true if the message is a phantom (should be hidden in UI). */
  isPhantom(msg: Message): boolean {
    return msg.msg_type === 99;
  }

  isMine(msg: Message): boolean {
    return msg.sender_id === this.currentUser()?.id;
  }

  senderLabel(msg: Message): string {
    const me = this.currentUser();
    if (me && msg.sender_id === me.id) return 'You';
    return `User ${msg.sender_id}`;
  }

  formatTime(iso: string): string {
    return new Date(iso).toLocaleTimeString([], { hour: '2-digit', minute: '2-digit' });
  }

  async send(): Promise<void> {
    const content = this.inputText.trim();
    if (!content || this.sending()) return;

    const clientMsgId = crypto.randomUUID();
    this.inputText = '';
    this.sending.set(true);
    this.error.set('');

    // Optimistic append
    const optimistic: Message = {
      id: 0,
      channel_id: this.channelId,
      seq: 0,
      client_msg_id: clientMsgId,
      sender_id: this.currentUser()?.id ?? 0,
      msg_type: 1,
      content,
      created_at: new Date().toISOString(),
    };
    this.messageService.appendOptimistic(optimistic);
    this.shouldScroll = true;

    try {
      const confirmed = await this.messageService.sendMessage(this.channelId, {
        content,
        client_msg_id: clientMsgId,
        msg_type: 1,
      });
      this.messageService.confirmSent(clientMsgId, confirmed);
    } catch {
      this.error.set('Failed to send. Please try again.');
      // Remove the optimistic message on failure
      this.messageService.messages.update(msgs =>
        msgs.filter(m => m.client_msg_id !== clientMsgId)
      );
    } finally {
      this.sending.set(false);
    }
  }

  onKeydown(event: KeyboardEvent): void {
    if (event.key === 'Enter' && !event.shiftKey) {
      event.preventDefault();
      this.send();
    }
  }

  private scrollToBottom(): void {
    const el = this.messageListRef?.nativeElement;
    if (el) el.scrollTop = el.scrollHeight;
  }
}
```

### 6.2 Create `client/src/app/features/chat/chat.component.html`

```html
<div class="chat-container">
  <header class="chat-header">
    <h2>{{ channelName() }}</h2>
  </header>

  <div class="message-list" #messageList>
    @for (msg of messages(); track msg.seq || msg.client_msg_id) {
      @if (!isPhantom(msg)) {
        <div class="message" [class.mine]="isMine(msg)">
          <span class="sender">{{ senderLabel(msg) }}</span>
          <div class="bubble">{{ msg.content }}</div>
          <span class="time">
            @if (msg.created_at) { {{ formatTime(msg.created_at) }} }
            @if (!msg.seq) { <em>sending…</em> }
          </span>
        </div>
      }
    } @empty {
      <p class="empty">No messages yet. Say hello!</p>
    }
  </div>

  @if (error()) {
    <div class="error-bar">{{ error() }}</div>
  }

  <div class="input-area">
    <textarea
      [(ngModel)]="inputText"
      (keydown)="onKeydown($event)"
      placeholder="Message…"
      rows="1"
      [disabled]="sending()"
    ></textarea>
    <button (click)="send()" [disabled]="!inputText.trim() || sending()">
      Send
    </button>
  </div>
</div>
```

### 6.3 Create `client/src/app/features/chat/chat.component.scss`

```scss
.chat-container {
  display: flex;
  flex-direction: column;
  height: 100%;
  overflow: hidden;
  background: #fff;
}

.chat-header {
  padding: 12px 16px;
  border-bottom: 1px solid #e5e7eb;
  flex-shrink: 0;

  h2 {
    margin: 0;
    font-size: 16px;
    font-weight: 600;
    color: #111;
  }
}

.message-list {
  flex: 1;
  overflow-y: auto;
  padding: 16px;
  display: flex;
  flex-direction: column;
  gap: 8px;
}

.message {
  display: flex;
  flex-direction: column;
  max-width: 70%;
  align-self: flex-start;

  &.mine {
    align-self: flex-end;
    align-items: flex-end;
  }
}

.sender {
  font-size: 11px;
  color: #6b7280;
  margin-bottom: 2px;

  .mine & {
    display: none;
  }
}

.bubble {
  padding: 8px 12px;
  border-radius: 16px;
  background: #f3f4f6;
  font-size: 14px;
  line-height: 1.4;
  word-break: break-word;

  .mine & {
    background: #2563eb;
    color: #fff;
  }
}

.time {
  font-size: 10px;
  color: #9ca3af;
  margin-top: 2px;

  em {
    font-style: italic;
    color: #d1d5db;
  }
}

.error-bar {
  background: #fee2e2;
  color: #dc2626;
  padding: 8px 16px;
  font-size: 13px;
  flex-shrink: 0;
}

.input-area {
  display: flex;
  align-items: flex-end;
  gap: 8px;
  padding: 12px 16px;
  border-top: 1px solid #e5e7eb;
  flex-shrink: 0;

  textarea {
    flex: 1;
    resize: none;
    border: 1px solid #d1d5db;
    border-radius: 8px;
    padding: 8px 12px;
    font-size: 14px;
    font-family: inherit;
    outline: none;
    min-height: 36px;
    max-height: 120px;

    &:focus {
      border-color: #2563eb;
    }

    &:disabled {
      opacity: 0.6;
    }
  }

  button {
    padding: 8px 16px;
    background: #2563eb;
    color: #fff;
    border: none;
    border-radius: 8px;
    font-size: 14px;
    cursor: pointer;
    height: 36px;
    flex-shrink: 0;

    &:hover:not(:disabled) {
      background: #1d4ed8;
    }

    &:disabled {
      opacity: 0.5;
      cursor: not-allowed;
    }
  }
}

.empty {
  text-align: center;
  color: #9ca3af;
  font-size: 14px;
  margin-top: 40px;
}
```

### 6.4 Update `client/src/app/app.routes.ts`

Add the `channels/:id` route as a child of the main layout, **before** the `channels/:id/settings` route:

```typescript
{
  path: 'channels/:id',
  loadComponent: () =>
    import('./features/chat/chat.component').then((m) => m.ChatComponent),
},
```

The full children array should look like:

```typescript
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
    path: 'channels/:id',
    loadComponent: () =>
      import('./features/chat/chat.component').then((m) => m.ChatComponent),
  },
  {
    path: 'channels/:id/settings',
    loadComponent: () =>
      import('./features/channel-settings/channel-settings.component').then(
        (m) => m.ChannelSettingsComponent
      ),
  },
],
```

### 6.5 Update `client/src/app/features/channel-list/channel-list.component.ts`

Fix the `openChannel` method to navigate to the chat view instead of settings:

```typescript
openChannel(ch: ChannelWithPreview): void {
  this.router.navigate(['channels', ch.id]);
}
```

Replace only the `openChannel` method body — do not change any other code.

### Commands

```bash
cd /Users/mac17/workspace/ai/im/client
npx ng build --configuration=development 2>&1 | tail -20
```

### Commit

```bash
cd /Users/mac17/workspace/ai/im
git add client/src/app/core/messages/message.service.ts
git add client/src/app/features/chat/
git add client/src/app/app.routes.ts
git add client/src/app/features/channel-list/channel-list.component.ts
git commit -m "feat(client): add chat window component with message send/fetch/read"
```

---

## Task 7: Integration verification

### 7.1 Check server builds cleanly

```bash
cd /Users/mac17/workspace/ai/im/server
go build ./...
go vet ./...
```

### 7.2 Run all unit tests

```bash
cd /Users/mac17/workspace/ai/im/server
go test ./internal/handler/... -v
go test ./internal/pulsar/... -v 2>/dev/null || echo "pulsar tests require running broker - skip"
```

**Expected:** All `handler` tests pass. Pulsar tests are skipped if no broker is running.

### 7.3 Manual smoke test (requires running stack)

Start the backend:
```bash
cd /Users/mac17/workspace/ai/im/server
go run ./cmd/gateway/
```

Send a message:
```bash
# 1. Login
TOKEN=$(curl -s -X POST http://localhost:8080/api/auth/login \
  -H 'Content-Type: application/json' \
  -d '{"login":"alice","password":"pass123"}' | jq -r '.token')

# 2. Create DM channel (requires a known peer_id, e.g. user 2)
CHANNEL_ID=$(curl -s -X POST http://localhost:8080/api/channels/dm \
  -H "Authorization: Bearer $TOKEN" \
  -H 'Content-Type: application/json' \
  -d '{"peer_id":2}' | jq -r '.id')

# 3. Send a message
curl -s -X POST http://localhost:8080/api/channels/$CHANNEL_ID/messages \
  -H "Authorization: Bearer $TOKEN" \
  -H 'Content-Type: application/json' \
  -d '{"content":"hello world","client_msg_id":"test-001","msg_type":1}'

# 4. Fetch messages
curl -s "http://localhost:8080/api/channels/$CHANNEL_ID/messages?after_seq=0" \
  -H "Authorization: Bearer $TOKEN" | jq .

# 5. Mark read
curl -s -X POST http://localhost:8080/api/channels/$CHANNEL_ID/read \
  -H "Authorization: Bearer $TOKEN" | jq .
```

Expected responses:
- Step 3: `{"id":1,"channel_id":N,"seq":1,"sender_id":1,...}`
- Step 4: `{"messages":[{"id":1,"seq":1,...}]}`
- Step 5: `{"seq":1}`

### 7.4 Client build check

```bash
cd /Users/mac17/workspace/ai/im/client
npx ng build --configuration=development 2>&1 | grep -E "error|Error|warning" | head -20
echo "Exit code: $?"
```

Expected: exit code 0, no TypeScript errors.

### 7.5 Final commit (if any remaining changes)

```bash
cd /Users/mac17/workspace/ai/im
git status
# commit anything not yet committed
```

---

## Summary

After Plan 5 is complete, the system provides:

| Capability | Status |
|------------|--------|
| `POST /api/channels/{id}/messages` | Done — direct PG write, idempotent via client_msg_id |
| `GET /api/channels/{id}/messages` | Done — after/before/around modes, phantom filtering |
| `POST /api/channels/{id}/read` | Done — sets last_read_seq = channel.seq |
| Pulsar infrastructure | Done — Producer + Consumer wrappers ready |
| MessageService Pulsar loop | Done — consumes msg.incoming, calls MessageStore.Send |
| Client MessageService | Done — signals-based, optimistic send |
| Client chat window | Done — message list + send box, phantom hidden |

**What Plan 6 will build on top of this:**
- WebSocket connection in Gateway (replaces HTTP send with WS frame)
- Gateway publishes to `msg.incoming` Pulsar topic on WS message receive
- Gateway consumes from `msg.deliver.{gateway_id}` to push ACK back to sender
- Heartbeat pong with channel seq diffs
- Reconnect sync protocol

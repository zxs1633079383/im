# Plan 6: Gateway + 推送路径 Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** 实现 WebSocket 连接管理、心跳、消息实时推送、客户端 WebSocket 集成，构建完整的在线消息投递链路

**Architecture:** Gateway 管理 WebSocket 连接，通过 Redis 注册路由，Pulsar 接收推送事件。客户端建立 WebSocket 连接后可实时收到消息推送，断线后通过拉取兜底。

**Tech Stack:** Go gorilla/websocket, Redis, Pulsar, Angular WebSocket

---

## 目录结构（Plan 6 新增/修改文件）

```
server/
├── internal/
│   └── gateway/
│       ├── types.go          # NEW: wire message types (WSMessage, PushEvent, etc.)
│       ├── hub.go            # NEW: Hub — in-process connection registry + broadcast
│       ├── routing.go        # NEW: Redis routing (register/deregister/lookup)
│       ├── handler.go        # NEW: WebSocket upgrade HTTP handler (JWT from query param)
│       ├── heartbeat.go      # NEW: per-connection heartbeat loop (ping/pong + seq diff)
│       └── push_consumer.go  # NEW: Pulsar consumer → push to connected clients
├── cmd/
│   └── gateway/main.go       # MODIFY: wire Hub, Redis, Pulsar push consumer, /ws route
└── internal/
    └── config/config.go      # MODIFY: add GatewayID field

client/src/app/
└── core/
    └── websocket/
        ├── websocket.service.ts     # NEW: connect, reconnect, message dispatch
        └── websocket.models.ts      # NEW: WS message type definitions
```

---

## Task 1: Install gorilla/websocket + Gateway connection types

**Files to modify/create:**
- `server/go.mod` / `server/go.sum` (add gorilla/websocket)
- `server/internal/gateway/types.go`
- `server/internal/config/config.go` (add GatewayID)

### 1.1 Add gorilla/websocket dependency

```bash
cd server
go get github.com/gorilla/websocket@v1.5.3
```

### 1.2 Add GatewayID to config

Modify `server/internal/config/config.go` — add `ID` field to `GatewayConfig`:

```go
type GatewayConfig struct {
    HTTPAddr  string `yaml:"http_addr"`
    JWTSecret string `yaml:"jwt_secret"`
    ID        string `yaml:"id"` // resolved at runtime from HOSTNAME or UUID if blank
}
```

Add to `applyEnvOverrides`:

```go
if v := os.Getenv("HOSTNAME"); v != "" && cfg.Gateway.ID == "" {
    cfg.Gateway.ID = v
}
```

Add a helper at bottom of config.go:

```go
import "github.com/google/uuid"

// ResolveGatewayID returns cfg.Gateway.ID if set, else generates a random UUID.
// Call once at startup and store the result.
func ResolveGatewayID(cfg *Config) string {
    if cfg.Gateway.ID != "" {
        return cfg.Gateway.ID
    }
    return uuid.New().String()
}
```

> Note: `github.com/google/uuid` is already available transitively. If not, add it:
> `go get github.com/google/uuid`

### 1.3 Create `server/internal/gateway/types.go`

This file defines all WebSocket wire message types shared between handler, heartbeat, and push consumer.

```go
package gateway

import "time"

// ---- Inbound (client → server) ----

// WSMessageType identifies the payload type of a WS frame.
type WSMessageType string

const (
    TypePing      WSMessageType = "ping"
    TypeSend      WSMessageType = "send"       // client sends a chat message
    TypePushACK   WSMessageType = "push_ack"   // client ACKs a pushed message
    TypeSync      WSMessageType = "sync"       // client sends channel state on reconnect
)

// ---- Outbound (server → client) ----

const (
    TypePong      WSMessageType = "pong"
    TypePushMsg   WSMessageType = "push_msg"   // server pushes a chat message
    TypeSendACK   WSMessageType = "send_ack"   // server ACKs client's send
    TypeSyncResp  WSMessageType = "sync_resp"  // server responds to sync
)

// WSFrame is the top-level envelope for every WebSocket message.
type WSFrame struct {
    Type    WSMessageType `json:"type"`
    Payload []byte        `json:"payload,omitempty"` // raw JSON of the specific payload
}

// PingPayload is sent by the client every 15s.
type PingPayload struct {
    // ChannelSeqs maps channel_id (as string) to the client's local max seq.
    // Only channels the client has open/knows about need to be included.
    ChannelSeqs map[string]int64 `json:"channel_seqs,omitempty"`
}

// PongPayload is the server's response to ping.
// channel_seqs contains only channels where server_seq > client_seq.
type PongPayload struct {
    ServerTime  int64            `json:"server_time"` // unix ms, for latency measurement
    ChannelSeqs map[string]int64 `json:"channel_seqs,omitempty"`
}

// PushMsgPayload is sent server→client when a new message is available.
type PushMsgPayload struct {
    PushID    string `json:"push_id"`    // idempotency key for ACK
    ChannelID int64  `json:"channel_id"`
    Seq       int64  `json:"seq"`
    ServerID  int64  `json:"server_msg_id"`
    SenderID  int64  `json:"sender_id"`
    Content   string `json:"content,omitempty"`
    MsgType   int16  `json:"msg_type"`   // 1=normal, 2=phantom
    VisibleTo []int64 `json:"visible_to,omitempty"`
    CreatedAt time.Time `json:"created_at"`
}

// PushACKPayload is the client's acknowledgement of a PushMsgPayload.
type PushACKPayload struct {
    PushID string `json:"push_id"`
}

// SendPayload is a client-initiated message send over WebSocket.
// (Alternative to HTTP POST /api/channels/{id}/messages)
type SendPayload struct {
    ClientMsgID string  `json:"client_msg_id"`
    ChannelID   int64   `json:"channel_id"`
    Content     string  `json:"content"`
    MsgType     int16   `json:"msg_type,omitempty"`
    VisibleTo   []int64 `json:"visible_to,omitempty"`
}

// SendACKPayload is the server's acknowledgement of a client send.
type SendACKPayload struct {
    ClientMsgID string `json:"client_msg_id"`
    ServerMsgID int64  `json:"server_msg_id"`
    Seq         int64  `json:"seq"`
    ChannelID   int64  `json:"channel_id"`
}

// SyncChannelState is one entry in a sync request.
type SyncChannelState struct {
    ID  int64 `json:"id"`
    Seq int64 `json:"seq"` // client's local max seq for this channel
}

// SyncPayload is sent on reconnect.
type SyncPayload struct {
    Channels []SyncChannelState `json:"channels"`
}

// PulsarPushEvent is the message published by MessageService to msg.push.{gateway_id}.
// Gateway consumes this and routes to the WebSocket connection.
type PulsarPushEvent struct {
    PushID    string  `json:"push_id"`     // unique per delivery attempt
    TargetUID int64   `json:"target_uid"`  // user to receive this push
    ChannelID int64   `json:"channel_id"`
    Seq       int64   `json:"seq"`
    ServerID  int64   `json:"server_msg_id"`
    SenderID  int64   `json:"sender_id"`
    Content   string  `json:"content,omitempty"`
    MsgType   int16   `json:"msg_type"`
    VisibleTo []int64 `json:"visible_to,omitempty"`
    CreatedAt string  `json:"created_at"` // RFC3339
}
```

### 1.4 Checklist

- [ ] Run `go get github.com/gorilla/websocket@v1.5.3` in `server/`
- [ ] Modify `server/internal/config/config.go` — add `GatewayConfig.ID` field + `ResolveGatewayID()`
- [ ] Create `server/internal/gateway/types.go` with all types above
- [ ] Run `go build ./...` from `server/` — should compile cleanly
- [ ] Commit: `feat: gateway types and config gateway ID`

---

## Task 2: WebSocket Hub (in-process connection registry)

**File to create:** `server/internal/gateway/hub.go`

### Overview

The Hub is the single in-process registry of all WebSocket connections on this gateway pod. It is safe for concurrent use. Each connection is represented by a `Conn` struct.

### 2.1 `Conn` struct

```go
package gateway

import (
    "encoding/json"
    "sync"
    "time"

    "github.com/gorilla/websocket"
)

// Conn represents one authenticated WebSocket connection.
type Conn struct {
    UserID   int64
    DeviceID string // e.g. "web-<uuid>" or from JWT claim

    ws       *websocket.Conn
    send     chan []byte // buffered outbound channel; closed on disconnect
    hub      *Hub

    // known_seq tracks the last seq successfully pushed to this connection
    // per channel. Used to compute the pong diff.
    mu       sync.RWMutex
    knownSeq map[int64]int64 // channel_id → seq

    lastPong time.Time // updated on every pong received
}

// UpdateKnownSeq records that this connection has received up to seq for channelID.
func (c *Conn) UpdateKnownSeq(channelID, seq int64) {
    c.mu.Lock()
    defer c.mu.Unlock()
    if current, ok := c.knownSeq[channelID]; !ok || seq > current {
        c.knownSeq[channelID] = seq
    }
}

// KnownSeqFor returns the last known seq for channelID (0 if unknown).
func (c *Conn) KnownSeqFor(channelID int64) int64 {
    c.mu.RLock()
    defer c.mu.RUnlock()
    return c.knownSeq[channelID]
}

// KnownSeqs returns a snapshot of the full known_seq map.
func (c *Conn) KnownSeqs() map[int64]int64 {
    c.mu.RLock()
    defer c.mu.RUnlock()
    m := make(map[int64]int64, len(c.knownSeq))
    for k, v := range c.knownSeq {
        m[k] = v
    }
    return m
}

// Push enqueues a JSON-serialisable payload for asynchronous delivery.
// Returns false if the connection's send buffer is full (slow consumer).
func (c *Conn) Push(msgType WSMessageType, payload any) bool {
    data, err := json.Marshal(payload)
    if err != nil {
        return false
    }
    frame := struct {
        Type    WSMessageType   `json:"type"`
        Payload json.RawMessage `json:"payload"`
    }{Type: msgType, Payload: data}
    b, err := json.Marshal(frame)
    if err != nil {
        return false
    }
    select {
    case c.send <- b:
        return true
    default:
        return false // slow consumer — caller should close the conn
    }
}
```

### 2.2 `Hub` struct

```go
// Hub is the in-process connection registry.
// It is safe for concurrent use from multiple goroutines.
type Hub struct {
    mu    sync.RWMutex
    conns map[int64][]*Conn // userID → list of active connections
}

// NewHub creates an empty Hub.
func NewHub() *Hub {
    return &Hub{conns: make(map[int64][]*Conn)}
}

// Register adds a connection to the hub.
func (h *Hub) Register(c *Conn) {
    h.mu.Lock()
    defer h.mu.Unlock()
    h.conns[c.UserID] = append(h.conns[c.UserID], c)
}

// Deregister removes a connection from the hub.
func (h *Hub) Deregister(c *Conn) {
    h.mu.Lock()
    defer h.mu.Unlock()
    list := h.conns[c.UserID]
    updated := list[:0]
    for _, existing := range list {
        if existing != c {
            updated = append(updated, existing)
        }
    }
    if len(updated) == 0 {
        delete(h.conns, c.UserID)
    } else {
        h.conns[c.UserID] = updated
    }
}

// ConnsForUser returns a snapshot of all connections for the given user.
// Callers must not modify the returned slice.
func (h *Hub) ConnsForUser(userID int64) []*Conn {
    h.mu.RLock()
    defer h.mu.RUnlock()
    list := h.conns[userID]
    if len(list) == 0 {
        return nil
    }
    out := make([]*Conn, len(list))
    copy(out, list)
    return out
}

// PushToUser pushes a payload to all connections of userID.
// Returns the number of connections reached.
func (h *Hub) PushToUser(userID int64, msgType WSMessageType, payload any) int {
    conns := h.ConnsForUser(userID)
    sent := 0
    for _, c := range conns {
        if c.Push(msgType, payload) {
            sent++
        }
    }
    return sent
}
```

### 2.3 Write pump (per connection)

Each `Conn` runs a `writePump` goroutine that reads from `c.send` and writes to the WebSocket. The read side is handled by the upgrade handler.

```go
const (
    writeWait = 10 * time.Second
    sendBufSize = 256
)

// writePump drains c.send and writes to the WebSocket.
// It exits when c.send is closed.
func (c *Conn) writePump() {
    defer c.ws.Close()
    for b := range c.send {
        c.ws.SetWriteDeadline(time.Now().Add(writeWait))
        if err := c.ws.WriteMessage(websocket.TextMessage, b); err != nil {
            break
        }
    }
}
```

### 2.4 Checklist

- [ ] Create `server/internal/gateway/hub.go` with `Conn`, `Hub`, and `writePump`
- [ ] `go vet ./internal/gateway/...` passes
- [ ] Commit: `feat: gateway hub — in-process WebSocket connection registry`

---

## Task 3: Redis Routing (register/deregister/lookup)

**File to create:** `server/internal/gateway/routing.go`

### Overview

When a user connects to a gateway pod, we register their device in Redis so that MessageService can look up which gateway to publish push events to. The key schema is:

```
user:connections:{user_id}   hash   field=device_id   value=gateway_id
TTL: 2 hours (refreshed on heartbeat)
```

### 3.1 `Routing` struct

```go
package gateway

import (
    "context"
    "fmt"
    "time"

    "github.com/redis/go-redis/v9"
)

const (
    connKeyTTL = 2 * time.Hour
)

// Routing manages the Redis user-connection routing table.
type Routing struct {
    rdb       *redis.Client
    gatewayID string
}

// NewRouting creates a new Routing backed by rdb.
func NewRouting(rdb *redis.Client, gatewayID string) *Routing {
    return &Routing{rdb: rdb, gatewayID: gatewayID}
}

func connKey(userID int64) string {
    return fmt.Sprintf("user:connections:%d", userID)
}

// Register records that deviceID for userID is connected to this gateway.
// Sets a TTL on the hash key so stale entries expire automatically.
func (r *Routing) Register(ctx context.Context, userID int64, deviceID string) error {
    key := connKey(userID)
    pipe := r.rdb.TxPipeline()
    pipe.HSet(ctx, key, deviceID, r.gatewayID)
    pipe.Expire(ctx, key, connKeyTTL)
    _, err := pipe.Exec(ctx)
    return err
}

// Deregister removes the deviceID entry for userID.
func (r *Routing) Deregister(ctx context.Context, userID int64, deviceID string) error {
    return r.rdb.HDel(ctx, connKey(userID), deviceID).Err()
}

// RefreshTTL resets the expiry of the routing key (call on each heartbeat).
func (r *Routing) RefreshTTL(ctx context.Context, userID int64) error {
    return r.rdb.Expire(ctx, connKey(userID), connKeyTTL).Err()
}

// GatewayIDsForUser returns the set of distinct gateway IDs that userID is connected to.
// Returns empty slice if the user has no active connections.
func (r *Routing) GatewayIDsForUser(ctx context.Context, userID int64) ([]string, error) {
    m, err := r.rdb.HGetAll(ctx, connKey(userID)).Result()
    if err != nil {
        return nil, err
    }
    seen := make(map[string]struct{})
    out := make([]string, 0, len(m))
    for _, gwID := range m {
        if _, dup := seen[gwID]; !dup {
            seen[gwID] = struct{}{}
            out = append(out, gwID)
        }
    }
    return out, nil
}

// DevicesForUser returns all device_id → gateway_id entries for userID.
func (r *Routing) DevicesForUser(ctx context.Context, userID int64) (map[string]string, error) {
    return r.rdb.HGetAll(ctx, connKey(userID)).Result()
}
```

### 3.2 Checklist

- [ ] Create `server/internal/gateway/routing.go`
- [ ] `go vet ./internal/gateway/...` passes
- [ ] Write a brief test (`routing_test.go`) using `miniredis` or a real Redis in CI:
  - Register a device, verify `HGetAll` returns gateway ID
  - Deregister, verify key is removed
- [ ] Commit: `feat: gateway Redis routing (register/deregister/lookup)`

---

## Task 4: WebSocket Upgrade Endpoint (JWT auth from query param)

**File to create:** `server/internal/gateway/handler.go`

### Overview

Clients connect to `GET /ws?token=<jwt>`. The handler:
1. Validates the JWT token from query param
2. Upgrades to WebSocket
3. Creates a `Conn`, registers it in Hub + Redis
4. Spawns writePump goroutine
5. Runs the readPump inline (blocking until disconnect)
6. On exit: deregisters from Hub + Redis

### 4.1 Upgrader configuration

```go
package gateway

import (
    "encoding/json"
    "fmt"
    "log/slog"
    "net/http"
    "time"

    "github.com/gorilla/websocket"
    "im-server/internal/auth"
    "im-server/internal/store"
)

var upgrader = websocket.Upgrader{
    ReadBufferSize:  1024,
    WriteBufferSize: 4096,
    // Allow all origins for development; tighten in production.
    CheckOrigin: func(r *http.Request) bool { return true },
}

const (
    pongTimeout  = 45 * time.Second // server closes conn if no pong in this window
    maxMessageBytes = 64 * 1024     // 64 KB max inbound message
)
```

### 4.2 `Handler` struct

```go
// Handler handles WebSocket upgrade requests.
type Handler struct {
    hub       *Hub
    routing   *Routing
    jwtSecret string
    gatewayID string
    channelSt ChannelSeqStore // to compute pong diff
    log       *slog.Logger
}

// ChannelSeqStore is the minimal interface needed to look up server-side seqs.
type ChannelSeqStore interface {
    // GetChannelSeqs returns the current seq for each channel the user is a member of.
    // Returns map[channel_id]seq.
    GetMemberChannelSeqs(ctx context.Context, userID int64) (map[int64]int64, error)
}

// NewHandler creates a Handler.
func NewHandler(hub *Hub, routing *Routing, jwtSecret, gatewayID string,
    channelSt ChannelSeqStore, log *slog.Logger) *Handler {
    return &Handler{
        hub: hub, routing: routing,
        jwtSecret: jwtSecret, gatewayID: gatewayID,
        channelSt: channelSt, log: log,
    }
}
```

### 4.3 ServeHTTP — upgrade + lifecycle

```go
func (h *Handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
    // 1. Authenticate via JWT in query param.
    tokenStr := r.URL.Query().Get("token")
    if tokenStr == "" {
        http.Error(w, "missing token", http.StatusUnauthorized)
        return
    }
    claims, err := auth.ValidateToken(h.jwtSecret, tokenStr)
    if err != nil {
        http.Error(w, "invalid token", http.StatusUnauthorized)
        return
    }

    // 2. Derive device ID: prefer "device" query param, else generate one.
    deviceID := r.URL.Query().Get("device")
    if deviceID == "" {
        deviceID = fmt.Sprintf("web-%d", time.Now().UnixNano())
    }

    // 3. Upgrade to WebSocket.
    ws, err := upgrader.Upgrade(w, r, nil)
    if err != nil {
        h.log.Warn("ws upgrade failed", "error", err)
        return
    }

    // 4. Build and register the connection.
    conn := &Conn{
        UserID:   claims.UserID,
        DeviceID: deviceID,
        ws:       ws,
        send:     make(chan []byte, sendBufSize),
        hub:      h.hub,
        knownSeq: make(map[int64]int64),
        lastPong: time.Now(),
    }

    ctx := r.Context()
    h.hub.Register(conn)
    if err := h.routing.Register(ctx, claims.UserID, deviceID); err != nil {
        h.log.Warn("redis register failed", "error", err, "user_id", claims.UserID)
    }

    h.log.Info("ws connected", "user_id", claims.UserID, "device_id", deviceID)

    // 5. Start write pump goroutine.
    go conn.writePump()

    // 6. Start heartbeat loop (sends pings, closes conn on timeout).
    go runHeartbeat(conn, h.channelSt, h.log)

    // 7. Read pump runs in this goroutine until disconnect.
    h.readPump(conn)

    // 8. Cleanup on disconnect.
    close(conn.send)
    h.hub.Deregister(conn)
    bgCtx := context.Background()
    if err := h.routing.Deregister(bgCtx, conn.UserID, conn.DeviceID); err != nil {
        h.log.Warn("redis deregister failed", "error", err)
    }
    h.log.Info("ws disconnected", "user_id", conn.UserID, "device_id", conn.DeviceID)
}
```

### 4.4 Read pump

```go
// readPump reads inbound frames from the WebSocket and dispatches them.
func (h *Handler) readPump(conn *Conn) {
    conn.ws.SetReadLimit(maxMessageBytes)
    conn.ws.SetReadDeadline(time.Now().Add(pongTimeout))

    for {
        _, data, err := conn.ws.ReadMessage()
        if err != nil {
            break // connection closed or timed out
        }
        // Reset read deadline on any inbound traffic.
        conn.ws.SetReadDeadline(time.Now().Add(pongTimeout))

        var frame struct {
            Type    WSMessageType   `json:"type"`
            Payload json.RawMessage `json:"payload"`
        }
        if err := json.Unmarshal(data, &frame); err != nil {
            h.log.Debug("malformed ws frame", "error", err)
            continue
        }

        switch frame.Type {
        case TypePing:
            // Handled by heartbeat goroutine via lastPong refresh.
            // Re-use payload to update known_seq from client state.
            var ping PingPayload
            if err := json.Unmarshal(frame.Payload, &ping); err == nil {
                for chIDStr, seq := range ping.ChannelSeqs {
                    var chID int64
                    fmt.Sscanf(chIDStr, "%d", &chID)
                    conn.UpdateKnownSeq(chID, seq)
                }
            }
            conn.lastPong = time.Now() // treat ping as liveness proof
        case TypePushACK:
            var ack PushACKPayload
            if err := json.Unmarshal(frame.Payload, &ack); err == nil {
                h.log.Debug("push_ack received", "push_id", ack.PushID)
                // ACK is handled: push consumer awaits on a per-push_id channel.
                globalACKRegistry.resolve(ack.PushID)
            }
        default:
            h.log.Debug("unhandled ws frame type", "type", frame.Type)
        }
    }
}
```

> **Note on ACK registry:** `globalACKRegistry` is a lightweight in-process structure (map[push_id]chan struct{}) used by the push consumer to await client ACKs. Defined in `push_consumer.go`.

### 4.5 Implement `GetMemberChannelSeqs` in ChannelStore

Add to `server/internal/store/channel.go`:

```go
// GetMemberChannelSeqs returns the current server seq for every channel
// the user belongs to. Used by the heartbeat to compute the pong diff.
func (s *ChannelStore) GetMemberChannelSeqs(ctx context.Context, userID int64) (map[int64]int64, error) {
    rows, err := s.pool.Query(ctx, `
        SELECT c.id, c.seq
        FROM channels c
        JOIN channel_members cm ON cm.channel_id = c.id
        WHERE cm.user_id = $1
    `, userID)
    if err != nil {
        return nil, err
    }
    defer rows.Close()

    m := make(map[int64]int64)
    for rows.Next() {
        var id, seq int64
        if err := rows.Scan(&id, &seq); err != nil {
            return nil, err
        }
        m[id] = seq
    }
    return m, rows.Err()
}
```

### 4.6 Checklist

- [ ] Create `server/internal/gateway/handler.go`
- [ ] Add `GetMemberChannelSeqs` to `server/internal/store/channel.go`
- [ ] Define `ChannelSeqStore` interface in handler.go (or types.go)
- [ ] `go build ./...` passes
- [ ] Commit: `feat: WebSocket upgrade handler with JWT auth`

---

## Task 5: Heartbeat Mechanism (ping/pong with seq diff)

**File to create:** `server/internal/gateway/heartbeat.go`

### Overview

Per the design spec:
- Server sends ping every 15s, client must pong within 30s (server side: we close if `lastPong` is >30s stale)
- But since the client owns the ping and we read `lastPong` from the read pump, the server's role is:
  1. Periodically read current channel seqs from DB
  2. Compare with `conn.knownSeq`
  3. Push a `pong` frame (server-initiated heartbeat push) to the client with the diff
  4. If `conn.lastPong` exceeds deadline, close the connection

Wait — re-reading the spec: "客户端: 每 15 秒发 ping，30 秒无 pong 认为断线 → 重连 / 服务端: 收到 ping 回 pong + channel seq diff，45 秒无数据 → 关闭连接"

So the flow is:
- **Client** sends `ping` (with its `channel_seqs`)
- **Server** replies with `pong` (with server-side seq diff)
- **Server** closes connection if it receives no data (any frame) for 45s

The read pump already resets the read deadline on every message. The heartbeat goroutine just needs to watch for the ping and respond with a pong carrying the diff. But since the read pump handles the incoming ping and updates `lastPong`, the heartbeat goroutine reads that timestamp.

Revised approach: heartbeat goroutine runs a ticker every 15s. On each tick, it computes the diff and sends a `pong` proactively (so the client always has fresh seq data even without sending a ping). The read pump's `SetReadDeadline` (45s) closes the conn if the client disappears.

```go
package gateway

import (
    "context"
    "fmt"
    "log/slog"
    "time"
)

const (
    heartbeatInterval = 15 * time.Second
)

// runHeartbeat sends periodic pong frames to conn with the current channel seq diff.
// It exits when conn.send is closed (conn is dead) or ctx is done.
func runHeartbeat(conn *Conn, channelSt ChannelSeqStore, log *slog.Logger) {
    ticker := time.NewTicker(heartbeatInterval)
    defer ticker.Stop()

    for range ticker.C {
        // If send channel is closed, exit.
        select {
        case _, ok := <-conn.send:
            if !ok {
                return
            }
        default:
        }

        ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
        serverSeqs, err := channelSt.GetMemberChannelSeqs(ctx, conn.UserID)
        cancel()
        if err != nil {
            log.Warn("heartbeat: get channel seqs failed", "error", err, "user_id", conn.UserID)
            continue
        }

        // Compute diff: channels where server_seq > client's known_seq.
        diff := make(map[string]int64)
        for chID, serverSeq := range serverSeqs {
            known := conn.KnownSeqFor(chID)
            if serverSeq > known {
                diff[fmt.Sprintf("%d", chID)] = serverSeq
            }
        }

        payload := PongPayload{
            ServerTime:  time.Now().UnixMilli(),
            ChannelSeqs: diff,
        }
        if !conn.Push(TypePong, payload) {
            log.Warn("heartbeat: send buffer full, closing conn",
                "user_id", conn.UserID, "device_id", conn.DeviceID)
            conn.ws.Close()
            return
        }
    }
}
```

> **Read deadline acts as server-side timeout.** The read pump sets `SetReadDeadline(now + 45s)` and resets it on every inbound message. If the client sends no ping for 45s, `ReadMessage` returns an error and the read pump exits, triggering cleanup.

### 5.1 Checklist

- [ ] Create `server/internal/gateway/heartbeat.go`
- [ ] Verify `runHeartbeat` is called from `handler.go` in the `ServeHTTP` goroutine
- [ ] Manual test: connect a WebSocket client, observe pong frames every 15s
- [ ] Commit: `feat: gateway heartbeat — periodic pong with channel seq diff`

---

## Task 6: Push Consumer (Pulsar → WebSocket)

**File to create:** `server/internal/gateway/push_consumer.go`

### Overview

The gateway subscribes to `msg.push.{gateway_id}` Pulsar topic. When a `PulsarPushEvent` arrives, it finds the target user's connections on this pod and pushes the message. It then waits up to 3s for a client ACK. If no ACK, it logs and moves on (pull-based fallback will catch it).

### 6.1 ACK Registry

```go
package gateway

import (
    "context"
    "encoding/json"
    "fmt"
    "log/slog"
    "sync"
    "time"

    imPulsar "im-server/internal/pulsar"
)

// ackRegistry is a simple in-process map from push_id to resolution channel.
type ackRegistry struct {
    mu      sync.Mutex
    pending map[string]chan struct{}
}

func newACKRegistry() *ackRegistry {
    return &ackRegistry{pending: make(map[string]chan struct{})}
}

// await registers a channel for pushID and returns it.
// The caller should call cleanup() when done.
func (r *ackRegistry) await(pushID string) (ch chan struct{}, cleanup func()) {
    c := make(chan struct{}, 1)
    r.mu.Lock()
    r.pending[pushID] = c
    r.mu.Unlock()
    return c, func() {
        r.mu.Lock()
        delete(r.pending, pushID)
        r.mu.Unlock()
    }
}

// resolve signals that pushID has been ACKed.
func (r *ackRegistry) resolve(pushID string) {
    r.mu.Lock()
    ch, ok := r.pending[pushID]
    r.mu.Unlock()
    if ok {
        select {
        case ch <- struct{}{}:
        default:
        }
    }
}

// Package-level registry (one per gateway process).
var globalACKRegistry = newACKRegistry()
```

### 6.2 PushConsumer struct

```go
// PushConsumer subscribes to msg.push.{gatewayID} and delivers to connected clients.
type PushConsumer struct {
    hub       *Hub
    gatewayID string
    log       *slog.Logger
}

// NewPushConsumer creates a PushConsumer.
func NewPushConsumer(hub *Hub, gatewayID string, log *slog.Logger) *PushConsumer {
    return &PushConsumer{hub: hub, gatewayID: gatewayID, log: log}
}

// Handle is the Pulsar HandlerFunc. Processes one PulsarPushEvent.
func (pc *PushConsumer) Handle(ctx context.Context, data []byte) error {
    var event PulsarPushEvent
    if err := json.Unmarshal(data, &event); err != nil {
        return fmt.Errorf("unmarshal push event: %w", err)
    }

    conns := pc.hub.ConnsForUser(event.TargetUID)
    if len(conns) == 0 {
        // User not connected to this pod — this shouldn't happen if routing is correct,
        // but log and ACK to avoid redelivery.
        pc.log.Debug("push: no connections for user", "uid", event.TargetUID)
        return nil
    }

    payload := PushMsgPayload{
        PushID:    event.PushID,
        ChannelID: event.ChannelID,
        Seq:       event.Seq,
        ServerID:  event.ServerID,
        SenderID:  event.SenderID,
        Content:   event.Content,
        MsgType:   event.MsgType,
        VisibleTo: event.VisibleTo,
    }

    // Push to all connections of this user, then wait for any ACK.
    ackCh, cleanup := globalACKRegistry.await(event.PushID)
    defer cleanup()

    sent := 0
    for _, conn := range conns {
        if conn.Push(TypePushMsg, payload) {
            sent++
            // Update known_seq optimistically; will be corrected if ACK never arrives.
            conn.UpdateKnownSeq(event.ChannelID, event.Seq)
        }
    }

    if sent == 0 {
        pc.log.Warn("push: all send buffers full", "uid", event.TargetUID)
        return nil // don't NACK — will be covered by pull
    }

    // Wait up to 3s for client ACK.
    select {
    case <-ackCh:
        pc.log.Debug("push ack received", "push_id", event.PushID)
    case <-time.After(3 * time.Second):
        pc.log.Debug("push ack timeout — pull will catch up", "push_id", event.PushID)
        // Do NOT retry here — spec says 1 retry at 5s, then give up.
        // Simple version: just give up now; pull fallback covers it.
    case <-ctx.Done():
    }

    return nil // always ACK the Pulsar message to avoid redelivery storm
}
```

### 6.3 MessageService changes: publish push events per member

Currently `cmd/message/main.go` publishes to `msg.deliver.{gateway_id}` (sender ACK only). Plan 6 requires it to publish a `PulsarPushEvent` to `msg.push.{gateway_id}` for **every member** of the channel. This is done in `messageService.handle()`.

Add to `cmd/message/main.go`:

```go
// After msg is persisted, look up channel members and push to each.
// Each member's target gateway is found via Redis routing.
func (svc *messageService) pushToMembers(ctx context.Context, msg *model.Message) {
    members, err := svc.channelStore.ListMembers(ctx, msg.ChannelID)
    if err != nil {
        svc.log.Warn("pushToMembers: list members failed", "error", err)
        return
    }

    for _, member := range members {
        gatewayIDs, err := svc.routing.GatewayIDsForUser(ctx, member.UserID)
        if err != nil || len(gatewayIDs) == 0 {
            continue // user offline, skip
        }

        // Determine visibility for this member.
        isVisible := msg.VisibleTo == nil || contains(msg.VisibleTo, member.UserID)
        msgType := int16(1) // normal
        content := msg.Content
        visibleTo := msg.VisibleTo
        if !isVisible {
            msgType = 2 // phantom
            content = ""
            visibleTo = nil
        }

        pushID := fmt.Sprintf("%d-%d-%d", msg.ChannelID, msg.Seq, member.UserID)
        event := PulsarPushEvent{
            PushID:    pushID,
            TargetUID: member.UserID,
            ChannelID: msg.ChannelID,
            Seq:       msg.Seq,
            ServerID:  msg.ID,
            SenderID:  msg.SenderID,
            Content:   content,
            MsgType:   msgType,
            VisibleTo: visibleTo,
            CreatedAt: msg.CreatedAt.Format(time.RFC3339),
        }

        for _, gwID := range gatewayIDs {
            topic := "msg.push." + gwID
            key := fmt.Sprintf("%d", member.UserID)
            if err := svc.pushProducer.Send(ctx, key, event); err != nil {
                svc.log.Warn("push event send failed", "topic", topic, "error", err)
            }
        }
    }
}

func contains(list []int64, id int64) bool {
    for _, v := range list {
        if v == id {
            return true
        }
    }
    return false
}
```

The `PulsarPushEvent` type is defined in `gateway/types.go`. To avoid circular imports, move it to a shared `internal/model/push.go` or duplicate it in `cmd/message/`. The simplest approach: **duplicate the struct** in `cmd/message/main.go` with the same JSON tags. No shared package needed.

Add fields to `messageService`:

```go
type messageService struct {
    store        *store.MessageStore
    channelStore *store.ChannelStore // NEW
    routing      *gateway.Routing   // NEW — but this creates import cycle
    producer     *imPulsar.Producer // deliver ack (existing)
    pushProducer *imPulsar.Producer // NEW: push events per member
    log          *slog.Logger
}
```

> **Import cycle note:** `cmd/message` importing `internal/gateway` would be fine since `gateway` doesn't import `message`. The routing logic is in `internal/gateway/routing.go` which only depends on `redis`. This is a clean dependency.

Actually, to keep it cleaner, move `Routing` to `internal/store/routing.go` (alongside the other stores) so both gateway and message service can import it:

- Move `Routing` struct from `internal/gateway/routing.go` to `internal/store/routing.go`
- `internal/gateway/routing.go` becomes a thin re-export or is removed; gateway imports from `store`

### 6.4 Refactor: Move Routing to `internal/store/routing.go`

**File to create:** `server/internal/store/routing.go`

Move the `Routing` struct (with `Register`, `Deregister`, `RefreshTTL`, `GatewayIDsForUser`, `DevicesForUser`) to `internal/store`. Update the import in gateway handler.

```go
package store

// (same code as section 3.1, but in package store)
```

Then in `internal/gateway/routing.go`, keep just the import alias or delete the file.

### 6.5 Checklist

- [ ] Create `server/internal/gateway/push_consumer.go` with `ackRegistry` and `PushConsumer`
- [ ] Move `Routing` to `server/internal/store/routing.go`
- [ ] Update `server/internal/gateway/handler.go` imports to use `store.Routing`
- [ ] Modify `cmd/message/main.go`:
  - Add `channelStore`, `routing`, `pushProducer` fields to `messageService`
  - Call `svc.pushToMembers(ctx, msg)` after `store.Send` succeeds
  - Wire new stores/producers in `run()`
- [ ] Add `PulsarPushEvent` struct to `cmd/message/main.go` (mirrored from gateway types)
- [ ] `go build ./...` passes
- [ ] Commit: `feat: push consumer and message-service fan-out to gateway topics`

---

## Task 7: Wire Everything in `cmd/gateway/main.go`

**File to modify:** `server/cmd/gateway/main.go`

### Overview

Add to the gateway startup sequence:
1. Connect to Redis
2. Resolve gateway ID
3. Create Hub, Routing, Handler
4. Connect to Pulsar, start push consumer goroutine
5. Register `/ws` route

### 7.1 New `run()` additions

```go
// After pool setup, add:

// Redis
rdb, err := store.NewRedisClient(ctx, cfg.Redis.Addr, cfg.Redis.Password, cfg.Redis.DB)
if err != nil {
    log.Error("connect to redis", "error", err)
    return 1
}
defer rdb.Close()

// Gateway ID
gatewayID := config.ResolveGatewayID(cfg)
log.Info("gateway started", "id", gatewayID)

// WebSocket Hub
hub := gateway.NewHub()

// Redis routing
routing := store.NewRouting(rdb, gatewayID)

// ChannelStore (already exists for channelHandler; reuse it)
// channelStore already created above — pass it to wsHandler

// WebSocket handler
wsHandler := gateway.NewHandler(hub, routing, cfg.Gateway.JWTSecret, gatewayID, channelStore, log)

// Pulsar push consumer (non-fatal if Pulsar is unavailable)
if cfg.Pulsar.URL != "" {
    pulsarClient, err := imPulsar.New(cfg.Pulsar.URL, log)
    if err != nil {
        log.Warn("pulsar unavailable (push disabled)", "error", err)
    } else {
        defer pulsarClient.Close()
        pushConsumer := gateway.NewPushConsumer(hub, gatewayID, log)
        topic := "msg.push." + gatewayID
        consumer, err := pulsarClient.NewConsumer(topic, "gateway-"+gatewayID, pushConsumer.Handle)
        if err != nil {
            log.Warn("push consumer create failed (push disabled)", "error", err)
        } else {
            defer consumer.Close()
            go func() {
                if err := consumer.Consume(runCtx); err != nil {
                    log.Error("push consumer error", "error", err)
                }
            }()
        }
    }
}

// Register /ws route (no JWT middleware — handler validates token from query param)
mux.HandleFunc("GET /ws", wsHandler.ServeHTTP)
```

> Note: `runCtx` needs to be available before the server starts. Restructure `run()` to create `runCtx` early (before server start), and cancel it on SIGINT.

### 7.2 Restructured `run()` outline

```go
func run() int {
    // ... logging, config, pg pool ...

    // Create runCtx early for Pulsar consumer lifetime.
    runCtx, runCancel := context.WithCancel(context.Background())
    defer runCancel()

    // ... redis, gateway ID, hub, routing ...
    // ... pulsar consumer in goroutine with runCtx ...
    // ... http server setup ...

    // Signal handling
    quit := make(chan os.Signal, 1)
    signal.Notify(quit, syscall.SIGINT, syscall.SIGTERM)
    go func() {
        log.Info("HTTP server listening", "addr", cfg.Gateway.HTTPAddr)
        if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
            log.Error("server error", "error", err)
            runCancel()
        }
    }()

    <-quit
    runCancel() // stop Pulsar consumer
    // ... graceful HTTP shutdown ...
}
```

### 7.3 Checklist

- [ ] Modify `server/cmd/gateway/main.go` with Redis, Hub, Routing, WsHandler, Pulsar consumer
- [ ] Add `/ws` route to mux
- [ ] Ensure `runCtx` is created early and cancelled on shutdown
- [ ] `go build ./cmd/gateway/` passes
- [ ] Manual test: `wscat -c "ws://localhost:8080/ws?token=<jwt>"` connects successfully
- [ ] Commit: `feat: wire WebSocket hub, routing, and push consumer in gateway main`

---

## Task 8: Client WebSocket Service

**Files to create:**
- `client/src/app/core/websocket/websocket.models.ts`
- `client/src/app/core/websocket/websocket.service.ts`

### 8.1 `websocket.models.ts`

```typescript
export type WSMessageType =
  | 'ping' | 'pong'
  | 'push_msg' | 'push_ack'
  | 'send' | 'send_ack'
  | 'sync' | 'sync_resp';

export interface WSFrame<T = unknown> {
  type: WSMessageType;
  payload: T;
}

export interface PingPayload {
  channel_seqs: Record<string, number>; // channel_id (string) → local max seq
}

export interface PongPayload {
  server_time: number;
  channel_seqs: Record<string, number>; // only channels with diff
}

export interface PushMsgPayload {
  push_id: string;
  channel_id: number;
  seq: number;
  server_msg_id: number;
  sender_id: number;
  content: string;
  msg_type: number; // 1=normal, 2=phantom
  visible_to?: number[];
  created_at: string;
}

export interface PushACKPayload {
  push_id: string;
}

export interface SendPayload {
  client_msg_id: string;
  channel_id: number;
  content: string;
  msg_type?: number;
  visible_to?: number[];
}

export interface SendACKPayload {
  client_msg_id: string;
  server_msg_id: number;
  seq: number;
  channel_id: number;
}

export interface SyncChannelState {
  id: number;
  seq: number;
}

export interface SyncPayload {
  channels: SyncChannelState[];
}
```

### 8.2 `websocket.service.ts`

```typescript
import { Injectable, OnDestroy, inject } from '@angular/core';
import { Subject, BehaviorSubject, interval, Subscription } from 'rxjs';
import { AuthService } from '../auth/auth.service';
import {
  WSFrame, WSMessageType,
  PingPayload, PongPayload,
  PushMsgPayload, PushACKPayload,
  SendACKPayload
} from './websocket.models';

const WS_URL = 'ws://localhost:8080/ws';
const PING_INTERVAL_MS = 15_000;
const RECONNECT_DELAY_MS = 3_000;
const MAX_RECONNECT_ATTEMPTS = 10;

@Injectable({ providedIn: 'root' })
export class WebSocketService implements OnDestroy {
  private auth = inject(AuthService);

  // Public observables
  readonly connected$ = new BehaviorSubject<boolean>(false);
  readonly pushMsg$ = new Subject<PushMsgPayload>();
  readonly sendAck$ = new Subject<SendACKPayload>();
  readonly pong$ = new Subject<PongPayload>();

  private ws: WebSocket | null = null;
  private pingTimer: ReturnType<typeof setInterval> | null = null;
  private reconnectTimer: ReturnType<typeof setTimeout> | null = null;
  private reconnectAttempts = 0;
  private destroyed = false;

  // channel_id (as string) → local max seq (for ping payload)
  private channelSeqs: Record<string, number> = {};

  connect(): void {
    if (this.ws?.readyState === WebSocket.OPEN) return;
    const token = this.auth.getToken();
    if (!token) return;

    const deviceID = this.getOrCreateDeviceID();
    const url = `${WS_URL}?token=${encodeURIComponent(token)}&device=${deviceID}`;
    this.ws = new WebSocket(url);

    this.ws.onopen = () => {
      this.reconnectAttempts = 0;
      this.connected$.next(true);
      this.startPing();
      // Trigger sync on connect (Task 9).
      this.sendSync();
    };

    this.ws.onmessage = (event) => this.onMessage(event.data);

    this.ws.onclose = () => {
      this.connected$.next(false);
      this.stopPing();
      if (!this.destroyed) this.scheduleReconnect();
    };

    this.ws.onerror = () => {
      // onclose fires immediately after onerror; no extra handling needed.
    };
  }

  disconnect(): void {
    this.destroyed = true;
    this.stopPing();
    if (this.reconnectTimer) clearTimeout(this.reconnectTimer);
    this.ws?.close();
    this.ws = null;
  }

  /** Update the local max seq for a channel (call after storing a message). */
  updateChannelSeq(channelId: number, seq: number): void {
    const key = String(channelId);
    if ((this.channelSeqs[key] ?? -1) < seq) {
      this.channelSeqs[key] = seq;
    }
  }

  /** Send a raw frame over the WebSocket. */
  send<T>(type: WSMessageType, payload: T): void {
    if (this.ws?.readyState !== WebSocket.OPEN) return;
    this.ws.send(JSON.stringify({ type, payload }));
  }

  // ---- Private ----

  private onMessage(raw: string): void {
    let frame: WSFrame;
    try {
      frame = JSON.parse(raw);
    } catch {
      return;
    }

    switch (frame.type) {
      case 'pong': {
        const p = frame.payload as PongPayload;
        this.pong$.next(p);
        // If server reports channels with higher seq, emit for sync.
        // (MessageService listens to pong$ and triggers pull for those channels.)
        break;
      }
      case 'push_msg': {
        const msg = frame.payload as PushMsgPayload;
        // ACK immediately.
        const ack: PushACKPayload = { push_id: msg.push_id };
        this.send('push_ack', ack);
        // Update local seq tracking.
        this.updateChannelSeq(msg.channel_id, msg.seq);
        this.pushMsg$.next(msg);
        break;
      }
      case 'send_ack': {
        this.sendAck$.next(frame.payload as SendACKPayload);
        break;
      }
    }
  }

  private startPing(): void {
    this.stopPing();
    this.pingTimer = setInterval(() => {
      const payload: PingPayload = { channel_seqs: { ...this.channelSeqs } };
      this.send('ping', payload);
    }, PING_INTERVAL_MS);
  }

  private stopPing(): void {
    if (this.pingTimer) {
      clearInterval(this.pingTimer);
      this.pingTimer = null;
    }
  }

  private scheduleReconnect(): void {
    if (this.reconnectAttempts >= MAX_RECONNECT_ATTEMPTS) return;
    const delay = RECONNECT_DELAY_MS * Math.min(this.reconnectAttempts + 1, 5);
    this.reconnectTimer = setTimeout(() => {
      this.reconnectAttempts++;
      this.connect();
    }, delay);
  }

  private sendSync(): void {
    // Implemented in Task 9; called here as a hook.
    // MessageService will subscribe to connected$ and trigger sync.
  }

  private getOrCreateDeviceID(): string {
    const key = 'im_device_id';
    let id = localStorage.getItem(key);
    if (!id) {
      id = 'web-' + crypto.randomUUID();
      localStorage.setItem(key, id);
    }
    return id;
  }

  ngOnDestroy(): void {
    this.disconnect();
  }
}
```

### 8.3 Integrate WebSocket into AppComponent or AuthService

In `client/src/app/app.ts` (or the auth effect), call `wsService.connect()` after login and `wsService.disconnect()` on logout.

In `client/src/app/core/auth/auth.service.ts`, inject `WebSocketService` and:

```typescript
// After successful login:
this.wsService.connect();

// On logout:
this.wsService.disconnect();
```

### 8.4 Integrate pushed messages into MessageService

In `client/src/app/core/messages/message.service.ts`:

```typescript
constructor(private ws: WebSocketService, private db: DbService) {
  // Subscribe to pushed messages.
  this.ws.pushMsg$.subscribe(msg => this.handlePush(msg));
  
  // Subscribe to pong — trigger pull for channels with stale local seq.
  this.ws.pong$.subscribe(pong => this.handlePong(pong));
}

private async handlePush(msg: PushMsgPayload): Promise<void> {
  // Store in local SQLite.
  await this.db.saveMessage({
    channel_id: msg.channel_id,
    seq: msg.seq,
    server_id: String(msg.server_msg_id),
    sender_id: String(msg.sender_id),
    content: msg.content,
    msg_type: msg.msg_type,
    visible: msg.msg_type !== 2 ? 1 : 0,
    created_at: new Date(msg.created_at).getTime(),
  });
  // Update channel local state (unread count, last preview).
  this.refreshChannelState(msg.channel_id);
}

private handlePong(pong: PongPayload): void {
  // For each channel with server_seq > local_seq, trigger a pull.
  for (const [chIdStr, serverSeq] of Object.entries(pong.channel_seqs ?? {})) {
    const chId = Number(chIdStr);
    const localSeq = this.ws.channelSeqs[chIdStr] ?? 0;
    if (serverSeq > localSeq) {
      this.fetchMissedMessages(chId, localSeq);
    }
  }
}
```

### 8.5 Checklist

- [ ] Create `client/src/app/core/websocket/websocket.models.ts`
- [ ] Create `client/src/app/core/websocket/websocket.service.ts`
- [ ] Inject `WebSocketService` into `AuthService` (connect on login, disconnect on logout)
- [ ] Update `MessageService` to subscribe to `pushMsg$` and `pong$`
- [ ] `ng build` passes
- [ ] Manual test: send a message via HTTP, observe it arrive via WebSocket push in another browser tab
- [ ] Commit: `feat: client WebSocket service — connect, ping, push handling, ACK`

---

## Task 9: Client Sync on Reconnect

### Overview

When the WebSocket connects (or reconnects), the client sends a `sync` frame with its current channel states. The server compares with DB state and returns missing messages.

This requires a **sync endpoint** on the server side. For Plan 6, implement it as a WebSocket message handler in the read pump (not HTTP) since we already have the connection. Alternatively, use the existing HTTP `GET /api/channels/{id}/messages` with `after_seq` — which is simpler and avoids scope creep.

**Decision:** Use HTTP pull for sync (existing endpoint). The `WebSocketService` signals reconnect via `connected$`, and `MessageService` iterates known channels and calls the existing `fetchMessages(channelId, afterSeq)` for any channel where local seq is stale.

### 9.1 MessageService reconnect sync

In `client/src/app/core/messages/message.service.ts`:

```typescript
constructor(
  private ws: WebSocketService,
  private http: HttpClient,
  private db: DbService,
  private authService: AuthService,
) {
  // On each connection (including reconnects), pull missed messages.
  this.ws.connected$.pipe(
    filter(connected => connected),
    // Small debounce so the connection stabilizes before we hammer the server.
    debounceTime(200),
  ).subscribe(() => this.syncAllChannels());
}

private async syncAllChannels(): Promise<void> {
  const channels = await this.db.listLocalChannels();
  for (const ch of channels) {
    const localMaxSeq = await this.db.getMaxSeq(ch.id);
    if (localMaxSeq < ch.server_seq) {
      // Missed messages — pull them.
      await this.fetchMissedMessages(ch.id, localMaxSeq);
    }
  }
}

private async fetchMissedMessages(channelId: number, afterSeq: number): Promise<void> {
  try {
    const msgs = await this.http.get<Message[]>(
      `/api/channels/${channelId}/messages?after_seq=${afterSeq}&limit=100`
    ).toPromise();
    for (const msg of msgs ?? []) {
      await this.db.saveMessage(msg);
      this.ws.updateChannelSeq(channelId, msg.seq);
    }
    this.refreshChannelState(channelId);
  } catch (e) {
    console.warn('fetchMissedMessages failed', channelId, e);
  }
}
```

### 9.2 Update local channel server_seq on pong

When a `pong` arrives with `channel_seqs`, update the local channel's `server_seq` in SQLite so that `syncAllChannels` can detect the diff even before reconnect:

```typescript
private async handlePong(pong: PongPayload): Promise<void> {
  for (const [chIdStr, serverSeq] of Object.entries(pong.channel_seqs ?? {})) {
    const chId = Number(chIdStr);
    await this.db.updateChannelServerSeq(chId, serverSeq);
    // If we have a local seq less than serverSeq, pull now.
    const localSeq = this.ws.channelSeqs[chIdStr] ?? 0;
    if (serverSeq > localSeq) {
      this.fetchMissedMessages(chId, localSeq);
    }
  }
}
```

### 9.3 Add `updateChannelServerSeq` to DbService

In `client/src/app/core/db/db.service.ts`:

```typescript
async updateChannelServerSeq(channelId: number, serverSeq: number): Promise<void> {
  await this.db.execute(
    `UPDATE local_channels SET server_seq = MAX(server_seq, ?) WHERE id = ?`,
    [serverSeq, String(channelId)]
  );
}

async getMaxSeq(channelId: number): Promise<number> {
  const result = await this.db.select<{ max_seq: number }>(
    `SELECT COALESCE(MAX(seq), 0) as max_seq FROM local_messages WHERE channel_id = ?`,
    [String(channelId)]
  );
  return result[0]?.max_seq ?? 0;
}
```

### 9.4 Checklist

- [ ] Update `MessageService` constructor to subscribe to `connected$` and call `syncAllChannels()`
- [ ] Implement `syncAllChannels()` and `fetchMissedMessages()` in `MessageService`
- [ ] Update `handlePong()` to call `updateChannelServerSeq` and trigger pull
- [ ] Add `updateChannelServerSeq` and `getMaxSeq` to `DbService`
- [ ] Test: disconnect client, send messages via HTTP, reconnect — verify messages appear
- [ ] Commit: `feat: client sync on reconnect — pull missed messages via pong diff`

---

## Task 10: Integration Verification

### 10.1 End-to-end test checklist

- [ ] **WebSocket connect:** `wscat -c "ws://localhost:8080/ws?token=<jwt>"` connects without error
- [ ] **Heartbeat:** Server sends pong frames every 15 seconds; payload contains `server_time` and optionally `channel_seqs`
- [ ] **Push on send:** User A and B are in the same channel. User A sends a message via HTTP POST. User B (connected via WebSocket) receives a `push_msg` frame within 1s.
- [ ] **Push ACK:** Client ACKs the push; server logs "push ack received"
- [ ] **Phantom push:** User A sends a `visible_to=[A]` message. User B receives `{type:"push_msg", payload:{msg_type:2}}` (phantom, no content)
- [ ] **Reconnect sync:** Kill WebSocket connection. Send 3 messages to user B's channel. Reconnect. Verify all 3 messages are pulled via `syncAllChannels`.
- [ ] **Redis routing:** Inspect `HGETALL user:connections:{user_id}` in Redis — shows `device_id → gateway_id` while connected, entry removed on disconnect.
- [ ] **Multi-tab:** Open two browser tabs as the same user. Send a message from Tab A. Verify it arrives in Tab B via push (same user, two connections in Hub).

### 10.2 Unit test coverage

- [ ] `hub_test.go`: Register/Deregister/ConnsForUser/PushToUser
- [ ] `push_consumer_test.go`: Handle with mock Hub — verify PushToUser is called with correct payload
- [ ] `heartbeat_test.go`: runHeartbeat emits pong with correct diff (stub ChannelSeqStore)
- [ ] `websocket.service.spec.ts`: onMessage dispatches pushMsg$, send_ack$, pong$ correctly; ACK sent on push_msg

### 10.3 Final commit

```bash
cd /Users/mac17/workspace/ai/im
git add server/ client/
git commit -m "feat: Plan 6 — gateway WebSocket push delivery (hub, heartbeat, Pulsar consumer, client WS service)"
```

---

## Summary: Push Delivery Flow

```
[MessageService]
  store.Send() → msg persisted (seq=521)
  pushToMembers():
    for each member in channel:
      GatewayIDsForUser(member.user_id) → ["gw-pod-1"]
      Pulsar.Send("msg.push.gw-pod-1", PulsarPushEvent{target_uid, seq=521, ...})

[Gateway pod gw-pod-1]
  PushConsumer.Handle(PulsarPushEvent):
    hub.ConnsForUser(target_uid) → [conn1, conn2]
    conn1.Push(TypePushMsg, payload) → enqueue to send chan
    conn2.Push(TypePushMsg, payload) → enqueue to send chan
    await ACK (3s timeout)

[Conn.writePump()]
  reads from send chan → ws.WriteMessage(TextMessage, frame)

[Client]
  ws.onmessage → parse push_msg → store in SQLite → update UI
  send push_ack → ws.send({type:"push_ack", payload:{push_id}})

[Gateway readPump]
  parse push_ack → globalACKRegistry.resolve(push_id) → unblocks await
```

---

## Key File Summary

| File | Status | Purpose |
|------|--------|---------|
| `server/internal/gateway/types.go` | NEW | Wire types for all WS frames and Pulsar events |
| `server/internal/gateway/hub.go` | NEW | In-process connection registry (`Conn`, `Hub`, `writePump`) |
| `server/internal/store/routing.go` | NEW | Redis user→gateway routing (moved here for shared access) |
| `server/internal/gateway/handler.go` | NEW | WebSocket upgrade, JWT auth, readPump |
| `server/internal/gateway/heartbeat.go` | NEW | Periodic pong with channel seq diff |
| `server/internal/gateway/push_consumer.go` | NEW | Pulsar consumer → push to WebSocket, ACK registry |
| `server/internal/store/channel.go` | MODIFY | Add `GetMemberChannelSeqs` |
| `server/internal/config/config.go` | MODIFY | Add `GatewayConfig.ID`, `ResolveGatewayID()` |
| `server/cmd/gateway/main.go` | MODIFY | Wire Redis, Hub, Routing, WsHandler, Pulsar consumer, `/ws` route |
| `server/cmd/message/main.go` | MODIFY | Add `pushToMembers` fan-out, wire channelStore + routing |
| `client/src/app/core/websocket/websocket.models.ts` | NEW | TypeScript WS type definitions |
| `client/src/app/core/websocket/websocket.service.ts` | NEW | Connect, reconnect, ping, push ACK, observable streams |
| `client/src/app/core/messages/message.service.ts` | MODIFY | Subscribe pushMsg$, pong$; syncAllChannels on reconnect |
| `client/src/app/core/db/db.service.ts` | MODIFY | Add `updateChannelServerSeq`, `getMaxSeq` |

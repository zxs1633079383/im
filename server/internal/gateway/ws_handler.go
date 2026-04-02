package gateway

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"time"

	"github.com/gorilla/websocket"
	"im-server/internal/auth"
)

var upgrader = websocket.Upgrader{
	ReadBufferSize:  1024,
	WriteBufferSize: 4096,
	// Allow all origins for development; tighten in production.
	CheckOrigin: func(r *http.Request) bool { return true },
}

const (
	pongTimeout     = 45 * time.Second // server closes conn if no pong in this window
	maxMessageBytes = 64 * 1024        // 64 KB max inbound message
)

// WsHandler handles WebSocket upgrade requests.
type WsHandler struct {
	hub       *Hub
	routing   *Routing
	jwtSecret string
	gatewayID string
	channelSt ChannelSeqStore // to compute pong diff
	log       *slog.Logger
}

// NewWsHandler creates a WsHandler.
func NewWsHandler(hub *Hub, routing *Routing, jwtSecret, gatewayID string,
	channelSt ChannelSeqStore, log *slog.Logger) *WsHandler {
	return &WsHandler{
		hub:       hub,
		routing:   routing,
		jwtSecret: jwtSecret,
		gatewayID: gatewayID,
		channelSt: channelSt,
		log:       log,
	}
}

// ServeHTTP handles GET /ws?token=<jwt>&device=<device_id>.
// It validates the JWT, upgrades to WebSocket, registers the connection,
// runs the read pump inline, and cleans up on disconnect.
func (h *WsHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
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
	conn := NewConn(claims.UserID, deviceID, ws, h.hub)

	// Derive a cancellable context so the heartbeat exits on disconnect.
	ctx, cancel := context.WithCancel(r.Context())
	defer cancel()

	h.hub.Register(conn)
	if err := h.routing.Register(ctx, claims.UserID, deviceID); err != nil {
		h.log.Warn("redis register failed", "error", err, "user_id", claims.UserID)
	}

	h.log.Info("ws connected", "user_id", claims.UserID, "device_id", deviceID)

	// 5. Start heartbeat loop (sends pings, closes conn on timeout).
	go runHeartbeat(ctx, conn, h.channelSt, h.log)

	// 6. Read pump runs in this goroutine until disconnect.
	h.readPump(conn)

	// 7. Cleanup on disconnect.
	conn.Close()
	h.hub.Deregister(conn)
	bgCtx := context.Background()
	if err := h.routing.Deregister(bgCtx, conn.UserID, conn.DeviceID); err != nil {
		h.log.Warn("redis deregister failed", "error", err)
	}
	h.log.Info("ws disconnected", "user_id", conn.UserID, "device_id", conn.DeviceID)
}

// readPump reads inbound frames from the WebSocket and dispatches them.
// It blocks until the connection is closed or times out.
func (h *WsHandler) readPump(conn *Conn) {
	conn.ws.SetReadLimit(maxMessageBytes)
	conn.ws.SetReadDeadline(time.Now().Add(pongTimeout)) //nolint:errcheck

	for {
		_, data, err := conn.ws.ReadMessage()
		if err != nil {
			break // connection closed or timed out
		}
		// Reset read deadline on any inbound traffic.
		conn.ws.SetReadDeadline(time.Now().Add(pongTimeout)) //nolint:errcheck

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
			// Update known_seq from client's ping payload.
			var ping PingPayload
			if err := json.Unmarshal(frame.Payload, &ping); err == nil {
				for chIDStr, seq := range ping.ChannelSeqs {
					var chID int64
					fmt.Sscanf(chIDStr, "%d", &chID) //nolint:errcheck
					conn.UpdateKnownSeq(chID, seq)
				}
			}
			// Treat ping as liveness proof — refresh lastPong.
			conn.lastPong = time.Now()
		case TypePushACK:
			var ack PushACKPayload
			if err := json.Unmarshal(frame.Payload, &ack); err == nil {
				h.log.Debug("push_ack received", "push_id", ack.PushID)
				// Notify push consumer waiting on this push_id.
				globalACKRegistry.resolve(ack.PushID)
			}
		default:
			h.log.Debug("unhandled ws frame type", "type", frame.Type)
		}
	}
}

package gateway

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/gorilla/websocket"
	"github.com/lib/pq"
	"github.com/redis/go-redis/v9"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/trace"
	"im-server/internal/auth"
	"im-server/internal/middleware"
	"im-server/internal/repo"
)

// wsTracer is the OpenTelemetry tracer for WS frame dispatch spans.
// Each non-heartbeat frame handler opens a span rooted at this tracer.
var wsTracer = otel.Tracer("im-gateway/ws")

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

// WsSendStore is the subset of repo.MessageRepo needed for WS send.
type WsSendStore interface {
	Send(ctx context.Context, msg *repo.Message) error
}

// WsMemberLister lists channel members for push fan-out on WS send.
type WsMemberLister interface {
	ListMembers(ctx context.Context, channelID int64) ([]repo.ChannelMember, error)
	GetMember(ctx context.Context, channelID int64, userID string) (*repo.ChannelMember, error)
}

// WsHandler handles WebSocket upgrade requests.
type WsHandler struct {
	hub       *Hub
	routing   *Routing
	jwtSecret string
	gatewayID string
	channelSt ChannelSeqStore       // to compute pong diff
	msgStore  WsSendStore           // for WS send path (nil = WS send disabled)
	members   WsMemberLister        // for WS send push fan-out (nil = WS send disabled)
	rdb       redis.UniversalClient // for cookieId resolution; nil = JWT-only
	log       *slog.Logger
}

// NewWsHandler creates a WsHandler. rdb is optional — pass nil to keep
// JWT-only auth (legacy callers); pass the upstream cses Redis client to
// enable cookieId auth via either CookieId / cookieId header or
// ?cookie_id= query param. JWT remains accepted as a back-compat fallback.
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

// WithCookieAuth enables cookieId-based WS authentication using rdb as the
// upstream cses session store. Call after construction. JWT auth still
// works when set; cookie auth is tried first.
func (h *WsHandler) WithCookieAuth(rdb redis.UniversalClient) *WsHandler {
	h.rdb = rdb
	return h
}

// WithSendSupport enables the WS send path. Call after construction.
func (h *WsHandler) WithSendSupport(msgStore WsSendStore, members WsMemberLister) *WsHandler {
	h.msgStore = msgStore
	h.members = members
	return h
}

// ServeHTTP handles GET /ws.
//
// Auth precedence (first match wins):
//  1. CookieId / cookieId Header — message-v3 wire shape (cses parity)
//  2. cookie_id query param      — browser fallback (no custom header)
//  3. token query param          — JWT, kept for legacy clients
//
// Cookie auth resolves via middleware.ResolveCookieID so the LRU cache +
// im.auth.cookie_cache.{hit,miss,size} metrics cover WS connection bursts
// the same way they cover HTTP ones.
func (h *WsHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	userID, err := h.authenticate(r)
	if err != nil {
		http.Error(w, err.Error(), http.StatusUnauthorized)
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
	conn := NewConn(userID, deviceID, ws, h.hub)

	// Derive a cancellable context so the heartbeat exits on disconnect.
	ctx, cancel := context.WithCancel(r.Context())
	defer cancel()

	h.hub.Register(conn)
	if err := h.routing.Register(ctx, userID, deviceID); err != nil {
		h.log.Warn("redis register failed", "error", err, "user_id", userID)
	}

	h.log.Info("ws connected", "user_id", userID, "device_id", deviceID)

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

// authenticate resolves the connecting user's mm UserID from the upgrade
// request. Cookie auth (Header / query) is tried first; JWT is the legacy
// fall-through. Returns a 401-style error string on failure.
func (h *WsHandler) authenticate(r *http.Request) (string, error) {
	if cookieID := readCookieID(r); cookieID != "" && h.rdb != nil {
		mm, err := middleware.ResolveCookieID(r.Context(), h.rdb, cookieID, h.log)
		if err == nil {
			if uid := mm.ResolvedUserID(); uid != "" {
				return uid, nil
			}
		}
		// Cookie was supplied but resolution failed — refuse rather than
		// silently fall through to JWT, otherwise a stale cookie could
		// hide an upstream session timeout from the client.
		return "", fmt.Errorf("invalid cookieId")
	}
	tokenStr := r.URL.Query().Get("token")
	if tokenStr == "" {
		return "", fmt.Errorf("missing auth: cookieId header or ?token= required")
	}
	claims, err := auth.ValidateToken(h.jwtSecret, tokenStr)
	if err != nil {
		return "", fmt.Errorf("invalid token")
	}
	return claims.UserID, nil
}

// readCookieID returns the cookieId carried by either the upgrade Header
// (CookieId / cookieId — message-v3 sends the former) or a query param
// (?cookieId= per message-v3 wire shape, ?cookie_id= as snake_case
// alternative). Empty string when none is set.
func readCookieID(r *http.Request) string {
	for _, h := range []string{"CookieId", "cookieId", "Cookieid"} {
		if v := strings.TrimSpace(r.Header.Get(h)); v != "" {
			return v
		}
	}
	q := r.URL.Query()
	for _, k := range []string{"cookieId", "cookie_id"} {
		if v := strings.TrimSpace(q.Get(k)); v != "" {
			return v
		}
	}
	return ""
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
			// Refresh Redis presence TTL so a missed ping drops this conn from
			// routing within RoutingTTL. Run in background so the read pump
			// never blocks on Redis.
			if h.routing != nil {
				uid, devID := conn.UserID, conn.DeviceID
				gwID := h.gatewayID
				log := h.log
				go func() {
					refreshCtx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
					defer cancel()
					if err := h.routing.Refresh(refreshCtx, uid, gwID, devID); err != nil {
						log.Warn("routing refresh failed", "error", err, "user_id", uid)
					}
				}()
			}
		case TypePushACK:
			h.handlePushACK(conn, frame.Payload)
		case TypeSend:
			h.handleSend(conn, frame.Payload)
		default:
			h.log.Debug("unhandled ws frame type", "type", frame.Type)
		}
	}
}

// handlePushACK processes a TypePushACK frame: resolves any pending push waiter.
// Wrapped in an OTel span so client ACK delivery is observable end-to-end.
func (h *WsHandler) handlePushACK(conn *Conn, payload json.RawMessage) {
	_, span := wsTracer.Start(context.Background(), "ws.push_ack",
		trace.WithSpanKind(trace.SpanKindServer),
		trace.WithAttributes(
			attribute.String("user_id", conn.UserID),
			attribute.String("device_id", conn.DeviceID),
		))
	defer span.End()

	var ack PushACKPayload
	if err := json.Unmarshal(payload, &ack); err != nil {
		span.RecordError(err)
		return
	}
	span.SetAttributes(attribute.String("push_id", ack.PushID))
	h.log.Debug("push_ack received", "push_id", ack.PushID)
	// Notify push consumer waiting on this push_id.
	globalACKRegistry.resolve(ack.PushID)
}

// handleSend processes a TypeSend frame: persists the message and pushes to channel members.
// The handler opens an OTel span (root for this WS frame) so downstream DB and Pulsar
// operations are linked into a single trace.
func (h *WsHandler) handleSend(conn *Conn, payload json.RawMessage) {
	ctx, span := wsTracer.Start(context.Background(), "ws.send",
		trace.WithSpanKind(trace.SpanKindServer),
		trace.WithAttributes(
			attribute.String("user_id", conn.UserID),
			attribute.String("device_id", conn.DeviceID),
		))
	defer span.End()

	if h.msgStore == nil || h.members == nil {
		h.log.Debug("ws send not supported (no msgStore/members configured)")
		return
	}

	var sp SendPayload
	if err := json.Unmarshal(payload, &sp); err != nil {
		span.RecordError(err)
		h.log.Debug("malformed send payload", "error", err)
		return
	}
	if sp.ChannelID == 0 || sp.Content == "" {
		h.log.Debug("send payload missing channel_id or content")
		return
	}
	span.SetAttributes(
		attribute.Int64("channel_id", sp.ChannelID),
		attribute.String("client_msg_id", sp.ClientMsgID),
	)

	// Verify membership.
	if _, err := h.members.GetMember(ctx, sp.ChannelID, conn.UserID); err != nil {
		h.log.Debug("ws send: not a member", "channel_id", sp.ChannelID, "user_id", conn.UserID)
		return
	}

	msgType := sp.MsgType
	if msgType == 0 {
		msgType = repo.MsgTypeText
	}

	msg := &repo.Message{
		ChannelID:   sp.ChannelID,
		SenderID:    conn.UserID,
		ClientMsgID: sp.ClientMsgID,
		MsgType:     msgType,
		Content:     sp.Content,
		VisibleTo:   pq.StringArray(sp.VisibleTo),
	}

	if err := h.msgStore.Send(ctx, msg); err != nil {
		span.RecordError(err)
		h.log.Error("ws send: store failed", "error", err, "user_id", conn.UserID)
		return
	}
	span.SetAttributes(
		attribute.Int64("server_msg_id", msg.ID),
		attribute.Int64("seq", msg.Seq),
	)

	// Send ACK back to the sender.
	ack := SendACKPayload{
		ClientMsgID: sp.ClientMsgID,
		ServerMsgID: msg.ID,
		Seq:         msg.Seq,
		ChannelID:   msg.ChannelID,
	}
	conn.Push(TypeSendACK, ack)
	conn.UpdateKnownSeq(msg.ChannelID, msg.Seq)

	// Fan out push to all channel members. Use background ctx because the goroutine
	// outlives the span (we don't want the span to wait on the fan-out).
	go func() {
		members, err := h.members.ListMembers(context.Background(), msg.ChannelID)
		if err != nil {
			h.log.Error("ws send: list members failed", "error", err)
			return
		}
		for _, m := range members {
			pushMsg := msg
			if msg.VisibleTo != nil && !msg.IsVisibleTo(m.UserID) {
				pushMsg = &repo.Message{
					ChannelID: msg.ChannelID,
					Seq:       msg.Seq,
					MsgType:   repo.MsgTypePhantom,
					CreatedAt: msg.CreatedAt,
				}
			}
			pushPayload := PushMsgPayload{
				PushID:    fmt.Sprintf("ws-%d-%d", msg.ChannelID, msg.Seq),
				ChannelID: pushMsg.ChannelID,
				Seq:       pushMsg.Seq,
				ServerID:  pushMsg.ID,
				SenderID:  pushMsg.SenderID,
				Content:   pushMsg.Content,
				MsgType:   pushMsg.MsgType,
				VisibleTo: pushMsg.VisibleTo,
				CreatedAt: pushMsg.CreatedAt,
			}
			h.hub.PushToUser(m.UserID, TypePushMsg, pushPayload)
		}
	}()
}

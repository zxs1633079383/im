package gateway

import (
	"context"
	"time"
)

// ChannelSeqStore is the minimal interface needed to look up server-side channel seqs.
// Implemented by store.ChannelStore.
type ChannelSeqStore interface {
	// GetMemberChannelSeqs returns the current seq for each channel the user belongs to.
	// Returns map[channel_id]seq.
	GetMemberChannelSeqs(ctx context.Context, userID int64) (map[int64]int64, error)
}

// ---- Inbound (client → server) ----

// WSMessageType identifies the payload type of a WS frame.
type WSMessageType string

const (
	TypePing    WSMessageType = "ping"
	TypeSend    WSMessageType = "send"     // client sends a chat message
	TypePushACK WSMessageType = "push_ack" // client ACKs a pushed message
	TypeSync    WSMessageType = "sync"     // client sends channel state on reconnect
)

// ---- Outbound (server → client) ----

const (
	TypePong     WSMessageType = "pong"
	TypePushMsg  WSMessageType = "push_msg"  // server pushes a chat message
	TypeSendACK  WSMessageType = "send_ack"  // server ACKs client's send
	TypeSyncResp WSMessageType = "sync_resp" // server responds to sync
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
	PushID    string    `json:"push_id"`    // idempotency key for ACK
	ChannelID int64     `json:"channel_id"`
	Seq       int64     `json:"seq"`
	ServerID  int64     `json:"server_msg_id"`
	SenderID  int64     `json:"sender_id"`
	Content   string    `json:"content,omitempty"`
	MsgType   int16     `json:"msg_type"`             // 1=normal, 2=phantom
	VisibleTo []int64   `json:"visible_to,omitempty"`
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
	PushID    string  `json:"push_id"`    // unique per delivery attempt
	TargetUID int64   `json:"target_uid"` // user to receive this push
	ChannelID int64   `json:"channel_id"`
	Seq       int64   `json:"seq"`
	ServerID  int64   `json:"server_msg_id"`
	SenderID  int64   `json:"sender_id"`
	Content   string  `json:"content,omitempty"`
	MsgType   int16   `json:"msg_type"`
	VisibleTo []int64 `json:"visible_to,omitempty"`
	CreatedAt string  `json:"created_at"` // RFC3339
}

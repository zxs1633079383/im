package gateway

import (
	"context"
	"encoding/json"
	"time"

	"im-server/internal/repo"
)

// ChannelSeqStore is the minimal interface needed to look up server-side channel seqs.
// Implemented by store.ChannelStore.
//
// C012 P-D: channel_id key is now TEXT (string). Seq remains int64 — it is a
// monotonic counter per channel, not an entity ID.
type ChannelSeqStore interface {
	// GetMemberChannelSeqs returns the current seq for each channel the user belongs to.
	// Returns map[channel_id]seq. M4: userID is the mm UserID (24-hex string).
	GetMemberChannelSeqs(ctx context.Context, userID string) (map[string]int64, error)
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
	// TypeReadSync is pushed server→client when the same user marks read on another device.
	TypeReadSync WSMessageType = "read_sync"
	// TypeFriendEvent is pushed server→client for friend request/accept/reject events.
	TypeFriendEvent WSMessageType = "friend_event"
	// TypeChannelEvent is pushed server→client when a user is added to a channel.
	TypeChannelEvent WSMessageType = "channel_event"
	// TypeMsgUpdated is pushed server→client when a message is edited (M1 feature).
	TypeMsgUpdated WSMessageType = "msg_updated"
	// TypeMsgDeleted is pushed server→client when a message is revoked/soft-deleted (M1 feature).
	TypeMsgDeleted WSMessageType = "msg_deleted"
	// TypeAnnouncementPosted is pushed server→client when a new channel announcement is created (M2 feature).
	TypeAnnouncementPosted WSMessageType = "announcement_posted"
	// TypeUrgentPosted is pushed server→client when an urgent message is sent (M2 feature).
	TypeUrgentPosted WSMessageType = "urgent_posted"
	// TypeApprovalUpdated is pushed server→client on create/approve/reject/cancel (M2 feature).
	TypeApprovalUpdated WSMessageType = "approval_updated"
	// TypeNotificationReceived is pushed server→client when a new notification lands (M2 feature).
	TypeNotificationReceived WSMessageType = "notification_received"
	// TypeReactionAdded is pushed server→client when a user reacts to a message
	// with an emoji (v0.7.0 — replaces mattermost csesapi quickReply).
	TypeReactionAdded WSMessageType = "reaction_added"
	// TypeReactionRemoved is pushed server→client when a reaction is removed.
	TypeReactionRemoved WSMessageType = "reaction_removed"
	// TypeChannelTopUpdated is pushed server→client when caller pins / unpins
	// a channel to the top of their channel list (v0.7.0 — per-user state).
	TypeChannelTopUpdated WSMessageType = "channel_top_updated"
	// TypeChannelInfoUpdated is pushed to channel members when notice / purpose /
	// orient / permission changes (v0.7.0 — channel governance extras).
	TypeChannelInfoUpdated WSMessageType = "channel_info_updated"
	// TypeChannelClosed is pushed to every member of a channel when the owner
	// soft-deletes the channel via DELETE /api/channels/:id (v0.7.3 gap #1+#3).
	// Payload: ChannelClosedPayload.
	TypeChannelClosed WSMessageType = "channel_closed"
	// TypeChannelMemberUpdated is pushed to every member when the member roster
	// changes — add / remove / nickname (v0.7.3 gap #4 + #5). Payload carries
	// the full channel snapshot so clients can replace local channel state in
	// one pass instead of patching N fields. Same shape for join, leave, kick,
	// nickname rename — discriminator is `change_type` inside the payload.
	TypeChannelMemberUpdated WSMessageType = "channel_member_updated"
	// TypeScheduleCreated is pushed to the sender's other devices when they
	// create a scheduled message (v0.7.3 gap #7). Payload: ChannelSchedulePayload.
	TypeScheduleCreated WSMessageType = "schedule_created"
	// TypeScheduleCanceled is pushed to the sender's other devices when they
	// cancel a pending scheduled message (v0.7.3 gap #7).
	TypeScheduleCanceled WSMessageType = "schedule_canceled"
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

// NoticeType is the cses-client-facing discriminator carried at the top level
// of every push_msg frame. It mirrors the legacy mattermost wire shape so the
// existing cses-client Rust path (`data.get("type") == "NOTICE"` in
// `src-tauri/.../message_service.rs`) keeps working unchanged. Plain chat
// messages (text / image / file) leave it empty (omitempty drops the field);
// system messages (msg_type=4) carry "NOTICE" so the client can branch on it
// and decide whether to merge channel-level updates from props.sys_type.
type NoticeType string

// NoticeTypeNotice marks a frame as a system / NOTICE message — i.e. one whose
// props.sys_type drives channel-state derivation on the client side.
const NoticeTypeNotice NoticeType = "NOTICE"

// NoticeTypeForMsgType maps a repo.Message.MsgType to the wire-level
// NoticeType. Returning "" means "no notice classification" (regular chat);
// returning NoticeTypeNotice means "this is a system event the client must
// drain via props.sys_type". The mapping lives here so every push_msg
// builder (ws_handler / cmd/gateway / cmd/message) shares one rule.
func NoticeTypeForMsgType(msgType int16) NoticeType {
	if msgType == repo.MsgTypeSystem {
		return NoticeTypeNotice
	}
	return ""
}

// DerefStringPtr returns *p or "" when p is nil. Provided once at the gateway
// package level so every push_msg builder spells the optional Props copy the
// same way — keeps callers from inventing their own helper names.
func DerefStringPtr(p *string) string {
	if p == nil {
		return ""
	}
	return *p
}

// PushMsgPayload is sent server→client when a new message is available.
// M4: SenderID + VisibleTo are mm UserIDs (24-hex strings).
//
// C012 P-D: ChannelID + ServerID migrate to TEXT (string). Seq stays int64
// (per-channel monotonic counter, not an entity ID).
type PushMsgPayload struct {
	PushID    string     `json:"push_id"` // idempotency key for ACK
	Type      NoticeType `json:"type,omitempty"`
	ChannelID string     `json:"channel_id"`
	Seq       int64      `json:"seq"`
	ServerID  string     `json:"server_msg_id"`
	SenderID  string     `json:"sender_id"`
	Content   string     `json:"content,omitempty"`
	MsgType   int16      `json:"msg_type"` // 1=text/2=image/3=file/4=system/99=phantom
	VisibleTo []string   `json:"visible_to,omitempty"`
	Props     string     `json:"props,omitempty"` // raw JSONB; populated when MsgType=System
	CreatedAt time.Time  `json:"created_at"`
}

// PushACKPayload is the client's acknowledgement of a PushMsgPayload.
type PushACKPayload struct {
	PushID string `json:"push_id"`
}

// SendPayload is a client-initiated message send over WebSocket. M4:
// VisibleTo carries mm UserIDs as 24-hex strings.
//
// C012 P-D: ChannelID is now TEXT (string).
type SendPayload struct {
	ClientMsgID string   `json:"client_msg_id"`
	ChannelID   string   `json:"channel_id"`
	Content     string   `json:"content"`
	MsgType     int16    `json:"msg_type,omitempty"`
	VisibleTo   []string `json:"visible_to,omitempty"`
}

// SendACKPayload is the server's acknowledgement of a client send.
//
// C012 P-D: ServerMsgID + ChannelID migrate to string; Seq stays int64.
type SendACKPayload struct {
	ClientMsgID string `json:"client_msg_id"`
	ServerMsgID string `json:"server_msg_id"`
	Seq         int64  `json:"seq"`
	ChannelID   string `json:"channel_id"`
}

// SyncChannelState is one entry in a sync request.
//
// C012 P-D: channel id is TEXT (string).
type SyncChannelState struct {
	ID  string `json:"id"`
	Seq int64  `json:"seq"` // client's local max seq for this channel
}

// SyncPayload is sent on reconnect.
type SyncPayload struct {
	Channels []SyncChannelState `json:"channels"`
}

// ReadSyncPayload is pushed to the user's other devices when they mark a channel read.
type ReadSyncPayload struct {
	ChannelID string `json:"channel_id"`
	ReadSeq   int64  `json:"read_seq"` // the seq that was just marked as read
}

// FriendEventPayload is pushed to a user when a friend request/accept/reject occurs.
// M4: FromUserID is the mm UserID (24-hex string).
type FriendEventPayload struct {
	EventType  string `json:"event_type"`   // "request", "accepted", "rejected"
	FromUserID string `json:"from_user_id"` // the user who triggered the event
}

// ChannelEventPayload is pushed to a user when they are added to a channel.
type ChannelEventPayload struct {
	EventType string `json:"event_type"` // "added"
	ChannelID string `json:"channel_id"`
	Name      string `json:"name"`
}

// ChannelClosedPayload is pushed to every member when owner解散群聊.
// `deleted_at` is the RFC3339 timestamp that landed in channels.deleted_at —
// cses-client uses it as the `dialog.deleteAt` source-of-truth marker (see
// `channelConfigBase.service.ts` deleteAt > 0 branch).
type ChannelClosedPayload struct {
	ChannelID string    `json:"channel_id"`
	ActorID   string    `json:"actor_id"` // mm UserID of the owner who closed it
	DeletedAt time.Time `json:"deleted_at"`
}

// MemberChangeType discriminates ChannelMemberUpdatedPayload variants. cses-
// client routes the payload to add / remove / leave / kick / nickname branches
// based on this field. Constants spelt out so the wire grammar is stable.
type MemberChangeType string

const (
	// MemberChangeJoin: someone (caller or admin) added a new member.
	MemberChangeJoin MemberChangeType = "join"
	// MemberChangeLeave: a member voluntarily left.
	MemberChangeLeave MemberChangeType = "leave"
	// MemberChangeKick: an admin / owner removed a member.
	MemberChangeKick MemberChangeType = "kick"
	// MemberChangeNickname: a member updated their per-channel nickname.
	MemberChangeNickname MemberChangeType = "nickname"
)

// ChannelMemberUpdatedPayload carries the post-change channel snapshot plus
// the diff metadata (actor / target / change_type). cses-client overrides
// local dialog state with `payload.channel` and surfaces a UI notice based on
// `change_type`. NickName is filled only when ChangeType==Nickname.
type ChannelMemberUpdatedPayload struct {
	ChannelID  string           `json:"channel_id"`
	ChangeType MemberChangeType `json:"change_type"`
	ActorID    string           `json:"actor_id"`            // mm UserID of the actor
	TargetID   string           `json:"target_id"`           // mm UserID of the affected member
	NickName   string           `json:"nick_name,omitempty"` // only for change_type=nickname
	// Members is the full post-change roster snapshot so the client can
	// replace local membership in one pass (avoids re-fetching by channelId).
	Members []ChannelMemberSummary `json:"members"`
}

// ChannelMemberSummary is the minimal per-member projection bundled inside
// channel_member_updated. Keep it lean to bound payload size — clients fetch
// per-user profile (avatar, display name) from the cses Redis "User" hash.
type ChannelMemberSummary struct {
	UserID    string `json:"user_id"`
	Role      int16  `json:"role"`
	NickName  string `json:"nick_name,omitempty"`
	IsTop     bool   `json:"is_top,omitempty"`
	NotifyRef int16  `json:"notify_pref,omitempty"`
}

// ChannelSchedulePayload is pushed to the sender's other devices when a
// scheduled message is created / cancelled (v0.7.3 gap #7). cses-client uses
// the boolean to flip `dialog.hasSchedulePost` so the conversation badge stays
// in sync across devices.
type ChannelSchedulePayload struct {
	ChannelID       string `json:"channel_id"`
	ScheduledID     string `json:"scheduled_id"`
	HasSchedulePost bool   `json:"has_schedule_post"`
}

// PulsarPushEnvelope is the wire format for every cross-pod push event.
//
// Before the batch refactor the codebase had a push_msg-specific struct that
// silently dropped cross-pod sends because the app-level payload never
// included TargetUID. The envelope fixes both issues at once:
//
//   - `TargetUIDs` is carried at the envelope level so the receiving pod
//     always knows who to deliver to regardless of the inner payload shape.
//   - `MsgType` lets the consumer reconstruct the original WS frame type
//     (push_msg, read_sync, friend_event, msg_updated, ...) without any
//     hardcoded assumption.
//   - `Payload` stays opaque — producers pass whatever struct matches MsgType
//     and consumers hand the raw bytes back to the client's WS frame as-is.
//
// One Pulsar message can carry N UIDs so a broadcast to a group hosting many
// users on the same pod costs exactly one producer.Send per destination pod,
// not N.
type PulsarPushEnvelope struct {
	TargetUIDs []string        `json:"target_uids"` // mm UserIDs (24-hex), at least 1
	MsgType    WSMessageType   `json:"msg_type"`    // e.g. "push_msg", "read_sync"
	Payload    json.RawMessage `json:"payload"`     // app-level payload, opaque to the envelope
}

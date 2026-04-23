package service

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/lib/pq"

	"im-server/internal/repo"
)

// Message-service sentinels. Mapped by the HTTP transport to status codes:
//
//   - ErrNotMember (re-used from channel.go) → 403 ("not a member of this channel")
//   - ErrSourceNotMember                     → 403 ("not a member of the source channel")
//   - ErrSourceNotFound                      → 404 ("source message not found")
//
// Validation (zero IDs, missing content, target-channel cardinality) lives in
// the transport layer; the service enforces *semantic* rules — channel
// membership, attachment fan-out, push fan-out — and surfaces stable errors.
var (
	ErrSourceNotMember = errors.New("not a member of the source channel")
	ErrSourceNotFound  = errors.New("source message not found")
)

// MsgChannelStore is the subset of repo.ChannelRepo needed by MessageService.
// Defined locally (consumer-side) so the service file documents its surface,
// per Go's "accept small interfaces" idiom.
type MsgChannelStore interface {
	GetMember(ctx context.Context, channelID, userID int64) (*repo.ChannelMember, error)
	MarkRead(ctx context.Context, channelID, userID, seq int64) error
	GetByID(ctx context.Context, id int64) (*repo.Channel, error)
	ListMembers(ctx context.Context, channelID int64) ([]repo.ChannelMember, error)
}

// MsgAttachStore is the subset of repo.FileRepo needed for attaching files to
// messages on send. Optional — a nil files dependency disables linkage.
type MsgAttachStore interface {
	AttachToMessage(ctx context.Context, messageID, fileID int64) error
}

// SendParams is the input to MessageService.SendMessage. The transport layer
// constructs this from the HTTP body and the authenticated caller. Empty
// VisibleTo means broadcast; non-nil VisibleTo means directed (the repo bumps
// phantom_count for excluded members inside the same transaction).
type SendParams struct {
	ChannelID   int64
	SenderID    int64
	Content     string
	MsgType     int16
	ClientMsgID string
	VisibleTo   []int64
	ReplyTo     *int64
	FileIDs     []int64
}

// ForwardParams is the input to MessageService.ForwardMessages. The legacy
// handler accepted a single source message + multiple targets; preserve that
// shape so existing clients don't need to change.
type ForwardParams struct {
	MessageID        int64
	TargetChannelIDs []int64
}

// ForwardResult bundles the forwarded messages alongside any per-target skips
// (caller-not-a-member or repo errors) — the transport layer logs skips but
// returns the forwarded messages for the response body.
type ForwardResult struct {
	Forwarded []*repo.Message
}

// MessageService implements message send/fetch/read/forward on top of
// repo.MessageRepo, MsgChannelStore, and the optional MsgAttachStore.
//
// The push fan-out hook lives in the transport layer (the gateway hub adapter
// is constructed there); SendMessage returns the persisted message and the
// transport calls the hook. Same split keeps the service free of any
// gateway/Hub dependency.
type MessageService struct {
	messages repo.MessageRepo
	channels MsgChannelStore
	files    MsgAttachStore // optional; nil disables file attachment linkage
}

// NewMessageService wires the supplied repos. files may be nil to disable the
// attachment linkage (matches the legacy MessageHandler.WithAttachments hook).
func NewMessageService(messages repo.MessageRepo, channels MsgChannelStore, files MsgAttachStore) *MessageService {
	return &MessageService{messages: messages, channels: channels, files: files}
}

// SendMessage persists a new message in the channel after verifying the caller
// is a member. Returns ErrNotMember when the caller is not a member.
//
// Attachments: if FileIDs is non-empty AND a file repo is configured, each
// file is linked to the new message via AttachToMessage. Per-link failures
// match the legacy behaviour: log + continue (returned through the err arg
// only when *every* link failed is unnecessary — non-fatal).
func (s *MessageService) SendMessage(ctx context.Context, p SendParams) (*repo.Message, error) {
	if _, err := s.channels.GetMember(ctx, p.ChannelID, p.SenderID); err != nil {
		if errors.Is(err, repo.ErrNotFound) {
			return nil, ErrNotMember
		}
		return nil, fmt.Errorf("get member: %w", err)
	}

	msgType := p.MsgType
	if msgType == 0 {
		msgType = repo.MsgTypeText
	}

	msg := &repo.Message{
		ChannelID:   p.ChannelID,
		SenderID:    p.SenderID,
		ClientMsgID: p.ClientMsgID,
		MsgType:     msgType,
		Content:     p.Content,
		VisibleTo:   pq.Int64Array(p.VisibleTo),
		ReplyTo:     p.ReplyTo,
	}

	// Send delegates UPDATE channels SET seq=seq+1 RETURNING + INSERT messages
	// to repo.MessageRepo.AllocSeqAndInsert (the single primitive responsible
	// for seq monotonicity — see docs/BACKEND.md §4.1). Service layer must
	// NEVER run its own UPDATE channels SET seq = … statements.
	if err := s.messages.Send(ctx, msg); err != nil {
		return nil, fmt.Errorf("send message: %w", err)
	}

	if s.files != nil && len(p.FileIDs) > 0 {
		for _, fid := range p.FileIDs {
			// Per-link failures are non-fatal — preserve legacy log-and-skip.
			// The transport layer logs; the service silently swallows so the
			// caller still sees the message land successfully.
			_ = s.files.AttachToMessage(ctx, msg.ID, fid)
		}
	}

	return msg, nil
}

// FetchMessages returns up to limit messages with seq < beforeSeq for a member
// of channelID. Used for the "scroll up to load older" path — the transport
// layer maps this to GET ?before_seq=… and (with beforeSeq=MaxInt64) the
// default "latest N" path.
func (s *MessageService) FetchMessages(ctx context.Context, channelID, callerID, beforeSeq int64, limit int) ([]repo.Message, error) {
	if err := s.requireMember(ctx, channelID, callerID); err != nil {
		return nil, err
	}
	return s.messages.FetchBefore(ctx, channelID, callerID, beforeSeq, limit)
}

// FetchAfter returns up to limit messages with seq > afterSeq for a member of
// channelID. Used for the "catch-up since last sync" path.
func (s *MessageService) FetchAfter(ctx context.Context, channelID, callerID, afterSeq int64, limit int) ([]repo.Message, error) {
	if err := s.requireMember(ctx, channelID, callerID); err != nil {
		return nil, err
	}
	return s.messages.FetchForUser(ctx, channelID, callerID, afterSeq, limit)
}

// FetchAround returns up to limit messages centered on aroundSeq for a member
// of channelID. Used for the "jump to message + show context" path.
func (s *MessageService) FetchAround(ctx context.Context, channelID, callerID, aroundSeq int64, limit int) ([]repo.Message, error) {
	if err := s.requireMember(ctx, channelID, callerID); err != nil {
		return nil, err
	}
	return s.messages.FetchAround(ctx, channelID, callerID, aroundSeq, limit)
}

// MarkRead updates the caller's last_read_seq to the channel's current seq
// and returns the seq written so the transport can echo it. ErrNotMember when
// the caller isn't a member; repo.ErrNotFound when the channel is missing.
func (s *MessageService) MarkRead(ctx context.Context, channelID, callerID int64) (int64, error) {
	if err := s.requireMember(ctx, channelID, callerID); err != nil {
		return 0, err
	}
	ch, err := s.channels.GetByID(ctx, channelID)
	if err != nil {
		return 0, err // repo.ErrNotFound surfaces as 404
	}
	if err := s.channels.MarkRead(ctx, channelID, callerID, ch.Seq); err != nil {
		return 0, fmt.Errorf("mark read: %w", err)
	}
	return ch.Seq, nil
}

// ForwardMessages copies the source message into each target channel, provided
// the caller is a member of both source and target. Per-target failures are
// silently skipped (log-and-skip semantics from the legacy handler). Returns
// the list of newly created messages so the transport can both reply and
// (optionally) push them.
//
// Returns ErrSourceNotFound when the source message id is unknown, and
// ErrSourceNotMember when the caller is not a member of the source channel.
// Per-target "not a member" cases are silent skips (not errors).
func (s *MessageService) ForwardMessages(ctx context.Context, callerID int64, p ForwardParams) ([]*repo.Message, error) {
	source, err := s.messages.GetByID(ctx, p.MessageID)
	if err != nil {
		if errors.Is(err, repo.ErrNotFound) {
			return nil, ErrSourceNotFound
		}
		return nil, fmt.Errorf("get source: %w", err)
	}

	if _, err := s.channels.GetMember(ctx, source.ChannelID, callerID); err != nil {
		if errors.Is(err, repo.ErrNotFound) {
			return nil, ErrSourceNotMember
		}
		return nil, fmt.Errorf("get source member: %w", err)
	}

	forwarded := make([]*repo.Message, 0, len(p.TargetChannelIDs))
	for _, targetID := range p.TargetChannelIDs {
		// Skip target channels the caller is not a member of (silent skip,
		// matching the legacy handler).
		if _, err := s.channels.GetMember(ctx, targetID, callerID); err != nil {
			continue
		}

		fwd := &repo.Message{
			ChannelID:     targetID,
			SenderID:      callerID,
			MsgType:       source.MsgType,
			Content:       source.Content,
			ForwardedFrom: &source.ID,
		}
		if err := s.messages.Send(ctx, fwd); err != nil {
			// Non-fatal: continue with remaining targets.
			continue
		}
		forwarded = append(forwarded, fwd)
	}
	return forwarded, nil
}

// ListMembers returns every member of channelID. Used by the transport to
// drive the post-send push fan-out. Caller is NOT membership-checked here —
// the transport always calls SendMessage first (which does the check).
func (s *MessageService) ListMembers(ctx context.Context, channelID int64) ([]repo.ChannelMember, error) {
	return s.channels.ListMembers(ctx, channelID)
}

// FetchAroundTimestamp returns a window of messages for channelID centered on
// timestamp ts (unix millis). The window holds up to limit messages — half
// older (<=ts) and half newer (>ts) — ordered by seq ASC. hasOlder/hasNewer
// indicate whether more messages exist beyond the window bounds, so the
// client can decide whether to enable further pagination.
//
// Errors:
//   - ErrNotMember → 403 (caller not in channel)
func (s *MessageService) FetchAroundTimestamp(
	ctx context.Context,
	channelID, callerID int64,
	ts time.Time,
	limit int,
) ([]repo.Message, bool, bool, error) {
	if err := s.requireMember(ctx, channelID, callerID); err != nil {
		return nil, false, false, err
	}
	older, newer, err := s.messages.FetchAroundTimestamp(ctx, channelID, callerID, ts, limit)
	if err != nil {
		return nil, false, false, err
	}

	// hasOlder: did we fill the older half completely?
	halfLimit := limit / 2
	if halfLimit == 0 {
		halfLimit = 1
	}
	hasOlder := len(older) == halfLimit
	hasNewer := len(newer) == halfLimit

	combined := make([]repo.Message, 0, len(older)+len(newer))
	combined = append(combined, older...)
	combined = append(combined, newer...)
	return combined, hasOlder, hasNewer, nil
}

// GetReaders returns the user_ids of channel members who have read up to at
// least the seq of msgID. Pagination uses a cursor on user_id (pass 0 to
// start). Returns (readers, nextCursor, err).
//
// Errors:
//   - repo.ErrNotFound → 404 (message does not exist)
//   - ErrNotMember     → 403 (caller not in the message's channel)
func (s *MessageService) GetReaders(ctx context.Context, msgID, callerID int64, limit int, cursor int64) ([]int64, int64, error) {
	msg, err := s.messages.GetByID(ctx, msgID)
	if err != nil {
		return nil, 0, err
	}
	if err := s.requireMember(ctx, msg.ChannelID, callerID); err != nil {
		return nil, 0, err
	}
	return s.messages.GetReaders(ctx, msg.ChannelID, msg.Seq, cursor, limit)
}

// GetReplies returns every non-deleted reply to rootMsgID for a caller who is
// a member of the root message's channel. Results are ordered by seq ASC.
//
// Errors:
//   - repo.ErrNotFound → 404 (root message does not exist)
//   - ErrNotMember     → 403 (caller not in the root message's channel)
func (s *MessageService) GetReplies(ctx context.Context, rootMsgID, callerID int64) ([]repo.Message, error) {
	root, err := s.messages.GetByID(ctx, rootMsgID)
	if err != nil {
		return nil, err
	}
	if err := s.requireMember(ctx, root.ChannelID, callerID); err != nil {
		return nil, err
	}
	return s.messages.FetchReplies(ctx, rootMsgID, callerID)
}

// EditMessage updates the content of msgID on behalf of callerID. The caller
// MUST be the original sender and the message must not be soft-deleted.
// Returns the refreshed message so the transport can fan out a msg_updated
// event carrying the post-edit snapshot.
//
// Errors bubble up directly:
//   - repo.ErrNotFound  → 404
//   - repo.ErrForbidden → 403 (not the sender)
//   - repo.ErrGone      → 410 (already deleted — cannot edit a revoked msg)
func (s *MessageService) EditMessage(ctx context.Context, msgID, callerID int64, content string) (*repo.Message, error) {
	msg, err := s.messages.UpdateContent(ctx, msgID, callerID, content)
	if err != nil {
		return msg, err
	}
	return msg, nil
}

// DeleteMessage soft-deletes msgID on behalf of callerID. The caller MUST be
// the original sender — any other user is refused with repo.ErrForbidden.
// Returns the refreshed (soft-deleted) message so the transport can fan out
// a msg_deleted event to every channel member.
//
// Errors bubble up directly:
//   - repo.ErrNotFound  → 404
//   - repo.ErrForbidden → 403
//   - repo.ErrGone      → idempotent success (transport returns 200 but skips fan-out)
func (s *MessageService) DeleteMessage(ctx context.Context, msgID, callerID int64) (*repo.Message, error) {
	msg, err := s.messages.SoftDelete(ctx, msgID, callerID)
	if err != nil {
		return msg, err // pass raw sentinels through; HTTP layer maps status codes
	}
	return msg, nil
}

// requireMember returns ErrNotMember when callerID is not a member of
// channelID. Mirrors ChannelService.requireAdminOrOwner's shape.
func (s *MessageService) requireMember(ctx context.Context, channelID, callerID int64) error {
	if _, err := s.channels.GetMember(ctx, channelID, callerID); err != nil {
		if errors.Is(err, repo.ErrNotFound) {
			return ErrNotMember
		}
		return fmt.Errorf("get member: %w", err)
	}
	return nil
}

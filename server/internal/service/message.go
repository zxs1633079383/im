package service

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/lib/pq"

	"im-server/internal/repo"
)

// Message-service sentinels.
var (
	ErrSourceNotMember = errors.New("not a member of the source channel")
	ErrSourceNotFound  = errors.New("source message not found")
)

// MsgChannelStore is the subset of repo.ChannelRepo needed by MessageService.
// M4: user-id args are mm UserIDs (24-hex strings).
type MsgChannelStore interface {
	GetMember(ctx context.Context, channelID int64, userID string) (*repo.ChannelMember, error)
	MarkRead(ctx context.Context, channelID int64, userID string, seq int64) error
	GetByID(ctx context.Context, id int64) (*repo.Channel, error)
	ListMembers(ctx context.Context, channelID int64) ([]repo.ChannelMember, error)
}

// MsgAttachStore is the subset of repo.FileRepo needed for attaching files.
type MsgAttachStore interface {
	AttachToMessage(ctx context.Context, messageID, fileID int64) error
}

// SendParams is the input to MessageService.SendMessage.
//
// M4: SenderID is mm UserID; VisibleTo is []string of mm UserIDs; TeamID is
// the team scope frozen at send time (denormalised from channels.team_id).
type SendParams struct {
	ChannelID   int64
	SenderID    string
	TeamID      *string
	Content     string
	MsgType     int16
	ClientMsgID string
	VisibleTo   []string
	ReplyTo     *int64
	FileIDs     []int64
}

// ForwardParams is the input to MessageService.ForwardMessages.
type ForwardParams struct {
	MessageID        int64
	TargetChannelIDs []int64
}

// ForwardResult bundles the forwarded messages.
type ForwardResult struct {
	Forwarded []*repo.Message
}

// MessageService implements message send/fetch/read/forward.
type MessageService struct {
	messages repo.MessageRepo
	channels MsgChannelStore
	files    MsgAttachStore
}

// NewMessageService wires the supplied repos.
func NewMessageService(messages repo.MessageRepo, channels MsgChannelStore, files MsgAttachStore) *MessageService {
	return &MessageService{messages: messages, channels: channels, files: files}
}

// SendMessage persists a new message after verifying the caller is a member.
// teamID — when non-nil — is denormalised onto the message row so the row
// freezes the sender's team scope at send time.
func (s *MessageService) SendMessage(ctx context.Context, p SendParams) (*repo.Message, error) {
	ctx, span := tracer.Start(ctx, "MessageService.SendMessage")
	defer span.End()

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

	teamID := p.TeamID
	if teamID == nil {
		// Fall back to the channel's team_id if the caller did not supply one
		// (typical handler path: handler reads the cookie team and passes it
		// through; tests sometimes leave it nil).
		if ch, err := s.channels.GetByID(ctx, p.ChannelID); err == nil && ch != nil {
			teamID = ch.TeamID
		}
	}

	msg := &repo.Message{
		ChannelID:   p.ChannelID,
		SenderID:    p.SenderID,
		TeamID:      teamID,
		ClientMsgID: p.ClientMsgID,
		MsgType:     msgType,
		Content:     p.Content,
		VisibleTo:   pq.StringArray(p.VisibleTo),
		ReplyTo:     p.ReplyTo,
	}

	if err := s.messages.Send(ctx, msg); err != nil {
		return nil, fmt.Errorf("send message: %w", err)
	}

	if s.files != nil && len(p.FileIDs) > 0 {
		for _, fid := range p.FileIDs {
			_ = s.files.AttachToMessage(ctx, msg.ID, fid)
		}
	}

	return msg, nil
}

// FetchMessages returns up to limit messages with seq < beforeSeq.
func (s *MessageService) FetchMessages(ctx context.Context, channelID int64, callerID string, beforeSeq int64, limit int) ([]repo.Message, error) {
	ctx, span := tracer.Start(ctx, "MessageService.FetchMessages")
	defer span.End()

	if err := s.requireMember(ctx, channelID, callerID); err != nil {
		return nil, err
	}
	return s.messages.FetchBefore(ctx, channelID, callerID, beforeSeq, limit)
}

// FetchAfter returns up to limit messages with seq > afterSeq.
func (s *MessageService) FetchAfter(ctx context.Context, channelID int64, callerID string, afterSeq int64, limit int) ([]repo.Message, error) {
	ctx, span := tracer.Start(ctx, "MessageService.FetchAfter")
	defer span.End()

	if err := s.requireMember(ctx, channelID, callerID); err != nil {
		return nil, err
	}
	return s.messages.FetchForUser(ctx, channelID, callerID, afterSeq, limit)
}

// FetchAround returns up to limit messages centered on aroundSeq.
func (s *MessageService) FetchAround(ctx context.Context, channelID int64, callerID string, aroundSeq int64, limit int) ([]repo.Message, error) {
	ctx, span := tracer.Start(ctx, "MessageService.FetchAround")
	defer span.End()

	if err := s.requireMember(ctx, channelID, callerID); err != nil {
		return nil, err
	}
	return s.messages.FetchAround(ctx, channelID, callerID, aroundSeq, limit)
}

// MarkRead updates the caller's last_read_seq to the channel's current seq.
func (s *MessageService) MarkRead(ctx context.Context, channelID int64, callerID string) (int64, error) {
	ctx, span := tracer.Start(ctx, "MessageService.MarkRead")
	defer span.End()

	if err := s.requireMember(ctx, channelID, callerID); err != nil {
		return 0, err
	}
	ch, err := s.channels.GetByID(ctx, channelID)
	if err != nil {
		return 0, err
	}
	if err := s.channels.MarkRead(ctx, channelID, callerID, ch.Seq); err != nil {
		return 0, fmt.Errorf("mark read: %w", err)
	}
	return ch.Seq, nil
}

// BatchSendParams is the input to MessageService.BatchSendMessages — same
// content fan out to N channels. Mirrors mattermost csesapi createPosts
// (Posts + ChannelIds) but with simpler body shape (the same Content +
// MsgType across all channels).
type BatchSendParams struct {
	ChannelIDs  []int64
	SenderID    string
	TeamID      *string
	Content     string
	MsgType     int16
	ClientMsgID string // applied as-is to each row; clients pass distinct values per channel if needed
}

// BatchSendMessages persists one new message per target channel where the
// caller is a member. Targets where the caller is NOT a member are skipped
// silently — same defensive shape as ForwardMessages so partial broadcast
// degrades to per-channel best-effort. Returns the messages that succeeded
// in send order; caller can compare len(returned) vs len(ChannelIDs) to
// decide if a retry is required.
func (s *MessageService) BatchSendMessages(ctx context.Context, p BatchSendParams) ([]*repo.Message, error) {
	ctx, span := tracer.Start(ctx, "MessageService.BatchSendMessages")
	defer span.End()

	if len(p.ChannelIDs) == 0 || p.Content == "" {
		return nil, fmt.Errorf("batch send: channel_ids and content required")
	}
	msgType := p.MsgType
	if msgType == 0 {
		msgType = repo.MsgTypeText
	}
	out := make([]*repo.Message, 0, len(p.ChannelIDs))
	for _, channelID := range p.ChannelIDs {
		if _, err := s.channels.GetMember(ctx, channelID, p.SenderID); err != nil {
			continue // skip non-member channels silently
		}
		teamID := p.TeamID
		if teamID == nil {
			if ch, err := s.channels.GetByID(ctx, channelID); err == nil && ch != nil {
				teamID = ch.TeamID
			}
		}
		msg := &repo.Message{
			ChannelID:   channelID,
			SenderID:    p.SenderID,
			TeamID:      teamID,
			MsgType:     msgType,
			Content:     p.Content,
			ClientMsgID: p.ClientMsgID,
		}
		if err := s.messages.Send(ctx, msg); err != nil {
			continue
		}
		out = append(out, msg)
	}
	return out, nil
}

// MessagesAfter returns messages with seq strictly greater than the given
// message's seq, in the same channel, up to limit. Composes
// MessageRepo.GetByID + FetchForUser so the caller can pass a known
// message id without first looking up its seq client-side. Maps
// repo.ErrNotFound to ErrSourceNotFound; non-member to ErrSourceNotMember
// so the HTTP layer can surface 404 / 403 cleanly. Replaces the mattermost
// csesapi /posts/getPostsAfterFromSegment wire shape.
func (s *MessageService) MessagesAfter(ctx context.Context, messageID int64, callerID string, limit int) ([]repo.Message, error) {
	ctx, span := tracer.Start(ctx, "MessageService.MessagesAfter")
	defer span.End()

	src, err := s.messages.GetByID(ctx, messageID)
	switch {
	case errors.Is(err, repo.ErrNotFound):
		return nil, ErrSourceNotFound
	case err != nil:
		return nil, fmt.Errorf("get source: %w", err)
	}
	if _, err := s.channels.GetMember(ctx, src.ChannelID, callerID); err != nil {
		if errors.Is(err, repo.ErrNotFound) {
			return nil, ErrSourceNotMember
		}
		return nil, fmt.Errorf("get member: %w", err)
	}
	if limit <= 0 || limit > 200 {
		limit = 50
	}
	return s.messages.FetchForUser(ctx, src.ChannelID, callerID, src.Seq, limit)
}

// ForwardMessages copies the source message into each target channel.
func (s *MessageService) ForwardMessages(ctx context.Context, callerID string, p ForwardParams) ([]*repo.Message, error) {
	ctx, span := tracer.Start(ctx, "MessageService.ForwardMessages")
	defer span.End()

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
		if _, err := s.channels.GetMember(ctx, targetID, callerID); err != nil {
			continue
		}
		// Inherit team_id from the target channel (per-target denormalisation).
		var teamID *string
		if ch, err := s.channels.GetByID(ctx, targetID); err == nil && ch != nil {
			teamID = ch.TeamID
		}
		fwd := &repo.Message{
			ChannelID:     targetID,
			SenderID:      callerID,
			TeamID:        teamID,
			MsgType:       source.MsgType,
			Content:       source.Content,
			ForwardedFrom: &source.ID,
		}
		if err := s.messages.Send(ctx, fwd); err != nil {
			continue
		}
		forwarded = append(forwarded, fwd)
	}
	return forwarded, nil
}

// ListMembers returns every member of channelID.
func (s *MessageService) ListMembers(ctx context.Context, channelID int64) ([]repo.ChannelMember, error) {
	return s.channels.ListMembers(ctx, channelID)
}

// FetchAroundTimestamp returns a window of messages centered on ts.
func (s *MessageService) FetchAroundTimestamp(
	ctx context.Context,
	channelID int64, callerID string,
	ts time.Time,
	limit int,
) ([]repo.Message, bool, bool, error) {
	ctx, span := tracer.Start(ctx, "MessageService.FetchAroundTimestamp")
	defer span.End()

	if err := s.requireMember(ctx, channelID, callerID); err != nil {
		return nil, false, false, err
	}
	older, newer, err := s.messages.FetchAroundTimestamp(ctx, channelID, callerID, ts, limit)
	if err != nil {
		return nil, false, false, err
	}

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

// GetReaders returns mm UserIDs of channel members read up to msgID's seq.
func (s *MessageService) GetReaders(ctx context.Context, msgID int64, callerID string, limit int, cursor string) ([]string, string, error) {
	ctx, span := tracer.Start(ctx, "MessageService.GetReaders")
	defer span.End()

	msg, err := s.messages.GetByID(ctx, msgID)
	if err != nil {
		return nil, "", err
	}
	if err := s.requireMember(ctx, msg.ChannelID, callerID); err != nil {
		return nil, "", err
	}
	return s.messages.GetReaders(ctx, msg.ChannelID, msg.Seq, cursor, limit)
}

// GetReplies returns every non-deleted reply to rootMsgID.
func (s *MessageService) GetReplies(ctx context.Context, rootMsgID int64, callerID string) ([]repo.Message, error) {
	ctx, span := tracer.Start(ctx, "MessageService.GetReplies")
	defer span.End()

	root, err := s.messages.GetByID(ctx, rootMsgID)
	if err != nil {
		return nil, err
	}
	if err := s.requireMember(ctx, root.ChannelID, callerID); err != nil {
		return nil, err
	}
	return s.messages.FetchReplies(ctx, rootMsgID, callerID)
}

// EditMessage updates the content of msgID. Caller must be the sender.
func (s *MessageService) EditMessage(ctx context.Context, msgID int64, callerID, content string) (*repo.Message, error) {
	ctx, span := tracer.Start(ctx, "MessageService.EditMessage")
	defer span.End()

	msg, err := s.messages.UpdateContent(ctx, msgID, callerID, content)
	if err != nil {
		return msg, err
	}
	return msg, nil
}

// DeleteMessage soft-deletes msgID. Caller must be the sender.
func (s *MessageService) DeleteMessage(ctx context.Context, msgID int64, callerID string) (*repo.Message, error) {
	ctx, span := tracer.Start(ctx, "MessageService.DeleteMessage")
	defer span.End()

	msg, err := s.messages.SoftDelete(ctx, msgID, callerID)
	if err != nil {
		return msg, err
	}
	return msg, nil
}

func (s *MessageService) requireMember(ctx context.Context, channelID int64, callerID string) error {
	if _, err := s.channels.GetMember(ctx, channelID, callerID); err != nil {
		if errors.Is(err, repo.ErrNotFound) {
			return ErrNotMember
		}
		return fmt.Errorf("get member: %w", err)
	}
	return nil
}

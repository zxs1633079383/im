package service

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"
	"unicode/utf8"

	"im-server/internal/repo"
)

// ReactionService implements add / remove / list semantics on top of
// repo.ReactionRepo + a membership check via MsgChannelStore (caller must be
// a member of the message's channel before reacting). MessageStore is just
// repo.MessageRepo subset for fetching the parent message.
type ReactionService struct {
	reactions repo.ReactionRepo
	messages  ReactionMessageStore
	channels  MsgChannelStore
}

// ReactionMessageStore is the subset of repo.MessageRepo needed to look up
// the parent message — only used to read its ChannelID for the membership
// gate. Stays small so unit tests can stub it.
type ReactionMessageStore interface {
	GetByID(ctx context.Context, id string) (*repo.Message, error)
}

// NewReactionService wires the deps.
func NewReactionService(reactions repo.ReactionRepo, messages ReactionMessageStore, channels MsgChannelStore) *ReactionService {
	return &ReactionService{reactions: reactions, messages: messages, channels: channels}
}

// maxEmojiBytes caps how many bytes an emoji shortname / unicode literal can
// have. 64 is generous (longest mattermost shortname is :slightly_smiling_face:
// at 26 bytes incl. colons). Stops accidental abuse via huge body.
const maxEmojiBytes = 64

// Add validates the caller can react (member of the target channel) and
// upserts the (message, user, emoji) triple. Returns the parent channel id
// so the handler can stamp it onto the WS broadcast payload without a
// second DB round-trip.
func (s *ReactionService) Add(ctx context.Context, messageID string, userID, emoji string) (channelID string, err error) {
	emoji = strings.TrimSpace(emoji)
	if emoji == "" || len(emoji) > maxEmojiBytes || !utf8.ValidString(emoji) {
		return "", fmt.Errorf("invalid emoji")
	}
	channelID, err = s.requireChannelMember(ctx, messageID, userID)
	if err != nil {
		return "", err
	}
	react := &repo.MessageReaction{
		MessageID: messageID,
		UserID:    userID,
		Emoji:     emoji,
		CreatedAt: time.Now(),
	}
	if err := s.reactions.Add(ctx, react); err != nil {
		return "", err
	}
	return channelID, nil
}

// Remove deletes a reaction. ErrNotFound bubbles when the row didn't exist
// — handler maps that to 404 so the client can re-sync. Channel id is
// returned for the broadcast hook.
func (s *ReactionService) Remove(ctx context.Context, messageID string, userID, emoji string) (channelID string, err error) {
	emoji = strings.TrimSpace(emoji)
	if emoji == "" {
		return "", fmt.Errorf("invalid emoji")
	}
	channelID, err = s.requireChannelMember(ctx, messageID, userID)
	if err != nil {
		return "", err
	}
	if err := s.reactions.Remove(ctx, messageID, userID, emoji); err != nil {
		return "", err
	}
	return channelID, nil
}

// List returns reactions for a single message after enforcing caller
// membership. Reads only — safe to cache aggressively in front of the DB.
func (s *ReactionService) List(ctx context.Context, messageID string, callerID string) ([]repo.MessageReaction, error) {
	if _, err := s.requireChannelMember(ctx, messageID, callerID); err != nil {
		return nil, err
	}
	return s.reactions.List(ctx, messageID)
}

// requireChannelMember resolves the parent message to find its channel,
// then asserts the caller is a member. Returns the channel id on success.
// Maps repo.ErrNotFound on either lookup to ErrSourceNotFound /
// ErrSourceNotMember so the HTTP layer can map 404 / 403 cleanly.
func (s *ReactionService) requireChannelMember(ctx context.Context, messageID string, userID string) (string, error) {
	msg, err := s.messages.GetByID(ctx, messageID)
	switch {
	case errors.Is(err, repo.ErrNotFound):
		return "", ErrSourceNotFound
	case err != nil:
		return "", fmt.Errorf("reaction get message: %w", err)
	}
	if _, err := s.channels.GetMember(ctx, msg.ChannelID, userID); err != nil {
		if errors.Is(err, repo.ErrNotFound) {
			return "", ErrSourceNotMember
		}
		return "", fmt.Errorf("reaction get member: %w", err)
	}
	return msg.ChannelID, nil
}

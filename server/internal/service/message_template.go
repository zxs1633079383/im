package service

import (
	"context"
	"errors"
	"fmt"

	"im-server/internal/repo"
)

// MarkTemplateReceived appends callerID to the target template message's
// props.template.userIds list, then returns the refreshed message so the
// transport layer can broadcast the change via msg_updated.
//
// The flow is read-modify-write:
//
//  1. Load the message and verify the caller is a member of its channel
//     (prevents external poking).
//  2. Append callerID to props.template.userIds via repo.AppendTemplateReceiver
//     (which deduplicates).
//  3. Persist via repo.UpdateMessageProps. If the receipt was already present
//     (idempotent re-click), step 2 short-circuits and we return the existing
//     message without touching the DB or signalling a change.
//
// Returns:
//   - (*repo.Message, false, nil) when the click was a no-op (already received).
//     Callers should skip the WS broadcast in this case.
//   - (*repo.Message, true, nil) when the receipt was newly recorded and the
//     caller should fan out msg_updated.
//   - sentinels: ErrNotMember (not a channel member), repo.ErrNotFound,
//     repo.ErrGone (already deleted), repo.ErrInvalidTemplate (not a template
//     message — the props.template payload is missing).
func (s *MessageService) MarkTemplateReceived(
	ctx context.Context,
	msgID int64,
	callerID string,
) (*repo.Message, bool, error) {
	ctx, span := tracer.Start(ctx, "MessageService.MarkTemplateReceived")
	defer span.End()

	existing, err := s.messages.GetByID(ctx, msgID)
	if err != nil {
		return nil, false, err
	}
	if existing.Deleted {
		return existing, false, repo.ErrGone
	}
	if err := s.requireMember(ctx, existing.ChannelID, callerID); err != nil {
		return nil, false, err
	}

	newProps, changed, err := repo.AppendTemplateReceiver(existing.Props, callerID)
	if err != nil {
		return nil, false, fmt.Errorf("append template receiver: %w", err)
	}
	if !changed {
		// Idempotent re-click — the caller is already in the receipts list.
		return existing, false, nil
	}

	updated, err := s.messages.UpdateMessageProps(ctx, msgID, newProps)
	if err != nil {
		if errors.Is(err, repo.ErrGone) || errors.Is(err, repo.ErrNotFound) {
			return updated, false, err
		}
		return nil, false, fmt.Errorf("update message props: %w", err)
	}
	return updated, true, nil
}

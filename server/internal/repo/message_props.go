package repo

import (
	"context"
	"encoding/json"
	"fmt"
	"time"
)

// UpdateMessageProps overwrites messages.props with newProps (a JSON-encoded
// object) and bumps updated_at to now. Used by template-received and other
// callers that mutate the props payload after read-modify-write in the
// service layer.
//
// Returns the refreshed *Message. Errors:
//   - ErrNotFound when the message does not exist.
//   - ErrGone when the message is already soft-deleted (the caller's stale
//     props would no-op anyway; signal this so the transport layer can skip
//     fan-out).
//
// Concurrency note: this is a plain UPDATE — read-modify-write race is
// possible if two clients touch props simultaneously (e.g. two template
// receipts in the same millisecond). The lossy outcome is "one userId is
// dropped from the receipts list", which is acceptable for the template-
// receipt use case (low frequency, idempotent re-click recovers). Callers
// requiring strict consistency should drive the merge with a SQL-level
// jsonb_set + atomic predicate instead.
func (r *gormMessageRepo) UpdateMessageProps(
	ctx context.Context,
	msgID string,
	newProps string,
) (*Message, error) {
	ctx, span := tracer.Start(ctx, "MessageRepo.UpdateMessageProps")
	defer span.End()

	existing, err := r.GetByID(ctx, msgID)
	if err != nil {
		return nil, err
	}
	if existing.Deleted {
		return existing, ErrGone
	}

	now := time.Now().UTC()
	res := r.db.WithContext(ctx).
		Model(&Message{}).
		Where("id = ? AND deleted = FALSE", msgID).
		Updates(map[string]any{"props": newProps, "updated_at": now})
	if res.Error != nil {
		return nil, fmt.Errorf("update message props: %w", res.Error)
	}
	if res.RowsAffected == 0 {
		// Lost the race with a concurrent SoftDelete.
		existing.Deleted = true
		return existing, ErrGone
	}

	existing.Props = &newProps
	existing.UpdatedAt = &now
	return existing, nil
}

// AppendTemplateReceiver returns the new props JSON with userID appended to
// props.template.userIds (de-duplicated). Returns the original props string
// and (false, nil) when userID was already present, signalling the caller to
// skip the UPDATE (and the WS broadcast) for an idempotent re-click.
//
// Returns ErrInvalidTemplate when props is missing/null or does not contain
// a template object.
func AppendTemplateReceiver(currentProps *string, userID string) (string, bool, error) {
	if currentProps == nil || *currentProps == "" {
		return "", false, ErrInvalidTemplate
	}
	var props map[string]any
	if err := json.Unmarshal([]byte(*currentProps), &props); err != nil {
		return "", false, fmt.Errorf("parse props: %w", err)
	}
	template, ok := props["template"].(map[string]any)
	if !ok {
		return "", false, ErrInvalidTemplate
	}

	rawIDs, _ := template["userIds"].([]any)
	userIDs := make([]string, 0, len(rawIDs)+1)
	for _, v := range rawIDs {
		s, ok := v.(string)
		if !ok {
			continue
		}
		if s == userID {
			return *currentProps, false, nil
		}
		userIDs = append(userIDs, s)
	}
	userIDs = append(userIDs, userID)
	template["userIds"] = userIDs
	props["template"] = template

	out, err := json.Marshal(props)
	if err != nil {
		return "", false, fmt.Errorf("marshal props: %w", err)
	}
	return string(out), true, nil
}

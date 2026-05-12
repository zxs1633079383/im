package service

import (
	"context"
	"fmt"

	"im-server/internal/repo"
)

// replyBranchMaxLimit caps pagination so a single page can't exhaust memory
// even when callers ignore the documented ceiling. Mirrors the 200 cap used
// elsewhere in MessageService (see MessagesAfter).
const replyBranchMaxLimit = 200

// GetRepliesBranch returns a page of replies to rootMsgID ordered by seq ASC.
// The caller must be a channel member; the response carries `has_more=true`
// when len(messages) == limit, signalling the cses-client to fire the next
// page. (v0.7.3 gap #2 — replaces mattermost csesapi /posts/getReplyBranch.)
//
// offset / limit are zero-safe — negative input is normalised in the http
// layer; here we only guard the upper bound.
func (s *MessageService) GetRepliesBranch(
	ctx context.Context,
	rootMsgID int64,
	callerID string,
	offset, limit int,
) ([]repo.Message, bool, error) {
	ctx, span := tracer.Start(ctx, "MessageService.GetRepliesBranch")
	defer span.End()

	if limit <= 0 {
		limit = 50
	}
	if limit > replyBranchMaxLimit {
		limit = replyBranchMaxLimit
	}
	root, err := s.messages.GetByID(ctx, rootMsgID)
	if err != nil {
		return nil, false, err
	}
	if err := s.requireMember(ctx, root.ChannelID, callerID); err != nil {
		return nil, false, err
	}
	// Window: FetchReplies returns the full list ordered by seq ASC. We pull
	// limit+1 rows starting at offset so we can detect `has_more` without a
	// second COUNT(*). Repo-level page-aware variant lives in
	// repo/message_v073.go to keep the SQL local.
	rows, err := s.fetchReplyPage(ctx, rootMsgID, callerID, offset, limit+1)
	if err != nil {
		return nil, false, err
	}
	hasMore := len(rows) > limit
	if hasMore {
		rows = rows[:limit]
	}
	return rows, hasMore, nil
}

// fetchReplyPage delegates to the repo helper. Lives in service to keep the
// requireMember check + memberRoot resolution inside the service boundary.
func (s *MessageService) fetchReplyPage(
	ctx context.Context, rootID int64, userID string, offset, limit int,
) ([]repo.Message, error) {
	rows, err := s.messages.FetchRepliesPage(ctx, rootID, userID, offset, limit)
	if err != nil {
		return nil, fmt.Errorf("fetch reply page: %w", err)
	}
	return rows, nil
}

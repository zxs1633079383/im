package service

import (
	"context"
	"errors"
	"fmt"

	"im-server/internal/repo"
)

// MaxReadStatsBatch caps how many message IDs a single read-stats request may
// contain. Picked to bound response size + DB work; the cses-client side will
// chunk larger requests. Mirrored in the HTTP handler for early rejection.
const MaxReadStatsBatch = 100

// ErrTooManyReadStats is returned when a caller asks for more than
// MaxReadStatsBatch messages in one request.
var ErrTooManyReadStats = errors.New("too many message ids; max 100")

// GetReadStatsBatch returns per-message read summaries scoped to channels
// callerID is a member of. The repo layer filters by membership in SQL, so
// non-member messages are silently dropped from the result rather than
// surfacing as 403 errors — matches the cses-client UI which calls this on
// a heterogenous list of visible messages and tolerates a sparse response.
func (s *MessageService) GetReadStatsBatch(
	ctx context.Context,
	callerID string,
	msgIDs []int64,
) ([]repo.ReadStat, error) {
	ctx, span := tracer.Start(ctx, "MessageService.GetReadStatsBatch")
	defer span.End()

	if len(msgIDs) > MaxReadStatsBatch {
		return nil, ErrTooManyReadStats
	}
	stats, err := s.messages.GetReadStatsBatch(ctx, callerID, msgIDs)
	if err != nil {
		return nil, fmt.Errorf("get read stats batch: %w", err)
	}
	return stats, nil
}

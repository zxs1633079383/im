package service

import (
	"context"
	"fmt"

	"im-server/internal/repo"
)

// Search tunables — preserved verbatim from the legacy SearchHandler so the
// per-type result budget is identical after the cut-over.
const (
	// SearchDefaultLimit is the per-type result count when the caller omits
	// (or supplies an invalid) limit query parameter.
	SearchDefaultLimit = 20
	// SearchMaxLimit caps the per-type result count requested by callers.
	SearchMaxLimit = 50
)

// SearchStore is the subset of repo.SearchRepo SearchService consumes.
// Defined consumer-side (Go's "accept small interfaces" idiom) so the service
// surface is documented at the call site. The production binding is
// repo.SearchRepo — one repo backing all three lookups.
type SearchStore interface {
	SearchMessages(ctx context.Context, q string, userID, channelID int64, limit int) ([]repo.MessageSearchResult, error)
	SearchUsers(ctx context.Context, q string, callerID int64, limit int) ([]repo.User, error)
	SearchChannels(ctx context.Context, q string, callerID int64, limit int) ([]repo.Channel, error)
}

// Search "type" filter values. Empty string means "all three" — match the
// legacy handler exactly so existing clients continue to work after the
// cut-over.
const (
	SearchTypeAll      = ""
	SearchTypeMessages = "messages"
	SearchTypeUsers    = "users"
	SearchTypeChannels = "channels"
)

// SearchParams is the input to SearchService.Search. The transport layer is
// responsible for query-string parsing; the service consumes typed values.
//
// Type "" means search every category. ChannelID > 0 restricts the messages
// search to that channel — ignored for users/channels lookups (legacy parity).
type SearchParams struct {
	Query     string
	Type      string
	ChannelID int64
	Limit     int
}

// SearchResult bundles the per-type lookup outcomes. A nil slice means "the
// caller did not request this category" — distinct from "requested but no
// matches" (empty non-nil slice). The transport layer relies on this
// distinction to decide whether a key is emitted (`omitempty`-ish) in the
// JSON envelope, matching the legacy SearchHandler wire format.
type SearchResult struct {
	Messages []repo.MessageSearchResult
	Users    []repo.User
	Channels []repo.Channel
}

// SearchService runs the multi-type search algorithm on top of SearchStore.
// The fan-out + per-type budgeting is preserved verbatim from the legacy
// handler.SearchHandler — see Search below for the decision matrix.
type SearchService struct {
	store SearchStore
}

// NewSearchService wires the supplied store. Production passes
// repo.SearchRepo, which satisfies SearchStore by construction.
func NewSearchService(store SearchStore) *SearchService {
	return &SearchService{store: store}
}

// Search runs the three lookups gated by p.Type and returns the per-type
// results.
//
// Algorithm (preserved from the legacy handler):
//  1. Validate the query string (non-empty after the transport's Get).
//  2. Clamp limit to [1, SearchMaxLimit]; default to SearchDefaultLimit.
//  3. For each requested type (all three when p.Type == ""), call the
//     corresponding store method. A nil store result is normalised to an
//     empty (but non-nil) slice — the transport layer uses non-nil to
//     decide whether to emit the JSON key.
//  4. Per-type errors abort the whole call (legacy log-and-500 behaviour);
//     the transport wraps the error in a 500.
func (s *SearchService) Search(ctx context.Context, callerID int64, p SearchParams) (SearchResult, error) {
	ctx, span := tracer.Start(ctx, "SearchService.Search")
	defer span.End()

	if p.Query == "" {
		return SearchResult{}, fmt.Errorf("search: query is required")
	}

	limit := p.Limit
	if limit <= 0 {
		limit = SearchDefaultLimit
	}
	if limit > SearchMaxLimit {
		limit = SearchMaxLimit
	}

	var out SearchResult

	if p.Type == SearchTypeAll || p.Type == SearchTypeMessages {
		msgs, err := s.store.SearchMessages(ctx, p.Query, callerID, p.ChannelID, limit)
		if err != nil {
			return SearchResult{}, fmt.Errorf("search messages: %w", err)
		}
		if msgs == nil {
			msgs = []repo.MessageSearchResult{}
		}
		out.Messages = msgs
	}

	if p.Type == SearchTypeAll || p.Type == SearchTypeUsers {
		users, err := s.store.SearchUsers(ctx, p.Query, callerID, limit)
		if err != nil {
			return SearchResult{}, fmt.Errorf("search users: %w", err)
		}
		if users == nil {
			users = []repo.User{}
		}
		out.Users = users
	}

	if p.Type == SearchTypeAll || p.Type == SearchTypeChannels {
		channels, err := s.store.SearchChannels(ctx, p.Query, callerID, limit)
		if err != nil {
			return SearchResult{}, fmt.Errorf("search channels: %w", err)
		}
		if channels == nil {
			channels = []repo.Channel{}
		}
		out.Channels = channels
	}

	return out, nil
}

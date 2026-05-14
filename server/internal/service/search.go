package service

import (
	"context"
	"fmt"

	"im-server/internal/repo"
)

// Search tunables — preserved verbatim from the legacy SearchHandler so the
// per-type result budget is identical after the cut-over.
const (
	SearchDefaultLimit = 20
	SearchMaxLimit     = 50
)

// SearchStore is the subset of repo.SearchRepo SearchService consumes.
//
// M4: User search is gone — cses owns the user directory and im no longer
// keeps a local users table. The transport layer documents the change as
// "search type=users now returns an empty list; query the cses /search/user
// endpoint instead".
type SearchStore interface {
	SearchMessages(ctx context.Context, q string, userID string, channelID string, limit int) ([]repo.MessageSearchResult, error)
	SearchChannels(ctx context.Context, q string, callerID string, limit int) ([]repo.Channel, error)
}

// Search "type" filter values.
const (
	SearchTypeAll      = ""
	SearchTypeMessages = "messages"
	SearchTypeUsers    = "users"
	SearchTypeChannels = "channels"
)

// SearchParams is the input to SearchService.Search.
type SearchParams struct {
	Query     string
	Type      string
	ChannelID string
	Limit     int
}

// SearchResult bundles the per-type lookup outcomes.
//
// M4: Users is always nil — the JSON envelope still emits the key (legacy
// clients that switch on `result.users === undefined` work unchanged) but it
// resolves to an empty list at the wire boundary.
type SearchResult struct {
	Messages []repo.MessageSearchResult
	Users    []string // empty in M4 — kept for wire-shape compat
	Channels []repo.Channel
}

// SearchService runs the multi-type search algorithm on top of SearchStore.
type SearchService struct {
	store SearchStore
}

// NewSearchService wires the supplied store.
func NewSearchService(store SearchStore) *SearchService {
	return &SearchService{store: store}
}

// Search runs the three lookups gated by p.Type and returns the per-type
// results.
func (s *SearchService) Search(ctx context.Context, callerID string, p SearchParams) (SearchResult, error) {
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
		// M4: no local users — return an empty slice so the JSON envelope
		// still emits the key with the same shape.
		out.Users = []string{}
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

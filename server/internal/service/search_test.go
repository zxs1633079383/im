package service_test

import (
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"

	"im-server/internal/repo"
	"im-server/internal/repo/mocks"
	"im-server/internal/service"
)

// newSearchSvc returns a service backed by a fresh repo mock. Returning the
// mock lets each test pin only the calls it cares about — extra calls fail
// loudly so we don't hide regressions.
func newSearchSvc(t *testing.T) (*service.SearchService, *mocks.SearchRepoMock) {
	t.Helper()
	m := mocks.NewSearchRepoMock(t)
	return service.NewSearchService(m), m
}

func TestSearch_EmptyQuery_ReturnsError(t *testing.T) {
	svc, _ := newSearchSvc(t)
	_, err := svc.Search(context.Background(), 1, service.SearchParams{Query: ""})
	require.Error(t, err)
}

func TestSearch_TypeAll_FansOutToAllThreeStores(t *testing.T) {
	svc, store := newSearchSvc(t)
	store.EXPECT().SearchMessages(mock.Anything, "hello", int64(42), int64(0), service.SearchDefaultLimit).
		Return([]repo.MessageSearchResult{{Message: repo.Message{ID: 1}}}, nil)
	store.EXPECT().SearchUsers(mock.Anything, "hello", int64(42), service.SearchDefaultLimit).
		Return([]repo.User{{ID: 7}}, nil)
	store.EXPECT().SearchChannels(mock.Anything, "hello", int64(42), service.SearchDefaultLimit).
		Return([]repo.Channel{{ID: 9}}, nil)

	got, err := svc.Search(context.Background(), 42, service.SearchParams{Query: "hello"})
	require.NoError(t, err)
	require.Len(t, got.Messages, 1)
	require.Len(t, got.Users, 1)
	require.Len(t, got.Channels, 1)
}

func TestSearch_TypeMessages_OnlyHitsMessageStore(t *testing.T) {
	// Only the message lookup should run when type=messages — Users/Channels
	// remain nil so the transport layer can omit those JSON keys.
	svc, store := newSearchSvc(t)
	store.EXPECT().SearchMessages(mock.Anything, "hi", int64(1), int64(0), service.SearchDefaultLimit).
		Return([]repo.MessageSearchResult{}, nil)

	got, err := svc.Search(context.Background(), 1, service.SearchParams{Query: "hi", Type: service.SearchTypeMessages})
	require.NoError(t, err)
	require.NotNil(t, got.Messages)
	require.Nil(t, got.Users)
	require.Nil(t, got.Channels)
}

func TestSearch_TypeUsers_OnlyHitsUserStore(t *testing.T) {
	svc, store := newSearchSvc(t)
	store.EXPECT().SearchUsers(mock.Anything, "alice", int64(1), service.SearchDefaultLimit).
		Return([]repo.User{}, nil)

	got, err := svc.Search(context.Background(), 1, service.SearchParams{Query: "alice", Type: service.SearchTypeUsers})
	require.NoError(t, err)
	require.Nil(t, got.Messages)
	require.NotNil(t, got.Users)
	require.Nil(t, got.Channels)
}

func TestSearch_TypeChannels_OnlyHitsChannelStore(t *testing.T) {
	svc, store := newSearchSvc(t)
	store.EXPECT().SearchChannels(mock.Anything, "general", int64(1), service.SearchDefaultLimit).
		Return([]repo.Channel{}, nil)

	got, err := svc.Search(context.Background(), 1, service.SearchParams{Query: "general", Type: service.SearchTypeChannels})
	require.NoError(t, err)
	require.Nil(t, got.Messages)
	require.Nil(t, got.Users)
	require.NotNil(t, got.Channels)
}

func TestSearch_ChannelIDForwardedToMessages(t *testing.T) {
	// channel_id only meaningfully restricts message search; verify the
	// service forwards the value verbatim.
	svc, store := newSearchSvc(t)
	store.EXPECT().SearchMessages(mock.Anything, "hi", int64(1), int64(99), service.SearchDefaultLimit).
		Return(nil, nil)

	_, err := svc.Search(context.Background(), 1, service.SearchParams{
		Query:     "hi",
		Type:      service.SearchTypeMessages,
		ChannelID: 99,
	})
	require.NoError(t, err)
}

func TestSearch_LimitClampedToMax(t *testing.T) {
	svc, store := newSearchSvc(t)
	// Caller asks for 9999 → service clamps to SearchMaxLimit before hitting
	// the store. We assert the clamped value lands in the store call.
	store.EXPECT().SearchUsers(mock.Anything, "x", int64(1), service.SearchMaxLimit).
		Return(nil, nil)

	_, err := svc.Search(context.Background(), 1, service.SearchParams{
		Query: "x",
		Type:  service.SearchTypeUsers,
		Limit: 9999,
	})
	require.NoError(t, err)
}

func TestSearch_LimitDefaultsWhenZero(t *testing.T) {
	svc, store := newSearchSvc(t)
	store.EXPECT().SearchUsers(mock.Anything, "x", int64(1), service.SearchDefaultLimit).
		Return(nil, nil)

	_, err := svc.Search(context.Background(), 1, service.SearchParams{
		Query: "x",
		Type:  service.SearchTypeUsers,
		Limit: 0,
	})
	require.NoError(t, err)
}

func TestSearch_NilStoreResults_NormalisedToEmptySlices(t *testing.T) {
	// Stores returning a literal nil must not surface a nil slice — the
	// transport layer relies on non-nil to decide whether to emit each key.
	svc, store := newSearchSvc(t)
	store.EXPECT().SearchMessages(mock.Anything, "x", int64(1), int64(0), service.SearchDefaultLimit).
		Return(nil, nil)
	store.EXPECT().SearchUsers(mock.Anything, "x", int64(1), service.SearchDefaultLimit).
		Return(nil, nil)
	store.EXPECT().SearchChannels(mock.Anything, "x", int64(1), service.SearchDefaultLimit).
		Return(nil, nil)

	got, err := svc.Search(context.Background(), 1, service.SearchParams{Query: "x"})
	require.NoError(t, err)
	require.NotNil(t, got.Messages)
	require.NotNil(t, got.Users)
	require.NotNil(t, got.Channels)
	require.Empty(t, got.Messages)
	require.Empty(t, got.Users)
	require.Empty(t, got.Channels)
}

func TestSearch_StoreError_PropagatesWrapped(t *testing.T) {
	svc, store := newSearchSvc(t)
	boom := errors.New("db down")
	store.EXPECT().SearchMessages(mock.Anything, "x", int64(1), int64(0), service.SearchDefaultLimit).
		Return(nil, boom)

	_, err := svc.Search(context.Background(), 1, service.SearchParams{
		Query: "x",
		Type:  service.SearchTypeMessages,
	})
	require.ErrorIs(t, err, boom)
}

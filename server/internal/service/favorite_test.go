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

// newFavoriteSvc returns a service backed by a fresh repo mock. Returning
// the mock lets each test pin only the calls it cares about — extra calls
// fail loudly so we don't hide regressions.
func newFavoriteSvc(t *testing.T) (*service.FavoriteService, *mocks.FavoriteRepoMock) {
	t.Helper()
	m := mocks.NewFavoriteRepoMock(t)
	return service.NewFavoriteService(m), m
}

func TestFavorite_Add_DelegatesToStore(t *testing.T) {
	svc, store := newFavoriteSvc(t)
	store.EXPECT().Add(mock.Anything, int64(7), int64(42)).Return(nil)

	err := svc.Add(context.Background(), 7, 42)
	require.NoError(t, err)
}

func TestFavorite_Add_PropagatesStoreError(t *testing.T) {
	svc, store := newFavoriteSvc(t)
	boom := errors.New("db down")
	store.EXPECT().Add(mock.Anything, int64(7), int64(42)).Return(boom)

	err := svc.Add(context.Background(), 7, 42)
	require.ErrorIs(t, err, boom)
}

func TestFavorite_Remove_DelegatesToStore(t *testing.T) {
	svc, store := newFavoriteSvc(t)
	store.EXPECT().Remove(mock.Anything, int64(7), int64(42)).Return(nil)

	err := svc.Remove(context.Background(), 7, 42)
	require.NoError(t, err)
}

func TestFavorite_Remove_NotFound_PreservesSentinel(t *testing.T) {
	// The transport layer relies on errors.Is(err, repo.ErrNotFound) to map
	// the response to a 404, so the sentinel must survive unwrapped.
	svc, store := newFavoriteSvc(t)
	store.EXPECT().Remove(mock.Anything, int64(7), int64(42)).Return(repo.ErrNotFound)

	err := svc.Remove(context.Background(), 7, 42)
	require.ErrorIs(t, err, repo.ErrNotFound)
}

func TestFavorite_Remove_OtherErrorsWrapped(t *testing.T) {
	svc, store := newFavoriteSvc(t)
	boom := errors.New("db down")
	store.EXPECT().Remove(mock.Anything, int64(7), int64(42)).Return(boom)

	err := svc.Remove(context.Background(), 7, 42)
	require.ErrorIs(t, err, boom)
}

func TestFavorite_List_ReturnsItems(t *testing.T) {
	svc, store := newFavoriteSvc(t)
	want := []repo.FavoriteWithMessage{
		{UserID: 7, MessageID: 1},
		{UserID: 7, MessageID: 2},
	}
	store.EXPECT().List(mock.Anything, int64(7)).Return(want, nil)

	got, err := svc.List(context.Background(), 7)
	require.NoError(t, err)
	require.Len(t, got, 2)
}

func TestFavorite_List_NilNormalisedToEmptySlice(t *testing.T) {
	// Stores returning a literal nil must not surface a nil slice — the
	// transport layer always emits "favorites" as an array.
	svc, store := newFavoriteSvc(t)
	store.EXPECT().List(mock.Anything, int64(7)).Return(nil, nil)

	got, err := svc.List(context.Background(), 7)
	require.NoError(t, err)
	require.NotNil(t, got)
	require.Empty(t, got)
}

func TestFavorite_List_StoreError_Wrapped(t *testing.T) {
	svc, store := newFavoriteSvc(t)
	boom := errors.New("db down")
	store.EXPECT().List(mock.Anything, int64(7)).Return(nil, boom)

	_, err := svc.List(context.Background(), 7)
	require.ErrorIs(t, err, boom)
}

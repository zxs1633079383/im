//go:build integration

package integration

import (
	"context"
	"sync"
	"testing"

	"github.com/stretchr/testify/require"

	"im-server/internal/repo"
	"im-server/internal/testutil/containers"
)

// TestUserRepo_UpsertByMattermostID_LazyCreate covers the first-time path:
// no shadow row exists for the cookie, the upsert inserts one and returns
// it with mm_user_id wired up.
func TestUserRepo_UpsertByMattermostID_LazyCreate(t *testing.T) {
	dsn := containers.StartPostgres(t)
	db, err := repo.Open(repo.Config{DSN: dsn})
	require.NoError(t, err)
	t.Cleanup(func() {
		if sqlDB, e := db.DB(); e == nil {
			_ = sqlDB.Close()
		}
	})
	users := repo.NewUserRepo(db)

	got, err := users.UpsertByMattermostID(context.Background(), repo.MattermostUpsertParams{
		MattermostUserID: "6847ade6614b70055ea2a4b6",
		Username:         "alice@example.com",
		Email:            "alice@example.com",
		DisplayName:      "Alice",
	})
	require.NoError(t, err)
	require.Greater(t, got.ID, int64(0))
	require.NotNil(t, got.MattermostUserID)
	require.Equal(t, "6847ade6614b70055ea2a4b6", *got.MattermostUserID)
	require.Equal(t, "Alice", got.DisplayName)
	// Username carries the short suffix to dodge collisions with JWT-native users.
	require.Contains(t, got.Username, "5ea2a4b6")
}

// TestUserRepo_UpsertByMattermostID_ReturnsExisting — second call with the
// same mm id returns the existing row, doesn't create a duplicate.
func TestUserRepo_UpsertByMattermostID_ReturnsExisting(t *testing.T) {
	dsn := containers.StartPostgres(t)
	db, err := repo.Open(repo.Config{DSN: dsn})
	require.NoError(t, err)
	t.Cleanup(func() {
		if sqlDB, e := db.DB(); e == nil {
			_ = sqlDB.Close()
		}
	})
	users := repo.NewUserRepo(db)

	first, err := users.UpsertByMattermostID(context.Background(), repo.MattermostUpsertParams{
		MattermostUserID: "mm-1",
		Username:         "bob",
	})
	require.NoError(t, err)
	second, err := users.UpsertByMattermostID(context.Background(), repo.MattermostUpsertParams{
		MattermostUserID: "mm-1",
		Username:         "bob",
	})
	require.NoError(t, err)
	require.Equal(t, first.ID, second.ID, "second upsert must return the existing row")
}

// TestUserRepo_UpsertByMattermostID_ConcurrentSameMMIDFunnelsToOneRow —
// races N goroutines on the same mm id; the unique partial index makes the
// loser fall through the SELECT branch on retry, so all callers see the
// same im id.
func TestUserRepo_UpsertByMattermostID_ConcurrentSameMMIDFunnelsToOneRow(t *testing.T) {
	dsn := containers.StartPostgres(t)
	db, err := repo.Open(repo.Config{DSN: dsn})
	require.NoError(t, err)
	t.Cleanup(func() {
		if sqlDB, e := db.DB(); e == nil {
			_ = sqlDB.Close()
		}
	})
	users := repo.NewUserRepo(db)

	const concurrency = 10
	ids := make([]int64, concurrency)
	errs := make([]error, concurrency)
	var wg sync.WaitGroup
	for i := 0; i < concurrency; i++ {
		i := i
		wg.Add(1)
		go func() {
			defer wg.Done()
			u, err := users.UpsertByMattermostID(context.Background(), repo.MattermostUpsertParams{
				MattermostUserID: "race-mm",
				Username:         "carol",
			})
			if err == nil {
				ids[i] = u.ID
			}
			errs[i] = err
		}()
	}
	wg.Wait()

	for i, e := range errs {
		require.NoErrorf(t, e, "goroutine %d", i)
	}
	first := ids[0]
	require.Greater(t, first, int64(0))
	for i, id := range ids {
		require.Equalf(t, first, id, "goroutine %d returned a different im id", i)
	}
}

// TestUserRepo_UpsertByMattermostID_DistinctMMIDsCreateDistinctRows guards
// against accidental over-collapsing: two unrelated cookies must yield two
// independent shadow rows.
func TestUserRepo_UpsertByMattermostID_DistinctMMIDsCreateDistinctRows(t *testing.T) {
	dsn := containers.StartPostgres(t)
	db, err := repo.Open(repo.Config{DSN: dsn})
	require.NoError(t, err)
	t.Cleanup(func() {
		if sqlDB, e := db.DB(); e == nil {
			_ = sqlDB.Close()
		}
	})
	users := repo.NewUserRepo(db)

	a, err := users.UpsertByMattermostID(context.Background(), repo.MattermostUpsertParams{
		MattermostUserID: "mm-a", Username: "shared",
	})
	require.NoError(t, err)
	b, err := users.UpsertByMattermostID(context.Background(), repo.MattermostUpsertParams{
		MattermostUserID: "mm-b", Username: "shared",
	})
	require.NoError(t, err)
	require.NotEqual(t, a.ID, b.ID)
	require.NotEqual(t, a.Username, b.Username, "username suffix disambiguates collisions")
}

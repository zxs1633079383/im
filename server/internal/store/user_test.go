package store_test

import (
	"context"
	"testing"

	"im-server/internal/model"
	"im-server/internal/store"
	"im-server/internal/testutil"
)

func TestUserStore_CreateAndGet(t *testing.T) {
	pool := testutil.PGPool(t)
	us := store.NewUserStore(pool)
	ctx := context.Background()

	user := &model.User{
		Username:     "testuser",
		Email:        "test@example.com",
		PasswordHash: "hashed_pw",
		DisplayName:  "Test User",
	}

	err := us.Create(ctx, user)
	if err != nil {
		t.Fatalf("Create() error: %v", err)
	}
	if user.ID == 0 {
		t.Fatal("Create() did not set user.ID")
	}

	got, err := us.GetByID(ctx, user.ID)
	if err != nil {
		t.Fatalf("GetByID() error: %v", err)
	}
	if got.Username != "testuser" {
		t.Errorf("Username = %q, want testuser", got.Username)
	}
	if got.Email != "test@example.com" {
		t.Errorf("Email = %q, want test@example.com", got.Email)
	}
}

func TestUserStore_GetByUsername(t *testing.T) {
	pool := testutil.PGPool(t)
	us := store.NewUserStore(pool)
	ctx := context.Background()

	user := &model.User{
		Username:     "findme",
		Email:        "find@example.com",
		PasswordHash: "hashed",
		DisplayName:  "Find Me",
	}
	if err := us.Create(ctx, user); err != nil {
		t.Fatal(err)
	}

	got, err := us.GetByUsername(ctx, "findme")
	if err != nil {
		t.Fatalf("GetByUsername() error: %v", err)
	}
	if got.ID != user.ID {
		t.Errorf("ID = %d, want %d", got.ID, user.ID)
	}
}

func TestUserStore_DuplicateUsername(t *testing.T) {
	pool := testutil.PGPool(t)
	us := store.NewUserStore(pool)
	ctx := context.Background()

	user1 := &model.User{Username: "dup", Email: "a@example.com", PasswordHash: "h"}
	if err := us.Create(ctx, user1); err != nil {
		t.Fatal(err)
	}

	user2 := &model.User{Username: "dup", Email: "b@example.com", PasswordHash: "h"}
	err := us.Create(ctx, user2)
	if err == nil {
		t.Fatal("expected error for duplicate username, got nil")
	}
}

func TestUserStore_Search(t *testing.T) {
	pool := testutil.PGPool(t)
	us := store.NewUserStore(pool)
	ctx := context.Background()

	users := []model.User{
		{Username: "alpha", Email: "alpha@x.com", PasswordHash: "h", DisplayName: "Alpha User"},
		{Username: "beta", Email: "beta@x.com", PasswordHash: "h", DisplayName: "Beta Tester"},
		{Username: "gamma", Email: "gamma@x.com", PasswordHash: "h", DisplayName: "Gamma"},
	}
	for i := range users {
		if err := us.Create(ctx, &users[i]); err != nil {
			t.Fatal(err)
		}
	}

	// search by username prefix
	got, err := us.Search(ctx, "alp", 0)
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if len(got) != 1 || got[0].Username != "alpha" {
		t.Errorf("expected [alpha], got %v", got)
	}

	// search by display_name substring
	got, err = us.Search(ctx, "Tester", 0)
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if len(got) != 1 || got[0].Username != "beta" {
		t.Errorf("expected [beta], got %v", got)
	}

	// caller excluded from results
	got, err = us.Search(ctx, "alpha", users[0].ID)
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	for _, u := range got {
		if u.ID == users[0].ID {
			t.Error("caller should be excluded from search results")
		}
	}

	// empty query returns up to 20 others (not the caller)
	got, err = us.Search(ctx, "", users[0].ID)
	if err != nil {
		t.Fatalf("Search empty: %v", err)
	}
	if len(got) != 2 { // beta and gamma
		t.Errorf("expected 2, got %d", len(got))
	}
}

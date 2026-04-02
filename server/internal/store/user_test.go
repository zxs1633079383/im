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

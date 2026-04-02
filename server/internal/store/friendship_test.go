package store_test

import (
	"context"
	"testing"

	"im-server/internal/model"
	"im-server/internal/store"
	"im-server/internal/testutil"
)

// helper: create a minimal user and return its ID
func createUser(t *testing.T, us *store.UserStore, username string) int64 {
	t.Helper()
	u := &model.User{
		Username:     username,
		Email:        username + "@example.com",
		PasswordHash: "hash",
		DisplayName:  username,
	}
	if err := us.Create(context.Background(), u); err != nil {
		t.Fatalf("createUser %s: %v", username, err)
	}
	return u.ID
}

func TestFriendshipStore_SendAndAccept(t *testing.T) {
	pool := testutil.PGPool(t)
	us := store.NewUserStore(pool)
	fs := store.NewFriendshipStore(pool)
	ctx := context.Background()

	alice := createUser(t, us, "alice")
	bob := createUser(t, us, "bob")

	// send request
	if err := fs.SendRequest(ctx, alice, bob); err != nil {
		t.Fatalf("SendRequest: %v", err)
	}

	// duplicate should fail
	if err := fs.SendRequest(ctx, alice, bob); err != store.ErrAlreadyExists {
		t.Fatalf("expected ErrAlreadyExists, got %v", err)
	}

	// bob sees a pending request
	pending, err := fs.ListPendingRequests(ctx, bob)
	if err != nil {
		t.Fatalf("ListPendingRequests: %v", err)
	}
	if len(pending) != 1 {
		t.Fatalf("expected 1 pending, got %d", len(pending))
	}
	if pending[0].Requester.ID != alice {
		t.Errorf("requester ID = %d, want %d", pending[0].Requester.ID, alice)
	}

	// bob accepts
	if err := fs.AcceptRequest(ctx, pending[0].Friendship.ID, bob); err != nil {
		t.Fatalf("AcceptRequest: %v", err)
	}

	// now both see each other as friends
	aliceFriends, _ := fs.ListFriends(ctx, alice)
	if len(aliceFriends) != 1 || aliceFriends[0].ID != bob {
		t.Errorf("alice friends: got %v, want [bob]", aliceFriends)
	}
	bobFriends, _ := fs.ListFriends(ctx, bob)
	if len(bobFriends) != 1 || bobFriends[0].ID != alice {
		t.Errorf("bob friends: got %v, want [alice]", bobFriends)
	}
}

func TestFriendshipStore_RejectRequest(t *testing.T) {
	pool := testutil.PGPool(t)
	us := store.NewUserStore(pool)
	fs := store.NewFriendshipStore(pool)
	ctx := context.Background()

	carol := createUser(t, us, "carol")
	dave := createUser(t, us, "dave")

	if err := fs.SendRequest(ctx, carol, dave); err != nil {
		t.Fatal(err)
	}
	pending, _ := fs.ListPendingRequests(ctx, dave)
	if len(pending) != 1 {
		t.Fatal("expected 1 pending request")
	}
	if err := fs.RejectRequest(ctx, pending[0].Friendship.ID, dave); err != nil {
		t.Fatalf("RejectRequest: %v", err)
	}
	friends, _ := fs.ListFriends(ctx, dave)
	if len(friends) != 0 {
		t.Errorf("expected 0 friends after rejection, got %d", len(friends))
	}
}

func TestFriendshipStore_GetFriendship(t *testing.T) {
	pool := testutil.PGPool(t)
	us := store.NewUserStore(pool)
	fs := store.NewFriendshipStore(pool)
	ctx := context.Background()

	eve := createUser(t, us, "eve")
	frank := createUser(t, us, "frank")

	// not found before request
	_, err := fs.GetFriendship(ctx, eve, frank)
	if err != store.ErrNotFound {
		t.Fatalf("expected ErrNotFound, got %v", err)
	}

	if err := fs.SendRequest(ctx, eve, frank); err != nil {
		t.Fatal(err)
	}

	f, err := fs.GetFriendship(ctx, eve, frank)
	if err != nil {
		t.Fatalf("GetFriendship: %v", err)
	}
	if f.Status != model.FriendshipPending {
		t.Errorf("status = %d, want pending", f.Status)
	}

	// reverse lookup also works
	f2, err := fs.GetFriendship(ctx, frank, eve)
	if err != nil {
		t.Fatalf("GetFriendship reverse: %v", err)
	}
	if f2.ID != f.ID {
		t.Errorf("reverse lookup ID mismatch")
	}
}

func TestFriendshipStore_BlockUser(t *testing.T) {
	pool := testutil.PGPool(t)
	us := store.NewUserStore(pool)
	fs := store.NewFriendshipStore(pool)
	ctx := context.Background()

	grace := createUser(t, us, "grace")
	henry := createUser(t, us, "henry")

	// block without prior friendship
	if err := fs.BlockUser(ctx, grace, henry); err != nil {
		t.Fatalf("BlockUser (new): %v", err)
	}
	f, err := fs.GetFriendship(ctx, grace, henry)
	if err != nil {
		t.Fatal(err)
	}
	if f.Status != model.FriendshipBlocked {
		t.Errorf("status = %d, want blocked", f.Status)
	}
}

func TestFriendshipStore_SelfRequest(t *testing.T) {
	pool := testutil.PGPool(t)
	us := store.NewUserStore(pool)
	fs := store.NewFriendshipStore(pool)
	ctx := context.Background()

	ian := createUser(t, us, "ian")
	if err := fs.SendRequest(ctx, ian, ian); err == nil {
		t.Fatal("expected error for self-request, got nil")
	}
}

func TestFriendshipStore_AcceptWrongUser(t *testing.T) {
	pool := testutil.PGPool(t)
	us := store.NewUserStore(pool)
	fs := store.NewFriendshipStore(pool)
	ctx := context.Background()

	jane := createUser(t, us, "jane")
	kate := createUser(t, us, "kate")
	leo := createUser(t, us, "leo")

	if err := fs.SendRequest(ctx, jane, kate); err != nil {
		t.Fatal(err)
	}
	pending, _ := fs.ListPendingRequests(ctx, kate)
	// leo tries to accept jane's request to kate — should get ErrNotFound
	err := fs.AcceptRequest(ctx, pending[0].Friendship.ID, leo)
	if err != store.ErrNotFound {
		t.Fatalf("expected ErrNotFound, got %v", err)
	}
}

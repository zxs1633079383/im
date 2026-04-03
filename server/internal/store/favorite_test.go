package store_test

import (
	"context"
	"testing"

	"im-server/internal/model"
	"im-server/internal/store"
	"im-server/internal/testutil"
)

func TestFavoriteStore_AddRemoveList(t *testing.T) {
	pool := testutil.PGPool(t)
	fs := store.NewFavoriteStore(pool)
	ms := store.NewMessageStore(pool)
	cs := store.NewChannelStore(pool)
	us := store.NewUserStore(pool)
	ctx := context.Background()

	user := &model.User{Username: "fav_user", Email: "fav@test.com", PasswordHash: "h", DisplayName: "Fav User"}
	if err := us.Create(ctx, user); err != nil {
		t.Fatalf("Create user: %v", err)
	}

	ch := &model.Channel{Type: model.ChannelTypeGroup, Name: "fav-chan"}
	if err := cs.Create(ctx, ch); err != nil {
		t.Fatalf("Create channel: %v", err)
	}
	if err := cs.AddMember(ctx, ch.ID, user.ID, model.MemberRoleMember); err != nil {
		t.Fatalf("AddMember: %v", err)
	}

	msg := &model.Message{ChannelID: ch.ID, SenderID: user.ID, MsgType: model.MsgTypeText, Content: "fav me"}
	if err := ms.Send(ctx, msg); err != nil {
		t.Fatalf("Send: %v", err)
	}

	// Add
	if err := fs.Add(ctx, user.ID, msg.ID); err != nil {
		t.Fatalf("Add: %v", err)
	}

	// List
	favs, err := fs.List(ctx, user.ID)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(favs) == 0 {
		t.Fatal("expected at least one favorite")
	}
	if favs[0].MessageID != msg.ID {
		t.Errorf("want message ID %d, got %d", msg.ID, favs[0].MessageID)
	}

	// Idempotent add
	if err := fs.Add(ctx, user.ID, msg.ID); err != nil {
		t.Fatalf("duplicate Add should be idempotent: %v", err)
	}

	// Remove
	if err := fs.Remove(ctx, user.ID, msg.ID); err != nil {
		t.Fatalf("Remove: %v", err)
	}
	favs2, _ := fs.List(ctx, user.ID)
	for _, f := range favs2 {
		if f.MessageID == msg.ID {
			t.Fatal("message should have been removed from favorites")
		}
	}
}

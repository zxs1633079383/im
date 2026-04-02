package store_test

import (
	"context"
	"testing"

	"im-server/internal/model"
	"im-server/internal/store"
	"im-server/internal/testutil"
)

func TestChannelStore_CreateGroupAndAddMember(t *testing.T) {
	pool := testutil.PGPool(t)
	cs := store.NewChannelStore(pool)
	us := store.NewUserStore(pool)
	ctx := context.Background()

	user := &model.User{Username: "alice", Email: "alice@test.com", PasswordHash: "h", DisplayName: "Alice"}
	if err := us.Create(ctx, user); err != nil {
		t.Fatal(err)
	}

	ch := &model.Channel{Type: model.ChannelTypeGroup, Name: "test-group", CreatorID: &user.ID}
	err := cs.Create(ctx, ch)
	if err != nil {
		t.Fatalf("Create() error: %v", err)
	}
	if ch.ID == 0 {
		t.Fatal("channel ID not set")
	}
	if ch.Seq != 0 {
		t.Errorf("initial seq = %d, want 0", ch.Seq)
	}

	err = cs.AddMember(ctx, ch.ID, user.ID, model.MemberRoleOwner)
	if err != nil {
		t.Fatalf("AddMember() error: %v", err)
	}

	members, err := cs.ListMembers(ctx, ch.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(members) != 1 {
		t.Fatalf("len(members) = %d, want 1", len(members))
	}
	if members[0].UserID != user.ID {
		t.Errorf("member UserID = %d, want %d", members[0].UserID, user.ID)
	}
}

func TestChannelStore_CreateDM(t *testing.T) {
	pool := testutil.PGPool(t)
	cs := store.NewChannelStore(pool)
	us := store.NewUserStore(pool)
	ctx := context.Background()

	alice := &model.User{Username: "alice2", Email: "a2@test.com", PasswordHash: "h", DisplayName: "Alice"}
	bob := &model.User{Username: "bob2", Email: "b2@test.com", PasswordHash: "h", DisplayName: "Bob"}
	us.Create(ctx, alice)
	us.Create(ctx, bob)

	ch := &model.Channel{Type: model.ChannelTypeDM}
	if err := cs.Create(ctx, ch); err != nil {
		t.Fatal(err)
	}
	cs.AddMember(ctx, ch.ID, alice.ID, model.MemberRoleMember)
	cs.AddMember(ctx, ch.ID, bob.ID, model.MemberRoleMember)

	channels, err := cs.ListByUser(ctx, alice.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(channels) != 1 {
		t.Fatalf("ListByUser len = %d, want 1", len(channels))
	}
	if channels[0].Type != model.ChannelTypeDM {
		t.Errorf("type = %d, want DM", channels[0].Type)
	}
}

func TestChannelStore_IncrementSeq(t *testing.T) {
	pool := testutil.PGPool(t)
	cs := store.NewChannelStore(pool)
	ctx := context.Background()

	ch := &model.Channel{Type: model.ChannelTypeGroup, Name: "seq-test"}
	cs.Create(ctx, ch)

	seq, err := cs.IncrementSeq(ctx, nil, ch.ID)
	if err != nil {
		t.Fatal(err)
	}
	if seq != 1 {
		t.Errorf("first seq = %d, want 1", seq)
	}

	seq, err = cs.IncrementSeq(ctx, nil, ch.ID)
	if err != nil {
		t.Fatal(err)
	}
	if seq != 2 {
		t.Errorf("second seq = %d, want 2", seq)
	}
}

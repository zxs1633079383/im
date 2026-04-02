package store_test

import (
	"context"
	"testing"

	"im-server/internal/model"
	"im-server/internal/store"
	"im-server/internal/testutil"
)

func TestMessageStore_SendAndFetch(t *testing.T) {
	pool := testutil.PGPool(t)
	ms := store.NewMessageStore(pool)
	cs := store.NewChannelStore(pool)
	us := store.NewUserStore(pool)
	ctx := context.Background()

	user := &model.User{Username: "sender", Email: "s@test.com", PasswordHash: "h", DisplayName: "Sender"}
	us.Create(ctx, user)

	ch := &model.Channel{Type: model.ChannelTypeGroup, Name: "msg-test"}
	cs.Create(ctx, ch)
	cs.AddMember(ctx, ch.ID, user.ID, model.MemberRoleMember)

	msg := &model.Message{
		ChannelID:   ch.ID,
		SenderID:    user.ID,
		ClientMsgID: "uuid-001",
		MsgType:     model.MsgTypeText,
		Content:     "hello world",
	}
	err := ms.Send(ctx, msg)
	if err != nil {
		t.Fatalf("Send() error: %v", err)
	}
	if msg.Seq != 1 {
		t.Errorf("seq = %d, want 1", msg.Seq)
	}

	messages, err := ms.FetchAfter(ctx, ch.ID, 0, 50)
	if err != nil {
		t.Fatal(err)
	}
	if len(messages) != 1 {
		t.Fatalf("len = %d, want 1", len(messages))
	}
	if messages[0].Content != "hello world" {
		t.Errorf("content = %q", messages[0].Content)
	}
}

func TestMessageStore_Idempotent(t *testing.T) {
	pool := testutil.PGPool(t)
	ms := store.NewMessageStore(pool)
	cs := store.NewChannelStore(pool)
	us := store.NewUserStore(pool)
	ctx := context.Background()

	user := &model.User{Username: "idem", Email: "idem@test.com", PasswordHash: "h", DisplayName: "Idem"}
	us.Create(ctx, user)
	ch := &model.Channel{Type: model.ChannelTypeGroup, Name: "idem-test"}
	cs.Create(ctx, ch)

	msg1 := &model.Message{ChannelID: ch.ID, SenderID: user.ID, ClientMsgID: "same-uuid", Content: "first", MsgType: model.MsgTypeText}
	if err := ms.Send(ctx, msg1); err != nil {
		t.Fatal(err)
	}

	msg2 := &model.Message{ChannelID: ch.ID, SenderID: user.ID, ClientMsgID: "same-uuid", Content: "duplicate", MsgType: model.MsgTypeText}
	if err := ms.Send(ctx, msg2); err != nil {
		t.Fatal(err)
	}

	if msg2.Seq != msg1.Seq {
		t.Errorf("duplicate seq = %d, want %d", msg2.Seq, msg1.Seq)
	}

	messages, _ := ms.FetchAfter(ctx, ch.ID, 0, 50)
	if len(messages) != 1 {
		t.Errorf("message count = %d after duplicate send, want 1", len(messages))
	}
}

func TestMessageStore_FetchForUser_Phantom(t *testing.T) {
	pool := testutil.PGPool(t)
	ms := store.NewMessageStore(pool)
	cs := store.NewChannelStore(pool)
	us := store.NewUserStore(pool)
	ctx := context.Background()

	alice := &model.User{Username: "alice3", Email: "a3@test.com", PasswordHash: "h", DisplayName: "Alice"}
	bob := &model.User{Username: "bob3", Email: "b3@test.com", PasswordHash: "h", DisplayName: "Bob"}
	us.Create(ctx, alice)
	us.Create(ctx, bob)

	ch := &model.Channel{Type: model.ChannelTypeGroup, Name: "phantom-test"}
	cs.Create(ctx, ch)
	cs.AddMember(ctx, ch.ID, alice.ID, model.MemberRoleMember)
	cs.AddMember(ctx, ch.ID, bob.ID, model.MemberRoleMember)

	ms.Send(ctx, &model.Message{ChannelID: ch.ID, SenderID: alice.ID, ClientMsgID: "m1", Content: "public", MsgType: model.MsgTypeText})
	ms.Send(ctx, &model.Message{ChannelID: ch.ID, SenderID: alice.ID, ClientMsgID: "m2", Content: "secret", MsgType: model.MsgTypeText, VisibleTo: []int64{alice.ID}})
	ms.Send(ctx, &model.Message{ChannelID: ch.ID, SenderID: alice.ID, ClientMsgID: "m3", Content: "public2", MsgType: model.MsgTypeText})

	aliceMsgs, _ := ms.FetchAfter(ctx, ch.ID, 0, 50)
	if len(aliceMsgs) != 3 {
		t.Errorf("alice sees %d messages, want 3", len(aliceMsgs))
	}

	bobView, _ := ms.FetchForUser(ctx, ch.ID, bob.ID, 0, 50)
	if len(bobView) != 3 {
		t.Fatalf("bob sees %d items, want 3", len(bobView))
	}
	if bobView[1].Content != "" {
		t.Errorf("bob sees content %q for phantom, want empty", bobView[1].Content)
	}
	if bobView[1].MsgType != model.MsgTypePhantom {
		t.Errorf("bob msg_type = %d, want phantom(%d)", bobView[1].MsgType, model.MsgTypePhantom)
	}
}

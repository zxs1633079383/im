package store_test

import (
	"context"
	"testing"

	"im-server/internal/model"
	"im-server/internal/store"
	"im-server/internal/testutil"
)

func TestFileStore_CreateAndGet(t *testing.T) {
	pool := testutil.PGPool(t)
	fs := store.NewFileStore(pool)
	us := store.NewUserStore(pool)
	ctx := context.Background()

	user := &model.User{Username: "file_uploader", Email: "fu@test.com", PasswordHash: "h", DisplayName: "FU"}
	if err := us.Create(ctx, user); err != nil {
		t.Fatalf("create user: %v", err)
	}

	f := &model.File{
		UploaderID:  user.ID,
		FileName:    "test.png",
		FileSize:    1024,
		MimeType:    "image/png",
		StoragePath: "/data/uploads/test.png",
	}

	if err := fs.Create(ctx, f); err != nil {
		t.Fatalf("Create: %v", err)
	}
	if f.ID == 0 {
		t.Fatal("expected non-zero ID after Create")
	}

	got, err := fs.GetByID(ctx, f.ID)
	if err != nil {
		t.Fatalf("GetByID: %v", err)
	}
	if got.FileName != f.FileName {
		t.Errorf("want FileName %q, got %q", f.FileName, got.FileName)
	}
}

func TestFileStore_AttachAndList(t *testing.T) {
	pool := testutil.PGPool(t)
	fs := store.NewFileStore(pool)
	ms := store.NewMessageStore(pool)
	cs := store.NewChannelStore(pool)
	us := store.NewUserStore(pool)
	ctx := context.Background()

	user := &model.User{Username: "file_attacher", Email: "fa@test.com", PasswordHash: "h", DisplayName: "FA"}
	if err := us.Create(ctx, user); err != nil {
		t.Fatalf("create user: %v", err)
	}

	ch := &model.Channel{Type: model.ChannelTypeGroup, Name: "attach-test"}
	if err := cs.Create(ctx, ch); err != nil {
		t.Fatalf("create channel: %v", err)
	}
	if err := cs.AddMember(ctx, ch.ID, user.ID, model.MemberRoleMember); err != nil {
		t.Fatalf("add member: %v", err)
	}

	// Create file
	f := &model.File{UploaderID: user.ID, FileName: "attach.pdf", FileSize: 512, MimeType: "application/pdf", StoragePath: "/tmp/attach.pdf"}
	if err := fs.Create(ctx, f); err != nil {
		t.Fatalf("Create file: %v", err)
	}

	// Create message
	msg := &model.Message{ChannelID: ch.ID, SenderID: user.ID, MsgType: model.MsgTypeFile, Content: "see attachment"}
	if err := ms.Send(ctx, msg); err != nil {
		t.Fatalf("Send message: %v", err)
	}

	// Attach
	if err := fs.AttachToMessage(ctx, msg.ID, f.ID); err != nil {
		t.Fatalf("AttachToMessage: %v", err)
	}

	// List
	files, err := fs.ListByMessage(ctx, msg.ID)
	if err != nil {
		t.Fatalf("ListByMessage: %v", err)
	}
	if len(files) != 1 {
		t.Fatalf("want 1 file, got %d", len(files))
	}
	if files[0].ID != f.ID {
		t.Errorf("want file ID %d, got %d", f.ID, files[0].ID)
	}
}

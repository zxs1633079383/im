//go:build integration

package repo

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"
	"im-server/internal/testutil/containers"
)

func newFileRepo(t *testing.T) (FileRepo, MessageRepo, ChannelRepo, UserRepo, context.Context) {
	dsn := containers.StartPostgres(t)
	db, err := Open(Config{DSN: dsn})
	require.NoError(t, err)
	cr := NewChannelRepo(db)
	return NewFileRepo(db), NewMessageRepo(db, cr), cr, NewUserRepo(db), context.Background()
}

func mkFile(t *testing.T, fr FileRepo, ctx context.Context, uploaderID int64, name string) *File {
	f := &File{
		UploaderID:  uploaderID,
		FileName:    name,
		FileSize:    1024,
		MimeType:    "application/octet-stream",
		StoragePath: "/tmp/" + name,
	}
	require.NoError(t, fr.Create(ctx, f))
	return f
}

func TestFileRepo_CreateGetByID(t *testing.T) {
	fr, _, _, ur, ctx := newFileRepo(t)
	u := mkUser(t, ur, ctx, "uploader")

	f := mkFile(t, fr, ctx, u.ID, "report.pdf")
	require.NotZero(t, f.ID)
	require.False(t, f.CreatedAt.IsZero(), "CreatedAt should be set by DB default")

	got, err := fr.GetByID(ctx, f.ID)
	require.NoError(t, err)
	require.Equal(t, f.ID, got.ID)
	require.Equal(t, "report.pdf", got.FileName)
	require.Equal(t, u.ID, got.UploaderID)
	require.Equal(t, int64(1024), got.FileSize)
	require.Equal(t, "application/octet-stream", got.MimeType)
	require.Equal(t, "/tmp/report.pdf", got.StoragePath)
}

func TestFileRepo_GetByID_NotFound(t *testing.T) {
	fr, _, _, _, ctx := newFileRepo(t)
	_, err := fr.GetByID(ctx, 99999)
	require.ErrorIs(t, err, ErrNotFound)
}

func TestFileRepo_AttachToMessage_Idempotent(t *testing.T) {
	fr, mr, cr, ur, ctx := newFileRepo(t)
	u := mkUser(t, ur, ctx, "alice")
	ch := mkMsgChannel(t, cr, ctx, "files", u.ID)
	msg := &Message{ChannelID: ch.ID, SenderID: u.ID, Content: "with attachment", MsgType: 1}
	require.NoError(t, mr.Send(ctx, msg))
	f := mkFile(t, fr, ctx, u.ID, "doc.pdf")

	// Attach twice — second call must succeed (OnConflict DoNothing).
	require.NoError(t, fr.AttachToMessage(ctx, msg.ID, f.ID))
	require.NoError(t, fr.AttachToMessage(ctx, msg.ID, f.ID))

	// Only one row in the join table.
	files, err := fr.ListByMessage(ctx, msg.ID)
	require.NoError(t, err)
	require.Len(t, files, 1)
	require.Equal(t, f.ID, files[0].ID)
}

func TestFileRepo_ListByMessage(t *testing.T) {
	fr, mr, cr, ur, ctx := newFileRepo(t)
	u := mkUser(t, ur, ctx, "alice")
	ch := mkMsgChannel(t, cr, ctx, "list", u.ID)
	msg := &Message{ChannelID: ch.ID, SenderID: u.ID, Content: "multi attach", MsgType: 1}
	require.NoError(t, mr.Send(ctx, msg))

	f1 := mkFile(t, fr, ctx, u.ID, "a.pdf")
	f2 := mkFile(t, fr, ctx, u.ID, "b.pdf")
	require.NoError(t, fr.AttachToMessage(ctx, msg.ID, f1.ID))
	require.NoError(t, fr.AttachToMessage(ctx, msg.ID, f2.ID))

	// Unrelated file attached to nothing — must NOT show up.
	mkFile(t, fr, ctx, u.ID, "unrelated.pdf")

	got, err := fr.ListByMessage(ctx, msg.ID)
	require.NoError(t, err)
	require.Len(t, got, 2)
	require.Equal(t, f1.ID, got[0].ID, "ordered by file id ascending")
	require.Equal(t, f2.ID, got[1].ID)
	require.Equal(t, "a.pdf", got[0].FileName)
	require.Equal(t, "b.pdf", got[1].FileName)
}

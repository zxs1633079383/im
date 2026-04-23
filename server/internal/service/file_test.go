package service_test

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"

	"im-server/internal/repo"
	"im-server/internal/repo/mocks"
	"im-server/internal/service"
)

func TestFile_Upload_Success(t *testing.T) {
	dir := t.TempDir()
	m := mocks.NewFileRepoMock(t)

	var captured *repo.File
	m.EXPECT().Create(mock.Anything, mock.Anything).
		Run(func(_ context.Context, f *repo.File) {
			f.ID = 42
			captured = f
		}).
		Return(nil)

	svc := service.NewFileService(m, dir)
	body := "hello world"
	got, err := svc.Upload(context.Background(), service.UploadInput{
		UploaderID: 7,
		FileName:   "hello.txt",
		MimeType:   "text/plain; charset=utf-8",
		Size:       int64(len(body)),
		Content:    strings.NewReader(body),
	})
	require.NoError(t, err)
	require.NotNil(t, got)
	require.Equal(t, int64(42), got.ID)
	require.Equal(t, int64(7), got.UploaderID)
	require.Equal(t, "hello.txt", got.FileName)
	require.Equal(t, int64(len(body)), got.FileSize)
	require.Equal(t, "text/plain", got.MimeType, "mime parameters should be stripped")
	require.True(t, strings.HasPrefix(got.StoragePath, dir), "storage path under uploadDir")
	require.NotNil(t, captured)

	// File on disk must contain exactly what we uploaded.
	read, err := os.ReadFile(got.StoragePath)
	require.NoError(t, err)
	require.Equal(t, body, string(read))
}

func TestFile_Upload_TooLarge(t *testing.T) {
	dir := t.TempDir()
	m := mocks.NewFileRepoMock(t)
	svc := service.NewFileService(m, dir)

	_, err := svc.Upload(context.Background(), service.UploadInput{
		UploaderID: 1,
		FileName:   "big.bin",
		Size:       service.MaxUploadSize + 1,
		Content:    bytes.NewReader(nil),
	})
	require.ErrorIs(t, err, service.ErrFileTooLarge)
}

func TestFile_Upload_RepoFailureCleansUpFile(t *testing.T) {
	dir := t.TempDir()
	m := mocks.NewFileRepoMock(t)
	boom := errors.New("db down")
	m.EXPECT().Create(mock.Anything, mock.Anything).Return(boom)

	svc := service.NewFileService(m, dir)
	_, err := svc.Upload(context.Background(), service.UploadInput{
		UploaderID: 1,
		FileName:   "x.txt",
		Size:       3,
		Content:    strings.NewReader("xyz"),
	})
	require.ErrorIs(t, err, boom)

	// Walk the upload dir — no orphan files should remain.
	count := 0
	_ = filepath.Walk(dir, func(_ string, info os.FileInfo, _ error) error {
		if info != nil && !info.IsDir() {
			count++
		}
		return nil
	})
	require.Equal(t, 0, count, "orphan file must be cleaned up on repo failure")
}

func TestFile_Upload_DefaultMime(t *testing.T) {
	dir := t.TempDir()
	m := mocks.NewFileRepoMock(t)
	m.EXPECT().Create(mock.Anything, mock.Anything).Return(nil)

	svc := service.NewFileService(m, dir)
	got, err := svc.Upload(context.Background(), service.UploadInput{
		UploaderID: 1,
		FileName:   "x",
		Size:       1,
		Content:    strings.NewReader("a"),
	})
	require.NoError(t, err)
	require.Equal(t, "application/octet-stream", got.MimeType)
}

func TestFile_Download_Success(t *testing.T) {
	dir := t.TempDir()
	storagePath := filepath.Join(dir, "stored.bin")
	require.NoError(t, os.WriteFile(storagePath, []byte("payload"), 0o644))

	m := mocks.NewFileRepoMock(t)
	m.EXPECT().GetByID(mock.Anything, int64(5)).Return(&repo.File{
		ID:          5,
		FileName:    "stored.bin",
		FileSize:    7,
		MimeType:    "application/octet-stream",
		StoragePath: storagePath,
	}, nil)

	svc := service.NewFileService(m, dir)
	meta, rc, err := svc.Download(context.Background(), 5)
	require.NoError(t, err)
	defer rc.Close()
	require.Equal(t, int64(5), meta.ID)

	read, err := io.ReadAll(rc)
	require.NoError(t, err)
	require.Equal(t, "payload", string(read))
}

func TestFile_Download_NotFound(t *testing.T) {
	m := mocks.NewFileRepoMock(t)
	m.EXPECT().GetByID(mock.Anything, int64(999)).Return(nil, repo.ErrNotFound)

	svc := service.NewFileService(m, t.TempDir())
	_, _, err := svc.Download(context.Background(), 999)
	require.ErrorIs(t, err, repo.ErrNotFound)
}

func TestFile_Download_DiskMissing(t *testing.T) {
	m := mocks.NewFileRepoMock(t)
	m.EXPECT().GetByID(mock.Anything, int64(5)).Return(&repo.File{
		ID:          5,
		StoragePath: filepath.Join(t.TempDir(), "does-not-exist"),
	}, nil)

	svc := service.NewFileService(m, t.TempDir())
	_, _, err := svc.Download(context.Background(), 5)
	require.Error(t, err)
}

func TestFile_ListAttachments_Empty(t *testing.T) {
	m := mocks.NewFileRepoMock(t)
	m.EXPECT().ListByMessage(mock.Anything, int64(7)).Return(nil, nil)

	svc := service.NewFileService(m, t.TempDir())
	got, err := svc.ListAttachments(context.Background(), 7)
	require.NoError(t, err)
	require.NotNil(t, got, "empty list must not be nil")
	require.Len(t, got, 0)
}

func TestFile_ListAttachments_Many(t *testing.T) {
	m := mocks.NewFileRepoMock(t)
	want := []repo.File{
		{ID: 1, FileName: "a.txt"},
		{ID: 2, FileName: "b.txt"},
	}
	m.EXPECT().ListByMessage(mock.Anything, int64(7)).Return(want, nil)

	svc := service.NewFileService(m, t.TempDir())
	got, err := svc.ListAttachments(context.Background(), 7)
	require.NoError(t, err)
	require.Equal(t, want, got)
}

func TestFile_ListAttachments_RepoError(t *testing.T) {
	m := mocks.NewFileRepoMock(t)
	boom := errors.New("db down")
	m.EXPECT().ListByMessage(mock.Anything, int64(7)).Return(nil, boom)

	svc := service.NewFileService(m, t.TempDir())
	_, err := svc.ListAttachments(context.Background(), 7)
	require.ErrorIs(t, err, boom)
}

func TestFile_SanitizeFilename(t *testing.T) {
	cases := []struct {
		in, want string
	}{
		{"hello.txt", "hello.txt"},
		{"../etc/passwd", "passwd"},
		{"weird name with spaces.png", "weird_name_with_spaces.png"},
		{"a/b/c/d.go", "d.go"},
		{"", "upload"},
		{".", "upload"},
		{"a*b?c.txt", "a_b_c.txt"},
	}
	for _, tc := range cases {
		require.Equal(t, tc.want, service.SanitizeFilename(tc.in), "input=%q", tc.in)
	}
}

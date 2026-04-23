package http_test

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"

	"im-server/internal/auth"
	imhttp "im-server/internal/http"
	"im-server/internal/middleware"
	"im-server/internal/repo"
	"im-server/internal/repo/mocks"
	"im-server/internal/service"
	"im-server/internal/testutil"
)

func setupFileHandler(t *testing.T) (*gin.Engine, *mocks.FileRepoMock, string) {
	t.Helper()
	gin.SetMode(gin.TestMode)
	dir := t.TempDir()
	m := mocks.NewFileRepoMock(t)
	svc := service.NewFileService(m, dir)
	r := gin.New()
	authed := r.Group("/api")
	authed.Use(middleware.JWTGin(testSecret))
	imhttp.RegisterFileRoutes(authed, svc, nil)
	return r, m, dir
}

func newFileToken(t *testing.T, uid int64, username string) string {
	t.Helper()
	tok, err := auth.GenerateToken(testSecret, uid, username)
	require.NoError(t, err)
	return tok
}

func TestFileHandler_Upload_NoToken_401(t *testing.T) {
	r, _, _ := setupFileHandler(t)
	testutil.NewExpect(t, r).POST("/api/files").
		WithMultipart().WithFileBytes("file", "x.txt", []byte("hi")).
		Expect().Status(401)
}

func TestFileHandler_Upload_OK(t *testing.T) {
	r, m, dir := setupFileHandler(t)
	tok := newFileToken(t, 7, "alice")

	m.EXPECT().Create(mock.Anything, mock.Anything).
		Run(func(_ context.Context, f *repo.File) { f.ID = 99 }).
		Return(nil)

	body := "hello world"
	testutil.NewExpect(t, r).POST("/api/files").
		WithHeader("Authorization", "Bearer "+tok).
		WithMultipart().
		WithFileBytes("file", "hello.txt", []byte(body)).
		Expect().Status(201).JSON().Object().
		Value("id").IsEqual(99)

	// Verify a file was actually written under the upload dir.
	count := 0
	_ = filepath.Walk(dir, func(_ string, info os.FileInfo, _ error) error {
		if info != nil && !info.IsDir() {
			count++
		}
		return nil
	})
	require.Equal(t, 1, count, "exactly one file should be written")
}

func TestFileHandler_Upload_MissingFileField_400(t *testing.T) {
	r, _, _ := setupFileHandler(t)
	tok := newFileToken(t, 1, "alice")

	testutil.NewExpect(t, r).POST("/api/files").
		WithHeader("Authorization", "Bearer "+tok).
		WithMultipart().
		Expect().Status(400)
}

func TestFileHandler_Upload_NeverExposesStoragePath(t *testing.T) {
	r, m, _ := setupFileHandler(t)
	tok := newFileToken(t, 7, "alice")

	m.EXPECT().Create(mock.Anything, mock.Anything).
		Run(func(_ context.Context, f *repo.File) { f.ID = 1 }).
		Return(nil)

	// repo.File tags storage_path as json:"-", so it must not appear in the
	// response — protect against accidental tag changes that would leak the
	// on-disk layout to clients.
	body := testutil.NewExpect(t, r).POST("/api/files").
		WithHeader("Authorization", "Bearer "+tok).
		WithMultipart().
		WithFileBytes("file", "hello.txt", []byte("hi")).
		Expect().Status(201).Body().Raw()

	require.NotContains(t, body, "storage_path")
}

func TestFileHandler_Download_NotFound(t *testing.T) {
	r, m, _ := setupFileHandler(t)
	tok := newFileToken(t, 1, "alice")

	m.EXPECT().GetByID(mock.Anything, int64(999)).Return(nil, repo.ErrNotFound)

	testutil.NewExpect(t, r).GET("/api/files/999").
		WithHeader("Authorization", "Bearer "+tok).
		Expect().Status(404)
}

func TestFileHandler_Download_OK(t *testing.T) {
	r, m, dir := setupFileHandler(t)
	tok := newFileToken(t, 1, "alice")

	storagePath := filepath.Join(dir, "stored.bin")
	require.NoError(t, os.WriteFile(storagePath, []byte("payload"), 0o644))

	m.EXPECT().GetByID(mock.Anything, int64(5)).Return(&repo.File{
		ID:          5,
		FileName:    "report.txt",
		FileSize:    7,
		MimeType:    "text/plain",
		StoragePath: storagePath,
	}, nil)

	resp := testutil.NewExpect(t, r).GET("/api/files/5").
		WithHeader("Authorization", "Bearer "+tok).
		Expect().Status(200)

	resp.Header("Content-Type").Contains("text/plain")
	resp.Header("Content-Disposition").Contains("report.txt")
	resp.Body().IsEqual("payload")
}

func TestFileHandler_Download_BadID_400(t *testing.T) {
	r, _, _ := setupFileHandler(t)
	tok := newFileToken(t, 1, "alice")

	testutil.NewExpect(t, r).GET("/api/files/not-an-int").
		WithHeader("Authorization", "Bearer "+tok).
		Expect().Status(400)
}

func TestFileHandler_ListAttachments_Empty(t *testing.T) {
	r, m, _ := setupFileHandler(t)
	tok := newFileToken(t, 1, "alice")

	m.EXPECT().ListByMessage(mock.Anything, int64(7)).Return(nil, nil)

	testutil.NewExpect(t, r).GET("/api/messages/7/attachments").
		WithHeader("Authorization", "Bearer "+tok).
		Expect().Status(200).JSON().Object().
		Value("files").Array().IsEmpty()
}

func TestFileHandler_ListAttachments_Some(t *testing.T) {
	r, m, _ := setupFileHandler(t)
	tok := newFileToken(t, 1, "alice")

	m.EXPECT().ListByMessage(mock.Anything, int64(7)).Return([]repo.File{
		{ID: 1, FileName: "a.txt"},
		{ID: 2, FileName: "b.txt"},
	}, nil)

	testutil.NewExpect(t, r).GET("/api/messages/7/attachments").
		WithHeader("Authorization", "Bearer "+tok).
		Expect().Status(200).JSON().Object().
		Value("files").Array().Length().IsEqual(2)
}

func TestFileHandler_ListAttachments_BadID_400(t *testing.T) {
	r, _, _ := setupFileHandler(t)
	tok := newFileToken(t, 1, "alice")

	testutil.NewExpect(t, r).GET("/api/messages/abc/attachments").
		WithHeader("Authorization", "Bearer "+tok).
		Expect().Status(400)
}

// Catches a regression: large multipart strings must not be truncated by Gin's
// default 32 MB form-memory limit when streamed directly to disk.
func TestFileHandler_Upload_LargerBody(t *testing.T) {
	r, m, _ := setupFileHandler(t)
	tok := newFileToken(t, 7, "alice")

	m.EXPECT().Create(mock.Anything, mock.Anything).
		Run(func(_ context.Context, f *repo.File) { f.ID = 1 }).
		Return(nil)

	body := strings.Repeat("a", 1<<20) // 1 MB
	testutil.NewExpect(t, r).POST("/api/files").
		WithHeader("Authorization", "Bearer "+tok).
		WithMultipart().
		WithFileBytes("file", "big.txt", []byte(body)).
		Expect().Status(201).JSON().Object().
		Value("file_size").IsEqual(len(body))
}

package handler_test

import (
	"bytes"
	"context"
	"io"
	"log/slog"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"

	"im-server/internal/handler"
	"im-server/internal/repo"
)

// ---------- stubs ----------

type stubFileStore struct {
	files  map[int64]*repo.File
	nextID int64
}

func newStubFileStore() *stubFileStore {
	return &stubFileStore{files: make(map[int64]*repo.File), nextID: 1}
}

func (s *stubFileStore) Create(_ context.Context, f *repo.File) error {
	f.ID = s.nextID
	s.nextID++
	cp := *f
	s.files[f.ID] = &cp
	return nil
}

func (s *stubFileStore) GetByID(_ context.Context, id int64) (*repo.File, error) {
	f, ok := s.files[id]
	if !ok {
		return nil, handler.ErrNotFound
	}
	return f, nil
}

func (s *stubFileStore) AttachToMessage(_ context.Context, _, _ int64) error { return nil }

func (s *stubFileStore) ListByMessage(_ context.Context, _ int64) ([]repo.File, error) {
	return []repo.File{}, nil
}

// ---------- helpers ----------

func fileTestLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(os.Stderr, nil))
}

// ---------- tests ----------

func TestFileUpload_RequiresAuth(t *testing.T) {
	h := handler.NewFileHandler(newStubFileStore(), t.TempDir(), fileTestLogger())
	req := httptest.NewRequest(http.MethodPost, "/api/files", nil)
	w := httptest.NewRecorder()
	h.Upload(w, req)
	if w.Code != http.StatusUnauthorized {
		t.Errorf("want 401, got %d", w.Code)
	}
}

func TestFileUpload_MissingFileField(t *testing.T) {
	h := handler.NewFileHandler(newStubFileStore(), t.TempDir(), fileTestLogger())

	body := &bytes.Buffer{}
	mw := multipart.NewWriter(body)
	mw.Close()

	req := httptest.NewRequest(http.MethodPost, "/api/files", body)
	req.Header.Set("Content-Type", mw.FormDataContentType())
	req = withClaims(req, 1, "alice")
	w := httptest.NewRecorder()
	h.Upload(w, req)
	if w.Code != http.StatusBadRequest {
		t.Errorf("want 400, got %d", w.Code)
	}
}

func TestFileUpload_Success(t *testing.T) {
	h := handler.NewFileHandler(newStubFileStore(), t.TempDir(), fileTestLogger())

	body := &bytes.Buffer{}
	mw := multipart.NewWriter(body)
	fw, _ := mw.CreateFormFile("file", "hello.txt")
	io.WriteString(fw, "hello world") //nolint:errcheck
	mw.Close()

	req := httptest.NewRequest(http.MethodPost, "/api/files", body)
	req.Header.Set("Content-Type", mw.FormDataContentType())
	req = withClaims(req, 1, "alice")
	w := httptest.NewRecorder()
	h.Upload(w, req)
	if w.Code != http.StatusCreated {
		t.Errorf("want 201, got %d: %s", w.Code, w.Body.String())
	}
}

func TestFileDownload_NotFound(t *testing.T) {
	h := handler.NewFileHandler(newStubFileStore(), t.TempDir(), fileTestLogger())
	req := httptest.NewRequest(http.MethodGet, "/api/files/999", nil)
	req = withClaims(req, 1, "alice")
	req.SetPathValue("id", "999")
	w := httptest.NewRecorder()
	h.Download(w, req)
	if w.Code != http.StatusNotFound {
		t.Errorf("want 404, got %d", w.Code)
	}
}

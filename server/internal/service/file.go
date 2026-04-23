// Package service — Phase 7.7 file service. Wraps repo.FileRepo with the
// thin storage-layer logic (sanitization + safe path construction) that
// previously lived inline in handler.FileHandler. The HTTP transport owns
// multipart parsing; the service owns disk + repo writes.
package service

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"im-server/internal/repo"
)

// MaxUploadSize bounds the multipart payload accepted by FileService.Upload.
// Mirrors the legacy handler.maxUploadSize constant exactly.
const MaxUploadSize = 50 << 20 // 50 MB

// ErrFileTooLarge is returned by Upload when the supplied size exceeds
// MaxUploadSize. The transport layer maps this to 413 Request Entity Too Large.
var ErrFileTooLarge = errors.New("file exceeds maximum upload size")

// FileStore is the subset of repo.FileRepo used by FileService — keeps the
// interface narrow per the "accept interfaces, return structs" Go idiom.
type FileStore interface {
	Create(ctx context.Context, f *repo.File) error
	GetByID(ctx context.Context, id int64) (*repo.File, error)
	ListByMessage(ctx context.Context, messageID int64) ([]repo.File, error)
}

// FileService persists multipart uploads to a directory on disk and records
// metadata via repo.FileRepo. Download streams reopen the stored file by ID.
type FileService struct {
	files     FileStore
	uploadDir string
	now       func() time.Time // overridable for deterministic storage paths in tests
}

// NewFileService wires the supplied store and upload directory. An empty
// uploadDir falls back to /data/uploads to preserve the legacy default.
func NewFileService(files FileStore, uploadDir string) *FileService {
	if uploadDir == "" {
		uploadDir = "/data/uploads"
	}
	return &FileService{files: files, uploadDir: uploadDir, now: time.Now}
}

// UploadInput carries the per-request data Upload needs. Pulled out of the
// method signature so the transport layer can construct it from either a
// multipart.FileHeader (Gin's c.FormFile) or any other source — the service
// only depends on size, name, mime, and an io.Reader.
type UploadInput struct {
	UploaderID int64
	FileName   string
	MimeType   string
	Size       int64
	Content    io.Reader
}

// Upload writes in.Content to disk under uploadDir/<year>/<month>/ and inserts
// a corresponding repo.File row. Returns the populated *repo.File (with ID set
// by the repo) on success. Storage layout matches the legacy handler verbatim:
//
//	<uploadDir>/<YYYY>/<MM>/<uploaderID>_<unixNano>_<sanitizedFilename>
//
// Errors:
//   - ErrFileTooLarge when in.Size > MaxUploadSize
//   - wrapped fs/repo errors otherwise (transport maps to 500)
func (s *FileService) Upload(ctx context.Context, in UploadInput) (*repo.File, error) {
	ctx, span := tracer.Start(ctx, "FileService.Upload")
	defer span.End()

	if in.Size > MaxUploadSize {
		return nil, ErrFileTooLarge
	}

	mimeType := normalizeMime(in.MimeType)

	now := s.now()
	subDir := filepath.Join(s.uploadDir, fmt.Sprintf("%d/%02d", now.Year(), now.Month()))
	if err := os.MkdirAll(subDir, 0o755); err != nil {
		return nil, fmt.Errorf("create upload dir: %w", err)
	}

	safeName := SanitizeFilename(in.FileName)
	storageName := fmt.Sprintf("%d_%d_%s", in.UploaderID, now.UnixNano(), safeName)
	storagePath := filepath.Join(subDir, storageName)

	dst, err := os.Create(storagePath)
	if err != nil {
		return nil, fmt.Errorf("create file: %w", err)
	}

	written, copyErr := io.Copy(dst, in.Content)
	closeErr := dst.Close()
	if copyErr != nil {
		_ = os.Remove(storagePath)
		return nil, fmt.Errorf("write file: %w", copyErr)
	}
	if closeErr != nil {
		_ = os.Remove(storagePath)
		return nil, fmt.Errorf("close file: %w", closeErr)
	}

	f := &repo.File{
		UploaderID:  in.UploaderID,
		FileName:    in.FileName,
		FileSize:    written,
		MimeType:    mimeType,
		StoragePath: storagePath,
	}
	if err := s.files.Create(ctx, f); err != nil {
		// Best-effort cleanup if the metadata insert fails — orphan files
		// otherwise accumulate.
		_ = os.Remove(storagePath)
		return nil, fmt.Errorf("create file record: %w", err)
	}
	return f, nil
}

// Download returns the file metadata and an open *os.File for streaming the
// body. The caller MUST close the returned io.ReadCloser. Returns
// repo.ErrNotFound when the metadata row is missing; wrapped fs errors when
// the on-disk file is missing or unreadable.
func (s *FileService) Download(ctx context.Context, fileID int64) (*repo.File, io.ReadCloser, error) {
	ctx, span := tracer.Start(ctx, "FileService.Download")
	defer span.End()

	f, err := s.files.GetByID(ctx, fileID)
	if err != nil {
		return nil, nil, err
	}
	rc, err := os.Open(f.StoragePath)
	if err != nil {
		return nil, nil, fmt.Errorf("open file: %w", err)
	}
	return f, rc, nil
}

// ListAttachments returns all files attached to messageID in the order
// returned by the repo. A message with no attachments yields an empty slice
// (never nil) so transport layers can safely range without checking.
func (s *FileService) ListAttachments(ctx context.Context, messageID int64) ([]repo.File, error) {
	ctx, span := tracer.Start(ctx, "FileService.ListAttachments")
	defer span.End()

	files, err := s.files.ListByMessage(ctx, messageID)
	if err != nil {
		return nil, err
	}
	if files == nil {
		files = []repo.File{}
	}
	return files, nil
}

// SanitizeFilename strips path separators and replaces any non-alphanumeric
// character (other than '.', '-', '_') with '_'. Mirrors the legacy
// handler.sanitizeFilename helper exactly so the on-disk layout is preserved
// across the cut-over.
func SanitizeFilename(name string) string {
	base := filepath.Base(name)
	var sb strings.Builder
	for _, c := range base {
		if (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') || (c >= '0' && c <= '9') ||
			c == '.' || c == '-' || c == '_' {
			sb.WriteRune(c)
		} else {
			sb.WriteRune('_')
		}
	}
	result := sb.String()
	if result == "" || result == "." {
		result = "upload"
	}
	return result
}

// normalizeMime drops parameters from a Content-Type header and falls back to
// application/octet-stream when empty.
func normalizeMime(mimeType string) string {
	if mimeType == "" {
		return "application/octet-stream"
	}
	if idx := strings.Index(mimeType, ";"); idx >= 0 {
		mimeType = strings.TrimSpace(mimeType[:idx])
	}
	return mimeType
}

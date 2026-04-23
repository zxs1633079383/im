package handler

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"im-server/internal/repo"
)

const maxUploadSize = 50 << 20 // 50 MB

// ---------- store interface ----------

// FileStoreIface is the subset of repo.FileRepo used by FileHandler.
type FileStoreIface interface {
	Create(ctx context.Context, f *repo.File) error
	GetByID(ctx context.Context, id int64) (*repo.File, error)
	AttachToMessage(ctx context.Context, messageID, fileID int64) error
	ListByMessage(ctx context.Context, messageID int64) ([]repo.File, error)
}

// ---------- handler ----------

// FileHandler handles file upload and download.
type FileHandler struct {
	files     FileStoreIface
	uploadDir string
	log       *slog.Logger
}

func NewFileHandler(files FileStoreIface, uploadDir string, log *slog.Logger) *FileHandler {
	if uploadDir == "" {
		uploadDir = "/data/uploads"
	}
	return &FileHandler{files: files, uploadDir: uploadDir, log: log}
}

// ---------- POST /api/files ----------

// Upload handles multipart file upload.
// Form field: "file" (required).
// Returns the created File model (without storage_path for security).
func (h *FileHandler) Upload(w http.ResponseWriter, r *http.Request) {
	claims, ok := claimsFromCtx(r)
	if !ok {
		writeError(w, http.StatusUnauthorized, "unauthorized")
		return
	}

	if err := r.ParseMultipartForm(maxUploadSize); err != nil {
		writeError(w, http.StatusBadRequest, "file too large or invalid multipart form")
		return
	}

	uploadedFile, header, err := r.FormFile("file")
	if err != nil {
		writeError(w, http.StatusBadRequest, "missing file field")
		return
	}
	defer uploadedFile.Close()

	if header.Size > maxUploadSize {
		writeError(w, http.StatusRequestEntityTooLarge, "file exceeds 50 MB limit")
		return
	}

	mimeType := header.Header.Get("Content-Type")
	if mimeType == "" {
		mimeType = "application/octet-stream"
	}
	// Only allow the type header part (ignore params)
	if idx := strings.Index(mimeType, ";"); idx >= 0 {
		mimeType = strings.TrimSpace(mimeType[:idx])
	}

	// Build a safe storage path: uploads/<year>/<month>/<userID>_<timestamp>_<filename>
	now := time.Now()
	subDir := filepath.Join(h.uploadDir, fmt.Sprintf("%d/%02d", now.Year(), now.Month()))
	if err := os.MkdirAll(subDir, 0o755); err != nil {
		h.log.Error("create upload dir", "error", err)
		writeError(w, http.StatusInternalServerError, "internal error")
		return
	}

	safeName := sanitizeFilename(header.Filename)
	storageName := fmt.Sprintf("%d_%d_%s", claims.UserID, now.UnixNano(), safeName)
	storagePath := filepath.Join(subDir, storageName)

	dst, err := os.Create(storagePath)
	if err != nil {
		h.log.Error("create file", "error", err)
		writeError(w, http.StatusInternalServerError, "internal error")
		return
	}
	defer dst.Close()

	written, err := io.Copy(dst, uploadedFile)
	if err != nil {
		h.log.Error("write file", "error", err)
		writeError(w, http.StatusInternalServerError, "internal error")
		return
	}

	f := &repo.File{
		UploaderID:  claims.UserID,
		FileName:    header.Filename,
		FileSize:    written,
		MimeType:    mimeType,
		StoragePath: storagePath,
	}

	if err := h.files.Create(r.Context(), f); err != nil {
		h.log.Error("create file record", "error", err)
		writeError(w, http.StatusInternalServerError, "internal error")
		return
	}

	// Never expose storage_path to the client (it's tagged json:"-" on repo.File)
	writeJSON(w, http.StatusCreated, f)
}

// ---------- GET /api/files/{id} ----------

// Download serves the file content for a given file ID.
func (h *FileHandler) Download(w http.ResponseWriter, r *http.Request) {
	_, ok := claimsFromCtx(r)
	if !ok {
		writeError(w, http.StatusUnauthorized, "unauthorized")
		return
	}

	fileID, ok := pathID(r, "id")
	if !ok {
		writeError(w, http.StatusBadRequest, "invalid file id")
		return
	}

	f, err := h.files.GetByID(r.Context(), fileID)
	if err != nil {
		writeError(w, http.StatusNotFound, "file not found")
		return
	}

	w.Header().Set("Content-Type", f.MimeType)
	w.Header().Set("Content-Disposition", fmt.Sprintf(`attachment; filename=%q`, f.FileName))
	w.Header().Set("Content-Length", strconv.FormatInt(f.FileSize, 10))
	http.ServeFile(w, r, f.StoragePath)
}

// ---------- GET /api/messages/{id}/attachments ----------

// ListAttachments returns all files attached to a message.
func (h *FileHandler) ListAttachments(w http.ResponseWriter, r *http.Request) {
	_, ok := claimsFromCtx(r)
	if !ok {
		writeError(w, http.StatusUnauthorized, "unauthorized")
		return
	}

	messageID, ok := pathID(r, "id")
	if !ok {
		writeError(w, http.StatusBadRequest, "invalid message id")
		return
	}

	files, err := h.files.ListByMessage(r.Context(), messageID)
	if err != nil {
		h.log.Error("list attachments", "error", err)
		writeError(w, http.StatusInternalServerError, "internal error")
		return
	}
	if files == nil {
		files = []repo.File{}
	}
	writeJSON(w, http.StatusOK, map[string][]repo.File{"files": files})
}

// ---------- helpers ----------

func sanitizeFilename(name string) string {
	base := filepath.Base(name)
	// Replace any character that isn't alphanumeric, dot, hyphen, underscore
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

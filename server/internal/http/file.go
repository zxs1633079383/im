package http

import (
	"errors"
	"fmt"
	"log/slog"
	"strconv"

	"github.com/gin-gonic/gin"

	"im-server/internal/repo"
	"im-server/internal/service"
)

// RegisterFileRoutes wires the file upload, download, and message-attachment
// list endpoints onto authed. authed must already have JWT middleware applied
// (see RegisterProfileRoutes for the contract).
//
//	POST /api/files                          — multipart upload, field "file"
//	GET  /api/files/:id                      — stream the stored file
//	GET  /api/messages/:id/attachments       — list files attached to a message
//
// log is optional — pass nil to fall back to slog.Default(). Used for
// non-fatal 500 detail.
func RegisterFileRoutes(authed *gin.RouterGroup, svc *service.FileService, log *slog.Logger) {
	if log == nil {
		log = slog.Default()
	}

	authed.POST("/files", func(c *gin.Context) {
		uid, ok := userIDFromCtx(c)
		if !ok {
			return
		}

		// Cap the request body before ParseMultipartForm — Gin re-uses the
		// http.Request so this protects against memory exhaustion the same
		// way the legacy ParseMultipartForm(maxUploadSize) did.
		header, err := c.FormFile("file")
		if err != nil {
			c.JSON(400, gin.H{"error": "missing file field"})
			return
		}

		if header.Size > service.MaxUploadSize {
			c.JSON(413, gin.H{"error": "file exceeds 50 MB limit"})
			return
		}

		opened, err := header.Open()
		if err != nil {
			log.Error("open uploaded file", "error", err)
			c.JSON(500, gin.H{"error": "internal error"})
			return
		}
		defer opened.Close()

		f, err := svc.Upload(c.Request.Context(), service.UploadInput{
			UploaderID: uid,
			FileName:   header.Filename,
			MimeType:   header.Header.Get("Content-Type"),
			Size:       header.Size,
			Content:    opened,
		})
		switch {
		case errors.Is(err, service.ErrFileTooLarge):
			c.JSON(413, gin.H{"error": "file exceeds 50 MB limit"})
		case err != nil:
			log.Error("upload file", "user_id", uid, "error", err)
			c.JSON(500, gin.H{"error": "internal error"})
		default:
			c.JSON(201, f)
		}
	})

	authed.GET("/files/:id", func(c *gin.Context) {
		if _, ok := userIDFromCtx(c); !ok {
			return
		}

		fileID, ok := parsePathID(c, "id")
		if !ok {
			c.JSON(400, gin.H{"error": "invalid file id"})
			return
		}

		meta, rc, err := svc.Download(c.Request.Context(), fileID)
		switch {
		case errors.Is(err, repo.ErrNotFound):
			c.JSON(404, gin.H{"error": "file not found"})
			return
		case err != nil:
			log.Error("download file", "file_id", fileID, "error", err)
			c.JSON(500, gin.H{"error": "internal error"})
			return
		}
		defer rc.Close()

		c.Header("Content-Disposition", fmt.Sprintf("attachment; filename=%q", meta.FileName))
		// DataFromReader sets Content-Type, Content-Length, and copies the body
		// — equivalent to the legacy http.ServeFile minus range support, which
		// the legacy handler also did not advertise.
		c.DataFromReader(200, meta.FileSize, meta.MimeType, rc, nil)
	})

	authed.GET("/messages/:id/attachments", func(c *gin.Context) {
		if _, ok := userIDFromCtx(c); !ok {
			return
		}

		messageID, ok := parsePathID(c, "id")
		if !ok {
			c.JSON(400, gin.H{"error": "invalid message id"})
			return
		}

		files, err := svc.ListAttachments(c.Request.Context(), messageID)
		if err != nil {
			log.Error("list attachments", "message_id", messageID, "error", err)
			c.JSON(500, gin.H{"error": "internal error"})
			return
		}
		c.JSON(200, gin.H{"files": files})
	})
}

// parsePathID extracts a path parameter as int64, returning (0, false) for
// missing or unparseable values. Mirrors handler.pathID for the legacy parity.
func parsePathID(c *gin.Context, name string) (int64, bool) {
	s := c.Param(name)
	if s == "" {
		return 0, false
	}
	v, err := strconv.ParseInt(s, 10, 64)
	if err != nil {
		return 0, false
	}
	return v, true
}

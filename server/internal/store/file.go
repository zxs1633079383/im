package store

import (
	"context"
	"fmt"

	"github.com/jackc/pgx/v5/pgxpool"
	"im-server/internal/model"
)

// FileStore manages file metadata and message attachment associations.
type FileStore struct {
	pool *pgxpool.Pool
}

func NewFileStore(pool *pgxpool.Pool) *FileStore {
	return &FileStore{pool: pool}
}

// Create inserts a file record and sets f.ID and f.CreatedAt.
func (s *FileStore) Create(ctx context.Context, f *model.File) error {
	err := s.pool.QueryRow(ctx,
		`INSERT INTO files (uploader_id, file_name, file_size, mime_type, storage_path, thumbnail_path)
		 VALUES ($1, $2, $3, $4, $5, $6)
		 RETURNING id, created_at`,
		f.UploaderID, f.FileName, f.FileSize, f.MimeType, f.StoragePath, f.ThumbnailPath,
	).Scan(&f.ID, &f.CreatedAt)
	if err != nil {
		return fmt.Errorf("insert file: %w", err)
	}
	return nil
}

// GetByID returns a single file record.
func (s *FileStore) GetByID(ctx context.Context, id int64) (*model.File, error) {
	f := &model.File{}
	err := s.pool.QueryRow(ctx,
		`SELECT id, uploader_id, file_name, file_size, mime_type, storage_path, thumbnail_path, created_at
		 FROM files WHERE id = $1`, id,
	).Scan(&f.ID, &f.UploaderID, &f.FileName, &f.FileSize, &f.MimeType,
		&f.StoragePath, &f.ThumbnailPath, &f.CreatedAt)
	if err != nil {
		return nil, fmt.Errorf("get file: %w", err)
	}
	return f, nil
}

// AttachToMessage creates a row in message_attachments linking a file to a message.
func (s *FileStore) AttachToMessage(ctx context.Context, messageID, fileID int64) error {
	_, err := s.pool.Exec(ctx,
		`INSERT INTO message_attachments (message_id, file_id)
		 VALUES ($1, $2)
		 ON CONFLICT DO NOTHING`,
		messageID, fileID,
	)
	if err != nil {
		return fmt.Errorf("attach file to message: %w", err)
	}
	return nil
}

// ListByMessage returns all files attached to a message, ordered by attachment position.
func (s *FileStore) ListByMessage(ctx context.Context, messageID int64) ([]model.File, error) {
	rows, err := s.pool.Query(ctx,
		`SELECT f.id, f.uploader_id, f.file_name, f.file_size, f.mime_type,
		        f.storage_path, f.thumbnail_path, f.created_at
		 FROM files f
		 JOIN message_attachments ma ON ma.file_id = f.id
		 WHERE ma.message_id = $1
		 ORDER BY ma.id`,
		messageID,
	)
	if err != nil {
		return nil, fmt.Errorf("list files by message: %w", err)
	}
	defer rows.Close()

	var files []model.File
	for rows.Next() {
		var f model.File
		if err := rows.Scan(&f.ID, &f.UploaderID, &f.FileName, &f.FileSize, &f.MimeType,
			&f.StoragePath, &f.ThumbnailPath, &f.CreatedAt); err != nil {
			return nil, fmt.Errorf("scan file: %w", err)
		}
		files = append(files, f)
	}
	return files, rows.Err()
}

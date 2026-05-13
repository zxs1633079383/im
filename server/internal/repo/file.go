package repo

import (
	"context"
	"errors"
	"fmt"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// FileRepo manages file metadata and message_attachments associations.
//
// AttachToMessage is idempotent via OnConflict DoNothing on the
// (message_id, file_id) composite primary key — matches the existing
// store.FileStore INSERT ... ON CONFLICT DO NOTHING semantics.
type FileRepo interface {
	Create(ctx context.Context, f *File) error
	GetByID(ctx context.Context, id string) (*File, error)
	AttachToMessage(ctx context.Context, messageID string, fileID string) error
	ListByMessage(ctx context.Context, messageID string) ([]File, error)
}

type gormFileRepo struct{ db *gorm.DB }

// NewFileRepo returns a GORM-backed FileRepo.
func NewFileRepo(db *gorm.DB) FileRepo { return &gormFileRepo{db: db} }

func (r *gormFileRepo) Create(ctx context.Context, f *File) error {
	if err := r.db.WithContext(ctx).Create(f).Error; err != nil {
		return fmt.Errorf("insert file: %w", err)
	}
	return nil
}

func (r *gormFileRepo) GetByID(ctx context.Context, id string) (*File, error) {
	var f File
	if err := r.db.WithContext(ctx).First(&f, id).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, ErrNotFound
		}
		return nil, fmt.Errorf("get file: %w", err)
	}
	return &f, nil
}

func (r *gormFileRepo) AttachToMessage(ctx context.Context, messageID string, fileID string) error {
	a := MessageAttachment{MessageID: messageID, FileID: fileID}
	if err := r.db.WithContext(ctx).
		Clauses(clause.OnConflict{DoNothing: true}).
		Create(&a).Error; err != nil {
		return fmt.Errorf("attach file to message: %w", err)
	}
	return nil
}

func (r *gormFileRepo) ListByMessage(ctx context.Context, messageID string) ([]File, error) {
	var files []File
	err := r.db.WithContext(ctx).Raw(`
		SELECT f.* FROM files f
		JOIN message_attachments ma ON ma.file_id = f.id
		WHERE ma.message_id = ?
		ORDER BY f.id
	`, messageID).Scan(&files).Error
	if err != nil {
		return nil, fmt.Errorf("list files by message: %w", err)
	}
	return files, nil
}

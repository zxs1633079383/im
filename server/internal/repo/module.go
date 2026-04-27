package repo

import (
	"context"
	"fmt"

	"gorm.io/gorm"
)

// ModuleRepo is a read-only fetcher for the module catalog. The table is
// seeded from migration 016 (six fixed entries mirroring mattermost csesapi);
// no API mutates it at runtime. Splitting list off into its own repo keeps
// channel/message hot paths unaware of this lookup table.
type ModuleRepo interface {
	List(ctx context.Context) ([]Module, error)
}

type gormModuleRepo struct{ db *gorm.DB }

// NewModuleRepo wires the supplied GORM connection.
func NewModuleRepo(db *gorm.DB) ModuleRepo { return &gormModuleRepo{db: db} }

// List returns every module ordered by name (alphabetical) — stable order
// for the client's "module entry card" rendering, lower row count (6 in
// production) means the index isn't worth maintaining.
func (r *gormModuleRepo) List(ctx context.Context) ([]Module, error) {
	var out []Module
	if err := r.db.WithContext(ctx).Order("name ASC").Find(&out).Error; err != nil {
		return nil, fmt.Errorf("module list: %w", err)
	}
	return out, nil
}

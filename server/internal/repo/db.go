// Package repo holds GORM-backed data access for the IM service.
//
// Phase 5 of the cloud-native migration replaces the hand-written pgx
// store with GORM repositories. db.go provides shared connection setup;
// individual repository files (user.go, channel.go, etc.) consume *gorm.DB.
package repo

import (
	"fmt"
	"time"

	"gorm.io/driver/postgres"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
	"gorm.io/plugin/opentelemetry/tracing"
)

// Config configures Open. DSN is required; the rest have sane defaults.
//
// Defaults mirror the Java HikariCP config used by the sibling cses service
// (maximum-pool-size=300, idle-timeout=300s, max-lifetime=600s) so the
// Go-side gateway sustains comparable concurrency on the same PG instance.
type Config struct {
	DSN             string
	MaxOpen         int             // default 300  (was 25)
	MaxIdle         int             // default 30   (was 5)
	ConnMaxLifetime time.Duration   // default 10m  (was 30m; matches Java max-lifetime=600s)
	ConnMaxIdleTime time.Duration   // default 5m   (new; matches Java idle-timeout=300s)
	LogLevel        logger.LogLevel // default Warn
}

// Open returns a configured *gorm.DB with the OTel tracing plugin attached.
// Caller owns the underlying sql.DB — close on shutdown.
func Open(cfg Config) (*gorm.DB, error) {
	if cfg.MaxOpen == 0 {
		cfg.MaxOpen = 300
	}
	if cfg.MaxIdle == 0 {
		cfg.MaxIdle = 30
	}
	if cfg.ConnMaxLifetime == 0 {
		cfg.ConnMaxLifetime = 10 * time.Minute
	}
	if cfg.ConnMaxIdleTime == 0 {
		cfg.ConnMaxIdleTime = 5 * time.Minute
	}
	if cfg.LogLevel == 0 {
		cfg.LogLevel = logger.Warn
	}

	db, err := gorm.Open(postgres.Open(cfg.DSN), &gorm.Config{
		Logger:                                   logger.Default.LogMode(cfg.LogLevel),
		PrepareStmt:                              true,
		DisableForeignKeyConstraintWhenMigrating: true,
	})
	if err != nil {
		return nil, fmt.Errorf("gorm open: %w", err)
	}

	if err := db.Use(tracing.NewPlugin(tracing.WithoutMetrics())); err != nil {
		return nil, fmt.Errorf("otel tracing plugin: %w", err)
	}

	sqlDB, err := db.DB()
	if err != nil {
		return nil, fmt.Errorf("get sql.DB: %w", err)
	}
	sqlDB.SetMaxOpenConns(cfg.MaxOpen)
	sqlDB.SetMaxIdleConns(cfg.MaxIdle)
	sqlDB.SetConnMaxLifetime(cfg.ConnMaxLifetime)
	sqlDB.SetConnMaxIdleTime(cfg.ConnMaxIdleTime)
	return db, nil
}

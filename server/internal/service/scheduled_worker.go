package service

import (
	"context"
	"log/slog"
	"time"
)

// DefaultScheduledTick is the polling cadence used when no override is
// supplied via ScheduledWorkerConfig.Tick.
const DefaultScheduledTick = 10 * time.Second

// DefaultScheduledBatch is the per-tick batch size — how many due rows the
// worker claims in a single FetchDue round.
const DefaultScheduledBatch = 50

// ScheduledWorkerConfig tunes the worker's poll cadence + batch size.
type ScheduledWorkerConfig struct {
	Tick  time.Duration // polling interval; zero → DefaultScheduledTick
	Batch int           // per-tick batch size; zero → DefaultScheduledBatch
}

// ScheduledWorker is a background poller that drives *ScheduledService.Deliver
// for every row whose scheduled_at has passed and status is still pending.
//
// Multi-pod safety: the worker relies on the ScheduledRepo.MarkDelivered
// WHERE status = pending guard — if two pods race on the same row, only one
// UPDATE succeeds and the duplicate Deliver call surfaces ErrNotFound which
// the worker logs and skips. A follow-up SELECT … FOR UPDATE SKIP LOCKED can
// tighten this if needed.
type ScheduledWorker struct {
	svc *ScheduledService
	log *slog.Logger
	cfg ScheduledWorkerConfig
}

// NewScheduledWorker constructs a worker with sensible defaults. Pass a zero
// ScheduledWorkerConfig to accept every default.
func NewScheduledWorker(svc *ScheduledService, log *slog.Logger, cfg ScheduledWorkerConfig) *ScheduledWorker {
	if cfg.Tick <= 0 {
		cfg.Tick = DefaultScheduledTick
	}
	if cfg.Batch <= 0 {
		cfg.Batch = DefaultScheduledBatch
	}
	if log == nil {
		log = slog.Default()
	}
	return &ScheduledWorker{svc: svc, log: log, cfg: cfg}
}

// Run blocks until ctx is cancelled. Each tick it fetches up to Batch due
// rows, delivers them sequentially, and logs any failures at WARN. A single
// slow delivery can bleed into the next tick; that's acceptable — Deliver
// failures mark the row failed so nothing is re-tried forever.
func (w *ScheduledWorker) Run(ctx context.Context) {
	ticker := time.NewTicker(w.cfg.Tick)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			w.log.Info("scheduled worker stopping")
			return
		case <-ticker.C:
			w.tick(ctx)
		}
	}
}

// tick runs a single poll cycle. Exposed for tests.
func (w *ScheduledWorker) tick(ctx context.Context) {
	rows, err := w.svc.FetchDue(ctx, time.Now().UTC(), w.cfg.Batch)
	if err != nil {
		w.log.Warn("scheduled worker: fetch due failed", "error", err)
		return
	}
	for i := range rows {
		sm := rows[i]
		if _, err := w.svc.Deliver(ctx, &sm); err != nil {
			w.log.Warn("scheduled worker: deliver failed", "error", err, "id", sm.ID)
		}
	}
}

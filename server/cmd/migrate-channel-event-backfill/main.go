// migrate-channel-event-backfill provisions the per-channel PG sequences
// and back-fills the channel_event log for every channel that pre-existed
// the C017/C018/C019 cut-over. It is idempotent — re-running on an
// already-migrated database is a no-op (the SQL relies on CREATE SEQUENCE
// IF NOT EXISTS, ON CONFLICT DO NOTHING, and a NOT EXISTS guard on the
// event back-fill INSERT).
//
// Run:
//
//	# default — pull DSN from config.yaml in CWD (matches Makefile pattern)
//	cd server && go run ./cmd/migrate-channel-event-backfill
//
//	# override with an explicit DSN
//	DATABASE_URL='postgres://im:im@localhost:5432/im?sslmode=disable' \
//	  go run ./cmd/migrate-channel-event-backfill
//
//	# or via Makefile
//	make migrate-channel-event-backfill
//
// Per-channel work runs inside its own transaction so an error on channel
// N doesn't roll back channels 0..N-1. The summary line at the end prints
// channel count + total events back-filled.
package main

import (
	"context"
	"database/sql"
	"flag"
	"fmt"
	"log"
	"os"
	"regexp"
	"time"

	_ "github.com/lib/pq"
	"gopkg.in/yaml.v3"
)

// sanitizeID mirrors repo.sanitizeID — duplicated here to keep this binary
// dependency-free of the repo package (avoids dragging gorm into a one-off
// migration tool). [A-Za-z0-9_-] is the only allowed charset for PG
// identifier construction (C018 §3.2).
var sanitizeIDRe = regexp.MustCompile(`[^A-Za-z0-9_-]`)

func sanitizeID(id string) string {
	return sanitizeIDRe.ReplaceAllString(id, "")
}

type minimalPGConfig struct {
	PG struct {
		DSN string `yaml:"dsn"`
	} `yaml:"pg"`
}

func resolveDSN(configPath string) (string, error) {
	if v := os.Getenv("DATABASE_URL"); v != "" {
		return v, nil
	}
	if v := os.Getenv("IM_PG_DSN"); v != "" {
		return v, nil
	}
	if configPath == "" {
		return "", fmt.Errorf("DATABASE_URL not set and no -config provided")
	}
	data, err := os.ReadFile(configPath)
	if err != nil {
		return "", fmt.Errorf("read %s: %w", configPath, err)
	}
	var cfg minimalPGConfig
	if err := yaml.Unmarshal(data, &cfg); err != nil {
		return "", fmt.Errorf("parse %s: %w", configPath, err)
	}
	if cfg.PG.DSN == "" {
		return "", fmt.Errorf("%s has empty pg.dsn", configPath)
	}
	return cfg.PG.DSN, nil
}

func main() {
	var (
		configPath = flag.String("config", "config.yaml", "path to config.yaml (used when DATABASE_URL is unset)")
		dryRun     = flag.Bool("dry-run", false, "print the work plan without executing INSERT/CREATE SEQUENCE")
		batchLog   = flag.Int("log-every", 50, "log progress every N channels")
	)
	flag.Parse()

	dsn, err := resolveDSN(*configPath)
	if err != nil {
		log.Fatalf("resolve DSN: %v", err)
	}

	db, err := sql.Open("postgres", dsn)
	if err != nil {
		log.Fatalf("open db: %v", err)
	}
	defer db.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Minute)
	defer cancel()
	if err := db.PingContext(ctx); err != nil {
		log.Fatalf("ping db: %v", err)
	}

	channelIDs, err := listChannels(ctx, db)
	if err != nil {
		log.Fatalf("list channels: %v", err)
	}
	log.Printf("found %d channels to process (dry-run=%v)", len(channelIDs), *dryRun)

	var (
		processed int
		totalNew  int64
	)
	for i, chID := range channelIDs {
		newEvents, err := processChannel(ctx, db, chID, *dryRun)
		if err != nil {
			log.Printf("[%s] FAILED: %v", chID, err)
			continue
		}
		processed++
		totalNew += newEvents
		if (i+1)%*batchLog == 0 || i == len(channelIDs)-1 {
			log.Printf("progress: %d/%d channels, %d events backfilled so far",
				i+1, len(channelIDs), totalNew)
		}
	}

	log.Printf("DONE: %d/%d channels processed, %d total events backfilled (dry-run=%v)",
		processed, len(channelIDs), totalNew, *dryRun)
}

func listChannels(ctx context.Context, db *sql.DB) ([]string, error) {
	rows, err := db.QueryContext(ctx, `SELECT id FROM channels ORDER BY id`)
	if err != nil {
		return nil, fmt.Errorf("query channels: %w", err)
	}
	defer rows.Close()
	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, fmt.Errorf("scan channel id: %w", err)
		}
		ids = append(ids, id)
	}
	return ids, rows.Err()
}

// processChannel runs the per-channel idempotent migration inside one tx:
//  1. Compute max(messages.seq) — used as the START position for the msg
//     sequence so future allocations don't collide with historical rows.
//  2. CREATE SEQUENCE IF NOT EXISTS channel_msg_seq_<id>   START <maxMsg+1> CACHE 50
//  3. CREATE SEQUENCE IF NOT EXISTS channel_event_seq_<id> START 1          CACHE 100
//  4. INSERT INTO channel_sequence_meta ON CONFLICT DO NOTHING
//  5. INSERT INTO channel_event from messages WHERE NOT EXISTS — historical
//     messages get an EventTypeNew row ordered by their existing seq, so the
//     sync log carries a complete timeline going back as far as messages do.
//  6. SELECT setval(...) so the next nextval() doesn't collide with the
//     synthetic event rows we just inserted.
//
// Returns the number of event rows actually backfilled (zero on re-run).
//
// CREATE SEQUENCE cannot run inside a tx that has already touched the new
// sequence; we issue them outside the data-modifying portion. PG actually
// supports CREATE SEQUENCE inside a tx with rollback semantics, so one tx
// for the whole channel is fine — only `setval()` must be deferred to a
// separate statement (its result is per-session, not per-tx).
func processChannel(ctx context.Context, db *sql.DB, channelID string, dryRun bool) (int64, error) {
	safe := sanitizeID(channelID)
	if safe == "" {
		return 0, fmt.Errorf("channelID %q sanitises to empty", channelID)
	}
	msgSeq := "channel_msg_seq_" + safe
	eventSeq := "channel_event_seq_" + safe

	var maxMsgSeq int64
	err := db.QueryRowContext(ctx,
		`SELECT COALESCE(MAX(seq), 0) FROM messages WHERE channel_id = $1`,
		channelID,
	).Scan(&maxMsgSeq)
	if err != nil {
		return 0, fmt.Errorf("max msg seq: %w", err)
	}

	if dryRun {
		log.Printf("[%s] would: CREATE %s START %d / CREATE %s START 1 / backfill events",
			channelID, msgSeq, maxMsgSeq+1, eventSeq)
		return 0, nil
	}

	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return 0, fmt.Errorf("begin tx: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	startMsg := maxMsgSeq + 1
	// Double-quote identifiers so UUID-shaped channel ids (which contain
	// hyphens) survive PG identifier parsing.
	if _, err := tx.ExecContext(ctx,
		fmt.Sprintf(`CREATE SEQUENCE IF NOT EXISTS "%s" START %d CACHE 50`, msgSeq, startMsg),
	); err != nil {
		return 0, fmt.Errorf("create msg seq: %w", err)
	}
	if _, err := tx.ExecContext(ctx,
		fmt.Sprintf(`CREATE SEQUENCE IF NOT EXISTS "%s" START 1 CACHE 100`, eventSeq),
	); err != nil {
		return 0, fmt.Errorf("create event seq: %w", err)
	}
	if _, err := tx.ExecContext(ctx,
		`INSERT INTO channel_sequence_meta (channel_id, msg_seq_name, event_seq_name, created_at)
		 VALUES ($1, $2, $3, $4)
		 ON CONFLICT (channel_id) DO NOTHING`,
		channelID, msgSeq, eventSeq, time.Now().UnixMilli(),
	); err != nil {
		return 0, fmt.Errorf("insert meta: %w", err)
	}

	// Back-fill channel_event rows for historical messages. We synthesise an
	// EventTypeNew (1) row per message, ordered by seq so event_seq tracks
	// the original send order. The NOT EXISTS guard makes the statement
	// idempotent — re-running this command after fresh edits / writes won't
	// duplicate older rows.
	res, err := tx.ExecContext(ctx,
		`INSERT INTO channel_event
		     (channel_id, event_seq, event_type, msg_id, actor_id, created_at)
		 SELECT
		     m.channel_id,
		     ROW_NUMBER() OVER (PARTITION BY m.channel_id ORDER BY m.seq),
		     1,
		     m.id,
		     m.sender_id,
		     (EXTRACT(EPOCH FROM m.created_at) * 1000)::BIGINT
		 FROM messages m
		 WHERE m.channel_id = $1
		   AND NOT EXISTS (
		       SELECT 1 FROM channel_event ce
		       WHERE ce.channel_id = m.channel_id
		         AND ce.msg_id = m.id
		   )
		 ORDER BY m.seq`,
		channelID,
	)
	if err != nil {
		return 0, fmt.Errorf("backfill events: %w", err)
	}
	newEvents, _ := res.RowsAffected()

	// After back-fill, align the event sequence so the next nextval() does
	// not collide with the synthetic rows. We read max(event_seq) instead of
	// relying on the INSERT's RowsAffected because re-runs of this binary
	// would otherwise undercount.
	var maxEventSeq int64
	if err := tx.QueryRowContext(ctx,
		`SELECT COALESCE(MAX(event_seq), 0) FROM channel_event WHERE channel_id = $1`,
		channelID,
	).Scan(&maxEventSeq); err != nil {
		return 0, fmt.Errorf("max event seq: %w", err)
	}
	if maxEventSeq > 0 {
		if _, err := tx.ExecContext(ctx,
			fmt.Sprintf(`SELECT setval('"%s"', %d)`, eventSeq, maxEventSeq),
		); err != nil {
			return 0, fmt.Errorf("setval event seq: %w", err)
		}
	}

	if err := tx.Commit(); err != nil {
		return 0, fmt.Errorf("commit: %w", err)
	}
	return newEvents, nil
}

package main

import (
	"context"
	"database/sql"
	"fmt"
	"time"
)

// sqliteDSN builds the production connection string. modernc.org/sqlite
// takes pragmas as `_pragma=name(value)` query params — the previous
// `_journal=WAL&_timeout=5000` was mattn/go-sqlite3 syntax, which modernc
// silently ignored, so the DB ran with journal DELETE and busy_timeout 0
// and every concurrent write surfaced as SQLITE_BUSY (the nightly
// snapshot/sweep failures and dropped pings). Applied per connection.
//
// _txlock=immediate makes every BeginTx issue BEGIN IMMEDIATE. busy_timeout
// alone is not enough: in WAL mode a deferred transaction that reads first
// and writes second fails at once with SQLITE_BUSY or SQLITE_BUSY_SNAPSHOT
// if another connection committed in between, because its read snapshot is
// stale and waiting cannot fix that, so the busy handler is never consulted
// (snapshotDay hit this against live pings, "database is locked (5)/(517)").
// Taking the write lock up front turns that into an ordinary wait that
// busy_timeout covers. Every transaction in this server writes, and none is
// read-only (plain reads go through db.QueryRowContext, not BeginTx), so
// the handle-wide setting costs nothing and guards any future BeginTx.
func sqliteDSN(dbPath string) string {
	return dbPath +
		"?_pragma=journal_mode(WAL)" +
		"&_pragma=busy_timeout(5000)" +
		"&_pragma=synchronous(NORMAL)" +
		"&_txlock=immediate"
}

// createAggregateTables creates the daily aggregate tables (Phase 4), the
// per-day activity ledger, and the meta table. One row per day for the
// global counts, one per (day, version) for the version split, one per
// (day, field) for feature adoption. Aggregates have perpetual retention
// so charts can extend beyond the 60-day window we keep on raw rows.
// Shared by main() and the test fixture so the schemas can't drift.
func createAggregateTables(ctx context.Context, db *sql.DB) error {
	for _, stmt := range []string{
		`CREATE TABLE IF NOT EXISTS daily_global (
			day          TEXT PRIMARY KEY,
			active_day   INTEGER NOT NULL DEFAULT 0,
			new_installs INTEGER NOT NULL DEFAULT 0,
			total        INTEGER NOT NULL DEFAULT 0
		)`,
		`CREATE TABLE IF NOT EXISTS daily_version (
			day          TEXT NOT NULL,
			version      TEXT NOT NULL,
			active_count INTEGER NOT NULL,
			PRIMARY KEY (day, version)
		)`,
		`CREATE TABLE IF NOT EXISTS daily_features (
			day              TEXT NOT NULL,
			field            TEXT NOT NULL,
			enabled_count    INTEGER NOT NULL,
			reporting_count  INTEGER NOT NULL,
			PRIMARY KEY (day, field)
		)`,
		// Per-day activity ledger: one row per (day, install) written at
		// ping time. This is the source of truth for "how many distinct
		// installs were active on day X" — unlike last_seen, which rolls
		// forward with every ping and so only ever attributes an install
		// to its single most recent day (making historical days read as
		// dormancy counts, not activity). version is the last version the
		// install pinged with that day, feeding the version-mix trend.
		`CREATE TABLE IF NOT EXISTS daily_activity (
			day        TEXT NOT NULL,
			install_id TEXT NOT NULL,
			version    TEXT NOT NULL,
			PRIMARY KEY (day, install_id)
		) WITHOUT ROWID`,
		`CREATE TABLE IF NOT EXISTS meta (
			key   TEXT PRIMARY KEY,
			value TEXT NOT NULL
		)`,
	} {
		if _, err := db.ExecContext(ctx, stmt); err != nil {
			return err
		}
	}
	return nil
}

// ledgerRetentionWindow bounds daily_activity growth. At ~1k active
// installs/day the ledger adds ~365k rows/year, so 400 days is a few tens
// of MB — kept long so daily_global/daily_version can always be recomputed
// from raw per-day truth well beyond the 60-day installs retention.
const ledgerRetentionWindow = 400 * 24 * time.Hour

// ledgerEpochKey is the meta row recording the first day the activity
// ledger collected live pings. Days before it were seeded from last_seen
// and undercount; the dashboard footnotes any chart window that spans it.
const ledgerEpochKey = "activity_ledger_epoch"

// seedActivityLedger backfills daily_activity from installs.last_seen when
// the ledger is empty (first boot after the migration) and records the
// epoch. Idempotent: a non-empty ledger or an existing epoch row makes it
// a no-op, so restarts and crashes mid-seed are safe (the INSERT and the
// epoch write share one transaction).
func seedActivityLedger(ctx context.Context, db *sql.DB) error {
	var n int
	if err := db.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM daily_activity`).Scan(&n); err != nil {
		return fmt.Errorf("count: %w", err)
	}
	if n > 0 {
		return nil
	}
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	if _, err := tx.ExecContext(ctx, `
		INSERT OR IGNORE INTO daily_activity (day, install_id, version)
		SELECT substr(last_seen, 1, 10), install_id, version FROM installs
	`); err != nil {
		return fmt.Errorf("seed: %w", err)
	}
	if _, err := tx.ExecContext(ctx, `
		INSERT OR IGNORE INTO meta (key, value) VALUES (?, ?)
	`, ledgerEpochKey, time.Now().UTC().Format("2006-01-02")); err != nil {
		return fmt.Errorf("epoch: %w", err)
	}
	return tx.Commit()
}

package main

import (
	"compress/gzip"
	"context"
	"database/sql"
	"encoding/csv"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"regexp"
	"strings"
	"time"

	sqlite "modernc.org/sqlite"
)

// aggregateInterval is how often runAggregateLoop fires. Same shape as
// retentionInterval; both run independently so a slow snapshot can't
// starve the eviction sweep.
var aggregateInterval = 24 * time.Hour

// snapshotDay writes the daily aggregate rows for `day` from the current
// installs table state. All three aggregate tables (global, version,
// features) are upserted in one transaction so a partial failure leaves
// no orphan rows. day is normalised to UTC YYYY-MM-DD on the way in.
//
// The active_day and version split come from the daily_activity ledger,
// which records every (day, install) pair at ping time — so snapshotting
// a historical day is exact (within ledger retention), not approximate,
// and a snapshot that fails one night is fully healed by a later run.
// readVersionCounts returns a map of version → count of installs active
// on dayStr, per the ledger. Used by snapshotDay so its INSERT loop runs
// after the read cursor has been released (defer covers all return paths).
func readVersionCounts(ctx context.Context, tx *sql.Tx, dayStr string) (map[string]int, error) {
	rows, err := tx.QueryContext(ctx, `
		SELECT version, COUNT(*) FROM daily_activity
		WHERE day = ?
		GROUP BY version
	`, dayStr)
	if err != nil {
		return nil, fmt.Errorf("version rows: %w", err)
	}
	defer rows.Close()
	out := make(map[string]int)
	for rows.Next() {
		var v string
		var n int
		if err := rows.Scan(&v, &n); err != nil {
			return nil, fmt.Errorf("version scan: %w", err)
		}
		out[v] = n
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("version iter: %w", err)
	}
	return out, nil
}

// snapshotAfterReads is a test seam: snapshotDay calls it after its first
// reads and before its first write, the window in which a deferred
// transaction's read snapshot can go stale. Nil in production.
var snapshotAfterReads func()

// snapshotMaxAttempts and snapshotRetryBackoff bound the retry of a tick
// that still hits a busy error (see retryBusy).
const snapshotMaxAttempts = 3

var snapshotRetryBackoff = 250 * time.Millisecond

// isBusy reports whether err is SQLite's SQLITE_BUSY family: plain busy (5),
// busy-snapshot (517), busy-recovery (261) and so on. Only these are
// transient lock conflicts worth retrying.
func isBusy(err error) bool {
	var se *sqlite.Error
	return errors.As(err, &se) && se.Code()&0xff == 5
}

// retryBusy runs fn up to snapshotMaxAttempts times, sleeping a growing
// backoff between attempts, and only while fn fails with a busy error. Any
// other error, and success, return at once. aggregateInterval is 24h, so
// without this a transient lock would leave the dashboard rows stale until
// the next tick, up to a day later.
func retryBusy(ctx context.Context, fn func() error) error {
	var err error
	for attempt := 1; attempt <= snapshotMaxAttempts; attempt++ {
		if err = fn(); err == nil || !isBusy(err) {
			return err
		}
		if attempt == snapshotMaxAttempts {
			break
		}
		select {
		case <-ctx.Done():
			return err
		case <-time.After(time.Duration(attempt) * snapshotRetryBackoff):
		}
	}
	return err
}

func (s *server) snapshotDay(ctx context.Context, day time.Time) error {
	dayStr := day.UTC().Format("2006-01-02")

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("snapshot %s: begin: %w", dayStr, err)
	}
	defer func() { _ = tx.Rollback() }()

	// daily_global: one row per day with active_day, new_installs, total.
	// These reads run inside the write transaction opened above (see
	// sqliteDSN), so no ping can commit between them and the writes below.
	var activeDay, newInstalls, total int
	if err := tx.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM daily_activity WHERE day = ?`, dayStr,
	).Scan(&activeDay); err != nil {
		return fmt.Errorf("snapshot %s: active_day: %w", dayStr, err)
	}
	if err := tx.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM installs WHERE substr(first_seen, 1, 10) = ?`, dayStr,
	).Scan(&newInstalls); err != nil {
		return fmt.Errorf("snapshot %s: new_installs: %w", dayStr, err)
	}
	if err := tx.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM installs`,
	).Scan(&total); err != nil {
		return fmt.Errorf("snapshot %s: total: %w", dayStr, err)
	}
	if snapshotAfterReads != nil {
		snapshotAfterReads()
	}
	if _, err := tx.ExecContext(ctx, `
		INSERT INTO daily_global (day, active_day, new_installs, total)
		VALUES (?, ?, ?, ?)
		ON CONFLICT(day) DO UPDATE SET
			active_day   = excluded.active_day,
			new_installs = MAX(daily_global.new_installs, excluded.new_installs),
			total        = excluded.total
	`, dayStr, activeDay, newInstalls, total); err != nil {
		return fmt.Errorf("snapshot %s: upsert global: %w", dayStr, err)
	}

	// daily_version: one row per (day, version) with the active count on
	// that day. Replace any prior rows for `day` first so versions that
	// dropped to zero aren't left stranded (which would happen if we just
	// upserted: the row from yesterday's snapshot stays at its old count).
	if _, err := tx.ExecContext(ctx,
		`DELETE FROM daily_version WHERE day = ?`, dayStr,
	); err != nil {
		return fmt.Errorf("snapshot %s: clear version: %w", dayStr, err)
	}
	// Buffer the version rows fully before issuing any INSERTs so the
	// scope of the read cursor is bounded by readVersionCounts.
	versionCounts, err := readVersionCounts(ctx, tx, dayStr)
	if err != nil {
		return fmt.Errorf("snapshot %s: %w", dayStr, err)
	}
	for v, n := range versionCounts {
		if _, err := tx.ExecContext(ctx,
			`INSERT INTO daily_version (day, version, active_count) VALUES (?, ?, ?)`,
			dayStr, v, n); err != nil {
			return fmt.Errorf("snapshot %s: version insert: %w", dayStr, err)
		}
	}

	// daily_features: one row per (day, field). reporting_count is the
	// denominator (installs active on day with non-null features) and is
	// the same for every field. enabled_count is the per-field numerator.
	// Membership ("active that day") comes from the ledger; the features
	// payload itself still comes from installs, which holds each install's
	// latest-reported state. Exact for today/yesterday snapshots; for older
	// heal-window days it reflects the install's current features rather
	// than that day's (per-day feature history would need features in the
	// ledger, which isn't worth the row size for this chart).
	var reporting int
	if err := tx.QueryRowContext(ctx, `
		SELECT COUNT(*) FROM installs i
		JOIN daily_activity a ON a.install_id = i.install_id AND a.day = ?
		WHERE i.features IS NOT NULL
	`, dayStr).Scan(&reporting); err != nil {
		return fmt.Errorf("snapshot %s: features reporting: %w", dayStr, err)
	}
	if _, err := tx.ExecContext(ctx,
		`DELETE FROM daily_features WHERE day = ?`, dayStr,
	); err != nil {
		return fmt.Errorf("snapshot %s: clear features: %w", dayStr, err)
	}
	if reporting > 0 {
		for _, f := range featureFields {
			var enabled int
			if err := tx.QueryRowContext(ctx, `
				SELECT COUNT(*) FROM installs i
				JOIN daily_activity a ON a.install_id = i.install_id AND a.day = ?
				WHERE i.features IS NOT NULL
				  AND json_extract(i.features, '$.'||?) > 0
			`, dayStr, f.JSONKey).Scan(&enabled); err != nil {
				return fmt.Errorf("snapshot %s: feature %s: %w", dayStr, f.JSONKey, err)
			}
			if _, err := tx.ExecContext(ctx, `
				INSERT INTO daily_features (day, field, enabled_count, reporting_count)
				VALUES (?, ?, ?, ?)
			`, dayStr, f.JSONKey, enabled, reporting); err != nil {
				return fmt.Errorf("snapshot %s: feature insert %s: %w", dayStr, f.JSONKey, err)
			}
		}
	}

	if err := tx.Commit(); err != nil {
		return fmt.Errorf("snapshot %s: commit: %w", dayStr, err)
	}
	return nil
}

// backfillNewInstalls populates daily_global.new_installs for every day in
// the installs table's first_seen range. Only this column is historically
// recoverable; the others depend on last_seen which has rolled forward.
//
// Idempotent AND monotonic: the conflict clause raises new_installs but never
// lowers it (MAX). This matters because the source is COUNT(*) over installs,
// which the 60-day retention sweep erodes for old days — once the churned
// rows first-seen on day D are GC'd, recomputing D from survivors yields a
// smaller count. Without the MAX guard, every restart would clobber a
// correctly-recorded historical value downward. new_installs is genuinely
// monotonic (a day's true count only ever grew, on the day itself), so MAX is
// the correct reconciliation: whichever snapshot saw the day freshest wins.
//
// Runs once at startup. Cheap: one INSERT ... SELECT against an indexed
// substring; the installs table holds at most ~5k rows at our scale.
func (s *server) backfillNewInstalls(ctx context.Context) error {
	_, err := s.db.ExecContext(ctx, `
		INSERT INTO daily_global (day, active_day, new_installs, total)
		SELECT substr(first_seen, 1, 10) AS day, 0, COUNT(*), 0
		  FROM installs
		 WHERE first_seen IS NOT NULL
		 GROUP BY day
		ON CONFLICT(day) DO UPDATE SET
			new_installs = MAX(daily_global.new_installs, excluded.new_installs)
	`)
	return err
}

// dayKeyRE matches a YYYY-MM-DD seed key. Keys that don't match are skipped
// so a malformed seed file can't inject junk rows into daily_global.
var dayKeyRE = regexp.MustCompile(`^\d{4}-\d{2}-\d{2}$`)

// reconcileNewInstallsSeed raises daily_global.new_installs for each day in a
// {day: count} JSON file to at least the seeded value, never lowering it. It
// restores historical counts that a pre-monotonic backfill eroded once the
// underlying installs aged past the 60-day retention window (those raw rows
// are gone, so the only source of the true count is an external snapshot).
//
// The seed is aggregate-only (day -> integer count); it carries no install
// IDs or any per-install data. Idempotent: re-running with the same seed is a
// no-op. Returns the number of days whose stored value was actually raised.
func (s *server) reconcileNewInstallsSeed(ctx context.Context, path string) (int, error) {
	raw, err := os.ReadFile(path) // #nosec G304 -- path is an operator-supplied env var, not user input
	if err != nil {
		return 0, fmt.Errorf("read seed: %w", err)
	}
	var seed map[string]int
	if err := json.Unmarshal(raw, &seed); err != nil {
		return 0, fmt.Errorf("parse seed: %w", err)
	}

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return 0, fmt.Errorf("begin: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	raised := 0
	for day, count := range seed {
		if !dayKeyRE.MatchString(day) {
			slog.Warn("seed reconcile: skipping malformed day key", "key", day)
			continue
		}
		if count < 0 {
			continue
		}
		res, err := tx.ExecContext(ctx, `
			INSERT INTO daily_global (day, active_day, new_installs, total)
			VALUES (?, 0, ?, 0)
			ON CONFLICT(day) DO UPDATE SET
				new_installs = excluded.new_installs
			WHERE excluded.new_installs > daily_global.new_installs
		`, day, count)
		if err != nil {
			return 0, fmt.Errorf("upsert %s: %w", day, err)
		}
		if n, _ := res.RowsAffected(); n > 0 {
			raised++
		}
	}
	if err := tx.Commit(); err != nil {
		return 0, fmt.Errorf("commit: %w", err)
	}
	return raised, nil
}

// loadLedgerBackfill imports historical (day, install_id, version) rows
// into the daily_activity ledger from an operator-supplied CSV (gzipped
// when the path ends in .gz). Recovery path for days that predate the
// ledger: the rows are reconstructed offline from archived /api/backup
// snapshots (each install's last_seen day per snapshot) and ping log
// lines, then mounted into the pod. For every day touched it recomputes
// daily_global.active_day and the daily_version rows from the ledger —
// leaving new_installs and total alone, since those are historical values
// this data can't improve — and lowers the ledger epoch to the earliest
// imported day so the dashboard stops footnoting days that are now real.
//
// Runs once: the meta row ledger_backfill_done records the imported row
// count and the loader skips when it matches, so restarts don't re-import;
// shipping a bigger file re-runs it (INSERT OR IGNORE keeps that safe).
func (s *server) loadLedgerBackfill(ctx context.Context, path string) (int, error) {
	f, err := os.Open(path) // #nosec G304 -- operator-supplied env var, not user input
	if err != nil {
		return 0, fmt.Errorf("open backfill: %w", err)
	}
	defer f.Close()
	var r io.Reader = f
	if strings.HasSuffix(path, ".gz") {
		gz, err := gzip.NewReader(f)
		if err != nil {
			return 0, fmt.Errorf("gunzip backfill: %w", err)
		}
		defer func() { _ = gz.Close() }()
		r = gz
	}
	recs, err := csv.NewReader(r).ReadAll()
	if err != nil {
		return 0, fmt.Errorf("parse backfill: %w", err)
	}

	var done string
	if err := s.db.QueryRowContext(ctx,
		`SELECT COALESCE(MAX(value), '') FROM meta WHERE key = 'ledger_backfill_done'`,
	).Scan(&done); err != nil {
		return 0, fmt.Errorf("read done marker: %w", err)
	}
	if done == fmt.Sprint(len(recs)) {
		return 0, nil
	}

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return 0, fmt.Errorf("begin: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	inserted := 0
	days := map[string]bool{}
	minDay := ""
	for _, rec := range recs {
		if len(rec) != 3 {
			continue
		}
		day, id, version := rec[0], rec[1], rec[2]
		if !dayKeyRE.MatchString(day) || !uuidRE.MatchString(id) || !isReleaseVersion(version) {
			slog.Warn("ledger backfill: skipping malformed row", "day", day)
			continue
		}
		res, err := tx.ExecContext(ctx, `
			INSERT OR IGNORE INTO daily_activity (day, install_id, version)
			VALUES (?, ?, ?)
		`, day, id, normalizeVersion(version))
		if err != nil {
			return 0, fmt.Errorf("insert %s: %w", day, err)
		}
		if n, _ := res.RowsAffected(); n > 0 {
			inserted++
		}
		days[day] = true
		if minDay == "" || day < minDay {
			minDay = day
		}
	}

	// Re-derive the aggregates for every imported day from the (now
	// fuller) ledger. active_day and the version split only — see doc.
	for day := range days {
		if _, err := tx.ExecContext(ctx, `
			INSERT INTO daily_global (day, active_day, new_installs, total)
			SELECT ?, COUNT(*), 0, 0 FROM daily_activity WHERE day = ?
			ON CONFLICT(day) DO UPDATE SET active_day = excluded.active_day
		`, day, day); err != nil {
			return 0, fmt.Errorf("reaggregate global %s: %w", day, err)
		}
		if _, err := tx.ExecContext(ctx,
			`DELETE FROM daily_version WHERE day = ?`, day); err != nil {
			return 0, fmt.Errorf("clear version %s: %w", day, err)
		}
		if _, err := tx.ExecContext(ctx, `
			INSERT INTO daily_version (day, version, active_count)
			SELECT day, version, COUNT(*) FROM daily_activity
			WHERE day = ? GROUP BY version
		`, day); err != nil {
			return 0, fmt.Errorf("reaggregate version %s: %w", day, err)
		}
	}

	if minDay != "" {
		if _, err := tx.ExecContext(ctx, `
			UPDATE meta SET value = ? WHERE key = ? AND value > ?
		`, minDay, ledgerEpochKey, minDay); err != nil {
			return 0, fmt.Errorf("lower epoch: %w", err)
		}
	}
	if _, err := tx.ExecContext(ctx, `
		INSERT INTO meta (key, value) VALUES ('ledger_backfill_done', ?)
		ON CONFLICT(key) DO UPDATE SET value = excluded.value
	`, fmt.Sprint(len(recs))); err != nil {
		return 0, fmt.Errorf("done marker: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return 0, fmt.Errorf("commit: %w", err)
	}
	return inserted, nil
}

// aggregateHealDays is how many trailing days each aggregate tick
// re-snapshots. Snapshots read the daily_activity ledger, so recomputing a
// past day is exact — a tick that fails (or a process that was down over a
// rollover) is healed by any later tick inside this window.
const aggregateHealDays = 7

// runAggregateLoop drives snapshotDay on the same daily cadence as
// retention. Each tick snapshots today plus the trailing aggregateHealDays
// days from the ledger: today keeps the live dashboard row fresh, and the
// trailing window re-derives recent days so one failed or missed tick
// can't leave a permanently wrong aggregate row behind.
func (s *server) runAggregateLoop(ctx context.Context) {
	tick := time.NewTicker(aggregateInterval)
	defer tick.Stop()
	for {
		now := time.Now().UTC()
		for i := 0; i <= aggregateHealDays; i++ {
			day := now.AddDate(0, 0, -i)
			if err := retryBusy(ctx, func() error { return s.snapshotDay(ctx, day) }); err != nil {
				slog.Warn("snapshot failed", "days_ago", i, "error", err)
			}
		}
		select {
		case <-ctx.Done():
			return
		case <-tick.C:
		}
	}
}

package db

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
)

// DefaultRepairBatchSize is how many violating rows one repair transaction
// resolves before it commits and hands the connection back to the pool.
const DefaultRepairBatchSize = 500

// RepairOptions tunes RepairForeignKeyDrift.
type RepairOptions struct {
	// Path is the database file. With a real path the list of violating rows is
	// STREAMED from a short lived read only connection while the writes run on
	// the shared pool in small batches. Empty (or an in-memory or file: URI
	// database, which cannot be reopened) loads the list through the pool first.
	Path string
	// BatchSize is rows per transaction; zero means DefaultRepairBatchSize.
	BatchSize int
	// AfterBatch, when set, runs after each committed batch with the 1 based
	// batch number. Tests use it to cancel mid repair.
	AfterBatch func(batch int)
}

// IsReopenablePath reports whether path names a database file a second
// connection can open: not empty, not ":memory:" and not a "file:" URI.
func IsReopenablePath(path string) bool {
	return path != "" && path != ":memory:" && !strings.HasPrefix(path, "file:")
}

// RepairForeignKeyDrift is the running app's counterpart of
// RepairForeignKeyViolations. It resolves each violating row exactly the same
// way (both use the same per row repairer: CASCADE removes the row, SET NULL
// clears the reference, anything else is reported and left alone) but is built
// not to stall a live instance.
//
// The pool has ONE connection, so a repair that held it for its whole run would
// freeze /health, sign in and the UI, exactly as a scan on the pool did. So:
//
//   - finding the violating rows (the expensive full scan) runs on its own read
//     only connection and is streamed, never held in memory whole;
//   - writes happen in transactions of BatchSize rows. Between batches the
//     connection goes back to the pool and any request waiting for it is served
//     before the next batch starts, so the app is blocked for one batch at a
//     time (milliseconds), not for the repair;
//   - ctx is checked between batches and bounds every statement, so a cancel or
//     deadline stops the repair at a batch boundary.
//
// Atomicity: each batch is all or nothing, the whole repair is not. A repair
// that stops part way (cancelled, timed out, an error) leaves every committed
// batch in place and is safe to run again: it is idempotent, and each row is
// re-verified as still orphaned before it is touched. The caller takes the
// backup that makes the whole operation recoverable.
//
// Remaining is left zero: the caller runs a fresh scan to report what is left.
func RepairForeignKeyDrift(ctx context.Context, database *sql.DB, opts RepairOptions) (RepairReport, error) {
	size := opts.BatchSize
	if size <= 0 {
		size = DefaultRepairBatchSize
	}
	r := newRepairer()

	next, closeSrc, err := violationSource(ctx, database, opts.Path)
	if err != nil {
		return r.report, err
	}
	defer closeSrc()

	batch := make([]ForeignKeyViolation, 0, size)
	for n := 1; ; n++ {
		batch = batch[:0]
		for len(batch) < size {
			v, ok, err := next()
			if err != nil {
				return r.report, err
			}
			if !ok {
				break
			}
			batch = append(batch, v)
		}
		if len(batch) == 0 {
			return r.report, nil
		}
		if err := ctx.Err(); err != nil {
			return r.report, err
		}
		if err := r.applyBatch(ctx, database, batch); err != nil {
			return r.report, err
		}
		if opts.AfterBatch != nil {
			opts.AfterBatch(n)
		}
	}
}

// applyBatch resolves one batch in a single transaction. On error the batch is
// rolled back and the report goes back to what it was, so the counts only ever
// describe committed work.
func (r *repairer) applyBatch(ctx context.Context, database *sql.DB, batch []ForeignKeyViolation) error {
	saved := r.report
	savedNotes := len(r.report.SkipNotes)
	tx, err := database.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin repair batch: %w", err)
	}
	for _, v := range batch {
		if err := r.apply(ctx, tx, v); err != nil {
			_ = tx.Rollback()
			r.report = saved
			r.report.SkipNotes = r.report.SkipNotes[:savedNotes]
			return err
		}
	}
	if err := tx.Commit(); err != nil {
		r.report = saved
		r.report.SkipNotes = r.report.SkipNotes[:savedNotes]
		return fmt.Errorf("commit repair batch: %w", err)
	}
	return nil
}

// violationSource returns an iterator over every violating row and a closer.
func violationSource(ctx context.Context, database *sql.DB, path string) (func() (ForeignKeyViolation, bool, error), func(), error) {
	if !IsReopenablePath(path) {
		all, err := ForeignKeyViolations(ctx, database)
		if err != nil {
			return nil, nil, err
		}
		i := 0
		return func() (ForeignKeyViolation, bool, error) {
			if i >= len(all) {
				return ForeignKeyViolation{}, false, nil
			}
			i++
			return all[i-1], true, nil
		}, func() {}, nil
	}

	ro, err := sql.Open("sqlite", SQLiteFileURI(path, "mode=ro&_pragma=busy_timeout(5000)"))
	if err != nil {
		return nil, nil, fmt.Errorf("open %q read-only: %w", path, err)
	}
	ro.SetMaxOpenConns(1)
	rows, err := ro.QueryContext(ctx, `SELECT "table", "rowid", "parent", "fkid" FROM pragma_foreign_key_check`) //nolint:rowserrcheck // rows.Err() is checked where the iterator drains it, below
	if err != nil {
		_ = ro.Close()
		return nil, nil, fmt.Errorf("foreign_key_check: %w", err)
	}
	closeSrc := func() {
		_ = rows.Close()
		_ = ro.Close()
	}
	return func() (ForeignKeyViolation, bool, error) {
		if !rows.Next() {
			if err := rows.Err(); err != nil {
				return ForeignKeyViolation{}, false, fmt.Errorf("iterate foreign_key_check: %w", err)
			}
			return ForeignKeyViolation{}, false, nil
		}
		var v ForeignKeyViolation
		if err := rows.Scan(&v.Table, &v.RowID, &v.Parent, &v.FKID); err != nil {
			return ForeignKeyViolation{}, false, fmt.Errorf("scan foreign_key_check row: %w", err)
		}
		return v, true, nil
	}, closeSrc, nil
}

// ForeignKeyDriftFixableAt reports, for each relationship in found (same
// order), how many of its rows a repair can actually resolve, using its own
// short lived read only connection like the scan does. The scan counts every
// row foreign_key_check reports; a repair skips a row it cannot address (a
// WITHOUT ROWID table) or whose orphan status it cannot confirm with the same
// test it applies at repair time (a column affinity mismatch). The preview uses
// this so it does not promise more than the repair will do.
func ForeignKeyDriftFixableAt(ctx context.Context, path string, found []ForeignKeyDrift) ([]int, error) {
	ro, err := sql.Open("sqlite", SQLiteFileURI(path, "mode=ro&_pragma=busy_timeout(5000)"))
	if err != nil {
		return nil, fmt.Errorf("open %q read-only: %w", path, err)
	}
	defer func() { _ = ro.Close() }()
	ro.SetMaxOpenConns(1)
	return driftFixable(ctx, ro, found)
}

// ForeignKeyDriftFixable is ForeignKeyDriftFixableAt on the given pool, for
// databases that cannot be reopened from a path.
func ForeignKeyDriftFixable(ctx context.Context, database *sql.DB, found []ForeignKeyDrift) ([]int, error) {
	return driftFixable(ctx, database, found)
}

func driftFixable(ctx context.Context, q fkExecer, found []ForeignKeyDrift) ([]int, error) {
	out := make([]int, len(found))
	for i, d := range found {
		if DriftAction(d.OnDelete) == DriftActionKeep {
			continue
		}
		var withoutRowID int
		if err := scanOne(ctx, q, `SELECT "wr" FROM pragma_table_list(?) WHERE "schema" = 'main'`, d.ChildTable, &withoutRowID); err != nil {
			return nil, fmt.Errorf("table_list(%s): %w", d.ChildTable, err)
		}
		if withoutRowID != 0 {
			continue
		}
		actions, err := foreignKeyActions(ctx, q, d.ChildTable)
		if err != nil {
			return nil, err
		}
		total := 0
		for _, a := range actions {
			if a.parent != d.ParentTable || !strings.EqualFold(a.onDelete, d.OnDelete) {
				continue
			}
			if DriftAction(a.onDelete) == DriftActionClear {
				// SET NULL on a column that must have a value is refused by
				// the database for every row, so none of them is fixable.
				blocked, err := anyNotNull(ctx, q, d.ChildTable, a.columns)
				if err != nil {
					return nil, err
				}
				if blocked {
					continue
				}
			}
			guard, err := orphanGuard(ctx, q, d.ChildTable, a)
			if err != nil {
				continue // cannot be repaired, so not counted
			}
			var n int
			if err := scanOne(ctx, q, fmt.Sprintf(`SELECT COUNT(*) FROM %s WHERE %s`, quoteIdent(d.ChildTable), guard), "", &n); err != nil {
				return nil, fmt.Errorf("count repairable %s: %w", d.ChildTable, err)
			}
			total += n
		}
		out[i] = min(total, d.Violations)
	}
	return out, nil
}

// scanOne runs a query returning one integer column. arg, when non empty, is
// bound as the single parameter.
func scanOne(ctx context.Context, q fkExecer, query, arg string, dest *int, more ...any) error {
	var rows *sql.Rows
	var err error
	if arg != "" {
		rows, err = q.QueryContext(ctx, query, append([]any{arg}, more...)...)
	} else {
		rows, err = q.QueryContext(ctx, query)
	}
	if err != nil {
		return err
	}
	defer rows.Close()
	if rows.Next() {
		if err := rows.Scan(dest); err != nil {
			return err
		}
	}
	return rows.Err()
}

// anyNotNull reports whether any of the named columns of table is NOT NULL.
func anyNotNull(ctx context.Context, q fkExecer, table string, columns []string) (bool, error) {
	for _, col := range columns {
		var nn int
		if err := scanOne(ctx, q, `SELECT COALESCE(MAX("notnull"), 0) FROM pragma_table_info(?) WHERE "name" = ?`, table, &nn, col); err != nil {
			return false, fmt.Errorf("table_info(%s): %w", table, err)
		}
		if nn != 0 {
			return true, nil
		}
	}
	return false, nil
}

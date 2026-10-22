package db

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log/slog"
	"sort"
	"strings"

	"modernc.org/sqlite"
)

// This file holds the referential-integrity tooling that keeps a pre-existing
// orphan row from bricking an upgrade (#1972), plus the offline repair path
// operators need when an instance is already stuck.
//
// Background: until #1727, Bindery set `PRAGMA foreign_keys` once through
// db.Exec, so any connection database/sql opened to replace a bad one ran with
// foreign_keys=0 and the schema's ON DELETE CASCADE / SET NULL clauses silently
// stopped firing. #1727 stopped new drift; it never cleaned up what had already
// accumulated. Migration 072's post-rebuild gate then ran a database-WIDE
// `pragma_foreign_key_check` and treated every one of those historical orphans
// as a reason to abort — so the longer an instance had run, the more certain it
// was to refuse to start after upgrading to v1.30.1+.

// fkQueryer is the read-only surface foreignKeyViolationCounts needs, so it can
// run against either *sql.DB or the migration's *sql.Tx.
type fkQueryer interface {
	Query(query string, args ...any) (*sql.Rows, error)
}

// foreignKeyViolationCounts groups `PRAGMA foreign_key_check` over the whole
// database by child table. An empty map means the database is referentially
// clean. Tables absent from the map have no violations.
func foreignKeyViolationCounts(q fkQueryer) (map[string]int, error) {
	rows, err := q.Query(`SELECT "table", COUNT(*) FROM pragma_foreign_key_check GROUP BY "table"`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	counts := make(map[string]int)
	for rows.Next() {
		var table string
		var n int
		if err := rows.Scan(&table, &n); err != nil {
			return nil, err
		}
		counts[table] = n
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return counts, nil
}

// checkForeignKeyDelta is the gate a table-rebuilding migration is held to: it
// must not LEAVE BEHIND more broken references than it found.
//
// The scope is computed, not declared. Diffing a before/after snapshot pins the
// gate to the migration's own effect without anyone having to parse the SQL for
// table names or maintain a per-migration list that a future migration would
// forget to update — and it stays correct for a migration that rebuilds tables
// this one has never heard of.
//
// Violations that were already there are logged as a warning and allowed
// through. Losing an entire instance over drift a migration did not cause is
// never the right trade; the operator gets a named, actionable warning and a
// repair path instead of a stopped container.
func checkForeignKeyDelta(version int, before, after map[string]int) error {
	beforeTotal := 0
	for _, n := range before {
		beforeTotal += n
	}

	afterTotal := 0
	var introduced []string
	for table, n := range after {
		afterTotal += n
		if n > before[table] {
			introduced = append(introduced, fmt.Sprintf("%s=%d (was %d)", table, n, before[table]))
		}
	}
	sort.Strings(introduced)

	// The database-wide total must also have grown. A rebuild that renames a
	// table carries its pre-existing violations over to the new name, which
	// reads as "introduced" per-table while nothing was actually broken; the
	// total guard keeps that from aborting a migration.
	if len(introduced) > 0 && afterTotal > beforeTotal {
		return fmt.Errorf(
			"migration %d: the table rebuild introduced %d new foreign-key violation(s) in %s — "+
				"the migration was rolled back and your database is unchanged. "+
				"This is a bug in the migration, not damage to your data: please report it at "+
				"https://github.com/vavallee/bindery/issues with this message",
			version, afterTotal-beforeTotal, strings.Join(introduced, ", "))
	}

	if beforeTotal > 0 {
		slog.Warn("database carries pre-existing foreign-key violations from before this upgrade — "+
			"they are unrelated to this migration and were NOT treated as fatal (#1972)",
			"migration", version,
			"violations", beforeTotal,
			"tables", summariseCounts(before),
			"repair", "run `bindery db-check` to list the affected rows, `bindery db-repair --yes` to clean them up (see docs/Troubleshooting-Wiki.md)")
	}
	return nil
}

// summariseCounts renders a table->count map as a stable "a=1, b=2" string.
func summariseCounts(counts map[string]int) string {
	parts := make([]string, 0, len(counts))
	for table, n := range counts {
		parts = append(parts, fmt.Sprintf("%s=%d", table, n))
	}
	sort.Strings(parts)
	return strings.Join(parts, ", ")
}

// ForeignKeyViolation is one row reported by `PRAGMA foreign_key_check`: a row
// in Table whose foreign key FKID points at a Parent row that does not exist.
type ForeignKeyViolation struct {
	Table  string
	RowID  sql.NullInt64 // NULL for WITHOUT ROWID tables
	Parent string
	FKID   int
}

// OpenForMaintenance opens the database WITHOUT running migrations, for the
// offline `db-check` / `db-repair` subcommands. An instance that cannot get
// past migrations is exactly the one that needs these, so they must not depend
// on migrations succeeding.
func OpenForMaintenance(ctx context.Context, dbPath string) (*sql.DB, error) {
	database, err := sql.Open("sqlite", dbPath+connectionPragmaDSN)
	if err != nil {
		return nil, fmt.Errorf("open database %q: %w", dbPath, err)
	}
	database.SetMaxOpenConns(1)
	database.SetMaxIdleConns(1)
	if err := database.PingContext(ctx); err != nil {
		_ = database.Close()
		return nil, annotateCantOpen(dbPath, err)
	}
	return database, nil
}

// ForeignKeyViolations lists every row in the database whose foreign key points
// at a missing parent.
func ForeignKeyViolations(ctx context.Context, database *sql.DB) ([]ForeignKeyViolation, error) {
	rows, err := database.QueryContext(ctx, `SELECT "table", "rowid", "parent", "fkid" FROM pragma_foreign_key_check`)
	if err != nil {
		return nil, fmt.Errorf("foreign_key_check: %w", err)
	}
	defer rows.Close()

	var out []ForeignKeyViolation
	for rows.Next() {
		var v ForeignKeyViolation
		if err := rows.Scan(&v.Table, &v.RowID, &v.Parent, &v.FKID); err != nil {
			return nil, fmt.Errorf("scan foreign_key_check row: %w", err)
		}
		out = append(out, v)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate foreign_key_check: %w", err)
	}
	return out, nil
}

// SummariseViolations groups violations as "child -> parent" with a count,
// sorted, for a compact operator-facing report.
func SummariseViolations(violations []ForeignKeyViolation) []string {
	counts := make(map[string]int)
	for _, v := range violations {
		counts[fmt.Sprintf("%s -> %s", v.Table, v.Parent)]++
	}
	keys := make([]string, 0, len(counts))
	for k := range counts {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	out := make([]string, 0, len(keys))
	for _, k := range keys {
		out = append(out, fmt.Sprintf("%s: %d", k, counts[k]))
	}
	return out
}

// fkAction describes what the schema says should have happened to a child row
// when its parent was deleted.
type fkAction struct {
	onDelete   string
	columns    []string // the child-side column(s) of this foreign key
	parent     string   // the parent table
	parentCols []string // the parent-side column(s); empty means the parent's primary key
}

// fkExecer is the surface the repair needs, satisfied by *sql.DB and *sql.Tx.
// A batch runs on a *sql.Tx, which holds the pool's only connection, so every
// query inside it MUST go through the transaction and never back through the
// pool: that would wait forever on the connection the transaction holds.
type fkExecer interface {
	ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error)
	QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error)
}

// foreignKeyActions reads `PRAGMA foreign_key_list(table)` and returns, keyed by
// the foreign key's id, the declared ON DELETE action, the child columns and
// the parent they point at.
func foreignKeyActions(ctx context.Context, ex fkExecer, table string) (map[int]fkAction, error) {
	rows, err := ex.QueryContext(ctx, `SELECT "id", "table", "from", "to", "on_delete" FROM pragma_foreign_key_list(?) ORDER BY "id", "seq"`, table)
	if err != nil {
		return nil, fmt.Errorf("foreign_key_list(%s): %w", table, err)
	}
	defer rows.Close()

	actions := make(map[int]fkAction)
	for rows.Next() {
		var id int
		var parent, from, onDelete string
		var to sql.NullString
		if err := rows.Scan(&id, &parent, &from, &to, &onDelete); err != nil {
			return nil, fmt.Errorf("scan foreign_key_list(%s): %w", table, err)
		}
		a := actions[id]
		a.onDelete = strings.ToUpper(onDelete)
		a.parent = parent
		a.columns = append(a.columns, from)
		if to.Valid {
			a.parentCols = append(a.parentCols, to.String)
		}
		actions[id] = a
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate foreign_key_list(%s): %w", table, err)
	}
	return actions, nil
}

// Repair actions, as DriftAction names them. The preview shown to an operator
// and the repair itself both go through DriftAction, so what is promised is
// what is done.
const (
	DriftActionDelete = "delete" // ON DELETE CASCADE: remove the row
	DriftActionClear  = "clear"  // ON DELETE SET NULL: clear the reference
	DriftActionKeep   = "keep"   // anything else: report and leave alone
)

// DriftAction maps a declared ON DELETE action onto what a repair does.
func DriftAction(onDelete string) string {
	switch strings.ToUpper(onDelete) {
	case "CASCADE":
		return DriftActionDelete
	case "SET NULL":
		return DriftActionClear
	default:
		return DriftActionKeep
	}
}

// orphanGuard returns an SQL condition that is true only while the child row
// STILL has no parent, mirroring what foreign_key_check reports: a row with any
// NULL key column is never a violation. A repair works from a list of rowids
// taken earlier, possibly a minute earlier on a large database, so each
// statement re-checks it. Without the guard a rowid freed by an earlier cascade
// and reused by a new insert, or a parent re-created in the meantime, would
// have the repair delete a healthy row.
func orphanGuard(ctx context.Context, ex fkExecer, child string, a fkAction) (string, error) {
	parentCols := a.parentCols
	if len(parentCols) == 0 {
		rows, err := ex.QueryContext(ctx, `SELECT "name" FROM pragma_table_info(?) WHERE "pk" > 0 ORDER BY "pk"`, a.parent)
		if err != nil {
			return "", fmt.Errorf("table_info(%s): %w", a.parent, err)
		}
		defer rows.Close()
		for rows.Next() {
			var name string
			if err := rows.Scan(&name); err != nil {
				return "", err
			}
			parentCols = append(parentCols, name)
		}
		if err := rows.Err(); err != nil {
			return "", err
		}
	}
	if len(parentCols) == 0 || len(parentCols) != len(a.columns) {
		return "", fmt.Errorf("cannot match the columns of %s to %s", child, a.parent)
	}
	notNull := make([]string, 0, len(a.columns))
	match := make([]string, 0, len(a.columns))
	for i, col := range a.columns {
		notNull = append(notNull, fmt.Sprintf("%s.%s IS NOT NULL", quoteIdent(child), quoteIdent(col)))
		match = append(match, fmt.Sprintf("p.%s = %s.%s", quoteIdent(parentCols[i]), quoteIdent(child), quoteIdent(col)))
	}
	return fmt.Sprintf("%s AND NOT EXISTS (SELECT 1 FROM %s AS p WHERE %s)",
		strings.Join(notNull, " AND "), quoteIdent(a.parent), strings.Join(match, " AND ")), nil
}

// RepairReport records what RepairForeignKeyViolations did.
type RepairReport struct {
	Deleted   int      // rows removed because the schema says ON DELETE CASCADE
	Nulled    int      // rows whose reference was cleared (ON DELETE SET NULL)
	Skipped   int      // rows left alone because no safe action is declared
	SkipNotes []string // one line per distinct reason a row was skipped
	Remaining int      // violations still present after the repair
}

// repairer resolves violations one row at a time and accumulates the report.
// It is the single implementation behind the CLI (RepairForeignKeyViolations)
// and the Settings cleanup (RepairForeignKeyDrift), so both resolve a row the
// same way.
type repairer struct {
	report      RepairReport
	skipSeen    map[string]bool
	actionCache map[string]map[int]fkAction
	guardCache  map[string]string
	noGuard     map[string]bool
}

func newRepairer() *repairer {
	return &repairer{
		skipSeen:    make(map[string]bool),
		actionCache: make(map[string]map[int]fkAction),
		guardCache:  make(map[string]string),
		noGuard:     make(map[string]bool),
	}
}

func (r *repairer) skip(note string) {
	r.report.Skipped++
	if !r.skipSeen[note] {
		r.skipSeen[note] = true
		r.report.SkipNotes = append(r.report.SkipNotes, note)
	}
}

func (r *repairer) apply(ctx context.Context, ex fkExecer, v ForeignKeyViolation) error {
	if !v.RowID.Valid {
		r.skip(fmt.Sprintf("%s: WITHOUT ROWID table, cannot address the row", v.Table))
		return nil
	}
	actions, ok := r.actionCache[v.Table]
	if !ok {
		var err error
		actions, err = foreignKeyActions(ctx, ex, v.Table)
		if err != nil {
			return err
		}
		r.actionCache[v.Table] = actions
	}
	action, ok := actions[v.FKID]
	if !ok {
		r.skip(fmt.Sprintf("%s: no foreign key with id %d", v.Table, v.FKID))
		return nil
	}

	kind := DriftAction(action.onDelete)
	if kind == DriftActionKeep {
		// NO ACTION / RESTRICT / SET DEFAULT: the schema does not say what
		// should happen, so guessing risks throwing away real data.
		r.skip(fmt.Sprintf("%s -> %s: ON DELETE %s, no safe automatic repair", v.Table, v.Parent, action.onDelete))
		return nil
	}

	key := fmt.Sprintf("%s#%d", v.Table, v.FKID)
	if r.noGuard[key] {
		r.skip(fmt.Sprintf("%s -> %s: cannot verify the row is still an orphan", v.Table, v.Parent))
		return nil
	}
	guard, ok := r.guardCache[key]
	if !ok {
		g, err := orphanGuard(ctx, ex, v.Table, action)
		if err != nil {
			r.noGuard[key] = true
			r.skip(fmt.Sprintf("%s -> %s: cannot verify the row is still an orphan", v.Table, v.Parent))
			return nil
		}
		guard = g
		r.guardCache[key] = g
	}

	switch kind {
	case DriftActionDelete:
		res, err := ex.ExecContext(ctx,
			fmt.Sprintf(`DELETE FROM %s WHERE rowid = ? AND %s`, quoteIdent(v.Table), guard), v.RowID.Int64)
		if err != nil {
			if isConstraintError(err) {
				r.skip(constraintSkipNote(v))
				return nil
			}
			return fmt.Errorf("delete orphan %s rowid %d: %w", v.Table, v.RowID.Int64, err)
		}
		if n, _ := res.RowsAffected(); n > 0 {
			r.report.Deleted += int(n)
		}
	case DriftActionClear:
		sets := make([]string, 0, len(action.columns))
		for _, col := range action.columns {
			sets = append(sets, quoteIdent(col)+" = NULL")
		}
		res, err := ex.ExecContext(ctx,
			fmt.Sprintf(`UPDATE %s SET %s WHERE rowid = ? AND %s`, quoteIdent(v.Table), strings.Join(sets, ", "), guard),
			v.RowID.Int64)
		if err != nil {
			if isConstraintError(err) {
				r.skip(constraintSkipNote(v))
				return nil
			}
			return fmt.Errorf("clear dangling reference on %s rowid %d: %w", v.Table, v.RowID.Int64, err)
		}
		if n, _ := res.RowsAffected(); n > 0 {
			r.report.Nulled += int(n)
		}
	}
	return nil
}

// isConstraintError reports whether SQLite refused a statement on a constraint
// (a RESTRICT child still pointing at the row, a NOT NULL column that SET NULL
// would empty). SQLite undoes just that statement and leaves an enclosing
// transaction usable, so one such row is a skip, not a reason to abandon the
// batch: otherwise every rerun would stop at the same row.
//
// The check is on the driver's error code, the SQLITE_CONSTRAINT primary code
// (extended codes such as NOT NULL, CHECK, FOREIGN KEY and a trigger's RAISE
// all carry it in their low byte), never on message text.
func isConstraintError(err error) bool {
	var se *sqlite.Error
	return errors.As(err, &se) && se.Code()&0xff == sqliteConstraint
}

// sqliteConstraint is SQLITE_CONSTRAINT, the primary result code.
const sqliteConstraint = 19

func constraintSkipNote(v ForeignKeyViolation) string {
	return fmt.Sprintf("%s -> %s: a constraint refused the repair of at least one row, left as it is", v.Table, v.Parent)
}

// RepairForeignKeyViolations replays the ON DELETE action that never fired.
//
// It is deliberately NOT a blanket delete: each violating row is resolved the
// way the schema says it should have been when its parent went away — CASCADE
// removes the row, SET NULL clears the dangling reference (so download history
// and the like survive), and anything else is left alone and reported. Foreign
// key enforcement stays ON so a cascade removes the rows underneath it too,
// exactly as the original delete would have.
//
// The caller is responsible for confirming with the operator first; this
// function does not prompt and does not take a backup. It is for the offline
// CLI, which owns the database. The running app uses RepairForeignKeyDrift,
// which does the same per-row work in bounded batches.
func RepairForeignKeyViolations(ctx context.Context, database *sql.DB, violations []ForeignKeyViolation) (RepairReport, error) {
	r := newRepairer()
	for _, v := range violations {
		if err := r.apply(ctx, database, v); err != nil {
			return r.report, err
		}
	}

	// A cascade can clear violations further down the tree, so recount rather
	// than assuming Deleted+Nulled covered everything.
	remaining, err := ForeignKeyViolations(ctx, database)
	if err != nil {
		return r.report, err
	}
	r.report.Remaining = len(remaining)
	return r.report, nil
}

// quoteIdent wraps a SQLite identifier in double quotes. Identifiers here come
// from SQLite's own pragma output, never from user input, but the tables are
// interpolated into SQL so they are quoted anyway.
func quoteIdent(name string) string {
	return `"` + strings.ReplaceAll(name, `"`, `""`) + `"`
}

// ForeignKeyDrift is one relationship (child table to parent table) that has
// rows pointing at a parent which no longer exists. It carries counts only: no
// row ids and no row contents, so it is safe to hand to the Settings UI.
type ForeignKeyDrift struct {
	ChildTable  string
	ParentTable string
	// OnDelete is what the schema declares should have happened to the child
	// row when its parent was deleted: CASCADE, SET NULL, NO ACTION, ... It is
	// what a cleanup would replay. Empty when the schema has no matching
	// foreign key entry.
	OnDelete   string
	Violations int
}

// driftQuery groups `pragma_foreign_key_check` by child table AND parent
// table, joined to `pragma_foreign_key_list` for the declared ON DELETE
// action. seq = 0 keeps a composite key from being counted once per column.
const driftQuery = `
SELECT c."table", c."parent", COALESCE(l."on_delete", ''), COUNT(*)
FROM pragma_foreign_key_check AS c
LEFT JOIN pragma_foreign_key_list(c."table") AS l ON l."id" = c."fkid" AND l."seq" = 0
GROUP BY c."table", c."parent", c."fkid"
ORDER BY c."table", c."parent"`

// driftQueryer is the one method the scan needs, satisfied by *sql.DB.
type driftQueryer interface {
	QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error)
}

// ForeignKeyDriftReport runs the drift scan on the given pool. Read only: it
// never writes.
//
// The scan is a full pass over every table with a foreign key, so callers must
// run it on demand only, with a context that can cancel it. No transaction is
// opened. NOTE: on the application's pool this holds the pool's ONLY
// connection (SetMaxOpenConns(1)) for the whole scan, stalling every other
// query including the auth middleware's settings reads. Production callers use
// ForeignKeyDriftReportAt instead; this form exists for tests and for the
// no-path fallback (in-memory databases cannot be reopened from a path).
//
// hook, when given, runs while the scan's statement is open. Tests use it to
// hold a scan in flight.
func ForeignKeyDriftReport(ctx context.Context, database *sql.DB, hook ...func()) ([]ForeignKeyDrift, error) {
	return runDriftScan(ctx, database, hook)
}

// ForeignKeyDriftReportAt runs the same scan on its OWN short lived read only
// connection to the database file at path, so it never takes the shared pool's
// single connection and the rest of the app (health checks, auth, the UI)
// keeps answering for the duration of the scan.
//
// The connection is opened per scan and closed before returning. It is
// mode=ro, so it cannot write, and it deliberately sets no foreign_keys
// pragma: foreign_key_check works regardless of enforcement and nothing here
// toggles it. busy_timeout matches the other read-only opener (restore.go).
//
// WAL: a long read on a separate connection pins a WAL snapshot, so the WAL
// cannot be checkpointed past it until the scan ends. That is bounded by the
// caller's context deadline (the API uses 60 seconds), and the context is
// honoured on this connection too, so a cancel or timeout interrupts the scan.
func ForeignKeyDriftReportAt(ctx context.Context, path string, hook ...func()) ([]ForeignKeyDrift, error) {
	ro, err := sql.Open("sqlite", SQLiteFileURI(path, "mode=ro&_pragma=busy_timeout(5000)"))
	if err != nil {
		return nil, fmt.Errorf("open %q read-only: %w", path, err)
	}
	defer func() { _ = ro.Close() }()
	ro.SetMaxOpenConns(1)
	return runDriftScan(ctx, ro, hook)
}

func runDriftScan(ctx context.Context, q driftQueryer, hook []func()) ([]ForeignKeyDrift, error) {
	rows, err := q.QueryContext(ctx, driftQuery)
	if err != nil {
		return nil, fmt.Errorf("foreign_key_check: %w", err)
	}
	defer rows.Close()

	for _, h := range hook {
		if h != nil {
			h()
		}
	}

	out := []ForeignKeyDrift{}
	for rows.Next() {
		var d ForeignKeyDrift
		if err := rows.Scan(&d.ChildTable, &d.ParentTable, &d.OnDelete, &d.Violations); err != nil {
			return nil, fmt.Errorf("scan foreign_key_check row: %w", err)
		}
		d.OnDelete = strings.ToUpper(d.OnDelete)
		out = append(out, d)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate foreign_key_check: %w", err)
	}
	return out, nil
}

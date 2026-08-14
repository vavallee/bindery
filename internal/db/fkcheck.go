package db

import (
	"context"
	"database/sql"
	"fmt"
	"log/slog"
	"sort"
	"strings"
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
	onDelete string
	columns  []string // the child-side column(s) of this foreign key
}

// foreignKeyActions reads `PRAGMA foreign_key_list(table)` and returns, keyed by
// the foreign key's id, the declared ON DELETE action and the child columns.
func foreignKeyActions(ctx context.Context, database *sql.DB, table string) (map[int]fkAction, error) {
	rows, err := database.QueryContext(ctx, `SELECT "id", "from", "on_delete" FROM pragma_foreign_key_list(?)`, table)
	if err != nil {
		return nil, fmt.Errorf("foreign_key_list(%s): %w", table, err)
	}
	defer rows.Close()

	actions := make(map[int]fkAction)
	for rows.Next() {
		var id int
		var from, onDelete string
		if err := rows.Scan(&id, &from, &onDelete); err != nil {
			return nil, fmt.Errorf("scan foreign_key_list(%s): %w", table, err)
		}
		a := actions[id]
		a.onDelete = strings.ToUpper(onDelete)
		a.columns = append(a.columns, from)
		actions[id] = a
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate foreign_key_list(%s): %w", table, err)
	}
	return actions, nil
}

// RepairReport records what RepairForeignKeyViolations did.
type RepairReport struct {
	Deleted   int      // rows removed because the schema says ON DELETE CASCADE
	Nulled    int      // rows whose reference was cleared (ON DELETE SET NULL)
	Skipped   int      // rows left alone because no safe action is declared
	SkipNotes []string // one line per distinct reason a row was skipped
	Remaining int      // violations still present after the repair
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
// function does not prompt and does not take a backup.
func RepairForeignKeyViolations(ctx context.Context, database *sql.DB, violations []ForeignKeyViolation) (RepairReport, error) {
	var report RepairReport
	skipSeen := make(map[string]bool)
	skip := func(note string) {
		report.Skipped++
		if !skipSeen[note] {
			skipSeen[note] = true
			report.SkipNotes = append(report.SkipNotes, note)
		}
	}

	actionCache := make(map[string]map[int]fkAction)
	for _, v := range violations {
		if !v.RowID.Valid {
			skip(fmt.Sprintf("%s: WITHOUT ROWID table, cannot address the row", v.Table))
			continue
		}
		actions, ok := actionCache[v.Table]
		if !ok {
			var err error
			actions, err = foreignKeyActions(ctx, database, v.Table)
			if err != nil {
				return report, err
			}
			actionCache[v.Table] = actions
		}
		action, ok := actions[v.FKID]
		if !ok {
			skip(fmt.Sprintf("%s: no foreign key with id %d", v.Table, v.FKID))
			continue
		}

		switch action.onDelete {
		case "CASCADE":
			res, err := database.ExecContext(ctx,
				fmt.Sprintf(`DELETE FROM %s WHERE rowid = ?`, quoteIdent(v.Table)), v.RowID.Int64)
			if err != nil {
				return report, fmt.Errorf("delete orphan %s rowid %d: %w", v.Table, v.RowID.Int64, err)
			}
			if n, _ := res.RowsAffected(); n > 0 {
				report.Deleted += int(n)
			}
		case "SET NULL":
			sets := make([]string, 0, len(action.columns))
			for _, col := range action.columns {
				sets = append(sets, quoteIdent(col)+" = NULL")
			}
			res, err := database.ExecContext(ctx,
				fmt.Sprintf(`UPDATE %s SET %s WHERE rowid = ?`, quoteIdent(v.Table), strings.Join(sets, ", ")),
				v.RowID.Int64)
			if err != nil {
				return report, fmt.Errorf("clear dangling reference on %s rowid %d: %w", v.Table, v.RowID.Int64, err)
			}
			if n, _ := res.RowsAffected(); n > 0 {
				report.Nulled += int(n)
			}
		default:
			// NO ACTION / RESTRICT / SET DEFAULT: the schema does not say what
			// should happen, so guessing risks throwing away real data.
			skip(fmt.Sprintf("%s -> %s: ON DELETE %s, no safe automatic repair", v.Table, v.Parent, action.onDelete))
		}
	}

	// A cascade can clear violations further down the tree, so recount rather
	// than assuming Deleted+Nulled covered everything.
	remaining, err := ForeignKeyViolations(ctx, database)
	if err != nil {
		return report, err
	}
	report.Remaining = len(remaining)
	return report, nil
}

// quoteIdent wraps a SQLite identifier in double quotes. Identifiers here come
// from SQLite's own pragma output, never from user input, but the tables are
// interpolated into SQL so they are quoted anyway.
func quoteIdent(name string) string {
	return `"` + strings.ReplaceAll(name, `"`, `""`) + `"`
}

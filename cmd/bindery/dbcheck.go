package main

import (
	"context"
	"fmt"
	"os"

	"github.com/vavallee/bindery/internal/db"
)

// Offline database maintenance, for the case where Bindery will not start.
//
// `bindery db-check` and `bindery db-repair` open the database WITHOUT running
// migrations and report (or fix) rows whose foreign keys point at a parent that
// no longer exists. An instance stuck on a migration cannot be helped from the
// web UI, so this has to be reachable from the command line — on Docker:
//
//	docker run --rm -v bindery-config:/config ghcr.io/vavallee/bindery:latest db-check
//
// Operators who cannot change the command (some NAS UIs) can set
// BINDERY_DB_FK_CHECK=report — or =repair — instead; see runDBFKCheckFromEnv.
const (
	dbCheckMaxListedRows = 100

	fkCheckEnv = "BINDERY_DB_FK_CHECK"
)

// dbMaintenanceRequest is a parsed db-check / db-repair invocation.
type dbMaintenanceRequest struct {
	repair    bool
	path      string
	confirmed bool
}

// parseDBMaintenanceArgs interprets a full argv. It returns nil when the
// arguments name neither subcommand, so main can carry on with a normal boot.
// Any argument that isn't a flag overrides the database path.
func parseDBMaintenanceArgs(args []string, defaultPath string) *dbMaintenanceRequest {
	if len(args) < 2 {
		return nil
	}
	req := dbMaintenanceRequest{path: defaultPath}
	switch args[1] {
	case "db-check":
	case "db-repair":
		req.repair = true
	default:
		return nil
	}
	for _, arg := range args[2:] {
		switch arg {
		case "--yes", "-y":
			req.confirmed = true
		default:
			req.path = arg
		}
	}
	return &req
}

// runDBMaintenance dispatches the db-check / db-repair subcommands, exiting the
// process when it handles one, and returning normally when it does not.
func runDBMaintenance(defaultPath string) {
	req := parseDBMaintenanceArgs(os.Args, defaultPath)
	if req == nil {
		return
	}
	if req.repair && !req.confirmed {
		fmt.Fprintf(os.Stderr,
			"db-repair modifies your database. Back up %s first, then re-run with --yes:\n\n    bindery db-repair --yes\n\nRun `bindery db-check` to see what it would change.\n", req.path)
		os.Exit(2)
	}
	os.Exit(runFKTool(req.path, req.repair))
}

// fkCheckMode maps a BINDERY_DB_FK_CHECK value onto what to do: "" to boot
// normally, "report", "repair", or "invalid".
func fkCheckMode(value string) string {
	switch value {
	case "":
		return ""
	case "report", "check", "1", "true":
		return "report"
	case "repair":
		return "repair"
	default:
		return "invalid"
	}
}

// runDBFKCheckFromEnv is the same tool reachable through an environment
// variable, for deployments where the command line is not editable. It runs
// before the database is opened for real and exits the process whenever the
// variable is set.
func runDBFKCheckFromEnv(path string) {
	switch fkCheckMode(os.Getenv(fkCheckEnv)) {
	case "":
		return
	case "report":
		os.Exit(runFKTool(path, false))
	case "repair":
		fmt.Printf("%s=repair is set — repairing, then exiting. Unset it before starting Bindery normally.\n", fkCheckEnv)
		os.Exit(runFKTool(path, true))
	default:
		fmt.Fprintf(os.Stderr, "%s must be one of: report, repair\n", fkCheckEnv)
		os.Exit(2)
	}
}

// runFKTool prints the foreign-key integrity report and, when repair is set,
// replays the ON DELETE action that never fired. Returns a process exit code.
func runFKTool(path string, repair bool) int {
	ctx := context.Background()

	database, err := db.OpenForMaintenance(ctx, path)
	if err != nil {
		fmt.Fprintln(os.Stderr, "open database:", err)
		return 1
	}
	defer database.Close()

	fmt.Printf("bindery db-%s — foreign key integrity\ndatabase: %s\n\n", map[bool]string{false: "check", true: "repair"}[repair], path)

	violations, err := db.ForeignKeyViolations(ctx, database)
	if err != nil {
		fmt.Fprintln(os.Stderr, "foreign_key_check:", err)
		return 1
	}
	if len(violations) == 0 {
		fmt.Println("No foreign-key violations found. Nothing to repair.")
		return 0
	}

	fmt.Printf("%d foreign-key violation(s) found.\n\nby relationship:\n", len(violations))
	for _, line := range db.SummariseViolations(violations) {
		fmt.Printf("  %s\n", line)
	}

	fmt.Printf("\naffected rows (table, rowid, missing parent):\n")
	for i, v := range violations {
		if i == dbCheckMaxListedRows {
			fmt.Printf("  ... and %d more\n", len(violations)-dbCheckMaxListedRows)
			break
		}
		rowID := "-"
		if v.RowID.Valid {
			rowID = fmt.Sprintf("%d", v.RowID.Int64)
		}
		fmt.Printf("  %-32s %-12s %s\n", v.Table, rowID, v.Parent)
	}

	if !repair {
		fmt.Printf(`
These are orphan rows left behind while foreign key enforcement was off (#1727).
Your data is otherwise intact, and Bindery starts normally with them present.

To clean them up: stop Bindery, back up %s, then run

    bindery db-repair --yes

Each row is resolved the way the schema says it should have been: ON DELETE
CASCADE rows are removed, ON DELETE SET NULL references are cleared.
`, path)
		return 0
	}

	fmt.Printf("\nrepairing %d violation(s)...\n", len(violations))
	report, err := db.RepairForeignKeyViolations(ctx, database, violations)
	if err != nil {
		fmt.Fprintln(os.Stderr, "repair failed:", err)
		fmt.Fprintln(os.Stderr, "the database may be partially repaired — restore your backup and report this at https://github.com/vavallee/bindery/issues")
		return 1
	}

	fmt.Printf("\ndeleted %d orphan row(s) (ON DELETE CASCADE)\ncleared %d dangling reference(s) (ON DELETE SET NULL)\nskipped %d row(s)\n",
		report.Deleted, report.Nulled, report.Skipped)
	for _, note := range report.SkipNotes {
		fmt.Printf("  skipped: %s\n", note)
	}
	fmt.Printf("remaining violations: %d\n", report.Remaining)
	if report.Remaining > 0 {
		fmt.Println("\nSome violations could not be repaired automatically — the schema declares no safe")
		fmt.Println("ON DELETE action for them. Report the lines above at https://github.com/vavallee/bindery/issues.")
	}
	return 0
}

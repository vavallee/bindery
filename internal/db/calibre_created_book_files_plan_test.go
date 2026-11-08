package db

import (
	"context"
	"strings"
	"testing"
)

// TestListCreatedBookFiles_PlanHasNoCorrelatedScan pins the shape of the query
// the Calibre import runs at the start of every import (#3630). The snapshots
// table grows with every run and is never pruned; a plan that scans it once
// per provenance row (a correlated subquery) took minutes on a large library.
// The snapshots are read once, through the partial index migration 128 adds
// for exactly this query's predicate, so the pass covers only the created
// book_file rows rather than every snapshot; provenance and book_files must be
// index lookups. Which index those two lookups pick is left to the planner.
const createdBookFileIndex = "idx_calibre_snapshots_created_book_file"

func TestListCreatedBookFiles_PlanHasNoCorrelatedScan(t *testing.T) {
	database, err := OpenMemory()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { database.Close() })

	const prefix = "calibre:book-file:"
	rows, err := database.QueryContext(context.Background(), "EXPLAIN QUERY PLAN "+listCreatedBookFilesSQL, prefix, prefix, prefix)
	if err != nil {
		t.Fatalf("explain: %v", err)
	}
	defer rows.Close()
	var details []string
	for rows.Next() {
		var id, parent, notused int
		var detail string
		if err := rows.Scan(&id, &parent, &notused, &detail); err != nil {
			t.Fatal(err)
		}
		details = append(details, detail)
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	plan := strings.Join(details, "\n")
	if strings.Contains(plan, "CORRELATED") {
		t.Errorf("plan has a correlated subquery:\n%s", plan)
	}
	for _, d := range details {
		if !strings.HasPrefix(d, "SCAN ") {
			continue
		}
		// "s" is the distinct keys drawn from the snapshots; the snapshots
		// themselves may only be read through the partial index.
		if d != "SCAN s" && !strings.HasPrefix(d, "SCAN calibre_entity_snapshots USING COVERING INDEX "+createdBookFileIndex) {
			t.Errorf("plan scans something other than the created book_file snapshots once: %q\n%s", d, plan)
		}
	}
	if !strings.Contains(plan, "calibre_entity_snapshots USING COVERING INDEX "+createdBookFileIndex) {
		t.Errorf("plan does not read the snapshots through %s:\n%s", createdBookFileIndex, plan)
	}
	for _, table := range []string{"p USING", "bf USING"} {
		if !strings.Contains(plan, "SEARCH "+table) {
			t.Errorf("plan does not look %s up by index:\n%s", strings.Fields(table)[0], plan)
		}
	}
	t.Logf("plan:\n%s", plan)
}

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
// The only full scan allowed is the single pass over the snapshots; provenance
// and book_files must be index lookups.
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
		// The snapshots table itself, or "s", the distinct keys drawn from it.
		if d != "SCAN calibre_entity_snapshots" && d != "SCAN s" {
			t.Errorf("plan scans something other than the snapshots once: %q\n%s", d, plan)
		}
	}
	for _, table := range []string{"p USING", "bf USING"} {
		if !strings.Contains(plan, "SEARCH "+table) {
			t.Errorf("plan does not look %s up by index:\n%s", strings.Fields(table)[0], plan)
		}
	}
	t.Logf("plan:\n%s", plan)
}

package db

import (
	"context"
	"reflect"
	"testing"
)

// One child table with two parents must report two relationships, and a
// composite key must count each orphan row once, not once per key column.
func TestForeignKeyDriftReportSplitsParentsAndCompositeKeys(t *testing.T) {
	database, err := OpenMemory()
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	for _, s := range []string{
		`PRAGMA foreign_keys = OFF`,
		// downloads points at two parents; both are missing.
		`INSERT INTO downloads (id, guid, title, nzb_url, indexer_id, download_client_id) VALUES (1, 'g1', 'R', 'http://x', 55, 66)`,
		`CREATE TABLE drift_parent (a INTEGER, b INTEGER, PRIMARY KEY (a, b))`,
		`CREATE TABLE drift_child (id INTEGER PRIMARY KEY, a INTEGER, b INTEGER,
			FOREIGN KEY (a, b) REFERENCES drift_parent (a, b) ON DELETE CASCADE)`,
		`INSERT INTO drift_child (id, a, b) VALUES (1, 1, 1), (2, 2, 2)`,
		`PRAGMA foreign_keys = ON`,
	} {
		if _, err := database.Exec(s); err != nil {
			t.Fatalf("seed %q: %v", s, err)
		}
	}
	got, err := ForeignKeyDriftReport(context.Background(), database)
	if err != nil {
		t.Fatal(err)
	}
	counts := map[string]int{}
	for _, d := range got {
		counts[d.ChildTable+"->"+d.ParentTable] = d.Violations
	}
	want := map[string]int{"downloads->indexers": 1, "downloads->download_clients": 1, "drift_child->drift_parent": 2}
	if !reflect.DeepEqual(counts, want) {
		t.Errorf("counts = %v, want %v", counts, want)
	}
}

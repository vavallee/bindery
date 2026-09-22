package db

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/vavallee/bindery/internal/models"
)

// TestMigrate092ReversesQualityProfileItems covers #2733. Until this release
// the quality profile editor listed formats worst first and the order was read
// by nothing. Ranking now follows the stored order, top is best, so every
// existing list is reversed once: a profile nobody ever reordered then reads
// as the old built in ranking, best first. Seeded factory profiles carry '[]'
// and a single entry list has no order, so both are left alone.
func TestMigrate092ReversesQualityProfileItems(t *testing.T) {
	database, err := OpenMemory()
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()

	ctx := context.Background()
	repo := NewQualityProfileRepo(database)

	create := func(name string, items []models.QualityItem) int64 {
		t.Helper()
		p := &models.QualityProfile{Name: name, Items: items}
		if err := repo.Create(ctx, p); err != nil {
			t.Fatalf("create %s: %v", name, err)
		}
		return p.ID
	}
	a := create("A", []models.QualityItem{
		{Quality: "pdf", Allowed: false},
		{Quality: "mobi", Allowed: true},
		{Quality: "epub", Allowed: true},
		{Quality: "azw3", Allowed: true},
	})
	b := create("B", []models.QualityItem{{Quality: "m4b", Allowed: true}})
	c := create("C", nil)
	// D is written by raw SQL so the JSON text is exactly what a third party
	// client might have stored: objects with the keys in a different order.
	res, err := database.ExecContext(ctx,
		`INSERT INTO quality_profiles (name, upgrade_allowed, cutoff, items) VALUES ('D', 0, '', ?)`,
		`[{"allowed":true,"quality":"epub"},{"allowed":false,"quality":"mp3"}]`)
	if err != nil {
		t.Fatalf("seed D: %v", err)
	}
	d, err := res.LastInsertId()
	if err != nil {
		t.Fatal(err)
	}

	v092 := migrationVersionForTest(t, "092_quality_profile_items_best_first.sql")
	if _, err := database.ExecContext(ctx, `DELETE FROM schema_migrations WHERE version = ?`, v092); err != nil {
		t.Fatalf("clear migration 092 marker: %v", err)
	}
	if err := migrate(database); err != nil {
		t.Fatalf("rerun migration 092: %v", err)
	}

	order := func(id int64) []string {
		t.Helper()
		var raw string
		if err := database.QueryRowContext(ctx, `SELECT items FROM quality_profiles WHERE id = ?`, id).Scan(&raw); err != nil {
			t.Fatalf("read items %d: %v", id, err)
		}
		var items []models.QualityItem
		if err := json.Unmarshal([]byte(raw), &items); err != nil {
			t.Fatalf("items %d are no longer a list of objects: %v: %s", id, err, raw)
		}
		out := make([]string, len(items))
		for i, it := range items {
			out[i] = it.Quality
			if !it.Allowed && it.Quality != "pdf" && it.Quality != "mp3" {
				t.Errorf("profile %d lost the allowed flag on %q", id, it.Quality)
			}
		}
		return out
	}
	equal := func(got, want []string) bool {
		if len(got) != len(want) {
			return false
		}
		for i := range got {
			if got[i] != want[i] {
				return false
			}
		}
		return true
	}

	if got := order(a); !equal(got, []string{"azw3", "epub", "mobi", "pdf"}) {
		t.Errorf("A = %v, want the stored order reversed so the best format is first", got)
	}
	if got := order(d); !equal(got, []string{"mp3", "epub"}) {
		t.Errorf("D = %v, want [mp3 epub]", got)
	}
	if got := order(b); !equal(got, []string{"m4b"}) {
		t.Errorf("B = %v, want a single entry list untouched", got)
	}
	if got := order(c); len(got) != 0 {
		t.Errorf("C = %v, want an empty list untouched", got)
	}

	// One shot: a second migrate call with the marker still present must not
	// reverse the lists back. A Go backfill hook keyed by revision would be
	// re runnable and is exactly what this migration must not be.
	if err := migrate(database); err != nil {
		t.Fatalf("second migrate: %v", err)
	}
	if got := order(a); !equal(got, []string{"azw3", "epub", "mobi", "pdf"}) {
		t.Errorf("second migrate flipped A back to %v", got)
	}
}

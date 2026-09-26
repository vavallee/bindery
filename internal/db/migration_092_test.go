package db

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/vavallee/bindery/internal/models"
)

// TestMigrate092ReversesQualityProfileItems covers #2733. It asserts the
// reversal, the rows the guards must skip, and that an ordinary restart does
// not reverse anything a second time. Until this release
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
	// The rows below are written by raw SQL, so the JSON text is exactly what
	// a corrupted or hand edited row might hold rather than what marshalling
	// []models.QualityItem can produce.
	seedRaw := func(name, items string) int64 {
		t.Helper()
		res, err := database.ExecContext(ctx,
			`INSERT INTO quality_profiles (name, upgrade_allowed, cutoff, items) VALUES (?, 0, '', ?)`,
			name, items)
		if err != nil {
			t.Fatalf("seed %s: %v", name, err)
		}
		id, err := res.LastInsertId()
		if err != nil {
			t.Fatal(err)
		}
		return id
	}
	// D: objects with the keys in a different order, as a third party client
	// might have stored them.
	d := seedRaw("D", `[{"allowed":true,"quality":"epub"},{"allowed":false,"quality":"mp3"}]`)

	// E, F and G hold elements that are not objects. json(value) raises
	// "malformed JSON" on a bare SQL scalar, and applyMigration wraps each
	// migration in a transaction whose error aborts startup for the whole
	// instance, so a single corrupted row must not be touched rather than
	// bringing the process down. None of these is reachable through the API
	// (both writers marshal []models.QualityItem) but a hand edited or
	// corrupted row is.
	raw := map[string]string{
		"E": `["epub","pdf"]`,
		"F": `[{"quality":"epub","allowed":true},"pdf"]`,
		"G": `[1,2]`,
	}
	skipIDs := map[string]int64{}
	for name, items := range raw {
		skipIDs[name] = seedRaw(name, items)
	}

	v092 := migrationVersionForTest(t, "092_quality_profile_items_best_first.sql")
	if _, err := database.ExecContext(ctx, `DELETE FROM schema_migrations WHERE version = ?`, v092); err != nil {
		t.Fatalf("clear migration 092 marker: %v", err)
	}
	if err := migrate(database); err != nil {
		t.Fatalf("rerun migration 092: %v", err)
	}

	rawItems := func(id int64) string {
		t.Helper()
		var got string
		if err := database.QueryRowContext(ctx, `SELECT items FROM quality_profiles WHERE id = ?`, id).Scan(&got); err != nil {
			t.Fatalf("read raw items %d: %v", id, err)
		}
		return got
	}
	for name, id := range skipIDs {
		if got := rawItems(id); got != raw[name] {
			t.Errorf("%s holds an element that is not an object and must be left byte identical, got %s want %s", name, got, raw[name])
		}
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

	// A second migrate call, the way every restart makes one, must leave the
	// lists alone. What makes that true is schema_migrations: the runner skips
	// version 92 because its row is present. The reversal itself is NOT
	// idempotent, and replaying the UPDATE would flip every list back, which
	// is exactly why this is a numbered migration rather than a Go backfill
	// hook that can be re run by deleting its marker. So this pins the restart
	// path and the absence of a second writer, not a property of the SQL.
	if err := migrate(database); err != nil {
		t.Fatalf("second migrate: %v", err)
	}
	for name, id := range skipIDs {
		if got := rawItems(id); got != raw[name] {
			t.Errorf("second migrate changed %s to %s", name, got)
		}
	}
	if got := order(a); !equal(got, []string{"azw3", "epub", "mobi", "pdf"}) {
		t.Errorf("second migrate flipped A back to %v", got)
	}
}

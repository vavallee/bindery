package db

import (
	"context"
	"testing"

	"github.com/vavallee/bindery/internal/models"
)

func TestAuthorSortKey(t *testing.T) {
	cases := []struct{ in, want string }{
		{"", ""},
		{"  ", ""},
		{"Adams, Douglas", "adams, douglas"},
		{"adelson, Anita", "adelson, anita"},       // case folds
		{"de Balzac, Honoré", "de balzac, honore"}, // accent in tail folds
		{"Zola, Émile", "zola, emile"},
		{"Östergaard, Karl", "ostergaard, karl"}, // leading diacritic → base letter
		{"Ángel, José", "angel, jose"},
		{"Çelik, Ayşe", "celik, ayse"},
		{"Nowak, Łukasz", "nowak, lukasz"}, // ł is non-decomposable
		{"Ørsted, Hans", "orsted, hans"},   // ø is non-decomposable
		{"Æsop", "aesop"},                  // ligature expands
		{"Straße, Anna", "strasse, anna"},  // ß → ss
	}
	for _, c := range cases {
		if got := authorSortKey(c.in); got != c.want {
			t.Errorf("authorSortKey(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

// Leading-diacritic keys must order by their folded base letter, not after "z".
func TestAuthorSortKey_Ordering(t *testing.T) {
	// Östergaard folds to 'o', so it must sort between "nowak" and "zola".
	nowak := authorSortKey("Nowak, Łukasz")
	o := authorSortKey("Östergaard, Karl")
	zola := authorSortKey("Zola, Émile")
	if o <= nowak {
		t.Errorf("Östergaard key %q should sort after Nowak key %q", o, nowak)
	}
	if o >= zola {
		t.Errorf("Östergaard key %q should sort before Zola key %q", o, zola)
	}
}

// TestBackfillAuthorSortKeys covers the migration-058 startup backfill: a row
// whose sort_key is empty (as legacy rows are immediately after the ALTER TABLE,
// since SQLite can't accent-fold) must be populated from sort_name, and a second
// run must be a no-op.
func TestBackfillAuthorSortKeys(t *testing.T) {
	database, err := OpenMemory()
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	repo := NewAuthorRepo(database)
	ctx := context.Background()

	a := &models.Author{ForeignID: "OL-BF", Name: "Karl Östergaard", SortName: "Östergaard, Karl"}
	if err := repo.Create(ctx, a); err != nil {
		t.Fatalf("create: %v", err)
	}
	// Simulate a pre-058 row: blank the key the Create path just computed.
	if _, err := database.Exec("UPDATE authors SET sort_key = '' WHERE id = ?", a.ID); err != nil {
		t.Fatalf("blank sort_key: %v", err)
	}

	if err := backfillAuthorSortKeys(database); err != nil {
		t.Fatalf("backfill: %v", err)
	}
	var got string
	if err := database.QueryRow("SELECT sort_key FROM authors WHERE id = ?", a.ID).Scan(&got); err != nil {
		t.Fatalf("read sort_key: %v", err)
	}
	if want := "ostergaard, karl"; got != want {
		t.Errorf("sort_key after backfill = %q, want %q", got, want)
	}

	// Idempotent: a second pass finds nothing to update and errors on nothing.
	if err := backfillAuthorSortKeys(database); err != nil {
		t.Fatalf("backfill (second pass): %v", err)
	}
}

// TestBackfillAuthorSortKeys_SurfacesReadError ensures a failure reading the
// authors table propagates rather than being swallowed — a silent backfill
// failure at startup would leave sort_key empty and the list mis-ordered.
func TestBackfillAuthorSortKeys_SurfacesReadError(t *testing.T) {
	database, err := OpenMemory()
	if err != nil {
		t.Fatal(err)
	}
	database.Close() // query against a closed handle errors on the read phase
	if err := backfillAuthorSortKeys(database); err == nil {
		t.Fatal("expected an error backfilling against a closed database, got nil")
	}
}

// TestBackfillAuthorSortKeys_SurfacesWriteError ensures a write failure during
// the update phase propagates (and rolls back) rather than being ignored.
func TestBackfillAuthorSortKeys_SurfacesWriteError(t *testing.T) {
	database, err := OpenMemory()
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	repo := NewAuthorRepo(database)
	ctx := context.Background()

	a := &models.Author{ForeignID: "OL-WE", Name: "Anna Straße", SortName: "Straße, Anna"}
	if err := repo.Create(ctx, a); err != nil {
		t.Fatalf("create: %v", err)
	}
	if _, err := database.Exec("UPDATE authors SET sort_key = '' WHERE id = ?", a.ID); err != nil {
		t.Fatalf("blank sort_key: %v", err)
	}
	// Make the connection reject writes: the read phase still succeeds, so the
	// failure lands in the UPDATE, exercising the rollback/return path.
	if _, err := database.Exec("PRAGMA query_only = ON"); err != nil {
		t.Fatalf("set query_only: %v", err)
	}
	if err := backfillAuthorSortKeys(database); err == nil {
		t.Fatal("expected a write error backfilling under query_only, got nil")
	}
}

package db

import (
	"context"
	"path/filepath"
	"strconv"
	"testing"

	"github.com/vavallee/bindery/internal/isbnutil"
	"github.com/vavallee/bindery/internal/models"
)

// TestBackfillASINCase_UpperCasesRowsWrittenBeforeNormalization is the
// stored-data half of the ASIN change.
//
// Normalizing new writes is not enough on its own: an ASIN is compared as an
// exact string (importer.Lookup's first tier matches a filename's ASIN, which
// a regexp guarantees is upper case, against books.asin with ==), so every row
// the Audiobookshelf import wrote in lower case before this change matches
// nothing and would keep matching nothing until that book happened to be
// re-imported. The backfill brings those rows up to the same rule the ingest
// paths now apply.
//
// The row is written directly with SQL because the repositories are exactly
// what would prevent a lower-case ASIN from existing; the point is a database
// that predates the fix.
func TestBackfillASINCase_UpperCasesRowsWrittenBeforeNormalization(t *testing.T) {
	path := filepath.Join(t.TempDir(), "bindery.db")
	ctx := context.Background()

	database := openFileDB(t, path)
	if got, ok := settingValue(t, database, backfillRevKeyASINCase); !ok || got != strconv.Itoa(isbnutil.NormalizeASINRev) {
		t.Fatalf("marker after first open = %q (present %v), want %d", got, ok, isbnutil.NormalizeASINRev)
	}

	authors := NewAuthorRepo(database)
	books := NewBookRepo(database)
	editions := NewEditionRepo(database)

	author := &models.Author{ForeignID: "OL-ASIN-A", Name: "Andy Weir", SortName: "Weir, Andy"}
	if err := authors.Create(ctx, author); err != nil {
		t.Fatal(err)
	}
	book := &models.Book{ForeignID: "OL-ASIN-B", AuthorID: author.ID, Title: "Artemis", Status: models.BookStatusWanted}
	if err := books.Create(ctx, book); err != nil {
		t.Fatal(err)
	}
	upper := "B0B2RJRF1K"
	edition := &models.Edition{ForeignID: "OL-ASIN-E", BookID: book.ID, Title: "Artemis", ASIN: &upper}
	if err := editions.Upsert(ctx, edition); err != nil {
		t.Fatal(err)
	}

	// Rewind both rows to what an Audiobookshelf import used to write.
	if _, err := database.Exec("UPDATE books SET asin = ' b0b2rjrf1k ' WHERE id = ?", book.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := database.Exec("UPDATE editions SET asin = 'b0b2rjrf1k' WHERE id = ?", edition.ID); err != nil {
		t.Fatal(err)
	}
	database.Close()

	// The marker is current, so nothing is scanned and the lower-case values
	// survive — the gate behaves like the other backfills.
	database = openFileDB(t, path)
	var stored string
	if err := database.QueryRow("SELECT asin FROM books WHERE id = ?", book.ID).Scan(&stored); err != nil {
		t.Fatal(err)
	}
	if stored != " b0b2rjrf1k " {
		t.Errorf("books.asin = %q after a second open, want it untouched: the backfill re-ran despite a current revision marker", stored)
	}

	// Dropping the marker stands in for bumping isbnutil.NormalizeASINRev.
	if _, err := database.Exec("DELETE FROM settings WHERE key = ?", backfillRevKeyASINCase); err != nil {
		t.Fatal(err)
	}
	database.Close()

	database = openFileDB(t, path)
	defer database.Close()
	if err := database.QueryRow("SELECT asin FROM books WHERE id = ?", book.ID).Scan(&stored); err != nil {
		t.Fatal(err)
	}
	if stored != "B0B2RJRF1K" {
		t.Errorf("books.asin = %q after the repair run, want %q", stored, "B0B2RJRF1K")
	}
	if err := database.QueryRow("SELECT asin FROM editions WHERE id = ?", edition.ID).Scan(&stored); err != nil {
		t.Fatal(err)
	}
	if stored != "B0B2RJRF1K" {
		t.Errorf("editions.asin = %q after the repair run, want %q", stored, "B0B2RJRF1K")
	}
	if got, ok := settingValue(t, database, backfillRevKeyASINCase); !ok || got != strconv.Itoa(isbnutil.NormalizeASINRev) {
		t.Errorf("marker after the repair run = %q (present %v), want %d", got, ok, isbnutil.NormalizeASINRev)
	}
}

// TestBackfillASINCase_LeavesCanonicalRowsAlone: the backfill only ever changes
// case and whitespace, so a row already stored the way the ingest paths write
// one must come back byte-identical.
func TestBackfillASINCase_LeavesCanonicalRowsAlone(t *testing.T) {
	path := filepath.Join(t.TempDir(), "bindery.db")
	ctx := context.Background()

	database := openFileDB(t, path)
	authors := NewAuthorRepo(database)
	books := NewBookRepo(database)
	author := &models.Author{ForeignID: "OL-ASIN-A2", Name: "Andy Weir", SortName: "Weir, Andy"}
	if err := authors.Create(ctx, author); err != nil {
		t.Fatal(err)
	}
	book := &models.Book{
		ForeignID: "OL-ASIN-B2", AuthorID: author.ID, Title: "Artemis",
		Status: models.BookStatusWanted, ASIN: "B0B2RJRF1K",
	}
	if err := books.Create(ctx, book); err != nil {
		t.Fatal(err)
	}
	if _, err := database.Exec("DELETE FROM settings WHERE key = ?", backfillRevKeyASINCase); err != nil {
		t.Fatal(err)
	}
	database.Close()

	database = openFileDB(t, path)
	defer database.Close()
	var stored string
	if err := database.QueryRow("SELECT asin FROM books WHERE id = ?", book.ID).Scan(&stored); err != nil {
		t.Fatal(err)
	}
	if stored != "B0B2RJRF1K" {
		t.Errorf("books.asin = %q, want the row left as it was", stored)
	}
}

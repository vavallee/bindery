package db

import (
	"context"
	"path/filepath"
	"strconv"
	"testing"

	"github.com/vavallee/bindery/internal/models"
)

// TestBackfillLanguageCodes_NormalizesRowsWrittenBeforeNormalization is the
// stored-data half of #3086.
//
// Normalizing new writes leaves every row Google Books, the Audiobookshelf
// import and the edit form already wrote on its old spelling, so an author
// would go on showing "en" beside "eng" until each book happened to be
// refreshed. The backfill brings those rows up to the rule the write paths now
// apply.
//
// The rows are rewound directly with SQL: the point is a database that
// predates the fix.
func TestBackfillLanguageCodes_NormalizesRowsWrittenBeforeNormalization(t *testing.T) {
	path := filepath.Join(t.TempDir(), "bindery.db")
	ctx := context.Background()

	database := openFileDB(t, path)
	if got, ok := settingValue(t, database, backfillRevKeyLanguageCode); !ok || got != strconv.Itoa(models.NormalizeLanguageCodeRev) {
		t.Fatalf("marker after first open = %q (present %v), want %d", got, ok, models.NormalizeLanguageCodeRev)
	}

	authors := NewAuthorRepo(database)
	books := NewBookRepo(database)
	editions := NewEditionRepo(database)

	author := &models.Author{ForeignID: "OL-LANG-A", Name: "Andy Weir", SortName: "Weir, Andy"}
	if err := authors.Create(ctx, author); err != nil {
		t.Fatal(err)
	}
	// One book per spelling the old write paths produced, plus one the
	// normalizer does not know and one with no language at all.
	stored := map[string]string{
		"en":      "eng",
		"English": "eng",
		"en-US":   "eng",
		"deu":     "ger",
		"Klingon": "klingon",
		"":        "",
	}
	ids := map[string]int64{}
	i := 0
	for old := range stored {
		i++
		book := &models.Book{
			ForeignID: "OL-LANG-B" + strconv.Itoa(i), AuthorID: author.ID,
			Title: "Artemis " + strconv.Itoa(i), Status: models.BookStatusWanted,
		}
		if err := books.Create(ctx, book); err != nil {
			t.Fatal(err)
		}
		if _, err := database.Exec("UPDATE books SET language = ? WHERE id = ?", old, book.ID); err != nil {
			t.Fatal(err)
		}
		ids[old] = book.ID
	}
	edition := &models.Edition{ForeignID: "OL-LANG-E", BookID: ids["en"], Title: "Artemis"}
	if err := editions.Upsert(ctx, edition); err != nil {
		t.Fatal(err)
	}
	if _, err := database.Exec("UPDATE editions SET language = 'fr' WHERE id = ?", edition.ID); err != nil {
		t.Fatal(err)
	}
	database.Close()

	// The marker is current, so nothing is scanned and the old spellings
	// survive — the gate behaves like the other backfills.
	database = openFileDB(t, path)
	var got string
	if err := database.QueryRow("SELECT language FROM books WHERE id = ?", ids["en"]).Scan(&got); err != nil {
		t.Fatal(err)
	}
	if got != "en" {
		t.Errorf("books.language = %q after a second open, want it untouched: the backfill re-ran despite a current revision marker", got)
	}

	// Dropping the marker stands in for bumping models.NormalizeLanguageCodeRev.
	if _, err := database.Exec("DELETE FROM settings WHERE key = ?", backfillRevKeyLanguageCode); err != nil {
		t.Fatal(err)
	}
	database.Close()

	database = openFileDB(t, path)
	defer database.Close()
	for old, want := range stored {
		if err := database.QueryRow("SELECT COALESCE(language, '') FROM books WHERE id = ?", ids[old]).Scan(&got); err != nil {
			t.Fatal(err)
		}
		if got != want {
			t.Errorf("books.language %q after the repair run = %q, want %q", old, got, want)
		}
	}
	if err := database.QueryRow("SELECT language FROM editions WHERE id = ?", edition.ID).Scan(&got); err != nil {
		t.Fatal(err)
	}
	if got != "fre" {
		t.Errorf("editions.language = %q after the repair run, want %q", got, "fre")
	}
	if v, ok := settingValue(t, database, backfillRevKeyLanguageCode); !ok || v != strconv.Itoa(models.NormalizeLanguageCodeRev) {
		t.Errorf("marker after the repair run = %q (present %v), want %d", v, ok, models.NormalizeLanguageCodeRev)
	}
}

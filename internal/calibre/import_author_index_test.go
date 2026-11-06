package calibre

import (
	"context"
	"strconv"
	"testing"

	"github.com/vavallee/bindery/internal/models"
)

// countListAuthors wraps the importer's author loader and counts calls.
func countListAuthors(imp *Importer) *int {
	n := 0
	inner := imp.listAuthors
	imp.listAuthors = func(ctx context.Context) ([]models.Author, error) {
		n++
		return inner(ctx)
	}
	return &n
}

// TestImporter_LoadsAuthorsOncePerRun pins #1469: resolveAuthor used to scan
// the whole authors table once per imported book. N books by M authors, some
// pre-existing and some created mid-run, must cost exactly one List call.
func TestImporter_LoadsAuthorsOncePerRun(t *testing.T) {
	imp, fr, authorRepo, bookRepo, _, _, _ := newImporterFixture(t)
	ctx := context.Background()
	pre := &models.Author{ForeignID: "ol:A1", Name: "Alice Author", SortName: "Author, Alice", MetadataProvider: "openlibrary", Monitored: true}
	if err := authorRepo.Create(ctx, pre); err != nil {
		t.Fatal(err)
	}
	calls := countListAuthors(imp)

	var id int64
	for _, name := range []string{"alice author", "Bob Brown", "Carol Clark", "Bob Brown", "CAROL CLARK", "Alice Author"} {
		for n := 0; n < 3; n++ {
			id++
			fr.books = append(fr.books, sampleCalibreBook(id, name+" vol "+strconv.Itoa(int(id)), name))
		}
	}
	if _, err := imp.Run(ctx, "/lib"); err != nil {
		t.Fatalf("run: %v", err)
	}
	if *calls != 1 {
		t.Errorf("List called %d times for %d books, want 1", *calls, len(fr.books))
	}
	authors, _ := authorRepo.List(ctx)
	if len(authors) != 3 {
		t.Errorf("want 3 authors (1 existing + Bob + Carol, case-insensitively), got %d", len(authors))
	}
	books, _ := bookRepo.List(ctx)
	if len(books) != len(fr.books) {
		t.Errorf("want %d books, got %d", len(fr.books), len(books))
	}
}

// TestFindAuthorByName_IndexMatchesScan: lookups through the per-run index
// give the same answers as the one-shot scan, including the tie-breaks, and an
// author created mid-run is findable.
func TestFindAuthorByName_IndexMatchesScan(t *testing.T) {
	imp, _, authorRepo, _, _, _, _ := newImporterFixture(t)
	ctx := context.Background()
	variant := &models.Author{ForeignID: "OL-V", Name: "Tolkien, J.R.R.", SortName: "Tolkien, J.R.R."}
	exact := &models.Author{ForeignID: "OL-E", Name: "J. R. R. Tolkien", SortName: "Tolkien, J. R. R."}
	variant2 := &models.Author{ForeignID: "OL-V2", Name: "J.R.R. Tolkien", SortName: "Tolkien, J.R.R. 2"}
	for _, a := range []*models.Author{variant, exact, variant2} {
		if err := authorRepo.Create(ctx, a); err != nil {
			t.Fatal(err)
		}
	}
	queries := []string{"j. r. r. tolkien", "J.R.R.  Tolkien", "Tolkien, J. R. R.", "Nobody Here"}
	want := map[string]int64{}
	for _, q := range queries {
		got, err := imp.findAuthorByName(ctx, q)
		if err != nil {
			t.Fatal(err)
		}
		if got != nil {
			want[q] = got.ID
		}
	}

	imp.authorIdx = &authorIndex{}
	calls := countListAuthors(imp)
	for _, q := range queries {
		got, err := imp.findAuthorByName(ctx, q)
		if err != nil {
			t.Fatal(err)
		}
		var id int64
		if got != nil {
			id = got.ID
		}
		if id != want[q] {
			t.Errorf("indexed %q = %d, scan = %d", q, id, want[q])
		}
	}
	if want["J.R.R.  Tolkien"] != variant.ID {
		t.Errorf("tie-break should be lowest id %d, got %d", variant.ID, want["J.R.R.  Tolkien"])
	}

	fresh := &models.Author{ForeignID: "OL-N", Name: "Newly Created", SortName: "Created, Newly"}
	if err := authorRepo.Create(ctx, fresh); err != nil {
		t.Fatal(err)
	}
	imp.noteAuthorCreated(fresh)
	got, err := imp.findAuthorByName(ctx, "newly created")
	if err != nil || got == nil || got.ID != fresh.ID {
		t.Errorf("mid-run author not found: %+v %v", got, err)
	}
	if *calls != 1 {
		t.Errorf("List called %d times, want 1", *calls)
	}
}

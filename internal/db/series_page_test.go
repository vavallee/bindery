package db

import (
	"context"
	"testing"

	"github.com/vavallee/bindery/internal/models"
)

// seedPagedSeries creates n series titled "Series 01".."Series NN" (zero
// padded so title order matches creation order) each with one linked book.
func seedPagedSeries(t *testing.T, database *seriesPageFixture, n int) {
	t.Helper()
	ctx := context.Background()
	author := &models.Author{ForeignID: "OL-PAGE-A", Name: "Paged Author", SortName: "Paged Author"}
	if err := database.authors.Create(ctx, author); err != nil {
		t.Fatal(err)
	}
	for i := 1; i <= n; i++ {
		suffix := string(rune('0'+i/10)) + string(rune('0'+i%10))
		book := &models.Book{
			ForeignID: "OL-PAGE-B" + suffix,
			AuthorID:  author.ID,
			Title:     "Book " + suffix,
			SortTitle: "Book " + suffix,
			Status:    models.BookStatusWanted,
		}
		if err := database.books.Create(ctx, book); err != nil {
			t.Fatal(err)
		}
		s := &models.Series{ForeignID: "OL-PAGE-S" + suffix, Title: "Series " + suffix}
		if err := database.series.Create(ctx, s); err != nil {
			t.Fatal(err)
		}
		if err := database.series.LinkBook(ctx, s.ID, book.ID, "1", true); err != nil {
			t.Fatal(err)
		}
	}
}

type seriesPageFixture struct {
	series  *SeriesRepo
	books   *BookRepo
	authors *AuthorRepo
	users   *UserRepo
}

func newSeriesPageFixture(t *testing.T) *seriesPageFixture {
	t.Helper()
	database, err := OpenMemory()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { database.Close() })
	return &seriesPageFixture{
		series:  NewSeriesRepo(database),
		books:   NewBookRepo(database),
		authors: NewAuthorRepo(database),
		users:   NewUserRepo(database),
	}
}

// TestListPageWithBooksForUser_PagesInTitleOrder covers #2345: the page query
// must walk the collection in title order with no gaps and no repeats, and
// report the unpaginated total on every page.
func TestListPageWithBooksForUser_PagesInTitleOrder(t *testing.T) {
	f := newSeriesPageFixture(t)
	seedPagedSeries(t, f, 7)
	ctx := context.Background()

	var seen []string
	for offset := 0; offset < 7; offset += 3 {
		page, total, err := f.series.ListPageWithBooksForUser(ctx, 0, 3, offset)
		if err != nil {
			t.Fatalf("page at offset %d: %v", offset, err)
		}
		if total != 7 {
			t.Errorf("total at offset %d = %d, want 7", offset, total)
		}
		for _, s := range page {
			seen = append(seen, s.Title)
		}
	}
	if len(seen) != 7 {
		t.Fatalf("paged through %d series, want 7: %v", len(seen), seen)
	}
	for i, title := range seen {
		want := "Series 0" + string(rune('0'+i+1))
		if title != want {
			t.Errorf("series %d = %q, want %q", i, title, want)
		}
	}

	// Past the end is an empty page, not an error, and still carries the total.
	page, total, err := f.series.ListPageWithBooksForUser(ctx, 0, 3, 99)
	if err != nil {
		t.Fatalf("page past the end: %v", err)
	}
	if len(page) != 0 || total != 7 {
		t.Errorf("page past the end = %d rows, total %d; want 0 rows, total 7", len(page), total)
	}
}

// TestListPageWithBooksForUser_KeepsBooksWholePerSeries is why the page runs as
// two queries. LIMIT applies to result rows and the JOIN emits one row per
// book, so a naive windowed JOIN would truncate a series' book list at the page
// boundary.
func TestListPageWithBooksForUser_KeepsBooksWholePerSeries(t *testing.T) {
	f := newSeriesPageFixture(t)
	ctx := context.Background()

	author := &models.Author{ForeignID: "OL-WHOLE-A", Name: "Whole", SortName: "Whole"}
	if err := f.authors.Create(ctx, author); err != nil {
		t.Fatal(err)
	}
	s := &models.Series{ForeignID: "OL-WHOLE-S", Title: "Fat Series"}
	if err := f.series.Create(ctx, s); err != nil {
		t.Fatal(err)
	}
	for i := 1; i <= 5; i++ {
		book := &models.Book{
			ForeignID: "OL-WHOLE-B" + string(rune('0'+i)),
			AuthorID:  author.ID,
			Title:     "Volume " + string(rune('0'+i)),
			Status:    models.BookStatusWanted,
		}
		if err := f.books.Create(ctx, book); err != nil {
			t.Fatal(err)
		}
		if err := f.series.LinkBook(ctx, s.ID, book.ID, string(rune('0'+i)), true); err != nil {
			t.Fatal(err)
		}
	}

	page, total, err := f.series.ListPageWithBooksForUser(ctx, 0, 1, 0)
	if err != nil {
		t.Fatal(err)
	}
	if total != 1 || len(page) != 1 {
		t.Fatalf("page = %d rows, total %d; want 1 and 1", len(page), total)
	}
	if len(page[0].Books) != 5 {
		t.Errorf("series carries %d books on a limit=1 page, want all 5", len(page[0].Books))
	}
}

// TestListPageWithBooksForUser_ScopesBooksByOwner checks the paged path applies
// the same per-user book predicate as the unpaginated one (#1457): series rows
// are global, the books hanging off them are not.
func TestListPageWithBooksForUser_ScopesBooksByOwner(t *testing.T) {
	f := newSeriesPageFixture(t)
	ctx := context.Background()

	author := &models.Author{ForeignID: "OL-OWN-A", Name: "Owned", SortName: "Owned"}
	if err := f.authors.Create(ctx, author); err != nil {
		t.Fatal(err)
	}
	s := &models.Series{ForeignID: "OL-OWN-S", Title: "Shared Series"}
	if err := f.series.Create(ctx, s); err != nil {
		t.Fatal(err)
	}
	// books.owner_user_id carries a foreign key, so the owners have to exist.
	alice, err := f.users.GetOrCreateByOIDC(ctx, "https://idp.example", "sub-alice", "alice", "alice@example.com", "Alice", "user")
	if err != nil {
		t.Fatal(err)
	}
	bob, err := f.users.GetOrCreateByOIDC(ctx, "https://idp.example", "sub-bob", "bob", "bob@example.com", "Bob", "user")
	if err != nil {
		t.Fatal(err)
	}

	mine := &models.Book{ForeignID: "OL-OWN-B1", AuthorID: author.ID, Title: "Mine", Status: models.BookStatusWanted, OwnerUserID: alice.ID}
	theirs := &models.Book{ForeignID: "OL-OWN-B2", AuthorID: author.ID, Title: "Theirs", Status: models.BookStatusWanted, OwnerUserID: bob.ID}
	for _, b := range []*models.Book{mine, theirs} {
		if err := f.books.Create(ctx, b); err != nil {
			t.Fatal(err)
		}
		if err := f.series.LinkBook(ctx, s.ID, b.ID, "1", true); err != nil {
			t.Fatal(err)
		}
	}

	page, _, err := f.series.ListPageWithBooksForUser(ctx, alice.ID, 10, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(page) != 1 {
		t.Fatalf("page = %d series, want 1", len(page))
	}
	if len(page[0].Books) != 1 || page[0].Books[0].BookID != mine.ID {
		t.Errorf("alice sees %+v, want only their own book", page[0].Books)
	}

	unscoped, _, err := f.series.ListPageWithBooksForUser(ctx, 0, 10, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(unscoped) != 1 || len(unscoped[0].Books) != 2 {
		t.Errorf("unscoped page = %d series with %d books, want 1 series with 2 books", len(unscoped), len(unscoped[0].Books))
	}
}

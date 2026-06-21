package hardcoverlistsyncer

import (
	"context"
	"errors"
	"testing"

	"github.com/vavallee/bindery/internal/db"
	"github.com/vavallee/bindery/internal/metadata/hardcover"
	"github.com/vavallee/bindery/internal/models"
)

// The syncList path constructs a real hardcover.Client whose GraphQL
// endpoint is a package-level const, so it cannot be redirected to a test
// server without changing source. These tests cover the paths that don't
// reach the network: the empty-list short-circuit, error propagation from
// the ImportList repo, the sortName helper, and the constructor.

func newTestSyncer(t *testing.T) (*ListSyncer, *db.ImportListRepo) {
	t.Helper()
	database, err := db.OpenMemory()
	if err != nil {
		t.Fatalf("open memory db: %v", err)
	}
	t.Cleanup(func() { database.Close() })

	importLists := db.NewImportListRepo(database)
	authors := db.NewAuthorRepo(database)
	books := db.NewBookRepo(database)
	return New(importLists, authors, books), importLists
}

func TestNew_WiresRepos(t *testing.T) {
	s, _ := newTestSyncer(t)
	if s == nil {
		t.Fatal("New returned nil")
		return
	}
	if s.importLists == nil || s.authors == nil || s.books == nil {
		t.Errorf("expected all repo fields to be set, got %+v", s)
	}
}

// TestSync_NoEnabledLists exercises the early-return when no hardcover
// import lists are enabled. This is the happy no-op path: Sync must
// succeed without touching the network.
func TestSync_NoEnabledLists(t *testing.T) {
	s, _ := newTestSyncer(t)
	if err := s.Sync(context.Background()); err != nil {
		t.Errorf("Sync on empty list set: want nil, got %v", err)
	}
}

// TestSync_IgnoresNonHardcoverLists verifies that only lists with
// Type="hardcover" are considered. Seeding a goodreads list should not
// pull it into the sync loop, so the call is still a no-op.
func TestSync_IgnoresNonHardcoverLists(t *testing.T) {
	s, repo := newTestSyncer(t)
	ctx := context.Background()

	// Not a hardcover list — must be ignored by ListByType("hardcover").
	il := testImportList("Goodreads", "goodreads", true)
	if err := repo.Create(ctx, &il); err != nil {
		t.Fatalf("seed: %v", err)
	}
	if err := s.Sync(ctx); err != nil {
		t.Errorf("Sync: want nil, got %v", err)
	}
}

// TestSync_IgnoresDisabledHardcoverLists verifies disabled hardcover lists
// are filtered out by the ImportListRepo (ListByType only returns enabled).
func TestSync_IgnoresDisabledHardcoverLists(t *testing.T) {
	s, repo := newTestSyncer(t)
	ctx := context.Background()

	il := testImportList("DisabledHC", "hardcover", false)
	if err := repo.Create(ctx, &il); err != nil {
		t.Fatalf("seed: %v", err)
	}
	if err := s.Sync(ctx); err != nil {
		t.Errorf("Sync: want nil, got %v", err)
	}
}

func TestSortName(t *testing.T) {
	tests := []struct {
		in, want string
	}{
		{"", ""},
		{"Cher", "Cher"},            // single token → unchanged
		{"Andy Weir", "Weir, Andy"}, // two tokens
		{"Ursula K. Le Guin", "Guin, Ursula K. Le"}, // 4 tokens: last → front
		{"  Andy   Weir  ", "Weir, Andy"},           // whitespace normalised
	}
	for _, tt := range tests {
		if got := sortName(tt.in); got != tt.want {
			t.Errorf("sortName(%q) = %q, want %q", tt.in, got, tt.want)
		}
	}
}

// Compile-time check: *ListSyncer satisfies the HCListSyncer interface
// (the whole point of the interface's existence — keeps the scheduler
// from needing to import this package).
func TestHCListSyncerInterfaceSatisfied(t *testing.T) {
	var _ HCListSyncer = (*ListSyncer)(nil)
}

// RunSync is a fire-and-forget wrapper around Sync. With no enabled lists
// it must return cleanly (no panic, no error to observe).
func TestRunSync_NoEnabledLists(t *testing.T) {
	s, _ := newTestSyncer(t)
	// Should not panic; errors are swallowed inside RunSync by design.
	s.RunSync(context.Background())
}

// testImportList builds a models.ImportList pointer suitable for seeding
// via ImportListRepo.Create. The repo's Create only requires the fields
// set here.
func testImportList(name, typ string, enabled bool) models.ImportList {
	return models.ImportList{
		Name:    name,
		Type:    typ,
		URL:     "some-slug",
		APIKey:  "irrelevant-for-these-tests",
		Enabled: enabled,
	}
}

// TestSyncOne_ErrNotFound verifies that SyncOne returns ErrNotFound when the
// requested list ID does not exist in the database.
func TestSyncOne_ErrNotFound(t *testing.T) {
	s, _ := newTestSyncer(t)
	err := s.SyncOne(context.Background(), 99999)
	if !errors.Is(err, ErrNotFound) {
		t.Errorf("SyncOne(missing id): want ErrNotFound, got %v", err)
	}
}

// TestSyncOne_ErrWrongType verifies that SyncOne returns ErrWrongType when the
// list exists but has a type other than "hardcover".
func TestSyncOne_ErrWrongType(t *testing.T) {
	s, repo := newTestSyncer(t)
	ctx := context.Background()

	il := testImportList("My Goodreads", "goodreads", true)
	if err := repo.Create(ctx, &il); err != nil {
		t.Fatalf("seed: %v", err)
	}
	// repo.Create sets il.ID via LastInsertId
	err := s.SyncOne(ctx, il.ID)
	if !errors.Is(err, ErrWrongType) {
		t.Errorf("SyncOne(goodreads list): want ErrWrongType, got %v", err)
	}
}

func TestSyncOne_ErrDisabled(t *testing.T) {
	s, repo := newTestSyncer(t)
	ctx := context.Background()

	il := testImportList("Disabled", "hardcover", false)
	if err := repo.Create(ctx, &il); err != nil {
		t.Fatalf("seed: %v", err)
	}
	err := s.SyncOne(ctx, il.ID)
	if !errors.Is(err, ErrDisabled) {
		t.Errorf("SyncOne(disabled list): want ErrDisabled, got %v", err)
	}
}

func TestSyncOne_UsesGlobalTokenWhenListHasNoOverride(t *testing.T) {
	s, repo := newTestSyncer(t)
	ctx := context.Background()

	il := testImportList("Global", "hardcover", true)
	il.APIKey = ""
	if err := repo.Create(ctx, &il); err != nil {
		t.Fatalf("seed: %v", err)
	}
	s.WithTokenSource(func(context.Context) string { return "global-token" })
	var gotToken string
	s.WithClientFactory(func(token string) hardcoverClient {
		gotToken = token
		return &fakeHardcoverClient{lists: []hardcover.HCList{{ID: 12, Slug: il.URL, Name: il.Name}}}
	})

	if err := s.SyncOne(ctx, il.ID); err != nil {
		t.Fatalf("SyncOne: %v", err)
	}
	if gotToken != "global-token" {
		t.Fatalf("token = %q, want global-token", gotToken)
	}
}

func TestSyncOne_PerListTokenOverridesGlobalToken(t *testing.T) {
	s, repo := newTestSyncer(t)
	ctx := context.Background()

	il := testImportList("Override", "hardcover", true)
	il.APIKey = "per-list-token"
	if err := repo.Create(ctx, &il); err != nil {
		t.Fatalf("seed: %v", err)
	}
	s.WithTokenSource(func(context.Context) string { return "global-token" })
	var gotToken string
	s.WithClientFactory(func(token string) hardcoverClient {
		gotToken = token
		return &fakeHardcoverClient{lists: []hardcover.HCList{{ID: 24, Slug: il.URL, Name: il.Name}}}
	})

	if err := s.SyncOne(ctx, il.ID); err != nil {
		t.Fatalf("SyncOne: %v", err)
	}
	if gotToken != "per-list-token" {
		t.Fatalf("token = %q, want per-list-token", gotToken)
	}
}

func TestSyncOne_ReusesAuthorByAlternateIdentifier(t *testing.T) {
	s, repo := newTestSyncer(t)
	ctx := context.Background()

	existing := &models.Author{
		ForeignID:        "OL-AUTHOR-X",
		Name:             "Author X",
		SortName:         "X, Author",
		MetadataProvider: "openlibrary",
		Monitored:        true,
	}
	if err := s.authors.Create(ctx, existing); err != nil {
		t.Fatalf("seed author: %v", err)
	}
	if err := s.authors.UpsertAuthorIdentifier(ctx, existing.ID, "hc:author-x"); err != nil {
		t.Fatalf("seed author identifier: %v", err)
	}
	il := testImportList("Alt Author", "hardcover", true)
	if err := repo.Create(ctx, &il); err != nil {
		t.Fatalf("seed list: %v", err)
	}
	book := bookWithSeriesRef("hc:book-x", "Book X", nil)
	s.WithClientFactory(func(string) hardcoverClient {
		return &fakeHardcoverClient{
			lists: []hardcover.HCList{{ID: 12, Slug: il.URL, Name: il.Name}},
			books: []models.Book{book},
		}
	})

	if err := s.SyncOne(ctx, il.ID); err != nil {
		t.Fatalf("SyncOne: %v", err)
	}
	authors, err := s.authors.List(ctx)
	if err != nil {
		t.Fatalf("List authors: %v", err)
	}
	if len(authors) != 1 {
		t.Fatalf("authors = %d, want existing author reused", len(authors))
	}
	imported, err := s.books.GetByForeignID(ctx, "hc:book-x")
	if err != nil || imported == nil {
		t.Fatalf("imported book = %+v err=%v, want persisted", imported, err)
	}
	if imported.AuthorID != existing.ID {
		t.Fatalf("book author_id = %d, want existing author %d", imported.AuthorID, existing.ID)
	}
}

func TestSyncOne_ErrMissingToken(t *testing.T) {
	s, repo := newTestSyncer(t)
	ctx := context.Background()

	il := testImportList("No token", "hardcover", true)
	il.APIKey = ""
	if err := repo.Create(ctx, &il); err != nil {
		t.Fatalf("seed: %v", err)
	}

	err := s.SyncOne(ctx, il.ID)
	if !errors.Is(err, ErrMissingToken) {
		t.Errorf("SyncOne(no token): want ErrMissingToken, got %v", err)
	}
}

type fakeHardcoverClient struct {
	lists    []hardcover.HCList
	books    []models.Book
	editions []models.Edition
}

func (f *fakeHardcoverClient) GetUserLists(context.Context) ([]hardcover.HCList, error) {
	return f.lists, nil
}

func (f *fakeHardcoverClient) GetListBooks(context.Context, int) ([]models.Book, error) {
	return f.books, nil
}

func (f *fakeHardcoverClient) GetEditions(context.Context, string) ([]models.Edition, error) {
	return f.editions, nil
}

// newTestSyncerWithSeries returns a syncer wired against a real in-memory DB
// and gives the test direct access to the SeriesRepo so it can verify links
// were actually persisted.
func newTestSyncerWithSeries(t *testing.T) (*ListSyncer, *db.ImportListRepo, *db.SeriesRepo) {
	t.Helper()
	database, err := db.OpenMemory()
	if err != nil {
		t.Fatalf("open memory db: %v", err)
	}
	t.Cleanup(func() { database.Close() })
	importLists := db.NewImportListRepo(database)
	authors := db.NewAuthorRepo(database)
	books := db.NewBookRepo(database)
	series := db.NewSeriesRepo(database)
	s := New(importLists, authors, books).WithSeriesRepo(series)
	return s, importLists, series
}

func bookWithSeriesRef(foreignID, title string, refs []models.SeriesRef) models.Book {
	return models.Book{
		ForeignID:        foreignID,
		Title:            title,
		SortTitle:        title,
		MetadataProvider: "hardcover",
		Author: &models.Author{
			ForeignID:        "hc:author-x",
			Name:             "Author X",
			SortName:         "X, Author",
			MetadataProvider: "hardcover",
		},
		SeriesRefs: refs,
	}
}

// TestSyncOne_LinksSeriesRefsAfterBookImport is the issue #805 happy path:
// books that arrive with a populated SeriesRefs slice must be linked through
// the SeriesRepo so the import doesn't quietly lose series membership.
func TestSyncOne_LinksSeriesRefsAfterBookImport(t *testing.T) {
	s, repo, series := newTestSyncerWithSeries(t)
	ctx := context.Background()

	il := testImportList("With Series", "hardcover", true)
	if err := repo.Create(ctx, &il); err != nil {
		t.Fatalf("seed: %v", err)
	}

	book := bookWithSeriesRef("hc:dune", "Dune", []models.SeriesRef{{
		ForeignID: "hc-series:17",
		Title:     "Dune Chronicles",
		Position:  "1",
		Primary:   true,
	}})
	s.WithClientFactory(func(string) hardcoverClient {
		return &fakeHardcoverClient{
			lists: []hardcover.HCList{{ID: 12, Slug: il.URL, Name: il.Name}},
			books: []models.Book{book},
		}
	})

	if err := s.SyncOne(ctx, il.ID); err != nil {
		t.Fatalf("SyncOne: %v", err)
	}

	persisted, err := series.GetByForeignID(ctx, "hc-series:17")
	if err != nil {
		t.Fatalf("GetByForeignID: %v", err)
	}
	if persisted == nil {
		t.Fatal("series was not created during sync")
		return
	}
	booksInSeries, err := series.ListBooksInSeries(ctx, persisted.ID)
	if err != nil {
		t.Fatalf("ListBooksInSeries: %v", err)
	}
	if len(booksInSeries) != 1 || booksInSeries[0].ForeignID != "hc:dune" {
		t.Fatalf("series should contain the imported book, got %+v", booksInSeries)
	}
}

// TestSyncOne_SeriesLinkErrorDoesNotBlockImport guarantees the best-effort
// contract: when the SeriesRepo errors out, the book is still imported and
// the sync does not fail the whole list. Regression guard for the warning
// path.
func TestSyncOne_SeriesLinkErrorDoesNotBlockImport(t *testing.T) {
	s, repo, _ := newTestSyncerWithSeries(t)
	ctx := context.Background()

	stub := &erroringSeriesRepo{}
	s.series = stub

	il := testImportList("Series Error", "hardcover", true)
	if err := repo.Create(ctx, &il); err != nil {
		t.Fatalf("seed: %v", err)
	}

	book := bookWithSeriesRef("hc:dune", "Dune", []models.SeriesRef{{
		ForeignID: "hc-series:17",
		Title:     "Dune Chronicles",
		Position:  "1",
		Primary:   true,
	}})
	s.WithClientFactory(func(string) hardcoverClient {
		return &fakeHardcoverClient{
			lists: []hardcover.HCList{{ID: 12, Slug: il.URL, Name: il.Name}},
			books: []models.Book{book},
		}
	})

	if err := s.SyncOne(ctx, il.ID); err != nil {
		t.Fatalf("SyncOne should succeed even when series linking errors: %v", err)
	}
	if stub.upsertCalls == 0 {
		t.Errorf("expected CreateOrGet to be attempted, got 0 calls")
	}

	imported, err := s.books.GetByForeignID(ctx, "hc:dune")
	if err != nil || imported == nil {
		t.Fatalf("book should still be imported despite series link failure: %v, %v", imported, err)
	}
}

// erroringSeriesRepo always fails CreateOrGet so we can prove the syncer
// swallows the error.
type erroringSeriesRepo struct {
	upsertCalls int
	linkCalls   int
}

func (e *erroringSeriesRepo) CreateOrGet(context.Context, *models.Series) error {
	e.upsertCalls++
	return errors.New("simulated upsert failure")
}

func (e *erroringSeriesRepo) LinkBook(context.Context, int64, int64, string, bool) error {
	e.linkCalls++
	return errors.New("should not be called when upsert fails")
}

// TestSyncOne_NoSeriesRepo_NoSeriesLinkAttempted protects the optional
// nature of the repo: the syncer must remain functional when WithSeriesRepo
// was never called (e.g. older deployments wired before #805 landed).
func TestSyncOne_NoSeriesRepo_NoSeriesLinkAttempted(t *testing.T) {
	s, _ := newTestSyncer(t)
	if s.series != nil {
		t.Fatalf("default syncer should have no series repo, got %T", s.series)
	}

	il := testImportList("No Series Repo", "hardcover", true)
	importLists := s.importLists
	ctx := context.Background()
	if err := importLists.Create(ctx, &il); err != nil {
		t.Fatalf("seed: %v", err)
	}

	book := bookWithSeriesRef("hc:dune", "Dune", []models.SeriesRef{{
		ForeignID: "hc-series:17",
		Title:     "Dune Chronicles",
		Position:  "1",
		Primary:   true,
	}})
	s.WithClientFactory(func(string) hardcoverClient {
		return &fakeHardcoverClient{
			lists: []hardcover.HCList{{ID: 12, Slug: il.URL, Name: il.Name}},
			books: []models.Book{book},
		}
	})

	if err := s.SyncOne(ctx, il.ID); err != nil {
		t.Fatalf("SyncOne: %v", err)
	}
	imported, err := s.books.GetByForeignID(ctx, "hc:dune")
	if err != nil || imported == nil {
		t.Fatalf("book should be imported: %v, %v", imported, err)
	}
}

func TestSync_HydratesHardcoverEditions(t *testing.T) {
	database, err := db.OpenMemory()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { database.Close() })
	ctx := context.Background()
	importLists := db.NewImportListRepo(database)
	authors := db.NewAuthorRepo(database)
	books := db.NewBookRepo(database)
	editions := db.NewEditionRepo(database)

	audioASIN := "B123LISTEN"
	client := &fakeHardcoverClient{
		lists: []hardcover.HCList{{ID: 10, Slug: "want-to-read", Name: "Want to Read"}},
		books: []models.Book{{
			ForeignID:        "hc:list-book",
			Title:            "List Book",
			SortTitle:        "List Book",
			MetadataProvider: "hardcover",
			MediaType:        models.MediaTypeAudiobook,
			Genres:           []string{},
			Author: &models.Author{
				ForeignID:        "hc:list-author",
				Name:             "List Author",
				SortName:         "Author, List",
				MetadataProvider: "hardcover",
			},
		}},
		editions: []models.Edition{{
			ForeignID: "hc:list-book-audio",
			Title:     "List Book",
			ASIN:      &audioASIN,
			Format:    "Audiobook",
			Monitored: true,
		}},
	}
	syncer := New(importLists, authors, books).
		WithEditionHydration(editions, nil).
		WithClientFactory(func(string) hardcoverClient { return client })
	il := testImportList("Want", "hardcover", true)
	il.URL = "want-to-read"
	if err := importLists.Create(ctx, &il); err != nil {
		t.Fatal(err)
	}

	if err := syncer.Sync(ctx); err != nil {
		t.Fatal(err)
	}
	book, err := books.GetByForeignID(ctx, "hc:list-book")
	if err != nil {
		t.Fatal(err)
	}
	if book == nil || book.ASIN != audioASIN {
		t.Fatalf("book was not hydrated: %+v", book)
	}
	got, err := editions.ListByBook(ctx, book.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].ForeignID != "hc:list-book-audio" {
		t.Fatalf("expected hydrated edition, got %+v", got)
	}
}

// TestNormalizeAuthorName pins the canonicalization rules used by the
// name-based fallback in ensureAuthor. Pairs of names that should collapse to
// the same key must collapse, and distinct authors must remain distinct.
func TestNormalizeAuthorName(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want string
	}{
		{"empty", "", ""},
		{"whitespace-only", "   \t\n", ""},
		{"plain", "George R. R. Martin", "george r r martin"},
		{"periods-stripped", "George R.R. Martin", "george r r martin"},
		{"no-periods", "George R R Martin", "george r r martin"},
		{"collapsed-whitespace", "Stephen   King", "stephen king"},
		{"trimmed", "  Ursula K. Le Guin  ", "ursula k le guin"},
		{"lowercased", "STEPHEN KING", "stephen king"},
		{"commas-stripped", "Doe, John", "doe john"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := normalizeAuthorName(tc.in); got != tc.want {
				t.Errorf("normalizeAuthorName(%q) = %q, want %q", tc.in, got, tc.want)
			}
		})
	}
}

// TestNormalizeAuthorName_DistinctAuthors verifies the helper doesn't
// over-collapse. Authors that share a surname or a common first-name token
// must NOT map to the same key — otherwise the fallback in ensureAuthor
// would incorrectly merge them.
func TestNormalizeAuthorName_DistinctAuthors(t *testing.T) {
	pairs := [][2]string{
		{"Stephen King", "Stephen Hawking"},      // shared first name only
		{"George R. R. Martin", "George Martin"}, // middle-initials differ
	}
	for _, p := range pairs {
		if normalizeAuthorName(p[0]) == normalizeAuthorName(p[1]) {
			t.Errorf("expected %q and %q to be distinct, both normalize to %q",
				p[0], p[1], normalizeAuthorName(p[0]))
		}
	}
}

// TestSyncOne_ReusesAuthorByNormalizedName is the regression test for
// issue #1223. A library populated by ABS/OpenLibrary has authors with OL
// foreign_ids; a Hardcover list import brings authors with HC foreign_ids.
// The two never match by primary foreign_id or alias. Without the fix, each
// HC book creates a duplicate author row. With the fix, the syncer falls
// back to a normalized-name match, attaches the HC id as an alias, and
// reuses the existing author.
func TestSyncOne_ReusesAuthorByNormalizedName(t *testing.T) {
	s, repo := newTestSyncer(t)
	ctx := context.Background()

	existing := &models.Author{
		ForeignID:        "OL-AUTHOR-MARTIN",
		Name:             "George R. R. Martin",
		SortName:         "Martin, George R. R.",
		MetadataProvider: "openlibrary",
		Monitored:        true,
	}
	if err := s.authors.Create(ctx, existing); err != nil {
		t.Fatalf("seed author: %v", err)
	}

	il := testImportList("HC Martin List", "hardcover", true)
	if err := repo.Create(ctx, &il); err != nil {
		t.Fatalf("seed list: %v", err)
	}

	// Hardcover hands back the same author under a different surface name
	// ("George R.R. Martin" without spaces around the periods) and a
	// brand-new HC foreign_id that nothing in the library knows about.
	book := bookWithSeriesRef("hc:game-of-thrones", "A Game of Thrones", nil)
	book.Author.ForeignID = "hc:george-rr-martin"
	book.Author.Name = "George R.R. Martin"
	book.Author.SortName = ""
	book.Author.MetadataProvider = "hardcover"
	s.WithClientFactory(func(string) hardcoverClient {
		return &fakeHardcoverClient{
			lists: []hardcover.HCList{{ID: 12, Slug: il.URL, Name: il.Name}},
			books: []models.Book{book},
		}
	})

	if err := s.SyncOne(ctx, il.ID); err != nil {
		t.Fatalf("SyncOne: %v", err)
	}

	// Exactly one author must exist — the existing OL author, reused.
	authors, err := s.authors.List(ctx)
	if err != nil {
		t.Fatalf("List authors: %v", err)
	}
	if len(authors) != 1 {
		names := make([]string, 0, len(authors))
		for _, a := range authors {
			names = append(names, a.Name)
		}
		t.Fatalf("authors = %d (%v), want 1 (existing author reused; no duplicate)",
			len(authors), names)
	}
	if authors[0].ID != existing.ID {
		t.Fatalf("reused author id = %d, want existing %d", authors[0].ID, existing.ID)
	}

	// The book must be linked to the existing author.
	imported, err := s.books.GetByForeignID(ctx, "hc:game-of-thrones")
	if err != nil || imported == nil {
		t.Fatalf("imported book = %+v err=%v, want persisted", imported, err)
	}
	if imported.AuthorID != existing.ID {
		t.Fatalf("book author_id = %d, want existing author %d", imported.AuthorID, existing.ID)
	}

	// The HC foreign_id must have been attached as an alias so the next
	// list sync hits the cheap path (GetByAnyForeignID) directly.
	aliases, err := s.authors.ListAuthorIdentifiers(ctx, existing.ID)
	if err != nil {
		t.Fatalf("ListIdentifiers: %v", err)
	}
	found := false
	for _, a := range aliases {
		if a.ForeignID == "hc:george-rr-martin" {
			found = true
			break
		}
	}
	if !found {
		t.Fatalf("expected HC alias %q attached to existing author, got %+v",
			"hc:george-rr-martin", aliases)
	}
}

// TestSyncOne_CreatesNewAuthorWhenNoNameMatch protects the fallback: when
// neither the foreign_id nor a normalized-name match exists, the syncer
// must still create the author. This guards against the fallback silently
// swallowing real new authors.
func TestSyncOne_CreatesNewAuthorWhenNoNameMatch(t *testing.T) {
	s, repo := newTestSyncer(t)
	ctx := context.Background()

	il := testImportList("HC Fresh List", "hardcover", true)
	if err := repo.Create(ctx, &il); err != nil {
		t.Fatalf("seed list: %v", err)
	}
	book := bookWithSeriesRef("hc:fresh-book", "Fresh Book", nil)
	s.WithClientFactory(func(string) hardcoverClient {
		return &fakeHardcoverClient{
			lists: []hardcover.HCList{{ID: 12, Slug: il.URL, Name: il.Name}},
			books: []models.Book{book},
		}
	})

	if err := s.SyncOne(ctx, il.ID); err != nil {
		t.Fatalf("SyncOne: %v", err)
	}
	authors, err := s.authors.List(ctx)
	if err != nil {
		t.Fatalf("List authors: %v", err)
	}
	if len(authors) != 1 {
		t.Fatalf("authors = %d, want 1 new author created", len(authors))
	}
	imported, err := s.books.GetByForeignID(ctx, "hc:fresh-book")
	if err != nil || imported == nil {
		t.Fatalf("book should be imported: %+v err=%v", imported, err)
	}
	if imported.AuthorID != authors[0].ID {
		t.Fatalf("book author_id = %d, want new author %d", imported.AuthorID, authors[0].ID)
	}
}

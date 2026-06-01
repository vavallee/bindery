package migrate

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/vavallee/bindery/internal/db"
	"github.com/vavallee/bindery/internal/models"
)

// fakeGoodreadsResolver is a deterministic stand-in for *metadata.Aggregator.
// isbnHits maps a normalized ISBN to a book; searchHits maps a search query
// substring to a book. A nil entry models a clean miss.
type fakeGoodreadsResolver struct {
	isbnHits   map[string]*models.Book
	searchHits map[string][]models.Book
	isbnCalls  int
	searchCall int
}

func (f *fakeGoodreadsResolver) ResolveBookByISBN(_ context.Context, isbn string) (*models.Book, error) {
	f.isbnCalls++
	if b, ok := f.isbnHits[isbn]; ok {
		return b, nil
	}
	return nil, nil
}

func (f *fakeGoodreadsResolver) SearchBooks(_ context.Context, query string) ([]models.Book, error) {
	f.searchCall++
	for key, books := range f.searchHits {
		if strings.Contains(strings.ToLower(query), strings.ToLower(key)) {
			return books, nil
		}
	}
	return nil, nil
}

// bookWithAuthor builds a metadata-provider book carrying a resolvable author.
func bookWithAuthor(foreignID, title, authorForeignID, authorName string) *models.Book {
	return &models.Book{
		ForeignID: foreignID,
		Title:     title,
		Author: &models.Author{
			ForeignID: authorForeignID,
			Name:      authorName,
		},
	}
}

func TestResolveGoodreadsRows_ISBN13Preferred(t *testing.T) {
	resolver := &fakeGoodreadsResolver{
		isbnHits: map[string]*models.Book{
			"9780593135204": bookWithAuthor("OL1W", "Project Hail Mary", "OLA1", "Andy Weir"),
		},
	}
	rows := []GoodreadsRow{{
		RowNumber:      1,
		Title:          "Project Hail Mary",
		Author:         "Andy Weir",
		ISBN:           "0593135202",
		ISBN13:         "9780593135204",
		ExclusiveShelf: GoodreadsShelfToRead,
	}}

	out := ResolveGoodreadsRows(context.Background(), rows, GoodreadsImportOptions{}, resolver, nil, 0)
	if len(out) != 1 {
		t.Fatalf("want 1 result, got %d", len(out))
	}
	if out[0].Outcome != outcomeResolved {
		t.Fatalf("outcome = %q, want resolved", out[0].Outcome)
	}
	if out[0].MatchedBy != "isbn13" {
		t.Errorf("matchedBy = %q, want isbn13", out[0].MatchedBy)
	}
	// ISBN-13 hit means the ISBN-10 path and the search path must not run.
	if resolver.isbnCalls != 1 {
		t.Errorf("isbn calls = %d, want 1 (isbn13 should short-circuit)", resolver.isbnCalls)
	}
	if resolver.searchCall != 0 {
		t.Errorf("search calls = %d, want 0", resolver.searchCall)
	}
}

func TestResolveGoodreadsRows_FallsBackToISBN10(t *testing.T) {
	resolver := &fakeGoodreadsResolver{
		isbnHits: map[string]*models.Book{
			"0306406152": bookWithAuthor("OL2W", "Fallback Book", "OLA2", "Some Author"),
		},
	}
	rows := []GoodreadsRow{{
		RowNumber:      1,
		Title:          "Fallback Book",
		Author:         "Some Author",
		ISBN:           "0306406152",
		ISBN13:         "9999999999999", // not in isbnHits → miss
		ExclusiveShelf: GoodreadsShelfToRead,
	}}
	out := ResolveGoodreadsRows(context.Background(), rows, GoodreadsImportOptions{}, resolver, nil, 0)
	if out[0].Outcome != outcomeResolved || out[0].MatchedBy != "isbn10" {
		t.Fatalf("expected isbn10 match, got outcome=%q matchedBy=%q", out[0].Outcome, out[0].MatchedBy)
	}
}

// TestResolveGoodreadsRows_TitleAuthorFallback is the critical ISBN-sparse
// path: a row with no ISBN at all must still resolve via title+author search.
func TestResolveGoodreadsRows_TitleAuthorFallback(t *testing.T) {
	resolver := &fakeGoodreadsResolver{
		searchHits: map[string][]models.Book{
			"old book": {*bookWithAuthor("OL3W", "The Old Book", "OLA3", "Jane Austen")},
		},
	}
	rows := []GoodreadsRow{{
		RowNumber:      1,
		Title:          "The Old Book",
		Author:         "Jane Austen",
		ExclusiveShelf: GoodreadsShelfToRead,
		// no ISBN, no ISBN13
	}}
	out := ResolveGoodreadsRows(context.Background(), rows, GoodreadsImportOptions{}, resolver, nil, 0)
	if out[0].Outcome != outcomeResolved {
		t.Fatalf("ISBN-sparse row should resolve via title+author, got %q (%s)", out[0].Outcome, out[0].Reason)
	}
	if out[0].MatchedBy != "title+author" {
		t.Errorf("matchedBy = %q, want title+author", out[0].MatchedBy)
	}
	if resolver.isbnCalls != 0 {
		t.Errorf("isbn calls = %d, want 0 for an ISBN-less row", resolver.isbnCalls)
	}
}

// TestResolveGoodreadsRows_TitleAuthorSkipsAuthorlessResults verifies a search
// result with no resolvable author identity is rejected (it cannot be
// canonicalised), and the next usable result is taken.
func TestResolveGoodreadsRows_TitleAuthorSkipsAuthorlessResults(t *testing.T) {
	authorless := models.Book{ForeignID: "OLX", Title: "Ambiguous", Author: nil}
	usable := *bookWithAuthor("OLY", "Ambiguous", "OLA9", "Real Author")
	resolver := &fakeGoodreadsResolver{
		searchHits: map[string][]models.Book{
			"ambiguous": {authorless, usable},
		},
	}
	rows := []GoodreadsRow{{
		RowNumber:      1,
		Title:          "Ambiguous",
		Author:         "Real Author",
		ExclusiveShelf: GoodreadsShelfToRead,
	}}
	out := ResolveGoodreadsRows(context.Background(), rows, GoodreadsImportOptions{}, resolver, nil, 0)
	if out[0].Outcome != outcomeResolved {
		t.Fatalf("expected resolved, got %q", out[0].Outcome)
	}
	if out[0].book == nil || out[0].book.ForeignID != "OLY" {
		t.Errorf("expected the author-bearing result OLY, got %+v", out[0].book)
	}
}

func TestResolveGoodreadsRows_Unresolved(t *testing.T) {
	resolver := &fakeGoodreadsResolver{}
	rows := []GoodreadsRow{{
		RowNumber:      1,
		Title:          "Nonexistent",
		Author:         "Nobody",
		ExclusiveShelf: GoodreadsShelfToRead,
	}}
	out := ResolveGoodreadsRows(context.Background(), rows, GoodreadsImportOptions{}, resolver, nil, 0)
	if out[0].Outcome != outcomeUnresolved {
		t.Fatalf("outcome = %q, want unresolved", out[0].Outcome)
	}
	if out[0].Reason == "" {
		t.Error("unresolved row should carry a reason")
	}
}

// TestResolveGoodreadsRows_ShelfFilter checks the default (to-read only) and
// that filtered rows cost no provider call.
func TestResolveGoodreadsRows_ShelfFilter(t *testing.T) {
	resolver := &fakeGoodreadsResolver{
		isbnHits: map[string]*models.Book{
			"9780000000001": bookWithAuthor("OLW", "Wanted", "OLA", "A"),
			"9780000000002": bookWithAuthor("OLR", "AlreadyRead", "OLB", "B"),
		},
	}
	rows := []GoodreadsRow{
		{RowNumber: 1, Title: "Wanted", ISBN13: "9780000000001", ExclusiveShelf: GoodreadsShelfToRead},
		{RowNumber: 2, Title: "AlreadyRead", ISBN13: "9780000000002", ExclusiveShelf: GoodreadsShelfRead},
	}

	// Default options → to-read only.
	out := ResolveGoodreadsRows(context.Background(), rows, GoodreadsImportOptions{}, resolver, nil, 0)
	if out[0].Outcome != outcomeResolved {
		t.Errorf("to-read row should resolve, got %q", out[0].Outcome)
	}
	if out[1].Outcome != outcomeSkippedShelf {
		t.Errorf("read row should be shelf-skipped under default filter, got %q", out[1].Outcome)
	}
	if resolver.isbnCalls != 1 {
		t.Errorf("isbn calls = %d, want 1 (shelf-skipped row must not hit a provider)", resolver.isbnCalls)
	}

	// Explicitly include read → both resolve.
	resolver.isbnCalls = 0
	out = ResolveGoodreadsRows(context.Background(), rows,
		GoodreadsImportOptions{Shelves: []string{GoodreadsShelfToRead, GoodreadsShelfRead}}, resolver, nil, 0)
	if out[0].Outcome != outcomeResolved || out[1].Outcome != outcomeResolved {
		t.Errorf("both rows should resolve when read is included: %q, %q", out[0].Outcome, out[1].Outcome)
	}
}

// TestResolveGoodreadsRows_SkipsExisting verifies a book already tracked in
// Bindery is reported as skipped rather than re-imported.
func TestResolveGoodreadsRows_SkipsExisting(t *testing.T) {
	database := newTestDB(t)
	authors := db.NewAuthorRepo(database)
	books := db.NewBookRepo(database)
	ctx := context.Background()

	author := &models.Author{ForeignID: "OLA-EXIST", Name: "Existing Author", SortName: "Author, Existing"}
	if err := authors.Create(ctx, author); err != nil {
		t.Fatalf("create author: %v", err)
	}
	existing := &models.Book{ForeignID: "OL-EXIST-W", AuthorID: author.ID, Title: "Already Here", Genres: []string{}}
	if err := books.Create(ctx, existing); err != nil {
		t.Fatalf("create book: %v", err)
	}

	resolver := &fakeGoodreadsResolver{
		isbnHits: map[string]*models.Book{
			"9780000000009": bookWithAuthor("OL-EXIST-W", "Already Here", "OLA-EXIST", "Existing Author"),
		},
	}
	rows := []GoodreadsRow{{RowNumber: 1, Title: "Already Here", ISBN13: "9780000000009", ExclusiveShelf: GoodreadsShelfToRead}}

	out := ResolveGoodreadsRows(ctx, rows, GoodreadsImportOptions{}, resolver, books, 0)
	if out[0].Outcome != outcomeSkippedExisting {
		t.Fatalf("outcome = %q, want skipped_existing", out[0].Outcome)
	}
}

func TestCommitGoodreadsImport_AddsBooksAndAuthors(t *testing.T) {
	database := newTestDB(t)
	authors := db.NewAuthorRepo(database)
	books := db.NewBookRepo(database)
	ctx := context.Background()

	resolved := []GoodreadsResolvedRow{
		{
			Row:       GoodreadsRow{Title: "Book One"},
			Outcome:   outcomeResolved,
			MatchedBy: "isbn13",
			book:      bookWithAuthor("OL-W1", "Book One", "OL-A1", "Author One"),
		},
		{
			// Second book by the same author — the author must be reused.
			Row:       GoodreadsRow{Title: "Book Two"},
			Outcome:   outcomeResolved,
			MatchedBy: "title+author",
			book:      bookWithAuthor("OL-W2", "Book Two", "OL-A1", "Author One"),
		},
		{
			// Non-resolved rows are ignored by commit.
			Row:     GoodreadsRow{Title: "Skipped"},
			Outcome: outcomeUnresolved,
		},
	}

	result := CommitGoodreadsImport(ctx, resolved, authors, books)
	if result.Added != 2 {
		t.Fatalf("added = %d, want 2; failures=%v", result.Added, result.Failures)
	}
	if result.Failed != 0 {
		t.Errorf("failed = %d, want 0", result.Failed)
	}

	// Both books must be persisted as monitored + wanted.
	for _, fid := range []string{"OL-W1", "OL-W2"} {
		b, err := books.GetByForeignID(ctx, fid)
		if err != nil || b == nil {
			t.Fatalf("book %s not persisted: %v", fid, err)
		}
		if !b.Monitored {
			t.Errorf("book %s should be monitored", fid)
		}
		if b.Status != models.BookStatusWanted {
			t.Errorf("book %s status = %q, want wanted", fid, b.Status)
		}
	}

	// The author should exist exactly once and own both books.
	all, err := authors.List(ctx)
	if err != nil {
		t.Fatalf("list authors: %v", err)
	}
	if len(all) != 1 {
		t.Fatalf("want 1 author (deduped by foreign ID), got %d", len(all))
	}
	owned, err := books.ListByAuthor(ctx, all[0].ID)
	if err != nil {
		t.Fatalf("list by author: %v", err)
	}
	if len(owned) != 2 {
		t.Errorf("author should own 2 books, got %d", len(owned))
	}
}

func TestCommitGoodreadsImport_ReusesAuthorByAlternateIdentifier(t *testing.T) {
	database := newTestDB(t)
	authors := db.NewAuthorRepo(database)
	books := db.NewBookRepo(database)
	ctx := context.Background()
	existing := &models.Author{
		ForeignID:        "hc:author-one",
		Name:             "Author One",
		SortName:         "One, Author",
		MetadataProvider: "hardcover",
		Monitored:        true,
	}
	if err := authors.Create(ctx, existing); err != nil {
		t.Fatalf("seed author: %v", err)
	}
	if err := authors.UpsertAuthorIdentifier(ctx, existing.ID, "OL-A1"); err != nil {
		t.Fatalf("seed author identifier: %v", err)
	}
	resolved := []GoodreadsResolvedRow{{
		Row:     GoodreadsRow{Title: "Book One"},
		Outcome: outcomeResolved,
		book:    bookWithAuthor("OL-W1", "Book One", "OL-A1", "Author One"),
	}}

	result := CommitGoodreadsImport(ctx, resolved, authors, books)
	if result.Failed != 0 || result.Added != 1 {
		t.Fatalf("result = %+v, want imported book under existing alternate author", result)
	}
	all, err := authors.List(ctx)
	if err != nil {
		t.Fatalf("list authors: %v", err)
	}
	if len(all) != 1 {
		t.Fatalf("authors = %d, want existing author reused", len(all))
	}
	imported, err := books.GetByForeignID(ctx, "OL-W1")
	if err != nil || imported == nil {
		t.Fatalf("book = %+v err=%v, want persisted", imported, err)
	}
	if imported.AuthorID != existing.ID {
		t.Fatalf("book author_id = %d, want existing author %d", imported.AuthorID, existing.ID)
	}
}

// TestCommitGoodreadsImport_SkipsExistingAtCommit covers the race where a book
// resolved during preview is added (manually or by another import) before the
// commit lands.
func TestCommitGoodreadsImport_SkipsExistingAtCommit(t *testing.T) {
	database := newTestDB(t)
	authors := db.NewAuthorRepo(database)
	books := db.NewBookRepo(database)
	ctx := context.Background()

	author := &models.Author{ForeignID: "OL-A-RACE", Name: "Race Author", SortName: "Author, Race"}
	if err := authors.Create(ctx, author); err != nil {
		t.Fatalf("create author: %v", err)
	}
	if err := books.Create(ctx, &models.Book{ForeignID: "OL-W-RACE", AuthorID: author.ID, Title: "Raced", Genres: []string{}}); err != nil {
		t.Fatalf("create book: %v", err)
	}

	resolved := []GoodreadsResolvedRow{{
		Row:     GoodreadsRow{Title: "Raced"},
		Outcome: outcomeResolved,
		book:    bookWithAuthor("OL-W-RACE", "Raced", "OL-A-RACE", "Race Author"),
	}}
	result := CommitGoodreadsImport(ctx, resolved, authors, books)
	if result.Added != 0 || result.Skipped != 1 {
		t.Fatalf("want added=0 skipped=1, got added=%d skipped=%d", result.Added, result.Skipped)
	}
}

func TestGoodreadsImporter_PreviewAndCommit(t *testing.T) {
	database := newTestDB(t)
	authors := db.NewAuthorRepo(database)
	books := db.NewBookRepo(database)
	ctx := context.Background()

	imp := NewGoodreadsImporter(authors, books, nil).WithPacing(0)
	// Inject a deterministic resolution function — no live providers.
	imp.withResolveFn(func(_ context.Context, rows []GoodreadsRow, _ GoodreadsImportOptions, _ time.Duration) []GoodreadsResolvedRow {
		out := make([]GoodreadsResolvedRow, 0, len(rows))
		for _, r := range rows {
			out = append(out, GoodreadsResolvedRow{
				Row:       r,
				Outcome:   outcomeResolved,
				MatchedBy: "isbn13",
				book:      bookWithAuthor("OL-"+r.Title, r.Title, "OLA-"+r.Title, "Author "+r.Title),
			})
		}
		return out
	})

	rows := []GoodreadsRow{
		{RowNumber: 1, Title: "Alpha", ExclusiveShelf: GoodreadsShelfToRead},
		{RowNumber: 2, Title: "Beta", ExclusiveShelf: GoodreadsShelfToRead},
	}
	preview, err := imp.Preview(ctx, rows, GoodreadsImportOptions{})
	if err != nil {
		t.Fatalf("preview: %v", err)
	}
	if preview.Resolved != 2 || preview.Token == "" {
		t.Fatalf("preview = %+v", preview)
	}

	// Nothing should be written by the dry-run preview.
	all, _ := books.List(ctx)
	if len(all) != 0 {
		t.Fatalf("preview must not write books, found %d", len(all))
	}

	result, err := imp.Commit(ctx, preview.Token)
	if err != nil {
		t.Fatalf("commit: %v", err)
	}
	if result.Added != 2 {
		t.Fatalf("commit added = %d, want 2", result.Added)
	}

	// A second commit with the same token must fail — the token is consumed.
	if _, err := imp.Commit(ctx, preview.Token); err == nil {
		t.Error("expected an error committing a consumed token")
	}
}

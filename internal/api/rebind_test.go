package api

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"testing"

	"github.com/vavallee/bindery/internal/db"
	"github.com/vavallee/bindery/internal/metadata"
	"github.com/vavallee/bindery/internal/models"
)

// stubMetaLookup is a BookMetaLookup whose behaviour is fully configurable by
// the test. Returning errLookup simulates a provider error; returning nil book
// simulates a "not found" upstream response.
type stubMetaLookup struct {
	book *models.Book
	err  error
}

func (s *stubMetaLookup) GetBookFromProvider(_ context.Context, _, _ string) (*models.Book, error) {
	return s.book, s.err
}

// rebindFixture spins up an in-memory database with one author and one book,
// wires them into a BookHandler, and returns everything the test needs.
func rebindFixture(t *testing.T, lookup BookMetaLookup) (*BookHandler, *db.BookRepo, *db.AuthorRepo, *models.Author, *models.Book, context.Context) {
	t.Helper()
	database, err := db.OpenMemory()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { database.Close() })

	books := db.NewBookRepo(database)
	authors := db.NewAuthorRepo(database)
	series := db.NewSeriesRepo(database)
	history := db.NewHistoryRepo(database)

	ctx := context.Background()
	author := &models.Author{
		ForeignID: "OL1A", Name: "Alice Author", SortName: "Author, Alice",
		MetadataProvider: "openlibrary", Monitored: true,
	}
	if err := authors.Create(ctx, author); err != nil {
		t.Fatal(err)
	}

	book := &models.Book{
		ForeignID: "OL100W", AuthorID: author.ID, Title: "Old Title",
		SortTitle: "old title", Status: models.BookStatusWanted,
		Genres: []string{}, MetadataProvider: "openlibrary", Monitored: true,
	}
	if err := books.Create(ctx, book); err != nil {
		t.Fatal(err)
	}

	h := NewBookHandler(books, nil, history, nil).
		WithAuthors(authors).
		WithSeries(series).
		WithMetaLookup(lookup)

	return h, books, authors, author, book, ctx
}

// upstreamBook builds a minimal upstream Book for happy-path tests.
// authorName should match the local author's Name when no mismatch is wanted.
func upstreamBook(foreignID, title, authorName string) *models.Book {
	return &models.Book{
		ForeignID:        foreignID,
		Title:            title,
		SortTitle:        title,
		MetadataProvider: "openlibrary",
		Author:           &models.Author{Name: authorName},
		Genres:           []string{},
	}
}

// rebindReq serialises a Rebind request body.
func rebindReq(t *testing.T, provider, foreignID string, force bool) *bytes.Buffer {
	t.Helper()
	b, err := json.Marshal(map[string]any{
		"provider":   provider,
		"foreign_id": foreignID,
		"force":      force,
	})
	if err != nil {
		t.Fatal(err)
	}
	return bytes.NewBuffer(b)
}

// TestRebind_HappyPath verifies the nominal case: valid provider + foreign_id
// → 200, the book record is updated in the database.
func TestRebind_HappyPath(t *testing.T) {
	upstream := upstreamBook("OL999W", "New Title", "Alice Author")
	h, books, _, _, book, ctx := rebindFixture(t, &stubMetaLookup{book: upstream})

	body := rebindReq(t, "openlibrary", "OL999W", false)
	req := withURLParam(httptest.NewRequest(http.MethodPost, "/api/v1/books/"+strconv.FormatInt(book.ID, 10)+"/rebind", body), "id", strconv.FormatInt(book.ID, 10))
	rec := httptest.NewRecorder()
	h.Rebind(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	got, err := books.GetByID(ctx, book.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.ForeignID != "OL999W" {
		t.Errorf("foreign_id not updated: got %q", got.ForeignID)
	}
	if got.Title != "New Title" {
		t.Errorf("title not updated: got %q", got.Title)
	}
}

// TestRebind_BookNotFound verifies that requesting a non-existent book ID
// returns 404.
func TestRebind_BookNotFound(t *testing.T) {
	h, _, _, _, _, _ := rebindFixture(t, &stubMetaLookup{book: upstreamBook("OL999W", "T", "A")})

	body := rebindReq(t, "openlibrary", "OL999W", false)
	req := withURLParam(httptest.NewRequest(http.MethodPost, "/api/v1/books/99999/rebind", body), "id", "99999")
	rec := httptest.NewRecorder()
	h.Rebind(rec, req)

	if rec.Code != http.StatusNotFound {
		t.Errorf("expected 404, got %d", rec.Code)
	}
}

// TestRebind_InvalidProvider verifies that an unrecognised provider name
// returns 400.
func TestRebind_InvalidProvider(t *testing.T) {
	h, _, _, _, book, _ := rebindFixture(t, &stubMetaLookup{book: upstreamBook("X1", "T", "A")})

	body := rebindReq(t, "goodreads", "X1", false)
	req := withURLParam(httptest.NewRequest(http.MethodPost, "/api/v1/books/"+strconv.FormatInt(book.ID, 10)+"/rebind", body), "id", strconv.FormatInt(book.ID, 10))
	rec := httptest.NewRecorder()
	h.Rebind(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Errorf("expected 400, got %d", rec.Code)
	}
}

// TestRebind_MissingForeignID verifies that an empty foreign_id returns 400.
func TestRebind_MissingForeignID(t *testing.T) {
	h, _, _, _, book, _ := rebindFixture(t, &stubMetaLookup{book: upstreamBook("", "T", "A")})

	b, _ := json.Marshal(map[string]any{"provider": "openlibrary", "foreign_id": "", "force": false})
	req := withURLParam(httptest.NewRequest(http.MethodPost, "/api/v1/books/"+strconv.FormatInt(book.ID, 10)+"/rebind", bytes.NewBuffer(b)), "id", strconv.FormatInt(book.ID, 10))
	rec := httptest.NewRecorder()
	h.Rebind(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Errorf("expected 400, got %d", rec.Code)
	}
}

// TestRebind_AuthorMismatch_WithoutForce verifies that a mismatched upstream
// author name returns 409 with force_required:true when force is false.
func TestRebind_AuthorMismatch_WithoutForce(t *testing.T) {
	upstream := upstreamBook("OL999W", "New Title", "Completely Different Author")
	h, _, _, _, book, _ := rebindFixture(t, &stubMetaLookup{book: upstream})

	body := rebindReq(t, "openlibrary", "OL999W", false)
	req := withURLParam(httptest.NewRequest(http.MethodPost, "/api/v1/books/"+strconv.FormatInt(book.ID, 10)+"/rebind", body), "id", strconv.FormatInt(book.ID, 10))
	rec := httptest.NewRecorder()
	h.Rebind(rec, req)

	if rec.Code != http.StatusConflict {
		t.Fatalf("expected 409, got %d: %s", rec.Code, rec.Body.String())
	}
	var resp map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatal(err)
	}
	if resp["force_required"] != true {
		t.Errorf("expected force_required:true in response, got %v", resp)
	}
}

// TestRebind_AuthorMismatch_WithForce verifies that force:true overrides the
// author-mismatch guard and the rebind succeeds with HTTP 200.
func TestRebind_AuthorMismatch_WithForce(t *testing.T) {
	upstream := upstreamBook("OL999W", "New Title", "Completely Different Author")
	h, books, _, _, book, ctx := rebindFixture(t, &stubMetaLookup{book: upstream})

	body := rebindReq(t, "openlibrary", "OL999W", true)
	req := withURLParam(httptest.NewRequest(http.MethodPost, "/api/v1/books/"+strconv.FormatInt(book.ID, 10)+"/rebind", body), "id", strconv.FormatInt(book.ID, 10))
	rec := httptest.NewRecorder()
	h.Rebind(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200 with force:true, got %d: %s", rec.Code, rec.Body.String())
	}
	got, _ := books.GetByID(ctx, book.ID)
	if got.ForeignID != "OL999W" {
		t.Errorf("expected foreign_id updated, got %q", got.ForeignID)
	}
}

// TestRebind_ForeignIDCollision verifies that trying to rebind to a foreign_id
// already owned by a different book returns 409.
func TestRebind_ForeignIDCollision(t *testing.T) {
	upstream := upstreamBook("OL200W", "Other Book", "Alice Author")
	h, books, _, author, book, ctx := rebindFixture(t, &stubMetaLookup{book: upstream})

	// Create a second book that already owns OL200W.
	other := &models.Book{
		ForeignID: "OL200W", AuthorID: author.ID, Title: "Other", SortTitle: "other",
		Status: models.BookStatusWanted, Genres: []string{}, MetadataProvider: "openlibrary", Monitored: true,
	}
	if err := books.Create(ctx, other); err != nil {
		t.Fatal(err)
	}

	body := rebindReq(t, "openlibrary", "OL200W", false)
	req := withURLParam(httptest.NewRequest(http.MethodPost, "/api/v1/books/"+strconv.FormatInt(book.ID, 10)+"/rebind", body), "id", strconv.FormatInt(book.ID, 10))
	rec := httptest.NewRecorder()
	h.Rebind(rec, req)

	if rec.Code != http.StatusConflict {
		t.Errorf("expected 409 on foreign_id collision, got %d: %s", rec.Code, rec.Body.String())
	}
}

// TestRebind_AggregatorNil verifies that when no BookMetaLookup is wired the
// handler returns HTTP 424 (Failed Dependency) rather than panicking.
func TestRebind_AggregatorNil(t *testing.T) {
	database, err := db.OpenMemory()
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()

	books := db.NewBookRepo(database)
	authors := db.NewAuthorRepo(database)
	history := db.NewHistoryRepo(database)
	ctx := context.Background()

	author := &models.Author{
		ForeignID: "OL1A", Name: "Alice", SortName: "Alice",
		MetadataProvider: "openlibrary", Monitored: true,
	}
	if err := authors.Create(ctx, author); err != nil {
		t.Fatal(err)
	}
	book := &models.Book{
		ForeignID: "OL1W", AuthorID: author.ID, Title: "T", SortTitle: "t",
		Status: models.BookStatusWanted, Genres: []string{},
		MetadataProvider: "openlibrary", Monitored: true,
	}
	if err := books.Create(ctx, book); err != nil {
		t.Fatal(err)
	}

	// Deliberately do NOT call WithMetaLookup — lookup stays nil.
	h := NewBookHandler(books, nil, history, nil)

	body := rebindReq(t, "openlibrary", "OL999W", false)
	req := withURLParam(httptest.NewRequest(http.MethodPost, "/api/v1/books/"+strconv.FormatInt(book.ID, 10)+"/rebind", body), "id", strconv.FormatInt(book.ID, 10))
	rec := httptest.NewRecorder()
	h.Rebind(rec, req)

	if rec.Code != http.StatusFailedDependency {
		t.Errorf("expected 424 when aggregator is nil, got %d: %s", rec.Code, rec.Body.String())
	}
}

// TestRebind_SeriesRelinked verifies that after a successful rebind the book
// is re-linked to the series described in the upstream record.
func TestRebind_SeriesRelinked(t *testing.T) {
	upstream := upstreamBook("OL999W", "New Title", "Alice Author")
	upstream.SeriesRefs = []models.SeriesRef{
		{ForeignID: "S1", Title: "The Test Series", Position: "1", Primary: true},
	}
	h, _, _, _, book, ctx := rebindFixture(t, &stubMetaLookup{book: upstream})

	body := rebindReq(t, "openlibrary", "OL999W", false)
	req := withURLParam(httptest.NewRequest(http.MethodPost, "/api/v1/books/"+strconv.FormatInt(book.ID, 10)+"/rebind", body), "id", strconv.FormatInt(book.ID, 10))
	rec := httptest.NewRecorder()
	h.Rebind(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}

	// Verify the series membership was created.
	seriesTitle, position, err := h.series.GetPrimarySeriesForBook(ctx, book.ID)
	if err != nil {
		t.Fatal(err)
	}
	if seriesTitle != "The Test Series" {
		t.Errorf("expected series 'The Test Series', got %q", seriesTitle)
	}
	if position != "1" {
		t.Errorf("expected position '1', got %q", position)
	}
}

// TestRebind_HistoryEventWritten verifies that a HistoryEventBookRebound entry
// is recorded after a successful rebind.
func TestRebind_HistoryEventWritten(t *testing.T) {
	upstream := upstreamBook("OL999W", "New Title", "Alice Author")
	h, _, _, _, book, ctx := rebindFixture(t, &stubMetaLookup{book: upstream})

	body := rebindReq(t, "openlibrary", "OL999W", false)
	req := withURLParam(httptest.NewRequest(http.MethodPost, "/api/v1/books/"+strconv.FormatInt(book.ID, 10)+"/rebind", body), "id", strconv.FormatInt(book.ID, 10))
	rec := httptest.NewRecorder()
	h.Rebind(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}

	events, err := h.history.ListByBook(ctx, book.ID)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, e := range events {
		if e.EventType == models.HistoryEventBookRebound {
			found = true
			break
		}
	}
	if !found {
		t.Error("expected HistoryEventBookRebound entry, not found")
	}
}

// Compile-time check: *metadata.Aggregator satisfies BookMetaLookup.
var _ BookMetaLookup = (*metadata.Aggregator)(nil)

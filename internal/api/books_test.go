package api

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/vavallee/bindery/internal/db"
	"github.com/vavallee/bindery/internal/models"
)

// mockBookSearcher collects SearchAndGrabBook calls via a buffered channel so
// goroutine-launched searches can be awaited in tests.
type mockBookSearcher struct {
	ch chan models.Book
}

func newMockBookSearcher() *mockBookSearcher {
	return &mockBookSearcher{ch: make(chan models.Book, 8)}
}

func (m *mockBookSearcher) SearchAndGrabBook(_ context.Context, book models.Book) {
	select {
	case m.ch <- book:
	default:
	}
}

// waitForCall blocks until a search call arrives or the timeout elapses.
func (m *mockBookSearcher) waitForCall(t *testing.T, timeout time.Duration) models.Book {
	t.Helper()
	select {
	case b := <-m.ch:
		return b
	case <-time.After(timeout):
		t.Fatal("timeout: SearchAndGrabBook was not called within deadline")
		return models.Book{}
	}
}

// assertNoCall fails the test if a search call arrives within the window.
func (m *mockBookSearcher) assertNoCall(t *testing.T, window time.Duration) {
	t.Helper()
	select {
	case b := <-m.ch:
		t.Errorf("unexpected SearchAndGrabBook call for book %q", b.Title)
	case <-time.After(window):
		// good — nothing fired
	}
}

// bookFixture spins up in-memory storage with an author + N books and
// returns a wired BookHandler. The meta aggregator is nil — EnrichAudiobook
// is not exercised here because it hits the external audnex API.
func bookFixture(t *testing.T) (*BookHandler, *db.BookRepo, *db.AuthorRepo, *models.Author, context.Context) {
	t.Helper()
	database, err := db.OpenMemory()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { database.Close() })

	books := db.NewBookRepo(database)
	authors := db.NewAuthorRepo(database)
	history := db.NewHistoryRepo(database)

	ctx := context.Background()
	author := &models.Author{
		ForeignID: "OL1A", Name: "Test Author", SortName: "Author, Test",
		MetadataProvider: "openlibrary", Monitored: true,
	}
	if err := authors.Create(ctx, author); err != nil {
		t.Fatal(err)
	}
	return NewBookHandler(books, nil, history, nil), books, authors, author, ctx
}

func withURLParam(req *http.Request, key, val string) *http.Request {
	rctx := chi.NewRouteContext()
	rctx.URLParams.Add(key, val)
	return req.WithContext(context.WithValue(req.Context(), chi.RouteCtxKey, rctx))
}

// TestBookList_Empty returns a wrapped envelope with items=[] (not null) so
// the frontend can render without a null-check. A nil body here would break
// the books grid on first load. As of Wave 2 / Bundle E the shape is
// {items, total, limit, offset}, not a bare array.
func TestBookList_Empty(t *testing.T) {
	h, _, _, _, _ := bookFixture(t)
	rec := httptest.NewRecorder()
	h.List(rec, httptest.NewRequest(http.MethodGet, "/api/v1/book", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Code)
	}
	var got bookListResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode envelope: %v (body=%s)", err, rec.Body.String())
	}
	if got.Items == nil {
		t.Errorf("expected items=[] (not null) so the frontend can render, got %s", rec.Body.String())
	}
	if got.Total != 0 || got.Offset != 0 || got.Limit != bookListDefaultLimit {
		t.Errorf("expected zero totals + default limit, got %+v", got)
	}
}

// TestBookList_FiltersByAuthor confirms ?authorId= scopes the result. A bug
// here would mean the author detail page shows the entire library.
func TestBookList_FiltersByAuthor(t *testing.T) {
	h, books, authors, a1, ctx := bookFixture(t)
	a2 := &models.Author{ForeignID: "OL2A", Name: "Other", SortName: "Other", MetadataProvider: "openlibrary", Monitored: true}
	if err := authors.Create(ctx, a2); err != nil {
		t.Fatal(err)
	}
	for _, b := range []*models.Book{
		{ForeignID: "B1", AuthorID: a1.ID, Title: "A1-B1", SortTitle: "a1-b1", Status: "wanted", Genres: []string{}, MetadataProvider: "openlibrary", Monitored: true},
		{ForeignID: "B2", AuthorID: a1.ID, Title: "A1-B2", SortTitle: "a1-b2", Status: "wanted", Genres: []string{}, MetadataProvider: "openlibrary", Monitored: true},
		{ForeignID: "B3", AuthorID: a2.ID, Title: "A2-B1", SortTitle: "a2-b1", Status: "wanted", Genres: []string{}, MetadataProvider: "openlibrary", Monitored: true},
	} {
		if err := books.Create(ctx, b); err != nil {
			t.Fatal(err)
		}
	}

	rec := httptest.NewRecorder()
	h.List(rec, httptest.NewRequest(http.MethodGet, "/api/v1/book?authorId="+strconv.FormatInt(a1.ID, 10), nil))
	var got bookListResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if got.Total != 2 || len(got.Items) != 2 {
		t.Errorf("expected 2 books for author %d, got total=%d items=%d", a1.ID, got.Total, len(got.Items))
	}
}

// seedBooksForPagination creates n books under the given author with
// deterministic sort_titles ("Book 001", "Book 002", ...) so the order is
// stable and the test can assert which slice of titles came back.
func seedBooksForPagination(t *testing.T, books *db.BookRepo, ctx context.Context, authorID int64, n int) []string {
	t.Helper()
	titles := make([]string, 0, n)
	for i := 1; i <= n; i++ {
		title := fmt.Sprintf("Book %03d", i)
		b := &models.Book{
			ForeignID: fmt.Sprintf("PAGE-%03d", i), AuthorID: authorID, Title: title, SortTitle: title,
			Status: "wanted", Genres: []string{}, MetadataProvider: "openlibrary", Monitored: true,
		}
		if err := books.Create(ctx, b); err != nil {
			t.Fatal(err)
		}
		titles = append(titles, title)
	}
	return titles
}

// TestBookList_Paginates exercises ?limit=N&offset=K with N seeded rows.
// The first page returns three books and the trailing-edge page returns
// the leftover one. Total is the unsliced count regardless of page.
func TestBookList_Paginates(t *testing.T) {
	h, books, _, author, ctx := bookFixture(t)
	titles := seedBooksForPagination(t, books, ctx, author.ID, 10)

	// limit=3 offset=0 — first three by sort_title.
	rec := httptest.NewRecorder()
	h.List(rec, httptest.NewRequest(http.MethodGet, "/api/v1/book?limit=3&offset=0", nil))
	var first bookListResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &first); err != nil {
		t.Fatalf("decode first: %v", err)
	}
	if first.Total != 10 || first.Limit != 3 || first.Offset != 0 || len(first.Items) != 3 {
		t.Errorf("first page envelope = %+v, want total=10 limit=3 offset=0 len=3", first)
	}
	for i, b := range first.Items {
		if b.Title != titles[i] {
			t.Errorf("first page item %d = %q, want %q", i, b.Title, titles[i])
		}
	}

	// offset=9 lands on the last book alone.
	rec = httptest.NewRecorder()
	h.List(rec, httptest.NewRequest(http.MethodGet, "/api/v1/book?limit=3&offset=9", nil))
	var tail bookListResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &tail); err != nil {
		t.Fatalf("decode tail: %v", err)
	}
	if tail.Total != 10 || len(tail.Items) != 1 || tail.Items[0].Title != titles[9] {
		t.Errorf("tail page = %+v, want one item %q", tail, titles[9])
	}
}

// TestBookList_DefaultsAndCaps checks the default limit is applied when no
// query param is set and that an oversized request is clamped at the
// configured max so a client cannot pull the entire 50k library at once.
func TestBookList_DefaultsAndCaps(t *testing.T) {
	h, books, _, author, ctx := bookFixture(t)
	seedBooksForPagination(t, books, ctx, author.ID, 3)

	// No params — default limit applies.
	rec := httptest.NewRecorder()
	h.List(rec, httptest.NewRequest(http.MethodGet, "/api/v1/book", nil))
	var defaults bookListResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &defaults); err != nil {
		t.Fatalf("decode defaults: %v", err)
	}
	if defaults.Limit != bookListDefaultLimit {
		t.Errorf("expected default limit %d, got %d", bookListDefaultLimit, defaults.Limit)
	}

	// limit=10000 must be clamped to bookListMaxLimit.
	rec = httptest.NewRecorder()
	h.List(rec, httptest.NewRequest(http.MethodGet, "/api/v1/book?limit=10000", nil))
	var clamped bookListResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &clamped); err != nil {
		t.Fatalf("decode clamped: %v", err)
	}
	if clamped.Limit != bookListMaxLimit {
		t.Errorf("expected clamped limit %d, got %d", bookListMaxLimit, clamped.Limit)
	}
}

// TestBookList_OrderStable confirms the same query returns the same order on
// repeat calls. Stability is what backs the frontend's "load next page"
// pattern; if rows could shuffle between requests the user would see
// duplicates or gaps as they scrolled.
func TestBookList_OrderStable(t *testing.T) {
	h, books, _, author, ctx := bookFixture(t)
	seedBooksForPagination(t, books, ctx, author.ID, 5)

	collect := func() []string {
		rec := httptest.NewRecorder()
		h.List(rec, httptest.NewRequest(http.MethodGet, "/api/v1/book?limit=5&offset=0", nil))
		var page bookListResponse
		if err := json.Unmarshal(rec.Body.Bytes(), &page); err != nil {
			t.Fatalf("decode: %v", err)
		}
		out := make([]string, len(page.Items))
		for i, b := range page.Items {
			out[i] = b.Title
		}
		return out
	}
	first := collect()
	second := collect()
	if len(first) != 5 || len(second) != 5 {
		t.Fatalf("expected 5+5 items, got %d/%d", len(first), len(second))
	}
	for i := range first {
		if first[i] != second[i] {
			t.Errorf("order changed between calls at %d: %q vs %q", i, first[i], second[i])
		}
	}
}

// TestBookGet_NotFound verifies the 404 path rather than a 500 swallowing
// the missing row.
func TestBookGet_NotFound(t *testing.T) {
	h, _, _, _, _ := bookFixture(t)
	req := withURLParam(httptest.NewRequest(http.MethodGet, "/api/v1/book/99999", nil), "id", "99999")
	rec := httptest.NewRecorder()
	h.Get(rec, req)
	if rec.Code != http.StatusNotFound {
		t.Errorf("expected 404, got %d", rec.Code)
	}
}

// TestBookGet_BadID catches the parse-error branch — non-numeric ids.
func TestBookGet_BadID(t *testing.T) {
	h, _, _, _, _ := bookFixture(t)
	req := withURLParam(httptest.NewRequest(http.MethodGet, "/api/v1/book/abc", nil), "id", "abc")
	rec := httptest.NewRecorder()
	h.Get(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Errorf("expected 400, got %d", rec.Code)
	}
}

// TestBookUpdate_PatchesOnlyProvidedFields is the invariant under test:
// omitted keys must leave the existing value alone. A bug that zeroed
// unspecified fields would wipe user metadata on every PUT.
func TestBookUpdate_PatchesOnlyProvidedFields(t *testing.T) {
	h, books, _, author, ctx := bookFixture(t)
	book := &models.Book{
		ForeignID: "B1", AuthorID: author.ID, Title: "T", SortTitle: "t",
		Status: "wanted", MediaType: models.MediaTypeEbook, ASIN: "OLD",
		Narrator: "Nobody", Genres: []string{}, MetadataProvider: "openlibrary",
		Monitored: true,
	}
	if err := books.Create(ctx, book); err != nil {
		t.Fatal(err)
	}

	// Patch only Narrator — the rest must survive.
	body := bytes.NewBufferString(`{"narrator":"Real Person"}`)
	req := withURLParam(httptest.NewRequest(http.MethodPut, "/api/v1/book/"+strconv.FormatInt(book.ID, 10), body), "id", strconv.FormatInt(book.ID, 10))
	rec := httptest.NewRecorder()
	h.Update(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	got, err := books.GetByID(ctx, book.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Narrator != "Real Person" {
		t.Errorf("narrator not updated, got %q", got.Narrator)
	}
	if got.ASIN != "OLD" {
		t.Errorf("ASIN should survive partial patch, got %q", got.ASIN)
	}
	if got.MediaType != models.MediaTypeEbook {
		t.Errorf("mediaType should survive partial patch, got %q", got.MediaType)
	}
}

// TestBookUpdate_RejectsInvalidMediaType guards the enum. Free-form media
// types would break the importer routing (ebook vs audiobook folder logic).
func TestBookUpdate_RejectsInvalidMediaType(t *testing.T) {
	h, books, _, author, ctx := bookFixture(t)
	book := &models.Book{
		ForeignID: "B1", AuthorID: author.ID, Title: "T", SortTitle: "t",
		Status: "wanted", MediaType: models.MediaTypeEbook,
		Genres: []string{}, MetadataProvider: "openlibrary", Monitored: true,
	}
	if err := books.Create(ctx, book); err != nil {
		t.Fatal(err)
	}
	body := bytes.NewBufferString(`{"mediaType":"videogame"}`)
	req := withURLParam(httptest.NewRequest(http.MethodPut, "/api/v1/book/"+strconv.FormatInt(book.ID, 10), body), "id", strconv.FormatInt(book.ID, 10))
	rec := httptest.NewRecorder()
	h.Update(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Errorf("expected 400 for bogus mediaType, got %d", rec.Code)
	}
}

// TestBookUpdate_RejectsFilePathInBody documents the deliberate omission:
// file_path is set by the importer only. Even if clients send it, the decoder
// must ignore it — otherwise a malicious client could redirect the delete
// sweep at an arbitrary path on disk.
func TestBookUpdate_RejectsFilePathInBody(t *testing.T) {
	h, books, _, author, ctx := bookFixture(t)
	book := &models.Book{
		ForeignID: "B1", AuthorID: author.ID, Title: "T", SortTitle: "t",
		Status: "wanted", MediaType: models.MediaTypeEbook,
		Genres: []string{}, MetadataProvider: "openlibrary", Monitored: true,
	}
	if err := books.Create(ctx, book); err != nil {
		t.Fatal(err)
	}
	if err := books.SetFilePath(ctx, book.ID, "/safe/path.epub"); err != nil {
		t.Fatal(err)
	}

	body := bytes.NewBufferString(`{"filePath":"/etc/passwd","file_path":"/etc/passwd"}`)
	req := withURLParam(httptest.NewRequest(http.MethodPut, "/api/v1/book/"+strconv.FormatInt(book.ID, 10), body), "id", strconv.FormatInt(book.ID, 10))
	rec := httptest.NewRecorder()
	h.Update(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Code)
	}
	got, _ := books.GetByID(ctx, book.ID)
	if got.FilePath != "/safe/path.epub" {
		t.Errorf("filePath should be ignored from body, got %q", got.FilePath)
	}
}

// TestBookDelete_MultiFile_RemovesAllOnDisk verifies the #343 fix:
// ?deleteFiles=true enumerates via book_files and removes every on-disk
// file, not just the first registered path.
func TestBookDelete_MultiFile_RemovesAllOnDisk(t *testing.T) {
	h, books, _, author, ctx := bookFixture(t)
	tmp := t.TempDir()

	epub := filepath.Join(tmp, "book.epub")
	mobi := filepath.Join(tmp, "book.mobi")
	pdf := filepath.Join(tmp, "book.pdf")
	for _, p := range []string{epub, mobi, pdf} {
		if err := os.WriteFile(p, []byte("x"), 0o600); err != nil {
			t.Fatal(err)
		}
	}

	book := &models.Book{
		ForeignID: "B-MF1", AuthorID: author.ID, Title: "Multi Format", SortTitle: "mf",
		Status: "wanted", Genres: []string{}, MetadataProvider: "openlibrary", Monitored: true,
	}
	if err := books.Create(ctx, book); err != nil {
		t.Fatal(err)
	}
	for _, p := range []string{epub, mobi, pdf} {
		if err := books.AddBookFile(ctx, book.ID, models.MediaTypeEbook, p); err != nil {
			t.Fatalf("AddBookFile(%s): %v", p, err)
		}
	}

	req := withURLParam(
		httptest.NewRequest(http.MethodDelete, "/api/v1/book/"+strconv.FormatInt(book.ID, 10)+"?deleteFiles=true", nil),
		"id", strconv.FormatInt(book.ID, 10))
	rec := httptest.NewRecorder()
	h.Delete(rec, req)
	if rec.Code != http.StatusNoContent {
		t.Fatalf("expected 204, got %d", rec.Code)
	}

	for _, p := range []string{epub, mobi, pdf} {
		if _, err := os.Stat(p); !os.IsNotExist(err) {
			t.Errorf("file should be removed: %s (stat err=%v)", p, err)
		}
	}
}

// TestBookDelete_WithDeleteFiles exercises the ?deleteFiles=true branch end
// to end: on-disk path must be swept before the row is removed.
func TestBookDelete_WithDeleteFiles(t *testing.T) {
	h, books, _, author, ctx := bookFixture(t)

	path := filepath.Join(t.TempDir(), "Book.epub")
	if err := os.WriteFile(path, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	book := &models.Book{
		ForeignID: "B1", AuthorID: author.ID, Title: "T", SortTitle: "t",
		Status: "imported", Genres: []string{},
		MetadataProvider: "openlibrary", Monitored: true,
	}
	if err := books.Create(ctx, book); err != nil {
		t.Fatal(err)
	}
	if err := books.SetFilePath(ctx, book.ID, path); err != nil {
		t.Fatal(err)
	}

	req := withURLParam(httptest.NewRequest(http.MethodDelete, "/api/v1/book/"+strconv.FormatInt(book.ID, 10)+"?deleteFiles=true", nil), "id", strconv.FormatInt(book.ID, 10))
	rec := httptest.NewRecorder()
	h.Delete(rec, req)
	if rec.Code != http.StatusNoContent {
		t.Fatalf("expected 204, got %d", rec.Code)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Errorf("file should be removed, stat err=%v", err)
	}
}

// TestBookDelete_WithoutDeleteFiles preserves the file even though the row
// is gone — matches author-delete behaviour for non-opt-in sweeps.
func TestBookDelete_WithoutDeleteFiles(t *testing.T) {
	h, books, _, author, ctx := bookFixture(t)

	path := filepath.Join(t.TempDir(), "Book.epub")
	if err := os.WriteFile(path, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	book := &models.Book{
		ForeignID: "B1", AuthorID: author.ID, Title: "T", SortTitle: "t",
		Status: "imported", Genres: []string{},
		MetadataProvider: "openlibrary", Monitored: true,
	}
	if err := books.Create(ctx, book); err != nil {
		t.Fatal(err)
	}
	if err := books.SetFilePath(ctx, book.ID, path); err != nil {
		t.Fatal(err)
	}

	req := withURLParam(httptest.NewRequest(http.MethodDelete, "/api/v1/book/"+strconv.FormatInt(book.ID, 10), nil), "id", strconv.FormatInt(book.ID, 10))
	rec := httptest.NewRecorder()
	h.Delete(rec, req)
	if rec.Code != http.StatusNoContent {
		t.Fatalf("expected 204, got %d", rec.Code)
	}
	if _, err := os.Stat(path); err != nil {
		t.Errorf("file should survive delete without ?deleteFiles=true, stat err=%v", err)
	}
}

// TestBookDeleteFile_FlipsToWanted is the scrub-and-keep flow: the row stays,
// status flips to `wanted`, and the on-disk file is gone.
func TestBookDeleteFile_FlipsToWanted(t *testing.T) {
	h, books, _, author, ctx := bookFixture(t)

	path := filepath.Join(t.TempDir(), "Book.epub")
	if err := os.WriteFile(path, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	book := &models.Book{
		ForeignID: "B1", AuthorID: author.ID, Title: "T", SortTitle: "t",
		Status: "imported", Genres: []string{},
		MetadataProvider: "openlibrary", Monitored: true,
	}
	if err := books.Create(ctx, book); err != nil {
		t.Fatal(err)
	}
	if err := books.SetFilePath(ctx, book.ID, path); err != nil {
		t.Fatal(err)
	}

	req := withURLParam(httptest.NewRequest(http.MethodDelete, "/api/v1/book/"+strconv.FormatInt(book.ID, 10)+"/file", nil), "id", strconv.FormatInt(book.ID, 10))
	rec := httptest.NewRecorder()
	h.DeleteFile(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Errorf("file should be removed, stat err=%v", err)
	}
	got, _ := books.GetByID(ctx, book.ID)
	if got.FilePath != "" {
		t.Errorf("filePath should be cleared, got %q", got.FilePath)
	}
	if got.Status != models.BookStatusWanted {
		t.Errorf("status should flip to wanted, got %q", got.Status)
	}
}

// TestBookDeleteFile_NoFilePath is the 400 guard — there's nothing to delete
// if the row has no path.
func TestBookDeleteFile_NoFilePath(t *testing.T) {
	h, books, _, author, ctx := bookFixture(t)
	book := &models.Book{
		ForeignID: "B1", AuthorID: author.ID, Title: "T", SortTitle: "t",
		Status: "wanted", Genres: []string{},
		MetadataProvider: "openlibrary", Monitored: true,
	}
	if err := books.Create(ctx, book); err != nil {
		t.Fatal(err)
	}
	req := withURLParam(httptest.NewRequest(http.MethodDelete, "/api/v1/book/"+strconv.FormatInt(book.ID, 10)+"/file", nil), "id", strconv.FormatInt(book.ID, 10))
	rec := httptest.NewRecorder()
	h.DeleteFile(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Errorf("expected 400, got %d", rec.Code)
	}
}

// TestBookUpdate_RejectsInvalidStatus is the #715 finding 3 guard: an unknown
// status value must be rejected with 400 rather than written verbatim.
func TestBookUpdate_RejectsInvalidStatus(t *testing.T) {
	h, books, _, author, ctx := bookFixture(t)
	book := &models.Book{
		ForeignID: "B-BADSTATUS", AuthorID: author.ID, Title: "T", SortTitle: "t",
		Status: models.BookStatusWanted, Genres: []string{},
		MetadataProvider: "openlibrary", Monitored: true,
	}
	if err := books.Create(ctx, book); err != nil {
		t.Fatal(err)
	}

	body := bytes.NewBufferString(`{"status":"bananas"}`)
	req := withURLParam(httptest.NewRequest(http.MethodPut, "/api/v1/book/"+strconv.FormatInt(book.ID, 10), body), "id", strconv.FormatInt(book.ID, 10))
	rec := httptest.NewRecorder()
	h.Update(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 for invalid status, got %d: %s", rec.Code, rec.Body.String())
	}
	// The persisted status must be untouched.
	got, _ := books.GetByID(ctx, book.ID)
	if got.Status != models.BookStatusWanted {
		t.Errorf("status should be unchanged after rejected update, got %q", got.Status)
	}
}

// TestBookUpdate_AcceptsValidStatus confirms a known status constant still
// passes the new validation.
func TestBookUpdate_AcceptsValidStatus(t *testing.T) {
	h, books, _, author, ctx := bookFixture(t)
	book := &models.Book{
		ForeignID: "B-OKSTATUS", AuthorID: author.ID, Title: "T", SortTitle: "t",
		Status: models.BookStatusImported, Genres: []string{},
		MetadataProvider: "openlibrary", Monitored: true,
	}
	if err := books.Create(ctx, book); err != nil {
		t.Fatal(err)
	}

	body := bytes.NewBufferString(`{"status":"downloaded"}`)
	req := withURLParam(httptest.NewRequest(http.MethodPut, "/api/v1/book/"+strconv.FormatInt(book.ID, 10), body), "id", strconv.FormatInt(book.ID, 10))
	rec := httptest.NewRecorder()
	h.Update(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200 for valid status, got %d: %s", rec.Code, rec.Body.String())
	}
	got, _ := books.GetByID(ctx, book.ID)
	if got.Status != models.BookStatusDownloaded {
		t.Errorf("status should be 'downloaded', got %q", got.Status)
	}
}

// TestBookDeleteFile_FormatScopedKeepsSibling is the #715 finding 2 data-loss
// guard: deleting ?format=ebook must leave the same-stem audiobook on disk.
func TestBookDeleteFile_FormatScopedKeepsSibling(t *testing.T) {
	h, books, _, author, ctx := bookFixture(t)
	tmp := t.TempDir()

	epub := filepath.Join(tmp, "Book.epub")
	m4b := filepath.Join(tmp, "Book.m4b")
	for _, p := range []string{epub, m4b} {
		if err := os.WriteFile(p, []byte("x"), 0o600); err != nil {
			t.Fatal(err)
		}
	}

	book := &models.Book{
		ForeignID: "B-DUALFMT", AuthorID: author.ID, Title: "Dual Format", SortTitle: "df",
		Status: models.BookStatusImported, Genres: []string{}, MediaType: models.MediaTypeBoth,
		MetadataProvider: "openlibrary", Monitored: true,
	}
	if err := books.Create(ctx, book); err != nil {
		t.Fatal(err)
	}
	if err := books.AddBookFile(ctx, book.ID, models.MediaTypeEbook, epub); err != nil {
		t.Fatalf("AddBookFile(epub): %v", err)
	}
	if err := books.AddBookFile(ctx, book.ID, models.MediaTypeAudiobook, m4b); err != nil {
		t.Fatalf("AddBookFile(m4b): %v", err)
	}

	req := withURLParam(
		httptest.NewRequest(http.MethodDelete, "/api/v1/book/"+strconv.FormatInt(book.ID, 10)+"/file?format=ebook", nil),
		"id", strconv.FormatInt(book.ID, 10))
	rec := httptest.NewRecorder()
	h.DeleteFile(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}

	if _, err := os.Stat(epub); !os.IsNotExist(err) {
		t.Errorf("ebook should be deleted, stat err=%v", err)
	}
	if _, err := os.Stat(m4b); err != nil {
		t.Errorf("audiobook sibling must survive a format-scoped ebook delete, stat err=%v", err)
	}
}

// TestBookDeleteFile_FormatScopedAudiobookKeepsEbook is the mirror case:
// ?format=audiobook must not destroy the same-stem ebook.
func TestBookDeleteFile_FormatScopedAudiobookKeepsEbook(t *testing.T) {
	h, books, _, author, ctx := bookFixture(t)
	tmp := t.TempDir()

	epub := filepath.Join(tmp, "Book.epub")
	m4b := filepath.Join(tmp, "Book.m4b")
	for _, p := range []string{epub, m4b} {
		if err := os.WriteFile(p, []byte("x"), 0o600); err != nil {
			t.Fatal(err)
		}
	}

	book := &models.Book{
		ForeignID: "B-DUALFMT2", AuthorID: author.ID, Title: "Dual Format 2", SortTitle: "df2",
		Status: models.BookStatusImported, Genres: []string{}, MediaType: models.MediaTypeBoth,
		MetadataProvider: "openlibrary", Monitored: true,
	}
	if err := books.Create(ctx, book); err != nil {
		t.Fatal(err)
	}
	if err := books.AddBookFile(ctx, book.ID, models.MediaTypeEbook, epub); err != nil {
		t.Fatalf("AddBookFile(epub): %v", err)
	}
	if err := books.AddBookFile(ctx, book.ID, models.MediaTypeAudiobook, m4b); err != nil {
		t.Fatalf("AddBookFile(m4b): %v", err)
	}

	req := withURLParam(
		httptest.NewRequest(http.MethodDelete, "/api/v1/book/"+strconv.FormatInt(book.ID, 10)+"/file?format=audiobook", nil),
		"id", strconv.FormatInt(book.ID, 10))
	rec := httptest.NewRecorder()
	h.DeleteFile(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}

	if _, err := os.Stat(m4b); !os.IsNotExist(err) {
		t.Errorf("audiobook should be deleted, stat err=%v", err)
	}
	if _, err := os.Stat(epub); err != nil {
		t.Errorf("ebook sibling must survive a format-scoped audiobook delete, stat err=%v", err)
	}
}

// TestListWanted returns only wanted-status books — the Wanted page query.
func TestListWanted(t *testing.T) {
	h, books, _, author, ctx := bookFixture(t)
	for _, b := range []*models.Book{
		{ForeignID: "B1", AuthorID: author.ID, Title: "W1", SortTitle: "w1", Status: models.BookStatusWanted, Genres: []string{}, MetadataProvider: "openlibrary", Monitored: true},
		{ForeignID: "B2", AuthorID: author.ID, Title: "W2", SortTitle: "w2", Status: models.BookStatusWanted, Genres: []string{}, MetadataProvider: "openlibrary", Monitored: true},
		{ForeignID: "B3", AuthorID: author.ID, Title: "I1", SortTitle: "i1", Status: "imported", Genres: []string{}, MetadataProvider: "openlibrary", Monitored: true},
	} {
		if err := books.Create(ctx, b); err != nil {
			t.Fatal(err)
		}
	}
	rec := httptest.NewRecorder()
	h.ListWanted(rec, httptest.NewRequest(http.MethodGet, "/api/v1/wanted", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Code)
	}
	var got []models.Book
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 {
		t.Errorf("expected 2 wanted, got %d", len(got))
	}
}

// TestEnrichAudiobook_RejectsEbook and _RejectsMissingASIN exercise the
// cheap guards before the audnex call — we never reach a real HTTP hop.
func TestEnrichAudiobook_RejectsEbook(t *testing.T) {
	h, books, _, author, ctx := bookFixture(t)
	book := &models.Book{
		ForeignID: "B1", AuthorID: author.ID, Title: "T", SortTitle: "t",
		Status: "wanted", MediaType: models.MediaTypeEbook,
		Genres: []string{}, MetadataProvider: "openlibrary", Monitored: true,
	}
	if err := books.Create(ctx, book); err != nil {
		t.Fatal(err)
	}
	req := withURLParam(httptest.NewRequest(http.MethodPost, "/api/v1/book/"+strconv.FormatInt(book.ID, 10)+"/enrich", nil), "id", strconv.FormatInt(book.ID, 10))
	rec := httptest.NewRecorder()
	h.EnrichAudiobook(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Errorf("expected 400 for ebook enrich, got %d", rec.Code)
	}
}

func TestEnrichAudiobook_RejectsMissingASIN(t *testing.T) {
	h, books, _, author, ctx := bookFixture(t)
	book := &models.Book{
		ForeignID: "B1", AuthorID: author.ID, Title: "T", SortTitle: "t",
		Status: "wanted", MediaType: models.MediaTypeAudiobook,
		Genres: []string{}, MetadataProvider: "openlibrary", Monitored: true,
	}
	if err := books.Create(ctx, book); err != nil {
		t.Fatal(err)
	}
	req := withURLParam(httptest.NewRequest(http.MethodPost, "/api/v1/book/"+strconv.FormatInt(book.ID, 10)+"/enrich", nil), "id", strconv.FormatInt(book.ID, 10))
	rec := httptest.NewRecorder()
	h.EnrichAudiobook(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Errorf("expected 400 for missing ASIN, got %d", rec.Code)
	}
}

// bookFixtureWithSearcher is like bookFixture but wires a mock searcher.
func bookFixtureWithSearcher(t *testing.T, searcher BookSearcher) (*BookHandler, *db.BookRepo, *models.Author, context.Context) {
	t.Helper()
	database, err := db.OpenMemory()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { database.Close() })

	books := db.NewBookRepo(database)
	authors := db.NewAuthorRepo(database)
	history := db.NewHistoryRepo(database)

	ctx := context.Background()
	author := &models.Author{
		ForeignID: "OL1A", Name: "Test Author", SortName: "Author, Test",
		MetadataProvider: "openlibrary", Monitored: true,
	}
	if err := authors.Create(ctx, author); err != nil {
		t.Fatal(err)
	}
	return NewBookHandler(books, nil, history, searcher), books, author, ctx
}

// TestBookUpdate_FiresSearchOnTransitionToWanted verifies that updating a book
// from any non-wanted status to "wanted" immediately triggers an indexer
// search. The call happens in a goroutine so we use the channel-based mock.
func TestBookUpdate_FiresSearchOnTransitionToWanted(t *testing.T) {
	searcher := newMockBookSearcher()
	h, books, author, ctx := bookFixtureWithSearcher(t, searcher)

	book := &models.Book{
		ForeignID: "B99", AuthorID: author.ID, Title: "Transition Book", SortTitle: "transition book",
		Status: "imported", MediaType: models.MediaTypeEbook,
		Genres: []string{}, MetadataProvider: "openlibrary", Monitored: true,
	}
	if err := books.Create(ctx, book); err != nil {
		t.Fatal(err)
	}

	body := bytes.NewBufferString(`{"status":"wanted"}`)
	req := withURLParam(httptest.NewRequest(http.MethodPut, "/api/v1/book/"+strconv.FormatInt(book.ID, 10), body), "id", strconv.FormatInt(book.ID, 10))
	rec := httptest.NewRecorder()
	h.Update(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}

	got := searcher.waitForCall(t, time.Second)
	if got.ID != book.ID {
		t.Errorf("searcher called with wrong book id: got %d, want %d", got.ID, book.ID)
	}
}

// TestBookUpdate_NoSearchWhenAlreadyWanted confirms that updating a field on a
// book that is already "wanted" does NOT fire a duplicate search.
func TestBookUpdate_NoSearchWhenAlreadyWanted(t *testing.T) {
	searcher := newMockBookSearcher()
	h, books, author, ctx := bookFixtureWithSearcher(t, searcher)

	book := &models.Book{
		ForeignID: "B88", AuthorID: author.ID, Title: "Already Wanted", SortTitle: "already wanted",
		Status: models.BookStatusWanted, MediaType: models.MediaTypeEbook,
		Genres: []string{}, MetadataProvider: "openlibrary", Monitored: true,
	}
	if err := books.Create(ctx, book); err != nil {
		t.Fatal(err)
	}

	// Patch the media type — status stays "wanted" throughout, so no new search.
	body := bytes.NewBufferString(`{"status":"wanted","mediaType":"audiobook"}`)
	req := withURLParam(httptest.NewRequest(http.MethodPut, "/api/v1/book/"+strconv.FormatInt(book.ID, 10), body), "id", strconv.FormatInt(book.ID, 10))
	rec := httptest.NewRecorder()
	h.Update(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}

	searcher.assertNoCall(t, 50*time.Millisecond)
}

// staticRootLister is a RootLister stub that returns a fixed set of root
// folder paths without touching a database. Used by the path-containment
// tests so they don't have to wire a real RootFolderRepo.
type staticRootLister struct {
	paths []string
}

func (s staticRootLister) List(_ context.Context) ([]models.RootFolder, error) {
	out := make([]models.RootFolder, 0, len(s.paths))
	for i, p := range s.paths {
		out = append(out, models.RootFolder{ID: int64(i + 1), Path: p})
	}
	return out, nil
}

// TestBookDelete_PathContainment_RejectsOutsideRoots is the Wave 1 / Bundle B
// defence-in-depth guard: even with ?deleteFiles=true, a DB row whose
// file_path points outside every configured library root must not be
// followed onto disk. The DB row still goes away (the file is already
// orphaned from Bindery's perspective; preserving the row would just leave
// it permanently un-deletable through the UI) but the on-disk file is
// untouched.
func TestBookDelete_PathContainment_RejectsOutsideRoots(t *testing.T) {
	h, books, _, author, ctx := bookFixture(t)
	libA := t.TempDir()
	libB := t.TempDir()
	// Wire the handler with two library roots that do NOT contain the
	// book's file_path.
	h.WithRoots(NewLibraryRoots(staticRootLister{paths: []string{libA, libB}}))

	// Fixture file outside both roots. We use t.TempDir() so the test
	// cleanup deletes it if the production code regresses and removes it
	// here — better an orphan in the temp dir than a real /etc/passwd
	// touch in CI.
	outsideDir := t.TempDir()
	outsidePath := filepath.Join(outsideDir, "definitely-not-mine.epub")
	if err := os.WriteFile(outsidePath, []byte("untouchable"), 0o600); err != nil {
		t.Fatal(err)
	}

	book := &models.Book{
		ForeignID: "B-OUT", AuthorID: author.ID, Title: "Outside", SortTitle: "outside",
		Status: models.BookStatusImported, Genres: []string{},
		MetadataProvider: "openlibrary", Monitored: true,
	}
	if err := books.Create(ctx, book); err != nil {
		t.Fatal(err)
	}
	if err := books.SetFilePath(ctx, book.ID, outsidePath); err != nil {
		t.Fatal(err)
	}

	req := withURLParam(
		httptest.NewRequest(http.MethodDelete, "/api/v1/book/"+strconv.FormatInt(book.ID, 10)+"?deleteFiles=true", nil),
		"id", strconv.FormatInt(book.ID, 10))
	rec := httptest.NewRecorder()
	h.Delete(rec, req)
	if rec.Code != http.StatusNoContent {
		t.Fatalf("expected 204, got %d: %s", rec.Code, rec.Body.String())
	}

	// The on-disk file must survive.
	if _, err := os.Stat(outsidePath); err != nil {
		t.Errorf("file outside library roots must NOT be deleted: stat err=%v", err)
	}
	// The DB row must be gone.
	if got, _ := books.GetByID(ctx, book.ID); got != nil {
		t.Errorf("book row should be deleted even when on-disk delete is refused, got id=%d", got.ID)
	}
}

// TestBookDelete_PathContainment_AllowsInsideRoots confirms the positive
// case: when the file_path IS inside a configured root, the on-disk file
// is removed and the DB row goes away — the containment check must not
// regress the existing legitimate-delete path.
func TestBookDelete_PathContainment_AllowsInsideRoots(t *testing.T) {
	h, books, _, author, ctx := bookFixture(t)
	libA := t.TempDir()
	libB := t.TempDir()
	h.WithRoots(NewLibraryRoots(staticRootLister{paths: []string{libA, libB}}))

	// File legitimately under libA, mirroring the importer's
	// "<root>/<Author>/<Title>.epub" layout.
	authorDir := filepath.Join(libA, "Test Author")
	if err := os.MkdirAll(authorDir, 0o755); err != nil {
		t.Fatal(err)
	}
	insidePath := filepath.Join(authorDir, "book.epub")
	if err := os.WriteFile(insidePath, []byte("legit"), 0o600); err != nil {
		t.Fatal(err)
	}

	book := &models.Book{
		ForeignID: "B-IN", AuthorID: author.ID, Title: "Inside", SortTitle: "inside",
		Status: models.BookStatusImported, Genres: []string{},
		MetadataProvider: "openlibrary", Monitored: true,
	}
	if err := books.Create(ctx, book); err != nil {
		t.Fatal(err)
	}
	if err := books.SetFilePath(ctx, book.ID, insidePath); err != nil {
		t.Fatal(err)
	}

	req := withURLParam(
		httptest.NewRequest(http.MethodDelete, "/api/v1/book/"+strconv.FormatInt(book.ID, 10)+"?deleteFiles=true", nil),
		"id", strconv.FormatInt(book.ID, 10))
	rec := httptest.NewRecorder()
	h.Delete(rec, req)
	if rec.Code != http.StatusNoContent {
		t.Fatalf("expected 204, got %d: %s", rec.Code, rec.Body.String())
	}

	if _, err := os.Stat(insidePath); !os.IsNotExist(err) {
		t.Errorf("file inside library root SHOULD be deleted, stat err=%v", err)
	}
	if got, _ := books.GetByID(ctx, book.ID); got != nil {
		t.Errorf("book row should be deleted, got id=%d", got.ID)
	}
}

// TestBookDeleteFile_PathContainment_RejectsOutsideRoots covers the
// "scrub-and-keep" flow (DeleteFile, not Delete): an outside-roots
// file_path must not get unlinked, but the book row must keep moving
// through its state transition (book_files row dropped, status flipped
// back to wanted) so the orphan path doesn't get stuck in the UI.
func TestBookDeleteFile_PathContainment_RejectsOutsideRoots(t *testing.T) {
	h, books, _, author, ctx := bookFixture(t)
	libA := t.TempDir()
	h.WithRoots(NewLibraryRoots(staticRootLister{paths: []string{libA}}))

	outsideDir := t.TempDir()
	outsidePath := filepath.Join(outsideDir, "outside.epub")
	if err := os.WriteFile(outsidePath, []byte("untouchable"), 0o600); err != nil {
		t.Fatal(err)
	}

	book := &models.Book{
		ForeignID: "B-DF-OUT", AuthorID: author.ID, Title: "DF Outside", SortTitle: "df out",
		Status: models.BookStatusImported, Genres: []string{},
		MetadataProvider: "openlibrary", Monitored: true,
	}
	if err := books.Create(ctx, book); err != nil {
		t.Fatal(err)
	}
	if err := books.SetFilePath(ctx, book.ID, outsidePath); err != nil {
		t.Fatal(err)
	}

	req := withURLParam(
		httptest.NewRequest(http.MethodDelete, "/api/v1/book/"+strconv.FormatInt(book.ID, 10)+"/file", nil),
		"id", strconv.FormatInt(book.ID, 10))
	rec := httptest.NewRecorder()
	h.DeleteFile(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}

	// File survives.
	if _, err := os.Stat(outsidePath); err != nil {
		t.Errorf("file outside library roots must NOT be deleted: stat err=%v", err)
	}
	// Status flips, file_path is cleared — the orphan no longer shows up as
	// an imported file.
	got, _ := books.GetByID(ctx, book.ID)
	if got == nil {
		t.Fatal("book row should remain after DeleteFile")
	}
	if got.FilePath != "" {
		t.Errorf("file_path should be cleared after refused delete, got %q", got.FilePath)
	}
}

// TestBookHandler_LifetimeCtxFallsBackToBackground pins the bgCtx contract:
// when WithLifetimeCtx is not called the auto-grab goroutine spawned on a
// status flip to wanted runs against context.Background() (preserving legacy
// behaviour for tests that construct the handler bare). When set, the spawn
// uses the supplied lifetime ctx so Server.Shutdown can cancel it cleanly.
// This is the #846 follow-up sweep that closed the four remaining handlers
// that bypassed contextBackground via context.WithoutCancel.
func TestBookHandler_LifetimeCtxFallsBackToBackground(t *testing.T) {
	h := &BookHandler{}
	if h.bgCtx() != context.Background() {
		t.Error("bgCtx without WithLifetimeCtx must return context.Background()")
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	h.WithLifetimeCtx(ctx)
	if h.bgCtx() != ctx {
		t.Error("bgCtx with WithLifetimeCtx must return the supplied ctx")
	}
	// Nil ctx must be tolerated (matches BulkHandler/AuthorHandler).
	h.WithLifetimeCtx(nil) //nolint:staticcheck // SA1012 testing nil-tolerance contract
	if h.bgCtx() != ctx {
		t.Error("WithLifetimeCtx(nil) must not clobber a previously installed ctx")
	}
}

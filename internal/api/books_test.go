package api

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"testing"

	"github.com/go-chi/chi/v5"

	"github.com/vavallee/bindery/internal/db"
	"github.com/vavallee/bindery/internal/models"
)

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
	return NewBookHandler(books, nil, history), books, authors, author, ctx
}

func withURLParam(req *http.Request, key, val string) *http.Request {
	rctx := chi.NewRouteContext()
	rctx.URLParams.Add(key, val)
	return req.WithContext(context.WithValue(req.Context(), chi.RouteCtxKey, rctx))
}

// TestBookList_Empty returns [] (not null) so the frontend can render without
// a null-check. A nil body here would break the books grid on first load.
func TestBookList_Empty(t *testing.T) {
	h, _, _, _, _ := bookFixture(t)
	rec := httptest.NewRecorder()
	h.List(rec, httptest.NewRequest(http.MethodGet, "/api/v1/book", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Code)
	}
	if bytes.TrimSpace(rec.Body.Bytes())[0] != '[' {
		t.Errorf("expected JSON array, got %s", rec.Body.String())
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
	var got []models.Book
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 {
		t.Errorf("expected 2 books for author %d, got %d", a1.ID, len(got))
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

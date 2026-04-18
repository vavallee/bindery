package api

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/vavallee/bindery/internal/db"
	"github.com/vavallee/bindery/internal/models"
)

func seriesFixture(t *testing.T) (*SeriesHandler, *db.SeriesRepo, *db.AuthorRepo, *db.BookRepo) {
	t.Helper()
	database, err := db.OpenMemory()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { database.Close() })
	repo := db.NewSeriesRepo(database)
	bookRepo := db.NewBookRepo(database)
	return NewSeriesHandler(repo, bookRepo, &mockBookSearcher{}), repo, db.NewAuthorRepo(database), bookRepo
}

func TestSeriesList_Empty(t *testing.T) {
	h, _, _, _ := seriesFixture(t)
	rec := httptest.NewRecorder()
	h.List(rec, httptest.NewRequest(http.MethodGet, "/api/v1/series", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Code)
	}
	if bytes.TrimSpace(rec.Body.Bytes())[0] != '[' {
		t.Errorf("expected JSON array, got %s", rec.Body.String())
	}
}

func TestSeriesGet_BadID(t *testing.T) {
	h, _, _, _ := seriesFixture(t)
	rec := httptest.NewRecorder()
	h.Get(rec, withURLParam(httptest.NewRequest(http.MethodGet, "/api/v1/series/abc", nil), "id", "abc"))
	if rec.Code != http.StatusBadRequest {
		t.Errorf("bad id: expected 400, got %d", rec.Code)
	}
}

func TestSeriesGet_NotFound(t *testing.T) {
	h, _, _, _ := seriesFixture(t)
	rec := httptest.NewRecorder()
	h.Get(rec, withURLParam(httptest.NewRequest(http.MethodGet, "/api/v1/series/999", nil), "id", "999"))
	if rec.Code != http.StatusNotFound {
		t.Errorf("missing: expected 404, got %d", rec.Code)
	}
}

// TestSeriesListAndGet_WithData creates a series with linked books so the
// happy path (List returns rows; Get returns the Books array non-null) is
// covered.
func TestSeriesListAndGet_WithData(t *testing.T) {
	h, seriesRepo, authorRepo, bookRepo := seriesFixture(t)
	ctx := contextBackground()

	author := &models.Author{ForeignID: "OL1A", Name: "A", SortName: "A"}
	if err := authorRepo.Create(ctx, author); err != nil {
		t.Fatal(err)
	}
	book := &models.Book{ForeignID: "OL1B", AuthorID: author.ID, Title: "Book One", Status: models.BookStatusWanted}
	if err := bookRepo.Create(ctx, book); err != nil {
		t.Fatal(err)
	}

	s := &models.Series{ForeignID: "OLSER1", Title: "Series One"}
	if err := seriesRepo.Create(ctx, s); err != nil {
		t.Fatal(err)
	}
	if err := seriesRepo.LinkBook(ctx, s.ID, book.ID, "1", true); err != nil {
		t.Fatal(err)
	}

	// List
	rec := httptest.NewRecorder()
	h.List(rec, httptest.NewRequest(http.MethodGet, "/api/v1/series", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("list: expected 200, got %d", rec.Code)
	}
	var list []models.Series
	json.NewDecoder(rec.Body).Decode(&list)
	if len(list) != 1 {
		t.Fatalf("expected 1 series, got %d", len(list))
	}

	// Get with books
	rec = httptest.NewRecorder()
	h.Get(rec, withURLParam(httptest.NewRequest(http.MethodGet, "/api/v1/series/1", nil), "id", "1"))
	if rec.Code != http.StatusOK {
		t.Fatalf("get: expected 200, got %d", rec.Code)
	}
	var got models.Series
	json.NewDecoder(rec.Body).Decode(&got)
	if len(got.Books) != 1 || got.Books[0].BookID != book.ID {
		t.Errorf("expected linked book in series, got %+v", got.Books)
	}
}

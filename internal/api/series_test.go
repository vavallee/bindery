package api

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/vavallee/bindery/internal/db"
	"github.com/vavallee/bindery/internal/metadata"
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
	authorRepo := db.NewAuthorRepo(database)
	return NewSeriesHandler(repo, bookRepo, authorRepo, nil, &mockBookSearcher{}), repo, authorRepo, bookRepo
}

type stubSeriesProvider struct {
	searchResults []metadata.SeriesSearchResult
	catalogs      map[string]*metadata.SeriesCatalog
	searchErr     error
	catalogErr    error
	searchCalls   int
	searchLimits  []int
	searchQueries []string
	catalogCalls  int
}

func (s *stubSeriesProvider) Name() string { return "stub" }

func (s *stubSeriesProvider) SearchAuthors(context.Context, string) ([]models.Author, error) {
	return nil, nil
}

func (s *stubSeriesProvider) SearchBooks(context.Context, string) ([]models.Book, error) {
	return nil, nil
}

func (s *stubSeriesProvider) GetAuthor(context.Context, string) (*models.Author, error) {
	return nil, nil
}

func (s *stubSeriesProvider) GetBook(context.Context, string) (*models.Book, error) {
	return nil, nil
}

func (s *stubSeriesProvider) GetEditions(context.Context, string) ([]models.Edition, error) {
	return nil, nil
}

func (s *stubSeriesProvider) GetBookByISBN(context.Context, string) (*models.Book, error) {
	return nil, nil
}

func (s *stubSeriesProvider) SearchSeries(_ context.Context, query string, limit int) ([]metadata.SeriesSearchResult, error) {
	s.searchCalls++
	s.searchQueries = append(s.searchQueries, query)
	s.searchLimits = append(s.searchLimits, limit)
	return s.searchResults, s.searchErr
}

func (s *stubSeriesProvider) GetSeriesCatalog(_ context.Context, foreignID string) (*metadata.SeriesCatalog, error) {
	s.catalogCalls++
	if s.catalogErr != nil {
		return nil, s.catalogErr
	}
	return s.catalogs[foreignID], nil
}

func seriesFixtureWithProvider(t *testing.T, provider *stubSeriesProvider, searcher BookSearcher) (*SeriesHandler, *db.SeriesRepo, *db.AuthorRepo, *db.BookRepo) {
	t.Helper()
	database, err := db.OpenMemory()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { database.Close() })
	seriesRepo := db.NewSeriesRepo(database)
	bookRepo := db.NewBookRepo(database)
	authorRepo := db.NewAuthorRepo(database)
	if searcher == nil {
		searcher = &mockBookSearcher{}
	}
	return NewSeriesHandler(seriesRepo, bookRepo, authorRepo, metadata.NewAggregator(provider).WithAudnexClient(nil), searcher), seriesRepo, authorRepo, bookRepo
}

func seriesFixtureWithProviderAndSettings(t *testing.T, provider *stubSeriesProvider, searcher BookSearcher, envEnabled bool) (*SeriesHandler, *db.SeriesRepo, *db.AuthorRepo, *db.BookRepo, *db.SettingsRepo) {
	t.Helper()
	database, err := db.OpenMemory()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { database.Close() })
	seriesRepo := db.NewSeriesRepo(database)
	bookRepo := db.NewBookRepo(database)
	authorRepo := db.NewAuthorRepo(database)
	settingsRepo := db.NewSettingsRepo(database)
	if searcher == nil {
		searcher = &mockBookSearcher{}
	}
	handler := NewSeriesHandler(seriesRepo, bookRepo, authorRepo, metadata.NewAggregator(provider).WithAudnexClient(nil), searcher).
		WithHardcoverFeatureSettings(settingsRepo, envEnabled)
	return handler, seriesRepo, authorRepo, bookRepo, settingsRepo
}

func seriesFixtureWithProviderAndEditions(t *testing.T, provider *stubSeriesProvider, searcher BookSearcher) (*SeriesHandler, *db.SeriesRepo, *db.AuthorRepo, *db.BookRepo, *db.EditionRepo) {
	t.Helper()
	database, err := db.OpenMemory()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { database.Close() })
	seriesRepo := db.NewSeriesRepo(database)
	bookRepo := db.NewBookRepo(database)
	authorRepo := db.NewAuthorRepo(database)
	editionRepo := db.NewEditionRepo(database)
	if searcher == nil {
		searcher = &mockBookSearcher{}
	}
	handler := NewSeriesHandler(seriesRepo, bookRepo, authorRepo, metadata.NewAggregator(provider).WithAudnexClient(nil), searcher).
		WithEditionHydration(editionRepo)
	return handler, seriesRepo, authorRepo, bookRepo, editionRepo
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
	ctx := context.Background()

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
	if len(list[0].Books) != 1 || list[0].Books[0].BookID != book.ID {
		t.Fatalf("expected linked book in series list, got %+v", list[0].Books)
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

func TestSeriesCreateUpdateDeleteAndLink(t *testing.T) {
	h, seriesRepo, authorRepo, bookRepo := seriesFixture(t)
	ctx := context.Background()

	createBody := bytes.NewBufferString(`{"title":"  Dune Chronicles  "}`)
	rec := httptest.NewRecorder()
	h.Create(rec, httptest.NewRequest(http.MethodPost, "/api/v1/series", createBody))
	if rec.Code != http.StatusCreated {
		t.Fatalf("create: expected 201, got %d: %s", rec.Code, rec.Body.String())
	}
	var created models.Series
	if err := json.NewDecoder(rec.Body).Decode(&created); err != nil {
		t.Fatal(err)
	}
	if created.ID == 0 || created.Title != "Dune Chronicles" || !strings.HasPrefix(created.ForeignID, "manual:series:") {
		t.Fatalf("unexpected created series: %+v", created)
	}

	updateBody := bytes.NewBufferString(`{"title":"Dune Saga"}`)
	rec = httptest.NewRecorder()
	h.Update(rec, withURLParam(httptest.NewRequest(http.MethodPut, "/api/v1/series/1", updateBody), "id", strconv.FormatInt(created.ID, 10)))
	if rec.Code != http.StatusOK {
		t.Fatalf("update: expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	var updated models.Series
	if err := json.NewDecoder(rec.Body).Decode(&updated); err != nil {
		t.Fatal(err)
	}
	if updated.Title != "Dune Saga" {
		t.Fatalf("updated title = %q, want Dune Saga", updated.Title)
	}

	author := &models.Author{ForeignID: "OL1A", Name: "Frank Herbert", SortName: "Herbert, Frank"}
	if err := authorRepo.Create(ctx, author); err != nil {
		t.Fatal(err)
	}
	book := &models.Book{ForeignID: "OL1W", AuthorID: author.ID, Title: "Dune", SortTitle: "Dune", Status: models.BookStatusImported}
	if err := bookRepo.Create(ctx, book); err != nil {
		t.Fatal(err)
	}

	linkBody := bytes.NewBufferString(`{"bookId":` + strconv.FormatInt(book.ID, 10) + `,"positionInSeries":"1","primarySeries":true}`)
	rec = httptest.NewRecorder()
	h.AddBook(rec, withURLParam(httptest.NewRequest(http.MethodPost, "/api/v1/series/1/books", linkBody), "id", strconv.FormatInt(created.ID, 10)))
	if rec.Code != http.StatusOK {
		t.Fatalf("link: expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	var linked models.Series
	if err := json.NewDecoder(rec.Body).Decode(&linked); err != nil {
		t.Fatal(err)
	}
	if len(linked.Books) != 1 || linked.Books[0].BookID != book.ID || linked.Books[0].PositionInSeries != "1" {
		t.Fatalf("expected linked book, got %+v", linked.Books)
	}

	linkBody = bytes.NewBufferString(`{"bookId":` + strconv.FormatInt(book.ID, 10) + `,"positionInSeries":"1.5","primarySeries":false}`)
	rec = httptest.NewRecorder()
	h.AddBook(rec, withURLParam(httptest.NewRequest(http.MethodPost, "/api/v1/series/1/books", linkBody), "id", strconv.FormatInt(created.ID, 10)))
	if rec.Code != http.StatusOK {
		t.Fatalf("relink: expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	if got, err := seriesRepo.GetByID(ctx, created.ID); err != nil || got == nil || len(got.Books) != 1 || got.Books[0].PositionInSeries != "1.5" || got.Books[0].PrimarySeries {
		t.Fatalf("expected upserted link, got series=%+v err=%v", got, err)
	}

	rec = httptest.NewRecorder()
	h.Delete(rec, withURLParam(httptest.NewRequest(http.MethodDelete, "/api/v1/series/1", nil), "id", strconv.FormatInt(created.ID, 10)))
	if rec.Code != http.StatusNoContent {
		t.Fatalf("delete: expected 204, got %d: %s", rec.Code, rec.Body.String())
	}
	if got, err := bookRepo.GetByID(ctx, book.ID); err != nil || got == nil {
		t.Fatalf("delete series should preserve linked book, got book=%+v err=%v", got, err)
	}
	deleted, err := seriesRepo.GetByID(ctx, created.ID)
	if err != nil {
		t.Fatal(err)
	}
	if deleted != nil {
		t.Fatalf("expected series to be deleted, got %+v", deleted)
	}
}

func TestSeriesManagementInvalidInput(t *testing.T) {
	h, seriesRepo, _, _ := seriesFixture(t)
	ctx := context.Background()
	series := &models.Series{ForeignID: "ol-series:dune", Title: "Dune Chronicles"}
	if err := seriesRepo.Create(ctx, series); err != nil {
		t.Fatal(err)
	}

	tests := []struct {
		name string
		run  func(*httptest.ResponseRecorder)
		code int
	}{
		{
			name: "create empty title",
			run: func(rec *httptest.ResponseRecorder) {
				h.Create(rec, httptest.NewRequest(http.MethodPost, "/api/v1/series", bytes.NewBufferString(`{"title":" "}`)))
			},
			code: http.StatusBadRequest,
		},
		{
			name: "update missing series",
			run: func(rec *httptest.ResponseRecorder) {
				h.Update(rec, withURLParam(httptest.NewRequest(http.MethodPut, "/api/v1/series/999", bytes.NewBufferString(`{"title":"New"}`)), "id", "999"))
			},
			code: http.StatusNotFound,
		},
		{
			name: "delete missing series",
			run: func(rec *httptest.ResponseRecorder) {
				h.Delete(rec, withURLParam(httptest.NewRequest(http.MethodDelete, "/api/v1/series/999", nil), "id", "999"))
			},
			code: http.StatusNotFound,
		},
		{
			name: "link missing book",
			run: func(rec *httptest.ResponseRecorder) {
				h.AddBook(rec, withURLParam(httptest.NewRequest(http.MethodPost, "/api/v1/series/1/books", bytes.NewBufferString(`{"bookId":999}`)), "id", strconv.FormatInt(series.ID, 10)))
			},
			code: http.StatusNotFound,
		},
		{
			name: "link invalid book id",
			run: func(rec *httptest.ResponseRecorder) {
				h.AddBook(rec, withURLParam(httptest.NewRequest(http.MethodPost, "/api/v1/series/1/books", bytes.NewBufferString(`{"bookId":0}`)), "id", strconv.FormatInt(series.ID, 10)))
			},
			code: http.StatusBadRequest,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rec := httptest.NewRecorder()
			tt.run(rec)
			if rec.Code != tt.code {
				t.Fatalf("expected %d, got %d: %s", tt.code, rec.Code, rec.Body.String())
			}
		})
	}
}

func TestSeriesManagementRejectsOverlongTitle(t *testing.T) {
	h, seriesRepo, _, _ := seriesFixture(t)
	ctx := context.Background()
	tooLongTitle := strings.Repeat("a", seriesTitleMaxLength+1)

	rec := httptest.NewRecorder()
	h.Create(rec, httptest.NewRequest(http.MethodPost, "/api/v1/series", bytes.NewBufferString(`{"title":"`+tooLongTitle+`"}`)))
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("create: expected 400, got %d: %s", rec.Code, rec.Body.String())
	}
	list, err := seriesRepo.List(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != 0 {
		t.Fatalf("overlong create should not write a series, got %+v", list)
	}

	series := &models.Series{ForeignID: "ol-series:dune", Title: "Dune Chronicles"}
	if err := seriesRepo.Create(ctx, series); err != nil {
		t.Fatal(err)
	}

	rec = httptest.NewRecorder()
	h.Update(rec, withURLParam(httptest.NewRequest(http.MethodPut, "/api/v1/series/1", bytes.NewBufferString(`{"title":"`+tooLongTitle+`"}`)), "id", strconv.FormatInt(series.ID, 10)))
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("update: expected 400, got %d: %s", rec.Code, rec.Body.String())
	}
	got, err := seriesRepo.GetByID(ctx, series.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got == nil || got.Title != "Dune Chronicles" {
		t.Fatalf("overlong update should preserve title, got %+v", got)
	}
}

func TestSeriesMonitorEndpoint(t *testing.T) {
	h, seriesRepo, _, _ := seriesFixture(t)
	ctx := context.Background()
	series := &models.Series{ForeignID: "manual:stormlight", Title: "Stormlight Archive"}
	if err := seriesRepo.Create(ctx, series); err != nil {
		t.Fatal(err)
	}

	rec := httptest.NewRecorder()
	h.Monitor(rec, withURLParam(httptest.NewRequest(http.MethodPut, "/api/v1/series/1/monitor", bytes.NewBufferString(`{"monitored":true}`)), "id", strconv.FormatInt(series.ID, 10)))
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	var response map[string]bool
	if err := json.NewDecoder(rec.Body).Decode(&response); err != nil {
		t.Fatal(err)
	}
	if !response["monitored"] {
		t.Fatalf("response = %+v, want monitored true", response)
	}
	updated, err := seriesRepo.GetByID(ctx, series.ID)
	if err != nil {
		t.Fatal(err)
	}
	if updated == nil || !updated.Monitored {
		t.Fatalf("stored series = %+v, want monitored", updated)
	}

	tests := []struct {
		name string
		id   string
		body string
		code int
	}{
		{name: "invalid id", id: "abc", body: `{"monitored":true}`, code: http.StatusBadRequest},
		{name: "invalid body", id: strconv.FormatInt(series.ID, 10), body: `{`, code: http.StatusBadRequest},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rec := httptest.NewRecorder()
			h.Monitor(rec, withURLParam(httptest.NewRequest(http.MethodPut, "/api/v1/series/1/monitor", bytes.NewBufferString(tt.body)), "id", tt.id))
			if rec.Code != tt.code {
				t.Fatalf("expected %d, got %d: %s", tt.code, rec.Code, rec.Body.String())
			}
		})
	}
}

func TestSeriesHardcoverSearch(t *testing.T) {
	provider := &stubSeriesProvider{
		searchResults: []metadata.SeriesSearchResult{{
			ForeignID:    "hc-series:42",
			ProviderID:   "42",
			Title:        "The Stormlight Archive",
			AuthorName:   "Brandon Sanderson",
			BookCount:    10,
			ReadersCount: 19323,
			Books:        []string{"The Way of Kings", "Words of Radiance"},
		}},
		catalogs: map[string]*metadata.SeriesCatalog{},
	}
	h, _, _, _ := seriesFixtureWithProvider(t, provider, nil)

	rec := httptest.NewRecorder()
	h.SearchHardcover(rec, httptest.NewRequest(http.MethodGet, "/api/v1/series/hardcover/search?term=stormlight", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	var got []seriesHardcoverSearchResult
	if err := json.NewDecoder(rec.Body).Decode(&got); err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].ForeignID != "hc-series:42" || got[0].BookCount != 10 {
		t.Fatalf("unexpected search results: %+v", got)
	}
}

func TestSeriesHardcoverSearchNormalizesNilBooks(t *testing.T) {
	provider := &stubSeriesProvider{
		searchResults: []metadata.SeriesSearchResult{{
			ForeignID:  "hc-series:42",
			ProviderID: "42",
			Title:      "The Stormlight Archive",
		}},
		catalogs: map[string]*metadata.SeriesCatalog{},
	}
	h, _, _, _ := seriesFixtureWithProvider(t, provider, nil)

	rec := httptest.NewRecorder()
	h.SearchHardcover(rec, httptest.NewRequest(http.MethodGet, "/api/v1/series/hardcover/search?term=stormlight", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	var got []seriesHardcoverSearchResult
	if err := json.NewDecoder(rec.Body).Decode(&got); err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 {
		t.Fatalf("expected one search result, got %+v", got)
	}
	if got[0].Books == nil {
		t.Fatalf("expected books to encode as an empty array, got nil")
	}
}

func TestSeriesHardcoverSearchDisabledByFeatureState(t *testing.T) {
	provider := &stubSeriesProvider{}
	h, _, _, _, _ := seriesFixtureWithProviderAndSettings(t, provider, nil, false)

	rec := httptest.NewRecorder()
	h.SearchHardcover(rec, httptest.NewRequest(http.MethodGet, "/api/v1/series/hardcover/search?term=stormlight", nil))
	if rec.Code != http.StatusNotFound {
		t.Fatalf("expected 404 when enhanced Hardcover API is disabled, got %d: %s", rec.Code, rec.Body.String())
	}
	if provider.searchCalls != 0 {
		t.Fatalf("provider should not be called when disabled, got %d calls", provider.searchCalls)
	}
}

func TestSeriesHardcoverSearchValidationAndProviderErrors(t *testing.T) {
	tests := []struct {
		name string
		url  string
		code int
	}{
		{name: "missing term", url: "/api/v1/series/hardcover/search", code: http.StatusBadRequest},
		{name: "invalid limit", url: "/api/v1/series/hardcover/search?term=stormlight&limit=abc", code: http.StatusBadRequest},
		{name: "zero limit", url: "/api/v1/series/hardcover/search?term=stormlight&limit=0", code: http.StatusBadRequest},
		{name: "negative limit", url: "/api/v1/series/hardcover/search?term=stormlight&limit=-1", code: http.StatusBadRequest},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			provider := &stubSeriesProvider{}
			h, _, _, _ := seriesFixtureWithProvider(t, provider, nil)
			rec := httptest.NewRecorder()
			h.SearchHardcover(rec, httptest.NewRequest(http.MethodGet, tt.url, nil))
			if rec.Code != tt.code {
				t.Fatalf("expected %d, got %d: %s", tt.code, rec.Code, rec.Body.String())
			}
			if provider.searchCalls != 0 {
				t.Fatalf("provider should not be called for invalid request, got %d calls", provider.searchCalls)
			}
		})
	}

	provider := &stubSeriesProvider{searchErr: errors.New("hardcover unavailable")}
	h, _, _, _ := seriesFixtureWithProvider(t, provider, nil)
	rec := httptest.NewRecorder()
	h.SearchHardcover(rec, httptest.NewRequest(http.MethodGet, "/api/v1/series/hardcover/search?term=stormlight", nil))
	if rec.Code != http.StatusBadGateway {
		t.Fatalf("provider error: expected 502, got %d: %s", rec.Code, rec.Body.String())
	}

	provider = &stubSeriesProvider{
		searchResults: []metadata.SeriesSearchResult{{ForeignID: "hc-series:42", ProviderID: "42", Title: "Stormlight"}},
	}
	h, _, _, _ = seriesFixtureWithProvider(t, provider, nil)
	rec = httptest.NewRecorder()
	h.SearchHardcover(rec, httptest.NewRequest(http.MethodGet, "/api/v1/series/hardcover/search?term=stormlight&limit=99", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("limit cap: expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	if len(provider.searchLimits) != 1 || provider.searchLimits[0] != 50 {
		t.Fatalf("search limits = %+v, want capped limit 50", provider.searchLimits)
	}
	if len(provider.searchQueries) != 1 || provider.searchQueries[0] != "stormlight" {
		t.Fatalf("search queries = %+v, want stormlight", provider.searchQueries)
	}
}

func TestSeriesAutoLinkHardcoverPersistsTopCandidate(t *testing.T) {
	catalog := stormlightCatalog()
	provider := &stubSeriesProvider{
		searchResults: []metadata.SeriesSearchResult{{
			ForeignID:  catalog.ForeignID,
			ProviderID: catalog.ProviderID,
			Title:      catalog.Title,
			AuthorName: catalog.AuthorName,
			BookCount:  catalog.BookCount,
		}},
		catalogs: map[string]*metadata.SeriesCatalog{catalog.ForeignID: catalog},
	}
	h, seriesRepo, authorRepo, bookRepo := seriesFixtureWithProvider(t, provider, nil)
	ctx := context.Background()
	series := &models.Series{ForeignID: "ol-series:stormlight", Title: "The Stormlight Archive"}
	if err := seriesRepo.Create(ctx, series); err != nil {
		t.Fatal(err)
	}
	author := &models.Author{
		ForeignID:        "hc:brandon-sanderson",
		Name:             "Brandon Sanderson",
		SortName:         "Sanderson, Brandon",
		MetadataProvider: "hardcover",
	}
	if err := authorRepo.Create(ctx, author); err != nil {
		t.Fatal(err)
	}
	book := &models.Book{
		ForeignID:        "hc:the-way-of-kings",
		AuthorID:         author.ID,
		Title:            "The Way of Kings",
		SortTitle:        "The Way of Kings",
		Status:           models.BookStatusWanted,
		Genres:           []string{},
		MetadataProvider: "hardcover",
	}
	if err := bookRepo.Create(ctx, book); err != nil {
		t.Fatal(err)
	}
	if _, err := seriesRepo.LinkBookIfMissing(ctx, series.ID, book.ID, "1", true); err != nil {
		t.Fatal(err)
	}

	rec := httptest.NewRecorder()
	h.AutoLinkHardcover(rec, withURLParam(httptest.NewRequest(http.MethodPost, "/api/v1/series/1/hardcover-link/auto", nil), "id", strconv.FormatInt(series.ID, 10)))
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	var response seriesHardcoverAutoResponse
	if err := json.NewDecoder(rec.Body).Decode(&response); err != nil {
		t.Fatal(err)
	}
	if !response.Linked || response.Link == nil || response.Link.HardcoverSeriesID != catalog.ForeignID {
		t.Fatalf("expected persisted auto link, got %+v", response)
	}
}

func TestSeriesAutoLinkHardcoverRejectsExactTitleWrongAuthorWithoutOverlap(t *testing.T) {
	catalog := &metadata.SeriesCatalog{
		ForeignID:  "hc-series:wrong-author",
		ProviderID: "wrong-author",
		Title:      "Shared Series Title",
		AuthorName: "Wrong Author",
		BookCount:  1,
		Books: []metadata.SeriesCatalogBook{{
			ForeignID: "hc:unrelated-book",
			Title:     "Unrelated Book",
			Position:  "1",
			Book:      models.Book{ForeignID: "hc:unrelated-book", Title: "Unrelated Book", Author: &models.Author{Name: "Wrong Author"}},
		}},
	}
	provider := &stubSeriesProvider{
		searchResults: []metadata.SeriesSearchResult{{
			ForeignID:  catalog.ForeignID,
			ProviderID: catalog.ProviderID,
			Title:      catalog.Title,
			AuthorName: catalog.AuthorName,
			BookCount:  catalog.BookCount,
		}},
		catalogs: map[string]*metadata.SeriesCatalog{catalog.ForeignID: catalog},
	}
	h, seriesRepo, authorRepo, bookRepo := seriesFixtureWithProvider(t, provider, nil)
	ctx := context.Background()
	series := &models.Series{ForeignID: "ol-series:shared", Title: "Shared Series Title"}
	if err := seriesRepo.Create(ctx, series); err != nil {
		t.Fatal(err)
	}
	author := &models.Author{
		ForeignID:        "hc:right-author",
		Name:             "Right Author",
		SortName:         "Author, Right",
		MetadataProvider: "hardcover",
	}
	if err := authorRepo.Create(ctx, author); err != nil {
		t.Fatal(err)
	}
	book := &models.Book{
		ForeignID:        "hc:right-book",
		AuthorID:         author.ID,
		Title:            "Right Book",
		SortTitle:        "Right Book",
		Status:           models.BookStatusWanted,
		Genres:           []string{},
		MetadataProvider: "hardcover",
	}
	if err := bookRepo.Create(ctx, book); err != nil {
		t.Fatal(err)
	}
	if _, err := seriesRepo.LinkBookIfMissing(ctx, series.ID, book.ID, "1", true); err != nil {
		t.Fatal(err)
	}

	rec := httptest.NewRecorder()
	h.AutoLinkHardcover(rec, withURLParam(httptest.NewRequest(http.MethodPost, "/api/v1/series/1/hardcover-link/auto", nil), "id", strconv.FormatInt(series.ID, 10)))
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	var response seriesHardcoverAutoResponse
	if err := json.NewDecoder(rec.Body).Decode(&response); err != nil {
		t.Fatal(err)
	}
	if response.Linked {
		t.Fatalf("wrong-author exact-title result should not persist, got %+v", response)
	}
	if len(response.Candidates) != 1 || response.Candidates[0].Confidence >= autoHardcoverLinkMinConfidence {
		t.Fatalf("candidate confidence = %+v, want capped below auto-link threshold", response.Candidates)
	}
	link, err := seriesRepo.GetHardcoverLink(ctx, series.ID)
	if err != nil {
		t.Fatal(err)
	}
	if link != nil {
		t.Fatalf("expected no Hardcover link, got %+v", link)
	}
}

func TestSeriesAutoLinkHardcoverAmbiguousNoop(t *testing.T) {
	catalogA := &metadata.SeriesCatalog{
		ForeignID:  "hc-series:42",
		ProviderID: "42",
		Title:      "Rhythm of War",
		AuthorName: "Brandon Sanderson",
		BookCount:  1,
		Books:      []metadata.SeriesCatalogBook{},
	}
	catalogB := &metadata.SeriesCatalog{
		ForeignID:  "hc-series:99",
		ProviderID: "99",
		Title:      "Rhythm of War",
		AuthorName: "Brandon Sanderson",
		BookCount:  0,
		Books:      []metadata.SeriesCatalogBook{},
	}
	provider := &stubSeriesProvider{
		searchResults: []metadata.SeriesSearchResult{
			{ForeignID: catalogA.ForeignID, ProviderID: catalogA.ProviderID, Title: catalogA.Title, AuthorName: catalogA.AuthorName, BookCount: catalogA.BookCount},
			{ForeignID: catalogB.ForeignID, ProviderID: catalogB.ProviderID, Title: catalogB.Title, AuthorName: catalogB.AuthorName},
		},
		catalogs: map[string]*metadata.SeriesCatalog{
			catalogA.ForeignID: catalogA,
			catalogB.ForeignID: catalogB,
		},
	}
	h, seriesRepo, _, _ := seriesFixtureWithProvider(t, provider, nil)
	series := &models.Series{ForeignID: "ol-series:rhythm", Title: "Rhythm of War"}
	if err := seriesRepo.Create(context.Background(), series); err != nil {
		t.Fatal(err)
	}

	rec := httptest.NewRecorder()
	h.AutoLinkHardcover(rec, withURLParam(httptest.NewRequest(http.MethodPost, "/api/v1/series/1/hardcover-link/auto", nil), "id", "1"))
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	var response seriesHardcoverAutoResponse
	if err := json.NewDecoder(rec.Body).Decode(&response); err != nil {
		t.Fatal(err)
	}
	if response.Linked {
		t.Fatalf("ambiguous result should not persist, got %+v", response)
	}
	link, err := seriesRepo.GetHardcoverLink(context.Background(), series.ID)
	if err != nil {
		t.Fatal(err)
	}
	if link != nil {
		t.Fatalf("expected no link, got %+v", link)
	}
}

func TestSeriesPutHardcoverLinkPersistsManualLink(t *testing.T) {
	catalog := stormlightCatalog()
	provider := &stubSeriesProvider{
		catalogs: map[string]*metadata.SeriesCatalog{catalog.ForeignID: catalog},
	}
	h, seriesRepo, _, _ := seriesFixtureWithProvider(t, provider, nil)
	ctx := context.Background()
	series := &models.Series{ForeignID: "manual:series:stormlight", Title: "Stormlight"}
	if err := seriesRepo.Create(ctx, series); err != nil {
		t.Fatal(err)
	}

	body := bytes.NewBufferString(`{"foreignId":"hc-series:42","providerId":"draft","title":"Draft title","authorName":"Draft Author","bookCount":99,"confidence":0.4}`)
	rec := httptest.NewRecorder()
	h.PutHardcoverLink(rec, withURLParam(httptest.NewRequest(http.MethodPut, "/api/v1/series/1/hardcover-link", body), "id", strconv.FormatInt(series.ID, 10)))
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	var got models.SeriesHardcoverLink
	if err := json.NewDecoder(rec.Body).Decode(&got); err != nil {
		t.Fatal(err)
	}
	if got.HardcoverSeriesID != catalog.ForeignID || got.HardcoverTitle != catalog.Title || got.HardcoverBookCount != catalog.BookCount {
		t.Fatalf("expected catalog-backed manual link, got %+v", got)
	}
	if got.LinkedBy != "manual" || got.Confidence != 0.4 {
		t.Fatalf("expected manual confidence to persist, got %+v", got)
	}
	stored, err := seriesRepo.GetHardcoverLink(ctx, series.ID)
	if err != nil {
		t.Fatal(err)
	}
	if stored == nil || stored.HardcoverSeriesID != catalog.ForeignID {
		t.Fatalf("expected stored link, got %+v", stored)
	}
}

func TestSeriesPutHardcoverLinkInvalidRequests(t *testing.T) {
	catalog := stormlightCatalog()
	h, seriesRepo, _, _ := seriesFixtureWithProvider(t, &stubSeriesProvider{
		catalogs: map[string]*metadata.SeriesCatalog{catalog.ForeignID: catalog},
	}, nil)
	ctx := context.Background()
	series := &models.Series{ForeignID: "manual:series:stormlight", Title: "Stormlight"}
	if err := seriesRepo.Create(ctx, series); err != nil {
		t.Fatal(err)
	}

	tests := []struct {
		name string
		id   string
		body string
		code int
	}{
		{name: "missing series", id: "999", body: `{"foreignId":"hc-series:42"}`, code: http.StatusNotFound},
		{name: "invalid body", id: strconv.FormatInt(series.ID, 10), body: `{`, code: http.StatusBadRequest},
		{name: "missing foreign id", id: strconv.FormatInt(series.ID, 10), body: `{"title":"Stormlight"}`, code: http.StatusBadRequest},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rec := httptest.NewRecorder()
			h.PutHardcoverLink(rec, withURLParam(httptest.NewRequest(http.MethodPut, "/api/v1/series/1/hardcover-link", bytes.NewBufferString(tt.body)), "id", tt.id))
			if rec.Code != tt.code {
				t.Fatalf("expected %d, got %d: %s", tt.code, rec.Code, rec.Body.String())
			}
		})
	}
}

func TestSeriesPutHardcoverLinkProviderFailure(t *testing.T) {
	catalog := stormlightCatalog()
	h, seriesRepo, _, _ := seriesFixtureWithProvider(t, &stubSeriesProvider{
		catalogs:   map[string]*metadata.SeriesCatalog{catalog.ForeignID: catalog},
		catalogErr: errors.New("hardcover unavailable"),
	}, nil)
	ctx := context.Background()
	series := &models.Series{ForeignID: "manual:series:stormlight", Title: "Stormlight"}
	if err := seriesRepo.Create(ctx, series); err != nil {
		t.Fatal(err)
	}

	rec := httptest.NewRecorder()
	h.PutHardcoverLink(rec, withURLParam(httptest.NewRequest(http.MethodPut, "/api/v1/series/1/hardcover-link", bytes.NewBufferString(`{"foreignId":"hc-series:42"}`)), "id", strconv.FormatInt(series.ID, 10)))
	if rec.Code != http.StatusBadGateway {
		t.Fatalf("expected 502, got %d: %s", rec.Code, rec.Body.String())
	}
	link, err := seriesRepo.GetHardcoverLink(ctx, series.ID)
	if err != nil {
		t.Fatal(err)
	}
	if link != nil {
		t.Fatalf("provider failure should not persist a link, got %+v", link)
	}
}

func TestSeriesGetHardcoverLinkEndpoint(t *testing.T) {
	catalog := stormlightCatalog()
	h, seriesRepo, _, _ := seriesFixtureWithProvider(t, &stubSeriesProvider{
		catalogs: map[string]*metadata.SeriesCatalog{catalog.ForeignID: catalog},
	}, nil)
	ctx := context.Background()
	linked := &models.Series{ForeignID: "manual:series:linked", Title: "Linked"}
	if err := seriesRepo.Create(ctx, linked); err != nil {
		t.Fatal(err)
	}
	if err := seriesRepo.UpsertHardcoverLink(ctx, &models.SeriesHardcoverLink{
		SeriesID:            linked.ID,
		HardcoverSeriesID:   catalog.ForeignID,
		HardcoverProviderID: catalog.ProviderID,
		HardcoverTitle:      catalog.Title,
		HardcoverAuthorName: catalog.AuthorName,
		HardcoverBookCount:  catalog.BookCount,
		Confidence:          1,
		LinkedBy:            "manual",
	}); err != nil {
		t.Fatal(err)
	}
	unlinked := &models.Series{ForeignID: "manual:series:unlinked", Title: "Unlinked"}
	if err := seriesRepo.Create(ctx, unlinked); err != nil {
		t.Fatal(err)
	}

	rec := httptest.NewRecorder()
	h.GetHardcoverLink(rec, withURLParam(httptest.NewRequest(http.MethodGet, "/api/v1/series/1/hardcover-link", nil), "id", strconv.FormatInt(linked.ID, 10)))
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	var got models.SeriesHardcoverLink
	if err := json.NewDecoder(rec.Body).Decode(&got); err != nil {
		t.Fatal(err)
	}
	if got.HardcoverSeriesID != catalog.ForeignID {
		t.Fatalf("link = %+v, want %s", got, catalog.ForeignID)
	}

	tests := []struct {
		name string
		id   string
		code int
	}{
		{name: "missing link", id: strconv.FormatInt(unlinked.ID, 10), code: http.StatusNotFound},
		{name: "invalid id", id: "abc", code: http.StatusBadRequest},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rec := httptest.NewRecorder()
			h.GetHardcoverLink(rec, withURLParam(httptest.NewRequest(http.MethodGet, "/api/v1/series/1/hardcover-link", nil), "id", tt.id))
			if rec.Code != tt.code {
				t.Fatalf("expected %d, got %d: %s", tt.code, rec.Code, rec.Body.String())
			}
		})
	}
}

func TestSeriesGetHardcoverLinkDisabledByFeatureState(t *testing.T) {
	provider := &stubSeriesProvider{}
	h, _, _, _, _ := seriesFixtureWithProviderAndSettings(t, provider, nil, false)

	rec := httptest.NewRecorder()
	h.GetHardcoverLink(rec, withURLParam(httptest.NewRequest(http.MethodGet, "/api/v1/series/1/hardcover-link", nil), "id", "1"))
	if rec.Code != http.StatusNotFound {
		t.Fatalf("expected 404 when enhanced Hardcover API is disabled, got %d: %s", rec.Code, rec.Body.String())
	}
}

func TestSeriesDeleteHardcoverLinkRemovesStoredLink(t *testing.T) {
	catalog := stormlightCatalog()
	h, seriesRepo, _, _ := seriesFixtureWithProvider(t, &stubSeriesProvider{
		catalogs: map[string]*metadata.SeriesCatalog{catalog.ForeignID: catalog},
	}, nil)
	ctx := context.Background()
	series := &models.Series{ForeignID: "manual:series:stormlight", Title: "Stormlight"}
	if err := seriesRepo.Create(ctx, series); err != nil {
		t.Fatal(err)
	}
	if err := seriesRepo.UpsertHardcoverLink(ctx, &models.SeriesHardcoverLink{
		SeriesID:            series.ID,
		HardcoverSeriesID:   catalog.ForeignID,
		HardcoverProviderID: catalog.ProviderID,
		HardcoverTitle:      catalog.Title,
		HardcoverAuthorName: catalog.AuthorName,
		HardcoverBookCount:  catalog.BookCount,
		Confidence:          1,
		LinkedBy:            "manual",
	}); err != nil {
		t.Fatal(err)
	}

	rec := httptest.NewRecorder()
	h.DeleteHardcoverLink(rec, withURLParam(httptest.NewRequest(http.MethodDelete, "/api/v1/series/1/hardcover-link", nil), "id", strconv.FormatInt(series.ID, 10)))
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	link, err := seriesRepo.GetHardcoverLink(ctx, series.ID)
	if err != nil {
		t.Fatal(err)
	}
	if link != nil {
		t.Fatalf("expected link to be deleted, got %+v", link)
	}
}

func TestSeriesHardcoverDiffEndpoint(t *testing.T) {
	catalog := stormlightCatalog()
	catalog.Books = append(catalog.Books,
		metadata.SeriesCatalogBook{
			ForeignID:  "hc:words-of-radiance",
			ProviderID: "102",
			Title:      "Words of Radiance",
			Position:   "2",
			Book: models.Book{
				ForeignID: "hc:words-of-radiance",
				Title:     "Words of Radiance",
				Author:    catalog.Books[0].Book.Author,
			},
		},
		metadata.SeriesCatalogBook{
			ForeignID:  "hc:oathbringer",
			ProviderID: "103",
			Title:      "Oathbringer",
			Position:   "3",
			Book: models.Book{
				ForeignID: "hc:oathbringer",
				Title:     "Oathbringer",
				Author:    catalog.Books[0].Book.Author,
			},
		},
	)
	catalog.BookCount = len(catalog.Books)
	h, seriesRepo, authorRepo, bookRepo := seriesFixtureWithProvider(t, &stubSeriesProvider{
		catalogs: map[string]*metadata.SeriesCatalog{catalog.ForeignID: catalog},
	}, nil)
	ctx := context.Background()
	series := &models.Series{ForeignID: "manual:series:stormlight", Title: "Stormlight"}
	if err := seriesRepo.Create(ctx, series); err != nil {
		t.Fatal(err)
	}
	if err := seriesRepo.UpsertHardcoverLink(ctx, &models.SeriesHardcoverLink{
		SeriesID:            series.ID,
		HardcoverSeriesID:   catalog.ForeignID,
		HardcoverProviderID: catalog.ProviderID,
		HardcoverTitle:      catalog.Title,
		HardcoverAuthorName: catalog.AuthorName,
		HardcoverBookCount:  catalog.BookCount,
		Confidence:          1,
		LinkedBy:            "manual",
	}); err != nil {
		t.Fatal(err)
	}
	author := &models.Author{ForeignID: "hc:brandon-sanderson", Name: "Brandon Sanderson", SortName: "Sanderson, Brandon"}
	if err := authorRepo.Create(ctx, author); err != nil {
		t.Fatal(err)
	}
	books := []struct {
		foreignID string
		title     string
		position  string
	}{
		{foreignID: "hc:the-way-of-kings", title: "The Way of Kings", position: "1"},
		{foreignID: "local:radiant-words", title: "Radiant Words", position: ""},
		{foreignID: "local:unrelated", title: "Completely Different Local Book", position: ""},
	}
	for _, item := range books {
		book := &models.Book{
			ForeignID: item.foreignID,
			AuthorID:  author.ID,
			Title:     item.title,
			SortTitle: item.title,
			Status:    models.BookStatusWanted,
			Genres:    []string{},
		}
		if err := bookRepo.Create(ctx, book); err != nil {
			t.Fatal(err)
		}
		if _, err := seriesRepo.LinkBookIfMissing(ctx, series.ID, book.ID, item.position, true); err != nil {
			t.Fatal(err)
		}
	}

	rec := httptest.NewRecorder()
	h.HardcoverDiff(rec, withURLParam(httptest.NewRequest(http.MethodGet, "/api/v1/series/1/hardcover-diff", nil), "id", strconv.FormatInt(series.ID, 10)))
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	var got seriesHardcoverDiffResponse
	if err := json.NewDecoder(rec.Body).Decode(&got); err != nil {
		t.Fatal(err)
	}
	if len(got.Present) != 1 || got.Present[0].ForeignBookID != "hc:the-way-of-kings" {
		t.Fatalf("present = %+v, want The Way of Kings", got.Present)
	}
	if len(got.Uncertain) != 1 || got.Uncertain[0].ForeignBookID != "hc:words-of-radiance" {
		t.Fatalf("uncertain = %+v, want Words of Radiance", got.Uncertain)
	}
	if len(got.LocalOnly) != 1 || got.LocalOnly[0].LocalTitle != "Completely Different Local Book" {
		t.Fatalf("localOnly = %+v, want unrelated local book", got.LocalOnly)
	}
	if len(got.Missing) != 1 || got.Missing[0].ForeignBookID != "hc:oathbringer" || got.PresentCount != 1 || got.MissingCount != 1 {
		t.Fatalf("missing/counts = %+v present=%d missing=%d, want Oathbringer and counts 1/1", got.Missing, got.PresentCount, got.MissingCount)
	}
}

// TestBuildHardcoverDiffEnrichesMissingWithOwnedLibraryBook is the regression
// guard for #1210. A catalog book that is NOT linked to this series (so it
// lands in Missing) but already exists in the requesting user's library must
// get LocalBookID populated so the frontend renders a link instead of an "add"
// button. The companion assertion is the security guard: an identical book
// owned by a DIFFERENT user must NOT be linked (LocalBookID stays nil), or one
// tenant's series page would leak a link to another tenant's book.
func TestBuildHardcoverDiffEnrichesMissingWithOwnedLibraryBook(t *testing.T) {
	database, err := db.OpenMemory()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { database.Close() })
	seriesRepo := db.NewSeriesRepo(database)
	bookRepo := db.NewBookRepo(database)
	authorRepo := db.NewAuthorRepo(database)
	userRepo := db.NewUserRepo(database)
	ctx := context.Background()

	owner, err := userRepo.Create(ctx, "owner", "h")
	if err != nil {
		t.Fatal(err)
	}
	other, err := userRepo.Create(ctx, "other", "h")
	if err != nil {
		t.Fatal(err)
	}
	ownerUserID := owner.ID
	otherUserID := other.ID

	// Catalog: two books. "owned-missing" exists in user 7's library but is not
	// linked to the series; "other-missing" exists only in user 8's library.
	catalog := stormlightCatalog()
	catalog.Books = append(catalog.Books,
		metadata.SeriesCatalogBook{
			ForeignID:  "hc:owned-missing",
			ProviderID: "201",
			Title:      "Owned But Unlinked",
			Position:   "2",
			Book: models.Book{
				ForeignID: "hc:owned-missing",
				Title:     "Owned But Unlinked",
				Author:    catalog.Books[0].Book.Author,
			},
		},
		metadata.SeriesCatalogBook{
			ForeignID:  "hc:other-missing",
			ProviderID: "202",
			Title:      "Owned By Someone Else",
			Position:   "3",
			Book: models.Book{
				ForeignID: "hc:other-missing",
				Title:     "Owned By Someone Else",
				Author:    catalog.Books[0].Book.Author,
			},
		},
	)
	catalog.BookCount = len(catalog.Books)

	series := &models.Series{ForeignID: "manual:series:stormlight", Title: "Stormlight"}
	if err := seriesRepo.Create(ctx, series); err != nil {
		t.Fatal(err)
	}
	link := &models.SeriesHardcoverLink{
		SeriesID:          series.ID,
		HardcoverSeriesID: catalog.ForeignID,
	}

	author := &models.Author{ForeignID: "hc:brandon-sanderson", Name: "Brandon Sanderson", SortName: "Sanderson, Brandon"}
	if err := authorRepo.Create(ctx, author); err != nil {
		t.Fatal(err)
	}

	// Create the two library books and stamp their owners. BookRepo.Create does
	// not set owner_user_id, so stamp it directly (mirrors history_test.go).
	makeBook := func(foreignID, title string, owner int64) int64 {
		book := &models.Book{
			ForeignID: foreignID,
			AuthorID:  author.ID,
			Title:     title,
			SortTitle: title,
			Status:    models.BookStatusDownloaded,
			Genres:    []string{},
		}
		if err := bookRepo.Create(ctx, book); err != nil {
			t.Fatal(err)
		}
		if _, err := database.Exec("UPDATE books SET owner_user_id=? WHERE id=?", owner, book.ID); err != nil {
			t.Fatal(err)
		}
		return book.ID
	}
	ownedID := makeBook("hc:owned-missing", "Owned But Unlinked (local)", ownerUserID)
	makeBook("hc:other-missing", "Owned By Someone Else (local)", otherUserID)

	// Build the diff as user 7. Neither extra book is linked to the series, so
	// both land in Missing.
	diff := buildHardcoverDiff(ctx, bookRepo, ownerUserID, series, link, catalog)

	var ownedRow, otherRow *seriesHardcoverDiffBook
	for i := range diff.Missing {
		switch diff.Missing[i].ForeignBookID {
		case "hc:owned-missing":
			ownedRow = &diff.Missing[i]
		case "hc:other-missing":
			otherRow = &diff.Missing[i]
		}
	}
	if ownedRow == nil || otherRow == nil {
		t.Fatalf("expected both unlinked catalog books in Missing, got %+v", diff.Missing)
	}

	// Positive: the user's own unlinked-but-existing book is linked.
	if ownedRow.LocalBookID == nil || *ownedRow.LocalBookID != ownedID {
		t.Fatalf("owned Missing row LocalBookID = %v, want %d", ownedRow.LocalBookID, ownedID)
	}
	if ownedRow.LocalTitle != "Owned But Unlinked (local)" {
		t.Fatalf("owned Missing row LocalTitle = %q, want the local title", ownedRow.LocalTitle)
	}

	// Security guard: a book owned by a different user must NOT be linked.
	if otherRow.LocalBookID != nil {
		t.Fatalf("cross-tenant leak: other user's book linked on Missing row, LocalBookID = %v", *otherRow.LocalBookID)
	}
}

func TestSeriesHardcoverDiffEndpointErrors(t *testing.T) {
	catalog := stormlightCatalog()
	h, seriesRepo, _, _ := seriesFixtureWithProvider(t, &stubSeriesProvider{
		catalogs:   map[string]*metadata.SeriesCatalog{catalog.ForeignID: catalog},
		catalogErr: errors.New("hardcover unavailable"),
	}, nil)
	ctx := context.Background()
	linked := &models.Series{ForeignID: "manual:series:linked", Title: "Linked"}
	if err := seriesRepo.Create(ctx, linked); err != nil {
		t.Fatal(err)
	}
	if err := seriesRepo.UpsertHardcoverLink(ctx, &models.SeriesHardcoverLink{
		SeriesID:            linked.ID,
		HardcoverSeriesID:   catalog.ForeignID,
		HardcoverProviderID: catalog.ProviderID,
		HardcoverTitle:      catalog.Title,
		HardcoverBookCount:  catalog.BookCount,
		Confidence:          1,
		LinkedBy:            "manual",
	}); err != nil {
		t.Fatal(err)
	}
	unlinked := &models.Series{ForeignID: "manual:series:unlinked", Title: "Unlinked"}
	if err := seriesRepo.Create(ctx, unlinked); err != nil {
		t.Fatal(err)
	}

	tests := []struct {
		name string
		id   string
		code int
	}{
		{name: "missing series", id: "999", code: http.StatusNotFound},
		{name: "missing link", id: strconv.FormatInt(unlinked.ID, 10), code: http.StatusNotFound},
		{name: "provider failure", id: strconv.FormatInt(linked.ID, 10), code: http.StatusBadGateway},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rec := httptest.NewRecorder()
			h.HardcoverDiff(rec, withURLParam(httptest.NewRequest(http.MethodGet, "/api/v1/series/1/hardcover-diff", nil), "id", tt.id))
			if rec.Code != tt.code {
				t.Fatalf("expected %d, got %d: %s", tt.code, rec.Code, rec.Body.String())
			}
		})
	}
}

func TestSeriesFillEndpointErrors(t *testing.T) {
	t.Run("invalid id", func(t *testing.T) {
		h, _, _, _ := seriesFixture(t)
		rec := httptest.NewRecorder()
		h.Fill(rec, withURLParam(httptest.NewRequest(http.MethodPost, "/api/v1/series/abc/fill", nil), "id", "abc"))
		if rec.Code != http.StatusBadRequest {
			t.Fatalf("expected 400, got %d: %s", rec.Code, rec.Body.String())
		}
	})

	t.Run("invalid body", func(t *testing.T) {
		h, _, _, _ := seriesFixture(t)
		rec := httptest.NewRecorder()
		h.Fill(rec, withURLParam(httptest.NewRequest(http.MethodPost, "/api/v1/series/1/fill", bytes.NewBufferString(`{`)), "id", "1"))
		if rec.Code != http.StatusBadRequest {
			t.Fatalf("expected 400, got %d: %s", rec.Code, rec.Body.String())
		}
	})

	t.Run("invalid media type", func(t *testing.T) {
		h, _, _, _ := seriesFixture(t)
		rec := httptest.NewRecorder()
		h.Fill(rec, withURLParam(httptest.NewRequest(http.MethodPost, "/api/v1/series/1/fill", bytes.NewBufferString(`{"mediaType":"hologram"}`)), "id", "1"))
		if rec.Code != http.StatusBadRequest {
			t.Fatalf("expected 400, got %d: %s", rec.Code, rec.Body.String())
		}
	})

	t.Run("selector feature disabled", func(t *testing.T) {
		provider := &stubSeriesProvider{}
		h, _, _, _, _ := seriesFixtureWithProviderAndSettings(t, provider, nil, false)
		rec := httptest.NewRecorder()
		h.Fill(rec, withURLParam(httptest.NewRequest(http.MethodPost, "/api/v1/series/1/fill", bytes.NewBufferString(`{"foreignBookId":"hc:missing"}`)), "id", "1"))
		if rec.Code != http.StatusNotFound {
			t.Fatalf("expected 404, got %d: %s", rec.Code, rec.Body.String())
		}
		if provider.catalogCalls != 0 {
			t.Fatalf("provider should not be called when feature is disabled, got %d calls", provider.catalogCalls)
		}
	})

	t.Run("requested book not found", func(t *testing.T) {
		catalog := stormlightCatalog()
		h, seriesRepo, _, _ := seriesFixtureWithProvider(t, &stubSeriesProvider{
			catalogs: map[string]*metadata.SeriesCatalog{catalog.ForeignID: catalog},
		}, nil)
		ctx := context.Background()
		series := &models.Series{ForeignID: "manual:series:stormlight", Title: "Stormlight"}
		if err := seriesRepo.Create(ctx, series); err != nil {
			t.Fatal(err)
		}
		if err := seriesRepo.UpsertHardcoverLink(ctx, &models.SeriesHardcoverLink{
			SeriesID:            series.ID,
			HardcoverSeriesID:   catalog.ForeignID,
			HardcoverProviderID: catalog.ProviderID,
			HardcoverTitle:      catalog.Title,
			HardcoverBookCount:  catalog.BookCount,
			Confidence:          1,
			LinkedBy:            "manual",
		}); err != nil {
			t.Fatal(err)
		}
		rec := httptest.NewRecorder()
		h.Fill(rec, withURLParam(httptest.NewRequest(http.MethodPost, "/api/v1/series/1/fill", bytes.NewBufferString(`{"foreignBookId":"hc:not-there"}`)), "id", strconv.FormatInt(series.ID, 10)))
		if rec.Code != http.StatusNotFound {
			t.Fatalf("expected 404, got %d: %s", rec.Code, rec.Body.String())
		}
	})

	t.Run("missing link", func(t *testing.T) {
		h, seriesRepo, _, _ := seriesFixtureWithProvider(t, &stubSeriesProvider{}, nil)
		series := &models.Series{ForeignID: "manual:series:unlinked", Title: "Unlinked"}
		if err := seriesRepo.Create(context.Background(), series); err != nil {
			t.Fatal(err)
		}
		rec := httptest.NewRecorder()
		h.Fill(rec, withURLParam(httptest.NewRequest(http.MethodPost, "/api/v1/series/1/fill", bytes.NewBufferString(`{"position":"1"}`)), "id", strconv.FormatInt(series.ID, 10)))
		if rec.Code != http.StatusNotFound {
			t.Fatalf("expected 404, got %d: %s", rec.Code, rec.Body.String())
		}
	})

	t.Run("provider error", func(t *testing.T) {
		catalog := stormlightCatalog()
		h, seriesRepo, _, _ := seriesFixtureWithProvider(t, &stubSeriesProvider{
			catalogs:   map[string]*metadata.SeriesCatalog{catalog.ForeignID: catalog},
			catalogErr: errors.New("hardcover unavailable"),
		}, nil)
		ctx := context.Background()
		series := &models.Series{ForeignID: "manual:series:stormlight", Title: "Stormlight"}
		if err := seriesRepo.Create(ctx, series); err != nil {
			t.Fatal(err)
		}
		if err := seriesRepo.UpsertHardcoverLink(ctx, &models.SeriesHardcoverLink{
			SeriesID:            series.ID,
			HardcoverSeriesID:   catalog.ForeignID,
			HardcoverProviderID: catalog.ProviderID,
			HardcoverTitle:      catalog.Title,
			HardcoverBookCount:  catalog.BookCount,
			Confidence:          1,
			LinkedBy:            "manual",
		}); err != nil {
			t.Fatal(err)
		}
		rec := httptest.NewRecorder()
		h.Fill(rec, withURLParam(httptest.NewRequest(http.MethodPost, "/api/v1/series/1/fill", bytes.NewBufferString(`{"position":"1"}`)), "id", strconv.FormatInt(series.ID, 10)))
		if rec.Code != http.StatusBadGateway {
			t.Fatalf("expected 502, got %d: %s", rec.Code, rec.Body.String())
		}
	})
}

func TestSeriesFillSkipsHardcoverCatalogWhenFeatureDisabled(t *testing.T) {
	catalog := stormlightCatalog()
	provider := &stubSeriesProvider{
		catalogs: map[string]*metadata.SeriesCatalog{catalog.ForeignID: catalog},
	}
	searcher := newMockBookSearcher()
	h, seriesRepo, authorRepo, bookRepo, settingsRepo := seriesFixtureWithProviderAndSettings(t, provider, searcher, false)
	ctx := context.Background()
	series := &models.Series{ForeignID: "ol-series:stormlight", Title: "The Stormlight Archive"}
	if err := seriesRepo.Create(ctx, series); err != nil {
		t.Fatal(err)
	}
	link := &models.SeriesHardcoverLink{
		SeriesID:            series.ID,
		HardcoverSeriesID:   catalog.ForeignID,
		HardcoverProviderID: catalog.ProviderID,
		HardcoverTitle:      catalog.Title,
		HardcoverAuthorName: catalog.AuthorName,
		HardcoverBookCount:  catalog.BookCount,
		Confidence:          1,
		LinkedBy:            "manual",
	}
	if err := seriesRepo.UpsertHardcoverLink(ctx, link); err != nil {
		t.Fatal(err)
	}
	if err := settingsRepo.Set(ctx, SettingHardcoverAPIToken, "hc-secret"); err != nil {
		t.Fatal(err)
	}
	if err := settingsRepo.Set(ctx, SettingHardcoverEnhancedSeriesEnabled, "true"); err != nil {
		t.Fatal(err)
	}
	author := &models.Author{
		ForeignID:        "hc:brandon-sanderson",
		Name:             "Brandon Sanderson",
		SortName:         "Sanderson, Brandon",
		MetadataProvider: "hardcover",
		Monitored:        true,
	}
	if err := authorRepo.Create(ctx, author); err != nil {
		t.Fatal(err)
	}
	book := &models.Book{
		ForeignID:        "hc:words-of-radiance",
		AuthorID:         author.ID,
		Title:            "Words of Radiance",
		SortTitle:        "Words of Radiance",
		Status:           models.BookStatusSkipped,
		Genres:           []string{},
		MetadataProvider: "hardcover",
	}
	if err := bookRepo.Create(ctx, book); err != nil {
		t.Fatal(err)
	}
	if _, err := seriesRepo.LinkBookIfMissing(ctx, series.ID, book.ID, "2", true); err != nil {
		t.Fatal(err)
	}

	rec := httptest.NewRecorder()
	h.Fill(rec, withURLParam(httptest.NewRequest(http.MethodPost, "/api/v1/series/1/fill", nil), "id", "1"))
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	if provider.catalogCalls != 0 {
		t.Fatalf("hardcover catalog should not be called while env disables enhanced API, got %d calls", provider.catalogCalls)
	}
	queued := searcher.waitForCall(t, time.Second)
	if queued.ID != book.ID {
		t.Fatalf("expected local linked book to be queued, got %+v", queued)
	}
}

func TestSeriesFillCreatesMissingHardcoverBook(t *testing.T) {
	catalog := stormlightCatalog()
	searcher := newMockBookSearcher()
	h, seriesRepo, _, bookRepo := seriesFixtureWithProvider(t, &stubSeriesProvider{
		catalogs: map[string]*metadata.SeriesCatalog{catalog.ForeignID: catalog},
	}, searcher)
	series := &models.Series{ForeignID: "ol-series:stormlight", Title: "The Stormlight Archive"}
	if err := seriesRepo.Create(context.Background(), series); err != nil {
		t.Fatal(err)
	}
	link := &models.SeriesHardcoverLink{
		SeriesID:            series.ID,
		HardcoverSeriesID:   catalog.ForeignID,
		HardcoverProviderID: catalog.ProviderID,
		HardcoverTitle:      catalog.Title,
		HardcoverAuthorName: catalog.AuthorName,
		HardcoverBookCount:  catalog.BookCount,
		Confidence:          1,
		LinkedBy:            "manual",
	}
	if err := seriesRepo.UpsertHardcoverLink(context.Background(), link); err != nil {
		t.Fatal(err)
	}
	if err := seriesRepo.SetGenreOverride(context.Background(), series.ID, []string{"Fantasy", "Epic"}); err != nil {
		t.Fatal(err)
	}

	rec := httptest.NewRecorder()
	h.Fill(rec, withURLParam(httptest.NewRequest(http.MethodPost, "/api/v1/series/1/fill", nil), "id", "1"))
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	var body map[string]int
	if err := json.NewDecoder(rec.Body).Decode(&body); err != nil {
		t.Fatal(err)
	}
	if body["queued"] != 1 {
		t.Fatalf("expected one queued book, got %+v", body)
	}
	queued := searcher.waitForCall(t, time.Second)
	if queued.Title != "The Way of Kings" {
		t.Fatalf("unexpected queued book: %+v", queued)
	}
	created, err := bookRepo.GetByForeignID(context.Background(), "hc:the-way-of-kings")
	if err != nil {
		t.Fatal(err)
	}
	if created == nil {
		t.Fatal("expected Hardcover book to be created")
		return
	}
	if created.MetadataProvider != "hardcover" {
		t.Fatalf("expected metadata provider to be preserved, got %q", created.MetadataProvider)
	}
	if !created.AnyEditionOK {
		t.Fatal("expected anyEditionOk to be preserved")
	}
	if len(created.Genres) != 2 || created.Genres[0] != "Fantasy" || created.Genres[1] != "Epic" {
		t.Fatalf("expected series genres on created book, got %v", created.Genres)
	}
	if !created.IsFieldLocked(models.BookFieldGenres) {
		t.Fatal("expected series genres to be locked on created book")
	}
	books, err := seriesRepo.ListBooksInSeries(context.Background(), series.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(books) != 1 || books[0].ForeignID != "hc:the-way-of-kings" {
		t.Fatalf("expected created book linked to series, got %+v", books)
	}
}

// TestSeriesFillHonoursRequestedMediaType covers #1124: the per-book "add"
// selector must create the missing Hardcover book with the chosen media type
// rather than always defaulting to ebook.
func TestSeriesFillHonoursRequestedMediaType(t *testing.T) {
	cases := []struct {
		name      string
		mediaType string
	}{
		{name: "audiobook", mediaType: models.MediaTypeAudiobook},
		{name: "both", mediaType: models.MediaTypeBoth},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			catalog := stormlightCatalog()
			searcher := newMockBookSearcher()
			h, seriesRepo, _, bookRepo := seriesFixtureWithProvider(t, &stubSeriesProvider{
				catalogs: map[string]*metadata.SeriesCatalog{catalog.ForeignID: catalog},
			}, searcher)
			series := &models.Series{ForeignID: "ol-series:stormlight", Title: "The Stormlight Archive"}
			if err := seriesRepo.Create(context.Background(), series); err != nil {
				t.Fatal(err)
			}
			if err := seriesRepo.UpsertHardcoverLink(context.Background(), &models.SeriesHardcoverLink{
				SeriesID:            series.ID,
				HardcoverSeriesID:   catalog.ForeignID,
				HardcoverProviderID: catalog.ProviderID,
				HardcoverTitle:      catalog.Title,
				HardcoverAuthorName: catalog.AuthorName,
				HardcoverBookCount:  catalog.BookCount,
				Confidence:          1,
				LinkedBy:            "manual",
			}); err != nil {
				t.Fatal(err)
			}

			rec := httptest.NewRecorder()
			body := bytes.NewBufferString(`{"foreignBookId":"hc:the-way-of-kings","mediaType":"` + tc.mediaType + `"}`)
			h.Fill(rec, withURLParam(httptest.NewRequest(http.MethodPost, "/api/v1/series/1/fill", body), "id", strconv.FormatInt(series.ID, 10)))
			if rec.Code != http.StatusOK {
				t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
			}
			searcher.waitForCall(t, time.Second)
			created, err := bookRepo.GetByForeignID(context.Background(), "hc:the-way-of-kings")
			if err != nil {
				t.Fatal(err)
			}
			if created == nil {
				t.Fatal("expected Hardcover book to be created")
				return
			}
			if created.MediaType != tc.mediaType {
				t.Fatalf("created book mediaType = %q, want %q", created.MediaType, tc.mediaType)
			}
		})
	}
}

func TestSeriesFillHydratesHardcoverEditionsBeforeQueue(t *testing.T) {
	catalog := stormlightCatalog()
	catalog.Books[0].Book.MediaType = models.MediaTypeAudiobook
	searcher := newMockBookSearcher()
	h, seriesRepo, _, bookRepo, editionRepo := seriesFixtureWithProviderAndEditions(t, &stubSeriesProvider{
		catalogs: map[string]*metadata.SeriesCatalog{catalog.ForeignID: catalog},
	}, searcher)
	audioASIN := "B000STORML"
	h.WithEditionFetcher(func(context.Context, string) ([]models.Edition, error) {
		return []models.Edition{{
			ForeignID: "hc:stormlight-audio",
			Title:     "The Way of Kings",
			ASIN:      &audioASIN,
			Format:    "Audiobook",
			Monitored: true,
		}}, nil
	})
	series := &models.Series{ForeignID: "ol-series:stormlight", Title: "The Stormlight Archive"}
	if err := seriesRepo.Create(context.Background(), series); err != nil {
		t.Fatal(err)
	}
	if err := seriesRepo.UpsertHardcoverLink(context.Background(), &models.SeriesHardcoverLink{
		SeriesID:            series.ID,
		HardcoverSeriesID:   catalog.ForeignID,
		HardcoverProviderID: catalog.ProviderID,
		HardcoverTitle:      catalog.Title,
		HardcoverAuthorName: catalog.AuthorName,
		HardcoverBookCount:  catalog.BookCount,
		Confidence:          1,
		LinkedBy:            "manual",
	}); err != nil {
		t.Fatal(err)
	}

	rec := httptest.NewRecorder()
	h.Fill(rec, withURLParam(httptest.NewRequest(http.MethodPost, "/api/v1/series/1/fill", nil), "id", strconv.FormatInt(series.ID, 10)))
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	queued := searcher.waitForCall(t, time.Second)
	if queued.ASIN != audioASIN {
		t.Fatalf("queued book ASIN = %q, want %q", queued.ASIN, audioASIN)
	}
	created, err := bookRepo.GetByForeignID(context.Background(), "hc:the-way-of-kings")
	if err != nil {
		t.Fatal(err)
	}
	if created == nil || created.ASIN != audioASIN {
		t.Fatalf("created book ASIN not persisted: %+v", created)
	}
	editions, err := editionRepo.ListByBook(context.Background(), created.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(editions) != 1 || editions[0].ForeignID != "hc:stormlight-audio" {
		t.Fatalf("expected hydrated edition, got %+v", editions)
	}
}

// TestSeriesFillKeepsSelectedFormat pins #1802: the format dropdown on the
// Series tab constrains what the fill creates, so a book created for "ebook"
// must stay ebook even when Hardcover offers an audio edition for the same
// work. Before the fix, hydration widened it to "both" and the fill queued a
// grab for both formats. Covers both fill shapes, "add all" and one row.
func TestSeriesFillKeepsSelectedFormat(t *testing.T) {
	cases := []struct {
		name string
		body string
	}{
		{name: "add all", body: `{"mediaType":"ebook"}`},
		{name: "single book", body: `{"foreignBookId":"hc:the-way-of-kings","mediaType":"ebook"}`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			catalog := stormlightCatalog()
			searcher := newMockBookSearcher()
			h, seriesRepo, _, bookRepo, _ := seriesFixtureWithProviderAndEditions(t, &stubSeriesProvider{
				catalogs: map[string]*metadata.SeriesCatalog{catalog.ForeignID: catalog},
			}, searcher)
			audioASIN := "B000STORML"
			h.WithEditionFetcher(func(context.Context, string) ([]models.Edition, error) {
				return []models.Edition{{
					ForeignID: "hc:stormlight-audio",
					Title:     "The Way of Kings",
					ASIN:      &audioASIN,
					Format:    "Audiobook",
					Monitored: true,
				}}, nil
			})
			series := &models.Series{ForeignID: "ol-series:stormlight", Title: "The Stormlight Archive"}
			if err := seriesRepo.Create(context.Background(), series); err != nil {
				t.Fatal(err)
			}
			if err := seriesRepo.UpsertHardcoverLink(context.Background(), &models.SeriesHardcoverLink{
				SeriesID:            series.ID,
				HardcoverSeriesID:   catalog.ForeignID,
				HardcoverProviderID: catalog.ProviderID,
				HardcoverTitle:      catalog.Title,
				HardcoverAuthorName: catalog.AuthorName,
				HardcoverBookCount:  catalog.BookCount,
				Confidence:          1,
				LinkedBy:            "manual",
			}); err != nil {
				t.Fatal(err)
			}

			rec := httptest.NewRecorder()
			req := httptest.NewRequest(http.MethodPost, "/api/v1/series/1/fill", bytes.NewBufferString(tc.body))
			h.Fill(rec, withURLParam(req, "id", strconv.FormatInt(series.ID, 10)))
			if rec.Code != http.StatusOK {
				t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
			}

			queued := searcher.waitForCall(t, time.Second)
			if queued.MediaType != models.MediaTypeEbook {
				t.Errorf("queued book mediaType = %q, want %q", queued.MediaType, models.MediaTypeEbook)
			}
			if queued.NeedsAudiobook() {
				t.Error("queued book still wants an audiobook; the fill will grab both formats")
			}
			created, err := bookRepo.GetByForeignID(context.Background(), "hc:the-way-of-kings")
			if err != nil {
				t.Fatal(err)
			}
			if created == nil {
				t.Fatal("expected the Hardcover book to be created")
			}
			if created.MediaType != models.MediaTypeEbook {
				t.Errorf("persisted mediaType = %q, want %q", created.MediaType, models.MediaTypeEbook)
			}
		})
	}
}

func TestSeriesFillReusesCrossProviderAuthorAndExistingBook(t *testing.T) {
	catalog := stormlightCatalog()
	searcher := newMockBookSearcher()
	h, seriesRepo, authorRepo, bookRepo := seriesFixtureWithProvider(t, &stubSeriesProvider{
		catalogs: map[string]*metadata.SeriesCatalog{catalog.ForeignID: catalog},
	}, searcher)
	ctx := context.Background()
	series := &models.Series{ForeignID: "ol-series:stormlight", Title: "The Stormlight Archive"}
	if err := seriesRepo.Create(ctx, series); err != nil {
		t.Fatal(err)
	}
	if err := seriesRepo.UpsertHardcoverLink(ctx, &models.SeriesHardcoverLink{
		SeriesID:            series.ID,
		HardcoverSeriesID:   catalog.ForeignID,
		HardcoverProviderID: catalog.ProviderID,
		HardcoverTitle:      catalog.Title,
		HardcoverAuthorName: catalog.AuthorName,
		HardcoverBookCount:  catalog.BookCount,
		Confidence:          1,
		LinkedBy:            "manual",
	}); err != nil {
		t.Fatal(err)
	}
	author := &models.Author{
		ForeignID:        "ol:brandon-sanderson",
		Name:             "Brandon Sanderson",
		SortName:         "Sanderson, Brandon",
		MetadataProvider: "openlibrary",
	}
	if err := authorRepo.Create(ctx, author); err != nil {
		t.Fatal(err)
	}
	linkedLocalBook := &models.Book{
		ForeignID:        "ol:words-of-radiance",
		AuthorID:         author.ID,
		Title:            "Words of Radiance",
		SortTitle:        "Words of Radiance",
		Status:           models.BookStatusImported,
		Genres:           []string{},
		MetadataProvider: "openlibrary",
	}
	if err := bookRepo.Create(ctx, linkedLocalBook); err != nil {
		t.Fatal(err)
	}
	if _, err := seriesRepo.LinkBookIfMissing(ctx, series.ID, linkedLocalBook.ID, "2", true); err != nil {
		t.Fatal(err)
	}
	existingBook := &models.Book{
		ForeignID:        "ol:the-way-of-kings",
		AuthorID:         author.ID,
		Title:            "The Way of Kings",
		SortTitle:        "The Way of Kings",
		Status:           models.BookStatusSkipped,
		Genres:           []string{},
		MetadataProvider: "openlibrary",
	}
	if err := bookRepo.Create(ctx, existingBook); err != nil {
		t.Fatal(err)
	}

	rec := httptest.NewRecorder()
	h.Fill(rec, withURLParam(httptest.NewRequest(http.MethodPost, "/api/v1/series/1/fill", nil), "id", strconv.FormatInt(series.ID, 10)))
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	var response map[string]int
	if err := json.NewDecoder(rec.Body).Decode(&response); err != nil {
		t.Fatal(err)
	}
	if response["queued"] != 1 {
		t.Fatalf("expected one queued existing book, got %+v", response)
	}
	queued := searcher.waitForCall(t, time.Second)
	if queued.ID != existingBook.ID {
		t.Fatalf("expected existing cross-provider book to be queued, got %+v", queued)
	}
	if hcAuthor, err := authorRepo.GetByForeignID(ctx, "hc:brandon-sanderson"); err != nil || hcAuthor != nil {
		t.Fatalf("expected no duplicate Hardcover author, got author=%+v err=%v", hcAuthor, err)
	}
	if duplicate, err := bookRepo.GetByForeignID(ctx, "hc:the-way-of-kings"); err != nil || duplicate != nil {
		t.Fatalf("expected no duplicate Hardcover book, got book=%+v err=%v", duplicate, err)
	}
	books, err := seriesRepo.ListBooksInSeries(ctx, series.ID)
	if err != nil {
		t.Fatal(err)
	}
	foundExisting := false
	for _, book := range books {
		if book.ID == existingBook.ID {
			foundExisting = true
		}
	}
	if !foundExisting {
		t.Fatalf("expected existing book linked to series, got %+v", books)
	}
}

// TestSeriesFillPrefersExactTitleOverOmnibusOnScoreTie is the regression
// test for the live-observed bug where Fill matched a box-set/omnibus title
// to a catalog slot instead of the real single-book title it happened to
// contain. TitleScore's PartialRatio/TokenSetRatio components score a
// substring match as a perfect 100 — the same score an exact match gets —
// so the omnibus and the real book tie. The book is created deliberately
// BEFORE the omnibus does not exist here: the omnibus is created first (so
// it gets the lower ID and would win under first-match-wins iteration
// order, matching the real repro), then the exact match second. Fill must
// still queue and link the real book, not the omnibus.
func TestSeriesFillPrefersExactTitleOverOmnibusOnScoreTie(t *testing.T) {
	catalog := stormlightCatalog()
	searcher := newMockBookSearcher()
	h, seriesRepo, authorRepo, bookRepo := seriesFixtureWithProvider(t, &stubSeriesProvider{
		catalogs: map[string]*metadata.SeriesCatalog{catalog.ForeignID: catalog},
	}, searcher)
	ctx := context.Background()
	series := &models.Series{ForeignID: "ol-series:stormlight", Title: "The Stormlight Archive"}
	if err := seriesRepo.Create(ctx, series); err != nil {
		t.Fatal(err)
	}
	if err := seriesRepo.UpsertHardcoverLink(ctx, &models.SeriesHardcoverLink{
		SeriesID:            series.ID,
		HardcoverSeriesID:   catalog.ForeignID,
		HardcoverProviderID: catalog.ProviderID,
		HardcoverTitle:      catalog.Title,
		HardcoverAuthorName: catalog.AuthorName,
		HardcoverBookCount:  catalog.BookCount,
		Confidence:          1,
		LinkedBy:            "manual",
	}); err != nil {
		t.Fatal(err)
	}
	author := &models.Author{
		ForeignID:        "ol:brandon-sanderson",
		Name:             "Brandon Sanderson",
		SortName:         "Sanderson, Brandon",
		MetadataProvider: "openlibrary",
	}
	if err := authorRepo.Create(ctx, author); err != nil {
		t.Fatal(err)
	}
	// Created first so it has the lower ID, matching the real repro's
	// iteration order.
	omnibus := &models.Book{
		ForeignID:        "ol:stormlight-boxed-set",
		AuthorID:         author.ID,
		Title:            "The Stormlight Archive Boxed Set: The Way of Kings, Words of Radiance, Oathbringer",
		SortTitle:        "stormlight archive boxed set the way of kings words of radiance oathbringer",
		Status:           models.BookStatusWanted,
		Genres:           []string{},
		MetadataProvider: "openlibrary",
	}
	if err := bookRepo.Create(ctx, omnibus); err != nil {
		t.Fatal(err)
	}
	exactMatch := &models.Book{
		ForeignID:        "ol:the-way-of-kings",
		AuthorID:         author.ID,
		Title:            "The Way of Kings",
		SortTitle:        "The Way of Kings",
		Status:           models.BookStatusWanted,
		Genres:           []string{},
		MetadataProvider: "openlibrary",
	}
	if err := bookRepo.Create(ctx, exactMatch); err != nil {
		t.Fatal(err)
	}

	rec := httptest.NewRecorder()
	h.Fill(rec, withURLParam(httptest.NewRequest(http.MethodPost, "/api/v1/series/1/fill", nil), "id", strconv.FormatInt(series.ID, 10)))
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	var response map[string]int
	if err := json.NewDecoder(rec.Body).Decode(&response); err != nil {
		t.Fatal(err)
	}
	if response["queued"] != 1 {
		t.Fatalf("expected exactly one queued book, got %+v", response)
	}
	queued := searcher.waitForCall(t, time.Second)
	if queued.ID != exactMatch.ID {
		t.Fatalf("expected the exact-title match (id=%d) to be queued, got id=%d title=%q",
			exactMatch.ID, queued.ID, queued.Title)
	}

	books, err := seriesRepo.ListBooksInSeries(ctx, series.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(books) != 1 || books[0].ID != exactMatch.ID {
		t.Fatalf("expected only the exact-title match linked to the series, got %+v", books)
	}
}

// TestSeriesFillPrefersSuffixedRealTitleOverShorterUnrelatedTitle covers the
// inverted shape of TestSeriesFillPrefersExactTitleOverOmnibusOnScoreTie
// (maintainer review, PR #1969): a naive length-closeness tiebreak assumes
// the wrong candidate is always the LONGER one (an omnibus containing the
// target as a substring), but a real local title routinely carries its own
// appended qualifier from Calibre/ABS scanning ("The Way of Kings: The
// Stormlight Archive, Book One") and can end up longer than a short,
// unrelated title ("Kings") that merely shares a word with the target. Both
// tie at TitleScore 100 against target "The Way of Kings"; length-closeness
// alone would pick "Kings" (closer in length to the target) over the real
// match.
func TestSeriesFillPrefersSuffixedRealTitleOverShorterUnrelatedTitle(t *testing.T) {
	catalog := stormlightCatalog()
	searcher := newMockBookSearcher()
	h, seriesRepo, authorRepo, bookRepo := seriesFixtureWithProvider(t, &stubSeriesProvider{
		catalogs: map[string]*metadata.SeriesCatalog{catalog.ForeignID: catalog},
	}, searcher)
	ctx := context.Background()
	series := &models.Series{ForeignID: "ol-series:stormlight", Title: "The Stormlight Archive"}
	if err := seriesRepo.Create(ctx, series); err != nil {
		t.Fatal(err)
	}
	if err := seriesRepo.UpsertHardcoverLink(ctx, &models.SeriesHardcoverLink{
		SeriesID:            series.ID,
		HardcoverSeriesID:   catalog.ForeignID,
		HardcoverProviderID: catalog.ProviderID,
		HardcoverTitle:      catalog.Title,
		HardcoverAuthorName: catalog.AuthorName,
		HardcoverBookCount:  catalog.BookCount,
		Confidence:          1,
		LinkedBy:            "manual",
	}); err != nil {
		t.Fatal(err)
	}
	author := &models.Author{
		ForeignID:        "ol:brandon-sanderson",
		Name:             "Brandon Sanderson",
		SortName:         "Sanderson, Brandon",
		MetadataProvider: "openlibrary",
	}
	if err := authorRepo.Create(ctx, author); err != nil {
		t.Fatal(err)
	}
	// Created first so it has the lower ID, matching the real repro's
	// iteration order — first-match-wins would pick this one.
	unrelated := &models.Book{
		ForeignID:        "ol:kings",
		AuthorID:         author.ID,
		Title:            "Kings",
		SortTitle:        "kings",
		Status:           models.BookStatusWanted,
		Genres:           []string{},
		MetadataProvider: "openlibrary",
	}
	if err := bookRepo.Create(ctx, unrelated); err != nil {
		t.Fatal(err)
	}
	suffixedRealMatch := &models.Book{
		ForeignID:        "ol:the-way-of-kings-suffixed",
		AuthorID:         author.ID,
		Title:            "The Way of Kings: The Stormlight Archive, Book One",
		SortTitle:        "the way of kings the stormlight archive book one",
		Status:           models.BookStatusWanted,
		Genres:           []string{},
		MetadataProvider: "openlibrary",
	}
	if err := bookRepo.Create(ctx, suffixedRealMatch); err != nil {
		t.Fatal(err)
	}

	rec := httptest.NewRecorder()
	h.Fill(rec, withURLParam(httptest.NewRequest(http.MethodPost, "/api/v1/series/1/fill", nil), "id", strconv.FormatInt(series.ID, 10)))
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	var response map[string]int
	if err := json.NewDecoder(rec.Body).Decode(&response); err != nil {
		t.Fatal(err)
	}
	if response["queued"] != 1 {
		t.Fatalf("expected exactly one queued book, got %+v", response)
	}
	queued := searcher.waitForCall(t, time.Second)
	if queued.ID != suffixedRealMatch.ID {
		t.Fatalf("expected the suffixed real match (id=%d) to be queued, got id=%d title=%q",
			suffixedRealMatch.ID, queued.ID, queued.Title)
	}

	books, err := seriesRepo.ListBooksInSeries(ctx, series.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(books) != 1 || books[0].ID != suffixedRealMatch.ID {
		t.Fatalf("expected only the suffixed real match linked to the series, got %+v", books)
	}
}

func TestSeriesFillSkipsExcludedHardcoverForeignIDMatch(t *testing.T) {
	catalog := stormlightCatalog()
	searcher := newMockBookSearcher()
	h, seriesRepo, authorRepo, bookRepo := seriesFixtureWithProvider(t, &stubSeriesProvider{
		catalogs: map[string]*metadata.SeriesCatalog{catalog.ForeignID: catalog},
	}, searcher)
	ctx := context.Background()
	series := &models.Series{ForeignID: "ol-series:stormlight", Title: "The Stormlight Archive"}
	if err := seriesRepo.Create(ctx, series); err != nil {
		t.Fatal(err)
	}
	link := &models.SeriesHardcoverLink{
		SeriesID:            series.ID,
		HardcoverSeriesID:   catalog.ForeignID,
		HardcoverProviderID: catalog.ProviderID,
		HardcoverTitle:      catalog.Title,
		HardcoverAuthorName: catalog.AuthorName,
		HardcoverBookCount:  catalog.BookCount,
		Confidence:          1,
		LinkedBy:            "manual",
	}
	if err := seriesRepo.UpsertHardcoverLink(ctx, link); err != nil {
		t.Fatal(err)
	}
	author := &models.Author{
		ForeignID:        "hc:brandon-sanderson",
		Name:             "Brandon Sanderson",
		SortName:         "Sanderson, Brandon",
		MetadataProvider: "hardcover",
	}
	if err := authorRepo.Create(ctx, author); err != nil {
		t.Fatal(err)
	}
	book := &models.Book{
		ForeignID:        "hc:the-way-of-kings",
		AuthorID:         author.ID,
		Title:            "The Way of Kings",
		SortTitle:        "The Way of Kings",
		Status:           models.BookStatusSkipped,
		Genres:           []string{},
		MetadataProvider: "hardcover",
	}
	if err := bookRepo.Create(ctx, book); err != nil {
		t.Fatal(err)
	}
	if err := bookRepo.SetExcluded(ctx, book.ID, true); err != nil {
		t.Fatal(err)
	}

	body := bytes.NewBufferString(`{"foreignBookId":"hc:the-way-of-kings","providerId":"101","position":"1"}`)
	rec := httptest.NewRecorder()
	h.Fill(rec, withURLParam(httptest.NewRequest(http.MethodPost, "/api/v1/series/1/fill", body), "id", strconv.FormatInt(series.ID, 10)))
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	var response map[string]int
	if err := json.NewDecoder(rec.Body).Decode(&response); err != nil {
		t.Fatal(err)
	}
	if response["queued"] != 0 {
		t.Fatalf("expected no queued excluded book, got %+v", response)
	}
	searcher.assertNoCall(t, 50*time.Millisecond)
	books, err := seriesRepo.ListBooksInSeries(ctx, series.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(books) != 0 {
		t.Fatalf("expected excluded foreign-id match to remain unlinked, got %+v", books)
	}
}

func TestSeriesFillSkipsExcludedHardcoverTitleMatch(t *testing.T) {
	catalog := stormlightCatalog()
	catalog.Books[0].ForeignID = "hc:the-way-of-kings-new"
	catalog.Books[0].Book.ForeignID = "hc:the-way-of-kings-new"
	searcher := newMockBookSearcher()
	h, seriesRepo, authorRepo, bookRepo := seriesFixtureWithProvider(t, &stubSeriesProvider{
		catalogs: map[string]*metadata.SeriesCatalog{catalog.ForeignID: catalog},
	}, searcher)
	ctx := context.Background()
	series := &models.Series{ForeignID: "ol-series:stormlight", Title: "The Stormlight Archive"}
	if err := seriesRepo.Create(ctx, series); err != nil {
		t.Fatal(err)
	}
	link := &models.SeriesHardcoverLink{
		SeriesID:            series.ID,
		HardcoverSeriesID:   catalog.ForeignID,
		HardcoverProviderID: catalog.ProviderID,
		HardcoverTitle:      catalog.Title,
		HardcoverAuthorName: catalog.AuthorName,
		HardcoverBookCount:  catalog.BookCount,
		Confidence:          1,
		LinkedBy:            "manual",
	}
	if err := seriesRepo.UpsertHardcoverLink(ctx, link); err != nil {
		t.Fatal(err)
	}
	author := &models.Author{
		ForeignID:        "hc:brandon-sanderson",
		Name:             "Brandon Sanderson",
		SortName:         "Sanderson, Brandon",
		MetadataProvider: "hardcover",
	}
	if err := authorRepo.Create(ctx, author); err != nil {
		t.Fatal(err)
	}
	book := &models.Book{
		ForeignID:        "manual:excluded-way-of-kings",
		AuthorID:         author.ID,
		Title:            "The Way of Kings",
		SortTitle:        "The Way of Kings",
		Status:           models.BookStatusSkipped,
		Genres:           []string{},
		MetadataProvider: "manual",
	}
	if err := bookRepo.Create(ctx, book); err != nil {
		t.Fatal(err)
	}
	if err := bookRepo.SetExcluded(ctx, book.ID, true); err != nil {
		t.Fatal(err)
	}

	body := bytes.NewBufferString(`{"foreignBookId":"hc:the-way-of-kings-new","providerId":"101","position":"1"}`)
	rec := httptest.NewRecorder()
	h.Fill(rec, withURLParam(httptest.NewRequest(http.MethodPost, "/api/v1/series/1/fill", body), "id", strconv.FormatInt(series.ID, 10)))
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	var response map[string]int
	if err := json.NewDecoder(rec.Body).Decode(&response); err != nil {
		t.Fatal(err)
	}
	if response["queued"] != 0 {
		t.Fatalf("expected no queued excluded title match, got %+v", response)
	}
	searcher.assertNoCall(t, 50*time.Millisecond)
	created, err := bookRepo.GetByForeignID(ctx, "hc:the-way-of-kings-new")
	if err != nil {
		t.Fatal(err)
	}
	if created != nil {
		t.Fatalf("expected excluded title match to block duplicate creation, got %+v", created)
	}
	books, err := seriesRepo.ListBooksInSeries(ctx, series.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(books) != 0 {
		t.Fatalf("expected excluded title match to remain unlinked, got %+v", books)
	}
}

// TestSeriesFillLinksRealMatchDespiteEarlierExcludedTitle covers the scan-order
// finding from review: since the loop now scans every candidate instead of
// stopping at the first match, an excluded candidate encountered before a
// real (non-excluded) higher-or-equal-scoring one must not block the real
// match from being linked. blockedByExcludedTitle is only meaningful when
// nothing has matched, so a real match found later in the scan must win.
func TestSeriesFillLinksRealMatchDespiteEarlierExcludedTitle(t *testing.T) {
	catalog := stormlightCatalog()
	catalog.Books[0].ForeignID = "hc:the-way-of-kings-new"
	catalog.Books[0].Book.ForeignID = "hc:the-way-of-kings-new"
	searcher := newMockBookSearcher()
	h, seriesRepo, authorRepo, bookRepo := seriesFixtureWithProvider(t, &stubSeriesProvider{
		catalogs: map[string]*metadata.SeriesCatalog{catalog.ForeignID: catalog},
	}, searcher)
	ctx := context.Background()
	series := &models.Series{ForeignID: "ol-series:stormlight", Title: "The Stormlight Archive"}
	if err := seriesRepo.Create(ctx, series); err != nil {
		t.Fatal(err)
	}
	link := &models.SeriesHardcoverLink{
		SeriesID:            series.ID,
		HardcoverSeriesID:   catalog.ForeignID,
		HardcoverProviderID: catalog.ProviderID,
		HardcoverTitle:      catalog.Title,
		HardcoverAuthorName: catalog.AuthorName,
		HardcoverBookCount:  catalog.BookCount,
		Confidence:          1,
		LinkedBy:            "manual",
	}
	if err := seriesRepo.UpsertHardcoverLink(ctx, link); err != nil {
		t.Fatal(err)
	}
	author := &models.Author{
		ForeignID:        "hc:brandon-sanderson",
		Name:             "Brandon Sanderson",
		SortName:         "Sanderson, Brandon",
		MetadataProvider: "hardcover",
	}
	if err := authorRepo.Create(ctx, author); err != nil {
		t.Fatal(err)
	}
	// Excluded candidate created first, so ListByAuthorIncludingExcluded
	// returns it before the real match below — this is what makes the
	// scan-order case reachable: blockedByExcludedTitle gets set while
	// best is still nil, and only afterward does the real match arrive.
	excluded := &models.Book{
		ForeignID:        "manual:excluded-way-of-kings",
		AuthorID:         author.ID,
		Title:            "The Way of Kings",
		SortTitle:        "The Way of Kings",
		Status:           models.BookStatusSkipped,
		Genres:           []string{},
		MetadataProvider: "manual",
	}
	if err := bookRepo.Create(ctx, excluded); err != nil {
		t.Fatal(err)
	}
	if err := bookRepo.SetExcluded(ctx, excluded.ID, true); err != nil {
		t.Fatal(err)
	}
	real := &models.Book{
		ForeignID:        "ol:the-way-of-kings",
		AuthorID:         author.ID,
		Title:            "The Way of Kings",
		SortTitle:        "The Way of Kings",
		Status:           models.BookStatusSkipped,
		Genres:           []string{},
		MetadataProvider: "openlibrary",
	}
	if err := bookRepo.Create(ctx, real); err != nil {
		t.Fatal(err)
	}

	body := bytes.NewBufferString(`{"foreignBookId":"hc:the-way-of-kings-new","providerId":"101","position":"1"}`)
	rec := httptest.NewRecorder()
	h.Fill(rec, withURLParam(httptest.NewRequest(http.MethodPost, "/api/v1/series/1/fill", body), "id", strconv.FormatInt(series.ID, 10)))
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	var response map[string]int
	if err := json.NewDecoder(rec.Body).Decode(&response); err != nil {
		t.Fatal(err)
	}
	if response["queued"] != 1 {
		t.Fatalf("expected the real match to be queued despite an earlier excluded candidate, got %+v", response)
	}
	queued := searcher.waitForCall(t, time.Second)
	if queued.ID != real.ID {
		t.Fatalf("expected the real (non-excluded) book to be linked, got %+v", queued)
	}
	if duplicate, err := bookRepo.GetByForeignID(ctx, "hc:the-way-of-kings-new"); err != nil || duplicate != nil {
		t.Fatalf("expected no duplicate Hardcover book created, got book=%+v err=%v", duplicate, err)
	}
	books, err := seriesRepo.ListBooksInSeries(ctx, series.ID)
	if err != nil {
		t.Fatal(err)
	}
	foundReal := false
	for _, b := range books {
		if b.ID == real.ID {
			foundReal = true
		}
	}
	if !foundReal {
		t.Fatalf("expected the real book linked to series, got %+v", books)
	}
}

func TestSeriesFillSkipsExcludedCrossProviderTitleMatch(t *testing.T) {
	catalog := stormlightCatalog()
	catalog.Books[0].ForeignID = "hc:the-way-of-kings-new"
	catalog.Books[0].Book.ForeignID = "hc:the-way-of-kings-new"
	searcher := newMockBookSearcher()
	h, seriesRepo, authorRepo, bookRepo := seriesFixtureWithProvider(t, &stubSeriesProvider{
		catalogs: map[string]*metadata.SeriesCatalog{catalog.ForeignID: catalog},
	}, searcher)
	ctx := context.Background()
	series := &models.Series{ForeignID: "ol-series:stormlight", Title: "The Stormlight Archive"}
	if err := seriesRepo.Create(ctx, series); err != nil {
		t.Fatal(err)
	}
	if err := seriesRepo.UpsertHardcoverLink(ctx, &models.SeriesHardcoverLink{
		SeriesID:            series.ID,
		HardcoverSeriesID:   catalog.ForeignID,
		HardcoverProviderID: catalog.ProviderID,
		HardcoverTitle:      catalog.Title,
		HardcoverAuthorName: catalog.AuthorName,
		HardcoverBookCount:  catalog.BookCount,
		Confidence:          1,
		LinkedBy:            "manual",
	}); err != nil {
		t.Fatal(err)
	}
	author := &models.Author{
		ForeignID:        "ol:brandon-sanderson",
		Name:             "Brandon Sanderson",
		SortName:         "Sanderson, Brandon",
		MetadataProvider: "openlibrary",
	}
	if err := authorRepo.Create(ctx, author); err != nil {
		t.Fatal(err)
	}
	linkedLocalBook := &models.Book{
		ForeignID:        "ol:words-of-radiance",
		AuthorID:         author.ID,
		Title:            "Words of Radiance",
		SortTitle:        "Words of Radiance",
		Status:           models.BookStatusImported,
		Genres:           []string{},
		MetadataProvider: "openlibrary",
	}
	if err := bookRepo.Create(ctx, linkedLocalBook); err != nil {
		t.Fatal(err)
	}
	if _, err := seriesRepo.LinkBookIfMissing(ctx, series.ID, linkedLocalBook.ID, "2", true); err != nil {
		t.Fatal(err)
	}
	excludedBook := &models.Book{
		ForeignID:        "ol:the-way-of-kings",
		AuthorID:         author.ID,
		Title:            "The Way of Kings",
		SortTitle:        "The Way of Kings",
		Status:           models.BookStatusSkipped,
		Genres:           []string{},
		MetadataProvider: "openlibrary",
	}
	if err := bookRepo.Create(ctx, excludedBook); err != nil {
		t.Fatal(err)
	}
	if err := bookRepo.SetExcluded(ctx, excludedBook.ID, true); err != nil {
		t.Fatal(err)
	}

	rec := httptest.NewRecorder()
	h.Fill(rec, withURLParam(httptest.NewRequest(http.MethodPost, "/api/v1/series/1/fill", nil), "id", strconv.FormatInt(series.ID, 10)))
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	var response map[string]int
	if err := json.NewDecoder(rec.Body).Decode(&response); err != nil {
		t.Fatal(err)
	}
	if response["queued"] != 0 {
		t.Fatalf("expected no queued excluded book, got %+v", response)
	}
	searcher.assertNoCall(t, 50*time.Millisecond)
	if hcAuthor, err := authorRepo.GetByForeignID(ctx, "hc:brandon-sanderson"); err != nil || hcAuthor != nil {
		t.Fatalf("expected no duplicate Hardcover author, got author=%+v err=%v", hcAuthor, err)
	}
	if created, err := bookRepo.GetByForeignID(ctx, "hc:the-way-of-kings-new"); err != nil || created != nil {
		t.Fatalf("expected no duplicate Hardcover book, got book=%+v err=%v", created, err)
	}
	books, err := seriesRepo.ListBooksInSeries(ctx, series.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(books) != 1 || books[0].ID != linkedLocalBook.ID {
		t.Fatalf("expected only existing local series book to remain linked, got %+v", books)
	}
}

func TestSeriesFillSkipsAmbiguousCrossProviderAuthorMatch(t *testing.T) {
	catalog := stormlightCatalog()
	searcher := newMockBookSearcher()
	h, seriesRepo, authorRepo, bookRepo := seriesFixtureWithProvider(t, &stubSeriesProvider{
		catalogs: map[string]*metadata.SeriesCatalog{catalog.ForeignID: catalog},
	}, searcher)
	ctx := context.Background()
	series := &models.Series{ForeignID: "ol-series:stormlight", Title: "The Stormlight Archive"}
	if err := seriesRepo.Create(ctx, series); err != nil {
		t.Fatal(err)
	}
	if err := seriesRepo.UpsertHardcoverLink(ctx, &models.SeriesHardcoverLink{
		SeriesID:            series.ID,
		HardcoverSeriesID:   catalog.ForeignID,
		HardcoverProviderID: catalog.ProviderID,
		HardcoverTitle:      catalog.Title,
		HardcoverAuthorName: catalog.AuthorName,
		HardcoverBookCount:  catalog.BookCount,
		Confidence:          1,
		LinkedBy:            "manual",
	}); err != nil {
		t.Fatal(err)
	}
	for _, foreignID := range []string{"ol:brandon-sanderson", "manual:brandon-sanderson"} {
		author := &models.Author{
			ForeignID:        foreignID,
			Name:             "Brandon Sanderson",
			SortName:         "Sanderson, Brandon",
			MetadataProvider: "manual",
		}
		if err := authorRepo.Create(ctx, author); err != nil {
			t.Fatal(err)
		}
	}

	rec := httptest.NewRecorder()
	h.Fill(rec, withURLParam(httptest.NewRequest(http.MethodPost, "/api/v1/series/1/fill", nil), "id", strconv.FormatInt(series.ID, 10)))
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	var response map[string]int
	if err := json.NewDecoder(rec.Body).Decode(&response); err != nil {
		t.Fatal(err)
	}
	if response["queued"] != 0 {
		t.Fatalf("expected no queued ambiguous author match, got %+v", response)
	}
	searcher.assertNoCall(t, 50*time.Millisecond)
	if hcAuthor, err := authorRepo.GetByForeignID(ctx, "hc:brandon-sanderson"); err != nil || hcAuthor != nil {
		t.Fatalf("expected no duplicate Hardcover author, got author=%+v err=%v", hcAuthor, err)
	}
	if created, err := bookRepo.GetByForeignID(ctx, "hc:the-way-of-kings"); err != nil || created != nil {
		t.Fatalf("expected no Hardcover book creation, got book=%+v err=%v", created, err)
	}
	books, err := seriesRepo.ListBooksInSeries(ctx, series.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(books) != 0 {
		t.Fatalf("expected no series links, got %+v", books)
	}
}

func TestSeriesFillCreatesOnlyRequestedHardcoverBook(t *testing.T) {
	catalog := stormlightCatalog()
	catalog.Books = append(catalog.Books, metadata.SeriesCatalogBook{
		ForeignID:  "hc:words-of-radiance",
		ProviderID: "102",
		Title:      "Words of Radiance",
		Position:   "2",
		UsersCount: 456,
		Book: models.Book{
			ForeignID:        "hc:words-of-radiance",
			Title:            "Words of Radiance",
			SortTitle:        "Words of Radiance",
			MetadataProvider: "hardcover",
			Author:           catalog.Books[0].Book.Author,
		},
	})
	catalog.BookCount = len(catalog.Books)
	searcher := newMockBookSearcher()
	h, seriesRepo, _, bookRepo := seriesFixtureWithProvider(t, &stubSeriesProvider{
		catalogs: map[string]*metadata.SeriesCatalog{catalog.ForeignID: catalog},
	}, searcher)
	series := &models.Series{ForeignID: "ol-series:stormlight", Title: "The Stormlight Archive"}
	if err := seriesRepo.Create(context.Background(), series); err != nil {
		t.Fatal(err)
	}
	link := &models.SeriesHardcoverLink{
		SeriesID:            series.ID,
		HardcoverSeriesID:   catalog.ForeignID,
		HardcoverProviderID: catalog.ProviderID,
		HardcoverTitle:      catalog.Title,
		HardcoverAuthorName: catalog.AuthorName,
		HardcoverBookCount:  catalog.BookCount,
		Confidence:          1,
		LinkedBy:            "manual",
	}
	if err := seriesRepo.UpsertHardcoverLink(context.Background(), link); err != nil {
		t.Fatal(err)
	}

	body := bytes.NewBufferString(`{"foreignBookId":"hc:words-of-radiance","providerId":"102","position":"2"}`)
	rec := httptest.NewRecorder()
	h.Fill(rec, withURLParam(httptest.NewRequest(http.MethodPost, "/api/v1/series/1/fill", body), "id", "1"))
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	var response map[string]int
	if err := json.NewDecoder(rec.Body).Decode(&response); err != nil {
		t.Fatal(err)
	}
	if response["queued"] != 1 {
		t.Fatalf("expected one queued book, got %+v", response)
	}
	queued := searcher.waitForCall(t, time.Second)
	if queued.Title != "Words of Radiance" {
		t.Fatalf("unexpected queued book: %+v", queued)
	}
	searcher.assertNoCall(t, 50*time.Millisecond)
	created, err := bookRepo.GetByForeignID(context.Background(), "hc:words-of-radiance")
	if err != nil {
		t.Fatal(err)
	}
	if created == nil {
		t.Fatal("expected requested Hardcover book to be created")
	}
	notCreated, err := bookRepo.GetByForeignID(context.Background(), "hc:the-way-of-kings")
	if err != nil {
		t.Fatal(err)
	}
	if notCreated != nil {
		t.Fatalf("expected unrequested Hardcover book to remain missing, got %+v", notCreated)
	}
	books, err := seriesRepo.ListBooksInSeries(context.Background(), series.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(books) != 1 || books[0].ForeignID != "hc:words-of-radiance" {
		t.Fatalf("expected only requested book linked to series, got %+v", books)
	}
}

func TestSeriesFillQueuesLocalBooksWhenHardcoverCatalogFails(t *testing.T) {
	catalog := stormlightCatalog()
	searcher := newMockBookSearcher()
	h, seriesRepo, authorRepo, bookRepo := seriesFixtureWithProvider(t, &stubSeriesProvider{
		catalogs:   map[string]*metadata.SeriesCatalog{catalog.ForeignID: catalog},
		catalogErr: errors.New("hardcover unavailable"),
	}, searcher)
	ctx := context.Background()
	series := &models.Series{ForeignID: "ol-series:stormlight", Title: "The Stormlight Archive"}
	if err := seriesRepo.Create(ctx, series); err != nil {
		t.Fatal(err)
	}
	link := &models.SeriesHardcoverLink{
		SeriesID:            series.ID,
		HardcoverSeriesID:   catalog.ForeignID,
		HardcoverProviderID: catalog.ProviderID,
		HardcoverTitle:      catalog.Title,
		HardcoverAuthorName: catalog.AuthorName,
		HardcoverBookCount:  catalog.BookCount,
		Confidence:          1,
		LinkedBy:            "manual",
	}
	if err := seriesRepo.UpsertHardcoverLink(ctx, link); err != nil {
		t.Fatal(err)
	}
	author := &models.Author{
		ForeignID:        "hc:brandon-sanderson",
		Name:             "Brandon Sanderson",
		SortName:         "Sanderson, Brandon",
		MetadataProvider: "hardcover",
		Monitored:        true,
	}
	if err := authorRepo.Create(ctx, author); err != nil {
		t.Fatal(err)
	}
	book := &models.Book{
		ForeignID:        "hc:words-of-radiance",
		AuthorID:         author.ID,
		Title:            "Words of Radiance",
		SortTitle:        "Words of Radiance",
		Status:           models.BookStatusSkipped,
		Genres:           []string{},
		MetadataProvider: "hardcover",
	}
	if err := bookRepo.Create(ctx, book); err != nil {
		t.Fatal(err)
	}
	if _, err := seriesRepo.LinkBookIfMissing(ctx, series.ID, book.ID, "2", true); err != nil {
		t.Fatal(err)
	}

	rec := httptest.NewRecorder()
	h.Fill(rec, withURLParam(httptest.NewRequest(http.MethodPost, "/api/v1/series/1/fill", nil), "id", "1"))
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	var body map[string]int
	if err := json.NewDecoder(rec.Body).Decode(&body); err != nil {
		t.Fatal(err)
	}
	if body["queued"] != 1 {
		t.Fatalf("expected one local book queued despite provider failure, got %+v", body)
	}
	queued := searcher.waitForCall(t, time.Second)
	if queued.ID != book.ID || queued.Title != "Words of Radiance" {
		t.Fatalf("unexpected queued book: %+v", queued)
	}
	updated, err := bookRepo.GetByID(ctx, book.ID)
	if err != nil {
		t.Fatal(err)
	}
	if updated == nil || updated.Status != models.BookStatusWanted || !updated.Monitored {
		t.Fatalf("expected local book marked wanted and monitored, got %+v", updated)
	}
}

func stormlightCatalog() *metadata.SeriesCatalog {
	book := models.Book{
		ForeignID:        "hc:the-way-of-kings",
		Title:            "The Way of Kings",
		SortTitle:        "The Way of Kings",
		MetadataProvider: "hardcover",
		Author: &models.Author{
			ForeignID:        "hc:brandon-sanderson",
			Name:             "Brandon Sanderson",
			SortName:         "Sanderson, Brandon",
			MetadataProvider: "hardcover",
		},
	}
	return &metadata.SeriesCatalog{
		ForeignID:  "hc-series:42",
		ProviderID: "42",
		Title:      "The Stormlight Archive",
		AuthorName: "Brandon Sanderson",
		BookCount:  1,
		Books: []metadata.SeriesCatalogBook{{
			ForeignID:  book.ForeignID,
			ProviderID: "101",
			Title:      book.Title,
			Position:   "1",
			UsersCount: 123,
			Book:       book,
		}},
	}
}

// TestSeriesHandler_LifetimeCtxFallsBackToBackground is the #846 follow-up
// guard for fanOutSeriesSearches. Same contract as BookHandler.bgCtx().
func TestSeriesHandler_LifetimeCtxFallsBackToBackground(t *testing.T) {
	h := &SeriesHandler{}
	if h.bgCtx() != context.Background() {
		t.Error("bgCtx without WithLifetimeCtx must return context.Background()")
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	h.WithLifetimeCtx(ctx)
	if h.bgCtx() != ctx {
		t.Error("bgCtx with WithLifetimeCtx must return the supplied ctx")
	}
	h.WithLifetimeCtx(nil) //nolint:staticcheck // SA1012 testing nil-tolerance contract
	if h.bgCtx() != ctx {
		t.Error("WithLifetimeCtx(nil) must not clobber a previously installed ctx")
	}
}

// lightNovelCatalog builds a Hardcover series catalog whose volume titles
// differ only by their number — the shape that broke in #1682.
func lightNovelCatalog() *metadata.SeriesCatalog {
	author := &models.Author{
		ForeignID:        "hc:mei-hachimoku",
		Name:             "Mei Hachimoku",
		SortName:         "Hachimoku, Mei",
		MetadataProvider: "hardcover",
	}
	var books []metadata.SeriesCatalogBook
	for i := 1; i <= 5; i++ {
		title := fmt.Sprintf("The Mimosa Confessions, Vol. %d", i)
		books = append(books, metadata.SeriesCatalogBook{
			ForeignID:  fmt.Sprintf("hc:mimosa-confessions-%d", i),
			ProviderID: strconv.Itoa(100 + i),
			Title:      title,
			Position:   strconv.Itoa(i),
			Book: models.Book{
				ForeignID:        fmt.Sprintf("hc:mimosa-confessions-%d", i),
				Title:            title,
				SortTitle:        title,
				MetadataProvider: "hardcover",
				Author:           author,
			},
		})
	}
	return &metadata.SeriesCatalog{
		ForeignID:  "hc-series:mimosa",
		ProviderID: "900",
		Title:      "The Mimosa Confessions",
		AuthorName: "Mei Hachimoku",
		BookCount:  len(books),
		Books:      books,
	}
}

// TestSeriesFillCreatesEveryVolumeOfALightNovelSeries is the #1682 regression
// test.
//
// ensureHardcoverCatalogBook treats a >=92 fuzzy title score against one of the
// author's existing books as "this is the same book" and links that row at the
// new position instead of creating one. Light novel volume titles differ by a
// single number, so they score 93-100 against each other: volume 1 was created,
// then volumes 2-5 each "matched" volume 1 and were linked to its row. "Add
// all" on a 5-volume series produced exactly one book, and on the reporter's
// 13-volume series, exactly one book.
func TestSeriesFillCreatesEveryVolumeOfALightNovelSeries(t *testing.T) {
	catalog := lightNovelCatalog()
	searcher := newMockBookSearcher()
	h, seriesRepo, _, bookRepo := seriesFixtureWithProvider(t, &stubSeriesProvider{
		catalogs: map[string]*metadata.SeriesCatalog{catalog.ForeignID: catalog},
	}, searcher)

	ctx := context.Background()
	series := &models.Series{ForeignID: "ol-series:mimosa", Title: "The Mimosa Confessions"}
	if err := seriesRepo.Create(ctx, series); err != nil {
		t.Fatal(err)
	}
	if err := seriesRepo.UpsertHardcoverLink(ctx, &models.SeriesHardcoverLink{
		SeriesID: series.ID, HardcoverSeriesID: catalog.ForeignID,
		HardcoverProviderID: catalog.ProviderID, HardcoverTitle: catalog.Title,
		HardcoverAuthorName: catalog.AuthorName, HardcoverBookCount: catalog.BookCount,
		Confidence: 1, LinkedBy: "manual",
	}); err != nil {
		t.Fatal(err)
	}

	rec := httptest.NewRecorder()
	h.Fill(rec, withURLParam(httptest.NewRequest(http.MethodPost, "/api/v1/series/1/fill", nil), "id", "1"))
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}

	// Every volume must exist as its own row, with its own foreign ID.
	for i := 1; i <= 5; i++ {
		fid := fmt.Sprintf("hc:mimosa-confessions-%d", i)
		created, err := bookRepo.GetByForeignID(ctx, fid)
		if err != nil {
			t.Fatal(err)
		}
		if created == nil {
			t.Errorf("volume %d (%s) was not created", i, fid)
			continue
		}
		if want := fmt.Sprintf("The Mimosa Confessions, Vol. %d", i); created.Title != want {
			t.Errorf("volume %d title = %q, want %q", i, created.Title, want)
		}
	}

	inSeries, err := seriesRepo.ListBooksInSeries(ctx, series.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(inSeries) != 5 {
		t.Fatalf("expected 5 distinct books linked to the series, got %d: %+v", len(inSeries), inSeries)
	}
	// Distinct rows, not one row linked five times at five positions.
	ids := map[int64]bool{}
	for _, b := range inSeries {
		if ids[b.ID] {
			t.Errorf("book id %d is linked to the series more than once — volumes collapsed onto one row", b.ID)
		}
		ids[b.ID] = true
	}
}

// TestSeriesFillContinuesPastASkippedVolume pins that a volume the catalog
// loop declines to create does not stop the volumes after it. Volume 3 already
// exists as an EXCLUDED row, so ensureHardcoverCatalogBook returns early for
// it; volumes 4 and 5 must still be created.
//
// NOTE ON SCOPE: this covers the SKIP path, which returns (nil, nil) and was
// already handled correctly. It does NOT exercise the neighbouring change in
// createMissingHardcoverBooks that turned a per-book `return err` into
// log-and-continue — every way that function can actually error is a database
// failure (ListByAuthorIncludingExcluded, books.Create, LinkBookIfMissing), and
// reaching one needs a deliberately broken *db.BookRepo, which the project's
// testing guidance rules out. That change is defensive and unverified; it is
// called out as such rather than dressed up in a test that passes either way.
func TestSeriesFillContinuesPastASkippedVolume(t *testing.T) {
	catalog := lightNovelCatalog()
	searcher := newMockBookSearcher()
	h, seriesRepo, authorRepo, bookRepo := seriesFixtureWithProvider(t, &stubSeriesProvider{
		catalogs: map[string]*metadata.SeriesCatalog{catalog.ForeignID: catalog},
	}, searcher)

	ctx := context.Background()
	author := &models.Author{
		ForeignID: "hc:mei-hachimoku", Name: "Mei Hachimoku",
		SortName: "Hachimoku, Mei", MetadataProvider: "hardcover",
	}
	if err := authorRepo.Create(ctx, author); err != nil {
		t.Fatal(err)
	}
	excluded := &models.Book{
		ForeignID: "hc:mimosa-confessions-3", AuthorID: author.ID,
		Title: "The Mimosa Confessions, Vol. 3", SortTitle: "the mimosa confessions, vol. 3",
		Status: models.BookStatusWanted, Genres: []string{}, MetadataProvider: "hardcover",
	}
	if err := bookRepo.Create(ctx, excluded); err != nil {
		t.Fatal(err)
	}
	if err := bookRepo.SetExcluded(ctx, excluded.ID, true); err != nil {
		t.Fatal(err)
	}

	series := &models.Series{ForeignID: "ol-series:mimosa", Title: "The Mimosa Confessions"}
	if err := seriesRepo.Create(ctx, series); err != nil {
		t.Fatal(err)
	}
	if err := seriesRepo.UpsertHardcoverLink(ctx, &models.SeriesHardcoverLink{
		SeriesID: series.ID, HardcoverSeriesID: catalog.ForeignID,
		HardcoverProviderID: catalog.ProviderID, HardcoverTitle: catalog.Title,
		HardcoverAuthorName: catalog.AuthorName, HardcoverBookCount: catalog.BookCount,
		Confidence: 1, LinkedBy: "manual",
	}); err != nil {
		t.Fatal(err)
	}

	rec := httptest.NewRecorder()
	h.Fill(rec, withURLParam(httptest.NewRequest(http.MethodPost, "/api/v1/series/1/fill", nil), "id", "1"))
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}

	// The volumes AFTER the skipped one are what this test is really about.
	for _, i := range []int{4, 5} {
		fid := fmt.Sprintf("hc:mimosa-confessions-%d", i)
		created, err := bookRepo.GetByForeignID(ctx, fid)
		if err != nil {
			t.Fatal(err)
		}
		if created == nil {
			t.Errorf("volume %d was not created — the loop stopped at the skipped volume", i)
		}
	}
}

// dungeonCrawlerCatalog reproduces the Hardcover shape from #2238 and #2239:
// three catalog entries filed at position 1 (a box set first, then a novella,
// then the real volume 1) plus a later volume. Hardcover's own ordering puts
// the box set ahead of the book people actually want.
func dungeonCrawlerCatalog() *metadata.SeriesCatalog {
	author := &models.Author{
		ForeignID:        "hc:matt-dinniman",
		Name:             "Matt Dinniman",
		SortName:         "Dinniman, Matt",
		MetadataProvider: "hardcover",
	}
	entry := func(foreignID, providerID, title, position string) metadata.SeriesCatalogBook {
		return metadata.SeriesCatalogBook{
			ForeignID:  foreignID,
			ProviderID: providerID,
			Title:      title,
			Position:   position,
			Book: models.Book{
				ForeignID:        foreignID,
				Title:            title,
				SortTitle:        title,
				MetadataProvider: "hardcover",
				Author:           author,
			},
		}
	}
	books := []metadata.SeriesCatalogBook{
		entry("hc:dungeon-crawler-carl-series-by-matt-dinniman-3-books-collection-set", "446679",
			"Dungeon Crawler Carl Series by Matt Dinniman 3 Books Collection Set", "1"),
		entry("hc:backstage-at-the-pineapple-cabaret", "446680", "Backstage at the Pineapple Cabaret", "1"),
		entry("hc:dungeon-crawler-carl", "446681", "Dungeon Crawler Carl", "1"),
		entry("hc:the-beautiful-place", "446689", "The Beautiful Place", "9"),
	}
	return &metadata.SeriesCatalog{
		ForeignID:  "hc-series:12717",
		ProviderID: "12717",
		Title:      "Dungeon Crawler Carl",
		AuthorName: "Matt Dinniman",
		BookCount:  len(books),
		Books:      books,
	}
}

// enhancedSeriesFixture wires a handler with the enhanced Hardcover series
// feature switched on: env flag, saved token and admin toggle.
func enhancedSeriesFixture(t *testing.T, catalog *metadata.SeriesCatalog, searcher BookSearcher) (*SeriesHandler, *db.SeriesRepo, *db.AuthorRepo, *db.BookRepo, *db.SettingsRepo) {
	t.Helper()
	h, seriesRepo, authorRepo, bookRepo, settingsRepo := seriesFixtureWithProviderAndSettings(t, &stubSeriesProvider{
		catalogs: map[string]*metadata.SeriesCatalog{catalog.ForeignID: catalog},
	}, searcher, true)
	ctx := context.Background()
	if err := settingsRepo.Set(ctx, SettingHardcoverAPIToken, "hc-secret"); err != nil {
		t.Fatal(err)
	}
	if err := settingsRepo.Set(ctx, SettingHardcoverEnhancedSeriesEnabled, "true"); err != nil {
		t.Fatal(err)
	}
	return h, seriesRepo, authorRepo, bookRepo, settingsRepo
}

// linkedSeries creates a local series already linked to catalog.
func linkedSeries(t *testing.T, seriesRepo *db.SeriesRepo, catalog *metadata.SeriesCatalog) *models.Series {
	t.Helper()
	ctx := context.Background()
	series := &models.Series{ForeignID: "ol-series:local", Title: catalog.Title}
	if err := seriesRepo.Create(ctx, series); err != nil {
		t.Fatal(err)
	}
	if err := seriesRepo.UpsertHardcoverLink(ctx, &models.SeriesHardcoverLink{
		SeriesID:            series.ID,
		HardcoverSeriesID:   catalog.ForeignID,
		HardcoverProviderID: catalog.ProviderID,
		HardcoverTitle:      catalog.Title,
		HardcoverAuthorName: catalog.AuthorName,
		HardcoverBookCount:  catalog.BookCount,
		Confidence:          1,
		LinkedBy:            "manual",
	}); err != nil {
		t.Fatal(err)
	}
	return series
}

// TestSeriesFillAddsTheRequestedBookNotAPositionSibling is the #2238
// regression test. findCatalogBook tested the foreign ID and the position in
// one loop, so an earlier entry matching only on position was returned before
// the later entry the caller actually named. Hardcover files a box set, a
// novella and the real volume 1 all at position 1, so clicking add on the
// real volume created the box set instead.
func TestSeriesFillAddsTheRequestedBookNotAPositionSibling(t *testing.T) {
	catalog := dungeonCrawlerCatalog()
	searcher := newMockBookSearcher()
	h, seriesRepo, _, bookRepo, _ := enhancedSeriesFixture(t, catalog, searcher)
	ctx := context.Background()
	series := linkedSeries(t, seriesRepo, catalog)

	body := bytes.NewBufferString(`{"foreignBookId":"hc:dungeon-crawler-carl","providerId":"446681","position":"1"}`)
	rec := httptest.NewRecorder()
	h.Fill(rec, withURLParam(httptest.NewRequest(http.MethodPost, "/api/v1/series/1/fill", body), "id", strconv.FormatInt(series.ID, 10)))
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}

	created, err := bookRepo.GetByForeignID(ctx, "hc:dungeon-crawler-carl")
	if err != nil {
		t.Fatal(err)
	}
	if created == nil {
		t.Fatal("the requested book was not created")
	}
	if created.Title != "Dungeon Crawler Carl" {
		t.Errorf("created title = %q, want %q", created.Title, "Dungeon Crawler Carl")
	}
	// Nothing else at position 1 may be created in its place.
	for _, other := range []string{
		"hc:dungeon-crawler-carl-series-by-matt-dinniman-3-books-collection-set",
		"hc:backstage-at-the-pineapple-cabaret",
	} {
		wrong, err := bookRepo.GetByForeignID(ctx, other)
		if err != nil {
			t.Fatal(err)
		}
		if wrong != nil {
			t.Errorf("fill created %q (%s) instead of the requested book", wrong.Title, other)
		}
	}
}

// TestSeriesFillSkipsCatalogBoxSets is the #2239 gap-1 regression test. The
// unconditional bundle prune (#1780) ran on catalogue ingestion only, so a
// series fill happily created a "3 Books Collection Set" row as a monitored,
// wanted book that then went looking for releases.
func TestSeriesFillSkipsCatalogBoxSets(t *testing.T) {
	catalog := dungeonCrawlerCatalog()
	searcher := newMockBookSearcher()
	h, seriesRepo, _, bookRepo, _ := enhancedSeriesFixture(t, catalog, searcher)
	ctx := context.Background()
	series := linkedSeries(t, seriesRepo, catalog)

	rec := httptest.NewRecorder()
	h.Fill(rec, withURLParam(httptest.NewRequest(http.MethodPost, "/api/v1/series/1/fill", nil), "id", strconv.FormatInt(series.ID, 10)))
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}

	boxSet, err := bookRepo.GetByForeignID(ctx, "hc:dungeon-crawler-carl-series-by-matt-dinniman-3-books-collection-set")
	if err != nil {
		t.Fatal(err)
	}
	if boxSet != nil {
		t.Errorf("fill created the box set %q as a book", boxSet.Title)
	}
	// The real volumes still have to be created, or the prune has eaten the
	// catalogue rather than the bundle.
	for _, fid := range []string{"hc:dungeon-crawler-carl", "hc:the-beautiful-place"} {
		real, err := bookRepo.GetByForeignID(ctx, fid)
		if err != nil {
			t.Fatal(err)
		}
		if real == nil {
			t.Errorf("volume %s was not created", fid)
		}
	}
}

// TestSeriesFillIsNotBlockedByAnExcludedBoxSet is the #2239 gap-2 regression
// test, and covers the state a v1.33.0 install is already in: the box set was
// created by an earlier fill and then excluded. Its title contains the real
// book's title as a substring, and TitleScore scores a substring at 100, so
// the excluded box set was the only candidate over the threshold and blocked
// creation of the real book outright, so "Fill" reported nothing to fill for
// as long as the box set existed.
func TestSeriesFillIsNotBlockedByAnExcludedBoxSet(t *testing.T) {
	catalog := dungeonCrawlerCatalog()
	searcher := newMockBookSearcher()
	h, seriesRepo, authorRepo, bookRepo, _ := enhancedSeriesFixture(t, catalog, searcher)
	ctx := context.Background()
	series := linkedSeries(t, seriesRepo, catalog)

	author := &models.Author{
		ForeignID:        "hc:matt-dinniman",
		Name:             "Matt Dinniman",
		SortName:         "Dinniman, Matt",
		MetadataProvider: "hardcover",
	}
	if err := authorRepo.Create(ctx, author); err != nil {
		t.Fatal(err)
	}
	boxSet := &models.Book{
		ForeignID:        "hc:dungeon-crawler-carl-series-by-matt-dinniman-3-books-collection-set",
		AuthorID:         author.ID,
		Title:            "Dungeon Crawler Carl Series by Matt Dinniman 3 Books Collection Set",
		SortTitle:        "Dungeon Crawler Carl Series by Matt Dinniman 3 Books Collection Set",
		Status:           models.BookStatusSkipped,
		Genres:           []string{},
		MetadataProvider: "hardcover",
	}
	if err := bookRepo.Create(ctx, boxSet); err != nil {
		t.Fatal(err)
	}
	if err := bookRepo.SetExcluded(ctx, boxSet.ID, true); err != nil {
		t.Fatal(err)
	}

	body := bytes.NewBufferString(`{"foreignBookId":"hc:dungeon-crawler-carl","providerId":"446681","position":"1"}`)
	rec := httptest.NewRecorder()
	h.Fill(rec, withURLParam(httptest.NewRequest(http.MethodPost, "/api/v1/series/1/fill", body), "id", strconv.FormatInt(series.ID, 10)))
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	var response map[string]int
	if err := json.NewDecoder(rec.Body).Decode(&response); err != nil {
		t.Fatal(err)
	}
	if response["queued"] != 1 {
		t.Fatalf("expected the real book to be queued, got %+v", response)
	}
	created, err := bookRepo.GetByForeignID(ctx, "hc:dungeon-crawler-carl")
	if err != nil {
		t.Fatal(err)
	}
	if created == nil {
		t.Fatal("the excluded box set still blocks creation of the real book")
	}
	if created.ID == boxSet.ID {
		t.Fatal("fill linked the excluded box set instead of creating the real book")
	}
}

// TestSeriesFillExcludedSameTitleStillBlocks is the guard on the other side of
// #2239: narrowing the excluded-title block to the same title must not let a
// book the user deliberately excluded come back under a new Hardcover id.
func TestSeriesFillExcludedSameTitleStillBlocks(t *testing.T) {
	catalog := dungeonCrawlerCatalog()
	searcher := newMockBookSearcher()
	h, seriesRepo, authorRepo, bookRepo, _ := enhancedSeriesFixture(t, catalog, searcher)
	ctx := context.Background()
	series := linkedSeries(t, seriesRepo, catalog)

	author := &models.Author{
		ForeignID:        "hc:matt-dinniman",
		Name:             "Matt Dinniman",
		SortName:         "Dinniman, Matt",
		MetadataProvider: "hardcover",
	}
	if err := authorRepo.Create(ctx, author); err != nil {
		t.Fatal(err)
	}
	excluded := &models.Book{
		ForeignID:        "manual:dungeon-crawler-carl",
		AuthorID:         author.ID,
		Title:            "Dungeon Crawler Carl",
		SortTitle:        "Dungeon Crawler Carl",
		Status:           models.BookStatusSkipped,
		Genres:           []string{},
		MetadataProvider: "manual",
	}
	if err := bookRepo.Create(ctx, excluded); err != nil {
		t.Fatal(err)
	}
	if err := bookRepo.SetExcluded(ctx, excluded.ID, true); err != nil {
		t.Fatal(err)
	}

	body := bytes.NewBufferString(`{"foreignBookId":"hc:dungeon-crawler-carl","providerId":"446681","position":"1"}`)
	rec := httptest.NewRecorder()
	h.Fill(rec, withURLParam(httptest.NewRequest(http.MethodPost, "/api/v1/series/1/fill", body), "id", strconv.FormatInt(series.ID, 10)))
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	created, err := bookRepo.GetByForeignID(ctx, "hc:dungeon-crawler-carl")
	if err != nil {
		t.Fatal(err)
	}
	if created != nil {
		t.Fatalf("an excluded book of the same title was re-added as %q", created.Title)
	}
}

// TestSeriesFillHonoursAutoGrabKillSwitch is the #2242 regression test. Fill
// marked every book wanted and then fanned out indexer searches with no regard
// for autoGrab.enabled, so the one action most likely to grab a large batch at
// once was the one action the global kill switch did not cover: a user turned
// auto-grab off and a fill grabbed and imported fourteen releases a minute
// later.
//
// Books must still be marked wanted and monitored with the switch off, which
// is what the scheduled wanted sweep does; only the grabbing stops.
func TestSeriesFillHonoursAutoGrabKillSwitch(t *testing.T) {
	for _, tc := range []struct {
		name       string
		autoGrab   string
		wantSearch bool
	}{
		{name: "kill switch off", autoGrab: "false", wantSearch: false},
		{name: "kill switch on", autoGrab: "true", wantSearch: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			searcher := newMockBookSearcher()
			// Enhanced Hardcover off: this is about the local fill path and
			// the search fan-out, not about catalogue expansion.
			h, seriesRepo, authorRepo, bookRepo, settingsRepo := seriesFixtureWithProviderAndSettings(t, &stubSeriesProvider{}, searcher, false)
			ctx := context.Background()
			if err := settingsRepo.Set(ctx, "autoGrab.enabled", tc.autoGrab); err != nil {
				t.Fatal(err)
			}

			author := &models.Author{ForeignID: "hc:author", Name: "Author", SortName: "Author"}
			if err := authorRepo.Create(ctx, author); err != nil {
				t.Fatal(err)
			}
			series := &models.Series{ForeignID: "ol-series:local", Title: "A Series"}
			if err := seriesRepo.Create(ctx, series); err != nil {
				t.Fatal(err)
			}
			book := &models.Book{
				ForeignID: "hc:book-one", AuthorID: author.ID,
				Title: "Book One", SortTitle: "Book One",
				Status: models.BookStatusSkipped, Genres: []string{},
			}
			if err := bookRepo.Create(ctx, book); err != nil {
				t.Fatal(err)
			}
			if err := seriesRepo.LinkBook(ctx, series.ID, book.ID, "1", true); err != nil {
				t.Fatal(err)
			}

			rec := httptest.NewRecorder()
			h.Fill(rec, withURLParam(httptest.NewRequest(http.MethodPost, "/api/v1/series/1/fill", nil), "id", strconv.FormatInt(series.ID, 10)))
			if rec.Code != http.StatusOK {
				t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
			}
			var response map[string]int
			if err := json.NewDecoder(rec.Body).Decode(&response); err != nil {
				t.Fatal(err)
			}
			if response["queued"] != 1 {
				t.Fatalf("expected the book to be queued either way, got %+v", response)
			}
			// Wanted and monitored regardless of the switch.
			stored, err := bookRepo.GetByID(ctx, book.ID)
			if err != nil {
				t.Fatal(err)
			}
			if stored == nil || stored.Status != models.BookStatusWanted || !stored.Monitored {
				t.Fatalf("book should be wanted and monitored, got %+v", stored)
			}

			if tc.wantSearch {
				searcher.waitForCall(t, 2*time.Second)
			} else {
				searcher.assertNoCall(t, 200*time.Millisecond)
			}
		})
	}
}

// TestHandleNewWantedBookLinksHardcoverSeries is the #2245 regression test.
// A series created from a provider's SeriesRefs got the right hc-series
// foreign id and no series_hardcover_links row, and that table is the only
// thing the UI and the Hardcover fill paths read: every series on a freshly
// added Hardcover author showed "(no Hardcover link)" and Fill did nothing.
func TestHandleNewWantedBookLinksHardcoverSeries(t *testing.T) {
	_, seriesRepo, authorRepo, bookRepo := seriesFixture(t)
	ctx := context.Background()

	author := &models.Author{ForeignID: "hc:adrian-tchaikovsky", Name: "Adrian Tchaikovsky", SortName: "Tchaikovsky, Adrian"}
	if err := authorRepo.Create(ctx, author); err != nil {
		t.Fatal(err)
	}
	book := &models.Book{
		ForeignID: "hc:children-of-time", AuthorID: author.ID,
		Title: "Children of Time", SortTitle: "Children of Time",
		Status: models.BookStatusWanted, Genres: []string{},
		SeriesRefs: []models.SeriesRef{
			{ForeignID: "hc-series:1017", Title: "Children of Time", Position: "1", Primary: true},
			// An OpenLibrary ref must not produce a Hardcover link.
			{ForeignID: "ol-series:children", Title: "Children of Time", Position: "1"},
		},
	}
	if err := bookRepo.Create(ctx, book); err != nil {
		t.Fatal(err)
	}

	handleNewWantedBook(ctx, bookRepo, seriesRepo, nil, *book, author.Name)

	hcSeries, err := seriesRepo.GetByForeignID(ctx, "hc-series:1017")
	if err != nil {
		t.Fatal(err)
	}
	if hcSeries == nil {
		t.Fatal("the Hardcover series was not created")
	}
	link, err := seriesRepo.GetHardcoverLink(ctx, hcSeries.ID)
	if err != nil {
		t.Fatal(err)
	}
	if link == nil {
		t.Fatal("no series_hardcover_links row was written for a hc-series ref")
	}
	if link.HardcoverSeriesID != "hc-series:1017" || link.HardcoverProviderID != "1017" {
		t.Errorf("link identity = %q/%q, want hc-series:1017/1017", link.HardcoverSeriesID, link.HardcoverProviderID)
	}
	if link.LinkedBy != "auto" {
		t.Errorf("linkedBy = %q, want auto", link.LinkedBy)
	}

	olSeries, err := seriesRepo.GetByForeignID(ctx, "ol-series:children")
	if err != nil {
		t.Fatal(err)
	}
	if olSeries == nil {
		t.Fatal("the OpenLibrary series was not created")
	}
	olLink, err := seriesRepo.GetHardcoverLink(ctx, olSeries.ID)
	if err != nil {
		t.Fatal(err)
	}
	if olLink != nil {
		t.Errorf("a non-Hardcover series ref produced a Hardcover link: %+v", olLink)
	}
}

// TestEnsureHardcoverLinkFromForeignIDKeepsAManualLink pins the idempotence
// the sync path relies on: a link the user chose by hand is never overwritten
// by the automatic one (#2245).
func TestEnsureHardcoverLinkFromForeignIDKeepsAManualLink(t *testing.T) {
	_, seriesRepo, _, _ := seriesFixture(t)
	ctx := context.Background()

	series := &models.Series{ForeignID: "hc-series:1017", Title: "Children of Time"}
	if err := seriesRepo.Create(ctx, series); err != nil {
		t.Fatal(err)
	}
	if err := seriesRepo.UpsertHardcoverLink(ctx, &models.SeriesHardcoverLink{
		SeriesID:          series.ID,
		HardcoverSeriesID: "hc-series:9999",
		HardcoverTitle:    "Chosen By Hand",
		Confidence:        1,
		LinkedBy:          "manual",
	}); err != nil {
		t.Fatal(err)
	}

	linked, err := seriesRepo.EnsureHardcoverLinkFromForeignID(ctx, series.ID, "hc-series:1017", "Children of Time")
	if err != nil {
		t.Fatal(err)
	}
	if linked {
		t.Error("an existing link was overwritten")
	}
	link, err := seriesRepo.GetHardcoverLink(ctx, series.ID)
	if err != nil {
		t.Fatal(err)
	}
	if link == nil || link.LinkedBy != "manual" || link.HardcoverSeriesID != "hc-series:9999" {
		t.Fatalf("manual link was not preserved: %+v", link)
	}
}

// datingSimCatalog is a 13-volume light-novel catalog in position order, the
// shape that exposes #2343: every volume differs from every other by one digit
// in an otherwise identical string.
func datingSimCatalog() *metadata.SeriesCatalog {
	author := &models.Author{
		ForeignID:        "hc:yomu-mishima",
		Name:             "Yomu Mishima",
		SortName:         "Mishima, Yomu",
		MetadataProvider: "hardcover",
	}
	books := make([]metadata.SeriesCatalogBook, 0, 13)
	for i := 1; i <= 13; i++ {
		title := fmt.Sprintf("Trapped in a Dating Sim Vol. %d", i)
		foreignID := fmt.Sprintf("hc:dating-sim-vol-%d", i)
		books = append(books, metadata.SeriesCatalogBook{
			ForeignID:  foreignID,
			ProviderID: strconv.Itoa(700 + i),
			Title:      title,
			Position:   strconv.Itoa(i),
			Book: models.Book{
				ForeignID:        foreignID,
				Title:            title,
				SortTitle:        title,
				MetadataProvider: "hardcover",
				Author:           author,
			},
		})
	}
	return &metadata.SeriesCatalog{
		ForeignID:  "hc-series:7001",
		ProviderID: "7001",
		Title:      "Trapped in a Dating Sim",
		AuthorName: "Yomu Mishima",
		BookCount:  len(books),
		Books:      books,
	}
}

// localSeriesBook builds an unlinked local library book for the diff. The
// foreign id is deliberately a Calibre-style synthetic one so it can never
// foreign-ID-match a catalog entry, which is the situation the bug needs: an
// imported book with no provider identity and no PositionInSeries.
func localSeriesBook(id int64, title string) models.SeriesBook {
	return models.SeriesBook{
		BookID: id,
		Book: &models.Book{
			ID:        id,
			ForeignID: fmt.Sprintf("calibre:book:%d", id),
			Title:     title,
			SortTitle: title,
			Status:    models.BookStatusImported,
		},
	}
}

func diffForeignIDs(rows []seriesHardcoverDiffBook) []string {
	ids := make([]string, 0, len(rows))
	for _, row := range rows {
		ids = append(ids, row.ForeignBookID)
	}
	return ids
}

// TestBuildHardcoverDiffKeepsVolumesApart is the #2343 regression guard for the
// missing DifferentVolumes veto. Local "Vol. 13" scores a perfect 100 against
// catalog "Vol. 1" because PartialRatio reads "vol 1" as a substring of
// "vol 13", and index 0 wins the tie, so before the fix the diff reported
// volume 1 as Present under volume 13's local title and left volume 13 in
// Missing with an Add button that would refuse to add it.
func TestBuildHardcoverDiffKeepsVolumesApart(t *testing.T) {
	catalog := datingSimCatalog()
	series := &models.Series{
		ID:    1,
		Title: "Trapped in a Dating Sim",
		Books: []models.SeriesBook{localSeriesBook(1, "Trapped in a Dating Sim Vol. 13")},
	}
	link := &models.SeriesHardcoverLink{SeriesID: series.ID, HardcoverSeriesID: catalog.ForeignID}

	diff := buildHardcoverDiff(context.Background(), nil, 0, series, link, catalog)

	if len(diff.Present) != 1 {
		t.Fatalf("Present = %v, want exactly one row", diffForeignIDs(diff.Present))
	}
	if diff.Present[0].ForeignBookID != "hc:dating-sim-vol-13" {
		t.Fatalf("Present row = %q, want hc:dating-sim-vol-13", diff.Present[0].ForeignBookID)
	}
	if diff.Present[0].LocalBookID == nil || *diff.Present[0].LocalBookID != 1 {
		t.Fatalf("Present row LocalBookID = %v, want 1", diff.Present[0].LocalBookID)
	}
	if len(diff.LocalOnly) != 0 {
		t.Fatalf("LocalOnly = %+v, want empty", diff.LocalOnly)
	}

	missing := map[string]bool{}
	for _, row := range diff.Missing {
		missing[row.ForeignBookID] = true
	}
	if missing["hc:dating-sim-vol-13"] {
		t.Fatalf("volume 13 is in the library but was reported Missing: %v", diffForeignIDs(diff.Missing))
	}
	for i := 1; i <= 12; i++ {
		id := fmt.Sprintf("hc:dating-sim-vol-%d", i)
		if !missing[id] {
			t.Fatalf("volume %d is not in the library but is not Missing: %v", i, diffForeignIDs(diff.Missing))
		}
	}
	if diff.MissingCount != 12 || diff.PresentCount != 1 {
		t.Fatalf("counts = present %d / missing %d, want 1 / 12", diff.PresentCount, diff.MissingCount)
	}
}

// TestBuildHardcoverDiffDoesNotDoubleClaimOneCatalogEntry is the #2343
// regression guard for the missing exclusion set. Neither title carries a
// volume marker, so DifferentVolumes cannot help here: both locals score 100
// against catalog index 0 (PartialRatio again), and before the fix both
// claimed it, which duplicated that row in Present, over-counted PresentCount
// and pushed the second local's real catalog entry into Missing.
func TestBuildHardcoverDiffDoesNotDoubleClaimOneCatalogEntry(t *testing.T) {
	catalog := stormlightCatalog()
	catalog.Books = append(catalog.Books, metadata.SeriesCatalogBook{
		ForeignID:  "hc:the-way-of-kings-prime",
		ProviderID: "104",
		Title:      "The Way of Kings Prime",
		Position:   "2",
		Book: models.Book{
			ForeignID: "hc:the-way-of-kings-prime",
			Title:     "The Way of Kings Prime",
			Author:    catalog.Books[0].Book.Author,
		},
	})
	catalog.BookCount = len(catalog.Books)

	series := &models.Series{
		ID:    1,
		Title: "The Stormlight Archive",
		Books: []models.SeriesBook{
			localSeriesBook(1, "The Way of Kings"),
			localSeriesBook(2, "The Way of Kings Prime"),
		},
	}
	link := &models.SeriesHardcoverLink{SeriesID: series.ID, HardcoverSeriesID: catalog.ForeignID}

	diff := buildHardcoverDiff(context.Background(), nil, 0, series, link, catalog)

	seen := map[string]int{}
	for _, row := range diff.Present {
		seen[row.ForeignBookID]++
	}
	for id, n := range seen {
		if n > 1 {
			t.Fatalf("catalog entry %q appears %d times in Present: %v", id, n, diffForeignIDs(diff.Present))
		}
	}
	if len(diff.Present) != 2 || diff.PresentCount != 2 {
		t.Fatalf("Present = %v (count %d), want both catalog entries once", diffForeignIDs(diff.Present), diff.PresentCount)
	}
	for _, row := range diff.Missing {
		if row.ForeignBookID == "hc:the-way-of-kings-prime" {
			t.Fatalf("the second local book's own catalog entry was reported Missing: %v", diffForeignIDs(diff.Missing))
		}
	}
	if len(diff.Missing) != 0 {
		t.Fatalf("Missing = %v, want empty", diffForeignIDs(diff.Missing))
	}
}

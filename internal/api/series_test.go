package api

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
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
	books, err := seriesRepo.ListBooksInSeries(context.Background(), series.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(books) != 1 || books[0].ForeignID != "hc:the-way-of-kings" {
		t.Fatalf("expected created book linked to series, got %+v", books)
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

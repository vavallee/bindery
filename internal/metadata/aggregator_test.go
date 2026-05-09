package metadata

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/vavallee/bindery/internal/models"
)

// mockProvider is a test double for the Provider interface.
type mockProvider struct {
	name           string
	searchBooks    []models.Book
	searchBookErr  error
	searchAuthors  []models.Author
	searchAuthErr  error
	getAuthor      *models.Author
	getAuthorErr   error
	getBook        *models.Book
	getBookErr     error
	getEditions    []models.Edition
	getEditionsErr error
	getByISBN      *models.Book
	getByISBNErr   error
	// authorWorks implements worksProvider interface
	authorWorks    []models.Book
	authorWorksErr error
}

func (m *mockProvider) Name() string { return m.name }
func (m *mockProvider) SearchAuthors(_ context.Context, _ string) ([]models.Author, error) {
	return m.searchAuthors, m.searchAuthErr
}
func (m *mockProvider) SearchBooks(_ context.Context, _ string) ([]models.Book, error) {
	return m.searchBooks, m.searchBookErr
}
func (m *mockProvider) GetAuthor(_ context.Context, _ string) (*models.Author, error) {
	return m.getAuthor, m.getAuthorErr
}
func (m *mockProvider) GetBook(_ context.Context, _ string) (*models.Book, error) {
	return m.getBook, m.getBookErr
}
func (m *mockProvider) GetEditions(_ context.Context, _ string) ([]models.Edition, error) {
	return m.getEditions, m.getEditionsErr
}
func (m *mockProvider) GetBookByISBN(_ context.Context, _ string) (*models.Book, error) {
	return m.getByISBN, m.getByISBNErr
}

// worksProvider implementation (optional, only attached when needed).
type mockWorksProvider struct {
	mockProvider
}

func (m *mockWorksProvider) GetAuthorWorks(_ context.Context, _ string) ([]models.Book, error) {
	return m.authorWorks, m.authorWorksErr
}

type mockAuthorWorksByNameProvider struct {
	mockProvider
	authorWorksByName    []models.Book
	authorWorksByNameErr error
	gotAuthorName        string
	calls                int
}

func (m *mockAuthorWorksByNameProvider) GetAuthorWorksByName(_ context.Context, authorName string) ([]models.Book, error) {
	m.calls++
	m.gotAuthorName = authorName
	return m.authorWorksByName, m.authorWorksByNameErr
}

type mockSeriesCatalogProvider struct {
	mockProvider
	searchSeriesResults []SeriesSearchResult
	searchSeriesErr     error
	searchSeriesCalls   int
	searchSeriesQueries []string
	searchSeriesLimits  []int
	catalogs            map[string]*SeriesCatalog
	catalogErr          error
	catalogCalls        int
	catalogIDs          []string
}

func (m *mockSeriesCatalogProvider) SearchSeries(_ context.Context, query string, limit int) ([]SeriesSearchResult, error) {
	m.searchSeriesCalls++
	m.searchSeriesQueries = append(m.searchSeriesQueries, query)
	m.searchSeriesLimits = append(m.searchSeriesLimits, limit)
	return m.searchSeriesResults, m.searchSeriesErr
}

func (m *mockSeriesCatalogProvider) GetSeriesCatalog(_ context.Context, foreignID string) (*SeriesCatalog, error) {
	m.catalogCalls++
	m.catalogIDs = append(m.catalogIDs, foreignID)
	if m.catalogErr != nil {
		return nil, m.catalogErr
	}
	return m.catalogs[foreignID], nil
}

func TestAggregator_SearchAuthors(t *testing.T) {
	want := []models.Author{{Name: "Frank Herbert", ForeignID: "OL123A"}}
	primary := &mockProvider{name: "ol", searchAuthors: want}
	agg := newTestAggregator(primary)

	got, err := agg.SearchAuthors(context.Background(), "Herbert")
	if err != nil {
		t.Fatalf("SearchAuthors: %v", err)
	}
	if len(got) != 1 || got[0].Name != "Frank Herbert" {
		t.Errorf("unexpected result: %+v", got)
	}
}

func TestAggregator_SearchAuthors_Error(t *testing.T) {
	primary := &mockProvider{name: "ol", searchAuthErr: errors.New("network error")}
	agg := newTestAggregator(primary)

	_, err := agg.SearchAuthors(context.Background(), "Herbert")
	if err == nil {
		t.Fatal("expected error to be propagated")
	}
}

func TestAggregator_SearchBooks(t *testing.T) {
	want := []models.Book{{Title: "Dune", ForeignID: "OL456W"}}
	primary := &mockProvider{name: "ol", searchBooks: want}
	agg := newTestAggregator(primary)

	got, err := agg.SearchBooks(context.Background(), "Dune")
	if err != nil {
		t.Fatalf("SearchBooks: %v", err)
	}
	if len(got) != 1 || got[0].Title != "Dune" {
		t.Errorf("unexpected result: %+v", got)
	}
}

func TestAggregator_GetAuthor_Success(t *testing.T) {
	author := &models.Author{Name: "Ursula K. Le Guin", ForeignID: "OL111A"}
	primary := &mockProvider{name: "ol", getAuthor: author}
	agg := newTestAggregator(primary)

	got, err := agg.GetAuthor(context.Background(), "OL111A")
	if err != nil {
		t.Fatalf("GetAuthor: %v", err)
	}
	if got.Name != "Ursula K. Le Guin" {
		t.Errorf("Name: want 'Ursula K. Le Guin', got %q", got.Name)
	}
}

func TestAggregator_GetAuthor_Cached(t *testing.T) {
	calls := 0
	primary := &mockProvider{name: "ol", getAuthor: &models.Author{Name: "Isaac Asimov"}}
	agg := newTestAggregator(primary)
	// Wrap to count calls
	origGetAuthor := primary.getAuthor

	_, _ = agg.GetAuthor(context.Background(), "OL999A")
	calls++                 // first call
	primary.getAuthor = nil // second call should use cache, not nil author
	got, err := agg.GetAuthor(context.Background(), "OL999A")
	if err != nil {
		t.Fatalf("GetAuthor (cached): %v", err)
	}
	if got.Name != origGetAuthor.Name {
		t.Errorf("expected cached author, got %+v", got)
	}
	_ = calls
}

func TestAggregator_GetAuthor_Error(t *testing.T) {
	primary := &mockProvider{name: "ol", getAuthorErr: errors.New("not found")}
	agg := newTestAggregator(primary)

	_, err := agg.GetAuthor(context.Background(), "OL999A")
	if err == nil {
		t.Fatal("expected error to propagate")
	}
}

func TestAggregator_GetBook_LongDescription(t *testing.T) {
	// A book with a long description should NOT trigger enrichment.
	longDesc := string(make([]byte, 100)) // 100-char description
	for i := range longDesc {
		_ = i
	}
	longDesc = "This is a very long book description that exceeds the fifty character minimum and should never be enriched by secondary providers."
	book := &models.Book{Title: "Dune", Description: longDesc}

	enricherCalled := false
	enricher := &mockProvider{
		name:        "gb",
		searchBooks: []models.Book{{Description: "Should not be used"}},
	}
	// We'll detect if enricher was called by overriding its SearchBooks
	_ = enricherCalled
	primary := &mockProvider{name: "ol", getBook: book}
	agg := &Aggregator{
		primary:   primary,
		enrichers: []Provider{enricher},
		cache:     newTTLCache(time.Minute),
	}

	got, err := agg.GetBook(context.Background(), "OL456W")
	if err != nil {
		t.Fatalf("GetBook: %v", err)
	}
	if got.Description != longDesc {
		t.Errorf("description should not be overwritten when long enough")
	}
}

func TestAggregator_GetBook_ShortDescription_Enriched(t *testing.T) {
	shortDesc := "Short."
	richerDesc := "A much richer description that is longer than the short one from the primary provider."

	primary := &mockProvider{
		name:    "ol",
		getBook: &models.Book{Title: "Foundation", Description: shortDesc},
	}
	enricher := &mockProvider{
		name:        "gb",
		searchBooks: []models.Book{{Description: richerDesc}},
	}
	agg := &Aggregator{
		primary:   primary,
		enrichers: []Provider{enricher},
		cache:     newTTLCache(time.Minute),
	}

	got, err := agg.GetBook(context.Background(), "OL789W")
	if err != nil {
		t.Fatalf("GetBook: %v", err)
	}
	if got.Description != richerDesc {
		t.Errorf("expected enriched description %q, got %q", richerDesc, got.Description)
	}
}

func TestAggregator_GetBook_Enrichment_RatingFilled(t *testing.T) {
	primary := &mockProvider{
		name:    "ol",
		getBook: &models.Book{Title: "Short", Description: "x", AverageRating: 0},
	}
	enricher := &mockProvider{
		name:        "hc",
		searchBooks: []models.Book{{Description: "Some desc", AverageRating: 4.5, RatingsCount: 100}},
	}
	agg := &Aggregator{
		primary:   primary,
		enrichers: []Provider{enricher},
		cache:     newTTLCache(time.Minute),
	}

	got, err := agg.GetBook(context.Background(), "OL001W")
	if err != nil {
		t.Fatalf("GetBook: %v", err)
	}
	if got.AverageRating != 4.5 {
		t.Errorf("rating: want 4.5, got %f", got.AverageRating)
	}
	if got.RatingsCount != 100 {
		t.Errorf("ratingsCount: want 100, got %d", got.RatingsCount)
	}
}

func TestAggregator_GetBook_Cached(t *testing.T) {
	primary := &mockProvider{name: "ol", getBook: &models.Book{Title: "Cached Book", Description: "A sufficiently long description for caching test purposes here."}}
	agg := newTestAggregator(primary)

	first, _ := agg.GetBook(context.Background(), "OL111W")
	primary.getBook = nil // clear so second call must use cache

	second, err := agg.GetBook(context.Background(), "OL111W")
	if err != nil {
		t.Fatalf("GetBook (cache): %v", err)
	}
	if second.Title != first.Title {
		t.Errorf("cached book mismatch: got %q", second.Title)
	}
}

func TestAggregator_GetBook_Error(t *testing.T) {
	primary := &mockProvider{name: "ol", getBookErr: errors.New("lookup failed")}
	agg := newTestAggregator(primary)

	_, err := agg.GetBook(context.Background(), "OL999W")
	if err == nil {
		t.Fatal("expected error to propagate")
	}
}

func TestAggregator_GetEditions_Success(t *testing.T) {
	editions := []models.Edition{{Title: "1st ed."}, {Title: "2nd ed."}}
	primary := &mockProvider{name: "ol", getEditions: editions}
	agg := newTestAggregator(primary)

	got, err := agg.GetEditions(context.Background(), "OL456W")
	if err != nil {
		t.Fatalf("GetEditions: %v", err)
	}
	if len(got) != 2 {
		t.Errorf("expected 2 editions, got %d", len(got))
	}
}

func TestAggregator_GetEditions_Cached(t *testing.T) {
	editions := []models.Edition{{Title: "Paperback"}}
	primary := &mockProvider{name: "ol", getEditions: editions}
	agg := newTestAggregator(primary)

	_, _ = agg.GetEditions(context.Background(), "OL999W")
	primary.getEditions = nil // clear; second call must use cache

	got, err := agg.GetEditions(context.Background(), "OL999W")
	if err != nil {
		t.Fatalf("GetEditions (cache): %v", err)
	}
	if len(got) != 1 || got[0].Title != "Paperback" {
		t.Errorf("cached editions mismatch: %+v", got)
	}
}

func TestAggregator_GetBookByISBN_Success(t *testing.T) {
	book := &models.Book{Title: "The Left Hand of Darkness", Description: "A novel long enough description to pass the enrichment check easily."}
	primary := &mockProvider{name: "ol", getByISBN: book}
	agg := newTestAggregator(primary)

	got, err := agg.GetBookByISBN(context.Background(), "9780441478125")
	if err != nil {
		t.Fatalf("GetBookByISBN: %v", err)
	}
	if got.Title != "The Left Hand of Darkness" {
		t.Errorf("Title: want 'The Left Hand of Darkness', got %q", got.Title)
	}
}

func TestAggregator_GetBookByISBN_Cached(t *testing.T) {
	book := &models.Book{Title: "Cached ISBN Book", Description: "Long enough to skip enrichment and exercise the caching path correctly."}
	primary := &mockProvider{name: "ol", getByISBN: book}
	agg := newTestAggregator(primary)

	_, _ = agg.GetBookByISBN(context.Background(), "9780441478125")
	primary.getByISBN = nil

	got, err := agg.GetBookByISBN(context.Background(), "9780441478125")
	if err != nil {
		t.Fatalf("GetBookByISBN (cache): %v", err)
	}
	if got.Title != "Cached ISBN Book" {
		t.Errorf("cached book mismatch: got %q", got.Title)
	}
}

func TestAggregator_GetBookByISBN_NilBook(t *testing.T) {
	primary := &mockProvider{name: "ol", getByISBN: nil}
	agg := newTestAggregator(primary)

	got, err := agg.GetBookByISBN(context.Background(), "0000000000")
	if err != nil {
		t.Fatalf("GetBookByISBN(nil): %v", err)
	}
	if got != nil {
		t.Errorf("expected nil for missing ISBN, got %+v", got)
	}
}

func TestAggregator_SearchSeries_EdgeCases(t *testing.T) {
	agg := newTestAggregator(&mockProvider{name: "ol"})

	got, err := agg.SearchSeries(context.Background(), "  ", 10)
	if err != nil {
		t.Fatalf("SearchSeries empty query: %v", err)
	}
	if got != nil {
		t.Fatalf("empty query = %+v, want nil", got)
	}

	got, err = agg.SearchSeries(context.Background(), "Dune", 10)
	if err != nil {
		t.Fatalf("SearchSeries without providers: %v", err)
	}
	if got != nil {
		t.Fatalf("without providers = %+v, want nil", got)
	}
}

func TestAggregator_SearchSeries_FallbackDefaultLimitAndCache(t *testing.T) {
	primary := &mockSeriesCatalogProvider{
		mockProvider:    mockProvider{name: "primary"},
		searchSeriesErr: errors.New("primary unavailable"),
	}
	enricher := &mockSeriesCatalogProvider{
		mockProvider: mockProvider{name: "enricher"},
		searchSeriesResults: []SeriesSearchResult{{
			ForeignID:  "hc-series:1",
			ProviderID: "1",
			Title:      "Dune",
		}},
	}
	agg := &Aggregator{
		primary:   primary,
		enrichers: []Provider{enricher},
		cache:     newTTLCache(time.Minute),
	}

	got, err := agg.SearchSeries(context.Background(), "  Dune  ", 0)
	if err != nil {
		t.Fatalf("SearchSeries: %v", err)
	}
	if len(got) != 1 || got[0].ForeignID != "hc-series:1" {
		t.Fatalf("unexpected results: %+v", got)
	}
	if primary.searchSeriesCalls != 1 || enricher.searchSeriesCalls != 1 {
		t.Fatalf("provider calls primary=%d enricher=%d, want 1/1", primary.searchSeriesCalls, enricher.searchSeriesCalls)
	}
	if primary.searchSeriesQueries[0] != "Dune" || primary.searchSeriesLimits[0] != 10 {
		t.Fatalf("primary query/limit = %q/%d, want Dune/10", primary.searchSeriesQueries[0], primary.searchSeriesLimits[0])
	}
	if enricher.searchSeriesQueries[0] != "Dune" || enricher.searchSeriesLimits[0] != 10 {
		t.Fatalf("enricher query/limit = %q/%d, want Dune/10", enricher.searchSeriesQueries[0], enricher.searchSeriesLimits[0])
	}

	enricher.searchSeriesResults = nil
	got, err = agg.SearchSeries(context.Background(), "Dune", 10)
	if err != nil {
		t.Fatalf("SearchSeries cached: %v", err)
	}
	if len(got) != 1 || primary.searchSeriesCalls != 1 || enricher.searchSeriesCalls != 1 {
		t.Fatalf("cached lookup got=%+v calls primary=%d enricher=%d, want cached result and no new calls", got, primary.searchSeriesCalls, enricher.searchSeriesCalls)
	}
}

func TestAggregator_GetSeriesCatalog_EdgeCases(t *testing.T) {
	agg := newTestAggregator(&mockProvider{name: "ol"})

	got, err := agg.GetSeriesCatalog(context.Background(), "  ")
	if err != nil {
		t.Fatalf("GetSeriesCatalog empty id: %v", err)
	}
	if got != nil {
		t.Fatalf("empty id = %+v, want nil", got)
	}

	got, err = agg.GetSeriesCatalog(context.Background(), "hc-series:missing")
	if err != nil {
		t.Fatalf("GetSeriesCatalog without providers: %v", err)
	}
	if got != nil {
		t.Fatalf("without providers = %+v, want nil", got)
	}
}

func TestAggregator_GetSeriesCatalog_FallbackAndCache(t *testing.T) {
	catalog := &SeriesCatalog{ForeignID: "hc-series:1", ProviderID: "1", Title: "Dune"}
	primary := &mockSeriesCatalogProvider{
		mockProvider: mockProvider{name: "primary"},
		catalogErr:   errors.New("primary unavailable"),
	}
	enricher := &mockSeriesCatalogProvider{
		mockProvider: mockProvider{name: "enricher"},
		catalogs:     map[string]*SeriesCatalog{catalog.ForeignID: catalog},
	}
	agg := &Aggregator{
		primary:   primary,
		enrichers: []Provider{enricher},
		cache:     newTTLCache(time.Minute),
	}

	got, err := agg.GetSeriesCatalog(context.Background(), "  hc-series:1  ")
	if err != nil {
		t.Fatalf("GetSeriesCatalog: %v", err)
	}
	if got == nil || got.Title != "Dune" {
		t.Fatalf("unexpected catalog: %+v", got)
	}
	if primary.catalogCalls != 1 || enricher.catalogCalls != 1 {
		t.Fatalf("provider calls primary=%d enricher=%d, want 1/1", primary.catalogCalls, enricher.catalogCalls)
	}
	if primary.catalogIDs[0] != "hc-series:1" || enricher.catalogIDs[0] != "hc-series:1" {
		t.Fatalf("catalog ids primary=%q enricher=%q, want trimmed id", primary.catalogIDs[0], enricher.catalogIDs[0])
	}

	enricher.catalogs = nil
	got, err = agg.GetSeriesCatalog(context.Background(), "hc-series:1")
	if err != nil {
		t.Fatalf("GetSeriesCatalog cached: %v", err)
	}
	if got == nil || got.Title != "Dune" || primary.catalogCalls != 1 || enricher.catalogCalls != 1 {
		t.Fatalf("cached catalog=%+v calls primary=%d enricher=%d, want cached result and no new calls", got, primary.catalogCalls, enricher.catalogCalls)
	}
}

func TestAggregator_SeriesCatalogProviders_Order(t *testing.T) {
	primary := &mockSeriesCatalogProvider{mockProvider: mockProvider{name: "primary"}}
	plainEnricher := &mockProvider{name: "plain"}
	seriesEnricher := &mockSeriesCatalogProvider{mockProvider: mockProvider{name: "series"}}
	agg := &Aggregator{
		primary:   primary,
		enrichers: []Provider{plainEnricher, seriesEnricher},
		cache:     newTTLCache(time.Minute),
	}

	providers := agg.seriesCatalogProviders()
	if len(providers) != 2 {
		t.Fatalf("providers len = %d, want 2", len(providers))
	}
	if providers[0] != primary || providers[1] != seriesEnricher {
		t.Fatalf("provider order = %#v, want primary then series enricher", providers)
	}
	if providers := (*Aggregator)(nil).seriesCatalogProviders(); providers != nil {
		t.Fatalf("nil aggregator providers = %+v, want nil", providers)
	}
}

func TestAggregator_GetAuthorWorks_WorksProvider(t *testing.T) {
	books := []models.Book{{Title: "Dune"}, {Title: "Dune Messiah"}}
	primary := &mockWorksProvider{
		mockProvider: mockProvider{name: "ol", authorWorks: books},
	}
	agg := &Aggregator{
		primary: primary,
		cache:   newTTLCache(time.Minute),
	}

	got, err := agg.GetAuthorWorks(context.Background(), "OL123A")
	if err != nil {
		t.Fatalf("GetAuthorWorks: %v", err)
	}
	if len(got) != 2 {
		t.Errorf("expected 2 works, got %d", len(got))
	}
	if got[0].Title != "Dune" {
		t.Errorf("first title: want 'Dune', got %q", got[0].Title)
	}
}

func TestAggregator_GetAuthorWorks_Fallback(t *testing.T) {
	// Primary does not implement worksProvider → falls back to SearchBooks.
	books := []models.Book{{Title: "Foundation"}, {Title: "Foundation and Empire"}}
	primary := &mockProvider{name: "gb", searchBooks: books}
	agg := &Aggregator{
		primary: primary,
		cache:   newTTLCache(time.Minute),
	}

	got, err := agg.GetAuthorWorks(context.Background(), "OL999A")
	if err != nil {
		t.Fatalf("GetAuthorWorks (fallback): %v", err)
	}
	if len(got) != 2 {
		t.Errorf("expected 2 works from fallback, got %d", len(got))
	}
}

func TestAggregator_GetAuthorWorks_Cached(t *testing.T) {
	books := []models.Book{{Title: "Ender's Game"}}
	primary := &mockWorksProvider{
		mockProvider: mockProvider{name: "ol", authorWorks: books},
	}
	agg := &Aggregator{
		primary: primary,
		cache:   newTTLCache(time.Minute),
	}

	_, _ = agg.GetAuthorWorks(context.Background(), "OL555A")
	primary.authorWorks = nil // clear; next call must hit cache

	got, err := agg.GetAuthorWorks(context.Background(), "OL555A")
	if err != nil {
		t.Fatalf("GetAuthorWorks (cache): %v", err)
	}
	if len(got) != 1 || got[0].Title != "Ender's Game" {
		t.Errorf("cached works mismatch: %+v", got)
	}
}

func TestAggregator_GetAuthorWorksForAuthor_MergesSupplementalByTitle(t *testing.T) {
	primary := &mockWorksProvider{
		mockProvider: mockProvider{name: "ol", authorWorks: []models.Book{
			{ForeignID: "OL1W", Title: "Dune", MetadataProvider: "openlibrary"},
		}},
	}
	hardcover := &mockAuthorWorksByNameProvider{
		mockProvider: mockProvider{name: "hardcover"},
		authorWorksByName: []models.Book{
			{
				ForeignID:        "hc:dune",
				Title:            "Dune",
				Description:      "A desert planet.",
				ImageURL:         "https://img/dune.jpg",
				AverageRating:    4.5,
				RatingsCount:     1000,
				MetadataProvider: "hardcover",
			},
			{ForeignID: "hc:children-of-dune", Title: "Children of Dune", MetadataProvider: "hardcover"},
		},
	}
	agg := &Aggregator{
		primary:   primary,
		enrichers: []Provider{hardcover},
		cache:     newTTLCache(time.Minute),
	}

	got, err := agg.GetAuthorWorksForAuthor(context.Background(), models.Author{ForeignID: "OL123A", Name: "Frank Herbert"})
	if err != nil {
		t.Fatalf("GetAuthorWorksForAuthor: %v", err)
	}
	if hardcover.gotAuthorName != "Frank Herbert" {
		t.Fatalf("supplemental author name = %q", hardcover.gotAuthorName)
	}
	if len(got) != 2 {
		t.Fatalf("expected 2 merged works, got %d: %+v", len(got), got)
	}
	if got[0].ForeignID != "OL1W" || got[0].MetadataProvider != "openlibrary" {
		t.Fatalf("primary identity should win duplicate title: %+v", got[0])
	}
	if got[0].ImageURL != "https://img/dune.jpg" || got[0].AverageRating != 4.5 || got[0].Description == "" {
		t.Fatalf("supplemental metadata was not merged: %+v", got[0])
	}
	if got[1].ForeignID != "hc:children-of-dune" {
		t.Fatalf("supplemental-only book missing: %+v", got[1])
	}
}

func TestAggregator_GetAuthorWorksForAuthor_MergesSupplementalIntoFirstDuplicateTitle(t *testing.T) {
	primary := &mockWorksProvider{
		mockProvider: mockProvider{name: "ol", authorWorks: []models.Book{
			{ForeignID: "OL1W", Title: "Dune", MetadataProvider: "openlibrary"},
			{ForeignID: "OL2W", Title: "Dune", MetadataProvider: "openlibrary"},
		}},
	}
	hardcover := &mockAuthorWorksByNameProvider{
		mockProvider: mockProvider{name: "hardcover"},
		authorWorksByName: []models.Book{
			{
				ForeignID:        "hc:dune",
				Title:            "Dune",
				ImageURL:         "https://img/dune.jpg",
				AverageRating:    4.5,
				MetadataProvider: "hardcover",
			},
		},
	}
	agg := &Aggregator{
		primary:   primary,
		enrichers: []Provider{hardcover},
		cache:     newTTLCache(time.Minute),
	}

	got, err := agg.GetAuthorWorksForAuthor(context.Background(), models.Author{ForeignID: "OL123A", Name: "Frank Herbert"})
	if err != nil {
		t.Fatalf("GetAuthorWorksForAuthor: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("expected duplicate primary works to remain for downstream dedup, got %d: %+v", len(got), got)
	}
	if got[0].ImageURL != "https://img/dune.jpg" || got[0].AverageRating != 4.5 {
		t.Fatalf("first duplicate did not receive supplemental metadata: %+v", got[0])
	}
	if got[1].ImageURL != "" || got[1].AverageRating != 0 {
		t.Fatalf("supplemental metadata merged into later duplicate: %+v", got[1])
	}
}

func TestAggregator_GetAuthorWorksForAuthor_EnrichesMissingCoversAfterSupplement(t *testing.T) {
	primary := &mockWorksProvider{
		mockProvider: mockProvider{name: "ol", authorWorks: []models.Book{
			{ForeignID: "OL1W", Title: "Dune", MetadataProvider: "openlibrary"},
			{ForeignID: "OL2W", Title: "Heretics of Dune", MetadataProvider: "openlibrary"},
		}},
	}
	hardcover := &mockAuthorWorksByNameProvider{
		mockProvider: mockProvider{name: "hardcover"},
		authorWorksByName: []models.Book{
			{ForeignID: "hc:dune", Title: "Dune", ImageURL: "https://img/dune.jpg", MetadataProvider: "hardcover"},
		},
	}
	google := &mockProvider{
		name:        "googlebooks",
		searchBooks: []models.Book{{ImageURL: "https://books.google.com/heretics.jpg"}},
	}
	agg := &Aggregator{
		primary:   primary,
		enrichers: []Provider{hardcover, google},
		cache:     newTTLCache(time.Minute),
	}

	got, err := agg.GetAuthorWorksForAuthor(context.Background(), models.Author{ForeignID: "OL123A", Name: "Frank Herbert"})
	if err != nil {
		t.Fatalf("GetAuthorWorksForAuthor: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("expected 2 works, got %d: %+v", len(got), got)
	}
	if got[0].ImageURL != "https://img/dune.jpg" {
		t.Fatalf("matched supplemental cover was not merged: %+v", got[0])
	}
	if got[1].ImageURL != "https://books.google.com/heretics.jpg" {
		t.Fatalf("missing cover was not enriched after supplement: %+v", got[1])
	}
}

func TestAggregator_GetAuthorWorksForAuthor_ContinuesWhenSupplementFails(t *testing.T) {
	primary := &mockWorksProvider{
		mockProvider: mockProvider{name: "ol", authorWorks: []models.Book{{ForeignID: "OL1W", Title: "Dune", ImageURL: "cover"}}},
	}
	hardcover := &mockAuthorWorksByNameProvider{
		mockProvider:         mockProvider{name: "hardcover"},
		authorWorksByNameErr: errors.New("hardcover unavailable"),
	}
	agg := &Aggregator{
		primary:   primary,
		enrichers: []Provider{hardcover},
		cache:     newTTLCache(time.Minute),
	}

	got, err := agg.GetAuthorWorksForAuthor(context.Background(), models.Author{ForeignID: "OL123A", Name: "Frank Herbert"})
	if err != nil {
		t.Fatalf("GetAuthorWorksForAuthor: %v", err)
	}
	if len(got) != 1 || got[0].ForeignID != "OL1W" {
		t.Fatalf("expected primary result after supplement failure, got %+v", got)
	}
}

func TestAggregator_GetAuthorWorksForAuthor_DoesNotCacheUnconfiguredSupplement(t *testing.T) {
	primary := &mockWorksProvider{
		mockProvider: mockProvider{name: "ol", authorWorks: []models.Book{
			{ForeignID: "OL1W", Title: "Dune", ImageURL: "cover", MetadataProvider: "openlibrary"},
		}},
	}
	hardcover := &mockAuthorWorksByNameProvider{
		mockProvider:         mockProvider{name: "hardcover"},
		authorWorksByNameErr: ErrProviderNotConfigured,
	}
	agg := &Aggregator{
		primary:   primary,
		enrichers: []Provider{hardcover},
		cache:     newTTLCache(time.Minute),
	}

	got, err := agg.GetAuthorWorksForAuthor(context.Background(), models.Author{ForeignID: "OL123A", Name: "Frank Herbert"})
	if err != nil {
		t.Fatalf("GetAuthorWorksForAuthor: %v", err)
	}
	if len(got) != 1 || got[0].ForeignID != "OL1W" {
		t.Fatalf("expected primary-only result, got %+v", got)
	}

	hardcover.authorWorksByNameErr = nil
	hardcover.authorWorksByName = []models.Book{{ForeignID: "hc:children-of-dune", Title: "Children of Dune", MetadataProvider: "hardcover"}}
	got, err = agg.GetAuthorWorksForAuthor(context.Background(), models.Author{ForeignID: "OL123A", Name: "Frank Herbert"})
	if err != nil {
		t.Fatalf("GetAuthorWorksForAuthor after config: %v", err)
	}
	if hardcover.calls != 2 {
		t.Fatalf("supplement calls = %d, want 2", hardcover.calls)
	}
	if len(got) != 2 || got[1].ForeignID != "hc:children-of-dune" {
		t.Fatalf("expected supplemental result after config, got %+v", got)
	}
}

func TestAggregator_GetAuthorWorksForAuthor_DoesNotCacheFailedSupplement(t *testing.T) {
	primary := &mockWorksProvider{
		mockProvider: mockProvider{name: "ol", authorWorks: []models.Book{
			{ForeignID: "OL1W", Title: "Dune", ImageURL: "cover", MetadataProvider: "openlibrary"},
		}},
	}
	hardcover := &mockAuthorWorksByNameProvider{
		mockProvider:         mockProvider{name: "hardcover"},
		authorWorksByNameErr: errors.New("hardcover unavailable"),
	}
	agg := &Aggregator{
		primary:   primary,
		enrichers: []Provider{hardcover},
		cache:     newTTLCache(time.Minute),
	}

	got, err := agg.GetAuthorWorksForAuthor(context.Background(), models.Author{ForeignID: "OL123A", Name: "Frank Herbert"})
	if err != nil {
		t.Fatalf("GetAuthorWorksForAuthor: %v", err)
	}
	if len(got) != 1 || got[0].ForeignID != "OL1W" {
		t.Fatalf("expected primary-only result, got %+v", got)
	}

	hardcover.authorWorksByNameErr = nil
	hardcover.authorWorksByName = []models.Book{{ForeignID: "hc:dune-messiah", Title: "Dune Messiah", MetadataProvider: "hardcover"}}
	got, err = agg.GetAuthorWorksForAuthor(context.Background(), models.Author{ForeignID: "OL123A", Name: "Frank Herbert"})
	if err != nil {
		t.Fatalf("GetAuthorWorksForAuthor after recovery: %v", err)
	}
	if hardcover.calls != 2 {
		t.Fatalf("supplement calls = %d, want 2", hardcover.calls)
	}
	if len(got) != 2 || got[1].ForeignID != "hc:dune-messiah" {
		t.Fatalf("expected supplemental result after recovery, got %+v", got)
	}
}

func TestAggregator_GetAuthorWorksForAuthor_CachesSuccessfulSupplement(t *testing.T) {
	primary := &mockWorksProvider{
		mockProvider: mockProvider{name: "ol", authorWorks: []models.Book{
			{ForeignID: "OL1W", Title: "Dune", ImageURL: "cover", MetadataProvider: "openlibrary"},
		}},
	}
	hardcover := &mockAuthorWorksByNameProvider{
		mockProvider: mockProvider{name: "hardcover"},
		authorWorksByName: []models.Book{
			{ForeignID: "hc:children-of-dune", Title: "Children of Dune", MetadataProvider: "hardcover"},
		},
	}
	agg := &Aggregator{
		primary:   primary,
		enrichers: []Provider{hardcover},
		cache:     newTTLCache(time.Minute),
	}

	got, err := agg.GetAuthorWorksForAuthor(context.Background(), models.Author{ForeignID: "OL123A", Name: "Frank Herbert"})
	if err != nil {
		t.Fatalf("GetAuthorWorksForAuthor: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("expected merged works, got %+v", got)
	}

	hardcover.authorWorksByName = nil
	got, err = agg.GetAuthorWorksForAuthor(context.Background(), models.Author{ForeignID: "OL123A", Name: "Frank Herbert"})
	if err != nil {
		t.Fatalf("GetAuthorWorksForAuthor cached: %v", err)
	}
	if hardcover.calls != 1 {
		t.Fatalf("supplement calls = %d, want 1", hardcover.calls)
	}
	if len(got) != 2 || got[1].ForeignID != "hc:children-of-dune" {
		t.Fatalf("expected cached supplemental result, got %+v", got)
	}
}

func TestAggregator_EnrichAudiobook_NonAudiobook(t *testing.T) {
	agg := newTestAggregator(&mockProvider{name: "ol"})
	book := &models.Book{Title: "Ebook", MediaType: models.MediaTypeEbook, ASIN: "B001"}
	if err := agg.EnrichAudiobook(context.Background(), book); err != nil {
		t.Fatalf("EnrichAudiobook (ebook): %v", err)
	}
}

func TestAggregator_EnrichAudiobook_NilBook(t *testing.T) {
	agg := newTestAggregator(&mockProvider{name: "ol"})
	if err := agg.EnrichAudiobook(context.Background(), nil); err != nil {
		t.Fatalf("EnrichAudiobook(nil): %v", err)
	}
}

func TestAggregator_EnrichAudiobook_NoASIN(t *testing.T) {
	agg := newTestAggregator(&mockProvider{name: "ol"})
	book := &models.Book{Title: "Audiobook", MediaType: models.MediaTypeAudiobook, ASIN: ""}
	if err := agg.EnrichAudiobook(context.Background(), book); err != nil {
		t.Fatalf("EnrichAudiobook (no ASIN): %v", err)
	}
}

// TestAggregator_GetAuthorAudiobooks_Unconfigured verifies the nil-audible
// path used by every test aggregator returns an empty result instead of
// panicking — the aggregator is constructed without an audible.Client in
// unit tests, and callers rely on a safe fallback.
func TestAggregator_GetAuthorAudiobooks_Unconfigured(t *testing.T) {
	agg := newTestAggregator(&mockProvider{name: "ol"})
	books, err := agg.GetAuthorAudiobooks(context.Background(), "Frank Herbert")
	if err != nil {
		t.Fatalf("GetAuthorAudiobooks (nil client): %v", err)
	}
	if books != nil {
		t.Errorf("want nil, got %v", books)
	}
}

// TestAggregator_GetAuthorAudiobooks_EmptyName guards against the trivial
// case where an unnamed author triggers an unfiltered Audible browse.
func TestAggregator_GetAuthorAudiobooks_EmptyName(t *testing.T) {
	agg := newTestAggregator(&mockProvider{name: "ol"})
	books, err := agg.GetAuthorAudiobooks(context.Background(), "   ")
	if err != nil {
		t.Fatalf("GetAuthorAudiobooks (empty): %v", err)
	}
	if books != nil {
		t.Errorf("want nil, got %v", books)
	}
}

func TestAggregator_EnrichBook_SkipsOnSearchError(t *testing.T) {
	primary := &mockProvider{
		name:    "ol",
		getBook: &models.Book{Title: "Error Test", Description: "x"},
	}
	enricher := &mockProvider{
		name:          "hc",
		searchBookErr: errors.New("hardcover unavailable"),
	}
	agg := &Aggregator{
		primary:   primary,
		enrichers: []Provider{enricher},
		cache:     newTTLCache(time.Minute),
	}

	got, err := agg.GetBook(context.Background(), "OL002W")
	if err != nil {
		t.Fatalf("GetBook: %v", err)
	}
	// Description should remain unchanged since enricher errored.
	if got.Description != "x" {
		t.Errorf("description changed unexpectedly: %q", got.Description)
	}
}

func TestAggregator_enrichBook_FillsCoverWhenMissing(t *testing.T) {
	enricher := &mockProvider{
		name:        "gb",
		searchBooks: []models.Book{{Description: "A description.", ImageURL: "https://books.google.com/cover.jpg"}},
	}
	agg := &Aggregator{enrichers: []Provider{enricher}, cache: newTTLCache(time.Minute)}

	book := &models.Book{Title: "Sapiens", ImageURL: ""}
	agg.enrichBook(context.Background(), book)
	if book.ImageURL != "https://books.google.com/cover.jpg" {
		t.Errorf("expected cover to be filled from enricher, got %q", book.ImageURL)
	}
}

func TestAggregator_enrichBook_KeepsExistingCover(t *testing.T) {
	enricher := &mockProvider{
		name:        "gb",
		searchBooks: []models.Book{{ImageURL: "https://books.google.com/other.jpg"}},
	}
	agg := &Aggregator{enrichers: []Provider{enricher}, cache: newTTLCache(time.Minute)}

	existing := "https://covers.openlibrary.org/b/id/123-L.jpg"
	book := &models.Book{Title: "Dune", ImageURL: existing}
	agg.enrichBook(context.Background(), book)
	if book.ImageURL != existing {
		t.Errorf("existing cover should not be replaced, got %q", book.ImageURL)
	}
}

func TestAggregator_GetBook_NoCover_EnrichedFromProvider(t *testing.T) {
	// Book has a long description so the old trigger wouldn't fire — but
	// ImageURL is empty so enrichment must still run.
	primary := &mockProvider{
		name: "ol",
		getBook: &models.Book{
			Title:       "21 Lessons for the 21st Century",
			Description: "A sufficiently long description that would previously have skipped enrichment entirely.",
			ImageURL:    "",
		},
	}
	enricher := &mockProvider{
		name:        "gb",
		searchBooks: []models.Book{{ImageURL: "https://books.google.com/cover-en.jpg"}},
	}
	agg := &Aggregator{primary: primary, enrichers: []Provider{enricher}, cache: newTTLCache(time.Minute)}

	got, err := agg.GetBook(context.Background(), "OL123W")
	if err != nil {
		t.Fatalf("GetBook: %v", err)
	}
	if got.ImageURL != "https://books.google.com/cover-en.jpg" {
		t.Errorf("expected cover from enricher, got %q", got.ImageURL)
	}
}

func TestAggregator_GetAuthorWorks_CoversEnrichedForMissingOnes(t *testing.T) {
	primary := &mockWorksProvider{
		mockProvider: mockProvider{
			name: "ol",
			authorWorks: []models.Book{
				{ForeignID: "OL1W", Title: "Sapiens", ImageURL: ""},
				{ForeignID: "OL2W", Title: "Homo Deus", ImageURL: "https://covers.openlibrary.org/b/id/999-L.jpg"},
			},
		},
	}
	enricher := &mockProvider{
		name:        "gb",
		searchBooks: []models.Book{{ImageURL: "https://books.google.com/sapiens.jpg"}},
	}
	agg := &Aggregator{primary: primary, enrichers: []Provider{enricher}, cache: newTTLCache(time.Minute)}

	got, err := agg.GetAuthorWorks(context.Background(), "OL123A")
	if err != nil {
		t.Fatalf("GetAuthorWorks: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("expected 2 works, got %d", len(got))
	}
	// Book without OL cover should get enriched cover
	if got[0].ImageURL != "https://books.google.com/sapiens.jpg" {
		t.Errorf("Sapiens: expected enriched cover, got %q", got[0].ImageURL)
	}
	// Book with OL cover should keep it
	if got[1].ImageURL != "https://covers.openlibrary.org/b/id/999-L.jpg" {
		t.Errorf("Homo Deus: expected OL cover preserved, got %q", got[1].ImageURL)
	}
}

func TestAggregator_GetAuthorWorks_NoEnrichersNoCovers(t *testing.T) {
	// With no enrichers, works without covers stay coverless — no panic.
	primary := &mockWorksProvider{
		mockProvider: mockProvider{
			name:        "ol",
			authorWorks: []models.Book{{ForeignID: "OL1W", Title: "No Cover Book", ImageURL: ""}},
		},
	}
	agg := &Aggregator{primary: primary, cache: newTTLCache(time.Minute)}

	got, err := agg.GetAuthorWorks(context.Background(), "OL456A")
	if err != nil {
		t.Fatalf("GetAuthorWorks: %v", err)
	}
	if got[0].ImageURL != "" {
		t.Errorf("expected empty cover with no enrichers, got %q", got[0].ImageURL)
	}
}

func TestTTLCache_SetAndGet(t *testing.T) {
	c := newTTLCache(time.Minute)
	c.set("key1", "value1")
	v, ok := c.get("key1")
	if !ok {
		t.Fatal("expected cache hit")
	}
	if v.(string) != "value1" {
		t.Errorf("want 'value1', got %q", v)
	}
}

func TestTTLCache_Miss(t *testing.T) {
	c := newTTLCache(time.Minute)
	_, ok := c.get("missing")
	if ok {
		t.Error("expected cache miss for unknown key")
	}
}

func TestTTLCache_Expiry(t *testing.T) {
	c := newTTLCache(time.Nanosecond)
	c.set("k", "v")
	time.Sleep(2 * time.Millisecond)
	_, ok := c.get("k")
	if ok {
		t.Error("expected cache miss after TTL expiry")
	}
}

func TestTTLCache_Cleanup(t *testing.T) {
	c := newTTLCache(time.Nanosecond)
	c.set("a", 1)
	c.set("b", 2)
	time.Sleep(2 * time.Millisecond)
	c.cleanup()

	c.mu.RLock()
	n := len(c.items)
	c.mu.RUnlock()
	if n != 0 {
		t.Errorf("expected 0 items after cleanup, got %d", n)
	}
}

// newTestAggregator creates an aggregator with a real TTL cache and no enrichers.
func newTestAggregator(primary Provider) *Aggregator {
	return &Aggregator{
		primary: primary,
		cache:   newTTLCache(time.Minute),
	}
}

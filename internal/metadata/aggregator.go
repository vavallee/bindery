// Package metadata aggregates book and author data from multiple public
// sources (OpenLibrary, Google Books, Hardcover) behind a unified interface.
package metadata

import (
	"context"
	"errors"
	"log/slog"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/vavallee/bindery/internal/indexer"
	"github.com/vavallee/bindery/internal/metadata/audible"
	"github.com/vavallee/bindery/internal/metadata/audnex"
	"github.com/vavallee/bindery/internal/models"
)

// Aggregator fans out requests to multiple providers and merges results.
// OpenLibrary is always the primary source. Other providers enrich the data.
type Aggregator struct {
	primary   Provider
	enrichers []Provider
	audnex    *audnex.Client
	audible   *audible.Client
	cache     *ttlCache
}

// NewAggregator creates an aggregator with OpenLibrary as primary and optional enrichers.
func NewAggregator(primary Provider, enrichers ...Provider) *Aggregator {
	return &Aggregator{
		primary:   primary,
		enrichers: enrichers,
		audnex:    audnex.New(""),
		audible:   audible.New(),
		cache:     newTTLCache(24 * time.Hour),
	}
}

// EnrichAudiobook fills narrator, duration, and cover from audnex when a
// book has audiobook audio (MediaType=audiobook or both) and a known ASIN.
// No-op otherwise.
func (a *Aggregator) EnrichAudiobook(ctx context.Context, book *models.Book) error {
	if book == nil || book.ASIN == "" {
		return nil
	}
	if book.MediaType != models.MediaTypeAudiobook && book.MediaType != models.MediaTypeBoth {
		return nil
	}
	b, err := a.audnex.GetBook(ctx, book.ASIN)
	if err != nil || b == nil {
		return err
	}
	if narr := b.NarratorList(); narr != "" {
		book.Narrator = narr
	}
	if dur := b.DurationSeconds(); dur > 0 {
		book.DurationSeconds = dur
	}
	if book.ImageURL == "" && b.Image != "" {
		book.ImageURL = b.Image
	}
	if book.Description == "" && b.Summary != "" {
		book.Description = b.Summary
	}
	return nil
}

// GetAuthorAudiobooks queries the Audible catalogue directly for the given
// author name. Returned books carry MediaType=audiobook, an ASIN, and a
// normalized language code; the caller applies the active metadata
// profile's allowed_languages filter alongside OpenLibrary-sourced books.
//
// Callers use this as a supplement to GetAuthorWorks — neither OpenLibrary
// nor Hardcover has full Audible ASIN cross-referencing, so prolific
// authors (Sanderson, King, etc.) are missing a large share of their
// narrated catalogue without a direct Audible source.
//
// Returns an empty slice when the audible client is unconfigured (test
// aggregators) rather than nil-derefing. Errors propagate so the caller
// can log them without failing the broader ingestion.
func (a *Aggregator) GetAuthorAudiobooks(ctx context.Context, authorName string) ([]models.Book, error) {
	if a.audible == nil {
		return nil, nil
	}
	authorName = strings.TrimSpace(authorName)
	if authorName == "" {
		return nil, nil
	}
	key := "audible-author:" + strings.ToLower(authorName)
	if cached, ok := a.cache.get(key); ok {
		return cached.([]models.Book), nil
	}
	books, err := a.audible.SearchBooksByAuthor(ctx, authorName)
	if err != nil {
		return nil, err
	}
	if books == nil {
		books = []models.Book{}
	}
	a.cache.set(key, books)
	return books, nil
}

func (a *Aggregator) SearchAuthors(ctx context.Context, query string) ([]models.Author, error) {
	return a.primary.SearchAuthors(ctx, query)
}

func (a *Aggregator) SearchBooks(ctx context.Context, query string) ([]models.Book, error) {
	return a.primary.SearchBooks(ctx, query)
}

func (a *Aggregator) GetAuthor(ctx context.Context, foreignID string) (*models.Author, error) {
	key := "author:" + foreignID
	if cached, ok := a.cache.get(key); ok {
		return cached.(*models.Author), nil
	}

	author, err := a.primary.GetAuthor(ctx, foreignID)
	if err != nil {
		return nil, err
	}
	a.cache.set(key, author)
	return author, nil
}

type worksProvider interface {
	GetAuthorWorks(ctx context.Context, authorForeignID string) ([]models.Book, error)
}

type authorWorksByNameProvider interface {
	Name() string
	GetAuthorWorksByName(ctx context.Context, authorName string) ([]models.Book, error)
}

// GetAuthorWorks fetches all works by an author using the dedicated primary
// provider endpoint. It retains the legacy foreign-ID-only behavior for tests
// and existing callers; author ingestion should use GetAuthorWorksForAuthor so
// enrichers can run author-scoped supplemental queries.
func (a *Aggregator) GetAuthorWorks(ctx context.Context, authorForeignID string) ([]models.Book, error) {
	key := "authorworks:" + authorForeignID
	if cached, ok := a.cache.get(key); ok {
		return cached.([]models.Book), nil
	}

	books, err := a.primaryAuthorWorks(ctx, authorForeignID)
	if err != nil {
		return nil, err
	}
	a.enrichMissingAuthorWorkCovers(ctx, books)
	a.cache.set(key, books)
	return books, nil
}

// GetAuthorWorksForAuthor fetches the primary provider's author works and
// merges any author-scoped supplemental catalogs from enrichers before falling
// back to per-title cover enrichment for remaining gaps.
func (a *Aggregator) GetAuthorWorksForAuthor(ctx context.Context, author models.Author) ([]models.Book, error) {
	key := "authorworks-author:" + author.ForeignID + ":" + strings.ToLower(strings.TrimSpace(author.Name))
	if cached, ok := a.cache.get(key); ok {
		return cached.([]models.Book), nil
	}

	books, err := a.primaryAuthorWorks(ctx, author.ForeignID)
	if err != nil {
		return nil, err
	}

	authorName := strings.TrimSpace(author.Name)
	supplementsComplete := true
	if authorName != "" {
		for _, provider := range a.authorWorksByNameProviders() {
			supplemental, err := provider.GetAuthorWorksByName(ctx, authorName)
			if err != nil {
				supplementsComplete = false
				if errors.Is(err, ErrProviderNotConfigured) {
					continue
				}
				slog.Warn("author works supplement failed", "provider", provider.Name(), "author", authorName, "error", err)
				continue
			}
			if len(supplemental) == 0 {
				continue
			}
			books = mergeAuthorWorks(books, supplemental)
		}
	}

	a.enrichMissingAuthorWorkCovers(ctx, books)
	if supplementsComplete {
		a.cache.set(key, books)
	}
	return books, nil
}

func (a *Aggregator) primaryAuthorWorks(ctx context.Context, authorForeignID string) ([]models.Book, error) {
	if wp, ok := a.primary.(worksProvider); ok {
		return wp.GetAuthorWorks(ctx, authorForeignID)
	}
	return a.primary.SearchBooks(ctx, authorForeignID)
}

func (a *Aggregator) authorWorksByNameProviders() []authorWorksByNameProvider {
	if a == nil {
		return nil
	}
	providers := make([]authorWorksByNameProvider, 0, len(a.enrichers))
	for _, enricher := range a.enrichers {
		if provider, ok := enricher.(authorWorksByNameProvider); ok {
			providers = append(providers, provider)
		}
	}
	return providers
}

func (a *Aggregator) enrichMissingAuthorWorkCovers(ctx context.Context, books []models.Book) {
	for i := range books {
		if books[i].ImageURL == "" {
			a.enrichBook(ctx, &books[i])
		}
	}
}

func mergeAuthorWorks(primary, supplemental []models.Book) []models.Book {
	books := make([]models.Book, 0, len(primary)+len(supplemental))
	index := make(map[string]int, len(primary)+len(supplemental))
	for _, book := range primary {
		key := authorWorkMergeKey(book.Title)
		if key != "" {
			if _, exists := index[key]; !exists {
				index[key] = len(books)
			}
		}
		books = append(books, book)
	}
	for _, book := range supplemental {
		key := authorWorkMergeKey(book.Title)
		if key == "" {
			continue
		}
		if pos, ok := index[key]; ok {
			mergeAuthorWorkMetadata(&books[pos], book)
			continue
		}
		index[key] = len(books)
		books = append(books, book)
	}
	return books
}

func authorWorkMergeKey(title string) string {
	key := indexer.NormalizeTitleForDedup(title)
	if key != "" {
		return key
	}
	return strings.ToLower(strings.TrimSpace(title))
}

func mergeAuthorWorkMetadata(dst *models.Book, src models.Book) {
	if dst.ImageURL == "" {
		dst.ImageURL = src.ImageURL
	}
	if dst.Description == "" {
		dst.Description = src.Description
	}
	if dst.AverageRating == 0 {
		dst.AverageRating = src.AverageRating
	}
	if dst.RatingsCount == 0 {
		dst.RatingsCount = src.RatingsCount
	}
	if dst.ReleaseDate == nil {
		dst.ReleaseDate = src.ReleaseDate
	}
	if len(dst.Genres) == 0 {
		dst.Genres = src.Genres
	}
	if dst.DurationSeconds == 0 {
		dst.DurationSeconds = src.DurationSeconds
	}
	if dst.ASIN == "" {
		dst.ASIN = src.ASIN
	}
	if dst.MediaType == "" {
		dst.MediaType = src.MediaType
	}
}

func (a *Aggregator) GetBook(ctx context.Context, foreignID string) (*models.Book, error) {
	key := "book:" + foreignID
	if cached, ok := a.cache.get(key); ok {
		return cached.(*models.Book), nil
	}

	book, err := a.primary.GetBook(ctx, foreignID)
	if err != nil {
		return nil, err
	}

	// Enrich from secondary providers if description is sparse or cover is missing.
	if len(book.Description) < 50 || book.ImageURL == "" {
		a.enrichBook(ctx, book)
	}

	a.cache.set(key, book)
	return book, nil
}

func (a *Aggregator) GetEditions(ctx context.Context, bookForeignID string) ([]models.Edition, error) {
	key := "editions:" + bookForeignID
	if cached, ok := a.cache.get(key); ok {
		return cached.([]models.Edition), nil
	}

	editions, err := a.primary.GetEditions(ctx, bookForeignID)
	if err != nil {
		return nil, err
	}
	a.cache.set(key, editions)
	return editions, nil
}

func (a *Aggregator) GetBookByISBN(ctx context.Context, isbn string) (*models.Book, error) {
	key := "isbn:" + isbn
	if cached, ok := a.cache.get(key); ok {
		return cached.(*models.Book), nil
	}

	book, err := a.primary.GetBookByISBN(ctx, isbn)
	if err != nil {
		return nil, err
	}

	if book != nil && len(book.Description) < 50 {
		a.enrichBook(ctx, book)
	}

	a.cache.set(key, book)
	return book, nil
}

// SearchSeries queries metadata providers that expose series catalog search.
func (a *Aggregator) SearchSeries(ctx context.Context, query string, limit int) ([]SeriesSearchResult, error) {
	query = strings.TrimSpace(query)
	if query == "" {
		return nil, nil
	}
	if limit <= 0 {
		limit = 10
	}
	key := "series-search:" + strings.ToLower(query) + ":" + strconv.Itoa(limit)
	if cached, ok := a.cache.get(key); ok {
		return cached.([]SeriesSearchResult), nil
	}
	var lastErr error
	for _, provider := range a.seriesCatalogProviders() {
		results, err := provider.SearchSeries(ctx, query, limit)
		if err != nil {
			lastErr = err
			slog.Debug("series search failed", "error", err)
			continue
		}
		if results == nil {
			results = []SeriesSearchResult{}
		}
		a.cache.set(key, results)
		return results, nil
	}
	return nil, lastErr
}

// GetSeriesCatalog fetches the ordered book catalog for a provider series.
func (a *Aggregator) GetSeriesCatalog(ctx context.Context, foreignID string) (*SeriesCatalog, error) {
	foreignID = strings.TrimSpace(foreignID)
	if foreignID == "" {
		return nil, nil
	}
	key := "series-catalog:" + foreignID
	if cached, ok := a.cache.get(key); ok {
		return cached.(*SeriesCatalog), nil
	}
	var lastErr error
	for _, provider := range a.seriesCatalogProviders() {
		catalog, err := provider.GetSeriesCatalog(ctx, foreignID)
		if err != nil {
			lastErr = err
			slog.Debug("series catalog failed", "foreignID", foreignID, "error", err)
			continue
		}
		if catalog != nil {
			a.cache.set(key, catalog)
		}
		return catalog, nil
	}
	return nil, lastErr
}

func (a *Aggregator) seriesCatalogProviders() []SeriesCatalogProvider {
	if a == nil {
		return nil
	}
	providers := make([]SeriesCatalogProvider, 0, len(a.enrichers)+1)
	if provider, ok := a.primary.(SeriesCatalogProvider); ok {
		providers = append(providers, provider)
	}
	for _, enricher := range a.enrichers {
		if provider, ok := enricher.(SeriesCatalogProvider); ok {
			providers = append(providers, provider)
		}
	}
	return providers
}

// enrichBook tries to fill in missing data from secondary providers.
// It fills Description, AverageRating/RatingsCount, and ImageURL when
// the primary provider (OpenLibrary) left them empty or sparse.
func (a *Aggregator) enrichBook(ctx context.Context, book *models.Book) {
	for _, enricher := range a.enrichers {
		enriched, err := enricher.SearchBooks(ctx, book.Title)
		if err != nil {
			slog.Debug("enrichment failed", "provider", enricher.Name(), "error", err)
			continue
		}
		if len(enriched) == 0 {
			continue
		}
		e := enriched[0]
		if len(e.Description) > len(book.Description) {
			book.Description = e.Description
			slog.Debug("enriched description", "provider", enricher.Name(), "book", book.Title)
		}
		if book.AverageRating == 0 && e.AverageRating > 0 {
			book.AverageRating = e.AverageRating
			book.RatingsCount = e.RatingsCount
		}
		if book.ImageURL == "" && e.ImageURL != "" {
			book.ImageURL = e.ImageURL
			slog.Debug("enriched cover", "provider", enricher.Name(), "book", book.Title)
		}
	}
}

// ttlCache is a simple in-process cache with TTL expiry.
type ttlCache struct {
	mu    sync.RWMutex
	items map[string]cacheItem
	ttl   time.Duration
}

type cacheItem struct {
	value     interface{}
	expiresAt time.Time
}

func newTTLCache(ttl time.Duration) *ttlCache {
	c := &ttlCache{
		items: make(map[string]cacheItem),
		ttl:   ttl,
	}
	// Background cleanup every hour
	go func() {
		ticker := time.NewTicker(1 * time.Hour)
		defer ticker.Stop()
		for range ticker.C {
			c.cleanup()
		}
	}()
	return c
}

func (c *ttlCache) get(key string) (interface{}, bool) {
	c.mu.RLock()
	defer c.mu.RUnlock()

	item, ok := c.items[key]
	if !ok || time.Now().After(item.expiresAt) {
		return nil, false
	}
	return item.value, true
}

func (c *ttlCache) set(key string, value interface{}) {
	c.mu.Lock()
	defer c.mu.Unlock()

	c.items[key] = cacheItem{
		value:     value,
		expiresAt: time.Now().Add(c.ttl),
	}
}

func (c *ttlCache) cleanup() {
	c.mu.Lock()
	defer c.mu.Unlock()

	now := time.Now()
	for k, v := range c.items {
		if now.After(v.expiresAt) {
			delete(c.items, k)
		}
	}
}

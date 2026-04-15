// Package metadata aggregates book and author data from multiple public
// sources (OpenLibrary, Google Books, Hardcover) behind a unified interface.
package metadata

import (
	"context"
	"log/slog"
	"sync"
	"time"

	"github.com/vavallee/bindery/internal/metadata/audnex"
	"github.com/vavallee/bindery/internal/models"
)

// Aggregator fans out requests to multiple providers and merges results.
// OpenLibrary is always the primary source. Other providers enrich the data.
type Aggregator struct {
	primary   Provider
	enrichers []Provider
	audnex    *audnex.Client
	cache     *ttlCache
}

// NewAggregator creates an aggregator with OpenLibrary as primary and optional enrichers.
func NewAggregator(primary Provider, enrichers ...Provider) *Aggregator {
	return &Aggregator{
		primary:   primary,
		enrichers: enrichers,
		audnex:    audnex.New(""),
		cache:     newTTLCache(24 * time.Hour),
	}
}

// EnrichAudiobook fills narrator, duration, and cover from audnex when a
// book has MediaType=audiobook and a known ASIN. No-op otherwise.
func (a *Aggregator) EnrichAudiobook(ctx context.Context, book *models.Book) error {
	if book == nil || book.MediaType != models.MediaTypeAudiobook || book.ASIN == "" {
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

// GetAuthorWorks fetches all works by an author using the dedicated OL endpoint.
func (a *Aggregator) GetAuthorWorks(ctx context.Context, authorForeignID string) ([]models.Book, error) {
	key := "authorworks:" + authorForeignID
	if cached, ok := a.cache.get(key); ok {
		return cached.([]models.Book), nil
	}

	// Use the OL-specific method if available
	type worksProvider interface {
		GetAuthorWorks(ctx context.Context, authorForeignID string) ([]models.Book, error)
	}
	if wp, ok := a.primary.(worksProvider); ok {
		books, err := wp.GetAuthorWorks(ctx, authorForeignID)
		if err != nil {
			return nil, err
		}
		// Enrich covers for works that OL's works endpoint left without one.
		// OpenLibrary attaches cover IDs to editions, not always to works, so
		// many works come back coverless. Google Books and Hardcover have much
		// higher cover coverage and tend to return the dominant-language edition.
		for i := range books {
			if books[i].ImageURL == "" {
				a.enrichBook(ctx, &books[i])
			}
		}
		a.cache.set(key, books)
		return books, nil
	}

	// Fallback to search
	return a.primary.SearchBooks(ctx, authorForeignID)
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

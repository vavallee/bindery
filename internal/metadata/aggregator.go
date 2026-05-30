// Package metadata aggregates book and author data from multiple public
// sources (OpenLibrary, Google Books, Hardcover) behind a unified interface.
package metadata

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/vavallee/bindery/internal/isbnutil"
	"github.com/vavallee/bindery/internal/metadata/audible"
	"github.com/vavallee/bindery/internal/metadata/audnex"
	"github.com/vavallee/bindery/internal/models"
)

// Aggregator fans out requests to multiple providers and merges results.
// OpenLibrary is always the primary source. Other providers enrich the data.
type Aggregator struct {
	primary   Provider
	enrichers []Provider
	audnex    AudnexBookClient
	audible   *audible.Client
	cache     *ttlCache
}

// AudnexBookClient is the narrow audnex capability the aggregator needs for
// ASIN-based audiobook metadata lookup.
type AudnexBookClient interface {
	GetBook(ctx context.Context, asin string) (*audnex.Book, error)
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

// WithAudnexClient replaces the default audnex client. Tests use this to keep
// ASIN canonicalization deterministic without reaching the network.
func (a *Aggregator) WithAudnexClient(client AudnexBookClient) *Aggregator {
	a.audnex = client
	return a
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

	provider := a.providerForForeignID(foreignID)
	if provider == nil {
		return nil, nil
	}
	author, err := provider.GetAuthor(ctx, foreignID)
	if err != nil {
		return nil, err
	}
	a.cache.set(key, author)
	return author, nil
}

func (a *Aggregator) GetBook(ctx context.Context, foreignID string) (*models.Book, error) {
	key := "book:" + foreignID
	if cached, ok := a.cache.get(key); ok {
		return cached.(*models.Book), nil
	}

	provider := a.providerForForeignID(foreignID)
	if provider == nil {
		return nil, nil
	}
	book, err := provider.GetBook(ctx, foreignID)
	if err != nil {
		return nil, err
	}
	if book == nil {
		a.cache.set(key, book)
		return nil, nil
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

	provider := a.providerForForeignID(bookForeignID)
	if provider == nil {
		return nil, nil
	}
	editions, err := provider.GetEditions(ctx, bookForeignID)
	if err != nil {
		return nil, err
	}
	a.cache.set(key, editions)
	return editions, nil
}

// GetEditionsFromProvider fetches editions from a named provider, bypassing
// prefix-based routing. This is used when callers know the provider from UI
// state but the stored foreign ID is an unprefixed provider-native value.
func (a *Aggregator) GetEditionsFromProvider(ctx context.Context, providerName, bookForeignID string) ([]models.Edition, error) {
	providerName = strings.TrimSpace(strings.ToLower(providerName))
	bookForeignID = strings.TrimSpace(bookForeignID)
	if providerName == "" || bookForeignID == "" {
		return nil, nil
	}
	key := "editions-provider:" + providerName + ":" + bookForeignID
	if cached, ok := a.cache.get(key); ok {
		return cached.([]models.Edition), nil
	}

	for _, provider := range a.providers() {
		if provider == nil || normalizedProviderName(provider.Name()) != normalizedProviderName(providerName) {
			continue
		}
		editions, err := provider.GetEditions(ctx, bookForeignID)
		if err != nil {
			return nil, err
		}
		a.cache.set(key, editions)
		return editions, nil
	}
	return nil, ErrProviderNotConfigured
}

func (a *Aggregator) GetBookByISBN(ctx context.Context, isbn string) (*models.Book, error) {
	isbn = isbnutil.Normalize(isbn)
	key := "isbn:" + isbn
	if cached, ok := a.cache.get(key); ok {
		return cached.(*models.Book), nil
	}

	var errs []error
	skippedUnconfigured := false
	providers := a.providers()
	var primaryFallback *models.Book
	var firstFallback *models.Book
	for idx, provider := range providers {
		if provider == nil {
			continue
		}
		book, err := provider.GetBookByISBN(ctx, isbn)
		if err != nil {
			if errors.Is(err, ErrProviderNotConfigured) {
				skippedUnconfigured = true
				slog.Debug("isbn provider not configured", "provider", provider.Name())
				continue
			}
			errs = append(errs, fmt.Errorf("%s: %w", provider.Name(), err))
			slog.Debug("isbn lookup provider failed", "provider", provider.Name(), "error", err)
			continue
		}
		if book == nil {
			continue
		}
		if canonical, status := a.lookupCanonicalPrimaryBook(ctx, isbn, *book); status != canonicalPrimaryBookNoMatch {
			if status == canonicalPrimaryBookMatched {
				book = canonical
				return a.cacheISBNBook(ctx, key, book), nil
			}
			if idx > 0 || len(providers) == 1 {
				return a.cacheISBNBook(ctx, key, book), nil
			}
		}
		if firstFallback == nil {
			firstFallback = book
		}
		if idx == 0 && len(providers) > 1 {
			primaryFallback = book
			continue
		}
		if primaryFallback != nil {
			continue
		}
		return a.cacheISBNBook(ctx, key, book), nil
	}

	if primaryFallback != nil {
		return a.cacheISBNBook(ctx, key, primaryFallback), nil
	}
	if firstFallback != nil {
		return a.cacheISBNBook(ctx, key, firstFallback), nil
	}

	if len(errs) > 0 {
		return nil, errors.Join(errs...)
	}
	var noBook *models.Book
	if !skippedUnconfigured {
		a.cache.set(key, noBook)
	}
	return nil, nil
}

func (a *Aggregator) cacheISBNBook(ctx context.Context, key string, book *models.Book) *models.Book {
	if book != nil && len(book.Description) < 50 {
		// Try fetching the full record from the provider before falling back to
		// enrichers. ISBN search results are often lightweight (title + ForeignID
		// only); GetBook returns the canonical description, cover, etc.
		if book.ForeignID != "" {
			if provider := a.providerForForeignID(book.ForeignID); provider != nil {
				if full, err := provider.GetBook(ctx, book.ForeignID); err == nil && full != nil {
					if len(full.Description) > len(book.Description) {
						book.Description = full.Description
					}
					if book.ImageURL == "" && full.ImageURL != "" {
						book.ImageURL = full.ImageURL
					}
					if book.AverageRating == 0 && full.AverageRating > 0 {
						book.AverageRating = full.AverageRating
						book.RatingsCount = full.RatingsCount
					}
					if book.MetadataProvider == "" && full.MetadataProvider != "" {
						book.MetadataProvider = full.MetadataProvider
					}
					if book.Language == "" && full.Language != "" {
						book.Language = full.Language
					}
				}
			}
		}
		if len(book.Description) < 50 {
			a.enrichBook(ctx, book)
		}
	}
	a.cache.set(key, book)
	return book
}

// GetBookFromProvider fetches a single book by foreign ID from the named
// provider ("openlibrary" or "hardcover"). It bypasses the TTL cache so the
// rebind flow always gets a fresh record. Returns ErrProviderNotConfigured
// when no matching provider is found.
func (a *Aggregator) GetBookFromProvider(ctx context.Context, providerName, foreignID string) (*models.Book, error) {
	providerName = strings.TrimSpace(strings.ToLower(providerName))
	if a.primary.Name() == providerName {
		return a.primary.GetBook(ctx, foreignID)
	}
	for _, enricher := range a.enrichers {
		if enricher.Name() == providerName {
			return enricher.GetBook(ctx, foreignID)
		}
	}
	return nil, ErrProviderNotConfigured
}

// ResolveBookByISBN walks every provider (primary first, then enrichers) and
// returns the first hit whose author carries a usable foreignAuthorId. Used
// at add-time when the user picked a search result from a provider that
// doesn't expose author IDs (notably DNB), so we can fall back to a stronger
// provider for the author identity rather than synthesising an ID locally.
//
// Returns (nil, nil) when no provider has the ISBN — the caller should treat
// that as "couldn't resolve" and surface a friendly error to the user.
// A provider error on one source is logged at debug level and treated as a
// miss, so a single flaky provider doesn't block resolution.
func (a *Aggregator) ResolveBookByISBN(ctx context.Context, isbn string) (*models.Book, error) {
	providers := append([]Provider{a.primary}, a.enrichers...)
	for _, p := range providers {
		book, err := p.GetBookByISBN(ctx, isbn)
		if err != nil {
			slog.Debug("resolve isbn: provider failed", "provider", p.Name(), "isbn", isbn, "error", err)
			continue
		}
		if book == nil || book.Author == nil || book.Author.ForeignID == "" {
			continue
		}
		return book, nil
	}
	return nil, nil
}

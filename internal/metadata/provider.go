package metadata

import (
	"context"
	"errors"

	"github.com/vavallee/bindery/internal/models"
)

// ErrProviderNotConfigured signals that an optional provider capability was
// skipped because credentials or runtime configuration are missing.
var ErrProviderNotConfigured = errors.New("metadata provider not configured")

// Provider defines the interface that all metadata sources must implement.
type Provider interface {
	// Name returns the provider identifier (e.g. "openlibrary", "googlebooks").
	Name() string

	// SearchAuthors searches for authors by name.
	SearchAuthors(ctx context.Context, query string) ([]models.Author, error)

	// SearchBooks searches for books by title, author, or ISBN.
	SearchBooks(ctx context.Context, query string) ([]models.Book, error)

	// GetAuthor fetches a single author by their provider-specific foreign ID.
	GetAuthor(ctx context.Context, foreignID string) (*models.Author, error)

	// GetBook fetches a single book/work by its provider-specific foreign ID.
	GetBook(ctx context.Context, foreignID string) (*models.Book, error)

	// GetEditions fetches all editions for a book/work by its foreign ID.
	GetEditions(ctx context.Context, bookForeignID string) ([]models.Edition, error)

	// GetBookByISBN looks up a book by ISBN-13 or ISBN-10.
	GetBookByISBN(ctx context.Context, isbn string) (*models.Book, error)
}

// SeriesSearchResult is a provider-neutral series discovery result.
type SeriesSearchResult struct {
	ForeignID    string
	ProviderID   string
	Title        string
	AuthorName   string
	BookCount    int
	ReadersCount int
	Books        []string
}

// SeriesCatalog is an ordered provider catalog for a single series.
type SeriesCatalog struct {
	ForeignID  string
	ProviderID string
	Title      string
	AuthorName string
	BookCount  int
	Books      []SeriesCatalogBook
}

// SeriesCatalogBook is a provider book entry with its position in a series.
type SeriesCatalogBook struct {
	ForeignID  string
	ProviderID string
	Title      string
	Subtitle   string
	Position   string
	UsersCount int
	Book       models.Book
}

// SeriesCatalogProvider is an optional metadata provider capability used by
// importers that can safely link provider series without widening Provider.
type SeriesCatalogProvider interface {
	SearchSeries(ctx context.Context, query string, limit int) ([]SeriesSearchResult, error)
	GetSeriesCatalog(ctx context.Context, foreignID string) (*SeriesCatalog, error)
}

// CoverProvider is an optional capability for providers that can resolve a
// cover image URL from an ISBN out-of-band from their bibliographic
// records. DNB implements this — its MARC21 records carry no cover URL,
// but the MVB cover service (portal.dnb.de/opac/mvb/cover) serves a JPEG
// for many German-edition ISBNs. The aggregator's enrichBook falls back
// to this when no enricher has supplied an ImageURL.
//
// Implementations MUST return "" rather than an error when no cover is
// available — failures are best-effort and should not surface to the
// user. Implementations SHOULD validate the response (HTTP 2xx + image
// Content-Type) before returning a URL so callers can trust it.
type CoverProvider interface {
	CoverByISBN(ctx context.Context, isbn string) string
}

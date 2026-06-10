package metadata

import (
	"context"
	"errors"
	"log/slog"
	"strings"

	"github.com/vavallee/bindery/internal/indexer"
	"github.com/vavallee/bindery/internal/models"
)

type worksProvider interface {
	GetAuthorWorks(ctx context.Context, authorForeignID string) ([]models.Book, error)
}

// workLanguageFiller is the optional capability a provider implements when it
// can derive a work-level language for books that arrived without one (e.g.
// OpenLibrary, whose works carry no language and must be edition-sampled; #891).
type workLanguageFiller interface {
	FillMissingWorkLanguages(ctx context.Context, books []models.Book) int
}

// FillMissingAuthorWorkLanguages asks the primary provider to derive a language
// for any book in books that has Language=="" by edition-sampling, mutating the
// slice in place. It is a no-op when the primary provider lacks the capability.
//
// Callers should gate this on the active metadata profile actually restricting
// language: the edition sampling is bounded but still costs upstream
// round-trips, which are wasted when allowed_languages is "any" and every book
// passes the filter regardless of its language (#891).
func (a *Aggregator) FillMissingAuthorWorkLanguages(ctx context.Context, books []models.Book) int {
	if filler, ok := a.primary.(workLanguageFiller); ok {
		return filler.FillMissingWorkLanguages(ctx, books)
	}
	return 0
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
		return cloneBooks(cached.([]models.Book)), nil
	}

	books, err := a.rawPrimaryAuthorWorks(ctx, authorForeignID)
	if err != nil {
		return nil, err
	}
	a.enrichMissingAuthorWorkCovers(ctx, books)
	a.cache.set(key, cloneBooks(books))
	return books, nil
}

// GetAuthorWorksForAuthor fetches the primary provider's author works and
// merges any author-scoped supplemental catalogs from enrichers before falling
// back to per-title cover enrichment for remaining gaps.
func (a *Aggregator) GetAuthorWorksForAuthor(ctx context.Context, author models.Author) ([]models.Book, error) {
	key := "authorworks-author:" + author.ForeignID + ":" + strings.ToLower(strings.TrimSpace(author.Name))
	if cached, ok := a.cache.get(key); ok {
		return cloneBooks(cached.([]models.Book)), nil
	}

	books, err := a.rawPrimaryAuthorWorks(ctx, author.ForeignID)
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
		a.cache.set(key, cloneBooks(books))
	}
	return books, nil
}

func (a *Aggregator) rawPrimaryAuthorWorks(ctx context.Context, authorForeignID string) ([]models.Book, error) {
	key := "authorworks-raw:" + authorForeignID
	if cached, ok := a.cache.get(key); ok {
		return cloneBooks(cached.([]models.Book)), nil
	}

	books, err := a.primaryAuthorWorks(ctx, authorForeignID)
	if err != nil {
		return nil, err
	}
	a.cache.set(key, cloneBooks(books))
	return cloneBooks(books), nil
}

func (a *Aggregator) primaryAuthorWorks(ctx context.Context, authorForeignID string) ([]models.Book, error) {
	provider := a.providerForForeignID(authorForeignID)
	if provider == nil {
		return nil, nil
	}
	if wp, ok := provider.(worksProvider); ok {
		return wp.GetAuthorWorks(ctx, authorForeignID)
	}
	if !sameProvider(provider, a.primary) {
		return nil, nil
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

func cloneBooks(books []models.Book) []models.Book {
	if books == nil {
		return nil
	}
	cloned := make([]models.Book, len(books))
	copy(cloned, books)
	return cloned
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
	if dst.HardcoverForeignID == "" {
		dst.HardcoverForeignID = hardcoverForeignIDForAuthorWork(src)
	}
	if dst.ImageURL == "" {
		dst.ImageURL = src.ImageURL
	}
	if dst.Description == "" {
		dst.Description = src.Description
	}
	// #807: keep the (average, count) pair together and prefer the source with
	// the materially stronger ratings_count rather than filling each field
	// independently (which could pair an average from one source with a count
	// from another).
	if preferStrongerRating(dst.AverageRating, dst.RatingsCount, src.AverageRating, src.RatingsCount) {
		dst.AverageRating = src.AverageRating
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

func hardcoverForeignIDForAuthorWork(book models.Book) string {
	if id := strings.TrimSpace(book.HardcoverForeignID); strings.HasPrefix(id, "hc:") {
		return id
	}
	if id := strings.TrimSpace(book.ForeignID); strings.HasPrefix(id, "hc:") && normalizedProviderName(book.MetadataProvider) == "hardcover" {
		return id
	}
	return ""
}

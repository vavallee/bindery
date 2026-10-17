package metadata

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"sync"
	"sync/atomic"
	"time"

	"github.com/vavallee/bindery/internal/metadata/audible"
	"github.com/vavallee/bindery/internal/metadata/audnex"
	"github.com/vavallee/bindery/internal/models"
)

// Aggregator fans out requests to multiple providers and merges results. The
// configured primary provider decides what an author's catalogue looks like;
// the enrichers fill gaps and cross-check it.
//
// The provider set can change at runtime (Reconfigure, #1771), and the design
// makes a lookup that mixes two sets impossible rather than unlikely:
//
//   - Aggregator holds nothing a lookup reads except cur, a pointer to an
//     immutable aggregatorCore. Every exported lookup below is a one line
//     delegate that loads cur exactly once and runs the whole operation,
//     nested calls included, as methods on that one core.
//   - The lookup logic lives on aggregatorCore, which has no reference back
//     to the Aggregator, so code under a lookup has no way to load cur a
//     second time and observe a newer set halfway through.
//   - Writers (Reconfigure and the With* setup methods) build a fresh core
//     under mu and publish it with one atomic store. They never modify a
//     core that has been published.
//
// TestAggregatorDelegatesLoadCoreOnce holds the delegates to that shape.
type Aggregator struct {
	// mu serialises writers so two concurrent Reconfigure calls cannot
	// interleave their copy and store. Lookups never take it.
	mu  sync.Mutex
	cur atomic.Pointer[aggregatorCore]
}

// ErrInvalidProviderSet is returned by Reconfigure for a provider set it
// refuses to install. The previous set keeps serving.
var ErrInvalidProviderSet = errors.New("invalid metadata provider set")

// NewAggregator creates an aggregator with the given primary provider and
// optional enrichers. Reconfigure replaces both later without a restart.
func NewAggregator(primary Provider, enrichers ...Provider) *Aggregator {
	a := &Aggregator{}
	a.cur.Store(&aggregatorCore{
		primary:   primary,
		enrichers: append([]Provider(nil), enrichers...),
		audnex:    audnex.New(""),
		audible:   audible.New(),
		cache:     newTTLCache(24 * time.Hour),
		requests:  &requestCache{},
	})
	return a
}

// WithAudnexClient replaces the default audnex client. Tests use this to keep
// ASIN canonicalization deterministic without reaching the network.
func (a *Aggregator) WithAudnexClient(client AudnexBookClient) *Aggregator {
	a.update(func(c *aggregatorCore) { c.audnex = client })
	return a
}

// WithAudibleCatalogue replaces the Audible client GetAuthorAudiobooks asks.
// Tests outside this package use it to stand in for api.audible.com.
func (a *Aggregator) WithAudibleCatalogue(catalogue audibleCatalogue) *Aggregator {
	a.update(func(c *aggregatorCore) { c.audible = catalogue })
	return a
}

// load returns the core every part of one lookup runs on. A nil Aggregator
// yields a nil core, whose methods behave as a nil Aggregator's always did.
func (a *Aggregator) load() *aggregatorCore {
	if a == nil {
		return nil
	}
	return a.cur.Load()
}

// update publishes a copy of the current core with mutate applied. The
// published core is never written to, so in-flight lookups keep a consistent
// view.
func (a *Aggregator) update(mutate func(*aggregatorCore)) {
	a.mu.Lock()
	defer a.mu.Unlock()
	next := *a.cur.Load()
	mutate(&next)
	a.cur.Store(&next)
}

// Reconfigure swaps in a new primary provider and enricher list. Lookups that
// already started finish on the set they started with; every lookup that
// starts after Reconfigure returns uses the new one.
//
// It refuses (and keeps the current set serving) a nil primary, a nil
// enricher, or a provider name that appears twice: the aggregator routes by
// provider name, so a duplicate would make one of the two unreachable.
//
// The caches are shared with the previous set. Entries stay correct because
// their keys carry the identity of the provider set that produced them.
func (a *Aggregator) Reconfigure(primary Provider, enrichers ...Provider) error {
	if a == nil {
		return fmt.Errorf("%w: nil aggregator", ErrInvalidProviderSet)
	}
	if isNilProvider(primary) {
		return fmt.Errorf("%w: primary provider is nil", ErrInvalidProviderSet)
	}
	seen := map[string]bool{normalizedProviderName(primary.Name()): true}
	for i, p := range enrichers {
		if isNilProvider(p) {
			return fmt.Errorf("%w: enricher %d is nil", ErrInvalidProviderSet, i)
		}
		name := normalizedProviderName(p.Name())
		if seen[name] {
			return fmt.Errorf("%w: provider %q appears more than once", ErrInvalidProviderSet, name)
		}
		seen[name] = true
	}
	list := append([]Provider(nil), enrichers...)
	a.update(func(c *aggregatorCore) {
		c.primary = primary
		c.enrichers = list
	})
	return nil
}

// SetAuthorEnrichers replaces the author enrichers, asked in order. Like
// Reconfigure it publishes a new core, so a lookup already running finishes on
// the list it started with. Nil enrichers are dropped.
func (a *Aggregator) SetAuthorEnrichers(enrichers ...AuthorEnricher) {
	if a == nil {
		return
	}
	list := make([]AuthorEnricher, 0, len(enrichers))
	for _, e := range enrichers {
		if e != nil {
			list = append(list, e)
		}
	}
	a.update(func(c *aggregatorCore) { c.authorEnrichers = list })
}

// EnrichAuthor fills author's empty photo and bio from the author enrichers
// (#3287), and reports whether they gave a full answer. Only the author
// profile refreshes call it; GetAuthor never enriches. author must be the
// caller's own copy, such as the record GetAuthor returned.
func (a *Aggregator) EnrichAuthor(ctx context.Context, author *models.Author) bool {
	return a.load().EnrichAuthor(ctx, author)
}

// AuthorEnricherNames returns the names of the author enrichers in the order
// they are asked.
func (a *Aggregator) AuthorEnricherNames() []string {
	c := a.load()
	if c == nil {
		return nil
	}
	names := make([]string, 0, len(c.authorEnrichers))
	for _, e := range c.authorEnrichers {
		names = append(names, e.Name())
	}
	return names
}

// ProviderNames returns the normalized names of the current provider set,
// primary first and then the enrichers in the order they are asked. It reads
// the set once, so the answer is one set's, never a mix of two.
func (a *Aggregator) ProviderNames() []string {
	c := a.load()
	if c == nil {
		return nil
	}
	providers := c.providers()
	names := make([]string, 0, len(providers))
	for _, p := range providers {
		names = append(names, normalizedProviderName(providerName(p)))
	}
	return names
}

func isNilProvider(p Provider) bool {
	if p == nil {
		return true
	}
	v := reflect.ValueOf(p)
	switch v.Kind() {
	case reflect.Pointer, reflect.Map, reflect.Slice, reflect.Func, reflect.Interface, reflect.Chan:
		return v.IsNil()
	}
	return false
}

// The delegates below are the whole public lookup surface. Each loads the
// core once and hands the entire operation to it. Keep them one line: any
// logic here would be logic that could see two provider sets.

func (a *Aggregator) GetPrimaryAuthorWorksSnapshot(ctx context.Context, authorForeignID string) (AuthorWorksSnapshot, error) {
	return a.load().GetPrimaryAuthorWorksSnapshot(ctx, authorForeignID)
}

func (a *Aggregator) GetAuthorWorksSnapshotForAuthor(ctx context.Context, author models.Author) (AuthorWorksSnapshot, error) {
	return a.load().GetAuthorWorksSnapshotForAuthor(ctx, author)
}

func (a *Aggregator) GetAuthorWorkLanguageEvidence(ctx context.Context, books []models.Book, allowed []string) (map[string]AuthorWorkLanguageEvidence, error) {
	return a.load().GetAuthorWorkLanguageEvidence(ctx, books, allowed)
}

func (a *Aggregator) FillMissingAuthorWorkLanguages(ctx context.Context, books []models.Book) int {
	return a.load().FillMissingAuthorWorkLanguages(ctx, books)
}

func (a *Aggregator) GetAuthorWorks(ctx context.Context, authorForeignID string) ([]models.Book, error) {
	return a.load().GetAuthorWorks(ctx, authorForeignID)
}

func (a *Aggregator) GetAuthorWorksUnenriched(ctx context.Context, authorForeignID string) ([]models.Book, error) {
	return a.load().GetAuthorWorksUnenriched(ctx, authorForeignID)
}

func (a *Aggregator) GetAuthorWorksForAuthor(ctx context.Context, author models.Author) ([]models.Book, error) {
	return a.load().GetAuthorWorksForAuthor(ctx, author)
}

func (a *Aggregator) SearchAuthors(ctx context.Context, query string) ([]models.Author, error) {
	return a.load().SearchAuthors(ctx, query)
}

func (a *Aggregator) SearchAuthorsWithOutcome(ctx context.Context, query string) ([]models.Author, SearchOutcome, error) {
	return a.load().SearchAuthorsWithOutcome(ctx, query)
}

func (a *Aggregator) ResolveCanonicalAuthor(ctx context.Context, name string) (*models.Author, error) {
	return a.load().ResolveCanonicalAuthor(ctx, name)
}

func (a *Aggregator) SearchBooks(ctx context.Context, query string) ([]models.Book, error) {
	return a.load().SearchBooks(ctx, query)
}

func (a *Aggregator) SearchBooksWithOutcome(ctx context.Context, query string) ([]models.Book, SearchOutcome, error) {
	return a.load().SearchBooksWithOutcome(ctx, query)
}

func (a *Aggregator) GetAuthor(ctx context.Context, foreignID string) (*models.Author, error) {
	return a.load().GetAuthor(ctx, foreignID)
}

func (a *Aggregator) GetBook(ctx context.Context, foreignID string) (*models.Book, error) {
	return a.load().GetBook(ctx, foreignID)
}

func (a *Aggregator) GetEditions(ctx context.Context, bookForeignID string) ([]models.Edition, error) {
	return a.load().GetEditions(ctx, bookForeignID)
}

func (a *Aggregator) GetEditionsFromProvider(ctx context.Context, providerName, bookForeignID string) ([]models.Edition, error) {
	return a.load().GetEditionsFromProvider(ctx, providerName, bookForeignID)
}

func (a *Aggregator) GetBookByISBN(ctx context.Context, isbn string) (*models.Book, error) {
	return a.load().GetBookByISBN(ctx, isbn)
}

func (a *Aggregator) GetBookByISBNWithOutcome(ctx context.Context, isbn string) (*models.Book, SearchOutcome, error) {
	return a.load().GetBookByISBNWithOutcome(ctx, isbn)
}

func (a *Aggregator) GetBookFromProvider(ctx context.Context, providerName, foreignID string) (*models.Book, error) {
	return a.load().GetBookFromProvider(ctx, providerName, foreignID)
}

func (a *Aggregator) ResolveBookByISBNWithOutcome(ctx context.Context, isbn string) (*models.Book, SearchOutcome, error) {
	return a.load().ResolveBookByISBNWithOutcome(ctx, isbn)
}

func (a *Aggregator) SearchAuthorCandidates(ctx context.Context, query string) ([]models.Author, error) {
	return a.load().SearchAuthorCandidates(ctx, query)
}

func (a *Aggregator) EnrichAudiobook(ctx context.Context, book *models.Book) error {
	return a.load().EnrichAudiobook(ctx, book)
}

func (a *Aggregator) GetCanonicalBookByASIN(ctx context.Context, asin string) (*models.Book, error) {
	return a.load().GetCanonicalBookByASIN(ctx, asin)
}

func (a *Aggregator) GetAuthorAudiobooks(ctx context.Context, authorName string) ([]models.Book, error) {
	return a.load().GetAuthorAudiobooks(ctx, authorName)
}

func (a *Aggregator) PrimaryProviderName() string {
	return a.load().PrimaryProviderName()
}

func (a *Aggregator) CheckQuota(ctx context.Context) error {
	return a.load().CheckQuota(ctx)
}

func (a *Aggregator) EnrichMissingCovers(ctx context.Context, books []models.Book) {
	a.load().EnrichMissingCovers(ctx, books)
}

func (a *Aggregator) SearchSeries(ctx context.Context, query string, limit int) ([]SeriesSearchResult, error) {
	return a.load().SearchSeries(ctx, query, limit)
}

func (a *Aggregator) GetSeriesCatalog(ctx context.Context, foreignID string) (*SeriesCatalog, error) {
	return a.load().GetSeriesCatalog(ctx, foreignID)
}

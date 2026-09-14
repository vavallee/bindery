package metadata

import (
	"context"
	"errors"
	"strings"
	"sync/atomic"
	"testing"
	"testing/synctest"
	"time"

	"github.com/vavallee/bindery/internal/models"
)

// A live-configured provider whose bound copies retain catalogue capabilities.
type scopedCatalogueProvider struct {
	*mockProvider
	token   *atomic.Value
	bound   string
	books   map[string]models.Book
	works   map[string][]models.Book
	fetched func(string, string)
}

func (p *scopedCatalogueProvider) scope() string {
	if p.token != nil {
		return p.token.Load().(string)
	}
	return p.bound
}
func (p *scopedCatalogueProvider) ResolveCacheProvider(context.Context) (Provider, string) {
	scope := p.scope()
	copy := *p
	copy.token = nil
	copy.bound = scope
	return &copy, scope
}
func (p *scopedCatalogueProvider) GetBook(context.Context, string) (*models.Book, error) {
	scope := p.scope()
	if p.fetched != nil {
		p.fetched("book", scope)
	}
	if scope == "" {
		return nil, ErrProviderNotConfigured
	}
	book := p.books[scope]
	return cloneBook(&book), nil
}
func (p *scopedCatalogueProvider) GetAuthorWorks(context.Context, string) ([]models.Book, error) {
	scope := p.scope()
	if p.fetched != nil {
		p.fetched("works", scope)
	}
	if scope == "" {
		return nil, ErrProviderNotConfigured
	}
	return cloneBooks(p.works[scope]), nil
}
func (p *scopedCatalogueProvider) GetAuthorWorksByName(ctx context.Context, _ string) ([]models.Book, error) {
	return p.GetAuthorWorks(ctx, "")
}
func (p *scopedCatalogueProvider) GetAuthorWorksByIdentity(ctx context.Context, _ string) ([]models.Book, error) {
	return p.GetAuthorWorks(ctx, "")
}
func newScopedCatalogueProvider() *scopedCatalogueProvider {
	token := &atomic.Value{}
	token.Store("first")
	return &scopedCatalogueProvider{mockProvider: &mockProvider{name: "hardcover"}, token: token,
		books: map[string]models.Book{
			"first":  {ForeignID: "hc:dune", MetadataProvider: "hardcover", Title: "Dune", Description: "old description", ImageURL: "old-cover", Genres: []string{"old genre"}},
			"second": {ForeignID: "hc:dune", MetadataProvider: "hardcover", Title: "Dune", Description: "new", Genres: []string{"new genre"}},
		}, works: map[string][]models.Book{
			"first":  {{ForeignID: "hc:first", Title: "First Work", ImageURL: "cover"}},
			"second": {{ForeignID: "hc:second", Title: "Second Work", ImageURL: "cover"}},
		}}
}

func TestEnrichmentSnapshotTracksPrimaryScopeAndInput(t *testing.T) {
	for _, change := range []string{"credentials", "raw expiry"} {
		t.Run(change, func(t *testing.T) {
			p := newScopedCatalogueProvider()
			a := newTestAggregator(p)
			if _, err := a.GetBook(context.Background(), "hc:dune"); err != nil {
				t.Fatal(err)
			}
			if change == "credentials" {
				p.token.Store("second")
			} else {
				p.books["first"] = p.books["second"]
				// Raw entries are written before enrichment snapshots. Reproduce the
				// interval where raw metadata has expired but its snapshot is still live.
				a.cache.state.mu.Lock()
				for key, item := range a.cache.state.items {
					if strings.HasPrefix(key, "book:") {
						item.expiresAt = time.Now().Add(-time.Second)
						a.cache.state.items[key] = item
					}
				}
				a.cache.state.mu.Unlock()
			}
			got, err := a.GetBook(context.Background(), "hc:dune")
			if err != nil {
				t.Fatal(err)
			}
			if got.Description != "new" || got.ImageURL != "" || len(got.Genres) != 1 || got.Genres[0] != "new genre" {
				t.Fatalf("old primary fields restored: %+v", got)
			}
		})
	}
}

func TestCatalogueCachesTrackLiveScopes(t *testing.T) {
	for _, path := range []string{"raw", "enriched", "unenriched after enriched", "supplemented"} {
		t.Run(path, func(t *testing.T) {
			p := newScopedCatalogueProvider()
			a := newTestAggregator(p)
			var calls atomic.Int32
			p.fetched = func(_ string, _ string) { calls.Add(1) }
			fetch := func() ([]models.Book, error) {
				switch path {
				case "raw":
					return a.GetAuthorWorksUnenriched(context.Background(), "hc:author")
				case "enriched":
					return a.GetAuthorWorks(context.Background(), "hc:author")
				case "unenriched after enriched":
					if _, err := a.GetAuthorWorks(context.Background(), "hc:author"); err != nil {
						return nil, err
					}
					return a.GetAuthorWorksUnenriched(context.Background(), "hc:author")
				default:
					return a.GetAuthorWorksForAuthor(context.Background(), models.Author{ForeignID: "hc:author", Name: "Writer"})
				}
			}
			for _, scope := range []string{"first", "second"} {
				p.token.Store(scope)
				for range 2 {
					got, err := fetch()
					if err != nil || len(got) != 1 || got[0].ForeignID != "hc:"+scope {
						t.Fatalf("scope=%s catalogue=%v err=%v", scope, got, err)
					}
				}
			}
			if calls.Load() != 2 {
				t.Fatalf("provider calls=%d, want 2", calls.Load())
			}
			p.token.Store("")
			if _, err := fetch(); !errors.Is(err, ErrProviderNotConfigured) {
				t.Fatalf("removed token reused catalogue: %v", err)
			}
		})
	}
}

func TestSupplementCatalogueScopeDoesNotInvalidateRawPrimary(t *testing.T) {
	primary := &mockWorksProvider{mockProvider: mockProvider{name: "openlibrary", authorWorks: []models.Book{{ForeignID: "OL1W", Title: "Primary Work", ImageURL: "cover"}}}}
	supplement := newScopedCatalogueProvider()
	a := newTestAggregator(primary, supplement)
	for _, scope := range []string{"first", "second"} {
		supplement.token.Store(scope)
		got, err := a.GetAuthorWorksForAuthor(context.Background(), models.Author{ForeignID: "OL1A", Name: "Writer"})
		if err != nil || len(got) != 2 || got[1].ForeignID != "hc:"+scope {
			t.Fatalf("supplement scope=%s catalogue=%v err=%v", scope, got, err)
		}
		// A changed supplement must not cost another primary catalogue fetch.
		primary.authorWorks = nil
	}
}

func TestCatalogueScopeIsPinnedAcrossConcurrentCredentialChange(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		primary := newScopedCatalogueProvider()
		supplement := newScopedCatalogueProvider()
		supplement.name = "supplement"
		supplement.works = map[string][]models.Book{
			"first":  {{ForeignID: "supplement:first", Title: "First Supplement", ImageURL: "cover"}},
			"second": {{ForeignID: "supplement:second", Title: "Second Supplement", ImageURL: "cover"}},
		}
		a := newRequestTestAggregator(primary)
		a.enrichers = []Provider{supplement}
		release := make(chan struct{})
		var calls atomic.Int32
		primary.fetched = func(_ string, scope string) {
			calls.Add(1)
			if scope == "first" {
				<-release
			}
		}
		done := make(chan []models.Book, 1)
		go func() {
			b, err := a.GetAuthorWorksForAuthor(context.Background(), models.Author{ForeignID: "hc:author", Name: "Writer"})
			if err != nil {
				t.Error(err)
			}
			done <- b
		}()
		synctest.Wait()
		primary.token.Store("second")
		supplement.token.Store("second")
		fresh, err := a.GetAuthorWorksForAuthor(context.Background(), models.Author{ForeignID: "hc:author", Name: "Writer"})
		if err != nil || len(fresh) != 2 || fresh[0].ForeignID != "hc:second" || fresh[1].ForeignID != "supplement:second" {
			t.Fatalf("new scope: %v %v", fresh, err)
		}
		close(release)
		synctest.Wait()
		old := <-done
		if len(old) != 2 || old[0].ForeignID != "hc:first" || old[1].ForeignID != "supplement:first" {
			t.Fatalf("mixed configuration: %v", old)
		}
		again, err := a.GetAuthorWorksForAuthor(context.Background(), models.Author{ForeignID: "hc:author", Name: "Writer"})
		if err != nil || len(again) != 2 || again[0].ForeignID != "hc:second" || calls.Load() != 2 {
			t.Fatalf("old completion polluted new scope: %v %v calls=%d", again, err, calls.Load())
		}
	})
}

package metadata

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"
	"testing/synctest"
	"time"

	"github.com/vavallee/bindery/internal/models"
)

type cacheTestProvider struct {
	*mockProvider
	books    func(context.Context, string) ([]models.Book, error)
	author   func(context.Context, string) (*models.Author, error)
	book     func(context.Context, string) (*models.Book, error)
	editions func(context.Context, string) ([]models.Edition, error)
}

func (p *cacheTestProvider) SearchBooks(ctx context.Context, q string) ([]models.Book, error) {
	return p.books(ctx, q)
}
func (p *cacheTestProvider) GetEditions(ctx context.Context, id string) ([]models.Edition, error) {
	return p.editions(ctx, id)
}

func TestSearchCacheReusesProviderResults(t *testing.T) {
	isbn := "9780441172719"
	p := &mockProvider{name: "hardcover", searchBooks: []models.Book{{Title: "Dune", ForeignID: "hc:dune", ProviderISBNs: []string{isbn}, Editions: []models.Edition{{ISBN13: &isbn}}, Author: &models.Author{Name: "Herbert", AlternateNames: []string{"Frank"}}}}, searchAuthors: []models.Author{{ForeignID: "hc:herbert", Name: "Herbert", AlternateNames: []string{"Frank"}, ProviderIdentifiers: map[string]string{"hardcover": "1"}}}}
	a := newRequestTestAggregator(p)
	books, err := a.SearchBooks(context.Background(), "Dune")
	if err != nil || len(books) != 1 {
		t.Fatalf("search: %v, %v", books, err)
	}
	books[0].ProviderISBNs[0] = "mutated"
	*books[0].Editions[0].ISBN13 = "mutated"
	books[0].Author.AlternateNames[0] = "mutated"
	// This is the same path used by cover enrichment and canonical searches.
	again, err := a.searchProviderBooks(context.Background(), p, "Dune")
	if err != nil || again[0].ProviderISBNs[0] != isbn || *again[0].Editions[0].ISBN13 != isbn || again[0].Author.AlternateNames[0] != "Frank" {
		t.Fatalf("shared mutable results: %+v, %v", again, err)
	}
	authors, _, err := a.SearchAuthorsWithOutcome(context.Background(), "Herbert")
	if err != nil {
		t.Fatal(err)
	}
	authors[0].ProviderIdentifiers["hardcover"] = "changed"
	authors[0].AlternateNames[0] = "changed"
	candidates, err := a.SearchAuthorCandidates(context.Background(), "Herbert")
	if err != nil || candidates[0].ProviderIdentifiers["hardcover"] != "1" || candidates[0].AlternateNames[0] != "Frank" {
		t.Fatalf("author cache mutation: %+v %v", candidates, err)
	}
	if len(p.searchBookQueries) != 1 || len(p.searchAuthorQueries) != 1 {
		t.Fatalf("calls: books=%v authors=%v", p.searchBookQueries, p.searchAuthorQueries)
	}
	// Exact queries retain syntax, whitespace, and case rather than guessing
	// equivalence for providers with different search languages.
	_, _ = a.searchProviderBooks(context.Background(), p, "title:Dune")
	_, _ = a.searchProviderBooks(context.Background(), p, "dune")
	if len(p.searchBookQueries) != 3 {
		t.Fatal("different query options aliased")
	}
}

func TestSearchCacheBoundsExpiryAndFailures(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		p := &mockProvider{name: "hardcover"}
		a := newRequestTestAggregator(p)
		a.requests.searches.ttl = time.Minute
		a.requests.searches.state.maxEntries = 2
		search := func(q string) {
			t.Helper()
			if _, err := a.searchProviderBooks(context.Background(), p, q); err != nil {
				t.Fatal(err)
			}
		}
		search("empty")
		search("empty")
		if len(p.searchBookQueries) != 1 {
			t.Fatal("successful empty result not cached")
		}
		time.Sleep(time.Second)
		search("second")
		time.Sleep(time.Second)
		search("third")
		if len(a.requests.searches.state.items) != 2 {
			t.Fatal("cache exceeded bound")
		}
		search("empty")
		if len(p.searchBookQueries) != 4 {
			t.Fatal("oldest result not evicted")
		}
		time.Sleep(time.Minute + time.Nanosecond)
		search("empty")
		if len(p.searchBookQueries) != 5 {
			t.Fatal("expired result reused")
		}
		for _, failure := range []error{ErrProviderNotConfigured, errors.New("rate limited"), errors.New("authentication"), errors.New("network")} {
			p.searchBookErr = failure
			for range 2 {
				_, err := a.searchProviderBooks(context.Background(), p, "fails")
				if !errors.Is(err, failure) {
					t.Fatalf("error changed: %v", err)
				}
			}
		}
		if len(p.searchBookQueries) != 13 {
			t.Fatal("error cached as success")
		}
		p.searchBookErr = nil
		search("fails")
	})
}

func TestCachedRequestsCombineAndCancelIndependently(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		release := make(chan struct{})
		var calls atomic.Int32
		p := &cacheTestProvider{mockProvider: &mockProvider{name: "hardcover"}}
		p.books = func(ctx context.Context, _ string) ([]models.Book, error) {
			calls.Add(1)
			select {
			case <-ctx.Done():
				return nil, ctx.Err()
			case <-release:
				return []models.Book{{Title: "Dune"}}, nil
			}
		}
		a := newRequestTestAggregator(p)
		firstCtx, cancel := context.WithCancel(context.Background())
		first := make(chan error, 1)
		go func() { _, err := a.SearchBooks(firstCtx, "Dune"); first <- err }()
		synctest.Wait()
		results := make(chan []models.Book, 10)
		for range 10 {
			go func() {
				b, err := a.SearchBooks(context.Background(), "Dune")
				if err != nil {
					t.Error(err)
				}
				results <- b
			}()
		}
		synctest.Wait()
		cancel()
		synctest.Wait()
		if err := <-first; !errors.Is(err, context.Canceled) {
			t.Fatalf("leader cancellation: %v", err)
		}
		if calls.Load() != 1 {
			t.Fatalf("upstream calls = %d", calls.Load())
		}
		close(release)
		synctest.Wait()
		for range 10 {
			b := <-results
			if len(b) != 1 || b[0].Title != "Dune" {
				t.Fatalf("waiter lost result: %v", b)
			}
			b[0].Title = "changed"
		}
		canceled, stop := context.WithCancel(context.Background())
		stop()
		if _, err := a.SearchBooks(canceled, "Dune"); !errors.Is(err, context.Canceled) {
			t.Fatalf("canceled cache hit: %v", err)
		}
	})
}

func TestCachedRequestsAbandonedFetchCannotPopulateCache(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		releaseOld := make(chan struct{})
		var calls atomic.Int32
		var stopped atomic.Bool
		p := &cacheTestProvider{mockProvider: &mockProvider{name: "hardcover"}}
		p.books = func(ctx context.Context, _ string) ([]models.Book, error) {
			if calls.Add(1) == 1 {
				<-ctx.Done()
				stopped.Store(true)
				<-releaseOld
				return []models.Book{{Title: "stale"}}, nil
			}
			return []models.Book{{Title: "fresh"}}, nil
		}
		a := newRequestTestAggregator(p)
		ctx, cancel := context.WithCancel(context.Background())
		done := make(chan error, 1)
		go func() { _, err := a.searchProviderBooks(ctx, p, "q"); done <- err }()
		synctest.Wait()
		cancel()
		synctest.Wait()
		if !errors.Is(<-done, context.Canceled) || !stopped.Load() {
			t.Fatal("last waiter did not cancel upstream")
		}
		got, err := a.searchProviderBooks(context.Background(), p, "q")
		if err != nil || got[0].Title != "fresh" {
			t.Fatalf("replacement: %v %v", got, err)
		}
		close(releaseOld)
		synctest.Wait()
		got, err = a.searchProviderBooks(context.Background(), p, "q")
		if err != nil || got[0].Title != "fresh" || calls.Load() != 2 {
			t.Fatalf("abandoned fetch poisoned cache: %v %v", got, err)
		}
	})
}

func TestEditionCacheCanonicalRoutesAndIsolation(t *testing.T) {
	var calls []string
	p := &cacheTestProvider{mockProvider: &mockProvider{name: "hardcover"}}
	p.editions = func(_ context.Context, id string) ([]models.Edition, error) {
		id = strings.TrimSpace(strings.TrimPrefix(id, "hc:"))
		calls = append(calls, id)
		return []models.Edition{{Title: id, ISBN13: &id}}, nil
	}
	other := &cacheTestProvider{mockProvider: &mockProvider{name: "openlibrary"}, editions: func(_ context.Context, id string) ([]models.Edition, error) {
		return []models.Edition{{Title: "ol:" + id}}, nil
	}}
	a := newTestAggregator(other, p)
	ctx := context.Background()
	for _, id := range []string{"hc:123", "hc:00123", "hc:Dune", "hc:dune"} {
		got, err := a.GetEditions(ctx, id)
		if err != nil {
			t.Fatal(err)
		}
		*got[0].ISBN13 = "changed"
		for _, provider := range []string{"hardcover", "HC"} {
			got, err = a.GetEditionsFromProvider(ctx, provider, id[3:])
			if err != nil || got[0].Title != id[3:] || *got[0].ISBN13 != id[3:] {
				t.Fatalf("edition alias: %v %v", got, err)
			}
		}
	}
	if fmt.Sprint(calls) != "[123 00123 Dune dune]" {
		t.Fatalf("calls: %v", calls)
	}
	got, err := a.GetEditionsFromProvider(ctx, "openlibrary", "123")
	if err != nil || got[0].Title != "ol:123" {
		t.Fatalf("provider isolation: %v %v", got, err)
	}
}

// Synctest must not own the production janitor, which intentionally outlives
// requests and only stops when its cache is collected.
func newRequestTestAggregator(p Provider) *Aggregator {
	cache := func(ttl time.Duration) *ttlCache {
		return &ttlCache{ttl: ttl, state: &ttlState{items: make(map[string]cacheItem), maxEntries: 1000}}
	}
	return &Aggregator{primary: p, cache: cache(24 * time.Hour), requests: requestCache{searches: cache(5 * time.Minute)}}
}

func (p *cacheTestProvider) GetAuthor(ctx context.Context, id string) (*models.Author, error) {
	if p.author != nil {
		return p.author(ctx, id)
	}
	return p.mockProvider.GetAuthor(ctx, id)
}
func (p *cacheTestProvider) GetBook(ctx context.Context, id string) (*models.Book, error) {
	if p.book != nil {
		return p.book(ctx, id)
	}
	return p.mockProvider.GetBook(ctx, id)
}

func TestMetadataCacheCombinesProfileMisses(t *testing.T) {
	for _, operation := range []string{"author", "book"} {
		t.Run(operation, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				release := make(chan struct{})
				var calls atomic.Int32
				p := &cacheTestProvider{mockProvider: &mockProvider{name: "openlibrary"}}
				p.author = func(context.Context, string) (*models.Author, error) {
					calls.Add(1)
					<-release
					return &models.Author{Name: "Herbert", AlternateNames: []string{"Frank"}}, nil
				}
				p.book = func(context.Context, string) (*models.Book, error) {
					calls.Add(1)
					<-release
					return &models.Book{Title: "Dune", Genres: []string{"fiction"}}, nil
				}
				a := newRequestTestAggregator(p)
				fetch := func() {
					if operation == "author" {
						author, err := a.GetAuthor(context.Background(), "OL1A")
						if err != nil || author == nil || author.AlternateNames[0] != "Frank" {
							t.Errorf("author: %v %v", author, err)
							return
						}
						author.AlternateNames[0] = "mutated"
					} else {
						book, err := a.GetBook(context.Background(), "OL1W")
						if err != nil || book == nil || book.Genres[0] != "fiction" {
							t.Errorf("book: %v %v", book, err)
							return
						}
						book.Genres[0] = "mutated"
					}
				}
				for range 5 {
					go fetch()
				}
				synctest.Wait()
				close(release)
				synctest.Wait()
				fetch()
				if calls.Load() != 1 {
					t.Fatalf("calls=%d", calls.Load())
				}
			})
		})
	}
}

type scopedSearchProvider struct {
	*mockProvider
	scope  string
	result string
}

func (p *scopedSearchProvider) ResolveCacheProvider(context.Context) (Provider, string) {
	return &mockProvider{name: p.name, searchBooks: []models.Book{{Title: "Dune", ImageURL: p.result}}}, p.scope
}

func TestGetBookEnrichmentHonorsLiveScope(t *testing.T) {
	primary := &mockProvider{name: "openlibrary", getBook: &models.Book{ForeignID: "OL1W", Title: "Dune"}}
	enricher := &scopedSearchProvider{mockProvider: &mockProvider{name: "hardcover"}, scope: "first", result: "first-cover"}
	a := newTestAggregator(primary, enricher)
	for _, scope := range []string{"first", "second"} {
		enricher.scope = scope
		enricher.result = scope + "-cover"
		book, err := a.GetBook(context.Background(), "OL1W")
		if err != nil || book.ImageURL != scope+"-cover" {
			t.Fatalf("stale enrichment: %+v %v", book, err)
		}
	}
}

// A failing enricher is asked once per five minute window, not on every
// GetBook cache hit, and is asked again once the window passes.
func TestEnrichmentFailureRemainsRetryable(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		primary := &mockProvider{name: "openlibrary", getBook: &models.Book{ForeignID: "OL1W", Title: "Dune"}}
		enricher := &mockProvider{name: "hardcover", searchBookErr: errors.New("rate limited")}
		a := newRequestTestAggregator(primary)
		a.enrichers = []Provider{enricher}
		for range 5 {
			if _, err := a.GetBook(context.Background(), "OL1W"); err != nil {
				t.Fatal(err)
			}
		}
		if n := len(enricher.searchBookQueries); n != 1 {
			t.Fatalf("failing enricher asked %d times in one window, want 1", n)
		}
		enricher.searchBookErr = nil
		enricher.searchBooks = []models.Book{{Title: "Dune", ImageURL: "cover"}}
		time.Sleep(5*time.Minute + time.Nanosecond)
		book, err := a.GetBook(context.Background(), "OL1W")
		if err != nil || book.ImageURL != "cover" || len(enricher.searchBookQueries) != 2 {
			t.Fatalf("failed enrichment not retried after the window: %+v %v", book, err)
		}
		// The complete snapshot now holds for the long TTL.
		time.Sleep(5*time.Minute + time.Nanosecond)
		if _, err := a.GetBook(context.Background(), "OL1W"); err != nil || len(enricher.searchBookQueries) != 2 {
			t.Fatalf("complete snapshot not cached: %v, %d queries", err, len(enricher.searchBookQueries))
		}
	})
}

// An enricher failure stores a short-lived snapshot, but never for a book
// with a locked field (#2767) or a cancelled request.
func TestEnrichmentFailureShortCachesOnlyUnlockedLiveRequests(t *testing.T) {
	for _, tc := range []struct {
		name         string
		lock, cancel bool
		want         int
	}{
		{name: "unlocked", want: 1},
		{name: "locked description", lock: true},
		{name: "cancelled", cancel: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			a := newRequestTestAggregator(&mockProvider{name: "openlibrary"})
			a.enrichers = []Provider{&mockProvider{name: "hardcover", searchBookErr: errors.New("rate limited")}}
			book := models.Book{ForeignID: "OL1W", Title: "Dune"}
			if tc.lock {
				book.LockField(models.BookFieldDescription)
			}
			ctx, cancel := context.WithCancel(context.Background())
			if tc.cancel {
				cancel()
			}
			defer cancel()
			a.enrichBook(ctx, &book)
			if n := len(a.requests.searches.state.items); n != tc.want {
				t.Fatalf("short cache holds %d entries, want %d", n, tc.want)
			}
			if n := len(a.cache.state.items); n != 0 {
				t.Fatalf("24h cache holds %d entries, want 0", n)
			}
		})
	}
}

// A complete snapshot in the 24h cache wins over a short-lived one under the
// same key.
func TestEnrichmentCompleteSnapshotBeatsShortOne(t *testing.T) {
	a := newRequestTestAggregator(&mockProvider{name: "openlibrary"})
	a.enrichers = []Provider{&mockProvider{name: "hardcover", searchBooks: []models.Book{{Title: "Dune", ImageURL: "cover"}}}}
	first := models.Book{ForeignID: "OL1W", Title: "Dune"}
	a.enrichBook(context.Background(), &first)
	var key string
	for k := range a.cache.state.items {
		if strings.HasPrefix(k, "enrich") {
			key = k
		}
	}
	if key == "" {
		t.Fatal("complete snapshot not stored in the 24h cache")
	}
	a.requests.searches.set(key, enrichmentSnapshot{imageURL: "stale"})
	second := models.Book{ForeignID: "OL1W", Title: "Dune"}
	a.enrichBook(context.Background(), &second)
	if second.ImageURL != "cover" {
		t.Fatalf("ImageURL = %q, want the complete snapshot's cover", second.ImageURL)
	}
}

func TestEnrichmentSnapshotCachesPastUnconfiguredEnricher(t *testing.T) {
	primary := &mockProvider{name: "openlibrary", getBook: &models.Book{ForeignID: "OL1W", Title: "Dune"}}
	unconfigured := &mockProvider{name: "hardcover", searchBookErr: ErrProviderNotConfigured}
	covers := &mockProvider{name: "googlebooks", searchBooks: []models.Book{{Title: "Dune", ImageURL: "cover"}}}
	a := newTestAggregator(primary, unconfigured, covers)
	for i := 0; i < 2; i++ {
		book, err := a.GetBook(context.Background(), "OL1W")
		if err != nil || book.ImageURL != "cover" {
			t.Fatalf("call %d: %+v %v", i, book, err)
		}
	}
	// Failures are never search cached, so a second query here means the
	// enrichment snapshot was not stored.
	if n := len(unconfigured.searchBookQueries); n != 1 {
		t.Fatalf("unconfigured enricher searched %d times, want 1 (snapshot not cached)", n)
	}
}

func TestCachedRequestRecoversProviderPanic(t *testing.T) {
	var calls atomic.Int32
	p := &cacheTestProvider{mockProvider: &mockProvider{name: "openlibrary"}}
	p.books = func(context.Context, string) ([]models.Book, error) {
		if calls.Add(1) == 1 {
			panic("provider bug")
		}
		return []models.Book{{Title: "Dune"}}, nil
	}
	a := newRequestTestAggregator(p)
	_, err := a.searchProviderBooks(context.Background(), p, "q")
	if !errors.Is(err, errFetchPanicked) || strings.Contains(err.Error(), "provider bug") {
		t.Fatalf("panicking fetch: err = %v, want the generic errFetchPanicked", err)
	}
	got, err := a.searchProviderBooks(context.Background(), p, "q")
	if err != nil || len(got) != 1 || calls.Load() != 2 {
		t.Fatalf("after panic: %v %v calls=%d, want a fresh fetch", got, err, calls.Load())
	}
}

// Every slice on the sync summary must be copied, including ones added after
// cloneAuthor was written, or a caller editing a cached author's summary
// rewrites the cache entry.
func TestCloneAuthorCopiesEverySyncSummarySlice(t *testing.T) {
	summary := &models.AuthorSyncSummary{}
	v := reflect.ValueOf(summary).Elem()
	for i := 0; i < v.NumField(); i++ {
		if f := v.Field(i); f.Kind() == reflect.Slice {
			f.Set(reflect.MakeSlice(f.Type(), 1, 1))
		}
	}
	clone := cloneAuthor(&models.Author{LastSync: summary}).LastSync
	if clone == summary {
		t.Fatal("LastSync pointer shared with the cached author")
	}
	cv := reflect.ValueOf(clone).Elem()
	for i := 0; i < v.NumField(); i++ {
		if f := v.Field(i); f.Kind() == reflect.Slice && f.Pointer() == cv.Field(i).Pointer() {
			t.Errorf("LastSync.%s shares its backing array with the cached author", v.Type().Field(i).Name)
		}
	}
}

func TestEditionCacheDoesNotNormalizeProviderInputTwice(t *testing.T) {
	p := &cacheTestProvider{mockProvider: &mockProvider{name: "hardcover"}}
	var calls []string
	p.editions = func(_ context.Context, id string) ([]models.Edition, error) {
		calls = append(calls, id)
		return []models.Edition{{Title: strings.TrimSpace(strings.TrimPrefix(id, "hc:"))}}, nil
	}
	a := newTestAggregator(p)
	for _, id := range []string{"hc:dune", "hc:hc:dune"} {
		editions, err := a.GetEditions(context.Background(), id)
		if err != nil || editions[0].Title != strings.TrimPrefix(id, "hc:") {
			t.Fatalf("identity changed: %v %v", editions, err)
		}
	}
	if fmt.Sprint(calls) != "[hc:dune hc:hc:dune]" {
		t.Fatalf("provider input rewritten: %v", calls)
	}
}

func TestCachedRequestSchedulingBudgetFollowsRemainingWaiters(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		a := newRequestTestAggregator(nil)
		release := make(chan struct{})
		var upstream context.Context
		var calls int
		clone := func(n int) int { return n }
		fetch := func(ctx context.Context) (int, error) {
			// Composed reads must carry the outer callers' budget through a
			// second shared fetch without losing deadlines or locking recursively.
			return cachedRequest(ctx, a, a.cache, "inner", "inner", func(ctx context.Context) (int, error) {
				calls++
				upstream = ctx
				select {
				case <-release:
					return 42, nil
				case <-ctx.Done():
					return 0, ctx.Err()
				}
			}, clone)
		}
		start := func(ctx context.Context) <-chan error {
			result := make(chan error, 1)
			go func() {
				_, err := cachedRequest(ctx, a, a.cache, "outer", "outer", fetch, clone)
				result <- err
			}()
			synctest.Wait()
			return result
		}
		now := time.Now()
		short, cancelShort := context.WithTimeout(context.Background(), time.Second)
		defer cancelShort()
		first := start(short)
		assertDeadline := func(want time.Time, bounded bool) {
			t.Helper()
			got, ok := SchedulingDeadline(upstream)
			if ok != bounded || (bounded && !got.Equal(want)) {
				t.Fatalf("scheduling deadline = %v, %v; want %v, %v", got, ok, want, bounded)
			}
		}
		assertDeadline(now.Add(time.Second), true)
		long, cancelLong := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancelLong()
		second := start(long)
		assertDeadline(now.Add(3*time.Second), true)
		unbounded, cancelUnbounded := context.WithCancel(context.Background())
		third := start(unbounded)
		assertDeadline(time.Time{}, false)
		cancelUnbounded()
		if err := <-third; !errors.Is(err, context.Canceled) {
			t.Fatalf("unbounded waiter cancellation: %v", err)
		}
		assertDeadline(now.Add(3*time.Second), true)
		limited, cancelLimited := context.WithTimeout(upstream, 2*time.Second)
		defer cancelLimited()
		if got, ok := SchedulingDeadline(limited); !ok || !got.Equal(now.Add(2*time.Second)) {
			t.Fatalf("provider's own deadline lost: %v, %v", got, ok)
		}
		time.Sleep(time.Second)
		synctest.Wait()
		if err := <-first; !errors.Is(err, context.DeadlineExceeded) {
			t.Fatalf("first waiter deadline: %v", err)
		}
		assertDeadline(now.Add(3*time.Second), true)
		if upstream.Err() != nil {
			t.Fatal("first deadline canceled the remaining caller's fetch")
		}
		close(release)
		if err := <-second; err != nil {
			t.Fatalf("remaining caller: %v", err)
		}
		if calls != 1 {
			t.Fatalf("upstream calls = %d; want 1", calls)
		}
	})
}

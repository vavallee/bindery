package api

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/vavallee/bindery/internal/db"
	"github.com/vavallee/bindery/internal/metadata"
	"github.com/vavallee/bindery/internal/models"
)

// concurrentEditionProvider is a stubMetaProvider whose GetEditions blocks until
// wantConcurrent callers are inside it at once, or the deadline passes.
//
// That makes the serial case fail loudly instead of merely being slow: before
// #1929 the sync hydrated one created book at a time, so only one caller could
// ever be inside GetEditions and the barrier would never be reached.
type concurrentEditionProvider struct {
	stubMetaProvider

	wantConcurrent int
	timeout        time.Duration

	mu       sync.Mutex
	inFlight int
	maxSeen  int
	reached  chan struct{}
	once     sync.Once
}

func newConcurrentEditionProvider(works []models.Book, wantConcurrent int) *concurrentEditionProvider {
	return &concurrentEditionProvider{
		// The aggregator only takes an hc: author's works from a provider that
		// reports itself as Hardcover, so the name matters here.
		stubMetaProvider: stubMetaProvider{works: works, name: "hardcover"},
		wantConcurrent:   wantConcurrent,
		timeout:          2 * time.Second,
		reached:          make(chan struct{}),
	}
}

func (p *concurrentEditionProvider) GetEditions(ctx context.Context, fid string) ([]models.Edition, error) {
	p.editionCallsMu.Lock()
	p.editionCalls = append(p.editionCalls, fid)
	p.editionCallsMu.Unlock()

	p.mu.Lock()
	p.inFlight++
	if p.inFlight > p.maxSeen {
		p.maxSeen = p.inFlight
	}
	if p.inFlight >= p.wantConcurrent {
		p.once.Do(func() { close(p.reached) })
	}
	p.mu.Unlock()

	select {
	case <-p.reached:
	case <-time.After(p.timeout):
	case <-ctx.Done():
	}

	p.mu.Lock()
	p.inFlight--
	p.mu.Unlock()
	return []models.Edition{{ForeignID: fid + "-ed"}}, nil
}

func (p *concurrentEditionProvider) peakConcurrency() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.maxSeen
}

// hardcoverWorks builds n Hardcover works, which is what makes the sync hydrate
// editions for each one it creates.
func hardcoverWorks(n int) []models.Book {
	works := make([]models.Book, 0, n)
	for i := 0; i < n; i++ {
		id := string(rune('a' + i))
		works = append(works, models.Book{
			ForeignID:        "hc:work-" + id,
			Title:            "Work " + id,
			SortTitle:        "work " + id,
			Language:         "eng",
			MediaType:        models.MediaTypeEbook,
			Status:           models.BookStatusWanted,
			MetadataProvider: "hardcover",
		})
	}
	return works
}

func prefetchFixture(t *testing.T, provider *concurrentEditionProvider) (*AuthorHandler, *models.Author, *db.BookRepo) {
	t.Helper()
	database, err := db.OpenMemory()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { database.Close() })

	authorRepo := db.NewAuthorRepo(database)
	bookRepo := db.NewBookRepo(database)
	profileRepo := db.NewMetadataProfileRepo(database)
	settingsRepo := db.NewSettingsRepo(database)
	editionRepo := db.NewEditionRepo(database)

	author := &models.Author{
		ForeignID: "hc:prefetch-author", Name: "Prefetch Author", SortName: "Author, Prefetch",
		MetadataProvider: "hardcover", Monitored: false,
	}
	if err := authorRepo.Create(context.Background(), author); err != nil {
		t.Fatal(err)
	}

	agg := metadata.NewAggregator(provider)
	h := NewAuthorHandler(authorRepo, nil, bookRepo, nil, agg, settingsRepo, profileRepo, nil).
		WithEditionHydration(editionRepo)
	return h, author, bookRepo
}

// TestFetchAuthorBooks_EditionHydrationIsBatched is the #1929 regression: the
// per-created-book Hardcover edition fetch used to run one at a time, so a 65
// work author paid 65 round trips back to back.
func TestFetchAuthorBooks_EditionHydrationIsBatched(t *testing.T) {
	const works = 6
	provider := newConcurrentEditionProvider(hardcoverWorks(works), 2)
	h, author, bookRepo := prefetchFixture(t, provider)

	h.FetchAuthorBooks(author, false, models.MediaTypeEbook)

	if got := provider.peakConcurrency(); got < 2 {
		t.Errorf("peak concurrent edition fetches = %d, want at least 2; hydration is still serial", got)
	}

	books, err := bookRepo.ListByAuthor(context.Background(), author.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(books) != works {
		t.Fatalf("created %d books, want %d", len(books), works)
	}
}

// TestFetchAuthorBooks_EditionPrefetchFetchesEachWorkOnce guards the other half:
// batching must not turn one call per book into two, once in the prefetch and
// again during hydration.
func TestFetchAuthorBooks_EditionPrefetchFetchesEachWorkOnce(t *testing.T) {
	const works = 4
	provider := newConcurrentEditionProvider(hardcoverWorks(works), 2)
	h, author, _ := prefetchFixture(t, provider)

	h.FetchAuthorBooks(author, false, models.MediaTypeEbook)

	provider.editionCallsMu.Lock()
	calls := append([]string(nil), provider.editionCalls...)
	provider.editionCallsMu.Unlock()

	seen := make(map[string]int, len(calls))
	for _, id := range calls {
		seen[id]++
	}
	for id, n := range seen {
		if n != 1 {
			t.Errorf("edition lookup for %s ran %d times, want 1", id, n)
		}
	}
	if len(seen) != works {
		t.Errorf("fetched editions for %d distinct works, want %d (calls: %v)", len(seen), works, calls)
	}
}

// TestFetchAuthorBooks_NoCreatesFetchesNoEditions keeps the batching from
// costing anything on the common case: a refresh of an author whose catalogue
// has not changed creates nothing, so it must fetch nothing.
func TestFetchAuthorBooks_NoCreatesFetchesNoEditions(t *testing.T) {
	provider := newConcurrentEditionProvider(hardcoverWorks(3), 2)
	h, author, _ := prefetchFixture(t, provider)

	h.FetchAuthorBooks(author, false, models.MediaTypeEbook)

	provider.editionCallsMu.Lock()
	provider.editionCalls = nil
	provider.editionCallsMu.Unlock()

	// Second pass: every work already exists, so nothing is created.
	h.FetchAuthorBooks(author, false, models.MediaTypeEbook)

	provider.editionCallsMu.Lock()
	calls := append([]string(nil), provider.editionCalls...)
	provider.editionCallsMu.Unlock()
	if len(calls) != 0 {
		t.Errorf("a refresh that creates nothing made %d edition lookups: %v", len(calls), calls)
	}
}

package api

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/vavallee/bindery/internal/metadata"
	"github.com/vavallee/bindery/internal/metadata/openlibrary"
	"github.com/vavallee/bindery/internal/models"
)

// coverCountingProvider is stubMetaProvider plus the edition cover fallback
// the aggregator runs during cover enrichment. It records every work it was
// asked to find a cover for and gives each one a cover.
type coverCountingProvider struct {
	*stubMetaProvider
	mu     sync.Mutex
	asked  []string
	onList func()
}

func (p *coverCountingProvider) FillMissingWorkCovers(_ context.Context, books []models.Book) int {
	p.mu.Lock()
	defer p.mu.Unlock()
	for i := range books {
		if books[i].ImageURL == "" {
			p.asked = append(p.asked, books[i].ForeignID)
			books[i].ImageURL = "https://covers.example.test/" + books[i].ForeignID + ".jpg"
		}
	}
	return len(books)
}

func (p *coverCountingProvider) GetAuthorWorks(ctx context.Context, id string) ([]models.Book, error) {
	if p.onList != nil {
		p.onList()
	}
	return p.stubMetaProvider.GetAuthorWorks(ctx, id)
}

func (p *coverCountingProvider) askedFor() []string {
	p.mu.Lock()
	defer p.mu.Unlock()
	return append([]string(nil), p.asked...)
}

// Review item 2: a discovery run enriches covers only for works the author
// does not already have, and the cover still reaches the created book. A
// manual refresh keeps enriching every coverless work.
func TestDiscoverAuthorBooks_CoverEnrichmentOnlyForNewWorks(t *testing.T) {
	works := []models.Book{existingWork(), discoveryWork(1), discoveryWork(2)}
	works[2].ImageURL = "https://covers.example.test/already.jpg"

	t.Run("discovery", func(t *testing.T) {
		f := newDiscoveryFixture(t, true)
		p := &coverCountingProvider{stubMetaProvider: &stubMetaProvider{works: works}}
		h := NewAuthorHandler(f.authors, nil, f.books, nil, metadata.NewAggregator(p), nil, f.profile, nil)
		if _, err := h.DiscoverAuthorBooks(context.Background(), f.author); err != nil {
			t.Fatal(err)
		}
		if got := p.askedFor(); len(got) != 1 || got[0] != "OL2236W1" {
			t.Fatalf("cover lookups = %v, want only the new coverless work OL2236W1", got)
		}
		created, err := f.books.GetByForeignID(context.Background(), "OL2236W1")
		if err != nil || created == nil || created.ImageURL == "" {
			t.Fatalf("created book = %+v, %v; want it stored with the enriched cover", created, err)
		}
	})

	t.Run("manual refresh", func(t *testing.T) {
		f := newDiscoveryFixture(t, true)
		p := &coverCountingProvider{stubMetaProvider: &stubMetaProvider{works: works}}
		h := NewAuthorHandler(f.authors, nil, f.books, nil, metadata.NewAggregator(p), nil, f.profile, nil)
		h.RefreshAuthorBooks(f.author, false, models.MediaTypeEbook)
		got := p.askedFor()
		if len(got) != 2 {
			t.Fatalf("cover lookups = %v, want both coverless works on the manual path", got)
		}
	})
}

// Review item 3: a bulk or manual refresh that starts while discovery is
// syncing the same author waits for it rather than being refused, and the
// two runs together create each work once and announce at most once.
func TestDiscoverAuthorBooks_ConcurrentBulkRefreshCreatesOnceAnnouncesOnce(t *testing.T) {
	for iter := 0; iter < 8; iter++ {
		f := newDiscoveryFixture(t, true)
		gate := make(chan struct{})
		entered := make(chan bool, 4)
		works := []models.Book{existingWork(), discoveryWork(1), discoveryWork(2), discoveryWork(3)}
		stub := &stubMetaProvider{
			works:           works,
			author:          &models.Author{ForeignID: "OL2236A", Name: "Ann Leckie", MetadataProvider: "openlibrary"},
			getAuthorBypass: entered,
			getAuthorGate:   gate,
		}
		rec := &eventRecorder{}
		// Unshared, so the refresh reaches the provider itself instead of
		// joining discovery's in flight author lookup, and the gate holds
		// both syncs at the same point.
		h := NewAuthorHandler(f.authors, nil, f.books, nil, metadata.NewAggregator(&unsharedMetaProvider{stubMetaProvider: stub}),
			nil, f.profile, nil).WithNotifier(rec)

		var wg sync.WaitGroup
		wg.Add(2)
		go func() {
			defer wg.Done()
			if _, err := h.DiscoverAuthorBooks(context.Background(), f.author); err != nil {
				t.Errorf("discovery: %v", err)
			}
		}()
		select {
		case <-entered:
		case <-time.After(5 * time.Second):
			t.Fatal("discovery never reached the provider")
		}
		snapshot := *f.author
		go func() {
			defer wg.Done()
			h.RefreshAuthorBooks(&snapshot, false, models.MediaTypeEbook)
		}()
		select {
		case <-entered:
		case <-time.After(5 * time.Second):
			t.Fatal("the bulk refresh was refused or never reached the provider; user started syncs must run")
		}
		close(gate)
		wg.Wait()

		books, err := f.books.ListByAuthor(context.Background(), f.author.ID)
		if err != nil {
			t.Fatal(err)
		}
		count := map[string]int{}
		for _, b := range books {
			count[b.ForeignID]++
		}
		for _, id := range []string{"OL2236W0", "OL2236W1", "OL2236W2", "OL2236W3"} {
			if count[id] != 1 {
				t.Fatalf("iteration %d: work %s has %d rows, want 1 (%v)", iter, id, count[id], count)
			}
		}
		if len(books) != 4 {
			t.Fatalf("iteration %d: %d books, want 4", iter, len(books))
		}
		if events := rec.snapshot(); len(events) > 1 {
			t.Fatalf("iteration %d: %d bookAnnounced events for one set of new works, want at most 1", iter, len(events))
		}
	}
}

// Review item 4: the job picked the author while it was monitored; by the time
// its turn comes the user has unmonitored it. Nothing is created or announced.
func TestDiscoverAuthorBooks_RereadsAuthorBeforeSyncing(t *testing.T) {
	cases := map[string]func(t *testing.T, f *discoveryFixture){
		"unmonitored": func(t *testing.T, f *discoveryFixture) {
			current := *f.author
			current.Monitored = false
			if err := f.authors.Update(context.Background(), &current); err != nil {
				t.Fatal(err)
			}
		},
		"new items none": func(t *testing.T, f *discoveryFixture) {
			current := *f.author
			current.MonitorNewItems = models.AuthorMonitorNewItemsNone
			if err := f.authors.Update(context.Background(), &current); err != nil {
				t.Fatal(err)
			}
		},
		"deleted": func(t *testing.T, f *discoveryFixture) {
			if err := f.authors.Delete(context.Background(), f.author.ID); err != nil {
				t.Fatal(err)
			}
		},
	}
	for name, change := range cases {
		t.Run(name, func(t *testing.T) {
			f := newDiscoveryFixture(t, true)
			selected := *f.author // what the job's batch query returned
			change(t, f)
			stub := &stubMetaProvider{works: []models.Book{existingWork(), discoveryWork(1)}}
			rec := &eventRecorder{}
			h := f.handler(stub, rec)

			created, err := h.DiscoverAuthorBooks(context.Background(), &selected)
			if err != nil || created != 0 {
				t.Fatalf("created %d, err %v; want 0, nil (skipped and counted as checked)", created, err)
			}
			if b, _ := f.books.GetByForeignID(context.Background(), "OL2236W1"); b != nil {
				t.Fatal("a book was created for an author no longer taking new books")
			}
			if events := rec.snapshot(); len(events) != 0 {
				t.Fatalf("sent %d events, want none", len(events))
			}
			if h.runningSyncs.running(f.author.ID) {
				t.Fatal("the skipped run left the author marked as syncing")
			}
		})
	}
}

// Review item 5a: a failed read of the author's books must abort the sync. It
// used to read as an empty author, so the run recreated and announced books.
func TestDiscoverAuthorBooks_BookReadFailureAbortsTheSync(t *testing.T) {
	f := newDiscoveryFixture(t, true)
	if err := f.authors.MarkCataloguePopulated(context.Background(), f.author.ID, time.Now()); err != nil {
		t.Fatal(err)
	}
	stub := &stubMetaProvider{works: []models.Book{existingWork(), discoveryWork(1)}}
	p := &coverCountingProvider{stubMetaProvider: stub, onList: func() {
		// Break the book list query (it joins book_files) while leaving the
		// books table writable, after the works fetch and before the read.
		if _, err := f.db.Exec(`ALTER TABLE book_files RENAME TO book_files_gone`); err != nil {
			t.Errorf("rename: %v", err)
		}
	}}
	rec := &eventRecorder{}
	h := NewAuthorHandler(f.authors, nil, f.books, nil, metadata.NewAggregator(p), nil, f.profile, nil).WithNotifier(rec)

	created, err := h.DiscoverAuthorBooks(context.Background(), f.author)
	if err == nil {
		t.Fatalf("created %d with no error; a failed read of the author's books must abort the run", created)
	}
	if created != 0 {
		t.Fatalf("created = %d, want 0", created)
	}
	if _, rerr := f.db.Exec(`ALTER TABLE book_files_gone RENAME TO book_files`); rerr != nil {
		t.Fatal(rerr)
	}
	if b, _ := f.books.GetByForeignID(context.Background(), "OL2236W1"); b != nil {
		t.Fatal("the aborted run still created a book")
	}
	if events := rec.snapshot(); len(events) != 0 {
		t.Fatalf("sent %d events, want none", len(events))
	}
}

// Review item 5b: an author the user emptied is not a populated catalogue.
// Its refill is a first population and announces nothing, even though
// catalogue_populated_at says it once had books.
func TestDiscoverAuthorBooks_EmptiedAuthorRefillDoesNotAnnounce(t *testing.T) {
	f := newDiscoveryFixture(t, true)
	ctx := context.Background()
	existing, err := f.books.GetByForeignID(ctx, "OL2236W0")
	if err != nil || existing == nil {
		t.Fatalf("fixture book: %v", err)
	}
	if err := f.books.Delete(ctx, existing.ID); err != nil {
		t.Fatal(err)
	}
	if when, err := f.authors.CataloguePopulatedAt(ctx, f.author.ID); err != nil || when == nil {
		t.Fatalf("catalogue_populated_at = %v, %v; the fixture should have stamped it", when, err)
	}
	rec := &eventRecorder{}
	h := f.handler(&stubMetaProvider{works: []models.Book{discoveryWork(1)}}, rec)
	created, err := h.DiscoverAuthorBooks(ctx, f.author)
	if err != nil || created != 1 {
		t.Fatalf("created %d, err %v; want 1, nil", created, err)
	}
	if events := rec.snapshot(); len(events) != 0 {
		t.Fatalf("refill of an emptied author sent %d events, want none", len(events))
	}
}

func TestRefreshConflictMessage(t *testing.T) {
	h := &AuthorHandler{}
	if msg := h.refreshConflictMessage(1); strings.Contains(msg, "checking") {
		t.Errorf("message without discovery = %q", msg)
	}
	h.discovering.Store(int64(1), struct{}{})
	if msg := h.refreshConflictMessage(1); !strings.Contains(msg, "already checking this author for new books") {
		t.Errorf("message during discovery = %q", msg)
	}
}

// Review item 1a: an OpenLibrary rate limit reaches DiscoverAuthorBooks's
// caller still marked, through the aggregator, so the job can back off.
func TestDiscoverAuthorBooks_ProviderRateLimitStaysMarked(t *testing.T) {
	f := newDiscoveryFixture(t, true)
	limited := fmt.Errorf("get author works OL2236A: primary=%w enrichment=%w",
		fmt.Errorf("HTTP 429: %w", openlibrary.ErrRateLimited), errors.New("HTTP 429"))
	agg := metadata.NewAggregator(ratelimitedWorksProvider{stubMetaProvider: &stubMetaProvider{}, err: limited})
	h := NewAuthorHandler(f.authors, nil, f.books, nil, agg, nil, f.profile, nil)
	_, err := h.DiscoverAuthorBooks(context.Background(), f.author)
	if !errors.Is(err, metadata.ErrRateLimited) {
		t.Fatalf("err = %v, want it to match metadata.ErrRateLimited", err)
	}
}

// unsharedMetaProvider is stubMetaProvider with a fresh cache scope for every
// request, so the aggregator neither caches its answers nor lets concurrent
// callers share one fetch (#2594). A test that needs each sync to reach the
// provider on its own uses it.
type unsharedMetaProvider struct {
	*stubMetaProvider
	requests atomic.Int64
}

func (p *unsharedMetaProvider) ResolveCacheProvider(context.Context) (metadata.Provider, string) {
	return p, strconv.FormatInt(p.requests.Add(1), 10)
}

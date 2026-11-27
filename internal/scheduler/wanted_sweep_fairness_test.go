package scheduler

import (
	"context"
	"database/sql"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/vavallee/bindery/internal/db"
	"github.com/vavallee/bindery/internal/indexer"
	"github.com/vavallee/bindery/internal/indexer/newznab"
	"github.com/vavallee/bindery/internal/models"
)

// The scheduled wanted sweep used to search the wanted list in title order
// every run with no memory of what it had searched. An indexer's daily query
// cap, or a restart, ends a sweep early, and the next one started from the
// same head: the first books by title were searched every run and the rest
// never. These tests pin the order that replaced it: never searched first,
// then least recently searched.

// allowanceSearcher stands in for an indexer with a query cap. It answers the
// first `left` searches as an indexer that was asked and had nothing, and
// refuses the rest the way the real searcher reports a spent cap: the indexer
// skipped, nothing sent.
type allowanceSearcher struct {
	mu       sync.Mutex
	left     int
	searched map[string]int
	order    []string
}

func (a *allowanceSearcher) SearchBook(ctx context.Context, idxs []models.Indexer, c indexer.MatchCriteria) []newznab.SearchResult {
	results, _ := a.SearchBookWithOutcomes(ctx, idxs, c)
	return results
}

func (a *allowanceSearcher) SearchBookWithOutcomes(_ context.Context, _ []models.Indexer, c indexer.MatchCriteria) ([]newznab.SearchResult, []indexer.IndexerDebug) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.left <= 0 {
		return nil, []indexer.IndexerDebug{{IndexerID: 1, IndexerName: "capped", Enabled: true,
			Skipped: true, SkipReason: "daily query cap reached (4 of 4), frees up in 24h0m0s"}}
	}
	a.left--
	if a.searched == nil {
		a.searched = map[string]int{}
	}
	a.searched[c.Title]++
	a.order = append(a.order, c.Title)
	return nil, []indexer.IndexerDebug{{IndexerID: 1, IndexerName: "capped", Enabled: true}}
}

// refill starts a new day: the cap frees up and the per sweep record clears.
func (a *allowanceSearcher) refill(n int) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.left = n
	a.order = nil
}

func (a *allowanceSearcher) sweepOrder() []string {
	a.mu.Lock()
	defer a.mu.Unlock()
	return append([]string(nil), a.order...)
}

func (a *allowanceSearcher) counts() map[string]int {
	a.mu.Lock()
	defer a.mu.Unlock()
	out := make(map[string]int, len(a.searched))
	for k, v := range a.searched {
		out[k] = v
	}
	return out
}

// paceSweepForTest replaces the 3 second launch gap. A few milliseconds, not
// zero: the sweep runs two searches at once, and the gap is what makes the
// book launched first reach the searcher first, so which books a cap admits
// is the queue order and not a race between two goroutines.
func paceSweepForTest(t *testing.T, gap time.Duration) {
	t.Helper()
	old := searchPaceInterval
	searchPaceInterval = gap
	t.Cleanup(func() { searchPaceInterval = old })
}

// seedWantedForSweep creates n monitored wanted ebooks titled "<prefix> 00"
// onwards under one monitored author, inserted in an order unrelated to their
// titles, and returns the titles in title order.
func seedWantedForSweep(t *testing.T, database *sql.DB, prefix string, n int) []string {
	t.Helper()
	ctx := context.Background()
	authors, books := db.NewAuthorRepo(database), db.NewBookRepo(database)
	author := &models.Author{ForeignID: "OL-" + prefix, Name: prefix + " Author", SortName: "Author, " + prefix,
		MetadataProvider: "ol", Monitored: true}
	if err := authors.Create(ctx, author); err != nil {
		t.Fatal(err)
	}
	titles := make([]string, n)
	for i := range n {
		k := (i*7 + 3) % n // 7 is coprime with every n used here
		title := fmt.Sprintf("%s %02d", prefix, k)
		titles[k] = title
		b := models.Book{ForeignID: fmt.Sprintf("OL-%s-%d", prefix, k), AuthorID: author.ID, Title: title, SortTitle: title,
			Status: models.BookStatusWanted, Genres: []string{}, MetadataProvider: "ol", Monitored: true,
			MediaType: models.MediaTypeEbook}
		if err := books.Create(ctx, &b); err != nil {
			t.Fatal(err)
		}
	}
	return titles
}

// sweepScheduler builds a scheduler the way a process start does: new repos
// over the database, nothing carried in memory from an earlier instance.
func sweepScheduler(database *sql.DB, searcher bookSearcher) *Scheduler {
	return &Scheduler{
		searcher: searcher, indexers: db.NewIndexerRepo(database), authors: db.NewAuthorRepo(database),
		books: db.NewBookRepo(database), settings: db.NewSettingsRepo(database),
		blocklist: db.NewBlocklistRepo(database), downloads: db.NewDownloadRepo(database),
		clients: db.NewDownloadClientRepo(database), profiles: db.NewMetadataProfileRepo(database),
	}
}

// N wanted books behind a cap that admits K searches a sweep: ceil(N/K) sweeps
// must search every book exactly once. Before the fix the same K books were
// searched every sweep and the other N-K never.
func TestSearchWanted_CappedSweepsCoverWholeWantedList(t *testing.T) {
	const (
		wanted   = 12
		perSweep = 4
	)
	paceSweepForTest(t, 10*time.Millisecond)
	database, err := db.OpenMemory()
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	titles := seedWantedForSweep(t, database, "Fairness", wanted)

	searcher := &allowanceSearcher{}
	s := sweepScheduler(database, searcher)
	sweeps := (wanted + perSweep - 1) / perSweep
	for n := range sweeps {
		searcher.refill(perSweep)
		s.searchWanted()
		t.Logf("sweep %d searched %v", n+1, searcher.sweepOrder())
	}

	counts := searcher.counts()
	var never, repeated []string
	for _, title := range titles {
		switch c := counts[title]; {
		case c == 0:
			never = append(never, title)
		case c > 1:
			repeated = append(repeated, fmt.Sprintf("%s x%d", title, c))
		}
	}
	if len(never) > 0 || len(repeated) > 0 {
		t.Fatalf("%d sweeps of %d searches over %d wanted books: never searched %v, searched more than once %v; want every book searched exactly once",
			sweeps, perSweep, wanted, never, repeated)
	}
}

// The first sweep over a list nothing has searched runs in title order, which
// is the order before the fix and the tie break between books with the same
// stamp. Once every book has been searched, the next rotation starts with the
// book searched longest ago.
func TestSearchWanted_OrderIsTitleThenLeastRecentlySearched(t *testing.T) {
	const wanted = 6
	paceSweepForTest(t, 10*time.Millisecond)
	database, err := db.OpenMemory()
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	titles := seedWantedForSweep(t, database, "Ordered", wanted)

	searcher := &allowanceSearcher{}
	s := sweepScheduler(database, searcher)

	searcher.refill(2)
	s.searchWanted()
	if got, want := searcher.sweepOrder(), titles[:2]; !sameTitles(got, want) {
		t.Fatalf("first sweep searched %v, want the first two by title %v", got, want)
	}
	searcher.refill(4)
	s.searchWanted()
	if got, want := searcher.sweepOrder(), titles[2:]; !sameTitles(got, want) {
		t.Fatalf("second sweep searched %v, want the four never searched %v", got, want)
	}
	// Everything has a stamp now. The two from the first sweep are the oldest.
	searcher.refill(2)
	s.searchWanted()
	if got, want := searcher.sweepOrder(), titles[:2]; !sameTitles(got, want) {
		t.Fatalf("third sweep searched %v, want the two searched longest ago %v", got, want)
	}
}

// sameTitles compares as sets: two searches run at once, so the order two
// neighbours reach the searcher in is not part of the contract.
func sameTitles(got, want []string) bool {
	g, w := append([]string(nil), got...), append([]string(nil), want...)
	sort.Strings(g)
	sort.Strings(w)
	return strings.Join(g, "|") == strings.Join(w, "|")
}

// A restart must not reset the rotation: the stamps are in the database, so a
// new scheduler with new repos carries on with the books the last one did not
// reach.
func TestSearchWanted_SearchStampsSurviveRestart(t *testing.T) {
	const (
		wanted   = 6
		perSweep = 3
	)
	paceSweepForTest(t, 10*time.Millisecond)
	database, err := db.OpenMemory()
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	titles := seedWantedForSweep(t, database, "Restart", wanted)

	before := &allowanceSearcher{}
	before.refill(perSweep)
	sweepScheduler(database, before).searchWanted()
	first := before.sweepOrder()
	if len(first) != perSweep {
		t.Fatalf("first instance searched %v, want %d books", first, perSweep)
	}

	// A new process: a new scheduler, new repos, a new searcher.
	after := &allowanceSearcher{}
	after.refill(perSweep)
	sweepScheduler(database, after).searchWanted()
	second := after.sweepOrder()

	seen := map[string]bool{}
	for _, title := range first {
		seen[title] = true
	}
	for _, title := range second {
		if seen[title] {
			t.Errorf("%q was searched by both instances; the restart forgot it had been searched", title)
		}
		seen[title] = true
	}
	if len(seen) != wanted {
		t.Fatalf("two instances searched %d of %d books (%v then %v), want all of %v", len(seen), wanted, first, second, titles)
	}
}

// The same property against the real searcher, quota and newznab client, with
// an httptest indexer that finds nothing and a real daily query cap. A day
// passing is the stored tally being cleared and a new searcher loading it,
// with a new scheduler, which is what a restart a day later looks like.
//
// One search costs several requests and a cap can run out part way through
// one, so the count per sweep is not fixed. The property that holds either
// way: every sweep reaches a book no earlier sweep did, until none are left.
func TestSearchWanted_RealDailyCapReachesEveryBook(t *testing.T) {
	const (
		wanted   = 15
		dailyCap = 20
	)
	paceSweepForTest(t, 0)
	database, err := db.OpenMemory()
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	ctx := context.Background()
	titles := seedWantedForSweep(t, database, "Capbook", wanted)

	var mu sync.Mutex
	asked := map[string]bool{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		w.Header().Set("Content-Type", "application/xml")
		if q.Get("t") == "caps" {
			_, _ = w.Write([]byte(`<?xml version="1.0"?><caps><searching><search available="yes" supportedParams="q"/><book-search available="yes" supportedParams="q,author,title"/></searching></caps>`))
			return
		}
		// Every query variant carries the title, as title= or inside q=.
		text := strings.ToLower(q.Get("title") + " " + q.Get("q"))
		mu.Lock()
		for _, title := range titles {
			if strings.Contains(text, strings.ToLower(title)) {
				asked[title] = true
			}
		}
		mu.Unlock()
		_, _ = w.Write([]byte(`<?xml version="1.0"?><rss version="2.0"><channel></channel></rss>`))
	}))
	defer srv.Close()

	indexers := db.NewIndexerRepo(database)
	limit := dailyCap
	if err := indexers.Create(ctx, &models.Indexer{Name: "capped", Type: "torznab", URL: srv.URL, Enabled: true,
		Categories: []int{7020}, DailyQueryLimit: &limit}); err != nil {
		t.Fatal(err)
	}
	askedCount := func() int {
		mu.Lock()
		defer mu.Unlock()
		return len(asked)
	}

	for sweep := 1; askedCount() < wanted; sweep++ {
		if sweep > wanted {
			t.Fatalf("%d sweeps and %d of %d books reached", sweep-1, askedCount(), wanted)
		}
		reached := askedCount()
		sweepScheduler(database, indexer.NewSearcher().WithQuota(indexers)).searchWanted()
		// The quota writes its tally behind the search; let the last write
		// land before the day is turned over.
		time.Sleep(50 * time.Millisecond)
		if _, err := database.ExecContext(ctx, "DELETE FROM indexer_query_counts"); err != nil {
			t.Fatal(err)
		}
		t.Logf("sweep %d: %d of %d books reached", sweep, askedCount(), wanted)
		// This also covers the books the cap refused: were they stamped as
		// searched, every book would carry a stamp after the first sweep in
		// the order it ran, and the second would repeat it.
		if askedCount() == reached {
			t.Fatalf("sweep %d reached no book the earlier sweeps had not: %d of %d, the capped sweep is repeating itself",
				sweep, reached, wanted)
		}
	}
}

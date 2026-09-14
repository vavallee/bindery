package metadata

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/vavallee/bindery/internal/models"
)

func TestCacheBypassed(t *testing.T) {
	if CacheBypassed(context.Background()) {
		t.Fatal("a plain context must not bypass the cache")
	}
	ctx := WithCacheBypass(context.Background())
	if !CacheBypassed(ctx) {
		t.Fatal("WithCacheBypass must be visible to CacheBypassed")
	}
	nested, bypassed := consumeCacheBypass(ctx)
	if !bypassed || CacheBypassed(nested) {
		t.Fatalf("consumeCacheBypass = (bypassed %v, nested still bypassed %v), want (true, false)", bypassed, CacheBypassed(nested))
	}
}

// TestGetAuthor_CacheBypassRefetchesAndRefreshesCache pins #2601: an explicit
// refresh must reach the provider even with a warm cache, and its answer must
// replace the cached one so the next ordinary read sees it.
func TestGetAuthor_CacheBypassRefetchesAndRefreshesCache(t *testing.T) {
	primary := &mockProvider{name: "ol", getAuthor: &models.Author{Name: "Ann Leckie", Description: "old bio"}}
	agg := newTestAggregator(primary)
	ctx := context.Background()

	if _, err := agg.GetAuthor(ctx, "OL2601A"); err != nil {
		t.Fatalf("warm GetAuthor: %v", err)
	}
	primary.getAuthor = &models.Author{Name: "Ann Leckie", Description: "new bio"}

	got, err := agg.GetAuthor(ctx, "OL2601A")
	if err != nil {
		t.Fatalf("ordinary GetAuthor: %v", err)
	}
	if got.Description != "old bio" {
		t.Fatalf("ordinary read = %q, want the cached %q", got.Description, "old bio")
	}

	got, err = agg.GetAuthor(WithCacheBypass(ctx), "OL2601A")
	if err != nil {
		t.Fatalf("bypassed GetAuthor: %v", err)
	}
	if got.Description != "new bio" {
		t.Fatalf("bypassed read = %q, want the provider's %q: it was served from the cache", got.Description, "new bio")
	}

	// A third provider answer proves the next ordinary read comes from the
	// cache, and that the cache now holds what the bypass fetched.
	primary.getAuthor = &models.Author{Name: "Ann Leckie", Description: "newer bio"}
	got, err = agg.GetAuthor(ctx, "OL2601A")
	if err != nil {
		t.Fatalf("ordinary GetAuthor after bypass: %v", err)
	}
	if got.Description != "new bio" {
		t.Fatalf("ordinary read after bypass = %q, want %q written back by the bypass", got.Description, "new bio")
	}
}

// A refresh that fails upstream must not cost the user the data they had.
func TestGetAuthor_CacheBypassErrorKeepsCachedEntry(t *testing.T) {
	primary := &mockProvider{name: "ol", getAuthor: &models.Author{Name: "Ann Leckie", Description: "old bio"}}
	agg := newTestAggregator(primary)
	ctx := context.Background()

	if _, err := agg.GetAuthor(ctx, "OL2601A"); err != nil {
		t.Fatalf("warm GetAuthor: %v", err)
	}
	primary.getAuthor = nil
	primary.getAuthorErr = errors.New("503 from upstream")
	if _, err := agg.GetAuthor(WithCacheBypass(ctx), "OL2601A"); err == nil {
		t.Fatal("bypassed GetAuthor swallowed the provider error")
	}

	primary.getAuthorErr = nil
	got, err := agg.GetAuthor(ctx, "OL2601A")
	if err != nil {
		t.Fatalf("ordinary GetAuthor: %v", err)
	}
	if got == nil || got.Description != "old bio" {
		t.Fatalf("ordinary read after a failed bypass = %+v, want the cached author kept", got)
	}
}

// TestGetAuthorWorksForAuthor_CacheBypassRefetchesAndRefreshesCache covers the
// catalogue half of #2601. Both cache layers under the merged catalogue (the
// merged entry and the raw primary works) have to be skipped, or the refresh
// re-merges a day old primary list with a fresh supplement.
func TestGetAuthorWorksForAuthor_CacheBypassRefetchesAndRefreshesCache(t *testing.T) {
	primary := &mockWorksProvider{
		mockProvider: mockProvider{name: "ol", authorWorks: []models.Book{
			{ForeignID: "OL1W", Title: "Ancillary Justice", ImageURL: "cover1", MetadataProvider: "openlibrary"},
		}},
	}
	hardcover := &mockAuthorWorksByNameProvider{mockProvider: mockProvider{name: "hardcover"}}
	agg := &Aggregator{
		primary:   primary,
		enrichers: []Provider{hardcover},
		cache:     newTTLCache(time.Minute),
	}
	author := models.Author{ForeignID: "OL2601A", Name: "Ann Leckie"}
	ctx := context.Background()

	got, err := agg.GetAuthorWorksForAuthor(ctx, author)
	if err != nil {
		t.Fatalf("warm GetAuthorWorksForAuthor: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("warm catalogue = %v, want 1 work", workTitles(got))
	}

	// Upstream gains a new primary work and a new supplemental one.
	primary.authorWorks = []models.Book{
		{ForeignID: "OL1W", Title: "Ancillary Justice", ImageURL: "cover1", MetadataProvider: "openlibrary"},
		{ForeignID: "OL2W", Title: "Translation State", ImageURL: "cover2", MetadataProvider: "openlibrary"},
	}
	hardcover.authorWorksByName = []models.Book{
		{ForeignID: "hc:provenance", Title: "Provenance", ImageURL: "cover3", MetadataProvider: "hardcover"},
	}

	got, err = agg.GetAuthorWorksForAuthor(ctx, author)
	if err != nil {
		t.Fatalf("ordinary GetAuthorWorksForAuthor: %v", err)
	}
	if len(got) != 1 || primary.authorWorksCalls != 1 || hardcover.calls != 1 {
		t.Fatalf("ordinary read: %v, primary calls %d, supplement calls %d; want the cached 1 work and no new calls",
			workTitles(got), primary.authorWorksCalls, hardcover.calls)
	}

	got, err = agg.GetAuthorWorksForAuthor(WithCacheBypass(ctx), author)
	if err != nil {
		t.Fatalf("bypassed GetAuthorWorksForAuthor: %v", err)
	}
	if primary.authorWorksCalls != 2 {
		t.Fatalf("primary works calls = %d, want 2: the bypass stopped at the merged entry and reused the raw works cache", primary.authorWorksCalls)
	}
	if hardcover.calls != 2 {
		t.Fatalf("supplement calls = %d, want 2", hardcover.calls)
	}
	if len(got) != 3 {
		t.Fatalf("bypassed catalogue = %v, want Ancillary Justice, Translation State and Provenance", workTitles(got))
	}

	// The next ordinary read serves the refreshed catalogue from the cache.
	primary.authorWorks = nil
	hardcover.authorWorksByName = nil
	got, err = agg.GetAuthorWorksForAuthor(ctx, author)
	if err != nil {
		t.Fatalf("ordinary GetAuthorWorksForAuthor after bypass: %v", err)
	}
	if len(got) != 3 || primary.authorWorksCalls != 2 || hardcover.calls != 2 {
		t.Fatalf("ordinary read after bypass: %v with primary calls %d, supplement calls %d; want the 3 refreshed works from the cache",
			workTitles(got), primary.authorWorksCalls, hardcover.calls)
	}
}

// The bypass rides the refresh context into every lookup the sync makes. The
// ones that are not the author's profile or catalogue have to keep their
// cache: the refresh is for one author, not for every book they touch.
func TestCacheBypass_LeavesBookCacheAlone(t *testing.T) {
	primary := &mockProvider{name: "ol", getBook: &models.Book{ForeignID: "OL1W", Title: "Ancillary Justice"}}
	agg := newTestAggregator(primary)
	ctx := context.Background()

	if _, err := agg.GetBook(ctx, "OL1W"); err != nil {
		t.Fatalf("warm GetBook: %v", err)
	}
	if _, err := agg.GetBook(WithCacheBypass(ctx), "OL1W"); err != nil {
		t.Fatalf("GetBook under a bypass context: %v", err)
	}
	if primary.getBookCalls != 1 {
		t.Fatalf("GetBook provider calls = %d, want 1: the author refresh bypass leaked into the book cache", primary.getBookCalls)
	}
}

// refreshWorksProvider is a primary that says whether its works answer is
// intact, the way OpenLibrary's GetAuthorWorksForRefresh does. Its plain
// GetAuthorWorks returns the same list with the signal dropped, which is what
// the aggregator saw before it asked.
type refreshWorksProvider struct {
	mockWorksProvider
	intact       bool
	refreshCalls int
}

func (m *refreshWorksProvider) GetAuthorWorksForRefresh(_ context.Context, _ string) ([]models.Book, bool, error) {
	m.refreshCalls++
	return m.authorWorks, m.intact, m.authorWorksErr
}

func olWork(id, title string) models.Book {
	return models.Book{ForeignID: id, Title: title, ImageURL: "cover-" + id, MetadataProvider: "openlibrary"}
}

// hasTitles reports whether books holds exactly the given titles, in any order.
func hasTitles(books []models.Book, titles ...string) bool {
	if len(books) != len(titles) {
		return false
	}
	seen := make(map[string]int, len(books))
	for _, b := range books {
		seen[b.Title]++
	}
	for _, title := range titles {
		if seen[title] == 0 {
			return false
		}
		seen[title]--
	}
	return true
}

// A refresh whose works answer came back short because a call failed (an
// OpenLibrary works page after the first one erroring, which the client
// returns as success) must not replace a complete cached catalogue: bulk,
// scheduled and ISBN canonicalisation reads would get the short list for a
// day. The sync still gets the works the short answer adds.
func TestGetAuthorWorksForAuthor_CacheBypassPartialAnswerKeepsCachedCatalogue(t *testing.T) {
	primary := &refreshWorksProvider{
		mockWorksProvider: mockWorksProvider{mockProvider: mockProvider{name: "ol", authorWorks: []models.Book{
			olWork("OL1W", "Ancillary Justice"), olWork("OL2W", "Ancillary Sword"), olWork("OL3W", "Ancillary Mercy"),
		}}},
		intact: true,
	}
	agg := &Aggregator{primary: primary, cache: newTTLCache(time.Hour)}
	author := models.Author{ForeignID: "OL2611A", Name: "Ann Leckie"}
	ctx := context.Background()

	if got, err := agg.GetAuthorWorksForAuthor(ctx, author); err != nil || len(got) != 3 {
		t.Fatalf("warm GetAuthorWorksForAuthor = %v, %v; want 3 works", workTitles(got), err)
	}

	// Page two fails: the provider answers with page one, which also carries a
	// work that is new upstream.
	primary.authorWorks = []models.Book{olWork("OL1W", "Ancillary Justice"), olWork("OL4W", "Translation State")}
	primary.intact = false
	got, err := agg.GetAuthorWorksForAuthor(WithCacheBypass(ctx), author)
	if err != nil {
		t.Fatalf("bypassed GetAuthorWorksForAuthor: %v", err)
	}
	if !hasTitles(got, "Ancillary Justice", "Ancillary Sword", "Ancillary Mercy", "Translation State") {
		t.Fatalf("bypassed catalogue from a partial answer = %v, want the cached three plus the new Translation State", workTitles(got))
	}

	// Ordinary readers still get the complete cached catalogue, not the short
	// answer. The provider now returns nothing, so anything read comes from
	// the cache.
	primary.authorWorks = nil
	primary.intact = true
	merged, err := agg.GetAuthorWorksForAuthor(ctx, author)
	if err != nil {
		t.Fatalf("ordinary GetAuthorWorksForAuthor: %v", err)
	}
	raw, err := agg.GetAuthorWorksUnenriched(ctx, author.ForeignID)
	if err != nil {
		t.Fatalf("ordinary GetAuthorWorksUnenriched: %v", err)
	}
	if !hasTitles(merged, "Ancillary Justice", "Ancillary Sword", "Ancillary Mercy") || !hasTitles(raw, "Ancillary Justice", "Ancillary Sword", "Ancillary Mercy") {
		t.Fatalf("after one partial bypassed answer, ordinary reads serve %v (merged) and %v (raw); the cache held all 3 before the click",
			workTitles(merged), workTitles(raw))
	}

	// An intact answer does replace the cache.
	primary.authorWorks = []models.Book{
		olWork("OL1W", "Ancillary Justice"), olWork("OL2W", "Ancillary Sword"), olWork("OL3W", "Ancillary Mercy"), olWork("OL4W", "Translation State"),
	}
	if got, err := agg.GetAuthorWorksForAuthor(WithCacheBypass(ctx), author); err != nil || len(got) != 4 {
		t.Fatalf("intact bypassed read = %v, %v; want 4 works", workTitles(got), err)
	}
	primary.authorWorks = nil
	if got, _ := agg.GetAuthorWorksForAuthor(ctx, author); len(got) != 4 {
		t.Fatalf("ordinary read after an intact refresh = %v, want the 4 refreshed works from the cache", workTitles(got))
	}
	if primary.refreshCalls != 2 {
		t.Fatalf("GetAuthorWorksForRefresh calls = %d, want 2 (one per bypassed read)", primary.refreshCalls)
	}
}

// A refresh that cannot reach the primary at all gives the sync the cached
// catalogue rather than failing it, so the click is never worse than the
// cached answer it replaced.
func TestGetAuthorWorksForAuthor_CacheBypassProviderErrorServesCachedCatalogue(t *testing.T) {
	primary := &refreshWorksProvider{
		mockWorksProvider: mockWorksProvider{mockProvider: mockProvider{name: "ol", authorWorks: []models.Book{
			olWork("OL1W", "Ancillary Justice"), olWork("OL2W", "Ancillary Sword"),
		}}},
		intact: true,
	}
	agg := &Aggregator{primary: primary, cache: newTTLCache(time.Hour)}
	author := models.Author{ForeignID: "OL2611A", Name: "Ann Leckie"}
	ctx := context.Background()

	if _, err := agg.GetAuthorWorksForAuthor(ctx, author); err != nil {
		t.Fatalf("warm GetAuthorWorksForAuthor: %v", err)
	}
	primary.authorWorks = nil
	primary.authorWorksErr = errors.New("503 from upstream")
	got, err := agg.GetAuthorWorksForAuthor(WithCacheBypass(ctx), author)
	if err != nil {
		t.Fatalf("bypassed GetAuthorWorksForAuthor with a cached catalogue failed the sync: %v", err)
	}
	if !hasTitles(got, "Ancillary Justice", "Ancillary Sword") {
		t.Fatalf("bypassed catalogue after a provider error = %v, want the cached 2 works", workTitles(got))
	}

	// With nothing cached there is nothing to fall back to.
	cold := &Aggregator{primary: primary, cache: newTTLCache(time.Hour)}
	if _, err := cold.GetAuthorWorksForAuthor(WithCacheBypass(ctx), author); err == nil {
		t.Fatal("bypassed GetAuthorWorksForAuthor with a cold cache swallowed the provider error")
	}
}

// With Hardcover throttled, a bypassed catalogue skips the compilation prune
// only Hardcover can drive, and the manual refresh sync would create box set
// rows from it. The cached merged catalogue carries the last prune decisions,
// so it is what the sync gets.
func TestGetAuthorWorksForAuthor_CacheBypassSupplementFailureServesCachedCatalogue(t *testing.T) {
	primary := &mockWorksProvider{mockProvider: mockProvider{name: "ol", authorWorks: []models.Book{
		olWork("OL1W", "Ancillary Justice"), olWork("OL9W", "Moon Harvest"),
	}}}
	hc := &mockAuthorWorksByNameProvider{mockProvider: mockProvider{name: "hardcover"},
		authorWorksByName: []models.Book{{ForeignID: "hc:mh", Title: "Moon Harvest", IsCompilation: true, MetadataProvider: "hardcover"}}}
	agg := &Aggregator{primary: primary, enrichers: []Provider{hc}, cache: newTTLCache(time.Hour)}
	author := models.Author{ForeignID: "OL2611A", Name: "Ann Leckie"}
	ctx := context.Background()

	warm, err := agg.GetAuthorWorksForAuthor(ctx, author)
	if err != nil || !hasTitles(warm, "Ancillary Justice") {
		t.Fatalf("warm = %v, %v; want the compilation pruned", workTitles(warm), err)
	}

	hc.authorWorksByNameErr = errors.New("429 after retries")
	got, err := agg.GetAuthorWorksForAuthor(WithCacheBypass(ctx), author)
	if err != nil {
		t.Fatalf("bypassed GetAuthorWorksForAuthor: %v", err)
	}
	if hc.calls != 2 {
		t.Fatalf("supplement calls = %d, want 2: the bypass should still ask Hardcover", hc.calls)
	}
	if !hasTitles(got, "Ancillary Justice") {
		t.Fatalf("a failed supplement under the bypass hands the sync %v instead of the pruned cached [Ancillary Justice]", workTitles(got))
	}
}

// GetAuthorWorksUnenriched prefers the enriched authorworks: entry. A refresh
// through GetAuthorWorksForAuthor rewrites the raw entry but not that one, so
// without dropping it the unenriched readers (ABS import title matching) keep
// the pre-refresh list for a day.
func TestGetAuthorWorksUnenriched_ServesRefreshedRawOverStaleEnriched(t *testing.T) {
	primary := &mockWorksProvider{mockProvider: mockProvider{name: "ol", authorWorks: []models.Book{olWork("OL1W", "Ancillary Justice")}}}
	agg := &Aggregator{primary: primary, cache: newTTLCache(time.Hour)}
	author := models.Author{ForeignID: "OL2611A", Name: "Ann Leckie"}
	ctx := context.Background()

	if got, err := agg.GetAuthorWorks(ctx, author.ForeignID); err != nil || len(got) != 1 {
		t.Fatalf("warm GetAuthorWorks = %v, %v", workTitles(got), err)
	}
	primary.authorWorks = []models.Book{olWork("OL1W", "Ancillary Justice"), olWork("OL2W", "Translation State")}
	if got, err := agg.GetAuthorWorksForAuthor(WithCacheBypass(ctx), author); err != nil || len(got) != 2 {
		t.Fatalf("bypassed GetAuthorWorksForAuthor = %v, %v; want 2 works", workTitles(got), err)
	}

	primary.authorWorks = nil
	raw, err := agg.GetAuthorWorksUnenriched(ctx, author.ForeignID)
	if err != nil {
		t.Fatalf("ordinary GetAuthorWorksUnenriched: %v", err)
	}
	if !hasTitles(raw, "Ancillary Justice", "Translation State") {
		t.Fatalf("GetAuthorWorksUnenriched after a refresh = %v, want the 2 refreshed works, not the stale enriched entry", workTitles(raw))
	}
	enriched, err := agg.GetAuthorWorks(ctx, author.ForeignID)
	if err != nil {
		t.Fatalf("ordinary GetAuthorWorks: %v", err)
	}
	if !hasTitles(enriched, "Ancillary Justice", "Translation State") {
		t.Fatalf("GetAuthorWorks after a refresh = %v, want it rebuilt from the refreshed raw works", workTitles(enriched))
	}
	if primary.authorWorksCalls != 2 {
		t.Fatalf("primary works calls = %d, want 2: the reads after the refresh should come from the cache", primary.authorWorksCalls)
	}
}

type mockAudibleCatalogue struct {
	books []models.Book
	err   error
	calls int
}

func (m *mockAudibleCatalogue) SearchBooksByAuthor(_ context.Context, _ string) ([]models.Book, error) {
	m.calls++
	return m.books, m.err
}

// Audiobook and "both" setups add the author's Audible catalogue to the sync.
// A manual refresh has to read it past the cache too, or a new Audible only
// release stays hidden for a day; and a failed Audible call must not cost the
// sync the cached list.
func TestGetAuthorAudiobooks_CacheBypassRefetchesAndKeepsCacheOnError(t *testing.T) {
	aud := &mockAudibleCatalogue{books: []models.Book{{ASIN: "B001", Title: "Ancillary Justice", MediaType: models.MediaTypeAudiobook}}}
	agg := &Aggregator{audible: aud, cache: newTTLCache(time.Hour)}
	ctx := context.Background()

	if got, err := agg.GetAuthorAudiobooks(ctx, "Ann Leckie"); err != nil || len(got) != 1 {
		t.Fatalf("warm GetAuthorAudiobooks = %v, %v", workTitles(got), err)
	}
	aud.books = []models.Book{
		{ASIN: "B001", Title: "Ancillary Justice", MediaType: models.MediaTypeAudiobook},
		{ASIN: "B002", Title: "Lake of Souls", MediaType: models.MediaTypeAudiobook},
	}
	if got, _ := agg.GetAuthorAudiobooks(ctx, "Ann Leckie"); len(got) != 1 || aud.calls != 1 {
		t.Fatalf("ordinary read = %v with %d Audible calls, want the cached 1 and no new call", workTitles(got), aud.calls)
	}

	got, err := agg.GetAuthorAudiobooks(WithCacheBypass(ctx), "Ann Leckie")
	if err != nil {
		t.Fatalf("bypassed GetAuthorAudiobooks: %v", err)
	}
	if !hasTitles(got, "Ancillary Justice", "Lake of Souls") {
		t.Fatalf("bypassed read = %v, want the new Audible release: it was served from the cache", workTitles(got))
	}
	if got, _ := agg.GetAuthorAudiobooks(ctx, "Ann Leckie"); len(got) != 2 || aud.calls != 2 {
		t.Fatalf("ordinary read after bypass = %v with %d calls, want the 2 refreshed books from the cache", workTitles(got), aud.calls)
	}

	aud.books = nil
	aud.err = errors.New("503 from Audible")
	got, err = agg.GetAuthorAudiobooks(WithCacheBypass(ctx), "Ann Leckie")
	if err != nil {
		t.Fatalf("bypassed GetAuthorAudiobooks with a cached list returned the Audible error: %v", err)
	}
	if len(got) != 2 || aud.calls != 3 {
		t.Fatalf("bypassed read after an Audible error = %v with %d calls, want the cached 2 after one attempt", workTitles(got), aud.calls)
	}
	if got, _ := agg.GetAuthorAudiobooks(ctx, "Ann Leckie"); len(got) != 2 {
		t.Fatalf("ordinary read after a failed bypass = %v, want the cached 2 kept", workTitles(got))
	}
}

func workTitles(books []models.Book) []string {
	titles := make([]string, 0, len(books))
	for _, b := range books {
		titles = append(titles, b.Title)
	}
	return titles
}

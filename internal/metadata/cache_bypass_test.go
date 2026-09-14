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

func workTitles(books []models.Book) []string {
	titles := make([]string, 0, len(books))
	for _, b := range books {
		titles = append(titles, b.Title)
	}
	return titles
}

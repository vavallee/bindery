package abs

import (
	"bytes"
	"context"
	"fmt"
	"log/slog"
	"path/filepath"
	"runtime/pprof"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/vavallee/bindery/internal/metadata"
	"github.com/vavallee/bindery/internal/models"
)

// slowCoverEnricher stands in for a rate-limited enricher (DNB's SRU search,
// Hardcover under its throttle): every SearchBooks costs a fixed delay. It
// counts calls so a test can tell one lookup from a per-work fan-out.
type slowCoverEnricher struct {
	delay time.Duration
	mu    sync.Mutex
	calls int
}

func (e *slowCoverEnricher) Name() string { return "slowcovers" }
func (e *slowCoverEnricher) SearchAuthors(context.Context, string) ([]models.Author, error) {
	return nil, nil
}
func (e *slowCoverEnricher) SearchBooks(ctx context.Context, _ string) ([]models.Book, error) {
	e.mu.Lock()
	e.calls++
	e.mu.Unlock()
	select {
	case <-time.After(e.delay):
	case <-ctx.Done():
		return nil, ctx.Err()
	}
	return nil, nil
}
func (e *slowCoverEnricher) GetAuthor(context.Context, string) (*models.Author, error) {
	return nil, nil
}
func (e *slowCoverEnricher) GetBook(context.Context, string) (*models.Book, error) {
	return nil, nil
}
func (e *slowCoverEnricher) GetEditions(context.Context, string) ([]models.Edition, error) {
	return nil, nil
}
func (e *slowCoverEnricher) GetBookByISBN(context.Context, string) (*models.Book, error) {
	return nil, nil
}
func (e *slowCoverEnricher) callCount() int {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.calls
}

// runImportWithWatchdog runs the import and fails the test with a goroutine
// dump of the importer if it has not returned within limit. The dump is the
// point: a stalled import parks without logging anything, and the blocked
// frame is the only evidence of where.
func runImportWithWatchdog(ctx context.Context, t *testing.T, importer *Importer, cfg ImportConfig, limit time.Duration) (*ImportStats, error) {
	t.Helper()
	type outcome struct {
		stats *ImportStats
		err   error
	}
	done := make(chan outcome, 1)
	go func() {
		stats, err := importer.Run(ctx, cfg)
		done <- outcome{stats, err}
	}()
	select {
	case got := <-done:
		return got.stats, got.err
	case <-time.After(limit):
		var buf bytes.Buffer
		_ = pprof.Lookup("goroutine").WriteTo(&buf, 2)
		var stacks []string
		for _, g := range strings.Split(buf.String(), "\n\n") {
			// Every importer goroutine, minus the test goroutine parked in
			// this select.
			if strings.Contains(g, "internal/abs.") && !strings.Contains(g, "testing.tRunner") {
				stacks = append(stacks, g)
			}
		}
		t.Fatalf("import still running after %s (progress %+v); importer goroutines:\n%s",
			limit, importer.Progress(), strings.Join(stacks, "\n\n"))
		return nil, nil
	}
}

// TestImporter_LargeAuthorCatalogueDoesNotStallItem reproduces #2578. An item
// by an author with a huge upstream catalogue (Arthur Conan Doyle: OpenLibrary
// returns its 2,000 work pagination cap) parked the whole import. The book
// title lookup asked the aggregator for the author's works, and the aggregator
// ran its per-work cover enrichment over every coverless work before
// answering: one rate-limited enricher round trip per work, thousands of
// them, all at DEBUG, inside one ABS item.
//
// The title lookup only needs titles and foreign IDs, so it must not pay for
// that fan-out.
func TestImporter_LargeAuthorCatalogueDoesNotStallItem(t *testing.T) {
	importer, _, bookRepo, _, _, _, _, _, _, _ := newABSImporterFixture(t)

	const authorID = "OL161167A"
	works := make([]models.Book, 0, 2000)
	for n := range 2000 {
		works = append(works, models.Book{
			ForeignID:        fmt.Sprintf("OL%dW", 100000+n),
			Title:            fmt.Sprintf("Collected Stories Volume %d", n),
			MetadataProvider: "openlibrary",
			Status:           models.BookStatusWanted,
		})
	}
	provider := &stubABSMetadataProvider{
		searchAuthors: []models.Author{{ForeignID: authorID, Name: "Arthur Conan Doyle", MetadataProvider: "openlibrary"}},
		authors: map[string]*models.Author{
			authorID: {ForeignID: authorID, Name: "Arthur Conan Doyle", SortName: "Doyle, Arthur Conan", MetadataProvider: "openlibrary"},
		},
		works: map[string][]models.Book{authorID: works},
	}
	enricher := &slowCoverEnricher{delay: 50 * time.Millisecond}
	importer.WithMetadata(metadata.NewAggregator(provider, enricher))

	item := sampleABSItem()
	item.ItemID = "li-sherlock"
	item.Title = "Sherlock Holmes Hörbücher"
	item.ASIN = ""
	item.Series = nil
	item.EbookPath = ""
	item.EbookINO = ""
	item.Authors = []NormalizedAuthor{{ID: "author-acd", Name: "Arthur Conan Doyle"}}
	item.Path = "/audiobooks/Sherlock Holmes"
	item.AudioFiles = make([]NormalizedAudioFile, 0, 702)
	for n := range 702 {
		item.AudioFiles = append(item.AudioFiles, NormalizedAudioFile{
			INO:  fmt.Sprintf("ino-%d", n),
			Path: fmt.Sprintf("/audiobooks/Sherlock Holmes/%03d.mp3", n),
		})
	}
	importer.enumerateFn = func(ctx context.Context, _ string, fn func(context.Context, NormalizedLibraryItem) error) (EnumerationStats, error) {
		if err := fn(ctx, item); err != nil {
			return EnumerationStats{}, err
		}
		return EnumerationStats{PagesScanned: 1, ItemsSeen: 1, ItemsNormalized: 1}, nil
	}

	stats, err := runImportWithWatchdog(context.Background(), t, importer, ImportConfig{
		SourceID:  DefaultSourceID,
		BaseURL:   "https://abs.example.com",
		APIKey:    "secret",
		LibraryID: item.LibraryID,
		Label:     "Shelf",
		Enabled:   true,
	}, 10*time.Second)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if stats.BooksCreated != 1 || stats.Failed != 0 {
		t.Fatalf("stats = %+v, want the item imported, not failed", stats)
	}
	// One enrichment of the matched book at most, never one per catalogue work.
	if calls := enricher.callCount(); calls > 20 {
		t.Fatalf("enricher SearchBooks calls = %d for one ABS item; the title lookup fanned out over the author's catalogue", calls)
	}
	books, err := bookRepo.List(context.Background())
	if err != nil || len(books) != 1 {
		t.Fatalf("books = %d err=%v, want 1", len(books), err)
	}
}

const (
	blockedAuthorID   = "OL161167A"
	blockedAuthorName = "Arthur Conan Doyle"
)

// blockingAuthorProvider wraps the ABS stub provider so one author's works
// never arrive. Searches matching blockName resolve to an upstream author, and
// that author's GetAuthorWorks parks until its context ends: the same call the
// reporter's import sat in, and one with no deadline of its own (the author
// search is bounded by the aggregator; the works fetch is not). onSearch, when
// set, runs on every author search so a test can act at a precise point.
type blockingAuthorProvider struct {
	*stubABSMetadataProvider
	blockName string

	mu           sync.Mutex
	blockedCalls int // searches for blockName: how often its item was entered
	onSearch     func(query string)
}

func (p *blockingAuthorProvider) SearchAuthors(ctx context.Context, query string) ([]models.Author, error) {
	p.mu.Lock()
	hook := p.onSearch
	hit := strings.Contains(strings.ToLower(query), p.blockName)
	if hit {
		p.blockedCalls++
	}
	p.mu.Unlock()
	if hook != nil {
		hook(query)
	}
	if hit {
		return []models.Author{{ForeignID: blockedAuthorID, Name: blockedAuthorName, MetadataProvider: "openlibrary"}}, nil
	}
	return p.stubABSMetadataProvider.SearchAuthors(ctx, query)
}

func (p *blockingAuthorProvider) GetAuthor(ctx context.Context, foreignID string) (*models.Author, error) {
	if foreignID == blockedAuthorID {
		return &models.Author{ForeignID: blockedAuthorID, Name: blockedAuthorName, SortName: "Doyle, Arthur Conan", MetadataProvider: "openlibrary"}, nil
	}
	return p.stubABSMetadataProvider.GetAuthor(ctx, foreignID)
}

func (p *blockingAuthorProvider) GetAuthorWorks(ctx context.Context, foreignID string) ([]models.Book, error) {
	if foreignID == blockedAuthorID {
		<-ctx.Done()
		return nil, ctx.Err()
	}
	return p.stubABSMetadataProvider.GetAuthorWorks(ctx, foreignID)
}

func (p *blockingAuthorProvider) setHook(hook func(string)) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.onSearch = hook
	p.blockedCalls = 0
}

func (p *blockingAuthorProvider) blocked() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.blockedCalls
}

// TestImporter_ItemTimeoutFailsItemAndCheckpointsPastIt is the backstop half
// of #2578: whatever the cause, one item that never finishes must cost that
// item, not the run. It times out and is recorded failed, the import moves on,
// and the checkpoint moves past it, so a restart does not resume into it. The
// restart is simulated the way the reporter's container restart happened:
// the run is cancelled while a later item is in flight.
func TestImporter_ItemTimeoutFailsItemAndCheckpointsPastIt(t *testing.T) {
	importer, _, bookRepo, _, _, _, _, _, _, _ := newABSImporterFixture(t)
	importer.itemTimeout = 300 * time.Millisecond

	provider := &blockingAuthorProvider{stubABSMetadataProvider: &stubABSMetadataProvider{}, blockName: "conan doyle"}
	importer.WithMetadata(metadata.NewAggregator(provider))

	items := []LibraryItem{
		sampleLibraryItemForLibrary("lib-books", "li-a", "Book A"),
		sampleLibraryItemForLibrary("lib-books", "li-b", "Book B"),
		sampleLibraryItemForLibrary("lib-books", "li-c", "Book C"),
	}
	// Distinct authors so the author matcher cannot pair them, and no ASIN so
	// nothing reaches the aggregator's live audnex client.
	for idx, name := range []string{"Muriel Barbery", "Arthur Conan Doyle", "Anne Weber"} {
		items[idx].Media.Metadata.Authors = []Author{{ID: "author-" + items[idx].ID, Name: name}}
		items[idx].Media.Metadata.ASIN = ""
	}
	client := &fakeEnumerationClient{pages: map[string]map[int]*LibraryItemsPage{
		"lib-books": {0: {MediaType: "book", Page: 0, Limit: 50, Total: len(items), Results: items}},
	}}
	importer.newClient = func(string, string) (enumerationClient, error) { return client, nil }
	cfg := ImportConfig{
		SourceID:  DefaultSourceID,
		BaseURL:   "https://abs.example.com",
		APIKey:    "secret",
		LibraryID: "lib-books",
		Label:     "Shelf",
		Enabled:   true,
	}

	runCtx, stop := context.WithCancel(context.Background())
	defer stop()
	provider.setHook(func(query string) {
		if strings.Contains(strings.ToLower(query), "anne weber") {
			stop()
		}
	})
	_, _ = runImportWithWatchdog(runCtx, t, importer, cfg, 10*time.Second)

	results := map[string]ImportItemResult{}
	for _, r := range importer.Progress().Results {
		results[r.ItemID] = r
	}
	if r := results["li-b"]; r.Outcome != itemOutcomeFailed || !strings.Contains(r.Message, "timed out after 300ms") {
		t.Fatalf("li-b result = %+v, want failed with a timeout message", r)
	}
	if r := results["li-a"]; r.Outcome != itemOutcomeCreated {
		t.Fatalf("li-a result = %+v, want created", r)
	}
	if r, ok := results["li-c"]; ok {
		t.Fatalf("li-c recorded as processed although the run stopped while it was in flight: %+v", r)
	}
	checkpoint, err := loadImportCheckpoint(context.Background(), importer.settings)
	if err != nil {
		t.Fatalf("load checkpoint: %v", err)
	}
	if checkpoint == nil || checkpoint.LastItemID != "li-b" {
		t.Fatalf("checkpoint = %+v, want LastItemID li-b: past the timed out item, before the interrupted one", checkpoint)
	}

	// The restart resumes: the timed out item is not entered again, and the
	// interrupted one is retried.
	provider.setHook(nil)
	if _, err := runImportWithWatchdog(context.Background(), t, importer, cfg, 10*time.Second); err != nil {
		t.Fatalf("resumed Run: %v", err)
	}
	if n := provider.blocked(); n != 0 {
		t.Fatalf("resumed run looked up li-b's author %d times; it resumed into the item that timed out", n)
	}
	books, err := bookRepo.List(context.Background())
	if err != nil {
		t.Fatalf("list books: %v", err)
	}
	var sawC bool
	for _, b := range books {
		if b.Title == "Book C" {
			sawC = true
		}
	}
	if !sawC {
		t.Fatalf("books = %+v, want Book C imported by the resumed run", books)
	}
}

// syncBuffer is a log sink safe to write from the importer goroutine while the
// test reads it.
type syncBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *syncBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *syncBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

// TestImporter_LogsWhyItemFilesWereNotAttached covers the second observation
// in #2578: with abs.path_remap = /audiobooks:/abs-audiobooks and the library
// mounted at /abs-audiobooks, no item got a book_files row, and nothing in the
// log said why. The remap applied; the remapped path is simply not under a
// Bindery root, so the item correctly imported as metadata only. The reason
// only ever reached the item's result message. Now each such item logs one
// line saying why, at INFO for a configuration choice and WARN for an in-scope
// path that cannot be read.
//
// Not parallel: it swaps the process-wide slog default.
func TestImporter_LogsWhyItemFilesWereNotAttached(t *testing.T) {
	logs := &syncBuffer{}
	prev := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(logs, &slog.HandlerOptions{Level: slog.LevelInfo})))
	t.Cleanup(func() { slog.SetDefault(prev) })

	importer, _, bookRepo, _, _, _, _, _, _, _ := newABSImporterFixture(t)
	libraryDir := t.TempDir()
	importer.WithStoragePaths(libraryDir, "", nil)

	// Distinct authors: near-identical names would let the author matcher
	// pair two items and park one in the review queue before reconciliation.
	mk := func(id, author, dir string) NormalizedLibraryItem {
		item := sampleABSItem()
		item.ItemID = id
		item.Title = "Title " + id
		item.ASIN = ""
		item.Series = nil
		item.EbookPath = ""
		item.EbookINO = ""
		item.Authors = []NormalizedAuthor{{ID: "author-" + id, Name: author}}
		item.Path = dir
		item.AudioFiles = []NormalizedAudioFile{{INO: "ino-" + id, Path: dir + "/01.mp3"}}
		return item
	}
	items := []NormalizedLibraryItem{
		// The reporter's configuration: remapped, but not onto a Bindery root.
		mk("li-remapped", "Muriel Barbery", "/audiobooks/Dear Britain"),
		// abs.path_remap is set but has no rule for this path.
		mk("li-unmapped", "Anne Weber", "/srv/elsewhere/Nein sagen"),
		// Inside Bindery storage but absent: a missing mount.
		mk("li-missing", "Arthur Conan Doyle", filepath.Join(libraryDir, "Not Mounted")),
	}
	importer.enumerateFn = func(ctx context.Context, _ string, fn func(context.Context, NormalizedLibraryItem) error) (EnumerationStats, error) {
		for _, item := range items {
			if err := fn(ctx, item); err != nil {
				return EnumerationStats{}, err
			}
		}
		return EnumerationStats{PagesScanned: 1, ItemsSeen: len(items), ItemsNormalized: len(items)}, nil
	}
	if _, err := importer.Run(context.Background(), ImportConfig{
		SourceID:  DefaultSourceID,
		BaseURL:   "https://abs.example.com",
		APIKey:    "secret",
		LibraryID: "lib-books",
		PathRemap: "/audiobooks:/abs-audiobooks",
		Label:     "Shelf",
		Enabled:   true,
	}); err != nil {
		t.Fatalf("Run: %v", err)
	}

	const msg = `msg="abs import: item files not attached, imported metadata only"`
	cases := []struct {
		itemID string
		level  string
		reason string
	}{
		{"li-remapped", "INFO", `remapped to \"/abs-audiobooks/Dear Britain\" but is still outside Bindery storage`},
		{"li-unmapped", "INFO", `is outside Bindery storage (no abs.path_remap rule matched it)`},
		{"li-missing", "WARN", `is not visible to Bindery`},
	}
	out := logs.String()
	for _, tc := range cases {
		var lines []string
		for _, line := range strings.Split(out, "\n") {
			if strings.Contains(line, msg) && strings.Contains(line, "itemID="+tc.itemID+" ") {
				lines = append(lines, line)
			}
		}
		if len(lines) != 1 {
			t.Fatalf("%s: %d skip log lines, want exactly one per item; log:\n%s", tc.itemID, len(lines), out)
		}
		line := lines[0]
		if !strings.Contains(line, "level="+tc.level) {
			t.Errorf("%s: want level %s, got %s", tc.itemID, tc.level, line)
		}
		if !strings.Contains(line, tc.reason) {
			t.Errorf("%s: want reason containing %q, got %s", tc.itemID, tc.reason, line)
		}
		if !strings.Contains(line, "audiobookRoots=") || !strings.Contains(line, libraryDir) {
			t.Errorf("%s: want the accepted audiobook roots (%s) in the line, got %s", tc.itemID, libraryDir, line)
		}
		if !strings.Contains(line, "pathRemap=/audiobooks:/abs-audiobooks") {
			t.Errorf("%s: want the configured remap in the line, got %s", tc.itemID, line)
		}
	}

	books, err := bookRepo.List(context.Background())
	if err != nil {
		t.Fatalf("list books: %v", err)
	}
	for _, b := range books {
		files, err := bookRepo.ListFiles(context.Background(), b.ID)
		if err != nil {
			t.Fatalf("list files: %v", err)
		}
		if len(files) != 0 {
			t.Fatalf("book %q has files %+v; every path in this test is out of scope or missing", b.Title, files)
		}
	}
}

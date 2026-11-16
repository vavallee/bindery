package abs

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"sort"
	"testing"

	"github.com/vavallee/bindery/internal/models"
)

// The pieces below let a second source (BookOrbit, #1927) run through this
// importer. Each test pins one of them.

func itemsEnumerator(items ...NormalizedLibraryItem) EnumerateFunc {
	return func(ctx context.Context, _ string, fn func(context.Context, NormalizedLibraryItem) error) (EnumerationStats, error) {
		for _, item := range items {
			if err := fn(ctx, item); err != nil {
				return EnumerationStats{}, err
			}
		}
		return EnumerationStats{ItemsSeen: len(items), ItemsNormalized: len(items)}, nil
	}
}

func otherSourceConfig(dryRun bool) ImportConfig {
	return ImportConfig{SourceID: "othersource", BaseURL: "http://other.invalid", APIKey: "k", LibraryIDs: []string{"othersource-1"}, Label: "Other", Enabled: true, DryRun: dryRun}
}

// Two importers sharing a gate never run side by side: each matches against an
// author list loaded once, so both would create the same new author.
func TestRunGateKeepsImportersApart(t *testing.T) {
	first, _, _, _, _, _, _, _, _, _ := newABSImporterFixture(t)
	second, _, _, _, _, _, _, _, _, _ := newABSImporterFixture(t)
	gate := NewRunGate()
	first.WithRunGate(gate)
	second.WithRunGate(gate)

	entered := make(chan struct{})
	release := make(chan struct{})
	first.WithEnumerator(func(context.Context, string, func(context.Context, NormalizedLibraryItem) error) (EnumerationStats, error) {
		close(entered)
		<-release
		return EnumerationStats{}, nil
	})
	second.WithEnumerator(itemsEnumerator())

	done := make(chan error, 1)
	go func() {
		_, err := first.Run(context.Background(), otherSourceConfig(true))
		done <- err
	}()
	<-entered

	_, err := second.Run(context.Background(), otherSourceConfig(true))
	if !errors.Is(err, ErrOtherImportRunning) {
		t.Fatalf("second importer started while the first held the gate: err = %v", err)
	}
	// Callers that only know the older sentinel still read it as busy.
	if !errors.Is(err, ErrAlreadyRunning) {
		t.Errorf("ErrOtherImportRunning does not wrap ErrAlreadyRunning")
	}
	if second.Running() {
		t.Error("the refused importer is left marked as running")
	}

	close(release)
	if err := <-done; err != nil {
		t.Fatalf("first run: %v", err)
	}
	// The gate is free again once the first run ends.
	if _, err := second.Run(context.Background(), otherSourceConfig(true)); err != nil {
		t.Fatalf("second importer after the first finished: %v", err)
	}
}

// Without a gate nothing changes for an importer on its own.
func TestNilRunGateAllowsRun(t *testing.T) {
	importer, _, _, _, _, _, _, _, _, _ := newABSImporterFixture(t)
	importer.WithEnumerator(itemsEnumerator())
	if _, err := importer.Run(context.Background(), otherSourceConfig(true)); err != nil {
		t.Fatalf("Run: %v", err)
	}
}

// A dry run writes no author, so every further book by the same new author
// used to count that author as created again: 3 books, "3 authors created".
func TestDryRunCountsANewAuthorOnce(t *testing.T) {
	importer, _, _, _, _, _, _, _, _, _ := newABSImporterFixture(t)
	mk := func(id, title string) NormalizedLibraryItem {
		return NormalizedLibraryItem{ItemID: id, LibraryID: "othersource-1", Title: title, EbookPath: "/x/" + id + ".epub",
			Authors: []NormalizedAuthor{{ID: "a1", Name: "Repeated Author"}}}
	}
	importer.WithEnumerator(itemsEnumerator(mk("1", "One"), mk("2", "Two"), mk("3", "Three")))
	stats, err := importer.Run(context.Background(), otherSourceConfig(true))
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if stats.AuthorsCreated != 1 || stats.AuthorsLinked != 2 || stats.BooksCreated != 3 {
		t.Fatalf("authorsCreated = %d, authorsLinked = %d, booksCreated = %d; want 1, 2, 3", stats.AuthorsCreated, stats.AuthorsLinked, stats.BooksCreated)
	}
}

// ExtraEbookPaths: a second ebook format of the same book is registered too,
// and a missing one is counted without blocking the first.
func TestExtraEbookPathsAreRegistered(t *testing.T) {
	importer, _, bookRepo, _, _, _, _, _, _, _ := newABSImporterFixture(t)
	lib := t.TempDir()
	importer.WithStoragePaths(lib, "", nil)
	write := func(name string) string {
		p := filepath.Join(lib, "Author", "Book", name)
		if err := os.MkdirAll(filepath.Dir(p), 0o750); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte("x"), 0o600); err != nil {
			t.Fatal(err)
		}
		return p
	}
	epub, azw3 := write("Book.epub"), write("Book.azw3")
	missing := filepath.Join(lib, "Author", "Book", "Book.mobi")
	importer.WithEnumerator(itemsEnumerator(NormalizedLibraryItem{
		ItemID: "1", LibraryID: "othersource-1", Title: "Book", EbookPath: epub,
		ExtraEbookPaths: []string{azw3, missing, epub},
		Authors:         []NormalizedAuthor{{ID: "a1", Name: "Some Author"}},
	}))
	stats, err := importer.Run(context.Background(), otherSourceConfig(false))
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if stats.OwnedMarked != 2 || stats.PendingManual != 1 {
		t.Fatalf("ownedMarked = %d, pendingManual = %d; want 2 and 1", stats.OwnedMarked, stats.PendingManual)
	}
	books, _ := bookRepo.List(context.Background())
	if len(books) != 1 {
		t.Fatalf("books = %d", len(books))
	}
	files, _ := bookRepo.ListFiles(context.Background(), books[0].ID)
	var got []string
	for _, f := range files {
		if f.Format != models.MediaTypeEbook {
			t.Errorf("file %q registered as %q", f.Path, f.Format)
		}
		got = append(got, f.Path)
	}
	sort.Strings(got)
	if len(got) != 2 || got[0] != azw3 || got[1] != epub {
		t.Fatalf("registered files = %v, want the epub and the azw3", got)
	}
}

// WithRunSource scopes the history to one source's runs.
func TestRunsPageScopedToSource(t *testing.T) {
	importer, _, _, _, _, _, runRepo, _, _, _ := newABSImporterFixture(t)
	ctx := context.Background()
	for _, source := range []string{DefaultSourceID, "othersource", DefaultSourceID} {
		if err := runRepo.Create(ctx, &models.ABSImportRun{SourceID: source, LibraryID: "l", Status: runStatusCompleted, SourceConfigJSON: "{}", CheckpointJSON: "{}", SummaryJSON: "{}"}); err != nil {
			t.Fatal(err)
		}
	}
	all, err := importer.RunsPage(ctx, 10, 0)
	if err != nil || len(all) != 3 {
		t.Fatalf("unscoped RunsPage = %d rows, %v; want 3", len(all), err)
	}
	importer.WithRunSource(DefaultSourceID)
	own, err := importer.RunsPage(ctx, 10, 0)
	if err != nil || len(own) != 2 {
		t.Fatalf("scoped RunsPage = %d rows, %v; want 2", len(own), err)
	}
	for _, run := range own {
		if run.SourceID != DefaultSourceID {
			t.Errorf("scoped history lists a %q run", run.SourceID)
		}
	}
}

func TestImportStartMessageNamesTheSource(t *testing.T) {
	if got := importStartMessage(ImportConfig{SourceID: DefaultSourceID, DryRun: true}); got != "starting ABS dry-run…" {
		t.Errorf("abs message changed: %q", got)
	}
	if got := importStartMessage(ImportConfig{}); got != "starting ABS import…" {
		t.Errorf("abs message changed: %q", got)
	}
	if got := importStartMessage(otherSourceConfig(false)); got != "starting Other import…" {
		t.Errorf("other source message = %q", got)
	}
}

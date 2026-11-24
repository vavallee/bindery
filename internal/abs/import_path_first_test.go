package abs

import (
	"context"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/vavallee/bindery/internal/metadata"
	"github.com/vavallee/bindery/internal/models"
)

// TestImporter_PathConflictDoesNotCreateDuplicateBook is #4077 point 1. The
// file BookOrbit reports is already tracked on a Bindery book whose author
// is spelled differently ("Olivia Ash" against "Olivia Chase"), so the
// author match misses, and the importer used to create a new author and a
// new wanted book that could never take the file — it stayed wanted beside
// the owned original. The path is the strongest match key: the item must
// not mint a duplicate. The author spelling is a human decision, so the
// item goes to the review queue instead, naming no new author or book.
func TestImporter_PathConflictDoesNotCreateDuplicateBook(t *testing.T) {
	t.Parallel()
	for _, format := range []string{models.MediaTypeEbook, models.MediaTypeAudiobook} {
		t.Run(format, func(t *testing.T) {
			t.Parallel()
			ctx := context.Background()
			f := newFileIdentityFixture(t, format, "Olivia Chase", "Olivia Ash", noOwner, noOwner)

			runSingleABSImport(t, f.env.importer, f.item)

			books, err := f.env.books.ListIncludingExcluded(ctx)
			if err != nil {
				t.Fatal(err)
			}
			if len(books) != 1 {
				for _, b := range books {
					t.Logf("book %d %q author %d status=%s", b.ID, b.Title, b.AuthorID, b.Status)
				}
				t.Fatalf("books = %d, want 1: the item must not create a duplicate beside the book tracking its file", len(books))
			}
			if got := books[0]; got.ID != f.owned.ID || got.Title != "Chapterhouse: Dune" || got.AuthorID != f.authorID {
				t.Errorf("book = id %d %q author %d, want the owned book kept untouched (author %d)", got.ID, got.Title, got.AuthorID, f.authorID)
			}
			authors, err := f.env.authors.List(ctx)
			if err != nil {
				t.Fatal(err)
			}
			if len(authors) != 1 {
				t.Errorf("authors = %d, want 1: no stray author for the item's spelling", len(authors))
			}
			links, err := f.env.provenance.ListByLocal(ctx, entityTypeBook, f.owned.ID)
			if err != nil {
				t.Fatal(err)
			}
			if len(links) != 0 {
				t.Errorf("provenance = %+v, want the item not linked to another author's book", links)
			}
			pending, err := f.env.reviews.ListByStatus(ctx, "pending")
			if err != nil {
				t.Fatal(err)
			}
			if len(pending) != 1 || pending[0].ItemID != f.item.ItemID || pending[0].ReviewReason != reviewReasonAmbiguousBook {
				t.Errorf("pending reviews = %+v, want the item queued as an ambiguous book match", pending)
			}
		})
	}
}

// duplicateCopiesFixture seeds one author with two books of the same title;
// only the second tracks the item's file.
type duplicateCopiesFixture struct {
	env    *covImpEnv
	first  *models.Book
	second *models.Book
	item   NormalizedLibraryItem
}

func newDuplicateCopiesFixture(t *testing.T, format string) duplicateCopiesFixture {
	t.Helper()
	ctx := context.Background()
	env := covImpNewEnv(t)
	storageRoot := t.TempDir()
	libraryDir := filepath.Join(storageRoot, "books")
	audiobookDir := filepath.Join(storageRoot, "audiobooks")
	env.importer.WithStoragePaths(libraryDir, audiobookDir, nil)

	author := &models.Author{ForeignID: "OL79034A", Name: "Rebecca Shea", SortName: "Rebecca Shea", MetadataProvider: "openlibrary", Monitored: true}
	if err := env.authors.Create(ctx, author); err != nil {
		t.Fatal(err)
	}
	mkBook := func(foreignID string) *models.Book {
		book := &models.Book{
			ForeignID: foreignID, AuthorID: author.ID,
			Title: "The Copper Gauntlet", SortTitle: "The Copper Gauntlet",
			Status: models.BookStatusWanted, Monitored: true, AnyEditionOK: true, MediaType: format, MetadataProvider: "openlibrary",
		}
		if err := env.books.Create(ctx, book); err != nil {
			t.Fatal(err)
		}
		return book
	}
	first := mkBook("OL893415W")
	second := mkBook("OL893416W")

	item := sampleABSItem()
	item.ItemID = "li-copper-gauntlet"
	item.Title = "The Copper Gauntlet"
	item.ASIN = ""
	item.Series = nil
	item.Authors = []NormalizedAuthor{{ID: "author-shea", Name: "Rebecca Shea"}}
	var trackedPath string
	if format == models.MediaTypeEbook {
		trackedPath = filepath.Join(libraryDir, "Rebecca Shea", "The Copper Gauntlet.epub")
		mustWriteFile(t, trackedPath)
		item.EbookPath = trackedPath
		item.Path = ""
		item.AudioFiles = nil
	} else {
		trackedPath = filepath.Join(audiobookDir, "Rebecca Shea", "The Copper Gauntlet")
		mustWriteFile(t, filepath.Join(trackedPath, "part1.m4b"))
		item.EbookPath = ""
		item.Path = trackedPath
		item.AudioFiles = []NormalizedAudioFile{{INO: "audio-1", Path: filepath.Join(trackedPath, "part1.m4b")}}
	}
	if err := env.books.SetFormatFilePath(ctx, second.ID, format, trackedPath); err != nil {
		t.Fatalf("track file: %v", err)
	}
	return duplicateCopiesFixture{env: env, first: first, second: second, item: item}
}

// TestImporter_PathConflictPrefersPathHolderAmongDuplicates is #4077 point 2
// with one author spelling for both copies. Bindery holds two copies of one
// book; the file sits on the second. The file-path rung already links the
// copy holding the path here, so this is the control case: it pins the
// working behaviour the fix must keep, while the variant-spelling test
// below covers the shape that actually failed for the reporter.
func TestImporter_PathConflictPrefersPathHolderAmongDuplicates(t *testing.T) {
	t.Parallel()
	for _, format := range []string{models.MediaTypeEbook, models.MediaTypeAudiobook} {
		t.Run(format, func(t *testing.T) {
			t.Parallel()
			ctx := context.Background()
			f := newDuplicateCopiesFixture(t, format)

			runSingleABSImport(t, f.env.importer, f.item)

			books, err := f.env.books.ListIncludingExcluded(ctx)
			if err != nil {
				t.Fatal(err)
			}
			if len(books) != 2 {
				t.Fatalf("books = %d, want 2: no new book beside the duplicates", len(books))
			}
			links, err := f.env.provenance.ListByLocal(ctx, entityTypeBook, f.second.ID)
			if err != nil {
				t.Fatal(err)
			}
			if len(links) != 1 || links[0].ExternalID != f.item.ItemID {
				t.Errorf("provenance on second copy = %+v, want the item linked to the copy tracking its file", links)
			}
			firstLinks, err := f.env.provenance.ListByLocal(ctx, entityTypeBook, f.first.ID)
			if err != nil {
				t.Fatal(err)
			}
			if len(firstLinks) != 0 {
				t.Errorf("provenance on first copy = %+v, want the item not linked to the copy without the file", firstLinks)
			}
			got, err := f.env.books.GetByID(ctx, f.second.ID)
			if err != nil || got == nil {
				t.Fatalf("reload second: %v", err)
			}
			if got.Status != models.BookStatusImported {
				t.Errorf("second copy status = %s, want imported", got.Status)
			}
		})
	}
}

// TestImporter_PathConflictDuplicateCopiesUnderDifferentAuthorSpellings is
// #4077 point 2 as the reporter actually hit it: the two copies sit under
// different spellings of the author ("Shelby Rebecca" against "Rebecca
// Shea"), the item credits the first spelling, and the file sits on the
// second copy. The importer used to link the first copy by title and then
// fail to attach the file. Which copy the item belongs to is a human call,
// so it must be queued, not linked to the copy without the file.
func TestImporter_PathConflictDuplicateCopiesUnderDifferentAuthorSpellings(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	env := covImpNewEnv(t)
	storageRoot := t.TempDir()
	libraryDir := filepath.Join(storageRoot, "books")
	env.importer.WithStoragePaths(libraryDir, filepath.Join(storageRoot, "audiobooks"), nil)

	mkAuthor := func(name, foreignID string) *models.Author {
		author := &models.Author{ForeignID: foreignID, Name: name, SortName: name, MetadataProvider: "openlibrary", Monitored: true}
		if err := env.authors.Create(ctx, author); err != nil {
			t.Fatal(err)
		}
		return author
	}
	firstAuthor := mkAuthor("Shelby Rebecca", "OL79034A")
	secondAuthor := mkAuthor("Rebecca Shea", "OL79035A")
	mkBook := func(author *models.Author, foreignID string) *models.Book {
		book := &models.Book{
			ForeignID: foreignID, AuthorID: author.ID,
			Title: "The Copper Gauntlet", SortTitle: "The Copper Gauntlet",
			Status: models.BookStatusWanted, Monitored: true, AnyEditionOK: true, MediaType: models.MediaTypeEbook, MetadataProvider: "openlibrary",
		}
		if err := env.books.Create(ctx, book); err != nil {
			t.Fatal(err)
		}
		return book
	}
	first := mkBook(firstAuthor, "OL893415W")
	second := mkBook(secondAuthor, "OL893416W")

	trackedPath := filepath.Join(libraryDir, "Rebecca Shea", "The Copper Gauntlet.epub")
	mustWriteFile(t, trackedPath)
	if err := env.books.SetFormatFilePath(ctx, second.ID, models.MediaTypeEbook, trackedPath); err != nil {
		t.Fatalf("track file: %v", err)
	}

	item := sampleABSItem()
	item.ItemID = "li-copper-gauntlet-variant"
	item.Title = "The Copper Gauntlet"
	item.ASIN = ""
	item.Series = nil
	item.Authors = []NormalizedAuthor{{ID: "author-shelby", Name: "Shelby Rebecca"}}
	item.EbookPath = trackedPath
	item.Path = ""
	item.AudioFiles = nil

	runSingleABSImport(t, env.importer, item)

	books, err := env.books.ListIncludingExcluded(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(books) != 2 {
		t.Fatalf("books = %d, want 2: no new book beside the duplicates", len(books))
	}
	for _, b := range []*models.Book{first, second} {
		links, err := env.provenance.ListByLocal(ctx, entityTypeBook, b.ID)
		if err != nil {
			t.Fatal(err)
		}
		if len(links) != 0 {
			t.Errorf("provenance on book %d = %+v, want the item linked to neither copy", b.ID, links)
		}
	}
	authors, err := env.authors.List(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(authors) != 2 {
		t.Errorf("authors = %d, want 2: no stray author created", len(authors))
	}
	pending, err := env.reviews.ListByStatus(ctx, "pending")
	if err != nil {
		t.Fatal(err)
	}
	if len(pending) != 1 || pending[0].ItemID != item.ItemID || pending[0].ReviewReason != reviewReasonAmbiguousBook {
		t.Errorf("pending reviews = %+v, want the item queued as an ambiguous book match", pending)
	}
}

// TestRollbackPreview_IgnoresFailedItemEntities: the failed row recorded
// for #4077 point 3 exists only so the run lists what failed. It wrote
// nothing, so a rollback preview must not list it as "already rolled back".
func TestRollbackPreview_IgnoresFailedItemEntities(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	env := covImpNewEnv(t)
	item := sampleABSItem()
	item.Authors = nil

	runID := runSingleABSImport(t, env.importer, item)

	result, err := env.importer.RollbackPreview(ctx, runID)
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Actions) != 0 {
		t.Fatalf("rollback actions = %+v, want none for a run whose only entity is a failed item", result.Actions)
	}
}

// TestImporter_ForeignIDAuthorSkipsPathConflict: the same ABS author id
// behind two spellings. The book's author was created from an earlier item
// crediting "Olivia Chase" with ABS author id "author-olivia", so it
// carries the stable foreign id for that author. This item credits
// "Olivia Ash" with the same id: the names do not auto-match and no alias
// exists, but resolveAuthor's foreign-id tier returns the book's author
// with no name check, and the file-path rung links the book. The pre-check
// must agree with that tier instead of queueing on the names.
func TestImporter_ForeignIDAuthorSkipsPathConflict(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	env := covImpNewEnv(t)
	storageRoot := t.TempDir()
	libraryDir := filepath.Join(storageRoot, "books")
	audiobookDir := filepath.Join(storageRoot, "audiobooks")
	env.importer.WithStoragePaths(libraryDir, audiobookDir, nil)

	author := &models.Author{
		ForeignID: absForeignID("author", "lib-books", "author-olivia"),
		Name:      "Olivia Chase", SortName: "Olivia Chase",
		MetadataProvider: "openlibrary", Monitored: true,
	}
	if err := env.authors.Create(ctx, author); err != nil {
		t.Fatal(err)
	}
	book := &models.Book{
		ForeignID: "OL893415W", AuthorID: author.ID,
		Title: "The Vanishing", SortTitle: "The Vanishing",
		Status: models.BookStatusWanted, Monitored: true, AnyEditionOK: true, MediaType: models.MediaTypeEbook, MetadataProvider: "openlibrary",
	}
	if err := env.books.Create(ctx, book); err != nil {
		t.Fatal(err)
	}
	ebookPath := filepath.Join(libraryDir, "Olivia Chase", "The Vanishing.epub")
	mustWriteFile(t, ebookPath)
	if err := env.books.SetFormatFilePath(ctx, book.ID, models.MediaTypeEbook, ebookPath); err != nil {
		t.Fatalf("track ebook: %v", err)
	}

	item := sampleABSItem()
	item.ItemID = "li-foreign-id-author"
	item.Title = "The Vanishing"
	item.ASIN = ""
	item.Series = nil
	item.Authors = []NormalizedAuthor{{ID: "author-olivia", Name: "Olivia Ash"}}
	item.EbookPath = ebookPath
	item.Path = ""
	item.AudioFiles = nil

	runSingleABSImport(t, env.importer, item)

	books, err := env.books.ListIncludingExcluded(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(books) != 1 {
		t.Fatalf("books = %d, want 1", len(books))
	}
	if got := books[0]; got.ID != book.ID || got.Status != models.BookStatusImported {
		t.Errorf("book = id %d status %s, want id %d imported", got.ID, got.Status, book.ID)
	}
	authors, err := env.authors.List(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(authors) != 1 {
		t.Errorf("authors = %d, want 1: the foreign-id tier must reuse the book's author, not mint a spelling", len(authors))
	}
	pending, err := env.reviews.ListByStatus(ctx, "pending")
	if err != nil {
		t.Fatal(err)
	}
	if len(pending) != 0 {
		t.Errorf("pending reviews = %d, want 0: the same author id must link, not queue", len(pending))
	}
}

// TestImporter_ManualResolutionSkipsPathConflict is the escape hatch for
// the review queue #4077 point 1 creates: once the user resolves the item's
// author to the book's author, approving it must import the book, not
// re-queue the item forever. The pre-check skips items carrying a manual
// resolution, and the manual author path owns the decision.
func TestImporter_ManualResolutionSkipsPathConflict(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	f := newFileIdentityFixture(t, models.MediaTypeEbook, "Olivia Chase", "Olivia Ash", noOwner, noOwner)
	f.item.ResolvedAuthorForeignID = "OL79034A"
	f.item.ResolvedAuthorName = "Olivia Chase"

	result, err := f.env.importer.ImportReview(ctx, covImpConfig(f.item.LibraryID), f.item)
	if err != nil {
		t.Fatalf("ImportReview: %v", err)
	}
	if result.Outcome != itemOutcomeLinked {
		t.Errorf("outcome = %s, want linked: a manually resolved item must import, not re-queue", result.Outcome)
	}
	books, err := f.env.books.ListIncludingExcluded(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(books) != 1 {
		t.Fatalf("books = %d, want 1", len(books))
	}
	if got := books[0]; got.ID != f.owned.ID || got.Status != models.BookStatusImported {
		t.Errorf("book = id %d status %s, want id %d imported", got.ID, got.Status, f.owned.ID)
	}
	pending, err := f.env.reviews.ListByStatus(ctx, "pending")
	if err != nil {
		t.Fatal(err)
	}
	if len(pending) != 0 {
		t.Errorf("pending reviews = %d, want 0: the resolved item must not come back to the queue", len(pending))
	}
}

// TestImporter_ManualBookResolutionSkipsPathConflict is the book half of
// the manual-resolution escape hatch: the user resolves the queued item to
// the book itself, and approving it must import the book, not re-queue it.
// (The manual book path keeps the author resolveAuthor returns, so the
// book takes the item's author here — that reassignment is the manual
// path's existing behavior; the point of this test is the item imports
// instead of looping back to the queue.)
func TestImporter_ManualBookResolutionSkipsPathConflict(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	f := newFileIdentityFixture(t, models.MediaTypeEbook, "Olivia Chase", "Olivia Ash", noOwner, noOwner)
	f.item.ResolvedBookForeignID = "OL893415W"
	f.item.ResolvedBookTitle = "Chapterhouse: Dune"

	result, err := f.env.importer.ImportReview(ctx, covImpConfig(f.item.LibraryID), f.item)
	if err != nil {
		t.Fatalf("ImportReview: %v", err)
	}
	if result.Outcome != itemOutcomeLinked {
		t.Errorf("outcome = %s, want linked: a manually resolved item must import, not re-queue", result.Outcome)
	}
	books, err := f.env.books.ListIncludingExcluded(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(books) != 1 {
		t.Fatalf("books = %d, want 1", len(books))
	}
	if got := books[0]; got.ID != f.owned.ID || got.Status != models.BookStatusImported {
		t.Errorf("book = id %d status %s, want id %d imported", got.ID, got.Status, f.owned.ID)
	}
	pending, err := f.env.reviews.ListByStatus(ctx, "pending")
	if err != nil {
		t.Fatal(err)
	}
	if len(pending) != 0 {
		t.Errorf("pending reviews = %d, want 0: the resolved item must not come back to the queue", len(pending))
	}
}

// blockingBookProvider wraps the ABS stub provider so one ISBN's lookup
// never returns: enrichBook parks in it after upsertBook has already
// recorded the linked row, so the item timeout fires after the link.
type blockingBookProvider struct {
	*stubABSMetadataProvider
	blockISBN string
}

func (p *blockingBookProvider) GetBookByISBN(ctx context.Context, isbn string) (*models.Book, error) {
	if isbn == p.blockISBN {
		<-ctx.Done()
		return nil, ctx.Err()
	}
	return p.stubABSMetadataProvider.GetBookByISBN(ctx, isbn)
}

// TestImporter_TimeoutAfterLinkKeepsLinkedRow is the timeout-after-link
// case #4077 point 3 must survive: the item links its book, then the
// upstream lookup in enrichBook stalls and the item times out. The run
// must list both the link and the failure — the failed row with local id
// 0, never overwriting the linked row — and rollback must still be able
// to restore the linked book from its before-snapshot.
func TestImporter_TimeoutAfterLinkKeepsLinkedRow(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	f := newFileIdentityFixture(t, models.MediaTypeEbook, "Olivia Chase", "Olivia Chase", noOwner, noOwner)
	f.item.ISBN = "9780441172719"
	f.env.importer.WithMetadata(metadata.NewAggregator(&blockingBookProvider{stubABSMetadataProvider: &stubABSMetadataProvider{}, blockISBN: "9780441172719"}))
	f.env.importer.itemTimeout = 300 * time.Millisecond

	runID := runSingleABSImport(t, f.env.importer, f.item)

	results := f.env.importer.Progress().Results
	if len(results) != 1 || results[0].Outcome != itemOutcomeFailed || !strings.Contains(results[0].Message, "timed out after") {
		t.Fatalf("results = %+v, want one timed-out failure", results)
	}
	entities, err := f.env.entities.ListByRun(ctx, runID)
	if err != nil {
		t.Fatal(err)
	}
	var gotLinked, gotFailed bool
	for _, e := range entities {
		if e.EntityType == entityTypeBook && e.Outcome == itemOutcomeLinked && e.LocalID == f.owned.ID {
			gotLinked = true
		}
		if e.EntityType == entityTypeBook && e.Outcome == itemOutcomeFailed && e.LocalID == 0 {
			gotFailed = true
		}
	}
	if !gotLinked || !gotFailed {
		t.Fatalf("entities = %+v, want the linked row (local %d) and the failed row (local 0) side by side", entities, f.owned.ID)
	}
	preview, err := f.env.importer.RollbackPreview(ctx, runID)
	if err != nil {
		t.Fatal(err)
	}
	restorable := false
	for _, a := range preview.Actions {
		if a.EntityType == entityTypeBook && a.LocalID == f.owned.ID {
			restorable = true
		}
	}
	if !restorable {
		t.Fatalf("rollback actions = %+v, want the linked book's restore still listed", preview.Actions)
	}
}

// TestImporter_TrustedAliasSkipsPathConflict: a relink alias ("Olivia Ash"
// for "Olivia Chase", recorded with a provider source) is trusted
// unconditionally, so the author matcher resolves the item's author to the
// book's author and the file-path rung links the book. The pre-check must
// agree with that decision instead of queuing on the raw names.
func TestImporter_TrustedAliasSkipsPathConflict(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	f := newFileIdentityFixture(t, models.MediaTypeEbook, "Olivia Chase", "Olivia Ash", noOwner, noOwner)
	if err := f.env.aliases.Create(ctx, &models.AuthorAlias{AuthorID: f.authorID, Name: "Olivia Ash", SourceOLID: "OL79034A"}); err != nil {
		t.Fatalf("create alias: %v", err)
	}

	runSingleABSImport(t, f.env.importer, f.item)

	books, err := f.env.books.ListIncludingExcluded(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(books) != 1 {
		t.Fatalf("books = %d, want 1", len(books))
	}
	if got := books[0]; got.ID != f.owned.ID || got.Status != models.BookStatusImported {
		t.Errorf("book = id %d status %s, want id %d imported", got.ID, got.Status, f.owned.ID)
	}
	pending, err := f.env.reviews.ListByStatus(ctx, "pending")
	if err != nil {
		t.Fatal(err)
	}
	if len(pending) != 0 {
		t.Errorf("pending reviews = %d, want 0: the alias-resolved item must link, not queue", len(pending))
	}
}

// TestImporter_PathConflictSplitCandidatesLinkMatchingCopy: one item whose
// ebook sits on Brian Herbert's copy and whose audiobook folder sits on
// Frank Herbert's copy, crediting Frank. The file-path rung links Frank's
// copy; the conflict check must agree with it — it keeps scanning past the
// non-matching ebook candidate instead of queuing on it.
func TestImporter_PathConflictSplitCandidatesLinkMatchingCopy(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	env := covImpNewEnv(t)
	storageRoot := t.TempDir()
	libraryDir := filepath.Join(storageRoot, "books")
	audiobookDir := filepath.Join(storageRoot, "audiobooks")
	env.importer.WithStoragePaths(libraryDir, audiobookDir, nil)

	mkAuthor := func(name, foreignID string) *models.Author {
		author := &models.Author{ForeignID: foreignID, Name: name, SortName: name, MetadataProvider: "openlibrary", Monitored: true}
		if err := env.authors.Create(ctx, author); err != nil {
			t.Fatal(err)
		}
		return author
	}
	frank := mkAuthor("Frank Herbert", "OL79034A")
	brian := mkAuthor("Brian Herbert", "OL79035A")
	mkBook := func(author *models.Author, foreignID string) *models.Book {
		book := &models.Book{
			ForeignID: foreignID, AuthorID: author.ID,
			Title: "Chapterhouse: Dune", SortTitle: "Chapterhouse: Dune",
			Status: models.BookStatusWanted, Monitored: true, AnyEditionOK: true, MediaType: models.MediaTypeBoth, MetadataProvider: "openlibrary",
		}
		if err := env.books.Create(ctx, book); err != nil {
			t.Fatal(err)
		}
		return book
	}
	frankBook := mkBook(frank, "OL893415W")
	brianBook := mkBook(brian, "OL893416W")

	ebookPath := filepath.Join(libraryDir, "Brian Herbert", "Chapterhouse Dune.epub")
	mustWriteFile(t, ebookPath)
	if err := env.books.SetFormatFilePath(ctx, brianBook.ID, models.MediaTypeEbook, ebookPath); err != nil {
		t.Fatalf("track ebook: %v", err)
	}
	audioFolder := filepath.Join(audiobookDir, "Frank Herbert", "Chapterhouse Dune")
	mustWriteFile(t, filepath.Join(audioFolder, "part1.m4b"))
	if err := env.books.SetFormatFilePath(ctx, frankBook.ID, models.MediaTypeAudiobook, audioFolder); err != nil {
		t.Fatalf("track audiobook: %v", err)
	}

	item := sampleABSItem()
	item.ItemID = "li-split-candidates"
	item.Title = "Chapter House Dune"
	item.ASIN = ""
	item.Series = nil
	item.Authors = []NormalizedAuthor{{ID: "author-herbert", Name: "Frank Herbert"}}
	item.EbookPath = ebookPath
	item.Path = audioFolder
	item.AudioFiles = []NormalizedAudioFile{{INO: "audio-1", Path: filepath.Join(audioFolder, "part1.m4b")}}

	runSingleABSImport(t, env.importer, item)

	books, err := env.books.ListIncludingExcluded(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(books) != 2 {
		t.Fatalf("books = %d, want 2", len(books))
	}
	links, err := env.provenance.ListByLocal(ctx, entityTypeBook, frankBook.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(links) != 1 || links[0].ExternalID != item.ItemID {
		t.Errorf("provenance on Frank's book = %+v, want the item linked to the copy whose author matches", links)
	}
	brianLinks, err := env.provenance.ListByLocal(ctx, entityTypeBook, brianBook.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(brianLinks) != 0 {
		t.Errorf("provenance on Brian's book = %+v, want the item not linked there", brianLinks)
	}
	pending, err := env.reviews.ListByStatus(ctx, "pending")
	if err != nil {
		t.Fatal(err)
	}
	if len(pending) != 0 {
		t.Errorf("pending reviews = %d, want 0: a matching candidate must win over a conflicting one", len(pending))
	}
}

// TestImporter_RunEntityMergeKeepsFailedRowSeparate pins the merge
// invariant the failed-row recording relies on: two rows for one item
// under different local ids coexist, so a failed row (local id 0) can
// never overwrite the outcome of a success row for the same item. It
// does not execute the callback that chooses local id 0 — that is
// TestImporter_TimeoutAfterLinkKeepsLinkedRow's job.
func TestImporter_RunEntityMergeKeepsFailedRowSeparate(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	env := covImpNewEnv(t)
	run := &models.ABSImportRun{SourceID: DefaultSourceID, LibraryID: "lib-books", Status: "completed"}
	if err := env.runs.Create(ctx, run); err != nil {
		t.Fatalf("create run: %v", err)
	}
	linked := &models.ABSImportRunEntity{RunID: run.ID, SourceID: DefaultSourceID, LibraryID: "lib-books", ItemID: "li-timeout", EntityType: entityTypeBook, ExternalID: "li-timeout", LocalID: 42, Outcome: itemOutcomeLinked, MetadataJSON: "{}"}
	if err := env.entities.Record(ctx, linked); err != nil {
		t.Fatalf("record linked: %v", err)
	}
	failed := &models.ABSImportRunEntity{RunID: run.ID, SourceID: DefaultSourceID, LibraryID: "lib-books", ItemID: "li-timeout", EntityType: entityTypeBook, ExternalID: "li-timeout", LocalID: 0, Outcome: itemOutcomeFailed, MetadataJSON: `{"message":"timed out"}`}
	if err := env.entities.Record(ctx, failed); err != nil {
		t.Fatalf("record failed: %v", err)
	}
	entities, err := env.entities.ListByRun(ctx, run.ID)
	if err != nil {
		t.Fatal(err)
	}
	var gotLinked, gotFailed bool
	for _, e := range entities {
		if e.Outcome == itemOutcomeLinked && e.LocalID == 42 {
			gotLinked = true
		}
		if e.Outcome == itemOutcomeFailed && e.LocalID == 0 {
			gotFailed = true
		}
	}
	if !gotLinked || !gotFailed {
		t.Fatalf("entities = %+v, want the linked row (local 42) and the failed row (local 0) side by side", entities)
	}
}

// TestImporter_FailedItemRecordedInRunEntities is #4077 point 3. An item
// that fails leaves no row in abs_import_run_entities today, so after the
// run there is no list of what failed. The failure must be persisted on
// the run, with its message, so the run itself shows it.
func TestImporter_FailedItemRecordedInRunEntities(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	env := covImpNewEnv(t)
	item := sampleABSItem()
	item.Authors = nil

	runID := runSingleABSImport(t, env.importer, item)

	entities, err := env.entities.ListByRun(ctx, runID)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entities {
		t.Logf("entity type=%s external=%s local=%d outcome=%s metadata=%s", e.EntityType, e.ExternalID, e.LocalID, e.Outcome, e.MetadataJSON)
	}
	found := false
	for _, e := range entities {
		if e.EntityType == entityTypeBook && e.ExternalID == item.ItemID && e.Outcome == itemOutcomeFailed {
			found = true
			if e.ItemID != item.ItemID {
				t.Errorf("failed entity item_id = %q, want %q", e.ItemID, item.ItemID)
			}
			if e.MetadataJSON == "" || e.MetadataJSON == "{}" {
				t.Errorf("failed entity metadata = %q, want the failure message kept", e.MetadataJSON)
			}
		}
	}
	if !found {
		t.Fatalf("no failed book entity for item %q in run %d, want the failure persisted", item.ItemID, runID)
	}
}

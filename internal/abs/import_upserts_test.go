package abs

import (
	"context"
	"database/sql"
	"slices"
	"strings"
	"testing"

	"github.com/vavallee/bindery/internal/db"
	"github.com/vavallee/bindery/internal/metadata"
	"github.com/vavallee/bindery/internal/models"
)

// mergeRaceUpstreamID is the upstream id the ISBN lookup relinks the book to.
const mergeRaceUpstreamID = "OL-RACE-1W"

// editingISBNProvider answers the ISBN lookup and, while it handles the
// request, runs edit: a user change committed between the importer's read of
// the book and its merge write (#2926).
type editingISBNProvider struct {
	bindingStubProvider
	edit func()
}

func (p *editingISBNProvider) GetBookByISBN(ctx context.Context, isbn string) (*models.Book, error) {
	if p.edit != nil {
		p.edit()
	}
	return p.bindingStubProvider.GetBookByISBN(ctx, isbn)
}

type mergeRaceFixture struct {
	importer  *Importer
	database  *sql.DB
	books     *db.BookRepo
	conflicts *db.ABSMetadataConflictRepo
	provider  *editingISBNProvider
	item      NormalizedLibraryItem
	book      *models.Book
}

func newMergeRaceFixture(t *testing.T) *mergeRaceFixture {
	t.Helper()
	database, err := db.OpenMemory()
	if err != nil {
		t.Fatalf("open memory db: %v", err)
	}
	t.Cleanup(func() { database.Close() })
	books := db.NewBookRepo(database)
	conflicts := db.NewABSMetadataConflictRepo(database)
	importer := NewImporter(
		db.NewAuthorRepo(database),
		db.NewAuthorAliasRepo(database),
		books,
		db.NewEditionRepo(database),
		db.NewSeriesRepo(database),
		db.NewSettingsRepo(database),
		db.NewABSImportRunRepo(database),
		db.NewABSImportRunEntityRepo(database),
		db.NewABSProvenanceRepo(database),
		db.NewABSReviewItemRepo(database),
		conflicts,
	)
	author := asinTestAuthor(t, importer)
	provider := &editingISBNProvider{bindingStubProvider: bindingStubProvider{
		name: "openlibrary",
		booksByISBN: map[string]*models.Book{bookBindingISBN: {
			ForeignID:        mergeRaceUpstreamID,
			Title:            "Project Hail Mary",
			Description:      "Upstream description of the book.",
			MetadataProvider: "openlibrary",
		}},
	}}
	importer.meta = metadata.NewAggregator(provider)

	item := sampleABSItem()
	item.ISBN = bookBindingISBN
	book := &models.Book{
		ForeignID:        "abs:book:" + item.LibraryID + ":" + item.ItemID,
		AuthorID:         author.ID,
		Title:            item.Title,
		SortTitle:        item.Title,
		Status:           models.BookStatusWanted,
		Monitored:        true,
		Genres:           []string{},
		MetadataProvider: providerAudiobookshelf,
	}
	if err := books.Create(context.Background(), book); err != nil {
		t.Fatalf("create book: %v", err)
	}
	// Create records the ABS id itself now; a row from before that has no
	// identifier, which is the case the relink's own record exists for, and
	// it makes that record observable.
	if err := books.DeleteBookIdentifier(context.Background(), book.ID, book.ForeignID); err != nil {
		t.Fatalf("drop identifier: %v", err)
	}
	return &mergeRaceFixture{importer: importer, database: database, books: books, conflicts: conflicts,
		provider: provider, item: item, book: book}
}

// userEdit unmonitors the book and sets a narrator, the kind of change a
// user saves from the book page, through the ordinary full row Update.
func (f *mergeRaceFixture) userEdit(t *testing.T) func() {
	return func() {
		current, err := f.books.GetByID(context.Background(), f.book.ID)
		if err != nil || current == nil {
			t.Errorf("load book for concurrent edit: %v", err)
			return
		}
		current.Monitored = false
		current.Narrator = "User Narrator"
		if err := f.books.Update(context.Background(), current); err != nil {
			t.Errorf("concurrent edit: %v", err)
		}
	}
}

// TestMergeUpstreamBookRetriesOnTopOfConcurrentEdit covers #2926 for the ABS
// metadata merge: an edit committed while the upstream lookup is in flight
// survives, and the upstream record already in hand is merged once more onto
// the edited row, so the relink and the conflict bookkeeping still land.
func TestMergeUpstreamBookRetriesOnTopOfConcurrentEdit(t *testing.T) {
	t.Parallel()
	f := newMergeRaceFixture(t)
	ctx := context.Background()
	absForeignID := f.book.ForeignID
	f.provider.edit = f.userEdit(t)

	result, err := f.importer.enrichBook(ctx, asinTestConfig(), f.item, nil, f.book)
	if err != nil {
		t.Fatalf("enrichBook: %v", err)
	}

	stored, err := f.books.GetByID(ctx, f.book.ID)
	if err != nil || stored == nil {
		t.Fatalf("reload book: %v", err)
	}
	if stored.Monitored || stored.Narrator != "User Narrator" {
		t.Fatalf("concurrent edit overwritten by the metadata merge: monitored=%v narrator=%q", stored.Monitored, stored.Narrator)
	}
	if stored.ForeignID != mergeRaceUpstreamID || stored.Description != "Upstream description of the book." {
		t.Fatalf("merge not applied on the retry: foreignID=%q description=%q", stored.ForeignID, stored.Description)
	}
	if f.book.ForeignID != mergeRaceUpstreamID || f.book.Monitored {
		t.Fatalf("caller row not refreshed: foreignID=%q monitored=%v", f.book.ForeignID, f.book.Monitored)
	}
	if result.Relinked != 1 {
		t.Errorf("Relinked = %d, want 1", result.Relinked)
	}
	if ident, err := f.books.GetBookIdentifier(ctx, absForeignID); err != nil || ident == nil || ident.BookID != f.book.ID {
		t.Errorf("ABS identifier not kept across the relink: %+v err=%v", ident, err)
	}
	if conflict, err := f.conflicts.GetByEntityField(ctx, entityTypeBook, f.book.ID, "description"); err != nil || conflict == nil {
		t.Errorf("description conflict not recorded: %+v err=%v", conflict, err)
	}
}

// TestMergeUpstreamBookSkipsWhenTheBookKeepsChanging is the second loss: the
// merge is dropped for this import, and neither the relink count nor the
// conflict rows are recorded for a merge that never landed. A trigger stands
// in for an edit that lands on every attempt by silently ignoring the relink
// write.
func TestMergeUpstreamBookSkipsWhenTheBookKeepsChanging(t *testing.T) {
	t.Parallel()
	f := newMergeRaceFixture(t)
	ctx := context.Background()
	absForeignID := f.book.ForeignID
	if _, err := f.database.ExecContext(ctx, `
		CREATE TRIGGER test_ignore_relink BEFORE UPDATE OF foreign_id ON books
		WHEN NEW.foreign_id = '`+mergeRaceUpstreamID+`'
		BEGIN SELECT RAISE(IGNORE); END`); err != nil {
		t.Fatal(err)
	}
	f.provider.edit = f.userEdit(t)

	result, err := f.importer.enrichBook(ctx, asinTestConfig(), f.item, nil, f.book)
	if err != nil {
		t.Fatalf("enrichBook: %v", err)
	}

	stored, err := f.books.GetByID(ctx, f.book.ID)
	if err != nil || stored == nil {
		t.Fatalf("reload book: %v", err)
	}
	if stored.Monitored || stored.Narrator != "User Narrator" || stored.ForeignID != absForeignID {
		t.Fatalf("stored row = monitored=%v narrator=%q foreignID=%q, want the user's edit and no relink",
			stored.Monitored, stored.Narrator, stored.ForeignID)
	}
	if f.book.Monitored || f.book.ForeignID != absForeignID {
		t.Fatalf("caller row not refreshed: foreignID=%q monitored=%v", f.book.ForeignID, f.book.Monitored)
	}
	if result.Relinked != 0 || result.Conflicts != 0 || result.AutoResolved != 0 {
		t.Errorf("result counts a merge that was not written: %+v", result)
	}
	if msg := strings.Join(result.Messages, "; "); !strings.Contains(msg, "changed") {
		t.Errorf("messages = %q, want a skipped merge reason", msg)
	}
	// The ABS id is kept before the relink write is attempted (#1691), so it
	// is there even though the relink lost; it still names this row.
	if ident, err := f.books.GetBookIdentifier(ctx, absForeignID); err != nil || ident == nil || ident.BookID != f.book.ID {
		t.Errorf("ABS identifier not kept ahead of the relink: %+v err=%v", ident, err)
	}
	if conflict, err := f.conflicts.GetByEntityField(ctx, entityTypeBook, f.book.ID, "description"); err != nil || conflict != nil {
		t.Errorf("conflict recorded for a merge that did not happen: %+v err=%v", conflict, err)
	}
}

// TestMergeUpstreamBookPersistsWithoutConcurrentEdit is the control: with
// nothing racing it, the guarded merge lands on the first attempt.
func TestMergeUpstreamBookPersistsWithoutConcurrentEdit(t *testing.T) {
	t.Parallel()
	f := newMergeRaceFixture(t)
	ctx := context.Background()

	result, err := f.importer.enrichBook(ctx, asinTestConfig(), f.item, nil, f.book)
	if err != nil {
		t.Fatalf("enrichBook: %v", err)
	}
	stored, err := f.books.GetByID(ctx, f.book.ID)
	if err != nil || stored == nil {
		t.Fatalf("reload book: %v", err)
	}
	if !stored.Monitored || stored.ForeignID != mergeRaceUpstreamID || stored.LastMetadataRefreshAt == nil {
		t.Fatalf("merge not persisted: monitored=%v foreignID=%q refreshed=%v", stored.Monitored, stored.ForeignID, stored.LastMetadataRefreshAt)
	}
	if result.Relinked != 1 {
		t.Errorf("Relinked = %d, want 1", result.Relinked)
	}
}

// authorMergeRaceUpstreamID is the upstream id the author lookup relinks to.
const authorMergeRaceUpstreamID = "OL-RACE-AUTHOR-A"

// editingAuthorProvider answers the author search and, while it fetches the
// full author record, runs edit: a user change committed between the
// importer's read of the author and its merge write (#2926).
type editingAuthorProvider struct {
	bindingStubProvider
	edit func()
}

func (p *editingAuthorProvider) GetAuthor(ctx context.Context, foreignID string) (*models.Author, error) {
	if p.edit != nil {
		p.edit()
	}
	return p.bindingStubProvider.GetAuthor(ctx, foreignID)
}

type authorMergeRaceFixture struct {
	importer  *Importer
	database  *sql.DB
	authors   *db.AuthorRepo
	aliases   *db.AuthorAliasRepo
	conflicts *db.ABSMetadataConflictRepo
	provider  *editingAuthorProvider
	matcher   *authorMatcher
	item      NormalizedLibraryItem
	author    *models.Author
}

// newAuthorMergeRaceFixture holds an author on its ABS identity, which the
// upstream record renames ("Andy Weir" to "Andrew Weir", so the old name is
// kept as an alias), relinks and gives a description that conflicts with the
// ABS one.
func newAuthorMergeRaceFixture(t *testing.T) *authorMergeRaceFixture {
	t.Helper()
	database, err := db.OpenMemory()
	if err != nil {
		t.Fatalf("open memory db: %v", err)
	}
	t.Cleanup(func() { database.Close() })
	authors := db.NewAuthorRepo(database)
	aliases := db.NewAuthorAliasRepo(database)
	conflicts := db.NewABSMetadataConflictRepo(database)
	importer := NewImporter(
		authors,
		aliases,
		db.NewBookRepo(database),
		db.NewEditionRepo(database),
		db.NewSeriesRepo(database),
		db.NewSettingsRepo(database),
		db.NewABSImportRunRepo(database),
		db.NewABSImportRunEntityRepo(database),
		db.NewABSProvenanceRepo(database),
		db.NewABSReviewItemRepo(database),
		conflicts,
	)
	provider := &editingAuthorProvider{bindingStubProvider: bindingStubProvider{
		name:    "openlibrary",
		authors: []models.Author{{Name: "Andy Weir", ForeignID: authorMergeRaceUpstreamID}},
		full: map[string]*models.Author{authorMergeRaceUpstreamID: {
			Name: "Andrew Weir", SortName: "Weir, Andrew", ForeignID: authorMergeRaceUpstreamID,
			Description: "Upstream biography.", MetadataProvider: "openlibrary",
		}},
	}}
	importer.meta = metadata.NewAggregator(provider)

	ctx := context.Background()
	author := &models.Author{
		ForeignID: "abs:author:lib-books:author-andy-weir", Name: "Andy Weir", SortName: "Weir, Andy",
		Description: "ABS biography.", MetadataProvider: providerAudiobookshelf, Monitored: true,
	}
	if err := authors.Create(ctx, author); err != nil {
		t.Fatalf("create author: %v", err)
	}
	matcher, err := importer.newAuthorMatcher(ctx)
	if err != nil {
		t.Fatalf("author matcher: %v", err)
	}
	item := sampleABSItem()
	item.Description = ""
	return &authorMergeRaceFixture{importer: importer, database: database, authors: authors, aliases: aliases,
		conflicts: conflicts, provider: provider, matcher: matcher, item: item, author: author}
}

// userEdit unmonitors the author through the ordinary full row Update.
func (f *authorMergeRaceFixture) userEdit(t *testing.T) func() {
	return func() {
		current, err := f.authors.GetByID(context.Background(), f.author.ID)
		if err != nil || current == nil {
			t.Errorf("load author for concurrent edit: %v", err)
			return
		}
		current.Monitored = false
		if err := f.authors.Update(context.Background(), current); err != nil {
			t.Errorf("concurrent edit: %v", err)
		}
	}
}

func (f *authorMergeRaceFixture) aliasNames(t *testing.T) []string {
	t.Helper()
	list, err := f.aliases.ListByAuthor(context.Background(), f.author.ID)
	if err != nil {
		t.Fatal(err)
	}
	names := make([]string, 0, len(list))
	for _, a := range list {
		names = append(names, a.Name)
	}
	return names
}

// TestMergeUpstreamAuthorRetriesOnTopOfConcurrentEdit covers #2926 for the
// ABS author merge: an edit committed while the upstream author is fetched
// survives, and the record in hand is merged once more onto the edited row.
func TestMergeUpstreamAuthorRetriesOnTopOfConcurrentEdit(t *testing.T) {
	t.Parallel()
	f := newAuthorMergeRaceFixture(t)
	ctx := context.Background()
	f.provider.edit = f.userEdit(t)

	result, err := f.importer.enrichAuthor(ctx, asinTestConfig(), f.item, f.author, f.matcher)
	if err != nil {
		t.Fatalf("enrichAuthor: %v", err)
	}

	stored, err := f.authors.GetByID(ctx, f.author.ID)
	if err != nil || stored == nil {
		t.Fatalf("reload author: %v", err)
	}
	if stored.Monitored {
		t.Fatal("concurrent unmonitor overwritten by the author merge")
	}
	if stored.ForeignID != authorMergeRaceUpstreamID || stored.Name != "Andrew Weir" || stored.LastMetadataRefreshAt == nil {
		t.Fatalf("merge not applied on the retry: foreignID=%q name=%q refreshed=%v", stored.ForeignID, stored.Name, stored.LastMetadataRefreshAt)
	}
	if f.author.Monitored || f.author.ForeignID != authorMergeRaceUpstreamID {
		t.Fatalf("caller row not refreshed: foreignID=%q monitored=%v", f.author.ForeignID, f.author.Monitored)
	}
	if result.Relinked != 1 {
		t.Errorf("Relinked = %d, want 1", result.Relinked)
	}
	if names := f.aliasNames(t); !slices.Contains(names, "Andy Weir") {
		t.Errorf("old name not kept as an alias: %v", names)
	}
	if conflict, err := f.conflicts.GetByEntityField(ctx, entityTypeAuthor, f.author.ID, "description"); err != nil || conflict == nil {
		t.Errorf("description conflict not recorded: %+v err=%v", conflict, err)
	}
}

// TestMergeUpstreamAuthorSkipsWhenTheAuthorKeepsChanging is the second loss:
// the merge is dropped for this import and leaves no alias, identifier or
// conflict row behind. A trigger ignores the relink write so every attempt
// loses.
func TestMergeUpstreamAuthorSkipsWhenTheAuthorKeepsChanging(t *testing.T) {
	t.Parallel()
	f := newAuthorMergeRaceFixture(t)
	ctx := context.Background()
	absForeignID := f.author.ForeignID
	if _, err := f.database.ExecContext(ctx, `
		CREATE TRIGGER test_ignore_author_relink BEFORE UPDATE OF foreign_id ON authors
		WHEN NEW.foreign_id = '`+authorMergeRaceUpstreamID+`'
		BEGIN SELECT RAISE(IGNORE); END`); err != nil {
		t.Fatal(err)
	}
	f.provider.edit = f.userEdit(t)

	result, err := f.importer.enrichAuthor(ctx, asinTestConfig(), f.item, f.author, f.matcher)
	if err != nil {
		t.Fatalf("enrichAuthor: %v", err)
	}

	stored, err := f.authors.GetByID(ctx, f.author.ID)
	if err != nil || stored == nil {
		t.Fatalf("reload author: %v", err)
	}
	if stored.Monitored || stored.ForeignID != absForeignID || stored.Name != "Andy Weir" {
		t.Fatalf("stored row = monitored=%v foreignID=%q name=%q, want the user's edit and no merge",
			stored.Monitored, stored.ForeignID, stored.Name)
	}
	if f.author.Monitored || f.author.ForeignID != absForeignID {
		t.Fatalf("caller row not refreshed: foreignID=%q monitored=%v", f.author.ForeignID, f.author.Monitored)
	}
	if result.Relinked != 0 || result.Conflicts != 0 || result.AutoResolved != 0 {
		t.Errorf("result counts a merge that was not written: %+v", result)
	}
	if msg := strings.Join(result.Messages, "; "); !strings.Contains(msg, "changed") {
		t.Errorf("messages = %q, want a skipped merge reason", msg)
	}
	if names := f.aliasNames(t); len(names) != 0 {
		t.Errorf("alias recorded for a rename that did not happen: %v", names)
	}
	if owner, err := f.authors.GetAuthorIdentifier(ctx, authorMergeRaceUpstreamID); err != nil || owner != nil {
		t.Errorf("identifier recorded for a relink that did not happen: %+v err=%v", owner, err)
	}
	if conflict, err := f.conflicts.GetByEntityField(ctx, entityTypeAuthor, f.author.ID, "description"); err != nil || conflict != nil {
		t.Errorf("conflict recorded for a merge that did not happen: %+v err=%v", conflict, err)
	}
}

// TestMergeUpstreamAuthorPersistsWithoutConcurrentEdit is the control.
func TestMergeUpstreamAuthorPersistsWithoutConcurrentEdit(t *testing.T) {
	t.Parallel()
	f := newAuthorMergeRaceFixture(t)
	ctx := context.Background()

	result, err := f.importer.enrichAuthor(ctx, asinTestConfig(), f.item, f.author, f.matcher)
	if err != nil {
		t.Fatalf("enrichAuthor: %v", err)
	}
	stored, err := f.authors.GetByID(ctx, f.author.ID)
	if err != nil || stored == nil {
		t.Fatalf("reload author: %v", err)
	}
	if !stored.Monitored || stored.ForeignID != authorMergeRaceUpstreamID || stored.Name != "Andrew Weir" {
		t.Fatalf("merge not persisted: monitored=%v foreignID=%q name=%q", stored.Monitored, stored.ForeignID, stored.Name)
	}
	if result.Relinked != 1 {
		t.Errorf("Relinked = %d, want 1", result.Relinked)
	}
	if names := f.aliasNames(t); !slices.Contains(names, "Andy Weir") {
		t.Errorf("old name not kept as an alias: %v", names)
	}
}

// A book deleted while its upstream lookup runs is not an import failure:
// the merge is skipped with a message rather than failing the item with
// "sql: no rows".
func TestMergeUpstreamBookSkipsWhenTheBookIsDeletedDuringLookup(t *testing.T) {
	t.Parallel()
	f := newMergeRaceFixture(t)
	ctx := context.Background()
	f.provider.edit = func() {
		if err := f.books.Delete(ctx, f.book.ID); err != nil {
			t.Errorf("delete book: %v", err)
		}
	}

	result, err := f.importer.enrichBook(ctx, asinTestConfig(), f.item, nil, f.book)
	if err != nil {
		t.Fatalf("enrichBook failed the item for a deleted book: %v", err)
	}
	if msg := strings.Join(result.Messages, "; "); !strings.Contains(msg, "deleted") {
		t.Errorf("messages = %q, want a deleted row skip reason", msg)
	}
	if stored, err := f.books.GetByID(ctx, f.book.ID); err != nil || stored != nil {
		t.Errorf("the merge recreated a deleted book: %+v err=%v", stored, err)
	}
}

// The author counterpart of the deleted book case.
func TestMergeUpstreamAuthorSkipsWhenTheAuthorIsDeletedDuringLookup(t *testing.T) {
	t.Parallel()
	f := newAuthorMergeRaceFixture(t)
	ctx := context.Background()
	f.provider.edit = func() {
		if err := f.authors.Delete(ctx, f.author.ID); err != nil {
			t.Errorf("delete author: %v", err)
		}
	}

	result, err := f.importer.enrichAuthor(ctx, asinTestConfig(), f.item, f.author, f.matcher)
	if err != nil {
		t.Fatalf("enrichAuthor failed the item for a deleted author: %v", err)
	}
	if msg := strings.Join(result.Messages, "; "); !strings.Contains(msg, "deleted") {
		t.Errorf("messages = %q, want a deleted row skip reason", msg)
	}
	if stored, err := f.authors.GetByID(ctx, f.author.ID); err != nil || stored != nil {
		t.Errorf("the merge recreated a deleted author: %+v err=%v", stored, err)
	}
}

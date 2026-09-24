package hardcoverlistsyncer

import (
	"context"
	"testing"

	"github.com/vavallee/bindery/internal/auth"
	"github.com/vavallee/bindery/internal/db"
	"github.com/vavallee/bindery/internal/metadata/hardcover"
	"github.com/vavallee/bindery/internal/models"
)

// tenancyFixture is the two user shape every test in this file needs: a real
// database, the three repos the syncer takes, and two seeded users so owner
// columns can hold ids the FK accepts.
type tenancyFixture struct {
	syncer      *ListSyncer
	importLists *db.ImportListRepo
	authors     *db.AuthorRepo
	books       *db.BookRepo
	alice       int64
	bob         int64
}

func newTenancyFixture(t *testing.T) *tenancyFixture {
	t.Helper()
	database, err := db.OpenMemory()
	if err != nil {
		t.Fatalf("open memory db: %v", err)
	}
	t.Cleanup(func() { database.Close() })

	importLists := db.NewImportListRepo(database)
	authors := db.NewAuthorRepo(database)
	books := db.NewBookRepo(database)
	users := db.NewUserRepo(database)
	ctx := context.Background()

	alice, err := users.Create(ctx, "alice", "hash")
	if err != nil {
		t.Fatalf("seed alice: %v", err)
	}
	bob, err := users.Create(ctx, "bob", "hash")
	if err != nil {
		t.Fatalf("seed bob: %v", err)
	}

	return &tenancyFixture{
		syncer:      New(importLists, authors, books),
		importLists: importLists,
		authors:     authors,
		books:       books,
		alice:       alice.ID,
		bob:         bob.ID,
	}
}

// seedAuthor inserts an author owned by ownerID (0 = global/NULL owner).
func (f *tenancyFixture) seedAuthor(t *testing.T, foreignID, name string, ownerID int64) *models.Author {
	t.Helper()
	a := &models.Author{ForeignID: foreignID, Name: name, SortName: sortName(name), MetadataProvider: "openlibrary"}
	if err := f.authors.CreateForUser(context.Background(), a, ownerID); err != nil {
		t.Fatalf("seed author %q: %v", name, err)
	}
	return a
}

// seedBook inserts a book owned by ownerID (0 = global/NULL owner).
func (f *tenancyFixture) seedBook(t *testing.T, b *models.Book) *models.Book {
	t.Helper()
	if err := f.books.Create(context.Background(), b); err != nil {
		t.Fatalf("seed book %q: %v", b.Title, err)
	}
	return b
}

// aliceList creates an enabled Hardcover list owned by alice serving books.
func (f *tenancyFixture) aliceList(t *testing.T, name string, mediaType string, books []models.Book) models.ImportList {
	t.Helper()
	il := testImportList(name, "hardcover", true)
	il.OwnerUserID = &f.alice
	il.MediaType = mediaType
	if err := f.importLists.Create(context.Background(), &il); err != nil {
		t.Fatalf("seed list: %v", err)
	}
	f.syncer.WithClientFactory(func(string) hardcoverClient {
		return &fakeHardcoverClient{
			lists: []hardcover.HCList{{ID: 900, Slug: il.URL, Name: il.Name}},
			books: books,
		}
	})
	return il
}

// TestSyncOne_EnforcedTenancyDoesNotWidenAnotherUsersBook is the headline case
// of #2766. Bob owns an ebook with a Hardcover foreign id. Alice syncs an
// audiobook list that contains the same work. Before the fix the unscoped
// GetByForeignID found Bob's row, widened it to "both" and re-opened it as
// wanted, so one user's list rewrote another user's library row.
func TestSyncOne_EnforcedTenancyDoesNotWidenAnotherUsersBook(t *testing.T) {
	auth.SetEnforceTenancyForTests(t, true)
	f := newTenancyFixture(t)
	ctx := context.Background()

	bobAuthor := f.seedAuthor(t, "ol:OL1A", "Ursula K. Le Guin", f.bob)
	f.seedBook(t, &models.Book{
		ForeignID:        "hc:lefthand",
		Title:            "The Left Hand of Darkness",
		AuthorID:         bobAuthor.ID,
		OwnerUserID:      f.bob,
		MediaType:        models.MediaTypeEbook,
		Status:           models.BookStatusImported,
		MetadataProvider: "hardcover",
	})

	il := f.aliceList(t, "Alice audiobooks", models.MediaTypeAudiobook, []models.Book{
		{ForeignID: "hc:lefthand", Title: "The Left Hand of Darkness", MetadataProvider: "hardcover",
			Author: &models.Author{ForeignID: "hc:leguin", Name: "Ursula K. Le Guin", MetadataProvider: "hardcover"}},
	})

	if err := f.syncer.SyncOne(ctx, il.ID); err != nil {
		t.Fatalf("SyncOne: %v", err)
	}

	got, err := f.books.GetByForeignID(ctx, "hc:lefthand")
	if err != nil || got == nil {
		t.Fatalf("bob's book not found: %v", err)
	}
	if got.OwnerUserID != f.bob {
		t.Fatalf("book owner = %d, want bob (%d)", got.OwnerUserID, f.bob)
	}
	if got.MediaType != models.MediaTypeEbook {
		t.Errorf("bob's book MediaType = %q, want %q: alice's list widened a book she does not own (#2766)",
			got.MediaType, models.MediaTypeEbook)
	}
	if got.Status != models.BookStatusImported {
		t.Errorf("bob's book Status = %q, want %q: alice's list re-opened a book she does not own (#2766)",
			got.Status, models.BookStatusImported)
	}
}

// TestSyncOne_UnenforcedTenancyStillWidensSharedLibraryBook pins the default.
// With BINDERY_ENFORCE_TENANCY off every user shares one library view, two
// "users" are usually one person, and the widen is the wanted behaviour. This
// must keep passing before and after the fix.
func TestSyncOne_UnenforcedTenancyStillWidensSharedLibraryBook(t *testing.T) {
	auth.SetEnforceTenancyForTests(t, false)
	f := newTenancyFixture(t)
	ctx := context.Background()

	bobAuthor := f.seedAuthor(t, "ol:OL1A", "Ursula K. Le Guin", f.bob)
	f.seedBook(t, &models.Book{
		ForeignID:        "hc:lefthand",
		Title:            "The Left Hand of Darkness",
		AuthorID:         bobAuthor.ID,
		OwnerUserID:      f.bob,
		MediaType:        models.MediaTypeEbook,
		Status:           models.BookStatusImported,
		MetadataProvider: "hardcover",
	})

	il := f.aliceList(t, "Alice audiobooks", models.MediaTypeAudiobook, []models.Book{
		{ForeignID: "hc:lefthand", Title: "The Left Hand of Darkness", MetadataProvider: "hardcover",
			Author: &models.Author{ForeignID: "hc:leguin", Name: "Ursula K. Le Guin", MetadataProvider: "hardcover"}},
	})

	if err := f.syncer.SyncOne(ctx, il.ID); err != nil {
		t.Fatalf("SyncOne: %v", err)
	}

	got, err := f.books.GetByForeignID(ctx, "hc:lefthand")
	if err != nil || got == nil {
		t.Fatalf("book not found: %v", err)
	}
	if got.MediaType != models.MediaTypeBoth {
		t.Errorf("MediaType = %q, want %q: enforcement is off, the shared library view must still widen",
			got.MediaType, models.MediaTypeBoth)
	}
}

// TestSyncOne_EnforcedTenancyCreatesOwnBookUnderSharedAuthor is the case where
// scoping actually delivers the book. The author is global (NULL owner), so it
// stays reusable by everyone. Bob owns a row for the same work imported from
// Calibre with no Hardcover foreign id, which the unscoped dedup-key lookup
// found, so Alice's list skipped the book and she never got it.
func TestSyncOne_EnforcedTenancyCreatesOwnBookUnderSharedAuthor(t *testing.T) {
	auth.SetEnforceTenancyForTests(t, true)
	f := newTenancyFixture(t)
	ctx := context.Background()

	shared := f.seedAuthor(t, "hc:sanderson", "Brandon Sanderson", 0)
	f.seedBook(t, &models.Book{
		ForeignID:        "calibre:77",
		Title:            "The Final Empire",
		AuthorID:         shared.ID,
		OwnerUserID:      f.bob,
		MediaType:        models.MediaTypeEbook,
		Status:           models.BookStatusImported,
		MetadataProvider: "calibre",
	})

	il := f.aliceList(t, "Alice shelf", "", []models.Book{
		{ForeignID: "hc:finalempire", Title: "The Final Empire", MetadataProvider: "hardcover",
			Author: &models.Author{ForeignID: "hc:sanderson", Name: "Brandon Sanderson", MetadataProvider: "hardcover"}},
	})

	if err := f.syncer.SyncOne(ctx, il.ID); err != nil {
		t.Fatalf("SyncOne: %v", err)
	}

	got, err := f.books.GetByForeignID(ctx, "hc:finalempire")
	if err != nil {
		t.Fatalf("GetByForeignID: %v", err)
	}
	if got == nil {
		t.Fatal("alice's list created no book: it deduped against bob's row under a shared author (#2766)")
	}
	if got.OwnerUserID != f.alice {
		t.Errorf("new book owner = %d, want alice (%d)", got.OwnerUserID, f.alice)
	}
	if got.AuthorID != shared.ID {
		t.Errorf("new book author = %d, want the shared global author %d", got.AuthorID, shared.ID)
	}
}

// TestSyncOne_UnenforcedTenancyDedupsAgainstAnotherUsersBook is the companion
// that pins the default for the dedup-key path: with enforcement off the same
// fixture must still collapse onto the existing row and create nothing.
func TestSyncOne_UnenforcedTenancyDedupsAgainstAnotherUsersBook(t *testing.T) {
	auth.SetEnforceTenancyForTests(t, false)
	f := newTenancyFixture(t)
	ctx := context.Background()

	shared := f.seedAuthor(t, "hc:sanderson", "Brandon Sanderson", 0)
	f.seedBook(t, &models.Book{
		ForeignID:        "calibre:77",
		Title:            "The Final Empire",
		AuthorID:         shared.ID,
		OwnerUserID:      f.bob,
		MediaType:        models.MediaTypeEbook,
		Status:           models.BookStatusImported,
		MetadataProvider: "calibre",
	})

	il := f.aliceList(t, "Alice shelf", "", []models.Book{
		{ForeignID: "hc:finalempire", Title: "The Final Empire", MetadataProvider: "hardcover",
			Author: &models.Author{ForeignID: "hc:sanderson", Name: "Brandon Sanderson", MetadataProvider: "hardcover"}},
	})

	if err := f.syncer.SyncOne(ctx, il.ID); err != nil {
		t.Fatalf("SyncOne: %v", err)
	}

	under, err := f.books.ListByAuthor(ctx, shared.ID)
	if err != nil {
		t.Fatalf("ListByAuthor: %v", err)
	}
	if len(under) != 1 {
		t.Fatalf("got %d books under the shared author, want 1: enforcement is off, the dedup must still collapse", len(under))
	}
}

// TestSyncOne_EnforcedTenancyDoesNotReuseAnotherUsersAuthorByName covers the
// author name index. Bob owns "Nnedi Okorafor" under an OpenLibrary id.
// Alice's list carries the same author under a Hardcover id, so the global
// name index matched Bob's row and Alice's book landed under an author she
// cannot see.
func TestSyncOne_EnforcedTenancyDoesNotReuseAnotherUsersAuthorByName(t *testing.T) {
	auth.SetEnforceTenancyForTests(t, true)
	f := newTenancyFixture(t)
	ctx := context.Background()

	bobAuthor := f.seedAuthor(t, "ol:OL77A", "Nnedi Okorafor", f.bob)

	il := f.aliceList(t, "Alice shelf", "", []models.Book{
		{ForeignID: "hc:binti", Title: "Binti", MetadataProvider: "hardcover",
			Author: &models.Author{ForeignID: "hc:okorafor", Name: "Nnedi  Okorafor", MetadataProvider: "hardcover"}},
	})

	if err := f.syncer.SyncOne(ctx, il.ID); err != nil {
		t.Fatalf("SyncOne: %v", err)
	}

	got, err := f.books.GetByForeignID(ctx, "hc:binti")
	if err != nil || got == nil {
		t.Fatalf("alice's book not found: %v", err)
	}
	if got.AuthorID == bobAuthor.ID {
		t.Fatalf("alice's book landed under bob's author %d: the name index is not owner scoped (#2766)", bobAuthor.ID)
	}
	newAuthor, err := f.authors.GetByAnyForeignID(ctx, "hc:okorafor")
	if err != nil || newAuthor == nil {
		t.Fatalf("expected a new author for alice: %v", err)
	}
	if newAuthor.OwnerUserID != f.alice {
		t.Errorf("new author owner = %d, want alice (%d)", newAuthor.OwnerUserID, f.alice)
	}
}

// TestSyncOne_UnenforcedTenancyReusesAuthorByNameAcrossOwners is the companion
// default: with enforcement off the name index is global, so the same fixture
// must reuse the existing author instead of creating a second row.
func TestSyncOne_UnenforcedTenancyReusesAuthorByNameAcrossOwners(t *testing.T) {
	auth.SetEnforceTenancyForTests(t, false)
	f := newTenancyFixture(t)
	ctx := context.Background()

	bobAuthor := f.seedAuthor(t, "ol:OL77A", "Nnedi Okorafor", f.bob)

	il := f.aliceList(t, "Alice shelf", "", []models.Book{
		{ForeignID: "hc:binti", Title: "Binti", MetadataProvider: "hardcover",
			Author: &models.Author{ForeignID: "hc:okorafor", Name: "Nnedi  Okorafor", MetadataProvider: "hardcover"}},
	})

	if err := f.syncer.SyncOne(ctx, il.ID); err != nil {
		t.Fatalf("SyncOne: %v", err)
	}

	got, err := f.books.GetByForeignID(ctx, "hc:binti")
	if err != nil || got == nil {
		t.Fatalf("book not found: %v", err)
	}
	if got.AuthorID != bobAuthor.ID {
		t.Errorf("book author = %d, want the existing author %d: enforcement is off, the name index stays global",
			got.AuthorID, bobAuthor.ID)
	}
	all, err := f.authors.List(ctx)
	if err != nil {
		t.Fatalf("List authors: %v", err)
	}
	if len(all) != 1 {
		t.Errorf("got %d authors, want 1 reused row", len(all))
	}
}

// TestSyncOne_EnforcedTenancyReusesGlobalAuthor states the author rule: an
// unowned (NULL owner) author is shared infrastructure and stays reusable by
// every list under either setting. Only an author owned by somebody else is
// off limits.
func TestSyncOne_EnforcedTenancyReusesGlobalAuthor(t *testing.T) {
	auth.SetEnforceTenancyForTests(t, true)
	f := newTenancyFixture(t)
	ctx := context.Background()

	global := f.seedAuthor(t, "ol:OL5A", "Becky Chambers", 0)

	il := f.aliceList(t, "Alice shelf", "", []models.Book{
		{ForeignID: "hc:angry", Title: "The Long Way to a Small Angry Planet", MetadataProvider: "hardcover",
			Author: &models.Author{ForeignID: "hc:chambers", Name: "Becky Chambers", MetadataProvider: "hardcover"}},
	})

	if err := f.syncer.SyncOne(ctx, il.ID); err != nil {
		t.Fatalf("SyncOne: %v", err)
	}

	got, err := f.books.GetByForeignID(ctx, "hc:angry")
	if err != nil || got == nil {
		t.Fatalf("alice's book not found: %v", err)
	}
	if got.AuthorID != global.ID {
		t.Errorf("book author = %d, want the global author %d: an unowned author must stay reusable", got.AuthorID, global.ID)
	}
}

// TestSyncOne_EnforcedTenancySkipsWhenForeignIDBelongsToAnotherUser pins the
// limit of this fix. books.foreign_id and authors.foreign_id are globally
// UNIQUE (migration 001), so when the only row carrying the list's foreign id
// belongs to another user there is no second row to create. The sync must then
// leave that row alone and count the book as skipped, not failed.
func TestSyncOne_EnforcedTenancySkipsWhenForeignIDBelongsToAnotherUser(t *testing.T) {
	auth.SetEnforceTenancyForTests(t, true)
	f := newTenancyFixture(t)
	ctx := context.Background()

	bobAuthor := f.seedAuthor(t, "hc:atwood", "Margaret Atwood", f.bob)
	f.seedBook(t, &models.Book{
		ForeignID:        "hc:oryx",
		Title:            "Oryx and Crake",
		AuthorID:         bobAuthor.ID,
		OwnerUserID:      f.bob,
		MediaType:        models.MediaTypeEbook,
		Status:           models.BookStatusImported,
		MetadataProvider: "hardcover",
	})

	il := f.aliceList(t, "Alice shelf", "", []models.Book{
		{ForeignID: "hc:oryx", Title: "Oryx and Crake", MetadataProvider: "hardcover",
			Author: &models.Author{ForeignID: "hc:atwood", Name: "Margaret Atwood", MetadataProvider: "hardcover"}},
		{ForeignID: "hc:handmaid", Title: "The Handmaid's Tale", MetadataProvider: "hardcover",
			Author: &models.Author{ForeignID: "hc:atwood", Name: "Margaret Atwood", MetadataProvider: "hardcover"}},
	})

	if err := f.syncer.SyncOne(ctx, il.ID); err != nil {
		t.Fatalf("SyncOne: %v", err)
	}

	stats := f.syncer.Progress().Stats
	if stats.Failed != 0 {
		t.Errorf("Failed = %d, want 0: a foreign id owned by another user is a skip, not a failure", stats.Failed)
	}
	if stats.Skipped != 2 {
		t.Errorf("Skipped = %d, want 2 (both books hang off an author alice cannot use)", stats.Skipped)
	}
	if stats.Imported != 0 {
		t.Errorf("Imported = %d, want 0", stats.Imported)
	}

	authorsAll, err := f.authors.List(ctx)
	if err != nil {
		t.Fatalf("List authors: %v", err)
	}
	if len(authorsAll) != 1 {
		t.Errorf("got %d authors, want 1: bob's author must be neither reused nor duplicated", len(authorsAll))
	}
}

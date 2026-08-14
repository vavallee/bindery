package hardcoverlistsyncer

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/vavallee/bindery/internal/db"
	"github.com/vavallee/bindery/internal/jobs"
	"github.com/vavallee/bindery/internal/metadata/hardcover"
	"github.com/vavallee/bindery/internal/models"
)

// The syncList path constructs a real hardcover.Client whose GraphQL
// endpoint is a package-level const, so it cannot be redirected to a test
// server without changing source. These tests cover the paths that don't
// reach the network: the empty-list short-circuit, error propagation from
// the ImportList repo, the sortName helper, and the constructor.

func newTestSyncer(t *testing.T) (*ListSyncer, *db.ImportListRepo) {
	t.Helper()
	database, err := db.OpenMemory()
	if err != nil {
		t.Fatalf("open memory db: %v", err)
	}
	t.Cleanup(func() { database.Close() })

	importLists := db.NewImportListRepo(database)
	authors := db.NewAuthorRepo(database)
	books := db.NewBookRepo(database)
	return New(importLists, authors, books), importLists
}

func TestNew_WiresRepos(t *testing.T) {
	s, _ := newTestSyncer(t)
	if s == nil {
		t.Fatal("New returned nil")
		return
	}
	if s.importLists == nil || s.authors == nil || s.books == nil {
		t.Errorf("expected all repo fields to be set, got %+v", s)
	}
}

// TestSync_NoEnabledLists exercises the early-return when no hardcover
// import lists are enabled. This is the happy no-op path: Sync must
// succeed without touching the network.
func TestSync_NoEnabledLists(t *testing.T) {
	s, _ := newTestSyncer(t)
	if err := s.Sync(context.Background()); err != nil {
		t.Errorf("Sync on empty list set: want nil, got %v", err)
	}
}

// TestSync_IgnoresNonHardcoverLists verifies that only lists with
// Type="hardcover" are considered. Seeding a goodreads list should not
// pull it into the sync loop, so the call is still a no-op.
func TestSync_IgnoresNonHardcoverLists(t *testing.T) {
	s, repo := newTestSyncer(t)
	ctx := context.Background()

	// Not a hardcover list — must be ignored by ListByType("hardcover").
	il := testImportList("Goodreads", "goodreads", true)
	if err := repo.Create(ctx, &il); err != nil {
		t.Fatalf("seed: %v", err)
	}
	if err := s.Sync(ctx); err != nil {
		t.Errorf("Sync: want nil, got %v", err)
	}
}

// TestSync_IgnoresDisabledHardcoverLists verifies disabled hardcover lists
// are filtered out by the ImportListRepo (ListByType only returns enabled).
func TestSync_IgnoresDisabledHardcoverLists(t *testing.T) {
	s, repo := newTestSyncer(t)
	ctx := context.Background()

	il := testImportList("DisabledHC", "hardcover", false)
	if err := repo.Create(ctx, &il); err != nil {
		t.Fatalf("seed: %v", err)
	}
	if err := s.Sync(ctx); err != nil {
		t.Errorf("Sync: want nil, got %v", err)
	}
}

func TestUniqueAuthorByName(t *testing.T) {
	index := map[string][]models.Author{
		"john smith": {{ID: 1, Name: "John Smith"}, {ID: 2, Name: "John Smith"}}, // ambiguous namesakes
		"jane doe":   {{ID: 3, Name: "Jane Doe"}},
	}
	// Ambiguous → nil (never guess which namesake to merge into).
	if got := uniqueAuthorByName(index, "John  Smith"); got != nil {
		t.Errorf("ambiguous match should return nil, got %+v", got)
	}
	// Unique → match (normalization collapses spacing/case).
	if got := uniqueAuthorByName(index, "  jane   DOE "); got == nil || got.ID != 3 {
		t.Errorf("expected unique match id 3, got %+v", got)
	}
	// No match → nil.
	if got := uniqueAuthorByName(index, "Nobody Here"); got != nil {
		t.Errorf("no match should return nil, got %+v", got)
	}
}

// TestSyncOne_ReusesAuthorByNameAndDedupsOwnedBook is the #1223 regression:
// a Hardcover list whose author/book already exist in the library under a
// different provider's foreign id must not spawn a parallel author row or a
// duplicate "wanted" book. The author is reconciled by normalized name (and
// gets the Hardcover id attached as an alias), and the already-owned book is
// bound by canonical dedup key instead of re-created.
func TestSyncOne_ReusesAuthorByNameAndDedupsOwnedBook(t *testing.T) {
	s, repo := newTestSyncer(t)
	ctx := context.Background()

	// Existing OpenLibrary-backed author + owned book, as an ABS import leaves them.
	existingAuthor := &models.Author{
		ForeignID:        "ol:OL123A",
		Name:             "George R. R. Martin",
		SortName:         "Martin, George R. R.",
		MetadataProvider: "openlibrary",
	}
	if err := s.authors.Create(ctx, existingAuthor); err != nil {
		t.Fatalf("seed author: %v", err)
	}
	ownedBook := &models.Book{
		ForeignID:        "ol:OL999W",
		Title:            "A Game of Thrones",
		AuthorID:         existingAuthor.ID,
		MetadataProvider: "openlibrary",
		Status:           models.BookStatusImported,
	}
	if err := s.books.Create(ctx, ownedBook); err != nil {
		t.Fatalf("seed book: %v", err)
	}

	il := testImportList("HC", "hardcover", true)
	if err := repo.Create(ctx, &il); err != nil {
		t.Fatalf("seed list: %v", err)
	}

	hcAuthor := func() *models.Author {
		return &models.Author{ForeignID: "hc:grrm", Name: "George R.R. Martin", MetadataProvider: "hardcover"}
	}
	s.WithClientFactory(func(string) hardcoverClient {
		return &fakeHardcoverClient{
			lists: []hardcover.HCList{{ID: 5, Slug: il.URL, Name: il.Name}},
			books: []models.Book{
				// Same book, spacing-variant author name, no Hardcover book id match.
				{ForeignID: "hc:got", Title: "A Game of Thrones", MetadataProvider: "hardcover", Author: hcAuthor()},
				// Genuinely new book by the same author.
				{ForeignID: "hc:clash", Title: "A Clash of Kings", MetadataProvider: "hardcover", Author: hcAuthor()},
			},
		}
	})

	if err := s.SyncOne(ctx, il.ID); err != nil {
		t.Fatalf("SyncOne: %v", err)
	}

	// No duplicate author row.
	authors, err := s.authors.List(ctx)
	if err != nil {
		t.Fatalf("List authors: %v", err)
	}
	if len(authors) != 1 {
		t.Fatalf("expected the existing author to be reused (1 author), got %d", len(authors))
	}

	// Hardcover foreign id attached as an alias on the existing author.
	byHC, err := s.authors.GetByAnyForeignID(ctx, "hc:grrm")
	if err != nil {
		t.Fatalf("GetByAnyForeignID: %v", err)
	}
	if byHC == nil || byHC.ID != existingAuthor.ID {
		t.Fatalf("expected hc:grrm alias to resolve to existing author %d, got %+v", existingAuthor.ID, byHC)
	}

	// Owned book not duplicated; exactly one new book created under the same author.
	booksUnder, err := s.books.ListByAuthor(ctx, existingAuthor.ID)
	if err != nil {
		t.Fatalf("ListByAuthor: %v", err)
	}
	if len(booksUnder) != 2 {
		t.Fatalf("expected 2 books (owned + 1 new), got %d: %+v", len(booksUnder), booksUnder)
	}
	titles := map[string]bool{}
	for _, b := range booksUnder {
		titles[b.Title] = true
	}
	if !titles["A Game of Thrones"] || !titles["A Clash of Kings"] {
		t.Fatalf("unexpected book set under author: %v", titles)
	}
}

// TestSyncOne_ListMediaTypeOverridesDerived covers the per-list media type
// feature: a list with MediaType set pins the format of the books it creates,
// overriding the Hardcover-derived media type (most works report both
// editions, so without this two single-format lists yield identical types).
func TestSyncOne_ListMediaTypeOverridesDerived(t *testing.T) {
	s, repo := newTestSyncer(t)
	ctx := context.Background()

	il := testImportList("Audiobooks", "hardcover", true)
	il.MediaType = models.MediaTypeAudiobook
	if err := repo.Create(ctx, &il); err != nil {
		t.Fatalf("seed list: %v", err)
	}

	author := func() *models.Author {
		return &models.Author{ForeignID: "hc:auth", Name: "Some Author", MetadataProvider: "hardcover"}
	}
	s.WithClientFactory(func(string) hardcoverClient {
		return &fakeHardcoverClient{
			lists: []hardcover.HCList{{ID: 9, Slug: il.URL, Name: il.Name}},
			books: []models.Book{
				// Hardcover says this work has both editions, but the list is an
				// audiobook list, so it must land as audiobook.
				{ForeignID: "hc:b1", Title: "Both Editions Book", MetadataProvider: "hardcover", MediaType: models.MediaTypeBoth, Author: author()},
			},
		}
	})

	if err := s.SyncOne(ctx, il.ID); err != nil {
		t.Fatalf("SyncOne: %v", err)
	}

	got, err := s.books.GetByForeignID(ctx, "hc:b1")
	if err != nil || got == nil {
		t.Fatalf("created book not found: %v", err)
	}
	if got.MediaType != models.MediaTypeAudiobook {
		t.Fatalf("media type = %q, want audiobook (list override of the Hardcover 'both')", got.MediaType)
	}
}

// TestSyncOne_ListMediaTypeUnsetKeepsDerived confirms an unset list MediaType
// leaves the source-derived media type untouched (backwards compatible).
func TestSyncOne_ListMediaTypeUnsetKeepsDerived(t *testing.T) {
	s, repo := newTestSyncer(t)
	ctx := context.Background()

	il := testImportList("Default", "hardcover", true)
	// MediaType deliberately left empty.
	if err := repo.Create(ctx, &il); err != nil {
		t.Fatalf("seed list: %v", err)
	}
	s.WithClientFactory(func(string) hardcoverClient {
		return &fakeHardcoverClient{
			lists: []hardcover.HCList{{ID: 9, Slug: il.URL, Name: il.Name}},
			books: []models.Book{
				{ForeignID: "hc:b2", Title: "Audio Only", MetadataProvider: "hardcover", MediaType: models.MediaTypeAudiobook,
					Author: &models.Author{ForeignID: "hc:a2", Name: "Author Two", MetadataProvider: "hardcover"}},
			},
		}
	})

	if err := s.SyncOne(ctx, il.ID); err != nil {
		t.Fatalf("SyncOne: %v", err)
	}
	got, err := s.books.GetByForeignID(ctx, "hc:b2")
	if err != nil || got == nil {
		t.Fatalf("created book not found: %v", err)
	}
	if got.MediaType != models.MediaTypeAudiobook {
		t.Fatalf("media type = %q, want audiobook (source-derived, unchanged)", got.MediaType)
	}
}

// TestSyncOne_NewAuthorPinnedToMonitorModeNone is the #1290 regression: a list
// whose book belongs to a brand-new author must create that author with
// MonitorMode == "none", not the zero value "". An empty MonitorMode is treated
// as "all" by shouldMonitorBookForAuthor, which makes the scheduler's later
// catalogue-discovery pass auto-want the author's entire back-catalogue. Only
// the single listed book may end up monitored + wanted.
func TestSyncOne_NewAuthorPinnedToMonitorModeNone(t *testing.T) {
	s, repo := newTestSyncer(t)
	ctx := context.Background()

	il := testImportList("HC", "hardcover", true)
	if err := repo.Create(ctx, &il); err != nil {
		t.Fatalf("seed list: %v", err)
	}

	newAuthor := func() *models.Author {
		return &models.Author{ForeignID: "hc:newauthor", Name: "Brand New Author", MetadataProvider: "hardcover"}
	}
	s.WithClientFactory(func(string) hardcoverClient {
		return &fakeHardcoverClient{
			lists: []hardcover.HCList{{ID: 7, Slug: il.URL, Name: il.Name}},
			books: []models.Book{
				{ForeignID: "hc:listed", Title: "The Listed Book", MetadataProvider: "hardcover", Author: newAuthor()},
			},
		}
	})

	if err := s.SyncOne(ctx, il.ID); err != nil {
		t.Fatalf("SyncOne: %v", err)
	}

	created, err := s.authors.GetByAnyForeignID(ctx, "hc:newauthor")
	if err != nil {
		t.Fatalf("GetByAnyForeignID: %v", err)
	}
	if created == nil {
		t.Fatal("expected the new author to be created")
	}
	// Author stays monitored (so metadata refresh keeps running)...
	if !created.Monitored {
		t.Errorf("new author Monitored = false, want true")
	}
	// ...but MonitorMode must be "none" so the back-catalogue is never auto-wanted.
	if created.MonitorMode != models.AuthorMonitorModeNone {
		t.Errorf("new author MonitorMode = %q, want %q (#1290)", created.MonitorMode, models.AuthorMonitorModeNone)
	}

	// The single listed book is monitored + wanted.
	books, err := s.books.ListByAuthor(ctx, created.ID)
	if err != nil {
		t.Fatalf("ListByAuthor: %v", err)
	}
	if len(books) != 1 {
		t.Fatalf("expected exactly the 1 listed book, got %d: %+v", len(books), books)
	}
	if books[0].Title != "The Listed Book" {
		t.Errorf("listed book title = %q, want %q", books[0].Title, "The Listed Book")
	}
	if !books[0].Monitored {
		t.Errorf("listed book Monitored = false, want true")
	}
	if books[0].Status != models.BookStatusWanted {
		t.Errorf("listed book Status = %q, want %q", books[0].Status, models.BookStatusWanted)
	}
}

// TestSyncOne_NewAuthorDefaultsMetadataProfile is the #1736 regression:
// ensureAuthor built the author struct by hand and called CreateForUser
// directly, bypassing applyAuthorCreateOptions (internal/api/authors.go) —
// the only place a metadata profile was ever defaulted. Every other
// author-create path falls back to DefaultMetadataProfileID when the caller
// sends none; list-sync-created authors must do the same instead of landing
// with a permanently unset profile.
func TestSyncOne_NewAuthorDefaultsMetadataProfile(t *testing.T) {
	s, repo := newTestSyncer(t)
	ctx := context.Background()

	il := testImportList("HC", "hardcover", true)
	if err := repo.Create(ctx, &il); err != nil {
		t.Fatalf("seed list: %v", err)
	}

	newAuthor := func() *models.Author {
		return &models.Author{ForeignID: "hc:newauthor", Name: "Brand New Author", MetadataProvider: "hardcover"}
	}
	s.WithClientFactory(func(string) hardcoverClient {
		return &fakeHardcoverClient{
			lists: []hardcover.HCList{{ID: 7, Slug: il.URL, Name: il.Name}},
			books: []models.Book{
				{ForeignID: "hc:listed", Title: "The Listed Book", MetadataProvider: "hardcover", Author: newAuthor()},
			},
		}
	})

	if err := s.SyncOne(ctx, il.ID); err != nil {
		t.Fatalf("SyncOne: %v", err)
	}

	created, err := s.authors.GetByAnyForeignID(ctx, "hc:newauthor")
	if err != nil {
		t.Fatalf("GetByAnyForeignID: %v", err)
	}
	if created == nil {
		t.Fatal("expected the new author to be created")
	}
	if created.MetadataProfileID == nil {
		t.Fatal("new author MetadataProfileID is nil, want DefaultMetadataProfileID (#1736)")
	}
	if *created.MetadataProfileID != models.DefaultMetadataProfileID {
		t.Errorf("new author MetadataProfileID = %d, want %d", *created.MetadataProfileID, models.DefaultMetadataProfileID)
	}
}

// newTestSyncerWithQualityProfiles returns a syncer wired against a real
// in-memory DB and hands the test the QualityProfileRepo so it can seed a
// profile and resolve the one an author ends up with through the same helper
// the grab and interactive-search paths use.
func newTestSyncerWithQualityProfiles(t *testing.T) (*ListSyncer, *db.ImportListRepo, *db.QualityProfileRepo) {
	t.Helper()
	database, err := db.OpenMemory()
	if err != nil {
		t.Fatalf("open memory db: %v", err)
	}
	t.Cleanup(func() { database.Close() })
	importLists := db.NewImportListRepo(database)
	authors := db.NewAuthorRepo(database)
	books := db.NewBookRepo(database)
	profiles := db.NewQualityProfileRepo(database)
	return New(importLists, authors, books), importLists, profiles
}

// seedQualityProfile inserts a named profile and returns it.
func seedQualityProfile(t *testing.T, ctx context.Context, profiles *db.QualityProfileRepo, name, cutoff string) *models.QualityProfile {
	t.Helper()
	p := &models.QualityProfile{
		Name:   name,
		Cutoff: cutoff,
		Items:  []models.QualityItem{{Quality: cutoff, Allowed: true}},
	}
	if err := profiles.Create(ctx, p); err != nil {
		t.Fatalf("seed quality profile %q: %v", name, err)
	}
	return p
}

// TestSyncOne_NewAuthorInheritsListQualityProfile is the #1781 regression: an
// import list carries a quality profile (the per-list picker writes
// ImportList.QualityProfileID), but ensureAuthor never passed it on. Every
// list-synced author landed with a NULL quality_profile_id, so
// ResolveAuthorQualityProfile returned nil and the format filter the user
// configured on the list was silently inert for that author's whole catalogue.
func TestSyncOne_NewAuthorInheritsListQualityProfile(t *testing.T) {
	s, repo, profiles := newTestSyncerWithQualityProfiles(t)
	ctx := context.Background()

	audio := seedQualityProfile(t, ctx, profiles, "Audiobooks only", "audiobook")

	il := testImportList("HC audiobooks", "hardcover", true)
	il.QualityProfileID = &audio.ID
	if err := repo.Create(ctx, &il); err != nil {
		t.Fatalf("seed list: %v", err)
	}
	s.WithClientFactory(func(string) hardcoverClient {
		return &fakeHardcoverClient{
			lists: []hardcover.HCList{{ID: 11, Slug: il.URL, Name: il.Name}},
			books: []models.Book{
				{ForeignID: "hc:qp-book", Title: "Filtered Book", MetadataProvider: "hardcover",
					Author: &models.Author{ForeignID: "hc:qp-author", Name: "Filtered Author", MetadataProvider: "hardcover"}},
			},
		}
	})

	if err := s.SyncOne(ctx, il.ID); err != nil {
		t.Fatalf("SyncOne: %v", err)
	}

	created, err := s.authors.GetByAnyForeignID(ctx, "hc:qp-author")
	if err != nil {
		t.Fatalf("GetByAnyForeignID: %v", err)
	}
	if created == nil {
		t.Fatal("expected the new author to be created")
	}
	if created.QualityProfileID == nil {
		t.Fatal("new author QualityProfileID is nil, want the list's profile (#1781)")
	}
	if *created.QualityProfileID != audio.ID {
		t.Errorf("new author QualityProfileID = %d, want %d (the list's profile)", *created.QualityProfileID, audio.ID)
	}
	// The column being set is only half the point: the grab and interactive
	// search paths both read it back through ResolveAuthorQualityProfile, so
	// assert the author now resolves to a real profile instead of "no filter".
	resolved := db.ResolveAuthorQualityProfile(ctx, profiles, created)
	if resolved == nil {
		t.Fatal("ResolveAuthorQualityProfile returned nil for a list-synced author, want the list's profile")
	}
	if resolved.ID != audio.ID || resolved.Name != audio.Name {
		t.Errorf("resolved profile = %d/%q, want %d/%q", resolved.ID, resolved.Name, audio.ID, audio.Name)
	}
}

// TestSyncOne_ListWithoutQualityProfileLeavesAuthorUnfiltered pins the
// deliberate non-behaviour: a list with no quality profile configured must
// leave the author's profile unset rather than falling back to the seeded
// id=1 row. Migration 025 backfills owner_user_id=1 onto pre-multi-user
// profiles, so that row is one specific user's private profile on a tenanted
// install and must never be handed to another user's list-synced authors.
func TestSyncOne_ListWithoutQualityProfileLeavesAuthorUnfiltered(t *testing.T) {
	s, repo, profiles := newTestSyncerWithQualityProfiles(t)
	ctx := context.Background()

	il := testImportList("HC unconfigured", "hardcover", true)
	// QualityProfileID deliberately left nil.
	if err := repo.Create(ctx, &il); err != nil {
		t.Fatalf("seed list: %v", err)
	}
	s.WithClientFactory(func(string) hardcoverClient {
		return &fakeHardcoverClient{
			lists: []hardcover.HCList{{ID: 12, Slug: il.URL, Name: il.Name}},
			books: []models.Book{
				{ForeignID: "hc:noqp-book", Title: "Unfiltered Book", MetadataProvider: "hardcover",
					Author: &models.Author{ForeignID: "hc:noqp-author", Name: "Unfiltered Author", MetadataProvider: "hardcover"}},
			},
		}
	})

	if err := s.SyncOne(ctx, il.ID); err != nil {
		t.Fatalf("SyncOne: %v", err)
	}

	created, err := s.authors.GetByAnyForeignID(ctx, "hc:noqp-author")
	if err != nil || created == nil {
		t.Fatalf("created author not found: %v", err)
	}
	if created.QualityProfileID != nil {
		t.Errorf("author QualityProfileID = %d, want nil (list configured none)", *created.QualityProfileID)
	}
	if got := db.ResolveAuthorQualityProfile(ctx, profiles, created); got != nil {
		t.Errorf("ResolveAuthorQualityProfile = %+v, want nil (no filter)", got)
	}
}

// TestSyncOne_ExistingAuthorKeepsItsQualityProfile guards the create-only rule:
// re-syncing a list must not overwrite the profile on an author that already
// exists, whether the user set it by hand or another list did.
func TestSyncOne_ExistingAuthorKeepsItsQualityProfile(t *testing.T) {
	s, repo, profiles := newTestSyncerWithQualityProfiles(t)
	ctx := context.Background()

	chosen := seedQualityProfile(t, ctx, profiles, "Author's own", "epub")
	listProfile := seedQualityProfile(t, ctx, profiles, "List default", "audiobook")

	existing := &models.Author{
		ForeignID:        "hc:existing-author",
		Name:             "Existing Author",
		SortName:         "Author, Existing",
		MetadataProvider: "hardcover",
		QualityProfileID: &chosen.ID,
	}
	if err := s.authors.Create(ctx, existing); err != nil {
		t.Fatalf("seed author: %v", err)
	}

	il := testImportList("HC resync", "hardcover", true)
	il.QualityProfileID = &listProfile.ID
	if err := repo.Create(ctx, &il); err != nil {
		t.Fatalf("seed list: %v", err)
	}
	s.WithClientFactory(func(string) hardcoverClient {
		return &fakeHardcoverClient{
			lists: []hardcover.HCList{{ID: 13, Slug: il.URL, Name: il.Name}},
			books: []models.Book{
				{ForeignID: "hc:resync-book", Title: "Resynced Book", MetadataProvider: "hardcover",
					Author: &models.Author{ForeignID: "hc:existing-author", Name: "Existing Author", MetadataProvider: "hardcover"}},
			},
		}
	})

	if err := s.SyncOne(ctx, il.ID); err != nil {
		t.Fatalf("SyncOne: %v", err)
	}

	got, err := s.authors.GetByAnyForeignID(ctx, "hc:existing-author")
	if err != nil || got == nil {
		t.Fatalf("existing author not found: %v", err)
	}
	if got.QualityProfileID == nil || *got.QualityProfileID != chosen.ID {
		t.Errorf("existing author QualityProfileID = %v, want %d (unchanged)", got.QualityProfileID, chosen.ID)
	}
}

// TestSyncOne_StampsListOwner is the hoxtonia-report regression: under
// multi-user tenancy a list with an owner_user_id must stamp that owner onto
// every book AND author it creates, so scheduler-synced content is scoped to
// that user instead of landing NULL-owned (globally visible to everyone).
func TestSyncOne_StampsListOwner(t *testing.T) {
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

	owner, err := users.Create(ctx, "alice", "hash")
	if err != nil {
		t.Fatalf("seed user: %v", err)
	}

	s := New(importLists, authors, books)
	il := testImportList("Alice's Want to Read", "hardcover", true)
	il.OwnerUserID = &owner.ID
	if err := importLists.Create(ctx, &il); err != nil {
		t.Fatalf("seed list: %v", err)
	}
	s.WithClientFactory(func(string) hardcoverClient {
		return &fakeHardcoverClient{
			lists: []hardcover.HCList{{ID: 3, Slug: il.URL, Name: il.Name}},
			books: []models.Book{
				{ForeignID: "hc:owned", Title: "Owned Book", MetadataProvider: "hardcover",
					Author: &models.Author{ForeignID: "hc:ownedauthor", Name: "Owned Author", MetadataProvider: "hardcover"}},
			},
		}
	})

	if err := s.SyncOne(ctx, il.ID); err != nil {
		t.Fatalf("SyncOne: %v", err)
	}

	gotBook, err := books.GetByForeignID(ctx, "hc:owned")
	if err != nil || gotBook == nil {
		t.Fatalf("created book not found: %v", err)
	}
	if gotBook.OwnerUserID != owner.ID {
		t.Errorf("book OwnerUserID = %d, want %d (list owner)", gotBook.OwnerUserID, owner.ID)
	}
	gotAuthor, err := authors.GetByAnyForeignID(ctx, "hc:ownedauthor")
	if err != nil || gotAuthor == nil {
		t.Fatalf("created author not found: %v", err)
	}
	if gotAuthor.OwnerUserID != owner.ID {
		t.Errorf("author OwnerUserID = %d, want %d (list owner)", gotAuthor.OwnerUserID, owner.ID)
	}
}

// TestSyncOne_GlobalListLeavesContentUnowned confirms the back-compat default:
// a list with no owner (nil) creates NULL-owned (owner id 0) content, matching
// the pre-fix behaviour of a shared/global shelf.
func TestSyncOne_GlobalListLeavesContentUnowned(t *testing.T) {
	s, repo := newTestSyncer(t)
	ctx := context.Background()

	il := testImportList("Shared shelf", "hardcover", true)
	// OwnerUserID deliberately left nil → global.
	if err := repo.Create(ctx, &il); err != nil {
		t.Fatalf("seed list: %v", err)
	}
	s.WithClientFactory(func(string) hardcoverClient {
		return &fakeHardcoverClient{
			lists: []hardcover.HCList{{ID: 4, Slug: il.URL, Name: il.Name}},
			books: []models.Book{
				{ForeignID: "hc:global", Title: "Global Book", MetadataProvider: "hardcover",
					Author: &models.Author{ForeignID: "hc:globalauthor", Name: "Global Author", MetadataProvider: "hardcover"}},
			},
		}
	})

	if err := s.SyncOne(ctx, il.ID); err != nil {
		t.Fatalf("SyncOne: %v", err)
	}

	gotBook, err := s.books.GetByForeignID(ctx, "hc:global")
	if err != nil || gotBook == nil {
		t.Fatalf("created book not found: %v", err)
	}
	if gotBook.OwnerUserID != 0 {
		t.Errorf("book OwnerUserID = %d, want 0 (global)", gotBook.OwnerUserID)
	}
	gotAuthor, err := s.authors.GetByAnyForeignID(ctx, "hc:globalauthor")
	if err != nil || gotAuthor == nil {
		t.Fatalf("created author not found: %v", err)
	}
	if gotAuthor.OwnerUserID != 0 {
		t.Errorf("author OwnerUserID = %d, want 0 (global)", gotAuthor.OwnerUserID)
	}
}

func TestSortName(t *testing.T) {
	tests := []struct {
		in, want string
	}{
		{"", ""},
		{"Cher", "Cher"},            // single token → unchanged
		{"Andy Weir", "Weir, Andy"}, // two tokens
		{"Ursula K. Le Guin", "Guin, Ursula K. Le"}, // 4 tokens: last → front
		{"  Andy   Weir  ", "Weir, Andy"},           // whitespace normalised
	}
	for _, tt := range tests {
		if got := sortName(tt.in); got != tt.want {
			t.Errorf("sortName(%q) = %q, want %q", tt.in, got, tt.want)
		}
	}
}

// Compile-time check: *ListSyncer satisfies the HCListSyncer interface
// (the whole point of the interface's existence — keeps the scheduler
// from needing to import this package).
func TestHCListSyncerInterfaceSatisfied(t *testing.T) {
	var _ HCListSyncer = (*ListSyncer)(nil)
}

// RunSync is a fire-and-forget wrapper around Sync. With no enabled lists
// it must return cleanly (no panic, no error to observe).
func TestRunSync_NoEnabledLists(t *testing.T) {
	s, _ := newTestSyncer(t)
	// Should not panic; errors are swallowed inside RunSync by design.
	s.RunSync(context.Background())
}

// testImportList builds a models.ImportList pointer suitable for seeding
// via ImportListRepo.Create. The repo's Create only requires the fields
// set here.
func testImportList(name, typ string, enabled bool) models.ImportList {
	return models.ImportList{
		Name:    name,
		Type:    typ,
		URL:     "some-slug",
		APIKey:  "irrelevant-for-these-tests",
		Enabled: enabled,
	}
}

// TestSyncOne_ErrNotFound verifies that SyncOne returns ErrNotFound when the
// requested list ID does not exist in the database.
func TestSyncOne_ErrNotFound(t *testing.T) {
	s, _ := newTestSyncer(t)
	err := s.SyncOne(context.Background(), 99999)
	if !errors.Is(err, ErrNotFound) {
		t.Errorf("SyncOne(missing id): want ErrNotFound, got %v", err)
	}
}

// TestSyncOne_ErrWrongType verifies that SyncOne returns ErrWrongType when the
// list exists but has a type other than "hardcover".
func TestSyncOne_ErrWrongType(t *testing.T) {
	s, repo := newTestSyncer(t)
	ctx := context.Background()

	il := testImportList("My Goodreads", "goodreads", true)
	if err := repo.Create(ctx, &il); err != nil {
		t.Fatalf("seed: %v", err)
	}
	// repo.Create sets il.ID via LastInsertId
	err := s.SyncOne(ctx, il.ID)
	if !errors.Is(err, ErrWrongType) {
		t.Errorf("SyncOne(goodreads list): want ErrWrongType, got %v", err)
	}
}

func TestSyncOne_ErrDisabled(t *testing.T) {
	s, repo := newTestSyncer(t)
	ctx := context.Background()

	il := testImportList("Disabled", "hardcover", false)
	if err := repo.Create(ctx, &il); err != nil {
		t.Fatalf("seed: %v", err)
	}
	err := s.SyncOne(ctx, il.ID)
	if !errors.Is(err, ErrDisabled) {
		t.Errorf("SyncOne(disabled list): want ErrDisabled, got %v", err)
	}
}

func TestSyncOne_UsesGlobalTokenWhenListHasNoOverride(t *testing.T) {
	s, repo := newTestSyncer(t)
	ctx := context.Background()

	il := testImportList("Global", "hardcover", true)
	il.APIKey = ""
	if err := repo.Create(ctx, &il); err != nil {
		t.Fatalf("seed: %v", err)
	}
	s.WithTokenSource(func(context.Context) string { return "global-token" })
	var gotToken string
	s.WithClientFactory(func(token string) hardcoverClient {
		gotToken = token
		return &fakeHardcoverClient{lists: []hardcover.HCList{{ID: 12, Slug: il.URL, Name: il.Name}}}
	})

	if err := s.SyncOne(ctx, il.ID); err != nil {
		t.Fatalf("SyncOne: %v", err)
	}
	if gotToken != "global-token" {
		t.Fatalf("token = %q, want global-token", gotToken)
	}
}

func TestSyncOne_PerListTokenOverridesGlobalToken(t *testing.T) {
	s, repo := newTestSyncer(t)
	ctx := context.Background()

	il := testImportList("Override", "hardcover", true)
	il.APIKey = "per-list-token"
	if err := repo.Create(ctx, &il); err != nil {
		t.Fatalf("seed: %v", err)
	}
	s.WithTokenSource(func(context.Context) string { return "global-token" })
	var gotToken string
	s.WithClientFactory(func(token string) hardcoverClient {
		gotToken = token
		return &fakeHardcoverClient{lists: []hardcover.HCList{{ID: 24, Slug: il.URL, Name: il.Name}}}
	})

	if err := s.SyncOne(ctx, il.ID); err != nil {
		t.Fatalf("SyncOne: %v", err)
	}
	if gotToken != "per-list-token" {
		t.Fatalf("token = %q, want per-list-token", gotToken)
	}
}

func TestSyncOne_ReusesAuthorByAlternateIdentifier(t *testing.T) {
	s, repo := newTestSyncer(t)
	ctx := context.Background()

	existing := &models.Author{
		ForeignID:        "OL-AUTHOR-X",
		Name:             "Author X",
		SortName:         "X, Author",
		MetadataProvider: "openlibrary",
		Monitored:        true,
	}
	if err := s.authors.Create(ctx, existing); err != nil {
		t.Fatalf("seed author: %v", err)
	}
	if err := s.authors.UpsertAuthorIdentifier(ctx, existing.ID, "hc:author-x"); err != nil {
		t.Fatalf("seed author identifier: %v", err)
	}
	il := testImportList("Alt Author", "hardcover", true)
	if err := repo.Create(ctx, &il); err != nil {
		t.Fatalf("seed list: %v", err)
	}
	book := bookWithSeriesRef("hc:book-x", "Book X", nil)
	s.WithClientFactory(func(string) hardcoverClient {
		return &fakeHardcoverClient{
			lists: []hardcover.HCList{{ID: 12, Slug: il.URL, Name: il.Name}},
			books: []models.Book{book},
		}
	})

	if err := s.SyncOne(ctx, il.ID); err != nil {
		t.Fatalf("SyncOne: %v", err)
	}
	authors, err := s.authors.List(ctx)
	if err != nil {
		t.Fatalf("List authors: %v", err)
	}
	if len(authors) != 1 {
		t.Fatalf("authors = %d, want existing author reused", len(authors))
	}
	imported, err := s.books.GetByForeignID(ctx, "hc:book-x")
	if err != nil || imported == nil {
		t.Fatalf("imported book = %+v err=%v, want persisted", imported, err)
	}
	if imported.AuthorID != existing.ID {
		t.Fatalf("book author_id = %d, want existing author %d", imported.AuthorID, existing.ID)
	}
}

func TestSyncOne_ErrMissingToken(t *testing.T) {
	s, repo := newTestSyncer(t)
	ctx := context.Background()

	il := testImportList("No token", "hardcover", true)
	il.APIKey = ""
	if err := repo.Create(ctx, &il); err != nil {
		t.Fatalf("seed: %v", err)
	}

	err := s.SyncOne(ctx, il.ID)
	if !errors.Is(err, ErrMissingToken) {
		t.Errorf("SyncOne(no token): want ErrMissingToken, got %v", err)
	}
}

type fakeHardcoverClient struct {
	lists    []hardcover.HCList
	books    []models.Book
	editions []models.Edition

	// getEditionsCalls counts per-book edition fan-out. List sync must never
	// call GetEditions (#1694); tests assert this stays zero.
	getEditionsCalls int
}

func (f *fakeHardcoverClient) GetUserLists(context.Context) ([]hardcover.HCList, error) {
	return f.lists, nil
}

func (f *fakeHardcoverClient) GetListBooks(context.Context, int) ([]models.Book, error) {
	return f.books, nil
}

func (f *fakeHardcoverClient) GetEditions(context.Context, string) ([]models.Edition, error) {
	f.getEditionsCalls++
	return f.editions, nil
}

// newTestSyncerWithSeries returns a syncer wired against a real in-memory DB
// and gives the test direct access to the SeriesRepo so it can verify links
// were actually persisted.
func newTestSyncerWithSeries(t *testing.T) (*ListSyncer, *db.ImportListRepo, *db.SeriesRepo) {
	t.Helper()
	database, err := db.OpenMemory()
	if err != nil {
		t.Fatalf("open memory db: %v", err)
	}
	t.Cleanup(func() { database.Close() })
	importLists := db.NewImportListRepo(database)
	authors := db.NewAuthorRepo(database)
	books := db.NewBookRepo(database)
	series := db.NewSeriesRepo(database)
	s := New(importLists, authors, books).WithSeriesRepo(series)
	return s, importLists, series
}

func bookWithSeriesRef(foreignID, title string, refs []models.SeriesRef) models.Book {
	return models.Book{
		ForeignID:        foreignID,
		Title:            title,
		SortTitle:        title,
		MetadataProvider: "hardcover",
		Author: &models.Author{
			ForeignID:        "hc:author-x",
			Name:             "Author X",
			SortName:         "X, Author",
			MetadataProvider: "hardcover",
		},
		SeriesRefs: refs,
	}
}

// TestSyncOne_LinksSeriesRefsAfterBookImport is the issue #805 happy path:
// books that arrive with a populated SeriesRefs slice must be linked through
// the SeriesRepo so the import doesn't quietly lose series membership.
func TestSyncOne_LinksSeriesRefsAfterBookImport(t *testing.T) {
	s, repo, series := newTestSyncerWithSeries(t)
	ctx := context.Background()

	il := testImportList("With Series", "hardcover", true)
	if err := repo.Create(ctx, &il); err != nil {
		t.Fatalf("seed: %v", err)
	}

	book := bookWithSeriesRef("hc:dune", "Dune", []models.SeriesRef{{
		ForeignID: "hc-series:17",
		Title:     "Dune Chronicles",
		Position:  "1",
		Primary:   true,
	}})
	s.WithClientFactory(func(string) hardcoverClient {
		return &fakeHardcoverClient{
			lists: []hardcover.HCList{{ID: 12, Slug: il.URL, Name: il.Name}},
			books: []models.Book{book},
		}
	})

	if err := s.SyncOne(ctx, il.ID); err != nil {
		t.Fatalf("SyncOne: %v", err)
	}

	persisted, err := series.GetByForeignID(ctx, "hc-series:17")
	if err != nil {
		t.Fatalf("GetByForeignID: %v", err)
	}
	if persisted == nil {
		t.Fatal("series was not created during sync")
		return
	}
	booksInSeries, err := series.ListBooksInSeries(ctx, persisted.ID)
	if err != nil {
		t.Fatalf("ListBooksInSeries: %v", err)
	}
	if len(booksInSeries) != 1 || booksInSeries[0].ForeignID != "hc:dune" {
		t.Fatalf("series should contain the imported book, got %+v", booksInSeries)
	}
}

// TestSyncOne_SeriesLinkErrorDoesNotBlockImport guarantees the best-effort
// contract: when the SeriesRepo errors out, the book is still imported and
// the sync does not fail the whole list. Regression guard for the warning
// path.
func TestSyncOne_SeriesLinkErrorDoesNotBlockImport(t *testing.T) {
	s, repo, _ := newTestSyncerWithSeries(t)
	ctx := context.Background()

	stub := &erroringSeriesRepo{}
	s.series = stub

	il := testImportList("Series Error", "hardcover", true)
	if err := repo.Create(ctx, &il); err != nil {
		t.Fatalf("seed: %v", err)
	}

	book := bookWithSeriesRef("hc:dune", "Dune", []models.SeriesRef{{
		ForeignID: "hc-series:17",
		Title:     "Dune Chronicles",
		Position:  "1",
		Primary:   true,
	}})
	s.WithClientFactory(func(string) hardcoverClient {
		return &fakeHardcoverClient{
			lists: []hardcover.HCList{{ID: 12, Slug: il.URL, Name: il.Name}},
			books: []models.Book{book},
		}
	})

	if err := s.SyncOne(ctx, il.ID); err != nil {
		t.Fatalf("SyncOne should succeed even when series linking errors: %v", err)
	}
	if stub.upsertCalls == 0 {
		t.Errorf("expected CreateOrGet to be attempted, got 0 calls")
	}

	imported, err := s.books.GetByForeignID(ctx, "hc:dune")
	if err != nil || imported == nil {
		t.Fatalf("book should still be imported despite series link failure: %v, %v", imported, err)
	}
}

// erroringSeriesRepo always fails CreateOrGet so we can prove the syncer
// swallows the error.
type erroringSeriesRepo struct {
	upsertCalls int
	linkCalls   int
}

func (e *erroringSeriesRepo) CreateOrGet(context.Context, *models.Series) error {
	e.upsertCalls++
	return errors.New("simulated upsert failure")
}

func (e *erroringSeriesRepo) LinkBook(context.Context, int64, int64, string, bool) error {
	e.linkCalls++
	return errors.New("should not be called when upsert fails")
}

// TestSyncOne_NoSeriesRepo_NoSeriesLinkAttempted protects the optional
// nature of the repo: the syncer must remain functional when WithSeriesRepo
// was never called (e.g. older deployments wired before #805 landed).
func TestSyncOne_NoSeriesRepo_NoSeriesLinkAttempted(t *testing.T) {
	s, _ := newTestSyncer(t)
	if s.series != nil {
		t.Fatalf("default syncer should have no series repo, got %T", s.series)
	}

	il := testImportList("No Series Repo", "hardcover", true)
	importLists := s.importLists
	ctx := context.Background()
	if err := importLists.Create(ctx, &il); err != nil {
		t.Fatalf("seed: %v", err)
	}

	book := bookWithSeriesRef("hc:dune", "Dune", []models.SeriesRef{{
		ForeignID: "hc-series:17",
		Title:     "Dune Chronicles",
		Position:  "1",
		Primary:   true,
	}})
	s.WithClientFactory(func(string) hardcoverClient {
		return &fakeHardcoverClient{
			lists: []hardcover.HCList{{ID: 12, Slug: il.URL, Name: il.Name}},
			books: []models.Book{book},
		}
	})

	if err := s.SyncOne(ctx, il.ID); err != nil {
		t.Fatalf("SyncOne: %v", err)
	}
	imported, err := s.books.GetByForeignID(ctx, "hc:dune")
	if err != nil || imported == nil {
		t.Fatalf("book should be imported: %v, %v", imported, err)
	}
}

// TestSync_DoesNotFanOutToEditions is the #1694 regression guard: list sync
// used to call GetEditions once per newly imported book — a fully paginated
// GraphQL query per book — which made a first sync of a large list
// impossible to finish inside the request deadline. The edition data it was
// after now arrives inline on the list response instead.
func TestSync_DoesNotFanOutToEditions(t *testing.T) {
	database, err := db.OpenMemory()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { database.Close() })
	ctx := context.Background()
	importLists := db.NewImportListRepo(database)
	authors := db.NewAuthorRepo(database)
	books := db.NewBookRepo(database)
	editions := db.NewEditionRepo(database)

	audioASIN := "B123LISTEN"
	client := &fakeHardcoverClient{
		lists: []hardcover.HCList{{ID: 10, Slug: "want-to-read", Name: "Want to Read"}},
		books: []models.Book{{
			ForeignID:        "hc:list-book",
			Title:            "List Book",
			SortTitle:        "List Book",
			MetadataProvider: "hardcover",
			MediaType:        models.MediaTypeAudiobook,
			Genres:           []string{},
			Author: &models.Author{
				ForeignID:        "hc:list-author",
				Name:             "List Author",
				SortName:         "Author, List",
				MetadataProvider: "hardcover",
			},
		}},
		editions: []models.Edition{{
			ForeignID: "hc:list-book-audio",
			Title:     "List Book",
			ASIN:      &audioASIN,
			Format:    "Audiobook",
			Monitored: true,
		}},
	}
	syncer := New(importLists, authors, books).
		WithClientFactory(func(string) hardcoverClient { return client })
	il := testImportList("Want", "hardcover", true)
	il.URL = "want-to-read"
	if err := importLists.Create(ctx, &il); err != nil {
		t.Fatal(err)
	}

	if err := syncer.Sync(ctx); err != nil {
		t.Fatal(err)
	}
	book, err := books.GetByForeignID(ctx, "hc:list-book")
	if err != nil {
		t.Fatal(err)
	}
	if book == nil {
		t.Fatal("book was not created")
	}
	// #1694: list sync must never fan out to a per-book edition fetch. That
	// call is what made a large first sync impossible to finish inside the
	// request deadline — one paginated GraphQL round-trip per new book.
	if client.getEditionsCalls != 0 {
		t.Fatalf("GetEditions called %d times during list sync, want 0 (#1694 fan-out must not return)", client.getEditionsCalls)
	}
	// The ASIN now rides in on the list response itself (see the metadata
	// package's toBook), so nothing here should have written editions.
	got, err := editions.ListByBook(ctx, book.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 0 {
		t.Fatalf("list sync stored %d editions, want 0 — edition persistence belongs to the on-demand hydration paths", len(got))
	}
}

// newHydrationSyncer wires a syncer against a real in-memory DB and a stub
// client whose list serves one book carrying the given Hardcover-derived
// media type and an audiobook ASIN — the shape the list response itself now
// delivers since the per-book edition fan-out was removed (#1694). The
// client is returned so tests can assert GetEditions is never called.
func newHydrationSyncer(t *testing.T, listMediaType, derivedMediaType string) (*ListSyncer, *db.BookRepo, *fakeHardcoverClient, context.Context) {
	t.Helper()
	database, err := db.OpenMemory()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { database.Close() })
	ctx := context.Background()
	importLists := db.NewImportListRepo(database)
	authors := db.NewAuthorRepo(database)
	books := db.NewBookRepo(database)

	client := &fakeHardcoverClient{
		lists: []hardcover.HCList{{ID: 42, Slug: "pinned-list", Name: "Pinned"}},
		books: []models.Book{{
			ForeignID:        "hc:pinned-book",
			Title:            "Pinned Book",
			SortTitle:        "Pinned Book",
			MetadataProvider: "hardcover",
			MediaType:        derivedMediaType,
			// Supplied by the list response itself now, the way toBook fills
			// it from the inlined audio edition (#1694).
			ASIN:   "B123PINNED",
			Genres: []string{},
			Author: &models.Author{
				ForeignID:        "hc:pinned-author",
				Name:             "Pinned Author",
				SortName:         "Author, Pinned",
				MetadataProvider: "hardcover",
			},
		}},
	}
	syncer := New(importLists, authors, books).
		WithClientFactory(func(string) hardcoverClient { return client })
	il := testImportList("Pinned", "hardcover", true)
	il.URL = "pinned-list"
	il.MediaType = listMediaType
	if err := importLists.Create(ctx, &il); err != nil {
		t.Fatal(err)
	}
	return syncer, books, client, ctx
}

// TestSync_EbookPinnedListSurvivesAudioEditionHydration is the #1732
// regression: a list pinned to ebook creates the book as ebook, and the
// edition hydration that runs immediately after must not widen it to "both"
// just because the work has an audio edition on Hardcover (true for most
// popular titles).
func TestSync_EbookPinnedListSurvivesAudioEditionHydration(t *testing.T) {
	syncer, books, _, ctx := newHydrationSyncer(t, models.MediaTypeEbook, models.MediaTypeBoth)
	if err := syncer.Sync(ctx); err != nil {
		t.Fatal(err)
	}
	book, err := books.GetByForeignID(ctx, "hc:pinned-book")
	if err != nil || book == nil {
		t.Fatalf("created book not found: %v", err)
	}
	if book.MediaType != models.MediaTypeEbook {
		t.Fatalf("media type = %q, want ebook (list pin must survive edition hydration)", book.MediaType)
	}
	// An ebook must not carry the audio edition's ASIN either.
	if book.ASIN != "" {
		t.Fatalf("ASIN = %q, want empty on an ebook-pinned book", book.ASIN)
	}
}

// TestSync_AudiobookPinnedListKeepsASIN confirms the working half of the
// #1732 asymmetry stays working: an audiobook-pinned list stays audiobook and
// keeps the ASIN the list response supplied — without any edition fan-out.
func TestSync_AudiobookPinnedListKeepsASIN(t *testing.T) {
	syncer, books, client, ctx := newHydrationSyncer(t, models.MediaTypeAudiobook, models.MediaTypeBoth)
	if err := syncer.Sync(ctx); err != nil {
		t.Fatal(err)
	}
	book, err := books.GetByForeignID(ctx, "hc:pinned-book")
	if err != nil || book == nil {
		t.Fatalf("created book not found: %v", err)
	}
	if book.MediaType != models.MediaTypeAudiobook {
		t.Fatalf("media type = %q, want audiobook", book.MediaType)
	}
	if book.ASIN != "B123PINNED" {
		t.Fatalf("ASIN = %q, want the ASIN carried by the list response", book.ASIN)
	}
	if client.getEditionsCalls != 0 {
		t.Fatalf("GetEditions called %d times, want 0 (#1694)", client.getEditionsCalls)
	}
}

// TestSync_UnpinnedListKeepsDerivedMediaTypeAndASIN covers the unpinned case:
// with no list pin, the media type and ASIN the list response derived are
// both persisted as-is. Before #1694 this data came from a per-book edition
// fetch; it now rides in on the list query itself, so the syncer's only job
// is to not lose it.
func TestSync_UnpinnedListKeepsDerivedMediaTypeAndASIN(t *testing.T) {
	syncer, books, client, ctx := newHydrationSyncer(t, "", models.MediaTypeAudiobook)
	if err := syncer.Sync(ctx); err != nil {
		t.Fatal(err)
	}
	book, err := books.GetByForeignID(ctx, "hc:pinned-book")
	if err != nil || book == nil {
		t.Fatalf("created book not found: %v", err)
	}
	if book.MediaType != models.MediaTypeAudiobook {
		t.Fatalf("media type = %q, want audiobook (derived type must survive an unpinned list)", book.MediaType)
	}
	if book.ASIN != "B123PINNED" {
		t.Fatalf("ASIN = %q, want the ASIN carried by the list response", book.ASIN)
	}
	if client.getEditionsCalls != 0 {
		t.Fatalf("GetEditions called %d times, want 0 (#1694)", client.getEditionsCalls)
	}
}

// recordingEnricher implements bookhydrate.AudiobookEnricher and records the
// ASINs it was invoked with, mutating the book the way Audnex enrichment does
// so tests can assert the mutation is persisted.
type recordingEnricher struct {
	calls []string
	fail  bool
}

func (r *recordingEnricher) EnrichAudiobook(_ context.Context, book *models.Book) error {
	r.calls = append(r.calls, book.ASIN)
	if r.fail {
		return errors.New("audnex unavailable")
	}
	book.Narrator = "Recorded Narrator"
	return nil
}

// TestSync_EnrichesWhenListSuppliesASIN covers the #1694 Audnex re-wire: a
// list-synced book whose ASIN arrived inline must still get audiobook
// enrichment (previously triggered inside edition hydration), and the
// enriched fields must be persisted — all without any GetEditions call.
func TestSync_EnrichesWhenListSuppliesASIN(t *testing.T) {
	syncer, books, client, ctx := newHydrationSyncer(t, models.MediaTypeAudiobook, models.MediaTypeBoth)
	enricher := &recordingEnricher{}
	syncer.WithAudiobookEnricher(enricher)

	if err := syncer.Sync(ctx); err != nil {
		t.Fatal(err)
	}
	if len(enricher.calls) != 1 || enricher.calls[0] != "B123PINNED" {
		t.Fatalf("enricher calls = %v, want [B123PINNED]", enricher.calls)
	}
	book, err := books.GetByForeignID(ctx, "hc:pinned-book")
	if err != nil || book == nil {
		t.Fatalf("created book not found: %v", err)
	}
	if book.Narrator != "Recorded Narrator" {
		t.Fatalf("Narrator = %q, want the enricher's mutation persisted", book.Narrator)
	}
	if client.getEditionsCalls != 0 {
		t.Fatalf("GetEditions called %d times, want 0 — enrichment must not reintroduce the fan-out", client.getEditionsCalls)
	}
}

// TestSync_EnricherFailureDoesNotBlockImport: enrichment is best-effort; an
// Audnex outage must not fail the sync or lose the imported book.
func TestSync_EnricherFailureDoesNotBlockImport(t *testing.T) {
	syncer, books, _, ctx := newHydrationSyncer(t, models.MediaTypeAudiobook, models.MediaTypeBoth)
	syncer.WithAudiobookEnricher(&recordingEnricher{fail: true})

	if err := syncer.Sync(ctx); err != nil {
		t.Fatalf("Sync must succeed despite enricher failure, got: %v", err)
	}
	book, err := books.GetByForeignID(ctx, "hc:pinned-book")
	if err != nil || book == nil {
		t.Fatalf("book must still be imported: %v", err)
	}
	if book.ASIN != "B123PINNED" {
		t.Fatalf("ASIN = %q, want kept despite failed enrichment", book.ASIN)
	}
}

// TestSync_NoEnrichmentWithoutASIN: an ebook-pinned list clears the ASIN
// (#1732), so the enricher must never fire for it.
func TestSync_NoEnrichmentWithoutASIN(t *testing.T) {
	syncer, _, _, ctx := newHydrationSyncer(t, models.MediaTypeEbook, models.MediaTypeBoth)
	enricher := &recordingEnricher{}
	syncer.WithAudiobookEnricher(enricher)

	if err := syncer.Sync(ctx); err != nil {
		t.Fatal(err)
	}
	if len(enricher.calls) != 0 {
		t.Fatalf("enricher calls = %v, want none for an ebook-pinned book", enricher.calls)
	}
}

// blockingHardcoverClient holds GetUserLists open until release is closed, so a
// test can observe the sync mid-flight. started is closed once the background
// job has actually entered the client call.
type blockingHardcoverClient struct {
	fakeHardcoverClient
	started chan struct{}
	release chan struct{}
	once    sync.Once
}

func (b *blockingHardcoverClient) GetUserLists(ctx context.Context) ([]hardcover.HCList, error) {
	b.once.Do(func() { close(b.started) })
	select {
	case <-b.release:
	case <-ctx.Done():
		return nil, ctx.Err()
	}
	return b.fakeHardcoverClient.GetUserLists(ctx)
}

// newBlockingSyncer wires a syncer whose single enabled list stalls inside the
// Hardcover client until the returned release channel is closed.
func newBlockingSyncer(t *testing.T) (s *ListSyncer, listID int64, started, release chan struct{}) {
	t.Helper()
	s, repo := newTestSyncer(t)
	il := testImportList("Blocking", "hardcover", true)
	if err := repo.Create(context.Background(), &il); err != nil {
		t.Fatalf("seed list: %v", err)
	}
	client := &blockingHardcoverClient{
		fakeHardcoverClient: fakeHardcoverClient{
			lists: []hardcover.HCList{{ID: 7, Slug: il.URL, Name: il.Name}},
			books: []models.Book{bookWithSeriesRef("hc:blocked", "Blocked Book", nil)},
		},
		started: make(chan struct{}),
		release: make(chan struct{}),
	}
	s.WithClientFactory(func(string) hardcoverClient { return client })
	return s, il.ID, client.started, client.release
}

// waitFor polls cond until it holds or the deadline passes.
func waitFor(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s", what)
}

// TestStartOne_ReturnsWhileSyncStillRunning is the #1854 contract: the manual
// path must not wait for the sync. StartOne returns while the Hardcover client
// is still blocked, and the polled progress reports the run as in flight.
func TestStartOne_ReturnsWhileSyncStillRunning(t *testing.T) {
	s, listID, started, release := newBlockingSyncer(t)

	done := make(chan error, 1)
	go func() { done <- s.StartOne(context.Background(), listID) }()

	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("StartOne: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("StartOne did not return while the sync was still running")
	}

	<-started
	p := s.Progress()
	if !p.Running || p.ListID != listID || p.Trigger != TriggerManual {
		t.Fatalf("progress mid-flight = %+v, want running manual sync of list %d", p, listID)
	}

	close(release)
	waitFor(t, "sync to finish", func() bool { return !s.Progress().Running })

	final := s.Progress()
	if final.FinishedAt == nil {
		t.Error("finished progress must carry FinishedAt")
	}
	if final.Error != "" {
		t.Errorf("unexpected sync error: %s", final.Error)
	}
	if final.Stats.Imported != 1 || final.Stats.Total != 1 {
		t.Errorf("stats = %+v, want 1 book imported of 1", final.Stats)
	}
	if book, err := s.books.GetByForeignID(context.Background(), "hc:blocked"); err != nil || book == nil {
		t.Fatalf("background sync did not import the book: %+v err=%v", book, err)
	}
}

// TestStartOne_SingleFlight verifies a second manual start is rejected while
// one is in flight, and that the scheduled Sync skips instead of double-walking
// the same shelf.
func TestStartOne_SingleFlight(t *testing.T) {
	s, listID, started, release := newBlockingSyncer(t)
	defer close(release)

	if err := s.StartOne(context.Background(), listID); err != nil {
		t.Fatalf("first StartOne: %v", err)
	}
	<-started

	if err := s.StartOne(context.Background(), listID); !errors.Is(err, ErrSyncAlreadyRunning) {
		t.Errorf("second StartOne: want ErrSyncAlreadyRunning, got %v", err)
	}
	if err := s.SyncOne(context.Background(), listID); !errors.Is(err, ErrSyncAlreadyRunning) {
		t.Errorf("SyncOne during a run: want ErrSyncAlreadyRunning, got %v", err)
	}
	// The scheduled path skips silently rather than erroring.
	if err := s.Sync(context.Background()); err != nil {
		t.Errorf("scheduled Sync during a manual run: want nil (skipped), got %v", err)
	}
}

// TestStartOne_ValidationErrorsAreSynchronous confirms the preconditions the
// endpoint maps to 4xx are still checked before anything is launched, so a bad
// request never turns into a silently failing background job.
func TestStartOne_ValidationErrorsAreSynchronous(t *testing.T) {
	s, repo := newTestSyncer(t)
	ctx := context.Background()

	wrongType := testImportList("Goodreads", "goodreads", true)
	if err := repo.Create(ctx, &wrongType); err != nil {
		t.Fatalf("seed: %v", err)
	}
	disabled := testImportList("Disabled", "hardcover", false)
	if err := repo.Create(ctx, &disabled); err != nil {
		t.Fatalf("seed: %v", err)
	}
	noToken := testImportList("No token", "hardcover", true)
	noToken.APIKey = ""
	if err := repo.Create(ctx, &noToken); err != nil {
		t.Fatalf("seed: %v", err)
	}

	cases := []struct {
		name string
		id   int64
		want error
	}{
		{"missing", 99999, ErrNotFound},
		{"wrong type", wrongType.ID, ErrWrongType},
		{"disabled", disabled.ID, ErrDisabled},
		{"no token", noToken.ID, ErrMissingToken},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if err := s.StartOne(ctx, tc.id); !errors.Is(err, tc.want) {
				t.Errorf("StartOne: want %v, got %v", tc.want, err)
			}
			// A rejected start must not leave the gate held or publish progress.
			if p := s.Progress(); p.Running {
				t.Errorf("rejected start left progress running: %+v", p)
			}
		})
	}
}

// TestStartOne_StampsListOwnerFromTheBackgroundJob is the tenancy guard for the
// move off the request: ownership comes from the list row, not from whoever
// pressed "Sync now", so the background context must still scope created
// content to the list's owner.
func TestStartOne_StampsListOwnerFromTheBackgroundJob(t *testing.T) {
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

	owner, err := users.Create(ctx, "alice", "hash")
	if err != nil {
		t.Fatalf("seed user: %v", err)
	}

	s := New(importLists, authors, books)
	il := testImportList("Alice's shelf", "hardcover", true)
	il.OwnerUserID = &owner.ID
	if err := importLists.Create(ctx, &il); err != nil {
		t.Fatalf("seed list: %v", err)
	}
	s.WithClientFactory(func(string) hardcoverClient {
		return &fakeHardcoverClient{
			lists: []hardcover.HCList{{ID: 3, Slug: il.URL, Name: il.Name}},
			books: []models.Book{
				{ForeignID: "hc:bg-owned", Title: "Owned Book", MetadataProvider: "hardcover",
					Author: &models.Author{ForeignID: "hc:bg-author", Name: "Owned Author", MetadataProvider: "hardcover"}},
			},
		}
	})

	if err := s.StartOne(ctx, il.ID); err != nil {
		t.Fatalf("StartOne: %v", err)
	}
	waitFor(t, "background sync to finish", func() bool {
		p := s.Progress()
		return !p.Running && p.FinishedAt != nil
	})

	gotBook, err := books.GetByForeignID(ctx, "hc:bg-owned")
	if err != nil || gotBook == nil {
		t.Fatalf("created book not found: %v", err)
	}
	if gotBook.OwnerUserID != owner.ID {
		t.Errorf("book OwnerUserID = %d, want %d (list owner)", gotBook.OwnerUserID, owner.ID)
	}
	gotAuthor, err := authors.GetByAnyForeignID(ctx, "hc:bg-author")
	if err != nil || gotAuthor == nil {
		t.Fatalf("created author not found: %v", err)
	}
	if gotAuthor.OwnerUserID != owner.ID {
		t.Errorf("author OwnerUserID = %d, want %d (list owner)", gotAuthor.OwnerUserID, owner.ID)
	}
}

// TestProgress_ReportsFailureReason verifies a failed run closes out the
// snapshot with the error the UI shows instead of leaving it "running".
func TestProgress_ReportsFailureReason(t *testing.T) {
	s, repo := newTestSyncer(t)
	ctx := context.Background()

	il := testImportList("Missing slug", "hardcover", true)
	if err := repo.Create(ctx, &il); err != nil {
		t.Fatalf("seed: %v", err)
	}
	// No list on the account matches the configured slug.
	s.WithClientFactory(func(string) hardcoverClient { return &fakeHardcoverClient{} })

	if err := s.SyncOne(ctx, il.ID); err == nil {
		t.Fatal("SyncOne: want an error for an unmatched slug")
	}
	p := s.Progress()
	if p.Running || p.FinishedAt == nil {
		t.Fatalf("progress after failure = %+v, want finished", p)
	}
	if p.Error == "" {
		t.Error("progress must carry the failure reason")
	}
}

// panickingHardcoverClient blows up where the real client can: mid-fetch,
// after the run has already published its "running" snapshot.
type panickingHardcoverClient struct {
	fakeHardcoverClient
}

func (p *panickingHardcoverClient) GetUserLists(context.Context) ([]hardcover.HCList, error) {
	panic("hardcover client exploded")
}

// TestStartOne_PanicIsContainedAndLeavesASaneTerminalState is the #1854
// regression: before the move to a background job this body ran inside the chi
// handler, where middleware.Recoverer turned a panic into a 500 and the process
// lived. On a detached goroutine there is no such backstop, so an unrecovered
// panic in syncList or the audiobook enricher would kill the service. Remove
// the recover in syncListTracked and this test binary dies outright.
//
// Containment alone isn't enough: the single-flight gate has to be released and
// the progress snapshot has to reach a terminal state, or the next "Sync now"
// answers 409 forever and the UI spins on a run that will never finish.
func TestStartOne_PanicIsContainedAndLeavesASaneTerminalState(t *testing.T) {
	s, repo := newTestSyncer(t)
	ctx := context.Background()

	il := testImportList("Exploding", "hardcover", true)
	if err := repo.Create(ctx, &il); err != nil {
		t.Fatalf("seed: %v", err)
	}
	s.WithClientFactory(func(string) hardcoverClient { return &panickingHardcoverClient{} })

	if err := s.StartOne(ctx, il.ID); err != nil {
		t.Fatalf("StartOne: %v", err)
	}
	waitFor(t, "the panicking sync to close out", func() bool { return !s.Progress().Running })

	p := s.Progress()
	if p.FinishedAt == nil {
		t.Error("a panicking run must still record FinishedAt")
	}
	if p.Error == "" {
		t.Error("a panicking run must surface a failure reason, not look successful")
	}

	// The gate is free again: a fresh start is accepted rather than 409'd.
	if !s.syncRunning.CompareAndSwap(false, true) {
		t.Fatal("the single-flight gate stayed held after the panic")
	}
	s.syncRunning.Store(false)

	// And the panic is reported to the caller of the synchronous form, not
	// swallowed into a success.
	if err := s.SyncOne(ctx, il.ID); err == nil {
		t.Error("SyncOne over a panicking client returned nil")
	}
}

// TestStartOne_ShuttingDownReleasesTheGate covers the launch that never
// happens: jobs.Group.Go is a documented no-op once shutdown has begun, and
// StartOne publishes the gate and the "running" snapshot before calling it. If
// the drop isn't undone, syncRunning stays true and Progress() describes a run
// that will never finish for the rest of the process's life.
func TestStartOne_ShuttingDownReleasesTheGate(t *testing.T) {
	s, repo := newTestSyncer(t)
	ctx := context.Background()

	il := testImportList("Late", "hardcover", true)
	if err := repo.Create(ctx, &il); err != nil {
		t.Fatalf("seed: %v", err)
	}
	s.WithClientFactory(func(string) hardcoverClient { return &fakeHardcoverClient{} })

	g := jobs.NewGroup(context.Background())
	g.Shutdown(time.Second)
	s.WithJobs(g)

	if err := s.StartOne(ctx, il.ID); !errors.Is(err, ErrShuttingDown) {
		t.Fatalf("StartOne during shutdown = %v, want ErrShuttingDown", err)
	}
	if p := s.Progress(); p.Running {
		t.Errorf("progress still reports a running sync after a dropped launch: %+v", p)
	}
	if !s.syncRunning.CompareAndSwap(false, true) {
		t.Fatal("the single-flight gate stuck after a dropped launch")
	}
	s.syncRunning.Store(false)
}

// TestSync_ScheduledRunIsTrackedByTheJobsGroup closes the gap the manual path
// already had: the scheduler calls Sync on the process-lifecycle context, which
// SIGTERM cancels straight away, and nothing waited on it — so the run could
// still be inside books.Create when database.Close() fired. Wired to the jobs
// group, the whole scheduled pass is registered, so Shutdown both cancels it
// and reports it if it overruns the grace window.
func TestSync_ScheduledRunIsTrackedByTheJobsGroup(t *testing.T) {
	s, repo := newTestSyncer(t)
	ctx := context.Background()

	il := testImportList("Scheduled", "hardcover", true)
	if err := repo.Create(ctx, &il); err != nil {
		t.Fatalf("seed: %v", err)
	}
	// Deliberately deaf to cancellation, standing in for a run parked inside a
	// provider call or a books.Create that shutdown must wait out rather than
	// walk over.
	client := &stubbornHardcoverClient{
		fakeHardcoverClient: fakeHardcoverClient{lists: []hardcover.HCList{{ID: 7, Slug: il.URL, Name: il.Name}}},
		started:             make(chan struct{}),
		release:             make(chan struct{}),
	}
	s.WithClientFactory(func(string) hardcoverClient { return client })

	g := jobs.NewGroup(context.Background())
	s.WithJobs(g)

	done := make(chan error, 1)
	go func() { done <- s.Sync(ctx) }()
	<-client.started

	// The group knows about the run, which is only true if Sync registered it.
	if still := g.Shutdown(150 * time.Millisecond); len(still) != 1 || still[0] != "hardcover-list-sync-scheduled" {
		t.Fatalf("Shutdown reported %v, want the in-flight scheduled sync", still)
	}
	if !client.sawCancel.Load() {
		t.Error("the scheduled run did not observe the group's shutdown cancellation")
	}

	close(client.release)
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("scheduled Sync did not return")
	}
	// Shutdown now drains, proving the run released its tracking slot.
	if still := g.Shutdown(2 * time.Second); len(still) != 0 {
		t.Fatalf("scheduled sync did not release its tracking slot: %v", still)
	}
}

// stubbornHardcoverClient stalls until released, recording whether its context
// was cancelled in the meantime but declining to return early because of it.
type stubbornHardcoverClient struct {
	fakeHardcoverClient
	started   chan struct{}
	release   chan struct{}
	sawCancel atomic.Bool
	once      sync.Once
}

func (b *stubbornHardcoverClient) GetUserLists(ctx context.Context) ([]hardcover.HCList, error) {
	b.once.Do(func() { close(b.started) })
	for {
		select {
		case <-b.release:
			return b.fakeHardcoverClient.GetUserLists(ctx)
		case <-ctx.Done():
			b.sawCancel.Store(true)
			<-b.release
			return b.fakeHardcoverClient.GetUserLists(ctx)
		}
	}
}

// A scheduled run that arrives during shutdown is skipped rather than started
// against a database that is about to close, and it reports no error — the
// scheduler logs errors, and "we're shutting down" isn't one.
func TestSync_ScheduledRunSkippedDuringShutdown(t *testing.T) {
	s, repo := newTestSyncer(t)
	ctx := context.Background()

	il := testImportList("Late scheduled", "hardcover", true)
	if err := repo.Create(ctx, &il); err != nil {
		t.Fatalf("seed: %v", err)
	}
	client := &fakeHardcoverClient{}
	s.WithClientFactory(func(string) hardcoverClient { return client })

	g := jobs.NewGroup(context.Background())
	g.Shutdown(time.Second)
	s.WithJobs(g)

	if err := s.Sync(ctx); err != nil {
		t.Fatalf("scheduled Sync during shutdown = %v, want nil (skipped)", err)
	}
	if p := s.Progress(); p.Running {
		t.Errorf("a skipped scheduled run must not publish a running snapshot: %+v", p)
	}
	if !s.syncRunning.CompareAndSwap(false, true) {
		t.Fatal("the single-flight gate was taken by a skipped scheduled run")
	}
	s.syncRunning.Store(false)
}

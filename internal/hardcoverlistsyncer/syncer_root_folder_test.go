package hardcoverlistsyncer

import (
	"context"
	"testing"

	"github.com/vavallee/bindery/internal/db"
	"github.com/vavallee/bindery/internal/metadata/hardcover"
	"github.com/vavallee/bindery/internal/models"
)

// newTestSyncerWithRootFolders is newTestSyncerWithQualityProfiles with a root
// folder repo instead, so the test can seed a real folder row to point the list
// at rather than a made-up id.
func newTestSyncerWithRootFolders(t *testing.T) (*ListSyncer, *db.ImportListRepo, *db.RootFolderRepo) {
	t.Helper()
	database, err := db.OpenMemory()
	if err != nil {
		t.Fatalf("open memory db: %v", err)
	}
	t.Cleanup(func() { database.Close() })
	importLists := db.NewImportListRepo(database)
	return New(importLists, db.NewAuthorRepo(database), db.NewBookRepo(database)),
		importLists,
		db.NewRootFolderRepo(database)
}

// TestSyncOne_NewAuthorInheritsListRootFolder is #1864, the same defect #1781
// fixed for quality profiles: models.ImportList.RootFolderID is persisted and
// settable from the per-list picker, and the syncer never read it, so a list
// configured with a root folder produced authors with none.
func TestSyncOne_NewAuthorInheritsListRootFolder(t *testing.T) {
	s, repo, folders := newTestSyncerWithRootFolders(t)
	ctx := context.Background()

	folder, err := folders.Create(ctx, "/books/hardcover")
	if err != nil {
		t.Fatalf("seed root folder: %v", err)
	}

	il := testImportList("HC rooted", "hardcover", true)
	il.RootFolderID = &folder.ID
	if err := repo.Create(ctx, &il); err != nil {
		t.Fatalf("seed list: %v", err)
	}
	s.WithClientFactory(func(string) hardcoverClient {
		return &fakeHardcoverClient{
			lists: []hardcover.HCList{{ID: 21, Slug: il.URL, Name: il.Name}},
			books: []models.Book{
				{ForeignID: "hc:rf-book", Title: "Rooted Book", MetadataProvider: "hardcover",
					Author: &models.Author{ForeignID: "hc:rf-author", Name: "Rooted Author", MetadataProvider: "hardcover"}},
			},
		}
	})

	if err := s.SyncOne(ctx, il.ID); err != nil {
		t.Fatalf("SyncOne: %v", err)
	}

	created, err := s.authors.GetByAnyForeignID(ctx, "hc:rf-author")
	if err != nil {
		t.Fatalf("GetByAnyForeignID: %v", err)
	}
	if created == nil {
		t.Fatal("expected the new author to be created")
	}
	if created.RootFolderID == nil {
		t.Fatal("new author RootFolderID is nil, want the list's root folder (#1864)")
	}
	if *created.RootFolderID != folder.ID {
		t.Errorf("new author RootFolderID = %d, want %d (the list's root folder)", *created.RootFolderID, folder.ID)
	}
}

// TestSyncOne_ListWithoutRootFolderLeavesAuthorUnset pins the deliberate
// non-behaviour, for the reason #1781 established: root folders are
// owner-scoped, so a background job stamping a "default" row can hand one
// user's storage to another user's list-synced authors on a tenanted install.
// A list with none configured must leave the author's root folder nil.
func TestSyncOne_ListWithoutRootFolderLeavesAuthorUnset(t *testing.T) {
	s, repo, folders := newTestSyncerWithRootFolders(t)
	ctx := context.Background()

	// A folder exists and is the only one, which is exactly the shape a blind
	// fallback would latch onto.
	if _, err := folders.Create(ctx, "/books/someone-elses"); err != nil {
		t.Fatalf("seed root folder: %v", err)
	}

	il := testImportList("HC unrooted", "hardcover", true)
	// RootFolderID deliberately left nil.
	if err := repo.Create(ctx, &il); err != nil {
		t.Fatalf("seed list: %v", err)
	}
	s.WithClientFactory(func(string) hardcoverClient {
		return &fakeHardcoverClient{
			lists: []hardcover.HCList{{ID: 22, Slug: il.URL, Name: il.Name}},
			books: []models.Book{
				{ForeignID: "hc:nrf-book", Title: "Unrooted Book", MetadataProvider: "hardcover",
					Author: &models.Author{ForeignID: "hc:nrf-author", Name: "Unrooted Author", MetadataProvider: "hardcover"}},
			},
		}
	})

	if err := s.SyncOne(ctx, il.ID); err != nil {
		t.Fatalf("SyncOne: %v", err)
	}

	created, err := s.authors.GetByAnyForeignID(ctx, "hc:nrf-author")
	if err != nil {
		t.Fatalf("GetByAnyForeignID: %v", err)
	}
	if created == nil {
		t.Fatal("expected the new author to be created")
	}
	if created.RootFolderID != nil {
		t.Errorf("new author RootFolderID = %d, want nil: an unconfigured list must not inherit a root folder", *created.RootFolderID)
	}
}

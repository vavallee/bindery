package importer

import (
	"context"
	"testing"

	"github.com/vavallee/bindery/internal/db"
	"github.com/vavallee/bindery/internal/models"
)

// scannerWithRootFolders builds a Scanner wired to a real in-memory DB that
// includes a RootFolderRepo, so effectiveLibraryDir can be exercised end-to-end.
func scannerWithRootFolders(t *testing.T, libraryDir string) (*Scanner, *db.RootFolderRepo, *db.AuthorRepo, context.Context) {
	t.Helper()
	database, err := db.OpenMemory()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { database.Close() })

	books := db.NewBookRepo(database)
	authors := db.NewAuthorRepo(database)
	history := db.NewHistoryRepo(database)
	downloads := db.NewDownloadRepo(database)
	clients := db.NewDownloadClientRepo(database)
	rf := db.NewRootFolderRepo(database)

	s := NewScanner(downloads, clients, books, authors, history, libraryDir, "", "", "", "")
	s.WithRootFolders(rf)
	return s, rf, authors, context.Background()
}

func TestEffectiveLibraryDir_NilAuthor(t *testing.T) {
	s, _, _, ctx := scannerWithRootFolders(t, "/default/lib")
	got := s.effectiveLibraryDir(ctx, nil)
	if got != "/default/lib" {
		t.Errorf("nil author: want /default/lib, got %q", got)
	}
}

func TestEffectiveLibraryDir_AuthorNoRootFolder(t *testing.T) {
	s, _, _, ctx := scannerWithRootFolders(t, "/default/lib")
	author := &models.Author{RootFolderID: nil}
	got := s.effectiveLibraryDir(ctx, author)
	if got != "/default/lib" {
		t.Errorf("author with nil RootFolderID: want /default/lib, got %q", got)
	}
}

func TestEffectiveLibraryDir_AuthorWithRootFolder(t *testing.T) {
	dir := t.TempDir()
	s, rf, _, ctx := scannerWithRootFolders(t, "/default/lib")

	created, err := rf.Create(ctx, dir)
	if err != nil {
		t.Fatal(err)
	}
	id := created.ID
	author := &models.Author{RootFolderID: &id}
	got := s.effectiveLibraryDir(ctx, author)
	if got != dir {
		t.Errorf("want %q, got %q", dir, got)
	}
}

func TestEffectiveLibraryDir_AuthorWithDeletedRootFolder(t *testing.T) {
	dir := t.TempDir()
	s, rf, _, ctx := scannerWithRootFolders(t, "/default/lib")

	created, err := rf.Create(ctx, dir)
	if err != nil {
		t.Fatal(err)
	}
	id := created.ID
	if err := rf.Delete(ctx, id); err != nil {
		t.Fatal(err)
	}

	author := &models.Author{RootFolderID: &id}
	got := s.effectiveLibraryDir(ctx, author)
	// Deleted folder → falls back to global default
	if got != "/default/lib" {
		t.Errorf("deleted folder: want /default/lib, got %q", got)
	}
}

func TestEffectiveLibraryDir_NoRootFolderRepo(t *testing.T) {
	// Scanner without WithRootFolders — should always use libraryDir.
	database, err := db.OpenMemory()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { database.Close() })

	books := db.NewBookRepo(database)
	authors := db.NewAuthorRepo(database)
	history := db.NewHistoryRepo(database)
	downloads := db.NewDownloadRepo(database)
	clients := db.NewDownloadClientRepo(database)
	s := NewScanner(downloads, clients, books, authors, history, "/default/lib", "", "", "", "")
	// No WithRootFolders call — rootFolders field is nil.

	id := int64(42)
	author := &models.Author{RootFolderID: &id}
	got := s.effectiveLibraryDir(context.Background(), author)
	if got != "/default/lib" {
		t.Errorf("no repo: want /default/lib, got %q", got)
	}
}

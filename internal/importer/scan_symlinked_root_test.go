package importer

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/vavallee/bindery/internal/db"
	"github.com/vavallee/bindery/internal/models"
)

// A library folder configured as a symlink (/books -> /mnt/storage/books, or
// a container volume path that is a link) used to be invisible to Library
// Scan: filepath.Walk Lstats the root, sees a link, and never descends. These
// tests pin that the root itself is followed, that what gets stored is still
// under the configured path, and that links INSIDE the root stay unfollowed.

// symlinkedRootLibrary builds a real library folder holding
// "Andy Weir/Project Hail Mary.epub" plus a linked author folder
// "Martha Wells -> <elsewhere>/Martha Wells" holding "All Systems Red.epub",
// and returns a symlink to the real folder (the configured root).
func symlinkedRootLibrary(t *testing.T) (root string) {
	t.Helper()
	real := filepath.Join(t.TempDir(), "storage", "books")
	if err := os.MkdirAll(filepath.Join(real, "Andy Weir"), 0o755); err != nil {
		t.Fatal(err)
	}
	writeEpubAt(t, filepath.Join(real, "Andy Weir", "Project Hail Mary.epub"), "Project Hail Mary", "Andy Weir", "")

	elsewhere := filepath.Join(t.TempDir(), "Martha Wells")
	if err := os.MkdirAll(elsewhere, 0o755); err != nil {
		t.Fatal(err)
	}
	writeEpubAt(t, filepath.Join(elsewhere, "All Systems Red.epub"), "All Systems Red", "Martha Wells", "")
	symlinkOrSkip(t, elsewhere, filepath.Join(real, "Martha Wells"))

	root = filepath.Join(t.TempDir(), "books")
	symlinkOrSkip(t, real, root)
	return root
}

func TestScanLibrary_SymlinkedLibraryRootIsScanned(t *testing.T) {
	root := symlinkedRootLibrary(t)

	database, err := db.OpenMemory()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { database.Close() })
	ctx := context.Background()
	books := db.NewBookRepo(database)
	authors := db.NewAuthorRepo(database)
	s := NewScanner(db.NewDownloadRepo(database), db.NewDownloadClientRepo(database), books, authors,
		db.NewHistoryRepo(database), root, "", "", "", "")
	s.WithSettings(db.NewSettingsRepo(database))
	s.WithUnmatchedUnits(db.NewUnmatchedUnitRepo(database))

	seed := func(foreign, name, sort, bookForeign, title string) *models.Book {
		t.Helper()
		a := &models.Author{ForeignID: foreign, Name: name, SortName: sort, MetadataProvider: "openlibrary"}
		if err := authors.Create(ctx, a); err != nil {
			t.Fatal(err)
		}
		b := &models.Book{ForeignID: bookForeign, AuthorID: a.ID, Title: title,
			Status: models.BookStatusWanted, MediaType: models.MediaTypeEbook, MetadataProvider: "openlibrary"}
		if err := books.Create(ctx, b); err != nil {
			t.Fatal(err)
		}
		return b
	}
	phm := seed("ol:weir", "Andy Weir", "Weir, Andy", "ol:phm", "Project Hail Mary")
	asr := seed("ol:wells", "Martha Wells", "Wells, Martha", "ol:asr", "All Systems Red")

	s.ScanLibrary(ctx)

	got, err := books.GetByID(ctx, phm.ID)
	if err != nil {
		t.Fatal(err)
	}
	want := filepath.Join(root, "Andy Weir", "Project Hail Mary.epub")
	if got.EbookFilePath != want {
		t.Fatalf("ebook path = %q, want %q: a library folder that is a symlink must be scanned, and the stored path must stay under the configured root", got.EbookFilePath, want)
	}
	files, err := books.ListFiles(ctx, phm.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(files) != 1 || files[0].Path != want {
		t.Fatalf("book files = %+v, want one at %q", files, want)
	}

	// The linked author folder inside the root is still not followed.
	other, err := books.GetByID(ctx, asr.ID)
	if err != nil {
		t.Fatal(err)
	}
	if other.FilePath != "" || other.EbookFilePath != "" {
		t.Fatalf("book under a linked author folder was attached (%q / %q); links inside the root must stay unfollowed", other.FilePath, other.EbookFilePath)
	}
	if fs, _ := books.ListFiles(ctx, asr.ID); len(fs) != 0 {
		t.Fatalf("book under a linked author folder has files %+v", fs)
	}
}

func TestLibrarySnapshot_SymlinkedRootIsWalked(t *testing.T) {
	root := symlinkedRootLibrary(t)

	snap := NewLibrarySnapshot(root, "")
	want := filepath.Join(root, "Andy Weir", "Project Hail Mary.epub")
	if got := snap.FindExisting(context.Background(), "Project Hail Mary", "Andy Weir", models.MediaTypeEbook); got != want {
		t.Fatalf("FindExisting = %q, want %q", got, want)
	}
	if got := snap.FindExisting(context.Background(), "All Systems Red", "Martha Wells", models.MediaTypeEbook); got != "" {
		t.Fatalf("FindExisting followed a linked author folder inside the root: %q", got)
	}

	// The same holds for an audiobook root that is a symlink.
	abSnap := NewLibrarySnapshot(t.TempDir(), root)
	if got := abSnap.FindExisting(context.Background(), "Project Hail Mary", "Andy Weir", ""); got != want {
		t.Fatalf("audiobook root FindExisting = %q, want %q", got, want)
	}
}

func TestWalkRoot_ReportsConfiguredPaths(t *testing.T) {
	root := symlinkedRootLibrary(t)
	var got []string
	if err := walkRoot(root, nil, func(path string, info os.FileInfo, err error) error {
		if err == nil && !info.IsDir() {
			got = append(got, path)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	want := []string{
		filepath.Join(root, "Andy Weir", "Project Hail Mary.epub"),
		filepath.Join(root, "Martha Wells"), // the link itself, reported, not entered
	}
	if len(got) != len(want) || got[0] != want[0] || got[1] != want[1] {
		t.Fatalf("walk = %v, want %v", got, want)
	}

	// A plain directory root and a missing root behave exactly as
	// filepath.Walk does.
	plain := t.TempDir()
	writeFile(t, filepath.Join(plain, "a.epub"))
	got = nil
	_ = walkRoot(plain, nil, func(path string, info os.FileInfo, err error) error {
		if err == nil && !info.IsDir() {
			got = append(got, path)
		}
		return nil
	})
	if len(got) != 1 || got[0] != filepath.Join(plain, "a.epub") {
		t.Fatalf("plain root walk = %v", got)
	}
	missing := filepath.Join(plain, "nope")
	var sawErr bool
	_ = walkRoot(missing, nil, func(path string, _ os.FileInfo, err error) error {
		if path == missing && err != nil {
			sawErr = true
		}
		return nil
	})
	if !sawErr {
		t.Fatal("missing root: want the Walk error reported against the configured path")
	}
}

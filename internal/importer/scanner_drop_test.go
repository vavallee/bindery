package importer

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/vavallee/bindery/internal/db"
	"github.com/vavallee/bindery/internal/models"
)

// dropFixture wires a scanner with a SettingsRepo, library dir, an author and a
// book, and returns everything a drop-folder test needs. The book's media type
// is caller-controlled so ebook vs audiobook detection can be exercised.
func dropFixture(t *testing.T, mediaType string) (s *Scanner, settings *db.SettingsRepo, downloads *db.DownloadRepo, books *db.BookRepo, libraryDir, dropDir string, book *models.Book, ctx context.Context) {
	t.Helper()
	database, err := db.OpenMemory()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { database.Close() })

	ctx = context.Background()
	settings = db.NewSettingsRepo(database)
	downloads = db.NewDownloadRepo(database)
	books = db.NewBookRepo(database)
	authors := db.NewAuthorRepo(database)
	libraryDir = t.TempDir()
	dropDir = t.TempDir()

	author := &models.Author{ForeignID: "OLA1", Name: "Author A", SortName: "A, Author", Monitored: true, MetadataProvider: "openlibrary"}
	if err := authors.Create(ctx, author); err != nil {
		t.Fatal(err)
	}
	book = &models.Book{
		ForeignID: "OLB1", AuthorID: author.ID, Title: "Title T", SortTitle: "t, title",
		Status: models.BookStatusWanted, Monitored: true, AnyEditionOK: true,
		MediaType: mediaType, MetadataProvider: "openlibrary",
	}
	if err := books.Create(ctx, book); err != nil {
		t.Fatal(err)
	}

	s = NewScanner(
		downloads, db.NewDownloadClientRepo(database),
		books, authors, db.NewHistoryRepo(database),
		libraryDir, "", "", "", "",
	)
	s.WithSettings(settings)
	return s, settings, downloads, books, libraryDir, dropDir, book, ctx
}

func newDropDownload(t *testing.T, downloads *db.DownloadRepo, book *models.Book, ctx context.Context) *models.Download {
	t.Helper()
	dl := &models.Download{GUID: "guid-drop-" + book.ForeignID, Title: book.Title, BookID: &book.ID, Status: models.StateCompleted, NZBURL: "fake://url"}
	if err := downloads.Create(ctx, dl); err != nil {
		t.Fatal(err)
	}
	return dl
}

func setDropSettings(t *testing.T, settings *db.SettingsRepo, ctx context.Context, kv map[string]string) {
	t.Helper()
	kv["import.mode"] = "external"
	for k, v := range kv {
		if err := settings.Set(ctx, k, v); err != nil {
			t.Fatalf("set %s: %v", k, err)
		}
	}
}

// findOneFile returns the single regular file under root, failing if there
// isn't exactly one.
func findOneFile(t *testing.T, root string) string {
	t.Helper()
	var found []string
	_ = filepath.Walk(root, func(p string, info os.FileInfo, err error) error {
		if err == nil && !info.IsDir() {
			found = append(found, p)
		}
		return nil
	})
	if len(found) != 1 {
		t.Fatalf("expected exactly one file under %s, found %d: %v", root, len(found), found)
	}
	return found[0]
}

func assertEmptyDir(t *testing.T, dir, what string) {
	t.Helper()
	entries, _ := os.ReadDir(dir)
	if len(entries) != 0 {
		t.Errorf("%s should be empty, got %d entries", what, len(entries))
	}
}

func assertStatus(t *testing.T, downloads *db.DownloadRepo, ctx context.Context, guid string, want models.DownloadState) {
	t.Helper()
	got, err := downloads.GetByGUID(ctx, guid)
	if err != nil {
		t.Fatalf("reload download: %v", err)
	}
	if got.Status != want {
		t.Errorf("download status = %q, want %q", got.Status, want)
	}
}

// TestDrop_EbookFlatCopy: the flat layout drops a sanely-named file in the
// drop-folder root, leaves the source intact, parks StateImportExternal, and
// writes nothing into the library dir.
func TestDrop_EbookFlatCopy(t *testing.T) {
	s, settings, downloads, books, libraryDir, dropDir, book, ctx := dropFixture(t, models.MediaTypeEbook)
	setDropSettings(t, settings, ctx, map[string]string{"import.drop_folder": dropDir})

	downloadDir := t.TempDir()
	src := filepath.Join(downloadDir, "whatever.epub")
	if err := os.WriteFile(src, []byte("epub bytes"), 0o644); err != nil {
		t.Fatal(err)
	}
	dl := newDropDownload(t, downloads, book, ctx)

	s.tryImportInternal(ctx, dl, downloadDir, "", "", "", nil, nil)

	dst := filepath.Join(dropDir, "Title T - Author A.epub")
	got, err := os.ReadFile(dst)
	if err != nil {
		t.Fatalf("drop file not found at %s: %v", dst, err)
	}
	if string(got) != "epub bytes" {
		t.Errorf("drop contents = %q, want %q", got, "epub bytes")
	}
	if _, err := os.Stat(src); err != nil {
		t.Errorf("source removed after drop (copy semantics expected): %v", err)
	}
	assertEmptyDir(t, libraryDir, "library dir")
	assertStatus(t, downloads, ctx, dl.GUID, models.StateImportExternal)

	// The library file path must remain unset — drop does not place into the library.
	reloaded, _ := books.GetByID(ctx, book.ID)
	if reloaded.EbookFilePath != "" {
		t.Errorf("EbookFilePath = %q, want empty (drop must not record a library file)", reloaded.EbookFilePath)
	}
}

// TestDrop_EbookTemplated: the templated layout recreates the {Author}/… tree
// inside the drop folder.
func TestDrop_EbookTemplated(t *testing.T) {
	s, settings, downloads, _, _, dropDir, book, ctx := dropFixture(t, models.MediaTypeEbook)
	setDropSettings(t, settings, ctx, map[string]string{"import.drop_folder": dropDir, "import.drop_layout": "templated"})

	downloadDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(downloadDir, "x.epub"), []byte("epub"), 0o644); err != nil {
		t.Fatal(err)
	}
	dl := newDropDownload(t, downloads, book, ctx)

	s.tryImportInternal(ctx, dl, downloadDir, "", "", "", nil, nil)

	found := findOneFile(t, dropDir)
	rel, _ := filepath.Rel(dropDir, found)
	if !strings.HasPrefix(rel, "Author A"+string(filepath.Separator)) {
		t.Errorf("templated drop = %q, want it nested under 'Author A/'", rel)
	}
	if filepath.Base(found) != "Title T - Author A.epub" {
		t.Errorf("templated drop filename = %q, want 'Title T - Author A.epub'", filepath.Base(found))
	}
	assertStatus(t, downloads, ctx, dl.GUID, models.StateImportExternal)
}

// TestDrop_EbookHardlink: hardlink mode shares the inode with the source.
func TestDrop_EbookHardlink(t *testing.T) {
	s, settings, downloads, _, _, dropDir, book, ctx := dropFixture(t, models.MediaTypeEbook)
	setDropSettings(t, settings, ctx, map[string]string{"import.drop_folder": dropDir, "import.drop_link_mode": "hardlink"})

	downloadDir := t.TempDir()
	src := filepath.Join(downloadDir, "x.epub")
	if err := os.WriteFile(src, []byte("epub"), 0o644); err != nil {
		t.Fatal(err)
	}
	dl := newDropDownload(t, downloads, book, ctx)

	s.tryImportInternal(ctx, dl, downloadDir, "", "", "", nil, nil)

	dst := filepath.Join(dropDir, "Title T - Author A.epub")
	si, err := os.Stat(src)
	if err != nil {
		t.Fatalf("source missing: %v", err)
	}
	di, err := os.Stat(dst)
	if err != nil {
		t.Fatalf("drop file missing: %v", err)
	}
	if !os.SameFile(si, di) {
		t.Error("drop file is not the same inode as the source — hardlink not created")
	}
}

// TestDrop_AudiobookFlatCopy: audiobooks drop as a folder named flatly, source
// preserved, parked external, library untouched.
func TestDrop_AudiobookFlatCopy(t *testing.T) {
	s, settings, downloads, books, libraryDir, dropDir, book, ctx := dropFixture(t, models.MediaTypeAudiobook)
	setDropSettings(t, settings, ctx, map[string]string{"import.drop_folder": dropDir})

	downloadDir := t.TempDir()
	src := filepath.Join(downloadDir, "audiobook.m4b")
	if err := os.WriteFile(src, []byte("m4b bytes"), 0o644); err != nil {
		t.Fatal(err)
	}
	dl := newDropDownload(t, downloads, book, ctx)

	s.tryImportInternal(ctx, dl, downloadDir, "", "", "", nil, nil)

	dst := filepath.Join(dropDir, "Title T - Author A", "audiobook.m4b")
	if _, err := os.Stat(dst); err != nil {
		t.Fatalf("audiobook drop not found at %s: %v", dst, err)
	}
	if _, err := os.Stat(src); err != nil {
		t.Errorf("source removed after audiobook drop (copy semantics expected): %v", err)
	}
	assertEmptyDir(t, libraryDir, "library dir")
	assertStatus(t, downloads, ctx, dl.GUID, models.StateImportExternal)
	reloaded, _ := books.GetByID(ctx, book.ID)
	if reloaded.AudiobookFilePath != "" {
		t.Errorf("AudiobookFilePath = %q, want empty (drop must not record a library file)", reloaded.AudiobookFilePath)
	}
}

// TestDrop_NoFolderFallsBackToExternal: external mode with no drop folder keeps
// the original behaviour — file left in the download dir, parked external, drop
// dir untouched.
func TestDrop_NoFolderFallsBackToExternal(t *testing.T) {
	s, settings, downloads, _, _, dropDir, book, ctx := dropFixture(t, models.MediaTypeEbook)
	setDropSettings(t, settings, ctx, map[string]string{}) // import.mode=external, no drop_folder

	downloadDir := t.TempDir()
	src := filepath.Join(downloadDir, "x.epub")
	if err := os.WriteFile(src, []byte("epub"), 0o644); err != nil {
		t.Fatal(err)
	}
	dl := newDropDownload(t, downloads, book, ctx)

	s.tryImportInternal(ctx, dl, downloadDir, "", "", "", nil, nil)

	if _, err := os.Stat(src); err != nil {
		t.Errorf("plain external mode must leave the source in place: %v", err)
	}
	assertEmptyDir(t, dropDir, "drop dir")
	assertStatus(t, downloads, ctx, dl.GUID, models.StateImportExternal)
}

// TestDrop_PlacementFailureFailsImport: when the drop placement can't succeed
// (here the configured drop folder is actually a regular file), the import is
// failed retryably rather than silently claiming success.
func TestDrop_PlacementFailureFailsImport(t *testing.T) {
	s, settings, downloads, _, _, dropDir, book, ctx := dropFixture(t, models.MediaTypeEbook)
	// Make the drop "folder" a regular file so MkdirAll under it fails.
	badDrop := filepath.Join(dropDir, "not-a-dir")
	if err := os.WriteFile(badDrop, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	setDropSettings(t, settings, ctx, map[string]string{"import.drop_folder": badDrop})

	downloadDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(downloadDir, "x.epub"), []byte("epub"), 0o644); err != nil {
		t.Fatal(err)
	}
	dl := newDropDownload(t, downloads, book, ctx)

	s.tryImportInternal(ctx, dl, downloadDir, "", "", "", nil, nil)

	assertStatus(t, downloads, ctx, dl.GUID, models.StateImportFailed)
}

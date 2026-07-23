package importer

import (
	"context"
	"database/sql"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/vavallee/bindery/internal/db"
	"github.com/vavallee/bindery/internal/models"
)

// pairFixture wires a scanner with a media_type=both book for the drop-folder
// pair-gating tests (#942). It returns the raw *sql.DB too so tests can backdate
// completed_at to exercise the timeout escape hatch.
func pairFixture(t *testing.T, mediaType string) (s *Scanner, settings *db.SettingsRepo, downloads *db.DownloadRepo, books *db.BookRepo, database *sql.DB, dropDir string, book *models.Book, ctx context.Context) {
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
	libraryDir := t.TempDir()
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
	return s, settings, downloads, books, database, dropDir, book, ctx
}

// newPairDownload creates a completed download for a book with a format-specific
// GUID so both formats of a "both" book can coexist.
func newPairDownload(t *testing.T, downloads *db.DownloadRepo, book *models.Book, format string, ctx context.Context) *models.Download {
	t.Helper()
	dl := &models.Download{GUID: "guid-pair-" + format + "-" + book.ForeignID, Title: book.Title, BookID: &book.ID, Status: models.StateCompleted, NZBURL: "fake://url"}
	if err := downloads.Create(ctx, dl); err != nil {
		t.Fatal(err)
	}
	return dl
}

// writeEbookDownload creates a download dir with a single epub and returns it.
func writeEbookDownload(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "book.epub"), []byte("epub bytes"), 0o644); err != nil {
		t.Fatal(err)
	}
	return dir
}

// writeAudiobookDownload creates a download dir with a single m4b and returns it.
func writeAudiobookDownload(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "audiobook.m4b"), []byte("m4b bytes"), 0o644); err != nil {
		t.Fatal(err)
	}
	return dir
}

func setPairSettings(t *testing.T, settings *db.SettingsRepo, ctx context.Context, kv map[string]string) {
	t.Helper()
	kv["import.mode"] = "external"
	for k, v := range kv {
		if err := settings.Set(ctx, k, v); err != nil {
			t.Fatalf("set %s: %v", k, err)
		}
	}
}

// (a) Gating OFF: a media_type=both book drops each format immediately, exactly
// as before — the first format lands in the drop folder and parks external.
func TestPairGating_Off_DropsImmediately(t *testing.T) {
	s, settings, downloads, _, _, dropDir, book, ctx := pairFixture(t, models.MediaTypeBoth)
	setPairSettings(t, settings, ctx, map[string]string{"import.drop_folder": dropDir}) // no pair gating

	ebookDir := writeEbookDownload(t)
	dl := newPairDownload(t, downloads, book, "ebook", ctx)

	s.tryImportInternal(ctx, dl, ebookDir, "", "", "", nil, nil)

	dst := filepath.Join(dropDir, "Title T - Author A.epub")
	if _, err := os.Stat(dst); err != nil {
		t.Fatalf("gating off: ebook should drop immediately, missing at %s: %v", dst, err)
	}
	assertStatus(t, downloads, ctx, dl.GUID, models.StateImportExternal)
}

// (b) Gating ON, first format completes: it is HELD, not dropped, and the
// download is parked in the non-terminal StateImportHeld with its source intact.
func TestPairGating_On_FirstFormatHeld(t *testing.T) {
	s, settings, downloads, _, _, dropDir, book, ctx := pairFixture(t, models.MediaTypeBoth)
	setPairSettings(t, settings, ctx, map[string]string{
		"import.drop_folder":      dropDir,
		"import.drop_pair_gating": "true",
	})

	ebookDir := writeEbookDownload(t)
	dl := newPairDownload(t, downloads, book, "ebook", ctx)

	s.tryImportInternal(ctx, dl, ebookDir, "", "", "", nil, nil)

	assertEmptyDir(t, dropDir, "drop dir")
	if _, err := os.Stat(filepath.Join(ebookDir, "book.epub")); err != nil {
		t.Errorf("held format source must be left intact: %v", err)
	}
	assertStatus(t, downloads, ctx, dl.GUID, models.StateImportHeld)

	// The recorded import path lets a later release find the files.
	reloaded, err := downloads.GetByGUID(ctx, dl.GUID)
	if err != nil {
		t.Fatal(err)
	}
	if reloaded.ImportPath != ebookDir {
		t.Errorf("held download import_path = %q, want %q", reloaded.ImportPath, ebookDir)
	}
}

// (c) Gating ON, second format completes: both formats are released together
// into the drop folder and both downloads finish.
func TestPairGating_On_SecondFormatReleasesBoth(t *testing.T) {
	s, settings, downloads, _, _, dropDir, book, ctx := pairFixture(t, models.MediaTypeBoth)
	setPairSettings(t, settings, ctx, map[string]string{
		"import.drop_folder":      dropDir,
		"import.drop_pair_gating": "true",
	})

	// First: ebook completes and is held.
	ebookDir := writeEbookDownload(t)
	ebookDL := newPairDownload(t, downloads, book, "ebook", ctx)
	s.tryImportInternal(ctx, ebookDL, ebookDir, "", "", "", nil, nil)
	assertStatus(t, downloads, ctx, ebookDL.GUID, models.StateImportHeld)
	assertEmptyDir(t, dropDir, "drop dir before second format")

	// Second: audiobook completes and releases both.
	audioDir := writeAudiobookDownload(t)
	audioDL := newPairDownload(t, downloads, book, "audiobook", ctx)
	s.tryImportInternal(ctx, audioDL, audioDir, "", "", "", nil, nil)

	ebookDst := filepath.Join(dropDir, "Title T - Author A.epub")
	if _, err := os.Stat(ebookDst); err != nil {
		t.Errorf("held ebook not released to drop folder at %s: %v", ebookDst, err)
	}
	audioDst := filepath.Join(dropDir, "Title T - Author A", "audiobook.m4b")
	if _, err := os.Stat(audioDst); err != nil {
		t.Errorf("audiobook not dropped at %s: %v", audioDst, err)
	}
	assertStatus(t, downloads, ctx, ebookDL.GUID, models.StateImportExternal)
	assertStatus(t, downloads, ctx, audioDL.GUID, models.StateImportExternal)
}

// (d) Single-format book: unaffected by gating — drops immediately even with
// the gate on (there is nothing to pair).
func TestPairGating_On_SingleFormatUnaffected(t *testing.T) {
	s, settings, downloads, _, _, dropDir, book, ctx := pairFixture(t, models.MediaTypeEbook)
	setPairSettings(t, settings, ctx, map[string]string{
		"import.drop_folder":      dropDir,
		"import.drop_pair_gating": "true",
	})

	ebookDir := writeEbookDownload(t)
	dl := newPairDownload(t, downloads, book, "ebook", ctx)

	s.tryImportInternal(ctx, dl, ebookDir, "", "", "", nil, nil)

	dst := filepath.Join(dropDir, "Title T - Author A.epub")
	if _, err := os.Stat(dst); err != nil {
		t.Fatalf("single-format book must drop immediately, missing at %s: %v", dst, err)
	}
	assertStatus(t, downloads, ctx, dl.GUID, models.StateImportExternal)
}

// (e) Timeout escape hatch: a held format whose sibling never arrives is dropped
// alone once it ages past the timeout, and the download finishes.
func TestPairGating_TimeoutReleasesHeldAlone(t *testing.T) {
	s, settings, downloads, _, database, dropDir, book, ctx := pairFixture(t, models.MediaTypeBoth)
	setPairSettings(t, settings, ctx, map[string]string{
		"import.drop_folder":      dropDir,
		"import.drop_pair_gating": "true",
	})

	// Hold the ebook.
	ebookDir := writeEbookDownload(t)
	dl := newPairDownload(t, downloads, book, "ebook", ctx)
	s.tryImportInternal(ctx, dl, ebookDir, "", "", "", nil, nil)
	assertStatus(t, downloads, ctx, dl.GUID, models.StateImportHeld)

	// Backdate the hold-start (completed_at) well past the default 72h timeout.
	old := time.Now().Add(-100 * time.Hour).UTC()
	if _, err := database.ExecContext(ctx, "UPDATE downloads SET completed_at=? WHERE id=?", old, dl.ID); err != nil {
		t.Fatalf("backdate completed_at: %v", err)
	}

	s.sweepHeldPairGating(ctx)

	dst := filepath.Join(dropDir, "Title T - Author A.epub")
	if _, err := os.Stat(dst); err != nil {
		t.Fatalf("timeout: held ebook should be dropped alone, missing at %s: %v", dst, err)
	}
	assertStatus(t, downloads, ctx, dl.GUID, models.StateImportExternal)
}

// A held format that has NOT yet aged out is left held by the sweep.
func TestPairGating_TimeoutNotReachedKeepsHeld(t *testing.T) {
	s, settings, downloads, _, _, dropDir, book, ctx := pairFixture(t, models.MediaTypeBoth)
	setPairSettings(t, settings, ctx, map[string]string{
		"import.drop_folder":      dropDir,
		"import.drop_pair_gating": "true",
	})

	ebookDir := writeEbookDownload(t)
	dl := newPairDownload(t, downloads, book, "ebook", ctx)
	s.tryImportInternal(ctx, dl, ebookDir, "", "", "", nil, nil)
	assertStatus(t, downloads, ctx, dl.GUID, models.StateImportHeld)

	// completed_at is unset and added_at is ~now, so the fallback age is tiny.
	s.sweepHeldPairGating(ctx)

	assertEmptyDir(t, dropDir, "drop dir")
	assertStatus(t, downloads, ctx, dl.GUID, models.StateImportHeld)
}

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

// writeSized writes a file of the given size (content is irrelevant — only the
// extension and size drive the video guard).
func writeSized(t *testing.T, path string, size int) {
	t.Helper()
	if err := os.WriteFile(path, make([]byte, size), 0o600); err != nil {
		t.Fatal(err)
	}
}

func TestLargestFileIsVideo(t *testing.T) {
	t.Run("walk mode: video-dominated movie folder", func(t *testing.T) {
		dir := t.TempDir()
		writeSized(t, filepath.Join(dir, "Unrelated.Release.WEBRip.1080p.x265.mkv"), 4096)
		writeSized(t, filepath.Join(dir, "soundtrack.mp3"), 512)
		if !largestFileIsVideo(dir, nil) {
			t.Error("largest file is an .mkv — want true")
		}
	})
	t.Run("walk mode: legitimate audiobook folder with cover art", func(t *testing.T) {
		dir := t.TempDir()
		writeSized(t, filepath.Join(dir, "Example Book.m4b"), 4096)
		writeSized(t, filepath.Join(dir, "cover.jpg"), 512)
		if largestFileIsVideo(dir, nil) {
			t.Error("largest file is an .m4b — want false")
		}
	})
	t.Run("explicit file list wins over shared download root", func(t *testing.T) {
		// Torrent case (#903): downloadPath is a shared root holding an
		// unrelated sibling's video; only the explicit list may be judged.
		root := t.TempDir()
		writeSized(t, filepath.Join(root, "Some.Other.Movie.mkv"), 8192)
		bookFile := filepath.Join(root, "Example Book.epub")
		writeSized(t, bookFile, 1024)
		if largestFileIsVideo(root, []string{bookFile}) {
			t.Error("explicit list holds only the .epub — the sibling .mkv must not be considered")
		}
	})
	t.Run("empty dir is not video", func(t *testing.T) {
		if largestFileIsVideo(t.TempDir(), nil) {
			t.Error("empty dir should never report video")
		}
	})
}

func videoGuardFixture(t *testing.T) (s *Scanner, downloads *db.DownloadRepo, dl *models.Download, downloadDir string, ctx context.Context) {
	t.Helper()
	database, err := db.OpenMemory()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { database.Close() })
	ctx = context.Background()
	downloads = db.NewDownloadRepo(database)
	books := db.NewBookRepo(database)
	authors := db.NewAuthorRepo(database)
	settings := db.NewSettingsRepo(database)
	s = NewScanner(downloads, db.NewDownloadClientRepo(database), books, authors,
		db.NewHistoryRepo(database), t.TempDir(), t.TempDir(), "", "", "")
	s.WithSettings(settings)
	if err := settings.Set(ctx, "import.mode", "copy"); err != nil {
		t.Fatal(err)
	}

	author := &models.Author{ForeignID: "OLA", Name: "Example Author", SortName: "Author, Example", Monitored: true, MetadataProvider: "openlibrary"}
	if err := authors.Create(ctx, author); err != nil {
		t.Fatal(err)
	}
	book := &models.Book{
		ForeignID: "OLB", AuthorID: author.ID, Title: "Example Book", SortTitle: "example book",
		Status: models.BookStatusWanted, Monitored: true, AnyEditionOK: true,
		MediaType: models.MediaTypeAudiobook, MetadataProvider: "openlibrary",
	}
	if err := books.Create(ctx, book); err != nil {
		t.Fatal(err)
	}

	// The #1591 shape: a movie folder whose lone soundtrack .mp3 passes book-file
	// discovery and tips format detection to audiobook.
	downloadDir = t.TempDir()
	writeSized(t, filepath.Join(downloadDir, "Unrelated.Release.WEBRip.1080p.x265.mkv"), 8192)
	writeSized(t, filepath.Join(downloadDir, "soundtrack.mp3"), 512)

	dl = &models.Download{GUID: "guid-video", Title: "Unrelated.Release.WEBRip.1080p.x265", BookID: &book.ID, Status: models.StateCompleted, NZBURL: "fake://url"}
	if err := downloads.Create(ctx, dl); err != nil {
		t.Fatal(err)
	}
	return s, downloads, dl, downloadDir, ctx
}

// TestImport_BlocksVideoDominatedDownload is the import-side regression test
// for #1591: a movie release whose only book-extension file is a soundtrack
// .mp3 must be blocked for manual review, not moved into the audiobook library
// and marked imported.
func TestImport_BlocksVideoDominatedDownload(t *testing.T) {
	s, downloads, dl, downloadDir, ctx := videoGuardFixture(t)

	s.tryImportInternal(ctx, dl, downloadDir, "", "", "", nil, nil)

	reloaded, err := downloads.GetByGUID(ctx, "guid-video")
	if err != nil {
		t.Fatal(err)
	}
	if reloaded.Status != models.StateImportBlocked {
		t.Fatalf("status = %q, want %q", reloaded.Status, models.StateImportBlocked)
	}
	if !strings.Contains(reloaded.ErrorMessage, "video") {
		t.Errorf("error message %q should name the video guard", reloaded.ErrorMessage)
	}
	// Nothing may have been moved or copied into either library dir.
	for _, root := range []string{s.libraryDir, s.audiobookDir} {
		_ = filepath.Walk(root, func(p string, info os.FileInfo, err error) error {
			if err == nil && !info.IsDir() {
				t.Errorf("file %s landed under %s despite the block", p, root)
			}
			return nil
		})
	}
}

// TestImport_FormatHintOverridesVideoGuard: an explicit format from manual
// import is a human decision — the guard must step aside.
func TestImport_FormatHintOverridesVideoGuard(t *testing.T) {
	s, downloads, dl, downloadDir, ctx := videoGuardFixture(t)

	s.tryImportInternal(ctx, dl, downloadDir, "", "", models.MediaTypeAudiobook, nil, nil)

	reloaded, err := downloads.GetByGUID(ctx, "guid-video")
	if err != nil {
		t.Fatal(err)
	}
	if reloaded.Status == models.StateImportBlocked {
		t.Fatalf("explicit formatHint must bypass the video guard, got %q: %s", reloaded.Status, reloaded.ErrorMessage)
	}
}

package importer

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/vavallee/bindery/internal/db"
	"github.com/vavallee/bindery/internal/models"
)

// fakeABSNotifier records every ScanLibrary call so tests can assert that
// the Scanner triggers an ABS scan after a successful audiobook import.
type fakeABSNotifier struct {
	scanCalls []string // libraryID passed to each ScanLibrary call
	err       error
}

func (f *fakeABSNotifier) ScanLibrary(_ context.Context, libraryID string) error {
	f.scanCalls = append(f.scanCalls, libraryID)
	return f.err
}

// absAudiobookFixture sets up an in-memory DB with a real author, book, and
// download record so that tryImportInternal can run the full audiobook path
// without panicking on a nil book pointer.
func absAudiobookFixture(t *testing.T, audiobookDir string) (
	s *Scanner,
	dl *models.Download,
	dlRepo *db.DownloadRepo,
	ctx context.Context,
) {
	t.Helper()
	database, err := db.OpenMemory()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { database.Close() })

	ctx = context.Background()
	bookRepo := db.NewBookRepo(database)
	authorRepo := db.NewAuthorRepo(database)
	histRepo := db.NewHistoryRepo(database)
	dlRepo = db.NewDownloadRepo(database)
	clientRepo := db.NewDownloadClientRepo(database)

	libraryDir := t.TempDir()
	s = NewScanner(dlRepo, clientRepo, bookRepo, authorRepo, histRepo, libraryDir, audiobookDir, "", "", "")

	author := &models.Author{ForeignID: "OL-abs-test", Name: "Blake Crouch", SortName: "Crouch, Blake"}
	if err := authorRepo.Create(ctx, author); err != nil {
		t.Fatal(err)
	}
	book := &models.Book{
		ForeignID: "OL-abs-book",
		AuthorID:  author.ID,
		Title:     "Recursion",
		Status:    models.BookStatusWanted,
		MediaType: models.MediaTypeAudiobook,
	}
	if err := bookRepo.Create(ctx, book); err != nil {
		t.Fatal(err)
	}
	dl = &models.Download{
		GUID:   "guid-abs-notify-test",
		Title:  book.Title,
		BookID: &book.ID,
		Status: models.StateCompleted,
		NZBURL: "fake://url",
	}
	if err := dlRepo.Create(ctx, dl); err != nil {
		t.Fatal(err)
	}
	return s, dl, dlRepo, ctx
}

// TestTryImportInternal_NotifiesABSAfterAudiobookImport is the regression test
// for Bug #10: after a successful audiobook import, Bindery must trigger an ABS
// library scan so the new item is surfaced promptly rather than waiting for the
// next scheduled ABS scan (which defaults to every 24 h).
func TestTryImportInternal_NotifiesABSAfterAudiobookImport(t *testing.T) {
	t.Parallel()

	audiobookDir := t.TempDir()
	downloadPath := t.TempDir()
	if err := os.WriteFile(filepath.Join(downloadPath, "book.m4b"), []byte("audio-data"), 0o644); err != nil {
		t.Fatal(err)
	}

	s, dl, _, ctx := absAudiobookFixture(t, audiobookDir)

	notifier := &fakeABSNotifier{}
	const wantLibraryID = "lib-audiobooks-123"
	s.WithABSNotifier(notifier, func() []string { return []string{wantLibraryID} })

	s.tryImportInternal(ctx, dl, downloadPath, "qbittorrent", "abc123", nil, nil)

	if len(notifier.scanCalls) != 1 {
		t.Fatalf("expected 1 ScanLibrary call after audiobook import, got %d", len(notifier.scanCalls))
	}
	if notifier.scanCalls[0] != wantLibraryID {
		t.Errorf("ScanLibrary called with libraryID=%q, want %q", notifier.scanCalls[0], wantLibraryID)
	}
}

func TestTryImportInternal_NotifiesAllConfiguredABSLibraries(t *testing.T) {
	t.Parallel()

	audiobookDir := t.TempDir()
	downloadPath := t.TempDir()
	if err := os.WriteFile(filepath.Join(downloadPath, "book.m4b"), []byte("audio-data"), 0o644); err != nil {
		t.Fatal(err)
	}

	s, dl, _, ctx := absAudiobookFixture(t, audiobookDir)

	notifier := &fakeABSNotifier{}
	s.WithABSNotifier(notifier, func() []string { return []string{"lib-books", "lib-audio", "lib-books", ""} })

	s.tryImportInternal(ctx, dl, downloadPath, "qbittorrent", "abc123", nil, nil)

	if got, want := len(notifier.scanCalls), 2; got != want {
		t.Fatalf("ScanLibrary calls = %d, want %d: %v", got, want, notifier.scanCalls)
	}
	if notifier.scanCalls[0] != "lib-books" || notifier.scanCalls[1] != "lib-audio" {
		t.Fatalf("ScanLibrary calls = %v, want lib-books then lib-audio", notifier.scanCalls)
	}
}

// TestTryImportInternal_DoesNotNotifyABSForEbookImport verifies that the ABS
// notifier is NOT called when an ebook (not an audiobook) is imported.
func TestTryImportInternal_DoesNotNotifyABSForEbookImport(t *testing.T) {
	t.Parallel()

	libraryDir := t.TempDir()
	downloadPath := t.TempDir()
	if err := os.WriteFile(filepath.Join(downloadPath, "dune.epub"), []byte("epub-data"), 0o644); err != nil {
		t.Fatal(err)
	}

	database, err := db.OpenMemory()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { database.Close() })

	ctx := context.Background()
	dlRepo := db.NewDownloadRepo(database)
	clientRepo := db.NewDownloadClientRepo(database)
	bookRepo := db.NewBookRepo(database)
	authorRepo := db.NewAuthorRepo(database)
	histRepo := db.NewHistoryRepo(database)

	s := NewScanner(dlRepo, clientRepo, bookRepo, authorRepo, histRepo, libraryDir, "", "", "", "")

	notifier := &fakeABSNotifier{}
	s.WithABSNotifier(notifier, func() []string { return []string{"lib-audiobooks-123"} })

	dl := &models.Download{
		Title:  "Dune",
		GUID:   "test-guid-ebook",
		Status: models.StateCompleted,
	}
	if err := dlRepo.Create(ctx, dl); err != nil {
		t.Fatal(err)
	}

	s.tryImportInternal(ctx, dl, downloadPath, "qbittorrent", "abc123", nil, nil)

	if len(notifier.scanCalls) != 0 {
		t.Fatalf("expected 0 ScanLibrary calls for ebook import, got %d: %v", len(notifier.scanCalls), notifier.scanCalls)
	}
}

// TestTryImportInternal_ABSNotifySkippedWhenNoLibraryID verifies that when the
// ABS library ID resolver returns an empty string (ABS not configured), the
// notifier is not called and the import succeeds normally.
func TestTryImportInternal_ABSNotifySkippedWhenNoLibraryID(t *testing.T) {
	t.Parallel()

	audiobookDir := t.TempDir()
	downloadPath := t.TempDir()
	if err := os.WriteFile(filepath.Join(downloadPath, "recursion.m4b"), []byte("audio"), 0o644); err != nil {
		t.Fatal(err)
	}

	s, dl, _, ctx := absAudiobookFixture(t, audiobookDir)

	notifier := &fakeABSNotifier{}
	// Library ID resolver returns "" — ABS not configured.
	s.WithABSNotifier(notifier, func() []string { return nil })

	s.tryImportInternal(ctx, dl, downloadPath, "qbittorrent", "abc123", nil, nil)

	if len(notifier.scanCalls) != 0 {
		t.Fatalf("expected 0 ScanLibrary calls when libraryID is empty, got %d", len(notifier.scanCalls))
	}
}

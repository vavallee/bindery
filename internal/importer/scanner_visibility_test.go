package importer

// Tests for the library-scan visibility fields added in
// feat/library-scan-visibility: writeScanResult now records the resolved
// roots that were walked, an explicit no_files_found signal, and the
// individual library_dir / audiobook_dir so the UI can name the offending
// path when a new user points BINDERY_LIBRARY_DIR at the wrong place.

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/vavallee/bindery/internal/db"
	"github.com/vavallee/bindery/internal/models"
)

// scanResultPayload mirrors the JSON persisted under "library.lastScan".
type scanResultPayload struct {
	RanAt        string   `json:"ran_at"`
	FilesFound   int      `json:"files_found"`
	Reconciled   int      `json:"reconciled"`
	Unmatched    int      `json:"unmatched"`
	AlreadyTrack int      `json:"already_tracked"`
	TagReadFail  int      `json:"tag_read_failed"`
	LibraryDir   string   `json:"library_dir"`
	AudiobookDir string   `json:"audiobook_dir"`
	ScannedPaths []string `json:"scanned_paths"`
	NoFilesFound bool     `json:"no_files_found"`
	ScanError    string   `json:"scan_error"`
}

// visibilityFixture wires a Scanner with a SettingsRepo attached so the scan
// result is actually persisted and can be read back.
func visibilityFixture(t *testing.T, libraryDir, audiobookDir string) (*Scanner, *db.BookRepo, *db.AuthorRepo, *db.SettingsRepo, context.Context) {
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
	settings := db.NewSettingsRepo(database)

	s := NewScanner(downloads, clients, books, authors, history, libraryDir, audiobookDir, "", "", "").
		WithSettings(settings)
	return s, books, authors, settings, context.Background()
}

func readScanResult(t *testing.T, ctx context.Context, settings *db.SettingsRepo) scanResultPayload {
	t.Helper()
	setting, err := settings.Get(ctx, "library.lastScan")
	if err != nil {
		t.Fatalf("get library.lastScan: %v", err)
	}
	if setting == nil {
		t.Fatal("expected library.lastScan to be persisted, got nil")
	}
	var p scanResultPayload
	if err := json.Unmarshal([]byte(setting.Value), &p); err != nil {
		t.Fatalf("unmarshal scan result %q: %v", setting.Value, err)
	}
	return p
}

// TestScanLibrary_EmptyDirSurfacesPathAndZeroSignal is the new-user repro:
// BINDERY_LIBRARY_DIR points at an empty (or wrong) directory. The scan must
// report 0 files AND surface which path it walked, plus the explicit
// no_files_found signal so the UI can warn precisely.
func TestScanLibrary_EmptyDirSurfacesPathAndZeroSignal(t *testing.T) {
	libDir := t.TempDir() // exists but contains no book files
	s, _, _, settings, ctx := visibilityFixture(t, libDir, "")

	s.ScanLibrary(ctx)

	p := readScanResult(t, ctx, settings)
	if p.FilesFound != 0 {
		t.Errorf("FilesFound = %d, want 0 for an empty library dir", p.FilesFound)
	}
	if !p.NoFilesFound {
		t.Error("NoFilesFound must be true when the scan finds zero files")
	}
	if p.LibraryDir != libDir {
		t.Errorf("LibraryDir = %q, want %q (the scan must name the path it walked)", p.LibraryDir, libDir)
	}
	if len(p.ScannedPaths) == 0 || p.ScannedPaths[0] != libDir {
		t.Errorf("ScannedPaths = %v, want it to include %q", p.ScannedPaths, libDir)
	}
}

// TestScanLibrary_NonexistentDirSurfacesPath covers the case where the
// configured library dir does not exist at all (a common container
// misconfiguration). filepath.Walk fails, zero files are found, and the path
// is still surfaced.
func TestScanLibrary_NonexistentDirSurfacesPath(t *testing.T) {
	missing := filepath.Join(t.TempDir(), "does-not-exist")
	s, _, _, settings, ctx := visibilityFixture(t, missing, "")

	s.ScanLibrary(ctx)

	p := readScanResult(t, ctx, settings)
	if p.FilesFound != 0 {
		t.Errorf("FilesFound = %d, want 0 for a nonexistent dir", p.FilesFound)
	}
	if !p.NoFilesFound {
		t.Error("NoFilesFound must be true for a nonexistent dir")
	}
	if p.LibraryDir != missing {
		t.Errorf("LibraryDir = %q, want %q", p.LibraryDir, missing)
	}
}

// TestScanLibrary_FilesFoundNoMatchReportsUnmatched is the empty-catalogue
// repro: files exist on disk but there are no candidate books (e.g. after a
// plain CSV import that didn't fetch books). Every file comes back unmatched,
// reconciled stays 0, and the scanned path is surfaced (no false zero signal).
func TestScanLibrary_FilesFoundNoMatchReportsUnmatched(t *testing.T) {
	libDir := t.TempDir()
	epub := filepath.Join(libDir, "Some Orphan Book.epub")
	if err := os.WriteFile(epub, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	// Intentionally create NO books — the catalogue is empty.
	s, _, _, settings, ctx := visibilityFixture(t, libDir, "")

	s.ScanLibrary(ctx)

	p := readScanResult(t, ctx, settings)
	if p.FilesFound != 1 {
		t.Errorf("FilesFound = %d, want 1", p.FilesFound)
	}
	if p.NoFilesFound {
		t.Error("NoFilesFound must be false when files were found")
	}
	if p.Reconciled != 0 {
		t.Errorf("Reconciled = %d, want 0 (no candidate books exist)", p.Reconciled)
	}
	if p.Unmatched < 1 {
		t.Errorf("Unmatched = %d, want >= 1 when files exist but no book matches", p.Unmatched)
	}
	if p.LibraryDir != libDir {
		t.Errorf("LibraryDir = %q, want %q", p.LibraryDir, libDir)
	}
}

// TestScanLibrary_SeparateAudiobookDirInScannedPaths verifies that when a
// distinct audiobook root is configured both roots are surfaced in
// scanned_paths and audiobook_dir.
func TestScanLibrary_SeparateAudiobookDirInScannedPaths(t *testing.T) {
	libDir := t.TempDir()
	abDir := t.TempDir()
	s, _, _, settings, ctx := visibilityFixture(t, libDir, abDir)

	s.ScanLibrary(ctx)

	p := readScanResult(t, ctx, settings)
	if p.AudiobookDir != abDir {
		t.Errorf("AudiobookDir = %q, want %q", p.AudiobookDir, abDir)
	}
	if len(p.ScannedPaths) != 2 {
		t.Fatalf("ScannedPaths = %v, want both roots", p.ScannedPaths)
	}
	if p.ScannedPaths[0] != libDir || p.ScannedPaths[1] != abDir {
		t.Errorf("ScannedPaths = %v, want [%q %q]", p.ScannedPaths, libDir, abDir)
	}
}

// TestScanLibrary_UnsetLibraryDirWritesError is the #965 repro for a literally
// unset library directory. Previously ScanLibrary returned without writing any
// result, so the UI kept showing a stale prior scan and the #962 "no files
// found" warning never fired. The scan must now persist a result carrying a
// scan_error and the zero-files signal.
func TestScanLibrary_UnsetLibraryDirWritesError(t *testing.T) {
	s, _, _, settings, ctx := visibilityFixture(t, "", "")

	s.ScanLibrary(ctx)

	p := readScanResult(t, ctx, settings)
	if p.ScanError == "" {
		t.Error("ScanError must be set when the library dir is unset")
	}
	if !p.NoFilesFound {
		t.Error("NoFilesFound must be true when the library dir is unset")
	}
	if p.FilesFound != 0 {
		t.Errorf("FilesFound = %d, want 0", p.FilesFound)
	}
}

// TestScanLibrary_BookListErrorWritesError is the #965 repro for the second
// early return: the book listing fails. To exercise it the BookRepo is built on
// a CLOSED database (so books.List errors) while the SettingsRepo uses a live
// database (so the failed-scan result can still be persisted and read back).
func TestScanLibrary_BookListErrorWritesError(t *testing.T) {
	// Library dir with one book file so the scan gets past the zero-files early
	// return and reaches the books.List call.
	libDir := t.TempDir()
	epub := filepath.Join(libDir, "Some Book.epub")
	if err := os.WriteFile(epub, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}

	// Live DB backs everything the scanner needs to actually persist the result.
	liveDB, err := db.OpenMemory()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { liveDB.Close() })

	// Separate DB that we close immediately so its BookRepo.List errors.
	deadDB, err := db.OpenMemory()
	if err != nil {
		t.Fatal(err)
	}
	if err := deadDB.Close(); err != nil {
		t.Fatalf("close dead db: %v", err)
	}

	books := db.NewBookRepo(deadDB) // List() will error
	authors := db.NewAuthorRepo(liveDB)
	history := db.NewHistoryRepo(liveDB)
	downloads := db.NewDownloadRepo(liveDB)
	clients := db.NewDownloadClientRepo(liveDB)
	settings := db.NewSettingsRepo(liveDB)
	s := NewScanner(downloads, clients, books, authors, history, libDir, "", "", "", "").
		WithSettings(settings)
	ctx := context.Background()

	s.ScanLibrary(ctx)

	p := readScanResult(t, ctx, settings)
	if p.ScanError == "" {
		t.Error("ScanError must be set when the book listing fails")
	}
	if p.FilesFound != 0 {
		t.Errorf("FilesFound = %d, want 0 on the failed-scan result", p.FilesFound)
	}
}

// guard: keep models import used even if reconcile assertions change.
var _ = models.BookStatusWanted

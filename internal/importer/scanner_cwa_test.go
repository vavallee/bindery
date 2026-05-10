package importer

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/vavallee/bindery/internal/db"
)

// cwaFixture extends importScannerFixture with a SettingsRepo wired in,
// since the cwa.ingest_path lookup goes through settings. Returns the
// scanner, its settings repo (so tests can write cwa.ingest_path), the
// configured ingest dir, and a path to a freshly written source file
// inside the library dir that tests can hand to pushToCWA.
func cwaFixture(t *testing.T) (s *Scanner, settings *db.SettingsRepo, ingestDir, srcPath string, ctx context.Context) {
	t.Helper()
	database, err := db.OpenMemory()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { database.Close() })

	ctx = context.Background()
	settings = db.NewSettingsRepo(database)
	libraryDir := t.TempDir()
	ingestDir = t.TempDir()

	srcPath = filepath.Join(libraryDir, "Author A - Title T.epub")
	if err := os.WriteFile(srcPath, []byte("epub bytes"), 0o644); err != nil {
		t.Fatalf("seed src: %v", err)
	}

	s = NewScanner(
		db.NewDownloadRepo(database), db.NewDownloadClientRepo(database),
		db.NewBookRepo(database), db.NewAuthorRepo(database), db.NewHistoryRepo(database),
		libraryDir, "", "", "", "",
	)
	s.WithSettings(settings)
	return s, settings, ingestDir, srcPath, ctx
}

// TestPushToCWA_DisabledByEmptyPath: cwa.ingest_path unset is the default
// for fresh installs and must be a no-op — no copy, no error.
func TestPushToCWA_DisabledByEmptyPath(t *testing.T) {
	s, _, ingestDir, srcPath, ctx := cwaFixture(t)
	// Don't write the setting at all — value is "" by default.

	s.pushToCWA(ctx, srcPath)

	entries, _ := os.ReadDir(ingestDir)
	if len(entries) != 0 {
		t.Errorf("ingest dir should be empty when cwa.ingest_path is unset, got %d entries", len(entries))
	}
}

// TestPushToCWA_NoSettings: scanner built without WithSettings (e.g. in
// migrate-only invocations) must not panic on a nil settings repo.
func TestPushToCWA_NoSettings(t *testing.T) {
	s := NewScanner(nil, nil, nil, nil, nil, t.TempDir(), "", "", "", "")
	// No WithSettings call — s.settings is nil.
	s.pushToCWA(context.Background(), "/library/anything.epub") // must not panic
}

// TestPushToCWA_HappyPath: when cwa.ingest_path resolves to a writable dir,
// the source file lands at <ingest>/<basename>.
func TestPushToCWA_HappyPath(t *testing.T) {
	s, settings, ingestDir, srcPath, ctx := cwaFixture(t)
	if err := settings.Set(ctx, "cwa.ingest_path", ingestDir); err != nil {
		t.Fatalf("set ingest path: %v", err)
	}

	s.pushToCWA(ctx, srcPath)

	dst := filepath.Join(ingestDir, filepath.Base(srcPath))
	got, err := os.ReadFile(dst)
	if err != nil {
		t.Fatalf("ingest copy not found at %s: %v", dst, err)
	}
	if string(got) != "epub bytes" {
		t.Errorf("ingest copy contents = %q, want %q", got, "epub bytes")
	}
	// Source must still exist — copy semantics, not move.
	if _, err := os.Stat(srcPath); err != nil {
		t.Errorf("source file removed after pushToCWA, expected copy semantics: %v", err)
	}
}

// TestPushToCWA_MissingSourceSwallowed: if the source file disappears
// between the import and the push (concurrent cleanup, NFS race, etc.)
// the failure must be logged but not propagated — never roll back an
// otherwise-good bindery import.
func TestPushToCWA_MissingSourceSwallowed(t *testing.T) {
	s, settings, ingestDir, srcPath, ctx := cwaFixture(t)
	if err := settings.Set(ctx, "cwa.ingest_path", ingestDir); err != nil {
		t.Fatalf("set ingest path: %v", err)
	}

	// Hand pushToCWA a path to a file that does not exist — CopyFileCtx
	// should fail, the warn log should fire, and the function should
	// return cleanly (no panic, no bubble).
	s.pushToCWA(ctx, srcPath+".does-not-exist")
}

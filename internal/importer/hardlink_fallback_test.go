//go:build !windows

package importer

import (
	"context"
	"os"
	"path/filepath"
	"syscall"
	"testing"
)

// forceEXDEV makes osLink behave as if every link crosses a device boundary,
// the way os.Link does for separate bind mounts / Unraid user shares. Restored
// on test cleanup.
func forceEXDEV(t *testing.T) {
	t.Helper()
	orig := osLink
	osLink = func(_, _ string) error {
		return &os.LinkError{Op: "link", Err: syscall.EXDEV}
	}
	t.Cleanup(func() { osLink = orig })
}

// TestHardlinkFile_CrossDeviceFallsBackToCopy is the regression test for the
// "stage hardlink: invalid cross-device link" import failure: when os.Link
// returns EXDEV, the import must degrade to a copy (seeding-safe) instead of
// failing, and the result must be an independent file, not a shared inode.
func TestHardlinkFile_CrossDeviceFallsBackToCopy(t *testing.T) {
	forceEXDEV(t)
	dir := t.TempDir()
	src := filepath.Join(dir, "src.epub")
	if err := os.WriteFile(src, []byte("seed-payload"), 0o644); err != nil {
		t.Fatal(err)
	}
	dst := filepath.Join(dir, "library", "Author", "dst.epub")

	if err := HardlinkFile(src, dst); err != nil {
		t.Fatalf("HardlinkFile should fall back to copy on EXDEV, got: %v", err)
	}

	// Source must survive so the download client keeps seeding.
	si, err := os.Stat(src)
	if err != nil {
		t.Fatalf("source removed by fallback: %v", err)
	}
	got, err := os.ReadFile(dst)
	if err != nil {
		t.Fatalf("read dst: %v", err)
	}
	if string(got) != "seed-payload" {
		t.Fatalf("dst contents = %q, want seed-payload", got)
	}
	// It must be a real copy, not a hardlink (different inode).
	di, err := os.Stat(dst)
	if err != nil {
		t.Fatal(err)
	}
	if os.SameFile(si, di) {
		t.Fatal("expected an independent copy, got a shared inode")
	}
}

// TestStagedImport_HardlinkCrossDeviceFallsBackToCopy covers the same fallback
// on the primary single-file book import path (StagedImport), and verifies the
// staged file commits to dst with the source preserved.
func TestStagedImport_HardlinkCrossDeviceFallsBackToCopy(t *testing.T) {
	forceEXDEV(t)
	dir := t.TempDir()
	src := filepath.Join(dir, "download", "book.epub")
	if err := os.MkdirAll(filepath.Dir(src), 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(src, []byte("book-bytes"), 0o644); err != nil {
		t.Fatal(err)
	}
	dst := filepath.Join(dir, "library", "book.epub")
	if err := os.MkdirAll(filepath.Dir(dst), 0o750); err != nil {
		t.Fatal(err)
	}

	staged, commit, rollback, err := StagedImport(context.Background(), "hardlink", src, dst)
	if err != nil {
		t.Fatalf("StagedImport hardlink EXDEV fallback: %v", err)
	}
	if staged == "" || commit == nil || rollback == nil {
		t.Fatal("StagedImport returned nil commit/rollback")
	}
	if err := commit(); err != nil {
		t.Fatalf("commit: %v", err)
	}
	if _, err := os.Stat(src); err != nil {
		t.Fatalf("source removed (hardlink/copy must preserve seeding): %v", err)
	}
	got, err := os.ReadFile(dst)
	if err != nil {
		t.Fatalf("read committed dst: %v", err)
	}
	if string(got) != "book-bytes" {
		t.Fatalf("committed dst = %q, want book-bytes", got)
	}
}

// TestHardlinkable_RealProbe confirms the probe reports true for two paths that
// genuinely support hardlinks (same temp dir) and false for empty input.
func TestHardlinkable_RealProbe(t *testing.T) {
	dir := t.TempDir()
	if !hardlinkable(dir, dir) {
		t.Fatal("same directory should be reported hardlinkable")
	}
	if hardlinkable("", dir) || hardlinkable(dir, "") {
		t.Fatal("empty path must not be reported hardlinkable")
	}
}

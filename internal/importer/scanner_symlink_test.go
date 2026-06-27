package importer

import (
	"os"
	"path/filepath"
	"testing"
)

// TestDiscoverBookFiles_SkipsSymlinks is the security regression for the
// arbitrary-file-read primitive: a malicious release that ships a
// book-extension symlink pointing at an arbitrary file must not be collected
// for import (which would copy the link target's bytes into the library).
func TestDiscoverBookFiles_SkipsSymlinks(t *testing.T) {
	dir := t.TempDir()

	real := filepath.Join(dir, "real.epub")
	if err := os.WriteFile(real, []byte("book"), 0o644); err != nil {
		t.Fatal(err)
	}
	secret := filepath.Join(dir, "secret.txt")
	if err := os.WriteFile(secret, []byte("SECRET"), 0o644); err != nil {
		t.Fatal(err)
	}
	evil := filepath.Join(dir, "evil.epub")
	if err := os.Symlink(secret, evil); err != nil {
		t.Skipf("symlinks unsupported on this platform: %v", err)
	}

	has := func(files []string, want string) bool {
		for _, f := range files {
			if f == want {
				return true
			}
		}
		return false
	}

	// Walk path (no explicit file list).
	walked := discoverBookFiles(dir, nil)
	if !has(walked, real) {
		t.Errorf("walk: expected the real ebook to be discovered, got %v", walked)
	}
	if has(walked, evil) {
		t.Errorf("walk: symlinked file must be skipped, got %v", walked)
	}

	// Explicit-file path (download client's authoritative file list).
	explicit := discoverBookFiles(dir, []string{real, evil})
	if !has(explicit, real) {
		t.Errorf("explicit: expected the real ebook, got %v", explicit)
	}
	if has(explicit, evil) {
		t.Errorf("explicit: symlinked file must be skipped, got %v", explicit)
	}
}

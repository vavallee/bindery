//go:build unix

package importer

import (
	"os"
	"path/filepath"
	"syscall"
	"testing"
)

// TestPlacementHonorsDirMode runs an import placement with the umask cleared
// and BINDERY_DIR_MODE set, then stats the created author and book
// directories to confirm the configured mode is applied exactly. Without the
// umask cleared the process umask would mask the comparison, which is why the
// accepted value set only holds umask-surviving modes in production.
func TestPlacementHonorsDirMode(t *testing.T) {
	old := syscall.Umask(0)
	t.Cleanup(func() { syscall.Umask(old) })
	t.Setenv("BINDERY_DIR_MODE", "0755")

	base := t.TempDir()
	src := filepath.Join(base, "downloads", "book.epub")
	writeMoveFile(t, src, "content")
	authorDir := filepath.Join(base, "library", "Sarah J. Maas")
	dst := filepath.Join(authorDir, "A Court of Thorns and Roses (2013)", "book.epub")

	if err := MoveFile(src, dst); err != nil {
		t.Fatalf("MoveFile: %v", err)
	}

	for _, dir := range []string{authorDir, filepath.Dir(dst)} {
		info, err := os.Stat(dir)
		if err != nil {
			t.Fatalf("stat %s: %v", dir, err)
		}
		if got := info.Mode().Perm(); got != 0o755 {
			t.Fatalf("%s mode = %#o, want 0755", dir, got)
		}
	}
}

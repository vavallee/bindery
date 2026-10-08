package calibre

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// TestCalibredbStartFailures tells the two #1940 cases apart. A calibredb
// that is not there is "not installed". One that is there but cannot start,
// as a Calibre install mounted into the distroless image is (its loader or
// interpreter is missing, and the kernel reports that as ENOENT for the
// binary itself), was reported the same way, which sent users looking for a
// file that was right there.
func TestCalibredbStartFailures(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("needs a script whose interpreter is missing")
	}
	dir := t.TempDir()
	unrunnable := filepath.Join(dir, "calibredb")
	if err := os.WriteFile(unrunnable, []byte("#!/no/such/interpreter\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	missing := filepath.Join(dir, "absent", "calibredb")

	for _, tc := range []struct {
		name    string
		binary  string
		want    error
		notWant error
		text    string
	}{
		{"present but cannot start", unrunnable, ErrCalibredbCannotRun, ErrCalibredbMissing, "can't run in this image"},
		{"missing", missing, ErrCalibredbMissing, ErrCalibredbCannotRun, "not installed"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c := New(Config{Enabled: true, LibraryPath: t.TempDir(), BinaryPath: tc.binary})
			_, testErr := c.Test(context.Background())
			_, addErr := c.Add(context.Background(), filepath.Join(dir, "book.epub"), Metadata{})
			for name, err := range map[string]error{"Test": testErr, "Add": addErr} {
				if !errors.Is(err, tc.want) || errors.Is(err, tc.notWant) {
					t.Errorf("%s: got %v, want %v", name, err, tc.want)
				}
				if !IsCalibredbUnusable(err) {
					t.Errorf("%s: %v must count as unusable", name, err)
				}
				if err == nil || !strings.Contains(err.Error(), tc.text) || !strings.Contains(err.Error(), "Calibre Bridge plugin") {
					t.Errorf("%s: message %q must say %q and name the Bridge plugin", name, err, tc.text)
				}
			}
		})
	}
}

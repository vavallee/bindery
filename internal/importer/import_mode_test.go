package importer

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/vavallee/bindery/internal/db"
)

// TestHardlinkFile verifies that HardlinkFile creates a hard link so both
// paths refer to the same inode, and that the source is not removed.
func TestHardlinkFile(t *testing.T) {
	dir := t.TempDir()
	src := filepath.Join(dir, "src.epub")
	dst := filepath.Join(dir, "subdir", "dst.epub")

	if err := os.WriteFile(src, []byte("content"), 0o644); err != nil {
		t.Fatal(err)
	}

	if err := HardlinkFile(src, dst); err != nil {
		t.Fatalf("HardlinkFile: %v", err)
	}

	// Both files must exist.
	if _, err := os.Stat(src); err != nil {
		t.Error("source was removed after hardlink — source must survive")
	}
	if _, err := os.Stat(dst); err != nil {
		t.Errorf("destination not found: %v", err)
	}

	// Same inode = true hardlink.
	srcInfo, _ := os.Stat(src)
	dstInfo, _ := os.Stat(dst)
	if !os.SameFile(srcInfo, dstInfo) {
		t.Error("src and dst are not the same inode — hardlink not created")
	}
}

// TestCopyFileMode verifies that CopyFile duplicates the file and leaves the source intact.
func TestCopyFileMode(t *testing.T) {
	dir := t.TempDir()
	src := filepath.Join(dir, "src.epub")
	dst := filepath.Join(dir, "subdir", "dst.epub")
	content := []byte("book content")

	if err := os.WriteFile(src, content, 0o644); err != nil {
		t.Fatal(err)
	}

	if err := CopyFile(src, dst); err != nil {
		t.Fatalf("CopyFile: %v", err)
	}

	if _, err := os.Stat(src); err != nil {
		t.Error("source was removed after copy — source must survive")
	}

	got, err := os.ReadFile(dst)
	if err != nil {
		t.Fatalf("read dst: %v", err)
	}
	if string(got) != string(content) {
		t.Errorf("dst content = %q, want %q", got, content)
	}

	srcInfo, _ := os.Stat(src)
	dstInfo, _ := os.Stat(dst)
	if os.SameFile(srcInfo, dstInfo) {
		t.Error("src and dst share an inode — expected an independent copy, not a hardlink")
	}
}

// TestHardlinkDir verifies that HardlinkDir mirrors a directory tree by
// creating hard links for each file, and that the source tree is untouched.
func TestHardlinkDir(t *testing.T) {
	dir := t.TempDir()
	src := filepath.Join(dir, "audiobook")
	dst := filepath.Join(dir, "library", "audiobook")

	// Build a simple tree: root file + nested file.
	if err := os.MkdirAll(filepath.Join(src, "sub"), 0o750); err != nil {
		t.Fatal(err)
	}
	files := []string{
		filepath.Join(src, "part1.mp3"),
		filepath.Join(src, "sub", "part2.mp3"),
	}
	for _, f := range files {
		if err := os.WriteFile(f, []byte(filepath.Base(f)), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	if err := HardlinkDir(src, dst); err != nil {
		t.Fatalf("HardlinkDir: %v", err)
	}

	// Source must still exist.
	for _, f := range files {
		if _, err := os.Stat(f); err != nil {
			t.Errorf("source file removed: %s", f)
		}
	}

	// Destination files must exist and share inodes with source.
	rel := []string{"part1.mp3", filepath.Join("sub", "part2.mp3")}
	for i, r := range rel {
		dstFile := filepath.Join(dst, r)
		dstInfo, err := os.Stat(dstFile)
		if err != nil {
			t.Errorf("dst file not found: %s", dstFile)
			continue
		}
		srcInfo, _ := os.Stat(files[i])
		if !os.SameFile(srcInfo, dstInfo) {
			t.Errorf("%s: not the same inode — hardlink not created", r)
		}
	}
}

// TestCopyDirMode verifies that CopyDir duplicates a directory tree and leaves
// the source intact.
func TestCopyDirMode(t *testing.T) {
	dir := t.TempDir()
	src := filepath.Join(dir, "audiobook")
	dst := filepath.Join(dir, "library", "audiobook")

	if err := os.MkdirAll(filepath.Join(src, "sub"), 0o750); err != nil {
		t.Fatal(err)
	}
	srcFile := filepath.Join(src, "part1.mp3")
	if err := os.WriteFile(srcFile, []byte("audio"), 0o644); err != nil {
		t.Fatal(err)
	}

	if err := CopyDir(src, dst); err != nil {
		t.Fatalf("CopyDir: %v", err)
	}

	if _, err := os.Stat(srcFile); err != nil {
		t.Error("source removed after CopyDir — source must survive")
	}

	dstFile := filepath.Join(dst, "part1.mp3")
	got, err := os.ReadFile(dstFile)
	if err != nil {
		t.Fatalf("read dst file: %v", err)
	}
	if string(got) != "audio" {
		t.Errorf("dst content = %q, want %q", got, "audio")
	}
}

// TestImportMode_Default verifies that a Scanner with no settings falls back
// to "move" mode.
func TestImportMode_Default(t *testing.T) {
	s := &Scanner{}
	if got := s.importMode(nil); got != "move" { //nolint:staticcheck
		t.Errorf("importMode without settings = %q, want %q", got, "move")
	}
}

// TestImportMode_Settings exercises all branches of importMode when a real
// SettingsRepo is attached: "copy", "hardlink", unknown value, and absent key.
func TestImportMode_Settings(t *testing.T) {
	database, err := db.OpenMemory()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { database.Close() })

	ctx := context.Background()
	sr := db.NewSettingsRepo(database)

	s := &Scanner{}
	s.WithSettings(sr)

	cases := []struct {
		setValue string // "" means don't set the key
		want     string
	}{
		{setValue: "copy", want: "copy"},
		{setValue: "hardlink", want: "hardlink"},
		{setValue: "invalid", want: "move"}, // unknown value falls back to move
		{setValue: "", want: "move"},        // absent key falls back to move
	}

	for _, tc := range cases {
		// Reset the setting before each case.
		_ = sr.Delete(ctx, "import.mode")
		if tc.setValue != "" {
			if err := sr.Set(ctx, "import.mode", tc.setValue); err != nil {
				t.Fatalf("Set: %v", err)
			}
		}
		got := s.importMode(ctx)
		if got != tc.want {
			t.Errorf("setValue=%q: importMode = %q, want %q", tc.setValue, got, tc.want)
		}
	}
}

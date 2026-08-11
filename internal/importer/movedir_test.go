package importer

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestMoveDir(t *testing.T) {
	src := t.TempDir()
	// Simulate an audiobook release folder with multi-part m4b + cover.
	mustWrite(t, filepath.Join(src, "Part.01.m4b"), "part1")
	mustWrite(t, filepath.Join(src, "Part.02.m4b"), "part2")
	mustWrite(t, filepath.Join(src, "nested", "cover.jpg"), "cover")

	dst := filepath.Join(t.TempDir(), "Author", "Title (2020)")
	if err := MoveDir(src, dst); err != nil {
		t.Fatal(err)
	}

	// Source should be gone, destination should hold everything preserved.
	if _, err := os.Stat(src); !os.IsNotExist(err) {
		t.Errorf("source should have been removed, err = %v", err)
	}
	for _, name := range []string{"Part.01.m4b", "Part.02.m4b", "nested/cover.jpg"} {
		if _, err := os.Stat(filepath.Join(dst, name)); err != nil {
			t.Errorf("missing %s in destination: %v", name, err)
		}
	}
}

func TestMoveDirRefusesExistingDst(t *testing.T) {
	src := t.TempDir()
	mustWrite(t, filepath.Join(src, "x.m4b"), "x")
	dst := t.TempDir() // already exists
	if err := MoveDir(src, dst); err == nil {
		t.Error("expected error when dst already exists")
	}
}

// TestMoveDirRefusesDstInsideSrc is the regression test for #1809: a reorganize
// that turns a flat author folder into a per-book folder underneath it computes
// a destination inside the source. Rename cannot do that (EINVAL), so the
// copy-based slow path took over and recursed into the directory it had just
// created, copying the tree into itself until the disk (or the path length)
// gave out. Both the move and the copy entrypoint must refuse it up front.
func TestMoveDirRefusesDstInsideSrc(t *testing.T) {
	src := filepath.Join(t.TempDir(), "Jane Doe")
	mustWrite(t, filepath.Join(src, "part1.mp3"), "part1")
	dst := filepath.Join(src, "My Book (2020)")

	if err := MoveDir(src, dst); !errors.Is(err, ErrDestInsideSource) {
		t.Errorf("MoveDir err = %v, want ErrDestInsideSource", err)
	}
	if err := CopyDir(src, dst); !errors.Is(err, ErrDestInsideSource) {
		t.Errorf("CopyDir err = %v, want ErrDestInsideSource", err)
	}
	// Nothing copied, nothing created, source left exactly as it was.
	if _, err := os.Stat(filepath.Join(src, "part1.mp3")); err != nil {
		t.Errorf("source content must survive the refused move: %v", err)
	}
	if _, err := os.Stat(dst); !os.IsNotExist(err) {
		t.Errorf("destination should not have been created, stat err = %v", err)
	}
}

func mustWrite(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0644); err != nil {
		t.Fatal(err)
	}
}

// hardlinkDirRooted MkdirAlls the destination and then walks the source, so a
// nested destination recurses into the tree it is creating — the same hazard as
// the copy path (#1809). Unreachable from reorganize, which never hardlinks,
// but HardlinkDir is exported.
func TestHardlinkDirRefusesDstInsideSrc(t *testing.T) {
	src := t.TempDir()
	if err := os.WriteFile(filepath.Join(src, "book.m4b"), []byte("data"), 0o600); err != nil {
		t.Fatalf("seed: %v", err)
	}
	dst := filepath.Join(src, "Series", "Title")

	err := HardlinkDir(src, dst)
	if !errors.Is(err, ErrDestInsideSource) {
		t.Fatalf("HardlinkDir err = %v, want ErrDestInsideSource", err)
	}
	// Nothing created: the guard runs before MkdirAll.
	if _, statErr := os.Stat(filepath.Join(src, "Series")); !os.IsNotExist(statErr) {
		t.Error("the refused hardlink still created a directory inside the source")
	}
	if _, statErr := os.Stat(filepath.Join(src, "book.m4b")); statErr != nil {
		t.Errorf("source content disturbed: %v", statErr)
	}
}

// The filesystem decides what "same directory" means and dirContains cannot see
// it. On a case-insensitive volume /library/Author and /library/author are one
// directory, so a case-only difference must still be refused — otherwise the
// guard is bypassed by nothing more than a template that lowercases.
func TestDirContainsIsCaseInsensitiveToo(t *testing.T) {
	for _, tc := range []struct {
		name, dir, p string
		want         bool
	}{
		{"exact nesting", "/library/Author", "/library/Author/Series/Title", true},
		{"case-only difference", "/library/Author", "/library/author/Series/Title", true},
		{"upper vs lower root", "/Library/AUTHOR", "/library/author/Title", true},
		{"same dir is not containment", "/library/Author", "/library/Author", false},
		{"sibling", "/library/Author", "/library/Authors", false},
		{"sibling differing in case only", "/library/Author", "/library/AUTHORS", false},
		{"parent", "/library/Author/Book", "/library/Author", false},
		{"unrelated", "/library/A", "/other/B", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := dirContains(tc.dir, tc.p); got != tc.want {
				t.Errorf("dirContains(%q, %q) = %v, want %v", tc.dir, tc.p, got, tc.want)
			}
		})
	}
}

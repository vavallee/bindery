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
	if err := MoveDirCtx(t.Context(), src, dst); err != nil {
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
	if err := MoveDirCtx(t.Context(), src, dst); err == nil {
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

	if err := MoveDirCtx(t.Context(), src, dst); !errors.Is(err, ErrDestInsideSource) {
		t.Errorf("MoveDirCtx err = %v, want ErrDestInsideSource", err)
	}
	if err := CopyDirCtx(t.Context(), src, dst); !errors.Is(err, ErrDestInsideSource) {
		t.Errorf("CopyDirCtx err = %v, want ErrDestInsideSource", err)
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

// The filesystem decides what "same directory" means and checkDestNotInsideSource
// cannot see which one it is standing on. On APFS or a Windows bind mount
// /library/Author and /library/author are one directory, so a destination
// differing from the source only in case is the nesting this guard exists to
// refuse — and EvalSymlinks does not normalise case, so resolving misses it.
func TestDestInsideSourceIsCaughtAcrossCase(t *testing.T) {
	for _, tc := range []struct {
		name, src, dst string
		wantErr        bool
	}{
		{"exact nesting", "/library/Author", "/library/Author/Series/Title", true},
		{"case-only difference", "/library/Author", "/library/author/Series/Title", true},
		{"upper source, lower dest", "/Library/AUTHOR", "/library/author/Title", true},

		// Must stay allowed: these are not containment, in any case-folding.
		{"same path", "/library/Author", "/library/Author", false},
		{"same path, case-only", "/library/Author", "/library/author", false},
		{"sibling", "/library/Author", "/library/Authors", false},
		{"sibling, case-only", "/library/Author", "/library/AUTHORS", false},
		{"parent", "/library/Author/Book", "/library/Author", false},
		{"unrelated", "/library/A", "/other/B", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			err := checkDestNotInsideSource("move", tc.src, tc.dst)
			if tc.wantErr && !errors.Is(err, ErrDestInsideSource) {
				t.Errorf("checkDestNotInsideSource(%q, %q) = %v, want ErrDestInsideSource",
					tc.src, tc.dst, err)
			}
			if !tc.wantErr && err != nil {
				t.Errorf("checkDestNotInsideSource(%q, %q) refused a legal move: %v",
					tc.src, tc.dst, err)
			}
		})
	}
}

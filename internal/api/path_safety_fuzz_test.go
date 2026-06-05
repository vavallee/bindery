package api

import (
	"path/filepath"
	"strings"
	"testing"
)

// FuzzContainsUnderRoot exercises the lexical containment primitive that backs
// the delete- and import-path safety checks. It must never panic, and it must
// never report containment for a path that actually escapes the root (a false
// "contained" would let a delete/import operate outside the library). The
// function is pure (no filesystem access), so fuzzing it is hermetic.
func FuzzContainsUnderRoot(f *testing.F) {
	seeds := [][2]string{
		{"/lib/Author/book.epub", "/lib"},
		{"/lib", "/lib"},
		{"/etc/passwd", "/lib"},
		{"/lib/../etc/passwd", "/lib"},
		{"Author/book.epub", "/lib"},
		{"", ""},
		{"/lib/a/b/c", "/lib/a"},
		{"/libother/x", "/lib"},
		{"/lib/", "/lib"},
		{"\x00", "/lib"},
	}
	for _, s := range seeds {
		f.Add(s[0], s[1])
	}

	f.Fuzz(func(t *testing.T, p, root string) {
		got := containsUnderRoot(p, root) // must not panic

		// Invariant: if it claims containment, filepath.Rel(root, p) must not
		// climb out of root (no ".." prefix) — otherwise the "containment"
		// control is unsound.
		if got {
			rel, err := filepath.Rel(root, p)
			if err != nil {
				t.Fatalf("containsUnderRoot(%q,%q)=true but filepath.Rel errored: %v", p, root, err)
			}
			if rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
				t.Fatalf("containsUnderRoot(%q,%q)=true but rel %q escapes root", p, root, rel)
			}
		}
	})
}

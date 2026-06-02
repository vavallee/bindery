package api

import (
	"context"
	"path/filepath"
	"testing"
)

// TestLibraryRoots_Contains exercises the containment primitive directly so a
// regression in the rules (clean, rel, abs, "." sentinel) shows up here
// before any handler-level test even has to construct fixture files.
func TestLibraryRoots_Contains(t *testing.T) {
	root := t.TempDir()
	other := t.TempDir()
	roots := NewLibraryRoots(staticRootLister{paths: []string{root}})

	cases := []struct {
		name string
		path string
		want bool
	}{
		{"nested file under root", filepath.Join(root, "Author", "book.epub"), true},
		{"nested deeper", filepath.Join(root, "a", "b", "c", "d.epub"), true},
		{"root itself is not a deletable path", root, false},
		{"sibling temp dir outside root", filepath.Join(other, "x.epub"), false},
		{"absolute /etc/passwd", "/etc/passwd", false},
		{"relative path rejected outright", "Author/book.epub", false},
		{"empty path rejected", "", false},
		{"traversal attempt with ..", filepath.Join(root, "..", filepath.Base(other), "x.epub"), false},
		{"trailing slash on input still inside", filepath.Join(root, "Author") + string(filepath.Separator), true},
	}
	ctx := context.Background()
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := roots.Contains(ctx, c.path)
			if got != c.want {
				t.Errorf("Contains(%q) = %v, want %v", c.path, got, c.want)
			}
		})
	}
}

// TestLibraryRoots_NilReceiverAllows confirms the nil-receiver shortcut: a
// handler that hasn't been wired with WithRoots keeps the legacy behaviour
// (no containment check). Critical so the existing test fixtures that don't
// configure roots don't suddenly start rejecting all delete operations.
func TestLibraryRoots_NilReceiverAllows(t *testing.T) {
	var r *LibraryRoots
	if !r.Contains(context.Background(), "/anything") {
		t.Error("nil *LibraryRoots must report Contains = true")
	}
}

// TestLibraryRoots_DefaultsOnlyContains verifies the static-defaults path
// (the BINDERY_LIBRARY_DIR / BINDERY_AUDIOBOOK_DIR cover for installs that
// never created a root_folders row). No DB lister at all, just defaults.
func TestLibraryRoots_DefaultsOnlyContains(t *testing.T) {
	libraryDir := t.TempDir()
	audiobookDir := t.TempDir()
	roots := NewLibraryRoots(nil, libraryDir, audiobookDir, "")

	ctx := context.Background()
	if !roots.Contains(ctx, filepath.Join(libraryDir, "x.epub")) {
		t.Errorf("path under libraryDir default must be contained")
	}
	if !roots.Contains(ctx, filepath.Join(audiobookDir, "y.m4b")) {
		t.Errorf("path under audiobookDir default must be contained")
	}
	if roots.Contains(ctx, "/etc/passwd") {
		t.Errorf("/etc/passwd must not be contained")
	}
}

// TestLibraryRoots_NoConfigurationFallsOpen documents the deliberate
// fail-open when neither a DB lister nor static defaults are supplied.
// The production wiring always supplies at least one default, but legacy
// tests that construct handlers without WithRoots rely on this branch.
func TestLibraryRoots_NoConfigurationFallsOpen(t *testing.T) {
	roots := NewLibraryRoots(nil)
	if !roots.Contains(context.Background(), "/tmp/whatever.epub") {
		t.Error("no roots configured: Contains must fall open to preserve legacy behaviour")
	}
}

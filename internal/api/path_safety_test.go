package api

import (
	"context"
	"os"
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

// TestLibraryRoots_ResolveContained is the security-critical test for the
// manual-import path: a symlink that physically lives inside a library root but
// points outside it must be rejected, not silently followed. The old Contains
// had a lexical fallback that let such a symlink pass, turning the import
// endpoints into an arbitrary-file read/move primitive.
func TestLibraryRoots_ResolveContained(t *testing.T) {
	root := t.TempDir()
	outside := t.TempDir()
	roots := NewLibraryRoots(staticRootLister{paths: []string{root}})
	ctx := context.Background()

	// A real file inside the root resolves and is contained.
	inside := filepath.Join(root, "book.epub")
	if err := os.WriteFile(inside, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	expectedInside, err := filepath.EvalSymlinks(inside)
	if err != nil {
		t.Fatal(err)
	}
	if got, ok := roots.ResolveContained(ctx, inside); !ok || got != expectedInside {
		t.Errorf("ResolveContained(inside) = %q, %v; want %q, true", got, ok, expectedInside)
	}

	// A secret outside the root, and a symlink to it placed INSIDE the root.
	secret := filepath.Join(outside, "secret.epub")
	if err := os.WriteFile(secret, []byte("top secret"), 0o644); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(root, "innocent.epub")
	if err := os.Symlink(secret, link); err != nil {
		t.Fatal(err)
	}
	if got, ok := roots.ResolveContained(ctx, link); ok {
		t.Errorf("ResolveContained(symlink-escaping-root) = %q, %v; want \"\", false", got, ok)
	}

	// A nonexistent path can't be resolved, so it's rejected (fail-closed).
	if _, ok := roots.ResolveContained(ctx, filepath.Join(root, "missing.epub")); ok {
		t.Error("ResolveContained(nonexistent) must be false")
	}

	// A path outside any root is rejected.
	if _, ok := roots.ResolveContained(ctx, secret); ok {
		t.Error("ResolveContained(outside root) must be false")
	}
}

// TestLibraryRoots_ResolveContained_AllowsRootItself covers #1373: the
// import/scan path may target a configured root as a whole ("scan everything
// under /books"), unlike the delete path where a root is never a deletable
// book. Contains must keep rejecting root-equality; ResolveContained must not.
func TestLibraryRoots_ResolveContained_AllowsRootItself(t *testing.T) {
	root := t.TempDir()
	roots := NewLibraryRoots(staticRootLister{paths: []string{root}})
	ctx := context.Background()

	expected, err := filepath.EvalSymlinks(root)
	if err != nil {
		t.Fatal(err)
	}
	if got, ok := roots.ResolveContained(ctx, root); !ok || got != expected {
		t.Errorf("ResolveContained(root itself) = %q, %v; want %q, true", got, ok, expected)
	}
	// The delete-path primitive stays strict: a root is not a deletable book.
	if roots.Contains(ctx, root) {
		t.Error("Contains(root itself) must remain false for the delete path")
	}
}

// TestLibraryRoots_ResolveContained_NilReceiverAllows mirrors the Contains
// nil-receiver opt-out: a handler not wired with WithRoots keeps legacy
// behaviour (used by fixtures that don't configure roots).
func TestLibraryRoots_ResolveContained_NilReceiverAllows(t *testing.T) {
	var r *LibraryRoots
	got, ok := r.ResolveContained(context.Background(), "/some/path.epub")
	if !ok || got != "/some/path.epub" {
		t.Errorf("nil receiver ResolveContained = %q, %v; want path, true", got, ok)
	}
}

// TestLibraryRoots_ResolveContained_NoRootsFailsClosed documents that, unlike
// Contains (which falls open for the delete path), the strict import-path check
// rejects when no roots are configured.
func TestLibraryRoots_ResolveContained_NoRootsFailsClosed(t *testing.T) {
	dir := t.TempDir()
	f := filepath.Join(dir, "x.epub")
	if err := os.WriteFile(f, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	roots := NewLibraryRoots(nil) // no lister, no defaults
	if _, ok := roots.ResolveContained(context.Background(), f); ok {
		t.Error("no roots configured: ResolveContained must fail closed")
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

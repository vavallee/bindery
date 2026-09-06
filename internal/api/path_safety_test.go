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

// mustWrite creates path (and its parents) with placeholder contents.
func mustWrite(t *testing.T, path string) string {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func exists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}

// TestRemoveBookDirScoped drives the directory branch directly. Handler-level
// tests cover the reported #2052 flow; this covers the decision table without
// building a database fixture per case.
func TestRemoveBookDirScoped(t *testing.T) {
	tests := []struct {
		name    string
		files   []string // relative to the book folder
		format  string
		owned   map[string]bool // relative paths another book still tracks
		gone    []string
		kept    []string
		dirGone bool
	}{
		{
			name:   "audiobook delete keeps the ebook and its cover",
			files:  []string{"Book.m4b", "Book.epub", "cover.jpg"},
			format: "audiobook",
			gone:   []string{"Book.m4b"},
			kept:   []string{"Book.epub", "cover.jpg"},
		},
		{
			name:   "ebook delete keeps the audio files and their cover",
			files:  []string{"Book.epub", "Book.mobi", "Disc 1/01.mp3", "cover.jpg"},
			format: "ebook",
			gone:   []string{"Book.epub", "Book.mobi"},
			kept:   []string{"Disc 1/01.mp3", "cover.jpg"},
		},
		{
			name:    "audiobook delete clears sidecars and the folder when nothing else is in it",
			files:   []string{"Disc 1/01.mp3", "Disc 2/01.mp3", "cover.jpg", "Book.cue"},
			format:  "audiobook",
			gone:    []string{"Disc 1/01.mp3", "Disc 2/01.mp3", "cover.jpg", "Book.cue"},
			dirGone: true,
		},
		{
			name:    "unscoped delete takes everything",
			files:   []string{"Book.epub", "Book.m4b", "cover.jpg"},
			format:  "",
			gone:    []string{"Book.epub", "Book.m4b", "cover.jpg"},
			dirGone: true,
		},
		{
			name:   "a file another book tracks is left behind, and keeps the folder alive",
			files:  []string{"Book.m4b", "Other Book.m4b", "cover.jpg"},
			format: "audiobook",
			owned:  map[string]bool{"Other Book.m4b": true},
			gone:   []string{"Book.m4b"},
			kept:   []string{"Other Book.m4b", "cover.jpg"},
		},
		{
			// The guard must hold on the whole-book/author-delete path too,
			// where sweepMatchesFormat matches everything.
			name:   "an unscoped delete still leaves another book's file alone",
			files:  []string{"Book.epub", "Book.m4b", "Other Book.m4b", "cover.jpg"},
			format: "",
			owned:  map[string]bool{"Other Book.m4b": true},
			gone:   []string{"Book.epub", "Book.m4b"},
			kept:   []string{"Other Book.m4b", "cover.jpg"},
		},
		{
			name:   "an unknown format filter removes nothing",
			files:  []string{"Book.m4b", "Book.epub"},
			format: "graphicnovel",
			kept:   []string{"Book.m4b", "Book.epub"},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			dir := filepath.Join(t.TempDir(), "Author", "Book")
			for _, rel := range tc.files {
				mustWrite(t, filepath.Join(dir, filepath.FromSlash(rel)))
			}
			ownedByOther := func(p string) bool {
				rel, err := filepath.Rel(dir, p)
				if err != nil {
					return false
				}
				return tc.owned[filepath.ToSlash(rel)]
			}

			if err := removeBookDirScoped(dir, tc.format, ownedByOther); err != nil {
				t.Fatalf("removeBookDirScoped: %v", err)
			}

			for _, rel := range tc.gone {
				if exists(filepath.Join(dir, filepath.FromSlash(rel))) {
					t.Errorf("%s should have been removed", rel)
				}
			}
			for _, rel := range tc.kept {
				if !exists(filepath.Join(dir, filepath.FromSlash(rel))) {
					t.Errorf("%s should have survived", rel)
				}
			}
			if got := exists(dir); got == tc.dirGone {
				t.Errorf("folder exists = %v, want %v", got, !tc.dirGone)
			}
		})
	}
}

// TestRemoveBookDirScoped_NilPredicate covers the callers that have no
// book_files repo to consult: with no ownership predicate the sweep proceeds.
func TestRemoveBookDirScoped_NilPredicate(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "Book")
	mustWrite(t, filepath.Join(dir, "Book.m4b"))

	if err := removeBookDirScoped(dir, "audiobook", nil); err != nil {
		t.Fatalf("removeBookDirScoped: %v", err)
	}
	if exists(dir) {
		t.Error("folder should be gone")
	}
}

// stubOwner lets the ownership guard be driven without a database.
type stubOwner struct {
	owned map[string]bool
	err   error
}

func (s stubOwner) PathOwnedByOtherBook(_ context.Context, path string, _ int64) (bool, error) {
	if s.err != nil {
		return false, s.err
	}
	return s.owned[path], nil
}

// TestSafeRemoveBookPath_OwnershipErrorFailsSafe covers the deliberate
// fail-closed branch: if ownership can't be established, the disk delete is
// skipped. A stranded path is recoverable; a deleted file is not.
func TestSafeRemoveBookPath_OwnershipErrorFailsSafe(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "Book")
	m4b := mustWrite(t, filepath.Join(dir, "Book.m4b"))

	skipped, err := safeRemoveBookPath(context.Background(), nil,
		stubOwner{err: os.ErrPermission}, 1, dir, "audiobook")
	if err != nil {
		t.Fatalf("safeRemoveBookPath: %v", err)
	}
	if !skipped {
		t.Error("expected skipped=true when ownership can't be verified")
	}
	if !exists(m4b) {
		t.Error("file should survive an unverifiable ownership check")
	}
}

// TestSafeRemoveBookPath_SiblingOwnedByAnotherBook is the file branch of the
// same guard: the same-stem sweep must not take a sibling another book tracks.
func TestSafeRemoveBookPath_SiblingOwnedByAnotherBook(t *testing.T) {
	dir := t.TempDir()
	target := mustWrite(t, filepath.Join(dir, "Book.epub"))
	sibling := mustWrite(t, filepath.Join(dir, "Book.mobi"))

	skipped, err := safeRemoveBookPath(context.Background(), nil,
		stubOwner{owned: map[string]bool{sibling: true}}, 1, target, "ebook")
	if err != nil {
		t.Fatalf("safeRemoveBookPath: %v", err)
	}
	if skipped {
		t.Error("the targeted file was not owned by another book; expected skipped=false")
	}
	if exists(target) {
		t.Error("targeted file should be removed")
	}
	if !exists(sibling) {
		t.Error("a sibling another book tracks must survive the stem sweep")
	}
}

func TestSafeRemoveBookPathExact_DeletesOnlyTarget(t *testing.T) {
	dir := t.TempDir()
	target := mustWrite(t, filepath.Join(dir, "Book.epub"))
	sibling := mustWrite(t, filepath.Join(dir, "Book.mobi"))

	skipped, err := safeRemoveBookPathExact(context.Background(), nil, nil, 1, target, "ebook")
	if err != nil || skipped {
		t.Fatalf("safeRemoveBookPathExact = skipped %v, err %v; want false, nil", skipped, err)
	}
	if exists(target) {
		t.Error("target should be removed")
	}
	if !exists(sibling) {
		t.Error("exact deletion must leave sibling untouched")
	}
}

func TestSafeRemoveBookPathExact_SafetyBranches(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	outside := mustWrite(t, filepath.Join(t.TempDir(), "outside.epub"))
	roots := NewLibraryRoots(nil, root)

	if skipped, err := safeRemoveBookPathExact(ctx, roots, nil, 1, outside, "ebook"); err != nil || !skipped {
		t.Errorf("outside path = skipped %v, err %v; want true, nil", skipped, err)
	}

	target := mustWrite(t, filepath.Join(root, "owned.epub"))
	if skipped, err := safeRemoveBookPathExact(ctx, nil, stubOwner{err: os.ErrPermission}, 1, target, "ebook"); err != nil || !skipped {
		t.Errorf("ownership error = skipped %v, err %v; want true, nil", skipped, err)
	}
	if !exists(target) {
		t.Error("ownership failure must preserve the file")
	}

	other := mustWrite(t, filepath.Join(root, "other.epub"))
	if skipped, err := safeRemoveBookPathExact(ctx, nil, stubOwner{owned: map[string]bool{other: true}}, 1, other, "ebook"); err != nil || !skipped {
		t.Errorf("other owner = skipped %v, err %v; want true, nil", skipped, err)
	}
	if !exists(other) {
		t.Error("a file owned by another book must survive")
	}

	missing := filepath.Join(root, "missing.epub")
	if skipped, err := safeRemoveBookPathExact(ctx, nil, nil, 1, missing, "ebook"); err != nil || skipped {
		t.Errorf("missing path = skipped %v, err %v; want false, nil", skipped, err)
	}
}

func TestSafeRemoveBookPathExact_AudiobookDirectory(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "Book")
	mustWrite(t, filepath.Join(dir, "Book.m4b"))

	skipped, err := safeRemoveBookPathExact(context.Background(), nil, nil, 1, dir, "audiobook")
	if err != nil || skipped {
		t.Fatalf("safeRemoveBookPathExact = skipped %v, err %v; want false, nil", skipped, err)
	}
	if exists(dir) {
		t.Error("audiobook directory should be removed when no sibling format remains")
	}
}

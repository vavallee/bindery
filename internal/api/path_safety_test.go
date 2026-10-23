package api

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/vavallee/bindery/internal/models"
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

// TestLibraryRoots_NoConfigurationFailsClosed: with neither a DB lister nor
// static defaults there is nothing to be "inside", so a delete is refused. The
// production wiring always seeds BINDERY_LIBRARY_DIR (default /books), so this
// state is only reachable if that wiring is removed. A nil *LibraryRoots (the
// explicit opt-out used by unwired test fixtures) still allows.
func TestLibraryRoots_NoConfigurationFailsClosed(t *testing.T) {
	roots := NewLibraryRoots(nil)
	if roots.Contains(context.Background(), "/tmp/whatever.epub") {
		t.Error("no roots configured: Contains must fail closed")
	}
}

type failingRootLister struct{}

func (failingRootLister) List(context.Context) ([]models.RootFolder, error) {
	return nil, errors.New("database is locked")
}

// A lister error with no static defaults leaves zero roots; that must refuse
// rather than allow, or a transient DB hiccup becomes allow-all deletes.
func TestLibraryRoots_ListerErrorWithoutDefaultsFailsClosed(t *testing.T) {
	roots := NewLibraryRoots(failingRootLister{})
	if roots.Contains(context.Background(), "/tmp/whatever.epub") {
		t.Error("lister error and no defaults: Contains must fail closed")
	}
	// With a default the check still works off the default alone.
	dir := t.TempDir()
	roots = NewLibraryRoots(failingRootLister{}, dir)
	if !roots.Contains(context.Background(), filepath.Join(dir, "a.epub")) {
		t.Error("lister error with a default root: path under the default must be allowed")
	}
}

// A symlink that sits inside the root but points outside must not pass the
// delete gate (S2): the old lexical fallback accepted it.
func TestLibraryRoots_Contains_SymlinkOutOfRootRefused(t *testing.T) {
	root := t.TempDir()
	outside := t.TempDir()
	roots := NewLibraryRoots(staticRootLister{paths: []string{root}})
	ctx := context.Background()

	secret := mustWrite(t, filepath.Join(outside, "secret.epub"))
	link := filepath.Join(root, "innocent.epub")
	if err := os.Symlink(secret, link); err != nil {
		t.Fatal(err)
	}
	if roots.Contains(ctx, link) {
		t.Error("file symlink escaping the root must be refused")
	}
	// A symlinked directory inside the root pointing out: the file under it
	// resolves outside, whether or not the leaf exists.
	dirLink := filepath.Join(root, "author")
	if err := os.Symlink(outside, dirLink); err != nil {
		t.Fatal(err)
	}
	if roots.Contains(ctx, filepath.Join(dirLink, "secret.epub")) {
		t.Error("file under a directory symlink escaping the root must be refused")
	}
	if roots.Contains(ctx, filepath.Join(dirLink, "already-gone.epub")) {
		t.Error("missing leaf under a directory symlink escaping the root must be refused")
	}
}

// A path that runs through a symlink that cannot be resolved (dangling target
// or a loop) is not "a missing file": EvalSymlinks fails the same way for both,
// and appending the tail to the last resolvable parent would accept a path the
// filesystem may send anywhere once the target appears. It must be refused.
func TestLibraryRoots_Contains_UnresolvableSymlinkRefused(t *testing.T) {
	root := t.TempDir()
	outside := t.TempDir()
	roots := NewLibraryRoots(staticRootLister{paths: []string{root}})
	ctx := context.Background()

	dangling := filepath.Join(root, "dangling")
	if err := os.Symlink(filepath.Join(outside, "not-yet"), dangling); err != nil {
		t.Fatal(err)
	}
	if roots.Contains(ctx, dangling) {
		t.Error("dangling symlink must be refused")
	}
	if roots.Contains(ctx, filepath.Join(dangling, "sub", "book.epub")) {
		t.Error("missing leaf under a dangling directory symlink must be refused")
	}
	loop1, loop2 := filepath.Join(root, "loop1"), filepath.Join(root, "loop2")
	if err := os.Symlink(loop2, loop1); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(loop1, loop2); err != nil {
		t.Fatal(err)
	}
	if roots.Contains(ctx, filepath.Join(loop1, "book.epub")) {
		t.Error("path through a symlink loop must be refused")
	}
}

// ".." after a directory symlink: Clean folds "root/link/../x" to "root/x", but
// the kernel resolves the link first and lands beside the link's target, which
// can be outside the root. The check would be looking at a different path than
// the delete uses, so any ".." segment is refused.
func TestLibraryRoots_Contains_DotDotAfterSymlinkRefused(t *testing.T) {
	base := t.TempDir()
	root := filepath.Join(base, "books")
	outside := filepath.Join(base, "outside", "deep")
	for _, d := range []string{root, outside} {
		if err := os.MkdirAll(d, 0o750); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.Symlink(outside, filepath.Join(root, "link")); err != nil {
		t.Fatal(err)
	}
	victim := mustWrite(t, filepath.Join(base, "outside", "victim.epub"))
	roots := NewLibraryRoots(staticRootLister{paths: []string{root}})
	// root/link/../victim.epub is outside/victim.epub on disk.
	p := root + "/link/../victim.epub"
	if _, err := os.Stat(p); err != nil {
		t.Fatalf("test setup: %s should reach the victim file: %v", p, err)
	}
	if roots.Contains(context.Background(), p) {
		t.Errorf("Contains(%q) = true; it reaches %s outside the root", p, victim)
	}
}

// A root that is itself a symlink (a library on another volume) must keep
// working: both sides are resolved before comparing.
func TestLibraryRoots_Contains_SymlinkedRoot(t *testing.T) {
	real := t.TempDir()
	linkParent := t.TempDir()
	linkRoot := filepath.Join(linkParent, "library")
	if err := os.Symlink(real, linkRoot); err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	roots := NewLibraryRoots(staticRootLister{paths: []string{linkRoot}})

	viaLink := mustWrite(t, filepath.Join(linkRoot, "Author", "book.epub"))
	if !roots.Contains(ctx, viaLink) {
		t.Error("path reached through the symlinked root must be allowed")
	}
	if !roots.Contains(ctx, filepath.Join(real, "Author", "book.epub")) {
		t.Error("path given as the real location of a symlinked root must be allowed")
	}
	if !roots.Contains(ctx, filepath.Join(linkRoot, "Author", "gone.epub")) {
		t.Error("missing file under a symlinked root must still be allowed (already deleted)")
	}
	// The same, with the root configured as the real path and the file path
	// given via the link.
	roots = NewLibraryRoots(staticRootLister{paths: []string{real}})
	if !roots.Contains(ctx, viaLink) {
		t.Error("link path into a real-path root must be allowed")
	}
	if roots.Contains(ctx, filepath.Join(t.TempDir(), "x.epub")) {
		t.Error("unrelated path must be refused")
	}
}

// An already deleted file under a normal root must still pass, so a stale row
// does not stay stuck behind a refusal.
func TestLibraryRoots_Contains_MissingFileUnderRootAllowed(t *testing.T) {
	root := t.TempDir()
	roots := NewLibraryRoots(staticRootLister{paths: []string{root}})
	if !roots.Contains(context.Background(), filepath.Join(root, "no", "such", "file.epub")) {
		t.Error("missing file under the root must be allowed")
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
	owned    map[string]bool
	err      error
	underErr error // fails only PathUnderOtherBookDir
	under    map[string]bool
}

func (s stubOwner) PathOwnedByOtherBook(_ context.Context, path string, _ int64) (bool, error) {
	if s.err != nil {
		return false, s.err
	}
	return s.owned[path], nil
}

func (s stubOwner) PathUnderOtherBookDir(_ context.Context, path string, _ int64) (bool, error) {
	if s.err != nil {
		return false, s.err
	}
	if s.underErr != nil {
		return false, s.underErr
	}
	return s.under[path], nil
}

// TestPathGuardedByOtherBook_AncestorLookupErrorPropagates: when only the
// folder lookup fails, the guard must report the error, and both delete
// sweeps must then leave the file alone (#3336).
func TestPathGuardedByOtherBook_AncestorLookupErrorPropagates(t *testing.T) {
	owner := stubOwner{underErr: os.ErrPermission}
	if _, err := pathGuardedByOtherBook(context.Background(), owner, 1, "/x/y.mp3"); err == nil {
		t.Fatal("ancestor lookup error was swallowed")
	}
	dir := filepath.Join(t.TempDir(), "Book")
	m4b := mustWrite(t, filepath.Join(dir, "Book.m4b"))
	if skipped, err := safeRemoveBookPath(context.Background(), nil, owner, 1, dir, "audiobook"); err != nil || !skipped {
		t.Errorf("safeRemoveBookPath = %v, %v, want skipped", skipped, err)
	}
	if !exists(m4b) {
		t.Error("file removed despite an unverifiable folder ownership check")
	}
	if skipped, err := safeRemoveBookPathExact(context.Background(), nil, owner, 1, m4b, "audiobook"); err != nil || !skipped {
		t.Errorf("safeRemoveBookPathExact = %v, %v, want skipped", skipped, err)
	}
	if !exists(m4b) {
		t.Error("file removed by the exact delete despite an unverifiable folder ownership check")
	}
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

// TestSafeRemoveBookPath_ReclaimsFolderWithOrphanedSidecar regression-tests a
// PR review finding: import.write_opf_sidecar's metadata.opf is not a book
// file, so it survived the old empty-folder check in removeBookPathScoped
// and stranded every book's folder on delete, holding nothing but a stale
// sidecar. removeBookPathScoped now reclaims an orphaned sidecar the same
// way Reorganize already did (RemoveOrphanedSidecar), before checking empty.
func TestSafeRemoveBookPath_ReclaimsFolderWithOrphanedSidecar(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "Book")
	ebook := mustWrite(t, filepath.Join(dir, "Book.epub"))
	if err := os.WriteFile(filepath.Join(dir, "metadata.opf"), []byte("<package/>"), 0o644); err != nil {
		t.Fatal(err)
	}

	skipped, err := safeRemoveBookPath(context.Background(), nil, stubOwner{}, 1, ebook, "ebook")
	if err != nil {
		t.Fatalf("safeRemoveBookPath: %v", err)
	}
	if skipped {
		t.Fatal("expected the delete to proceed, not be skipped")
	}
	if exists(ebook) {
		t.Error("ebook file should have been removed")
	}
	if _, statErr := os.Stat(dir); !os.IsNotExist(statErr) {
		t.Errorf("book folder should be gone once only the sidecar was left behind, stat err = %v", statErr)
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

// TestSafeRemoveBookPathExact_PrunesEmptyParent pins the PR #2486 review
// follow-up: the exact delete never sweeps same-stem siblings, but it must
// still prune the parent folder once removing the target leaves it empty,
// the same cleanup removeBookPathScoped does for the format-scoped sweep.
func TestSafeRemoveBookPathExact_PrunesEmptyParent(t *testing.T) {
	dir := t.TempDir()
	target := mustWrite(t, filepath.Join(dir, "Book.epub"))

	skipped, err := safeRemoveBookPathExact(context.Background(), nil, nil, 1, target, "ebook")
	if err != nil || skipped {
		t.Fatalf("safeRemoveBookPathExact = skipped %v, err %v; want false, nil", skipped, err)
	}
	if exists(dir) {
		t.Error("parent directory left empty by the deletion should be pruned")
	}
}

// TestSafeRemoveBookPathExact_KeepsNonEmptyParent is the other half: a
// parent that still holds a sibling file (or anything else) must survive.
func TestSafeRemoveBookPathExact_KeepsNonEmptyParent(t *testing.T) {
	dir := t.TempDir()
	target := mustWrite(t, filepath.Join(dir, "Book.epub"))
	sibling := mustWrite(t, filepath.Join(dir, "Book.mobi"))

	skipped, err := safeRemoveBookPathExact(context.Background(), nil, nil, 1, target, "ebook")
	if err != nil || skipped {
		t.Fatalf("safeRemoveBookPathExact = skipped %v, err %v; want false, nil", skipped, err)
	}
	if !exists(dir) {
		t.Error("parent directory with a surviving sibling must not be pruned")
	}
	if !exists(sibling) {
		t.Error("sibling file must survive the exact delete")
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

// CheckContained must accept and refuse exactly what ResolveContained does; it
// only adds why a path was refused (#3332).
func TestLibraryRoots_CheckContained(t *testing.T) {
	root := t.TempDir()
	outside := t.TempDir()
	roots := NewLibraryRoots(staticRootLister{paths: []string{root}})
	ctx := context.Background()

	file := filepath.Join(root, "a.epub")
	if err := os.WriteFile(file, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	secret := filepath.Join(outside, "s.epub")
	if err := os.WriteFile(secret, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	escape := filepath.Join(root, "escape.epub")
	if err := os.Symlink(secret, escape); err != nil {
		t.Fatal(err)
	}
	dangling := filepath.Join(root, "dangling.epub")
	if err := os.Symlink(filepath.Join(root, "gone.epub"), dangling); err != nil {
		t.Fatal(err)
	}

	danglingOut := filepath.Join(root, "dangling-out.epub")
	if err := os.Symlink(filepath.Join(outside, "gone.epub"), danglingOut); err != nil {
		t.Fatal(err)
	}
	escapeDir := filepath.Join(root, "escapedir")
	if err := os.Symlink(outside, escapeDir); err != nil {
		t.Fatal(err)
	}

	cases := []struct {
		name string
		path string
		want ContainResult
	}{
		{"existing in root", file, ContainOK},
		{"the root itself", root, ContainOK},
		{"missing in root", filepath.Join(root, "x", "missing.epub"), ContainUnreadable},
		{"dangling link to an in-root target", dangling, ContainOutside},
		{"dangling link to an outside target", danglingOut, ContainOutside},
		{"missing leaf through an escaping link", filepath.Join(escapeDir, "nonexistent"), ContainOutside},
		{"deeper missing path through an escaping link", filepath.Join(escapeDir, "no", "x"), ContainOutside},
		{"existing file through an escaping link", filepath.Join(escapeDir, "s.epub"), ContainOutside},
		{"symlink escaping the root", escape, ContainOutside},
		{"existing outside", secret, ContainOutside},
		{"missing outside", filepath.Join(outside, "missing.epub"), ContainOutside},
		{"dot dot out through a missing name", filepath.Join(root, "nope", "..", "..", filepath.Base(outside), "s.epub"), ContainOutside},
		{"relative", "a.epub", ContainOutside},
		{"empty", "", ContainOutside},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			resolved, got := roots.CheckContained(ctx, c.path)
			if got != c.want {
				t.Errorf("CheckContained(%q) = %v, want %v", c.path, got, c.want)
			}
			rResolved, ok := roots.ResolveContained(ctx, c.path)
			if ok != (c.want == ContainOK) || rResolved != resolved {
				t.Errorf("ResolveContained(%q) = %q, %v disagrees with CheckContained %q, %v", c.path, rResolved, ok, resolved, got)
			}
		})
	}
}

// A root reached through a symlink still counts as "written inside" for the
// wording, whichever spelling the stored path uses.
func TestLibraryRoots_CheckContained_SymlinkedRoot(t *testing.T) {
	base := t.TempDir()
	real := filepath.Join(base, "real")
	if err := os.Mkdir(real, 0o755); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(base, "link")
	if err := os.Symlink(real, link); err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	for name, c := range map[string]struct{ root, path string }{
		"root is the link, path via real":     {link, filepath.Join(real, "gone.epub")},
		"root is the real dir, path via link": {real, filepath.Join(link, "gone.epub")},
	} {
		t.Run(name, func(t *testing.T) {
			roots := NewLibraryRoots(nil, c.root)
			if _, got := roots.CheckContained(ctx, c.path); got != ContainUnreadable {
				t.Errorf("CheckContained(%q) = %v, want ContainUnreadable", c.path, got)
			}
		})
	}
}

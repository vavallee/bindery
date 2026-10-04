package importer

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/vavallee/bindery/internal/models"
)

// A download is untrusted input: whoever built the release decides what is in
// the folder, symlinks included. These tests pin that no placement path lets a
// symlink from a download become an entry in the library, and that the library
// scan never tracks one.

// nonRegularUnder returns every entry under root that is neither a regular
// file nor a directory (symlinks, devices, fifos), relative to root. The walk
// never follows a link, so a link to a directory is reported, not entered.
func nonRegularUnder(t *testing.T, root string) []string {
	t.Helper()
	var out []string
	err := filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if !d.IsDir() && !d.Type().IsRegular() {
			rel, _ := filepath.Rel(root, p)
			out = append(out, rel)
		}
		return nil
	})
	if err != nil {
		t.Fatalf("walk %s: %v", root, err)
	}
	return out
}

// symlinkOrSkip creates a symlink, skipping the test where the platform does
// not allow it (Windows without developer mode).
func symlinkOrSkip(t *testing.T, target, link string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(link), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, link); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
}

// writeHostileAudiobookDownload builds a two track audiobook job folder that
// also carries links to a file and a directory outside it, the way a hostile
// release would. It returns the job folder and the secret file the links name.
func writeHostileAudiobookDownload(t *testing.T) (job, secret string) {
	t.Helper()
	outside := t.TempDir()
	secret = filepath.Join(outside, "bindery.db")
	mustWrite(t, secret, "SECRET DATABASE BYTES")
	job = filepath.Join(t.TempDir(), "Author A - Title T")
	mustWrite(t, filepath.Join(job, "part1.mp3"), "track one")
	mustWrite(t, filepath.Join(job, "part2.mp3"), "track two")
	symlinkOrSkip(t, secret, filepath.Join(job, "cover.jpg"))
	symlinkOrSkip(t, secret, filepath.Join(job, "bonus.epub"))
	symlinkOrSkip(t, outside, filepath.Join(job, "extras", "config"))
	return job, secret
}

// TestImport_MoveModeAudiobookDropsSymlinks drives a real move mode audiobook
// import (the SABnzbd/NZBGet default) of a job folder holding symlinks. The
// download and the library share a filesystem, so placement takes MoveDir's
// rename fast path, which moves the folder wholesale.
func TestImport_MoveModeAudiobookDropsSymlinks(t *testing.T) {
	f := newRoutingFixture(t, models.MediaTypeAudiobook, map[string]string{
		"import.mode": "move",
	})
	job, secret := writeHostileAudiobookDownload(t)
	dl := newPairDownload(t, f.downloads, f.book, models.MediaTypeAudiobook, f.ctx)

	f.s.tryImportInternal(f.ctx, dl, job, "", "", "", nil, nil)

	assertHasFileNamed(t, f.abDir, "part1.mp3", "audiobook library")
	assertHasFileNamed(t, f.abDir, "part2.mp3", "audiobook library")
	if got := nonRegularUnder(t, f.abDir); len(got) != 0 {
		t.Fatalf("library holds non-regular entries from the download: %v", got)
	}
	// The links are dropped, never followed: the target is untouched.
	if b, err := os.ReadFile(secret); err != nil || string(b) != "SECRET DATABASE BYTES" {
		t.Fatalf("link target disturbed: %q, %v", b, err)
	}
}

// TestMoveDownloadDir_LinksNeverLand is the same guarantee at the entry point
// the import uses. The source holds links, so the rename is skipped and only
// regular files are copied.
func TestMoveDownloadDir_LinksNeverLand(t *testing.T) {
	job, secret := writeHostileAudiobookDownload(t)
	dst := filepath.Join(t.TempDir(), "Author A", "Title T")

	if err := MoveDownloadDirCtx(t.Context(), job, dst); err != nil {
		t.Fatalf("MoveDownloadDirCtx: %v", err)
	}
	if _, err := os.Lstat(job); !os.IsNotExist(err) {
		t.Errorf("source should be gone after a successful move, err = %v", err)
	}
	if got := nonRegularUnder(t, dst); len(got) != 0 {
		t.Fatalf("destination holds non-regular entries: %v", got)
	}
	for _, name := range []string{"part1.mp3", "part2.mp3"} {
		if _, err := os.Stat(filepath.Join(dst, name)); err != nil {
			t.Errorf("missing %s: %v", name, err)
		}
	}
	if _, err := os.Stat(secret); err != nil {
		t.Fatalf("link target removed: %v", err)
	}
}

// TestCopyDirContext_SkipsSymlinks covers MoveDir's slow (cross filesystem)
// path, which copies with copyDirContext. It already skipped links; this
// keeps it that way.
func TestCopyDirContext_SkipsSymlinks(t *testing.T) {
	job, _ := writeHostileAudiobookDownload(t)
	dst := filepath.Join(t.TempDir(), "out")

	if err := copyDirContext(t.Context(), job, dst); err != nil {
		t.Fatalf("copyDirContext: %v", err)
	}
	if got := nonRegularUnder(t, dst); len(got) != 0 {
		t.Fatalf("copy holds non-regular entries: %v", got)
	}
	if _, err := os.Stat(filepath.Join(dst, "part1.mp3")); err != nil {
		t.Fatalf("regular file not copied: %v", err)
	}
}

// TestMoveDownloadDir_RefusesSymlinkedSource: a job folder that is itself a
// link is refused. Renaming it would put the link into the library, and
// copying through it would import whatever it names.
func TestMoveDownloadDir_RefusesSymlinkedSource(t *testing.T) {
	real := filepath.Join(t.TempDir(), "real")
	mustWrite(t, filepath.Join(real, "part1.mp3"), "track one")
	link := filepath.Join(t.TempDir(), "job")
	symlinkOrSkip(t, real, link)
	dst := filepath.Join(t.TempDir(), "Author A", "Title T")

	if err := MoveDownloadDirCtx(t.Context(), link, dst); !errors.Is(err, ErrDownloadDirIsSymlink) {
		t.Fatalf("err = %v, want ErrDownloadDirIsSymlink", err)
	}
	if _, err := os.Lstat(dst); !os.IsNotExist(err) {
		t.Fatalf("destination created for a refused source: %v", err)
	}
	if _, err := os.Stat(filepath.Join(real, "part1.mp3")); err != nil {
		t.Fatalf("link target disturbed: %v", err)
	}
}

// TestMoveDownloadDir_LinkInReadOnlySubdirNeverLands: the download client's
// UID often owns the folders, so Bindery cannot delete inside a read only
// subfolder. A sweep after the rename would fail there and leave the link in
// the library; checking before the rename means it never gets there.
func TestMoveDownloadDir_LinkInReadOnlySubdirNeverLands(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root ignores directory permissions")
	}
	outside := t.TempDir()
	secret := filepath.Join(outside, "passwd")
	mustWrite(t, secret, "SECRET")
	job := filepath.Join(t.TempDir(), "job")
	mustWrite(t, filepath.Join(job, "part1.mp3"), "track one")
	sub := filepath.Join(job, "Disc 1")
	mustWrite(t, filepath.Join(sub, "part2.mp3"), "track two")
	symlinkOrSkip(t, secret, filepath.Join(sub, "cover.jpg"))
	if err := os.Chmod(sub, 0o555); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(sub, 0o755) })
	dst := filepath.Join(t.TempDir(), "Author A", "Title T")

	// The source cleanup after the copy fails on the read only folder, as it
	// always has for the copy path; what matters is what reached the library.
	_ = MoveDownloadDirCtx(t.Context(), job, dst)

	if got := nonRegularUnder(t, dst); len(got) != 0 {
		t.Fatalf("library holds links from the download: %v", got)
	}
	if b, err := os.ReadFile(filepath.Join(dst, "Disc 1", "part2.mp3")); err != nil || string(b) != "track two" {
		t.Fatalf("regular file in the read only folder not placed: %q, %v", b, err)
	}
}

// TestMoveDownloadDir_CleanTreeStillRenames: a download with no links keeps
// the cheap same filesystem rename (the inode is the same one).
func TestMoveDownloadDir_CleanTreeStillRenames(t *testing.T) {
	job := filepath.Join(t.TempDir(), "job")
	mustWrite(t, filepath.Join(job, "part1.mp3"), "track one")
	before, err := os.Stat(filepath.Join(job, "part1.mp3"))
	if err != nil {
		t.Fatal(err)
	}
	dst := filepath.Join(t.TempDir(), "Author A", "Title T")
	if err := MoveDownloadDirCtx(t.Context(), job, dst); err != nil {
		t.Fatalf("MoveDownloadDirCtx: %v", err)
	}
	after, err := os.Stat(filepath.Join(dst, "part1.mp3"))
	if err != nil {
		t.Fatal(err)
	}
	if !os.SameFile(before, after) {
		t.Fatal("clean tree was copied, want the rename fast path")
	}
}

// TestMoveDir_LibraryMoveKeepsUserLinks: MoveDir is also how reorganize moves
// a folder already in the library. Links there are the operator's own and
// must move with the folder unchanged.
func TestMoveDir_LibraryMoveKeepsUserLinks(t *testing.T) {
	lib := t.TempDir()
	src := filepath.Join(lib, "unsorted", "My Book audio")
	mustWrite(t, filepath.Join(src, "part1.mp3"), "track one")
	art := filepath.Join(lib, "art", "cover.jpg")
	mustWrite(t, art, "cover")
	symlinkOrSkip(t, art, filepath.Join(src, "cover.jpg"))
	dst := filepath.Join(lib, "Jane Doe", "My Book (2020)")

	if err := MoveDirCtx(t.Context(), src, dst); err != nil {
		t.Fatalf("MoveDirCtx: %v", err)
	}
	li, err := os.Lstat(filepath.Join(dst, "cover.jpg"))
	if err != nil || li.Mode()&os.ModeSymlink == 0 {
		t.Fatalf("user link not kept by a library move: %v, %v", li, err)
	}
}

// TestReorganize_KeepsUserLink drives the same through ApplyReorganize.
func TestReorganize_KeepsUserLink(t *testing.T) {
	env, _, audiobookDir, ctx := reorgFixture(t)
	book := env.seed(t, ctx, "Jane Doe", "My Book")
	oldDir := filepath.Join(audiobookDir, "unsorted", "My Book audio")
	writeFileAt(t, filepath.Join(oldDir, "part1.mp3"))
	art := filepath.Join(audiobookDir, "art", "cover.jpg")
	writeFileAt(t, art)
	symlinkOrSkip(t, art, filepath.Join(oldDir, "cover.jpg"))
	if err := env.books.AddBookFile(ctx, book.ID, models.MediaTypeAudiobook, oldDir); err != nil {
		t.Fatal(err)
	}

	moves, err := env.s.PreviewReorganizeBook(ctx, book.ID)
	if err != nil {
		t.Fatal(err)
	}
	results := env.s.ApplyReorganize(ctx, []int64{moves[0].FileID})
	if results[0].Status != ReorgStatusMoved {
		t.Fatalf("apply = %+v, want moved", results[0])
	}
	want := filepath.Join(audiobookDir, "Jane Doe", "My Book (2020)", "cover.jpg")
	li, err := os.Lstat(want)
	if err != nil || li.Mode()&os.ModeSymlink == 0 {
		t.Fatalf("reorganize dropped the user link: %v, %v", li, err)
	}
	if target, _ := os.Readlink(want); target != art {
		t.Fatalf("link target = %q, want %q", target, art)
	}
}

// TestScanLibrary_SymlinkedFileIsNotReconciled extends
// TestScanLibrary_SymlinkedBookIsNotListed from the Unmatched list to the
// reconcile pass: a link in the library whose name and embedded metadata
// match a wanted book must not become that book's tracked file.
func TestScanLibrary_SymlinkedFileIsNotReconciled(t *testing.T) {
	s, _, books, authors, _, libraryDir, ctx := unmatchedFixture(t)
	author := &models.Author{ForeignID: "ol:weir", Name: "Andy Weir", SortName: "Weir, Andy", MetadataProvider: "openlibrary"}
	if err := authors.Create(ctx, author); err != nil {
		t.Fatal(err)
	}
	book := &models.Book{ForeignID: "ol:phm", AuthorID: author.ID, Title: "Project Hail Mary",
		Status: models.BookStatusWanted, MediaType: models.MediaTypeEbook, MetadataProvider: "openlibrary"}
	if err := books.Create(ctx, book); err != nil {
		t.Fatal(err)
	}
	outside := filepath.Join(t.TempDir(), "Project Hail Mary.epub")
	writeEpubAt(t, outside, "Project Hail Mary", "Andy Weir", "")
	link := filepath.Join(libraryDir, "Andy Weir", "Project Hail Mary.epub")
	symlinkOrSkip(t, outside, link)

	s.ScanLibrary(ctx)

	files, err := books.ListFiles(ctx, book.ID)
	if err != nil {
		t.Fatal(err)
	}
	for _, f := range files {
		if strings.EqualFold(filepath.Clean(f.Path), filepath.Clean(link)) {
			t.Fatalf("scan tracked the symlink %s as a book file", f.Path)
		}
	}
	got, err := books.GetByID(ctx, book.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.FilePath != "" || got.EbookFilePath != "" {
		t.Fatalf("book paths = %q / %q, want none", got.FilePath, got.EbookFilePath)
	}
}

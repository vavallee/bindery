package abs

import (
	"context"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/vavallee/bindery/internal/db"
	"github.com/vavallee/bindery/internal/models"
)

func covImpBook(t *testing.T, env *covImpEnv, authorID int64, foreignID string) *models.Book {
	t.Helper()
	b := &models.Book{ForeignID: foreignID, AuthorID: authorID, Title: foreignID, SortTitle: foreignID,
		Status: models.BookStatusWanted, Genres: []string{}, Monitored: true}
	if err := env.books.Create(context.Background(), b); err != nil {
		t.Fatalf("Create book: %v", err)
	}
	return b
}

func covImpWriteFile(t *testing.T, path string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("data"), 0o600); err != nil {
		t.Fatal(err)
	}
}

func TestCovImpInspectAndReconcileFormatPathRefusals(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	env := covImpNewEnv(t)
	lib := t.TempDir()
	env.importer.WithStoragePaths(lib, "", nil)
	author := covImpCreateAuthor(t, env, "OL-FILES", "Files Author")
	book := covImpBook(t, env, author.ID, "covimp-files-book")

	ebook := filepath.Join(lib, "Files Author", "book.epub")
	covImpWriteFile(t, ebook)
	dir := filepath.Join(lib, "Files Author", "folder")
	if err := os.MkdirAll(dir, 0o750); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(lib, "Files Author", "link.epub")
	if err := os.Symlink(ebook, link); err != nil {
		t.Fatal(err)
	}
	remap := ImportConfig{PathRemap: "/abs:" + lib}
	plain := ImportConfig{}

	cases := []struct {
		name       string
		cfg        ImportConfig
		format     string
		path       string
		want       string
		unreadable bool
	}{
		{name: "missing", cfg: plain, format: models.MediaTypeEbook, path: "", want: "path missing from ABS metadata"},
		{name: "outside", cfg: plain, format: models.MediaTypeEbook, path: "/elsewhere/book.epub", want: "is outside Bindery storage"},
		{name: "outside with unmatched remap", cfg: remap, format: models.MediaTypeEbook, path: "/elsewhere/book.epub", want: "no abs.path_remap rule matched it"},
		{name: "remapped outside", cfg: ImportConfig{PathRemap: "/abs:/not-bindery"}, format: models.MediaTypeEbook, path: "/abs/book.epub", want: "remapped to"},
		{name: "symlink", cfg: plain, format: models.MediaTypeEbook, path: link, want: "symlink or special file"},
		{name: "not visible", cfg: plain, format: models.MediaTypeEbook, path: filepath.Join(lib, "nope.epub"), want: "is not visible to Bindery", unreadable: true},
		{name: "remapped not visible", cfg: remap, format: models.MediaTypeEbook, path: "/abs/nope.epub", want: "remapped to", unreadable: true},
		{name: "ebook directory", cfg: plain, format: models.MediaTypeEbook, path: dir, want: "is a directory"},
	}
	for _, tc := range cases {
		ok, msg := env.importer.inspectFormatPath(ctx, tc.cfg, tc.format, tc.path)
		if ok || !strings.Contains(msg, tc.want) {
			t.Fatalf("%s: inspectFormatPath = %v %q, want refusal containing %q", tc.name, ok, msg, tc.want)
		}
		ok, changed, msg, unreadable := env.importer.reconcileFormatPath(ctx, tc.cfg, author, book, tc.format, tc.path)
		if ok || changed || !strings.Contains(msg, tc.want) || unreadable != tc.unreadable {
			t.Fatalf("%s: reconcileFormatPath = %v %v %q %v, want refusal containing %q unreadable=%v", tc.name, ok, changed, msg, unreadable, tc.want, tc.unreadable)
		}
	}
	if files, _ := env.books.ListFiles(ctx, book.ID); len(files) != 0 {
		t.Fatalf("files = %+v, refusals must not register anything", files)
	}

	// The remapped visible file is accepted by both.
	if ok, msg := env.importer.inspectFormatPath(ctx, remap, models.MediaTypeEbook, "/abs/Files Author/book.epub"); !ok {
		t.Fatalf("inspectFormatPath remapped = %v %q, want ok", ok, msg)
	}
	dryRun := remap
	dryRun.DryRun = true
	ok, changed, msg, _ := env.importer.reconcileFormatPath(ctx, dryRun, author, book, models.MediaTypeEbook, "/abs/Files Author/book.epub")
	if !ok || !changed || msg != "" {
		t.Fatalf("dry-run reconcile = %v %v %q, want planned change", ok, changed, msg)
	}
	if files, _ := env.books.ListFiles(ctx, book.ID); len(files) != 0 {
		t.Fatalf("files = %+v, dry run must not register", files)
	}
}

func TestCovImpReconcileFormatPathStorageFailures(t *testing.T) {
	t.Parallel()
	ctx := context.Background()

	t.Run("register fails", func(t *testing.T) {
		t.Parallel()
		env := covImpNewEnv(t)
		lib := t.TempDir()
		env.importer.WithStoragePaths(lib, "", nil)
		author := covImpCreateAuthor(t, env, "OL-F1", "F One")
		book := covImpBook(t, env, author.ID, "covimp-f1")
		path := filepath.Join(lib, "a.epub")
		covImpWriteFile(t, path)
		env.failOn(t, "covimp_files_insert", "INSERT", "book_files", "")
		ok, changed, msg, _ := env.importer.reconcileFormatPath(ctx, ImportConfig{}, author, book, models.MediaTypeEbook, path)
		if ok || changed || !strings.Contains(msg, "could not be registered") {
			t.Fatalf("reconcile = %v %v %q, want registration failure", ok, changed, msg)
		}
	})

	t.Run("lookup fails", func(t *testing.T) {
		t.Parallel()
		env := covImpNewEnv(t)
		lib := t.TempDir()
		env.importer.WithStoragePaths(lib, "", nil)
		author := covImpCreateAuthor(t, env, "OL-F2", "F Two")
		book := covImpBook(t, env, author.ID, "covimp-f2")
		path := filepath.Join(lib, "a.epub")
		covImpWriteFile(t, path)
		env.exec(t, "ALTER TABLE book_files RENAME TO covimp_book_files_gone")
		ok, _, msg, _ := env.importer.reconcileFormatPath(ctx, ImportConfig{}, author, book, models.MediaTypeEbook, path)
		if ok || !strings.Contains(msg, "could not inspect existing Bindery files") {
			t.Fatalf("reconcile = %v %q, want lookup failure", ok, msg)
		}
		// Pruning is best effort and must swallow the same failure.
		env.importer.pruneVanishedFormatPaths(ctx, book.ID, models.MediaTypeEbook, path)
	})

	t.Run("prune keeps rows it cannot remove", func(t *testing.T) {
		t.Parallel()
		env := covImpNewEnv(t)
		lib := t.TempDir()
		env.importer.WithStoragePaths(lib, "", nil)
		author := covImpCreateAuthor(t, env, "OL-F3", "F Three")
		book := covImpBook(t, env, author.ID, "covimp-f3")
		gone := filepath.Join(lib, "gone.epub")
		if err := env.books.AddBookFile(ctx, book.ID, models.MediaTypeEbook, gone); err != nil {
			t.Fatalf("AddBookFile: %v", err)
		}
		current := filepath.Join(lib, "current.epub")
		covImpWriteFile(t, current)
		env.failOn(t, "covimp_files_delete", "DELETE", "book_files", "")
		ok, changed, msg, _ := env.importer.reconcileFormatPath(ctx, ImportConfig{}, author, book, models.MediaTypeEbook, current)
		if !ok || !changed || msg != "" {
			t.Fatalf("reconcile = %v %v %q, want success despite prune failure", ok, changed, msg)
		}
		files, _ := env.books.ListFiles(ctx, book.ID)
		if len(files) != 2 {
			t.Fatalf("files = %+v, want the stale row kept when its delete fails", files)
		}
	})
}

func TestCovImpRootFolderResolution(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	env := covImpNewEnv(t)
	roots := db.NewRootFolderRepo(env.db)
	libDir, audioDir := t.TempDir(), t.TempDir()
	ebookRoot, err := roots.Create(ctx, t.TempDir())
	if err != nil {
		t.Fatalf("Create root: %v", err)
	}
	audioRoot, err := roots.Create(ctx, t.TempDir())
	if err != nil {
		t.Fatalf("Create root: %v", err)
	}
	defaultAudio, err := roots.Create(ctx, t.TempDir())
	if err != nil {
		t.Fatalf("Create root: %v", err)
	}
	imp := env.importer.WithStoragePaths(libDir, audioDir, roots)

	author := &models.Author{RootFolderID: &ebookRoot.ID, AudiobookRootFolderID: &audioRoot.ID}
	if got := imp.effectiveLibraryDir(ctx, author); got != ebookRoot.Path {
		t.Fatalf("effectiveLibraryDir = %q, want author root %q", got, ebookRoot.Path)
	}
	if got := imp.effectiveAudiobookDir(ctx, author); got != audioRoot.Path {
		t.Fatalf("effectiveAudiobookDir = %q, want author root %q", got, audioRoot.Path)
	}
	if got := imp.effectiveLibraryDir(ctx, nil); got != libDir {
		t.Fatalf("effectiveLibraryDir(nil) = %q, want global %q", got, libDir)
	}

	// A malformed or dangling default falls through to the global dir; a
	// valid one wins.
	for _, value := range []string{"abc", "-3", "999999"} {
		if err := env.settings.Set(ctx, settingDefaultAudiobookRootID, value); err != nil {
			t.Fatal(err)
		}
		if got := imp.effectiveAudiobookDir(ctx, nil); got != audioDir {
			t.Fatalf("default %q: effectiveAudiobookDir = %q, want global %q", value, got, audioDir)
		}
	}
	if err := env.settings.Set(ctx, settingDefaultAudiobookRootID, strconv.FormatInt(defaultAudio.ID, 10)); err != nil {
		t.Fatal(err)
	}
	if got := imp.effectiveAudiobookDir(ctx, nil); got != defaultAudio.Path {
		t.Fatalf("effectiveAudiobookDir = %q, want default root %q", got, defaultAudio.Path)
	}

	// Audiobooks accept the author's audiobook root, the global audiobook dir
	// and the ebook roots; ebooks never accept the audiobook roots.
	audioRoots := imp.allowedRootsForBook(ctx, author, models.MediaTypeAudiobook)
	for _, want := range []string{audioRoot.Path, audioDir, ebookRoot.Path, libDir} {
		found := false
		for _, r := range audioRoots {
			if r == filepath.Clean(want) {
				found = true
			}
		}
		if !found {
			t.Fatalf("audiobook roots = %v, missing %q", audioRoots, want)
		}
	}
	for _, r := range imp.allowedRootsForBook(ctx, author, models.MediaTypeEbook) {
		if r == filepath.Clean(audioRoot.Path) || r == filepath.Clean(audioDir) {
			t.Fatalf("ebook roots include audiobook root %q", r)
		}
	}
}

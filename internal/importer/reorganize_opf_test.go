package importer

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/vavallee/bindery/internal/models"
)

// placedBook seeds a book whose ebook already sits at the templated path.
func placedBook(t *testing.T, env reorgEnv, libraryDir string) (*models.Book, string, int64) {
	t.Helper()
	ctx := t.Context()
	book := env.seed(t, ctx, "Jane Doe", "My Book")
	p := filepath.Join(libraryDir, "Jane Doe", "My Book (2020)", "My Book - Jane Doe.epub")
	writeFileAt(t, p)
	if err := env.books.AddBookFile(ctx, book.ID, models.MediaTypeEbook, p); err != nil {
		t.Fatal(err)
	}
	files, err := env.books.ListBookFiles(ctx, book.ID)
	if err != nil || len(files) != 1 {
		t.Fatalf("files = %v, err %v", files, err)
	}
	return book, p, files[0].ID
}

func setOPF(t *testing.T, env reorgEnv, on string) {
	t.Helper()
	if err := env.s.settings.Set(t.Context(), "import.write_opf_sidecar", on); err != nil {
		t.Fatal(err)
	}
}

func TestReorganizeOPF_NoopBookGetsSidecar(t *testing.T) {
	env, libraryDir, _, ctx := reorgFixture(t)
	setOPF(t, env, "true")
	_, p, fileID := placedBook(t, env, libraryDir)
	opf := filepath.Join(filepath.Dir(p), "metadata.opf")

	moves, err := env.s.PreviewReorganizeBook(ctx, 1)
	if err != nil {
		t.Fatal(err)
	}
	if moves[0].Status != ReorgStatusNoop || moves[0].Sidecar != ReorgSidecarWrite {
		t.Fatalf("preview = %+v, want noop with sidecar write", moves[0])
	}
	if _, err := os.Stat(opf); err == nil {
		t.Fatal("preview must not write the sidecar")
	}

	res := env.s.ApplyReorganize(ctx, []int64{fileID})
	if res[0].Status != ReorgStatusNoop || res[0].Sidecar != ReorgSidecarWritten {
		t.Fatalf("apply = %+v, want noop with sidecar written", res[0])
	}
	if _, err := os.Stat(opf); err != nil {
		t.Fatalf("metadata.opf not written: %v", err)
	}
}

func TestReorganizeOPF_SettingOffWritesNothing(t *testing.T) {
	env, libraryDir, _, ctx := reorgFixture(t)
	// Rename branch.
	book := env.seed(t, ctx, "Jane Doe", "Moved Book")
	old := filepath.Join(libraryDir, "misc", "x.epub")
	writeFileAt(t, old)
	if err := env.books.AddBookFile(ctx, book.ID, models.MediaTypeEbook, old); err != nil {
		t.Fatal(err)
	}
	// No rename branch.
	placed := env.seed(t, ctx, "John Roe", "Placed")
	p := filepath.Join(libraryDir, "John Roe", "Placed (2020)", "Placed - John Roe.epub")
	writeFileAt(t, p)
	if err := env.books.AddBookFile(ctx, placed.ID, models.MediaTypeEbook, p); err != nil {
		t.Fatal(err)
	}
	moves, _ := env.s.PreviewReorganizeLibrary(ctx)
	var ids []int64
	for _, m := range moves {
		if m.Sidecar != "" {
			t.Fatalf("preview offered a sidecar with the setting off: %+v", m)
		}
		ids = append(ids, m.FileID)
	}
	for _, r := range env.s.ApplyReorganize(ctx, ids) {
		if r.Sidecar != "" {
			t.Fatalf("apply reported a sidecar with the setting off: %+v", r)
		}
	}
	_ = filepath.WalkDir(libraryDir, func(path string, d os.DirEntry, err error) error {
		if err == nil && d.Name() == "metadata.opf" {
			t.Errorf("unexpected sidecar %s", path)
		}
		return nil
	})
	_ = p
}

func TestReorganizeOPF_RenameBranchStillWrites(t *testing.T) {
	env, libraryDir, _, ctx := reorgFixture(t)
	setOPF(t, env, "true")
	book := env.seed(t, ctx, "Jane Doe", "My Book")
	old := filepath.Join(libraryDir, "misc", "x.epub")
	writeFileAt(t, old)
	if err := env.books.AddBookFile(ctx, book.ID, models.MediaTypeEbook, old); err != nil {
		t.Fatal(err)
	}
	files, _ := env.books.ListBookFiles(ctx, book.ID)
	res := env.s.ApplyReorganize(ctx, []int64{files[0].ID})
	if res[0].Status != ReorgStatusMoved || res[0].Sidecar != ReorgSidecarWritten {
		t.Fatalf("apply = %+v", res[0])
	}
	if _, err := os.Stat(filepath.Join(libraryDir, "Jane Doe", "My Book (2020)", "metadata.opf")); err != nil {
		t.Fatalf("sidecar missing after rename: %v", err)
	}
}

func TestReorganizeOPF_ForeignFileLeftAlone(t *testing.T) {
	env, libraryDir, _, ctx := reorgFixture(t)
	setOPF(t, env, "true")
	_, p, fileID := placedBook(t, env, libraryDir)
	opf := filepath.Join(filepath.Dir(p), "metadata.opf")
	calibre := `<?xml version='1.0' encoding='utf-8'?>
<package xmlns="http://www.idpf.org/2007/opf" unique-identifier="uuid_id" version="2.0"><metadata><dc:title xmlns:dc="http://purl.org/dc/elements/1.1/">Hand edited</dc:title><meta name="calibre:timestamp" content="2020"/></metadata></package>`
	if err := os.WriteFile(opf, []byte(calibre), 0o644); err != nil {
		t.Fatal(err)
	}

	res := env.s.ApplyReorganize(ctx, []int64{fileID})
	if res[0].Sidecar != ReorgSidecarForeign {
		t.Fatalf("sidecar = %q, want foreign", res[0].Sidecar)
	}
	got, _ := os.ReadFile(opf)
	if string(got) != calibre {
		t.Fatalf("foreign metadata.opf was modified:\n%s", got)
	}
}

func TestReorganizeOPF_RegeneratesOwnOnlyWhenChanged(t *testing.T) {
	env, libraryDir, _, ctx := reorgFixture(t)
	setOPF(t, env, "true")
	book, p, fileID := placedBook(t, env, libraryDir)
	opf := filepath.Join(filepath.Dir(p), "metadata.opf")

	if r := env.s.ApplyReorganize(ctx, []int64{fileID}); r[0].Sidecar != ReorgSidecarWritten {
		t.Fatalf("first = %+v", r[0])
	}
	old := time.Date(2001, 1, 1, 0, 0, 0, 0, time.UTC)
	if err := os.Chtimes(opf, old, old); err != nil {
		t.Fatal(err)
	}

	// Nothing changed: not rewritten, mtime stays.
	if r := env.s.ApplyReorganize(ctx, []int64{fileID}); r[0].Sidecar != ReorgSidecarUnchanged {
		t.Fatalf("second = %+v, want unchanged", r[0])
	}
	if fi, _ := os.Stat(opf); !fi.ModTime().Equal(old) {
		t.Fatalf("identical sidecar was rewritten, mtime %v", fi.ModTime())
	}

	// Metadata changed: regenerated.
	book.Description = "A new blurb"
	if err := env.books.Update(ctx, book); err != nil {
		t.Fatal(err)
	}
	if r := env.s.ApplyReorganize(ctx, []int64{fileID}); r[0].Sidecar != ReorgSidecarWritten {
		t.Fatalf("third = %+v, want written", r[0])
	}
	got, _ := os.ReadFile(opf)
	if !strings.Contains(string(got), "A new blurb") {
		t.Fatalf("sidecar not regenerated:\n%s", got)
	}
}

func TestReorganizeOPF_LegacyBinderySidecarIsRefreshed(t *testing.T) {
	// A sidecar written before the generator marker existed is still Bindery's.
	env, libraryDir, _, ctx := reorgFixture(t)
	setOPF(t, env, "true")
	_, p, fileID := placedBook(t, env, libraryDir)
	opf := filepath.Join(filepath.Dir(p), "metadata.opf")
	legacy := `<?xml version="1.0" encoding="utf-8"?>
<package xmlns="http://www.idpf.org/2007/opf" unique-identifier="bindery-id" version="2.0">
  <metadata><dc:title>Old</dc:title></metadata>
</package>
`
	if err := os.WriteFile(opf, []byte(legacy), 0o644); err != nil {
		t.Fatal(err)
	}
	if r := env.s.ApplyReorganize(ctx, []int64{fileID}); r[0].Sidecar != ReorgSidecarWritten {
		t.Fatalf("legacy = %+v, want written", r[0])
	}
}

func TestReorganizeOPF_ExternalModeWritesNothing(t *testing.T) {
	env, libraryDir, _, ctx := reorgFixture(t)
	setOPF(t, env, "true")
	if err := env.s.settings.Set(ctx, "import.mode", "external"); err != nil {
		t.Fatal(err)
	}
	_, p, fileID := placedBook(t, env, libraryDir)
	moves, _ := env.s.PreviewReorganizeBook(ctx, 1)
	if moves[0].Sidecar != "" {
		t.Fatalf("preview offered a sidecar in External mode: %+v", moves[0])
	}
	if r := env.s.ApplyReorganize(ctx, []int64{fileID}); r[0].Sidecar != "" {
		t.Fatalf("apply = %+v", r[0])
	}
	if _, err := os.Stat(filepath.Join(filepath.Dir(p), "metadata.opf")); err == nil {
		t.Fatal("sidecar written in External mode")
	}
}

func TestReorganizeOPF_WriteFailureIsPerBook(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root ignores directory permissions")
	}
	env, libraryDir, _, ctx := reorgFixture(t)
	setOPF(t, env, "true")
	a := env.seedAuthor(t, ctx, "Jane Doe")
	var ids []int64
	var dirs []string
	for _, title := range []string{"Alpha", "Beta"} {
		b := env.seedBook(t, ctx, a, title)
		p := filepath.Join(libraryDir, "Jane Doe", title+" (2020)", title+" - Jane Doe.epub")
		writeFileAt(t, p)
		if err := env.books.AddBookFile(ctx, b.ID, models.MediaTypeEbook, p); err != nil {
			t.Fatal(err)
		}
		fs, _ := env.books.ListBookFiles(ctx, b.ID)
		ids = append(ids, fs[0].ID)
		dirs = append(dirs, filepath.Dir(p))
	}
	if err := os.Chmod(dirs[0], 0o500); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(dirs[0], 0o750) })

	res := env.s.ApplyReorganize(ctx, ids)
	if len(res) != 2 {
		t.Fatalf("run stopped early: %+v", res)
	}
	if res[0].Sidecar != ReorgSidecarFailed || res[0].SidecarMessage == "" {
		t.Fatalf("first = %+v, want failed with a message", res[0])
	}
	if res[1].Sidecar != ReorgSidecarWritten {
		t.Fatalf("second = %+v, want written", res[1])
	}
}

func TestSyncOPFSidecarFile_DryRunAndRoots(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, "A", "B")
	if err := os.MkdirAll(dir, 0o750); err != nil {
		t.Fatal(err)
	}
	book := &models.Book{ID: 1, Title: "T"}
	res, err := SyncOPFSidecarFile(dir, []string{root}, true, book, nil, nil, "", "")
	if err != nil || res != SidecarWritten {
		t.Fatalf("dry run = %v, %v", res, err)
	}
	if _, err := os.Stat(filepath.Join(dir, "metadata.opf")); err == nil {
		t.Fatal("dry run wrote the file")
	}
	if _, err := SyncOPFSidecarFile(dir, []string{t.TempDir()}, false, book, nil, nil, "", ""); err == nil {
		t.Fatal("expected a refusal outside the roots")
	}
	if _, err := SyncOPFSidecarFile(dir, []string{root}, false, nil, nil, nil, "", ""); err == nil {
		t.Fatal("expected an error for a nil book")
	}
}

// flatBooks seeds two books of one author whose files share one folder, the
// shape a flat {Author}/{Title}.{ext} template produces.
func flatBooks(t *testing.T, env reorgEnv, libraryDir string) (dir string, ids []int64) {
	t.Helper()
	ctx := t.Context()
	a := env.seedAuthor(t, ctx, "Jane Doe")
	dir = filepath.Join(libraryDir, "Jane Doe")
	for _, title := range []string{"Alpha", "Beta"} {
		b := env.seedBook(t, ctx, a, title)
		p := filepath.Join(dir, title+".epub")
		writeFileAt(t, p)
		if err := env.books.AddBookFile(ctx, b.ID, models.MediaTypeEbook, p); err != nil {
			t.Fatal(err)
		}
		fs, _ := env.books.ListBookFiles(ctx, b.ID)
		ids = append(ids, fs[0].ID)
	}
	if err := env.s.settings.Set(ctx, "naming.bookTemplate", "{Author}/{Title}.{ext}"); err != nil {
		t.Fatal(err)
	}
	return dir, ids
}

func TestReorganizeOPF_SharedFolderWritesNothing(t *testing.T) {
	env, libraryDir, _, ctx := reorgFixture(t)
	setOPF(t, env, "true")
	dir, ids := flatBooks(t, env, libraryDir)
	opf := filepath.Join(dir, "metadata.opf")

	moves, err := env.s.PreviewReorganizeLibrary(ctx)
	if err != nil {
		t.Fatal(err)
	}
	for _, m := range moves {
		if m.Status != ReorgStatusNoop || m.Sidecar != ReorgSidecarShared {
			t.Fatalf("preview row = %+v, want noop and shared", m)
		}
	}
	for i := 0; i < 2; i++ {
		for _, r := range env.s.ApplyReorganize(ctx, ids) {
			if r.Sidecar != ReorgSidecarShared {
				t.Fatalf("apply %d = %+v, want shared", i, r)
			}
		}
		if _, err := os.Stat(opf); err == nil {
			t.Fatal("a sidecar was written into a folder shared by two books")
		}
	}
}

func TestReorganizeOPF_TwoFormatsOfOneBookWriteOnce(t *testing.T) {
	env, libraryDir, _, ctx := reorgFixture(t)
	setOPF(t, env, "true")
	book := env.seed(t, ctx, "Jane Doe", "My Book")
	dir := filepath.Join(libraryDir, "Jane Doe", "My Book (2020)")
	var ids []int64
	for _, name := range []string{"My Book - Jane Doe.epub", "My Book - Jane Doe.mobi"} {
		writeFileAt(t, filepath.Join(dir, name))
		if err := env.books.AddBookFile(ctx, book.ID, models.MediaTypeEbook, filepath.Join(dir, name)); err != nil {
			t.Fatal(err)
		}
	}
	fs, _ := env.books.ListBookFiles(ctx, book.ID)
	for _, f := range fs {
		ids = append(ids, f.ID)
	}
	moves, _ := env.s.PreviewReorganizeBook(ctx, book.ID)
	writes := 0
	for _, m := range moves {
		if m.Sidecar == ReorgSidecarWrite {
			writes++
		}
	}
	if writes != 1 {
		t.Fatalf("preview offers %d writes for one folder, want 1: %+v", writes, moves)
	}
	written := 0
	for _, r := range env.s.ApplyReorganize(ctx, ids) {
		if r.Sidecar == ReorgSidecarWritten {
			written++
		}
	}
	if written != 1 {
		t.Fatalf("apply wrote %d times, want 1", written)
	}
	if _, err := os.Stat(filepath.Join(dir, "metadata.opf")); err != nil {
		t.Fatal(err)
	}
}

func TestReorganizeOPF_SymlinkIsNeverReplaced(t *testing.T) {
	env, libraryDir, _, ctx := reorgFixture(t)
	setOPF(t, env, "true")
	_, p, fileID := placedBook(t, env, libraryDir)
	elsewhere := filepath.Join(t.TempDir(), "mine.opf")
	if err := os.WriteFile(elsewhere, []byte("mine"), 0o644); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(filepath.Dir(p), "metadata.opf")
	if err := os.Symlink(elsewhere, link); err != nil {
		t.Skip("symlinks unavailable")
	}
	r := env.s.ApplyReorganize(ctx, []int64{fileID})
	if r[0].Sidecar != ReorgSidecarForeign {
		t.Fatalf("sidecar = %q, want foreign", r[0].Sidecar)
	}
	if fi, _ := os.Lstat(link); fi.Mode()&os.ModeSymlink == 0 {
		t.Fatal("the symlink was replaced")
	}
	if b, _ := os.ReadFile(elsewhere); string(b) != "mine" {
		t.Fatal("the link target was written through")
	}
}

func TestReorganizeOPF_SymlinkVariantsAreNeverReplaced(t *testing.T) {
	for _, tc := range []struct {
		name   string
		target func(t *testing.T, env reorgEnv, book *models.Book) string
	}{
		{"dangling", func(t *testing.T, _ reorgEnv, _ *models.Book) string {
			return filepath.Join(t.TempDir(), "gone.opf")
		}},
		{"to a Bindery file", func(t *testing.T, _ reorgEnv, book *models.Book) string {
			b, err := BuildOPFXML(book, nil, nil, "", "")
			if err != nil {
				t.Fatal(err)
			}
			p := filepath.Join(t.TempDir(), "theirs.opf")
			if err := os.WriteFile(p, append(b, "<!-- stale -->\n"...), 0o644); err != nil {
				t.Fatal(err)
			}
			return p
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			env, libraryDir, _, ctx := reorgFixture(t)
			setOPF(t, env, "true")
			book, p, fileID := placedBook(t, env, libraryDir)
			target := tc.target(t, env, book)
			before, _ := os.ReadFile(target)
			link := filepath.Join(filepath.Dir(p), "metadata.opf")
			if err := os.Symlink(target, link); err != nil {
				t.Skip("symlinks unavailable")
			}
			r := env.s.ApplyReorganize(ctx, []int64{fileID})
			if r[0].Sidecar != ReorgSidecarForeign {
				t.Fatalf("sidecar = %q, want foreign", r[0].Sidecar)
			}
			if fi, err := os.Lstat(link); err != nil || fi.Mode()&os.ModeSymlink == 0 {
				t.Fatalf("the symlink was replaced: %v", err)
			}
			after, _ := os.ReadFile(target)
			if string(after) != string(before) {
				t.Fatal("the link target was written through")
			}
		})
	}
}

func TestReorganizeOPF_SymlinkedBookFolderIsNotWrittenThrough(t *testing.T) {
	env, libraryDir, _, ctx := reorgFixture(t)
	setOPF(t, env, "true")
	book := env.seed(t, ctx, "Jane Doe", "My Book")
	real := t.TempDir()
	linkDir := filepath.Join(libraryDir, "Jane Doe", "My Book (2020)")
	if err := os.MkdirAll(filepath.Dir(linkDir), 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(real, linkDir); err != nil {
		t.Skip("symlinks unavailable")
	}
	p := filepath.Join(linkDir, "My Book - Jane Doe.epub")
	writeFileAt(t, p)
	if err := env.books.AddBookFile(ctx, book.ID, models.MediaTypeEbook, p); err != nil {
		t.Fatal(err)
	}
	fs, _ := env.books.ListBookFiles(ctx, book.ID)
	moves, _ := env.s.PreviewReorganizeBook(ctx, book.ID)
	if moves[0].Status != ReorgStatusNoop || moves[0].Sidecar != ReorgSidecarForeign {
		t.Fatalf("preview = %+v, want noop and skipped", moves[0])
	}
	r := env.s.ApplyReorganize(ctx, []int64{fs[0].ID})
	if r[0].Sidecar != ReorgSidecarForeign {
		t.Fatalf("apply = %+v", r[0])
	}
	if _, err := os.Stat(filepath.Join(real, "metadata.opf")); err == nil {
		t.Fatal("sidecar written through a symlinked folder")
	}
}

func TestReorganizeOPF_SymlinkedLibraryRootStillWorks(t *testing.T) {
	env, libraryDir, _, ctx := reorgFixture(t)
	setOPF(t, env, "true")
	// The library root itself being a link is normal and must not block.
	linked := filepath.Join(t.TempDir(), "lib")
	if err := os.Symlink(libraryDir, linked); err != nil {
		t.Skip("symlinks unavailable")
	}
	env.s.libraryDir = linked
	book := env.seed(t, ctx, "Jane Doe", "My Book")
	p := filepath.Join(linked, "Jane Doe", "My Book (2020)", "My Book - Jane Doe.epub")
	writeFileAt(t, p)
	if err := env.books.AddBookFile(ctx, book.ID, models.MediaTypeEbook, p); err != nil {
		t.Fatal(err)
	}
	fs, _ := env.books.ListBookFiles(ctx, book.ID)
	r := env.s.ApplyReorganize(ctx, []int64{fs[0].ID})
	if r[0].Status != ReorgStatusNoop || r[0].Sidecar != ReorgSidecarWritten {
		t.Fatalf("apply = %+v", r[0])
	}
}

// Two books moving into one flat folder: neither gets a sidecar, and the
// preview says so before anything is applied.
func TestReorganizeOPF_PlannedMovesIntoOneFolderAreShared(t *testing.T) {
	env, libraryDir, _, ctx := reorgFixture(t)
	setOPF(t, env, "true")
	a := env.seedAuthor(t, ctx, "Jane Doe")
	if err := env.s.settings.Set(ctx, "naming.bookTemplate", "{Author}/{Title}.{ext}"); err != nil {
		t.Fatal(err)
	}
	var ids []int64
	for _, title := range []string{"Alpha", "Beta"} {
		b := env.seedBook(t, ctx, a, title)
		p := filepath.Join(libraryDir, "misc", title, "x.epub")
		writeFileAt(t, p)
		if err := env.books.AddBookFile(ctx, b.ID, models.MediaTypeEbook, p); err != nil {
			t.Fatal(err)
		}
		fs, _ := env.books.ListBookFiles(ctx, b.ID)
		ids = append(ids, fs[0].ID)
	}
	moves, err := env.s.PreviewReorganizeLibrary(ctx)
	if err != nil {
		t.Fatal(err)
	}
	for _, m := range moves {
		if m.Status != ReorgStatusMove || m.Sidecar != ReorgSidecarShared {
			t.Fatalf("preview = %+v, want move and shared", m)
		}
	}
	for _, r := range env.s.ApplyReorganize(ctx, ids) {
		if r.Status != ReorgStatusMoved || r.Sidecar != ReorgSidecarShared {
			t.Fatalf("apply = %+v, want moved and shared", r)
		}
	}
	if _, err := os.Stat(filepath.Join(libraryDir, "Jane Doe", "metadata.opf")); err == nil {
		t.Fatal("a sidecar was written into the shared folder")
	}
}

// The folder check must be one query per run, not one per file: a per-file
// scan of book_files was quadratic (4.5 s at 3,000 books, 16 s at 6,000).
func TestReorganizeOPF_PreviewScalesLinearly(t *testing.T) {
	if testing.Short() {
		t.Skip("seeds thousands of books")
	}
	n := 3000
	if v, err := strconv.Atoi(os.Getenv("BINDERY_OPF_PERF_N")); err == nil && v > 0 {
		n = v
	}
	env, libraryDir, _, ctx := reorgFixture(t)
	setOPF(t, env, "true")
	a := env.seedAuthor(t, ctx, "Jane Doe")
	for i := 0; i < n; i++ {
		title := "Book " + strconv.Itoa(i)
		b := env.seedBook(t, ctx, a, title)
		p := filepath.Join(libraryDir, "Jane Doe", title+" (2020)", title+" - Jane Doe.epub")
		writeFileAt(t, p)
		if err := env.books.AddBookFile(ctx, b.ID, models.MediaTypeEbook, p); err != nil {
			t.Fatal(err)
		}
	}
	// The same preview with the setting off is the baseline: the sidecar
	// work may cost a constant factor, not a factor that grows with the library.
	setOPF(t, env, "false")
	start := time.Now()
	if _, err := env.s.PreviewReorganizeLibrary(ctx); err != nil {
		t.Fatal(err)
	}
	base := time.Since(start)
	setOPF(t, env, "true")
	start = time.Now()
	moves, err := env.s.PreviewReorganizeLibrary(ctx)
	elapsed := time.Since(start)
	if err != nil || len(moves) != n {
		t.Fatalf("moves = %d, err %v", len(moves), err)
	}
	t.Logf("preview of %d books: %v with sidecars, %v without", n, elapsed, base)
	if elapsed > 3*base+time.Second {
		t.Fatalf("preview of %d books took %v against %v without sidecars", n, elapsed, base)
	}
}

// Applying one of two books headed for the same folder must decide like the
// preview does: neither ends up with a sidecar.
func TestReorganizeOPF_SubsetApplyAgreesWithPreview(t *testing.T) {
	env, libraryDir, _, ctx := reorgFixture(t)
	setOPF(t, env, "true")
	a := env.seedAuthor(t, ctx, "Jane Doe")
	if err := env.s.settings.Set(ctx, "naming.bookTemplate", "{Author}/{Title}.{ext}"); err != nil {
		t.Fatal(err)
	}
	var ids []int64
	var bookIDs []int64
	for _, title := range []string{"Alpha", "Beta"} {
		b := env.seedBook(t, ctx, a, title)
		p := filepath.Join(libraryDir, "misc", title, "x.epub")
		writeFileAt(t, p)
		if err := env.books.AddBookFile(ctx, b.ID, models.MediaTypeEbook, p); err != nil {
			t.Fatal(err)
		}
		fs, _ := env.books.ListBookFiles(ctx, b.ID)
		ids = append(ids, fs[0].ID)
		bookIDs = append(bookIDs, b.ID)
	}
	// A preview scoped to one book already says shared.
	one, _ := env.s.PreviewReorganizeBook(ctx, bookIDs[0])
	if one[0].Sidecar != ReorgSidecarShared {
		t.Fatalf("single book preview = %+v, want shared", one[0])
	}
	for i, id := range ids {
		r := env.s.ApplyReorganize(ctx, []int64{id})
		if r[0].Status != ReorgStatusMoved || r[0].Sidecar != ReorgSidecarShared {
			t.Fatalf("apply %d = %+v, want moved and shared", i, r[0])
		}
		if _, err := os.Stat(filepath.Join(libraryDir, "Jane Doe", "metadata.opf")); err == nil {
			t.Fatalf("sidecar present after apply %d", i)
		}
	}
}

func TestReorganizeOPF_SharedFolderRemovesOnlyBinderyFile(t *testing.T) {
	for _, tc := range []struct {
		name    string
		content string
		removed bool
	}{
		{"bindery marker", "<package>" + opfGeneratorMeta + "</package>", true},
		{"foreign", "<package>calibre</package>", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			env, libraryDir, _, ctx := reorgFixture(t)
			setOPF(t, env, "true")
			dir, ids := flatBooks(t, env, libraryDir)
			opf := filepath.Join(dir, "metadata.opf")
			if err := os.WriteFile(opf, []byte(tc.content), 0o644); err != nil {
				t.Fatal(err)
			}
			env.s.ApplyReorganize(ctx, ids)
			_, err := os.Stat(opf)
			if tc.removed && err == nil {
				t.Fatal("stale Bindery sidecar was left in a shared folder")
			}
			if !tc.removed {
				if b, _ := os.ReadFile(opf); string(b) != tc.content {
					t.Fatal("foreign sidecar was touched")
				}
			}
		})
	}
}

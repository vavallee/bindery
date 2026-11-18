package importer

import (
	"os"
	"path/filepath"
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

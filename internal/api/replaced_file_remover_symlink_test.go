package api

import (
	"os"
	"path/filepath"
	"testing"
)

func TestUpgradeGate_Remover_SymlinkToDir(t *testing.T) {
	root := t.TempDir()
	outside := t.TempDir()
	books, one, _, ctx := replacedRemoverFixture(t)
	remove := NewReplacedFileRemover(NewLibraryRoots(staticRoots{root}), books)
	exists := func(p string) bool { _, err := os.Lstat(p); return err == nil }

	// symlink named like an ebook, pointing at a directory inside the root
	dir := filepath.Join(root, "real")
	writeFile(t, filepath.Join(dir, "new.epub"))
	link := filepath.Join(root, "x", "Book.mobi")
	_ = os.MkdirAll(filepath.Dir(link), 0o755)
	if err := os.Symlink(dir, link); err != nil {
		t.Fatal(err)
	}
	skipped, err := remove(ctx, one.ID, link)
	t.Logf("in-root dir symlink: skipped=%v err=%v new.epub=%v link=%v", skipped, err, exists(filepath.Join(dir, "new.epub")), exists(link))
	if !exists(filepath.Join(dir, "new.epub")) {
		t.Error("DIRECTORY CONTENTS DELETED THROUGH SYMLINK (in root)")
	}

	// symlink named like an ebook, pointing at a directory outside the root
	odir := filepath.Join(outside, "dir")
	writeFile(t, filepath.Join(odir, "precious.pdf"))
	link2 := filepath.Join(root, "y", "Book.mobi")
	_ = os.MkdirAll(filepath.Dir(link2), 0o755)
	if err := os.Symlink(odir, link2); err != nil {
		t.Fatal(err)
	}
	skipped, err = remove(ctx, one.ID, link2)
	t.Logf("outside dir symlink: skipped=%v err=%v precious=%v link=%v", skipped, err, exists(filepath.Join(odir, "precious.pdf")), exists(link2))
	if !exists(filepath.Join(odir, "precious.pdf")) {
		t.Error("OUTSIDE DIRECTORY CONTENTS DELETED THROUGH SYMLINK")
	}
}

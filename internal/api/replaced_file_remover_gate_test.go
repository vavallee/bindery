package api

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/vavallee/bindery/internal/models"
)

func TestEbookUpgradeGate_Remover(t *testing.T) {
	root := t.TempDir()
	outside := t.TempDir()
	books, one, two, ctx := replacedRemoverFixture(t)
	remove := NewReplacedFileRemover(NewLibraryRoots(staticRoots{root}), books)
	exists := func(p string) bool { _, err := os.Lstat(p); return err == nil }

	t.Run("exact file of another book", func(t *testing.T) {
		p := filepath.Join(root, "b", "Other.mobi")
		writeFile(t, p)
		if err := books.AddBookFile(ctx, two.ID, models.MediaTypeEbook, p); err != nil {
			t.Fatal(err)
		}
		skipped, err := remove(ctx, one.ID, p)
		if err != nil || !skipped || !exists(p) {
			t.Fatalf("skipped=%v err=%v exists=%v", skipped, err, exists(p))
		}
	})
	t.Run("symlink in root to outside file", func(t *testing.T) {
		target := filepath.Join(outside, "T.mobi")
		writeFile(t, target)
		link := filepath.Join(root, "c", "L.mobi")
		_ = os.MkdirAll(filepath.Dir(link), 0o755)
		if err := os.Symlink(target, link); err != nil {
			t.Fatal(err)
		}
		skipped, err := remove(ctx, one.ID, link)
		t.Logf("skipped=%v err=%v target=%v link=%v", skipped, err, exists(target), exists(link))
		if !exists(target) {
			t.Fatal("symlink target outside roots deleted")
		}
	})
	t.Run("symlinked dir in root to outside", func(t *testing.T) {
		target := filepath.Join(outside, "D", "T.mobi")
		writeFile(t, target)
		link := filepath.Join(root, "d")
		if err := os.Symlink(filepath.Dir(target), link); err != nil {
			t.Fatal(err)
		}
		skipped, err := remove(ctx, one.ID, filepath.Join(link, "T.mobi"))
		if !exists(target) {
			t.Fatalf("outside file deleted through symlinked dir: skipped=%v err=%v", skipped, err)
		}
	})
	t.Run("symlink in root to tracked file of other book", func(t *testing.T) {
		target := filepath.Join(root, "e", "Real.mobi")
		writeFile(t, target)
		if err := books.AddBookFile(ctx, two.ID, models.MediaTypeEbook, target); err != nil {
			t.Fatal(err)
		}
		link := filepath.Join(root, "e", "L.mobi")
		if err := os.Symlink(target, link); err != nil {
			t.Fatal(err)
		}
		skipped, err := remove(ctx, one.ID, link)
		t.Logf("skipped=%v err=%v target=%v link=%v", skipped, err, exists(target), exists(link))
		if !exists(target) {
			t.Fatal("other book's target deleted")
		}
	})
	t.Run("directory path named like a file", func(t *testing.T) {
		dir := filepath.Join(root, "f", "Book.mobi")
		writeFile(t, filepath.Join(dir, "new.epub"))
		writeFile(t, filepath.Join(dir, "keep.pdf"))
		skipped, err := remove(ctx, one.ID, dir)
		if err != nil || !skipped || !exists(filepath.Join(dir, "new.epub")) || !exists(filepath.Join(dir, "keep.pdf")) {
			t.Fatalf("a directory was not refused: skipped=%v err=%v", skipped, err)
		}
	})
	t.Run("relative and dotdot", func(t *testing.T) {
		p := filepath.Join(root, "g", "x.mobi")
		writeFile(t, p)
		skipped, _ := remove(ctx, one.ID, filepath.Join(root, "g", "..", "..", filepath.Base(outside), "x.mobi"))
		t.Logf("dotdot skipped=%v", skipped)
	})
}

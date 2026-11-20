//go:build !windows

package importer

import (
	"os"
	"path/filepath"
	"testing"
)

// A directory symlink inside a download must not let the client's file list
// reach a book outside it (the last-component Lstat alone passes it).
func TestDiscoverBookFiles_DirectorySymlinkOutsideDownloadRefused(t *testing.T) {
	outside := t.TempDir()
	secret := filepath.Join(outside, "stolen.epub")
	if err := os.WriteFile(secret, bookSized("OUTSIDE"), 0o644); err != nil {
		t.Fatal(err)
	}

	dl := t.TempDir()
	nested := filepath.Join(dl, "Release", "CD1")
	if err := os.MkdirAll(nested, 0o755); err != nil {
		t.Fatal(err)
	}
	good := filepath.Join(nested, "book.epub")
	if err := os.WriteFile(good, bookSized("INSIDE"), 0o644); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(dl, "Release", "link")
	symlinkOrSkip(t, outside, link)
	escaped := filepath.Join(link, "stolen.epub")

	got := discoverBookFiles(t.Context(), dl, []string{good, escaped})
	if len(got) != 1 || got[0] != good {
		t.Fatalf("got %v, want only the nested real file %s", got, good)
	}
	if importSourcePresent(dl, []string{escaped}) {
		t.Fatal("retry guard counted the escaped file as present")
	}
	if !importSourcePresent(dl, []string{good}) {
		t.Fatal("retry guard dropped a normal nested file")
	}
}

// A download reached through the operator's own link, and a link that stays
// inside the download, both keep importing.
func TestDiscoverBookFiles_InternalAndRootSymlinksStillImport(t *testing.T) {
	real := t.TempDir()
	if err := os.MkdirAll(filepath.Join(real, "a"), 0o755); err != nil {
		t.Fatal(err)
	}
	book := filepath.Join(real, "a", "book.epub")
	if err := os.WriteFile(book, bookSized("B"), 0o644); err != nil {
		t.Fatal(err)
	}
	holder := t.TempDir()
	rootLink := filepath.Join(holder, "downloads")
	symlinkOrSkip(t, real, rootLink)
	inner := filepath.Join(real, "alias")
	symlinkOrSkip(t, filepath.Join(real, "a"), inner)

	viaRoot := filepath.Join(rootLink, "a", "book.epub")
	viaInner := filepath.Join(rootLink, "alias", "book.epub")
	got := discoverBookFiles(t.Context(), rootLink, []string{viaRoot, viaInner})
	if len(got) != 2 {
		t.Fatalf("got %v, want both paths kept", got)
	}
}

// The copy itself refuses a symlink source and a non-regular source, whatever
// the caller checked first.
func TestCopyFile_RefusesSymlinkSource(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "target.epub")
	if err := os.WriteFile(target, bookSized("T"), 0o644); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(dir, "link.epub")
	symlinkOrSkip(t, target, link)
	dst := filepath.Join(dir, "out", "copy.epub")

	if err := CopyFileCtx(t.Context(), link, dst); err == nil {
		t.Fatal("copy followed a symlink source")
	}
	if _, err := os.Stat(dst); err == nil {
		t.Fatal("a destination was created from a symlink source")
	}
	if err := CopyFileCtx(t.Context(), dir, filepath.Join(dir, "out", "d")); err == nil {
		t.Fatal("copy accepted a directory as its source")
	}
	if err := CopyFileCtx(t.Context(), target, dst); err != nil {
		t.Fatalf("normal file copy failed: %v", err)
	}
}
